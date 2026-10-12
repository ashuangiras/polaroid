#!/usr/bin/env bash
# build-artifact.sh: a lightweight, local, gitignored verification artifact
# (ADR-0041) -- not a release. It builds polaroidd and polaroid from the
# exact current commit (HEAD, with a clean working tree) for one or more
# OS/ARCH targets, and writes a manifest beside them in OUTDIR/<commit>/:
# repository, commit, Go toolchain, the build command, and each target's
# executable name and SHA-256. It never claims independent review: the
# manifest's verification.independent_review.status is always "not_reviewed"
# here: that only changes when someone else performs and records one.
#
# An existing OUTDIR/<commit>/ whose manifest names the same targets, Go
# toolchain and build command, and whose checksums still match the files on
# disk, is reused, not rebuilt. Any difference in targets, toolchain, build
# command or a checksum forces a rebuild.
#
# Usage: scripts/build-artifact.sh OUTDIR [TARGET...]   (default: linux/amd64)
# Optional: LOCAL_GATE="<a reference to the commit's verification evidence>"
# is copied into the manifest's verification.local_gate; never set by this
# script itself.
set -euo pipefail
cd "$(dirname "$0")/.."
die() {
	echo "build-artifact: $*" >&2
	exit 1
}
command -v jq >/dev/null 2>&1 || die "jq is required"
[[ $# -ge 1 ]] || die "usage: scripts/build-artifact.sh OUTDIR [TARGET...]"
OUT=$1
shift
TARGETS=("$@")
[[ ${#TARGETS[@]} -gt 0 ]] || TARGETS=(linux/amd64)
PACKAGES="./cmd/polaroidd ./cmd/polaroid"
[[ -z $(git status --porcelain) ]] || die "the working tree has changes; build an artifact from a clean, committed state"
COMMIT=$(git rev-parse HEAD)
REPO=$(git remote get-url origin | sed -E 's#^[a-zA-Z]+://##; s#^git@##; s#:#/#; s#\.git$##' | tr '[:upper:]' '[:lower:]')
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
GOVERSION=$(go env GOVERSION)
DIR="$OUT/$COMMIT"

# setting KEY: one build setting from `go version -m`, read without running
# the binary, so a cross-compiled target is checked too.
setting() { go version -m "$1" | awk -v k="$2" '$1 == "build" && index($2, k "=") == 1 { print substr($2, length(k) + 2) }'; }

reusable() {
	[[ -f "$DIR/manifest.json" ]] || return 1
	jq -e . "$DIR/manifest.json" >/dev/null 2>&1 || return 1
	local want have
	want=$(printf '%s\n' "${TARGETS[@]}" | sort | tr '\n' ' ')
	have=$(jq -r '.assets[] | .os + "/" + .arch' "$DIR/manifest.json" 2>/dev/null | sort | tr '\n' ' ') || return 1
	[[ "$want" == "$have" ]] || return 1
	[[ "$(jq -r '.build.packages' "$DIR/manifest.json" 2>/dev/null)" == "$PACKAGES" ]] || return 1
	[[ "$(jq -r '.go' "$DIR/manifest.json" 2>/dev/null)" == "$GOVERSION" ]] || return 1
	local file want_sum got_sum
	while IFS=$'\t' read -r file want_sum; do
		[[ -n "$file" ]] || continue
		[[ -f "$DIR/$file" ]] || return 1
		got_sum=$(sha256 "$DIR/$file" | cut -d ' ' -f 1)
		[[ "$got_sum" == "$want_sum" ]] || return 1
	done < <(jq -r '.assets[] | .files | to_entries[] | .value.path + "\t" + .value.sha256' "$DIR/manifest.json" 2>/dev/null)
	return 0
}

if reusable; then
	echo "build-artifact: $DIR already holds a matching, checksum-verified artifact for $COMMIT; nothing to build" >&2
	exit 0
fi

rm -rf "$DIR"
mkdir -p "$DIR"
assets='[]'
for target in "${TARGETS[@]}"; do
	os=${target%/*} arch=${target#*/}
	tdir="$DIR/${os}_${arch}"
	mkdir -p "$tdir"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -o "$tdir/" $PACKAGES
	files='{}'
	for bin in polaroidd polaroid; do
		f="$tdir/$bin"
		[[ $(setting "$f" vcs.revision) == "$COMMIT" ]] || die "$target/$bin: vcs.revision is not $COMMIT"
		[[ $(setting "$f" vcs.modified) == false ]] || die "$target/$bin: built from a modified tree"
		for private in "$PWD" "$HOME"; do
			! grep -qaF -- "$private" "$f" || die "$target/$bin: contains the build path $private"
		done
		files=$(jq -c --arg name "$bin" --arg path "${os}_${arch}/$bin" --arg sum "$(sha256 "$f" | cut -d ' ' -f 1)" \
			'. + {($name): {path: $path, sha256: $sum}}' <<<"$files")
	done
	assets=$(jq -c --arg os "$os" --arg arch "$arch" --argjson files "$files" '. + [{os: $os, arch: $arch, files: $files}]' <<<"$assets")
done

jq -n --arg repository "$REPO" --arg commit "$COMMIT" --arg go "$GOVERSION" --arg packages "$PACKAGES" \
	--arg gate "${LOCAL_GATE:-}" --argjson assets "$assets" \
	'{format: 1, repository: $repository, commit: $commit, go: $go,
	  build: {packages: $packages, flags: ["-trimpath"], cgo: false},
	  assets: $assets,
	  verification: {local_gate: (if $gate == "" then null else $gate end), independent_review: {status: "not_reviewed"}}}' \
	>"$DIR/manifest.json"
echo "build-artifact: $COMMIT -> $DIR (targets: ${TARGETS[*]})" >&2
ls -l "$DIR" >&2
