#!/usr/bin/env bash
# Runs go test on PACKAGES (default ./...). Go's test cache may serve every
# package except those in UNCACHED, which always run with -count=1: their
# results depend on inputs the cache does not track. Cached results are
# printed by go test as "(cached)"; report them as cached, not as fresh runs.
# GOFLAGS=-count=1 in the environment still makes every package run.
#
# Usage: scripts/go-test.sh [GO TEST FLAGS...] [--] [PACKAGES...]
#   for example scripts/go-test.sh -race -- ./internal/memory/...
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2
GO=${GO:-go}

# internal/archtest: reads other packages with `go list`, which the cache does
#   not see, so a new forbidden import would not invalidate a cached pass.
# internal/lifecycle: builds polaroid from a snapshot of the working tree
#   before the tests start (unseen by the cache), and checks real processes
#   and their timing.
# internal/recovery: runs real daemons and stops them, and checks timing.
UNCACHED=(./internal/archtest ./internal/lifecycle ./internal/recovery)

flags=()
while (($# > 0)) && [[ $1 == -* ]]; do
	[[ $1 == -- ]] && { shift; break; }
	flags+=("$1")
	shift
done
(($# > 0)) || set -- ./...

all="$("$GO" list "$@")" || exit 1
fresh="$("$GO" list "${UNCACHED[@]}")" || exit 1
cached="$(grep -vxF -e "$fresh" <<<"$all" || true)"
uncached="$(grep -xF -e "$fresh" <<<"$all" || true)"

rc=0
shown="$GO test${flags[*]:+ ${flags[*]}}"
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
# The two groups run concurrently, as one go test of every package would;
# their outputs are printed one after the other.
if [[ -n "$cached" ]]; then
	# shellcheck disable=SC2086 # one package per word
	"$GO" test ${flags[@]+"${flags[@]}"} $cached >"$out/cached" 2>&1 &
	cached_pid=$!
fi
if [[ -n "$uncached" ]]; then
	# shellcheck disable=SC2086
	"$GO" test ${flags[@]+"${flags[@]}"} -count=1 $uncached >"$out/uncached" 2>&1 &
	uncached_pid=$!
fi
if [[ -n "$cached" ]]; then
	wait "$cached_pid" || rc=1
	echo "$shown <$(wc -l <<<"$cached" | tr -d ' ') packages; the test cache applies>"
	cat "$out/cached"
fi
if [[ -n "$uncached" ]]; then
	wait "$uncached_pid" || rc=1
	# shellcheck disable=SC2086 # one package per word
	echo "$shown -count=1" $uncached
	cat "$out/uncached"
fi
exit "$rc"
