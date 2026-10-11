#!/usr/bin/env bash
# release-notes.sh MANIFEST [TAG_MESSAGE]: prints the release notes for the release that
# build-manifest.json MANIFEST describes, with the annotated tag's message if given. A
# vX.Y.Z version is a stable release, vX.Y.Z-label a prerelease (ADR-0038). The release
# workflow runs it; it needs jq. Environment: GITHUB_SERVER_URL, GITHUB_REPOSITORY and
# GITHUB_RUN_ID name the packaging run.
set -euo pipefail
die() {
	echo "release-notes: $*" >&2
	exit 1
}
[[ $# == 1 || $# == 2 ]] || die "usage: scripts/release-notes.sh MANIFEST [TAG_MESSAGE]"
m=$1 message=${2:-}
TAG=$(jq -r .version "$m") commit=$(jq -r .source.commit "$m") go=$(jq -r .build.go "$m") pre=$(jq -r .prerelease "$m")
repo=${GITHUB_REPOSITORY:-ashuangiras/polaroid} server=${GITHUB_SERVER_URL:-https://github.com}
run_url="$server/$repo/actions/runs/${GITHUB_RUN_ID:-unknown}"
case $TAG in
v[0-9]*.[0-9]*.[0-9]*-*) [[ $pre == true ]] || die "$TAG is a prerelease version but the manifest says prerelease $pre" ;;
v[0-9]*.[0-9]*.[0-9]*) [[ $pre == false ]] || die "$TAG is a stable version but the manifest says prerelease $pre" ;;
*) die "$TAG is not vX.Y.Z or vX.Y.Z-label" ;;
esac
if [[ $pre == true ]]; then
	echo "**Prerelease, not a stable release.** It lets a reviewer download and test the exact build of one source commit, and makes no compatibility promise. Binaries are unsigned and not notarized."
else
	echo "**Stable release** for the supported scope: a local, single-user service on loopback, used by agents over MCP, the CLI and the HTTP API, on one machine ([release readiness](https://github.com/$repo/blob/$commit/docs/development/release-readiness.md)). It follows the [compatibility policy](https://github.com/$repo/blob/$commit/docs/architecture/decisions/0038-stable-releases-and-compatibility.md): within major version 1, later releases only add. Binaries are unsigned and not notarized."
fi
echo
echo "- **Source commit:** [\`$commit\`]($server/$repo/tree/$commit) (tag \`$TAG\`)"
echo "- **Packaging run:** $run_url (\`.github/workflows/release.yml\`)"
echo "- **Toolchain:** $go, \`CGO_ENABLED=0\`, \`-trimpath\`; both binaries report \`version\` $TAG and \`revision\` $commit"
echo
if [[ -n $message && -s $message ]]; then
	cat "$message"
	echo
fi
echo "## Assets"
echo
echo "| Platform | Archive | Tested in the packaging run |"
echo "| --- | --- | --- |"
echo "| linux/amd64 | \`polaroid_${TAG}_linux_amd64.tar.gz\` | Natively on ubuntu-latest, and in an Alpine container without systemd |"
echo "| linux/arm64 | \`polaroid_${TAG}_linux_arm64.tar.gz\` | Natively on ubuntu-24.04-arm |"
echo "| darwin/amd64 | \`polaroid_${TAG}_darwin_amd64.tar.gz\` | Natively on macos-15-intel |"
echo "| darwin/arm64 | \`polaroid_${TAG}_darwin_arm64.tar.gz\` | Natively on macos-latest (arm64) |"
echo
echo "Each test verified \`SHA256SUMS\`, extracted the archive, checked both binaries' reported version and commit, and ran the archive's \`smoke-test.sh\` with no checkout, no Go and an empty \`HOME\`: with the runner's temporary directory, with the platform's normal \`TMPDIR\` (on macOS the per-user directory, which ends in \`/\`), and with a \`TMPDIR\` containing spaces. The smoke test includes a backup of the running catalog, its inspection, and a restore with \`polaroid restore\` read back through the API, all inside its temporary directory. It also checked that the script reports a failing daemon or check and still cleans up. After publishing, the same run downloads the published assets, compares them with the tested ones and repeats the linux/amd64 test (job \"test the published assets\")."
echo
echo "\`SHA256SUMS\` covers every archive. \`build-manifest.json\` gives the full commit, version, Go toolchain, targets and per-asset hashes."
echo
echo "## Quick start (Linux amd64)"
echo
echo '```sh'
echo "base=$server/$repo/releases/download/$TAG"
echo "curl -fsSLO \"\$base/polaroid_${TAG}_linux_amd64.tar.gz\" -O \"\$base/SHA256SUMS\""
echo "sha256sum -c --ignore-missing SHA256SUMS"
echo "tar -xzf polaroid_${TAG}_linux_amd64.tar.gz && cd polaroid_${TAG}_linux_amd64"
echo "./polaroid version && ./polaroidd -version && ./smoke-test.sh"
echo '```'
echo
echo "On macOS use \`shasum -a 256 -c --ignore-missing SHA256SUMS\` and the \`darwin\` archive. The archive's README.md covers installation as a per-user service, connecting an MCP client, backups and upgrades."
echo
echo "## Known limitations"
echo
if [[ $pre == true ]]; then
	echo "- Prerelease: no compatibility promise."
fi
echo "- Unsigned and not notarized. On macOS, a browser download may need \`xattr -d com.apple.quarantine polaroid polaroidd\`."
echo "- The smoke test runs \`polaroidd\` directly. It does not test the managed service (\`polaroid install\`, launchd, systemd) or a restore of the managed catalog; the archive's README.md describes that separate test, and the repository tests both with \`make lifecycle\`."
echo "- The managed service supports macOS (launchd) and Linux (systemd user services) only; elsewhere, run \`polaroidd\` directly."
echo "- The API and MCP server have no authentication or authorization: one user, loopback only. Do not expose them on a network."
echo "- History is kept forever: there is no pruning or retention. Back up with \`polaroid backup\` and remove old backups yourself."
