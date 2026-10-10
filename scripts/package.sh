#!/usr/bin/env bash
# package.sh: builds polaroid and polaroidd for every release target from
# this clean checkout, and writes the release assets to OUTDIR (ADR-0028):
#   polaroid_<version>_<os>_<arch>.tar.gz   both binaries, README.md, smoke-test.sh
#   SHA256SUMS                              the SHA-256 of every archive
#   build-manifest.json                     commit, version, Go, targets, assets
# VERSION must be the tag at HEAD (vMAJOR.MINOR.PATCH-PRERELEASE): Go embeds it
# as the main module's version, and the full commit as vcs.revision. The
# release workflow (.github/workflows/release.yml) runs this; it needs Go, git,
# jq, tar and sha256sum or shasum.
#
# Usage: scripts/package.sh VERSION OUTDIR
set -euo pipefail
cd "$(dirname "$0")/.."
die() {
	echo "package: $*" >&2
	exit 1
}
[[ $# == 2 ]] || die "usage: scripts/package.sh VERSION OUTDIR"
VERSION=$1 OUT=$2
TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
[[ $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z.-]+$ ]] || die "$VERSION is not a prerelease version (vX.Y.Z-label); stable releases are not published"
[[ -z $(git status --porcelain) ]] || die "the checkout has changes; package a clean checkout"
COMMIT=$(git rev-parse HEAD)
[[ $(git rev-parse -q --verify "refs/tags/$VERSION^{commit}" || true) == "$COMMIT" ]] || die "tag $VERSION does not point at HEAD ($COMMIT)"
[[ ! -e $OUT ]] || die "$OUT already exists"
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
GOVERSION=$(go env GOVERSION)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

# setting BINARY KEY: one build setting from `go version -m`, read without
# running the binary, so cross-compiled targets are checked too.
setting() { go version -m "$1" | awk -v k="$2" '$1 == "build" && index($2, k "=") == 1 { print substr($2, length(k) + 2) }'; }
main_version() { go version -m "$1" | awk '$1 == "mod" { print $3; exit }'; }

assets='[]'
for target in $TARGETS; do
	os=${target%/*} arch=${target#*/}
	name="polaroid_${VERSION}_${os}_${arch}"
	dir="$WORK/$name"
	mkdir -p "$dir"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -o "$dir/" ./cmd/polaroidd ./cmd/polaroid
	for bin in polaroid polaroidd; do
		f="$dir/$bin"
		[[ $(setting "$f" vcs.revision) == "$COMMIT" ]] || die "$name/$bin: vcs.revision is not $COMMIT"
		[[ $(setting "$f" vcs.modified) == false ]] || die "$name/$bin: built from a modified tree"
		[[ $(main_version "$f") == "$VERSION" ]] || die "$name/$bin: embedded version $(main_version "$f"), want $VERSION"
		[[ $(setting "$f" GOOS)/$(setting "$f" GOARCH) == "$target" ]] || die "$name/$bin: not built for $target"
		for private in "$PWD" "$HOME" "$WORK"; do
			! grep -qaF -- "$private" "$f" || die "$name/$bin: contains the build path $private"
		done
	done
	cp packaging/README.md packaging/smoke-test.sh "$dir/"
	chmod 0755 "$dir/polaroid" "$dir/polaroidd" "$dir/smoke-test.sh"
	chmod 0644 "$dir/README.md"
	archive="$name.tar.gz"
	if tar --version 2>/dev/null | grep -q 'GNU tar'; then
		tar -C "$WORK" --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$(git log -1 --format=%ct)" -czf "$OUT/$archive" "$name"
	else
		COPYFILE_DISABLE=1 tar -C "$WORK" --uid 0 --gid 0 --uname root --gname root -czf "$OUT/$archive" "$name"
	fi
	listed=$(tar -tzf "$OUT/$archive" | sort | tr '\n' ' ')
	[[ $listed == "$name/ $name/README.md $name/polaroid $name/polaroidd $name/smoke-test.sh " ]] || die "$archive holds unexpected entries: $listed"
	assets=$(jq -c --arg os "$os" --arg arch "$arch" --arg archive "$archive" \
		--arg sum "$(sha256 "$OUT/$archive" | cut -d ' ' -f 1)" \
		--arg p "$(sha256 "$dir/polaroid" | cut -d ' ' -f 1)" --arg pd "$(sha256 "$dir/polaroidd" | cut -d ' ' -f 1)" \
		'. + [{os: $os, arch: $arch, archive: $archive, sha256: $sum, files: {polaroid: $p, polaroidd: $pd}}]' <<<"$assets")
done

(cd "$OUT" && sha256 ./*.tar.gz | sed 's| \./| |' >SHA256SUMS)
jq -n --arg version "$VERSION" --arg commit "$COMMIT" --arg time "$(git log -1 --format=%cI)" --arg go "$GOVERSION" \
	--arg run "${GITHUB_SERVER_URL:+$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID}" \
	--argjson assets "$assets" \
	'{format: 1, name: "polaroid", version: $version, prerelease: true,
	  source: {repository: "https://github.com/ashuangiras/polaroid", commit: $commit, commit_time: $time},
	  build: {go: $go, cgo: false, flags: ["-trimpath"], ci_run: (if $run == "" then null else $run end)},
	  checksums: "SHA256SUMS", assets: $assets}' >"$OUT/build-manifest.json"
echo "package: $VERSION at $COMMIT ($GOVERSION) -> $OUT" >&2
ls -l "$OUT" >&2
