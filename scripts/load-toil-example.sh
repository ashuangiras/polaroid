#!/usr/bin/env bash
# load-toil-example.sh DIR: creates the toil item in DIR/v1.create.json and
# appends DIR/vN.revise.json in order, against the daemon at $POLAROID_URL
# (default http://127.0.0.1:7417), through bin/polaroid (or $TOIL_EXAMPLE_CLI).
#
# The templates name records portably (ADR-0034): sources by
# {"procedure": KEY, "version": N}, executions by {"execution": {procedure,
# version, repository, commit[, occurrence]}} (occurrence counts matches
# oldest first; without it exactly one must match), and a local
# applicability by repository identifier. This script replaces each with the
# catalog's ID, so the history must already be in the catalog: a selector
# that matches nothing or several executions stops the script before any
# write. A stored revision is never compared or rewritten: revisions the
# item already has are reported and skipped.
set -euo pipefail

die() { echo "load-toil-example: $*" >&2; exit 1; }
dir="${1:?usage: scripts/load-toil-example.sh DIR}"
dir="${dir%/}"
cli="${TOIL_EXAMPLE_CLI:-$(cd "$(dirname "$0")/.." && pwd)/bin/polaroid}"
[[ -x "$cli" ]] || die "$cli is not executable; run make build"
[[ -f "$dir/v1.create.json" ]] || die "$dir has no v1.create.json"

procedure_id() {
	"$cli" get-by-key "$1" 2>/dev/null | jq -er .id || die "procedure $1 is not in the catalog"
}

# execution_id SELECTOR prints the ID of the one execution SELECTOR names.
execution_id() {
	local key version repository commit occurrence ids n
	read -r key version repository commit occurrence < <(jq -r '[.procedure, .version, .repository, .commit, (.occurrence // 0)] | @tsv' <<<"$1")
	ids="$("$cli" executions "$(procedure_id "$key")" "$repository" "version=$version" "commit=$commit" | jq -r '.executions[].id')" ||
		die "listing executions for $1 failed"
	n="$(grep -c . <<<"$ids" || true)"
	if ((occurrence == 0)); then
		((n == 1)) || die "$n executions match $1; name an occurrence"
		echo "$ids"
	else
		((occurrence <= n)) || die "only $n executions match $1"
		sed -n "${occurrence}p" <<<"$ids"
	fi
}

# resolve FILE prints the request in FILE with every portable name replaced
# by the catalog's ID.
resolve() {
	local body sel id i n path
	body="$(jq -c . "$1")"
	n="$(jq '.revision.sources | length' <<<"$body")"
	for ((i = 0; i < n; i++)); do
		id="$(procedure_id "$(jq -r ".revision.sources[$i].procedure" <<<"$body")")"
		body="$(jq -c --argjson i "$i" --arg id "$id" '.revision.sources[$i] |= ({procedure_id: $id} + del(.procedure))' <<<"$body")"
	done
	for path in observations validation.executions; do
		n="$(jq "(.revision.$path // []) | length" <<<"$body")"
		for ((i = 0; i < n; i++)); do
			sel="$(jq -c ".revision.$path[$i].execution" <<<"$body")"
			id="$(execution_id "$sel")"
			body="$(jq -c --argjson i "$i" --arg id "$id" ".revision.$path[\$i] |= ({execution_id: \$id} + del(.execution))" <<<"$body")"
		done
	done
	if jq -e '.revision.applicability.repository' <<<"$body" >/dev/null; then
		id="$("$cli" repository-by-identifier "$(jq -r .revision.applicability.repository <<<"$body")" | jq -er .id)" ||
			die "the repository of $1 is not registered"
		body="$(jq -c --arg id "$id" '.revision.applicability.repository = $id' <<<"$body")"
	fi
	printf '%s\n' "$body"
}

key="$(jq -r .canonical_key "$dir/v1.create.json")"
files=("$dir/v1.create.json")
for ((n = 2; ; n++)); do
	[[ -f "$dir/v$n.revise.json" ]] || break
	files+=("$dir/v$n.revise.json")
done
# Resolve everything before the first write.
requests=()
for f in "${files[@]}"; do requests+=("$(resolve "$f")"); done

if item="$("$cli" get-toil-item-by-key "$key" 2>/dev/null)"; then
	echo "load-toil-example: $key exists" >&2
else
	item="$("$cli" create-toil-item <<<"${requests[0]}")" || die "create $key failed: $item"
	echo "load-toil-example: $key: created revision 1" >&2
fi
id="$(jq -r .id <<<"$item")"
latest="$(jq -r .latest_revision <<<"$item")"
for ((i = 1; i < ${#files[@]}; i++)); do
	n=$((i + 1))
	if ((n <= latest)); then
		echo "load-toil-example: $key already has revision $n; not compared" >&2
		continue
	fi
	base="$(jq -r .base_revision <<<"${requests[i]}")"
	((base == latest)) || die "${files[i]} has base_revision $base, but $key's latest revision is $latest"
	out="$("$cli" revise-toil-item "$id" <<<"$(jq -c 'del(.canonical_key)' <<<"${requests[i]}")")" || die "revise $key failed: $out"
	latest=$n
	echo "load-toil-example: $key: appended revision $n" >&2
done
"$cli" get-toil-item "$id" | jq -c '{id, canonical_key, latest_revision, status, scope}'
