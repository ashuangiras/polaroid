#!/usr/bin/env bash
# Loads procedure and binding fixtures into a running polaroidd through the
# CLI (ADR-0017). A fixture is an API request body, except that the
# procedure_id of a reference or a binding may hold a canonical key, which is
# replaced with the ID of that procedure in the target store, and a
# repository ID (version.applicability.repository, origin.repository_id) may
# hold a repository identifier, which is replaced with the ID it is
# registered to (ADR-0019, ADR-0020).
#
#   DIR/repositories/*.json                                     register requests, plus optional "aliases"
#   DIR/procedures/<name>/v1.create.json, v2.revise.json, ...   one procedure
#   DIR/procedures/<name>/origin.json                           its origin, recorded once
#   DIR/bindings/*.json                                         create-binding requests
#
# Repositories are reused by canonical identifier if their name matches, and
# missing aliases are added. Procedures are reused by canonical key: every
# stored version must equal the fixture's, and missing versions are appended
# with base_version set to the stored latest; a missing origin is recorded.
# Bindings are reused by repository and name if they bind the same procedure
# with the same first revision. Nothing is ever overwritten, so a second run
# changes nothing. The loader knows record shapes, not tasks.
#
# Usage: scripts/load-fixtures.sh [-n MAX_VERSION] DIR
#   -n N loads each procedure's versions up to N only.
#   The server is $POLAROID_URL, else the CLI's default. Needs jq and
#   bin/polaroid (make build). Progress goes to stderr; stdout gets
#   {"repositories": {IDENTIFIER: ID, ...}, "procedures": {KEY: ID, ...},
#    "bindings": [{repository, name, id}, ...]}.
set -euo pipefail

cli="$(cd "$(dirname "$0")/.." && pwd)/bin/polaroid"
die() {
	echo "load-fixtures: $*" >&2
	exit 1
}
log() { echo "load-fixtures: $*" >&2; }
usage() {
	echo "usage: scripts/load-fixtures.sh [-n MAX_VERSION] DIR" >&2
	exit 2
}

max=0
while getopts n: opt; do
	case $opt in
	n) [[ "$OPTARG" =~ ^[1-9][0-9]*$ ]] || usage; max=$OPTARG ;;
	*) usage ;;
	esac
done
shift $((OPTIND - 1))
[[ $# -eq 1 && -d "$1" ]] || usage
dir=$1
command -v jq >/dev/null 2>&1 || die "jq is required"
[[ -x "$cli" ]] || die "run 'make build' first"

uuid='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'

# history KEY prints the stored procedure with canonical key KEY, or nothing.
history() {
	local out rc=0
	out="$("$cli" get-by-key "$1" 2>/dev/null)" || rc=$?
	if ((rc == 0)); then
		printf '%s\n' "$out"
	elif [[ "$(jq -r '.error.code // empty' <<<"$out" 2>/dev/null)" != not_found ]]; then
		die "get-by-key $1 failed: $out"
	fi
}

# id_of KEY_OR_ID prints a procedure ID, or nothing if KEY names no procedure.
id_of() {
	if [[ "$1" =~ $uuid ]]; then
		echo "$1"
	else
		history "$1" | jq -r '.id // empty'
	fi
}

# substitute FILE prints FILE with every canonical key in a reference's
# procedure_id replaced by its ID, and a repository identifier in
# version.applicability.repository replaced by the repository's ID. Every key
# and identifier must exist.
substitute() {
	local ids='{}' key repo
	for key in $(jq -r '.version.references[]?.procedure_id' "$1"); do
		ids="$(jq -c --arg k "$key" --arg id "$(id_of "$key")" '. + {($k): $id}' <<<"$ids")"
	done
	repo="$(jq -r '.version.applicability.repository // empty' "$1")"
	[[ -z "$repo" ]] || repo="$(repository_id "$repo")"
	jq -c --argjson ids "$ids" --arg repo "$repo" '(.version.references[]?.procedure_id) |= $ids[.]
		| if $repo != "" then .version.applicability.repository = $repo else . end' "$1"
}

# repository_id IDENTIFIER_OR_ID prints the ID of the registered repository.
repository_id() {
	local out
	if [[ "$1" =~ $uuid ]]; then
		echo "$1"
		return
	fi
	out="$("$cli" repository-by-identifier "$1")" || die "repository $1 is not registered: $out"
	jq -r .id <<<"$out"
}

# missing FILE prints the referenced keys that name no stored procedure.
missing() {
	local key
	for key in $(jq -r '.version.references[]?.procedure_id' "$1"); do
		[[ -n "$(id_of "$key")" ]] || echo "$key"
	done
}

comparable='{philosophy, method, goal: (.goal // null), applicability: (.applicability // null), contract, instructions, references: (.references // []), revision_reason}'

# load_procedure DIR loads one procedure. It sets deferred=1 and changes
# nothing if a version to load references a procedure that is not stored yet.
load_procedure() {
	local pdir=${1%/} files=() n=2 limit i key hist id latest want got base
	deferred=0
	files=("$pdir/v1.create.json")
	[[ -f "${files[0]}" ]] || die "$pdir has no v1.create.json"
	while [[ -f "$pdir/v$n.revise.json" ]]; do
		files+=("$pdir/v$n.revise.json")
		n=$((n + 1))
	done
	limit=${#files[@]}
	((max > 0 && max < limit)) && limit=$max
	for ((i = 0; i < limit; i++)); do
		if [[ -n "$(missing "${files[i]}")" ]]; then
			deferred=1
			return 0
		fi
	done

	key="$(jq -r .canonical_key "${files[0]}")"
	hist="$(history "$key")"
	if [[ -z "$hist" ]]; then
		hist="$(substitute "${files[0]}" | "$cli" create)" || die "create $key failed: $hist"
		log "$key: created version 1"
	fi
	id="$(jq -r .id <<<"$hist")"
	latest="$(jq -r .latest_version <<<"$hist")"
	for ((i = 0; i < limit; i++)); do
		n=$((i + 1))
		want="$(substitute "${files[i]}" | jq -S ".version | $comparable")"
		if ((n <= latest)); then
			got="$("$cli" get-version "$id" "$n" | jq -S "$comparable")" || die "get-version $key $n failed"
			[[ "$got" == "$want" ]] || die "$key version $n in the store differs from ${files[i]}; versions are never overwritten"
			continue
		fi
		base="$(jq -r .base_version "${files[i]}")"
		[[ "$base" == "$latest" ]] || die "${files[i]} has base_version $base, but the stored latest version of $key is $latest"
		got="$(substitute "${files[i]}" | "$cli" revise "$id")" || die "revise $key failed: $got"
		latest=$n
		log "$key: appended version $n"
	done
	if ((latest > limit)); then
		log "$key: the store also has versions $((limit + 1))..$latest beyond the loaded fixtures"
	fi
	if [[ -f "$pdir/origin.json" ]]; then
		want="$(jq -c --arg r "$(repository_id "$(jq -r .repository_id "$pdir/origin.json")")" '.repository_id = $r' "$pdir/origin.json")"
		got="$("$cli" get "$id" | jq -c '.origin // empty | {repository_id, reason}')"
		if [[ -z "$got" ]]; then
			got="$("$cli" origin "$id" <<<"$want")" || die "origin of $key failed: $got"
			log "$key: recorded its origin"
		elif [[ "$(jq -S . <<<"$got")" != "$(jq -S . <<<"$want")" ]]; then
			die "$key has another origin in the store than $pdir/origin.json; origins are never overwritten"
		fi
	fi
	log "$key: $id, versions 1..$limit match the fixtures"
}

# load_repository FILE registers a repository fixture, or reuses it, and
# adds its missing aliases.
load_repository() {
	local identifier name out id alias reason
	identifier="$(jq -r .identifier "$1")"
	name="$(jq -r .name "$1")"
	if out="$("$cli" repository-by-identifier "$identifier" 2>/dev/null)"; then
		[[ "$(jq -r '.identifier + "\n" + .name' <<<"$out")" == "$identifier"$'\n'"$name" ]] ||
			die "$identifier is registered differently from $1; repositories are never overwritten"
		log "$identifier: reused repository $(jq -r .id <<<"$out")"
	elif [[ "$(jq -r '.error.code // empty' <<<"$out" 2>/dev/null)" == not_found ]]; then
		out="$(jq -c '{identifier, name}' "$1" | "$cli" register)" || die "register $identifier failed: $out"
		log "$identifier: registered repository $(jq -r .id <<<"$out")"
	else
		die "repository-by-identifier $identifier failed: $out"
	fi
	id="$(jq -r .id <<<"$out")"
	while IFS=$'\t' read -r alias reason; do
		[[ -n "$alias" ]] || continue
		if jq -e --arg a "$alias" 'any(.aliases[]; .identifier == $a)' <<<"$out" >/dev/null; then
			continue
		fi
		out="$(jq -cn --arg i "$alias" --arg r "$reason" '{identifier: $i, reason: $r}' | "$cli" alias "$id")" || die "alias $alias failed: $out"
		log "$identifier: added alias $alias"
	done < <(jq -r '.aliases[]? | [.identifier, .reason] | @tsv' "$1")
	repositories="$(jq -c --arg i "$identifier" --arg id "$id" '. + {($i): $id}' <<<"$repositories")"
}

repositories='{}'
for f in "$dir"/repositories/*.json; do
	[[ -f "$f" ]] || continue
	load_repository "$f"
done

pending=("$dir"/procedures/*/)
[[ -d "${pending[0]}" ]] || die "no procedures in $dir/procedures"
while ((${#pending[@]} > 0)); do
	next=()
	for p in "${pending[@]}"; do
		load_procedure "$p"
		if ((deferred)); then next+=("$p"); fi
	done
	((${#next[@]} < ${#pending[@]})) || die "unresolved references in: ${next[*]}"
	pending=(${next[@]+"${next[@]}"})
done

procedures='{}'
for p in "$dir"/procedures/*/; do
	key="$(jq -r .canonical_key "$p/v1.create.json")"
	procedures="$(jq -c --arg k "$key" --arg id "$(id_of "$key")" '. + {($k): $id}' <<<"$procedures")"
done

bindings='[]'
for f in "$dir"/bindings/*.json; do
	[[ -f "$f" ]] || continue
	repository="$(jq -r .repository "$f")"
	name="$(jq -r .name "$f")"
	procedure="$(id_of "$(jq -r .procedure_id "$f")")"
	[[ -n "$procedure" ]] || die "$f binds a procedure that is not stored"
	listed="$("$cli" bindings "$repository")" || die "bindings $repository failed: $listed"
	existing="$(jq -c --arg n "$name" '.bindings[] | select(.name == $n)' <<<"$listed")"
	if [[ -n "$existing" ]]; then
		id="$(jq -r .id <<<"$existing")"
		[[ "$(jq -r .procedure_id <<<"$existing")" == "$procedure" ]] || die "$repository $name binds another procedure"
		got="$("$cli" get-binding-revision "$id" 1 | jq -S '{inputs, version_policy, revision_reason}')"
		[[ "$got" == "$(jq -S '.revision' "$f")" ]] || die "$repository $name revision 1 differs from $f; revisions are never overwritten"
		log "$repository $name: reused binding $id"
	else
		created="$(jq -c --arg id "$procedure" '.procedure_id = $id' "$f" | "$cli" bind)" || die "bind $repository $name failed: $created"
		id="$(jq -r .id <<<"$created")"
		log "$repository $name: created binding $id"
	fi
	bindings="$(jq -c --arg r "$repository" --arg n "$name" --arg id "$id" '. + [{repository: $r, name: $n, id: $id}]' <<<"$bindings")"
done

jq -n --argjson r "$repositories" --argjson p "$procedures" --argjson b "$bindings" '{repositories: $r, procedures: $p, bindings: $b}'
