#!/usr/bin/env bash
# Verifies docs/development/dependencies.md against the build:
#   1. it lists exactly the third-party modules compiled into Polaroid's
#      packages and tests on the supported platforms, at the versions in use;
#   2. each module ships a license file that reads as the listed license, and
#      that license is on the permissive allowlist.
set -euo pipefail

cd "$(dirname "$0")/.."
inventory="${DEPS_INVENTORY:-docs/development/dependencies.md}"
platforms="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
allowlist=" MIT BSD-2-Clause BSD-3-Clause Apache-2.0 ISC "

# classify prints the SPDX expression recognised in a license file, or
# "unknown". A file holding both the Apache-2.0 and MIT texts (a project
# relicensing, such as the MCP Go SDK) is "Apache-2.0 AND MIT".
classify() {
	local text
	text="$(tr -s '[:space:]' ' ' <"$1")"
	case "$text" in
	*"Apache License"*"Version 2.0"*"Permission is hereby granted, free of charge"*) echo "Apache-2.0 AND MIT" ;;
	*"Permission is hereby granted, free of charge"*) echo MIT ;;
	*"Apache License"*"Version 2.0"*) echo Apache-2.0 ;;
	*"Redistribution and use in source and binary forms"*"Neither the name"*) echo BSD-3-Clause ;;
	*"Redistribution and use in source and binary forms"*) echo BSD-2-Clause ;;
	*"Permission to use, copy, modify, and/or distribute this software for any purpose"*) echo ISC ;;
	*) echo unknown ;;
	esac
}

# Canary: the classifier must recognise known texts across line breaks and
# reject an unrecognised one, or every result below is meaningless.
canary="$(mktemp -d)"
trap 'rm -rf "$canary"' EXIT
printf 'Permission is hereby granted,\n  free of charge, to any person\n' >"$canary/mit"
printf 'Redistribution and use in source and binary\nforms ... 3. Neither the name of\n' >"$canary/bsd3"
printf 'All rights reserved.\n' >"$canary/none"
printf 'Apache License\n Version 2.0, January 2004 ... MIT License ... Permission is hereby granted,\nfree of charge\n' >"$canary/both"
if [[ "$(classify "$canary/mit")" != MIT || "$(classify "$canary/bsd3")" != BSD-3-Clause || "$(classify "$canary/none")" != unknown ||
	"$(classify "$canary/both")" != "Apache-2.0 AND MIT" ]]; then
	echo "deps-check: license classifier canary failed" >&2
	exit 1
fi

used=""
for platform in $platforms; do
	mods="$(GOOS="${platform%/*}" GOARCH="${platform#*/}" CGO_ENABLED=0 go list -deps -test \
		-f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}{{end}}{{end}}' ./...)" ||
		{ echo "deps-check: go list failed for $platform" >&2; exit 1; }
	used+=$'\n'"$mods"
done
used="$(printf '%s\n' "$used" | sed '/^$/d' | sort -u)"
# Canary: the SQLite driver is compiled, so an empty or partial list is a bug.
grep -q '^modernc.org/sqlite ' <<<"$used" || { echo "deps-check: module list is implausible: [$used]" >&2; exit 1; }

documented="$(sed -n 's/^| `\([^`]*\)` | `\([^`]*\)` | \([A-Za-z0-9.][A-Za-z0-9. -]*[A-Za-z0-9]\) |.*$/\1 \2 \3/p' "$inventory" | sort -u)"

failures=0
problem() {
	echo "deps-check: $*" >&2
	failures=$((failures + 1))
}

while read -r mod ver; do
	license="$(awk -v m="$mod" -v v="$ver" '$1 == m && $2 == v { $1 = ""; $2 = ""; sub(/^ +/, ""); print }' <<<"$documented")"
	if [[ -z "$license" ]]; then
		problem "$mod $ver is compiled but not listed in $inventory"
		continue
	fi
	for part in ${license// AND / }; do
		if [[ "$allowlist" != *" $part "* ]]; then
			problem "$mod is listed as $license, and $part is not on the permissive allowlist"
		fi
	done
	go mod download "$mod@$ver"
	dir="$(go list -m -f '{{.Dir}}' "$mod")"
	file=""
	for name in LICENSE LICENSE.md LICENSE.txt COPYING; do
		if [[ -f "$dir/$name" ]]; then
			file="$dir/$name"
			break
		fi
	done
	if [[ -z "$file" ]]; then
		problem "$mod $ver has no license file in $dir"
		continue
	fi
	found="$(classify "$file")"
	[[ "$found" == "$license" ]] || problem "$mod $ver is listed as $license but $file reads as $found"
done <<<"$used"

while read -r mod ver _; do
	grep -qxF "$mod $ver" <<<"$used" || problem "$inventory lists $mod $ver, which is not compiled"
done <<<"$documented"

if [[ $failures -gt 0 ]]; then
	echo "deps-check: FAIL ($failures problems)" >&2
	exit 1
fi
echo "deps-check: PASS ($(wc -l <<<"$used" | tr -d ' ') modules listed, licenses verified, platforms: $platforms)"
