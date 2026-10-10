#!/usr/bin/env bash
# Builds polaroidd and polaroid into $BIN (default bin) and records, in
# $BIN/.build-state, the source state they were built from: the commit, every
# uncommitted change (tracked and untracked), and the Go toolchain and
# environment. With -if-stale it builds only when that record is missing or
# differs from the current state, so a composed run (make ci) builds once per
# source state and never runs binaries built from another one. Outside a Git
# checkout the state is unknown, nothing is recorded, and it always builds.
#
# Usage: scripts/build.sh [-if-stale]   (GO and BIN may be set by make)
set -euo pipefail
cd "$(dirname "$0")/.."
GO=${GO:-go}
BIN=${BIN:-bin}

state() {
	git rev-parse --is-inside-work-tree >/dev/null 2>&1 || return 1
	{
		"$GO" version
		"$GO" env GOOS GOARCH GOFLAGS CGO_ENABLED GOEXPERIMENT GOTOOLCHAIN
		git rev-parse HEAD
		git status --porcelain=v1 --untracked-files=all
		git diff HEAD --binary
		git ls-files -z -o --exclude-standard | while IFS= read -r -d '' f; do
			printf '%s\n' "$f"
			if [[ -f "$f" ]]; then shasum -a 256 <"$f"; fi
		done
	} | shasum -a 256 | cut -d' ' -f1
}

current="$(state)" || current=""
if [[ ${1:-} == -if-stale && -n "$current" && -x "$BIN/polaroidd" && -x "$BIN/polaroid" &&
	"$(cat "$BIN/.build-state" 2>/dev/null)" == "$current" ]]; then
	echo "build: $BIN/ is current (built from source state ${current:0:12})"
	exit 0
fi
mkdir -p "$BIN"
rm -f "$BIN/.build-state"
echo "$GO build -trimpath -o $BIN/ ./cmd/polaroidd ./cmd/polaroid"
"$GO" build -trimpath -o "$BIN/" ./cmd/polaroidd ./cmd/polaroid
# Recorded only if nothing changed while building.
if [[ -n "$current" && "$(state)" == "$current" ]]; then
	printf '%s\n' "$current" >"$BIN/.build-state"
fi
