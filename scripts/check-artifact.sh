#!/usr/bin/env bash
# check-artifact.sh: targeted regression test for scripts/build-artifact.sh's
# reuse decision (ADR-0041). It builds once in an isolated detached worktree,
# then proves reuse is accepted only when nothing relevant changed, and
# rejected (rebuilt) when the target list, the Go toolchain record, or a
# checksum differs. It never touches the main worktree's checkout.
#
# Usage: scripts/check-artifact.sh   (run `make artifact-check`; needs jq, git)
set -euo pipefail
cd "$(dirname "$0")/.."
command -v jq >/dev/null 2>&1 || { echo "check-artifact: jq is required" >&2; exit 2; }
fail() {
	echo "check-artifact: FAIL: $*" >&2
	exit 1
}

work="$(mktemp -d)"
cleanup() {
	git worktree remove --force "$work/wt" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

git worktree add --detach "$work/wt" HEAD >/dev/null
cd "$work/wt"
out="$work/artifacts"
commit="$(git rev-parse HEAD)"
dir="$out/$commit"
manifest="$dir/manifest.json"

# run_build TARGET... runs build-artifact.sh and remembers whether its own
# message said it reused the existing artifact or built (rebuilt) one.
run_build() {
	last_output="$(./scripts/build-artifact.sh "$out" "$@" 2>&1)"
}
reused() { [[ "$last_output" == *"already holds a matching, checksum-verified artifact"* ]]; }
rebuilt() { ! reused; }

run_build linux/amd64
[[ -f "$manifest" ]] || fail "first build did not write a manifest"
rebuilt || fail "the first build should always build, never reuse: $last_output"
sum1="$(jq -r '.assets[0].files.polaroid.sha256' "$manifest")"
echo "check-artifact: built $commit ($sum1)"

run_build linux/amd64
reused || fail "an unchanged rerun rebuilt instead of reusing: $last_output"
echo "check-artifact: unchanged rerun reused the artifact"

run_build linux/amd64 darwin/arm64
rebuilt || fail "a different target list should force a rebuild: $last_output"
echo "check-artifact: a different target list forced a rebuild"

run_build linux/amd64 darwin/arm64
reused || fail "an unchanged rerun (two targets) rebuilt instead of reusing: $last_output"

jq '.go = "go0.0.0-not-the-real-toolchain"' "$manifest" >"$manifest.tmp" && mv "$manifest.tmp" "$manifest"
run_build linux/amd64 darwin/arm64
rebuilt || fail "a toolchain recorded differently from go env GOVERSION should force a rebuild: $last_output"
echo "check-artifact: a different recorded Go toolchain forced a rebuild"

real="$(jq -r '.assets[0].files.polaroid.path' "$manifest")"
printf '\0' >>"$dir/$real"
run_build linux/amd64 darwin/arm64
rebuilt || fail "a corrupted checksum should force a rebuild: $last_output"
sum2="$(jq -r '.assets[0].files.polaroid.sha256' "$manifest")"
[[ "$sum2" == "$sum1" ]] || fail "rebuilding from the identical, unmodified source changed the checksum"
echo "check-artifact: a corrupted file forced a rebuild, and the rebuilt checksum matches the first build"

echo "check-artifact: PASS"
