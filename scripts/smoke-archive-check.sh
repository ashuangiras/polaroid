#!/usr/bin/env bash
# smoke-archive-check.sh: runs an extracted release archive's own
# smoke-test.sh, unmodified, under the temporary directories users have (#52),
# and checks that it cleans up after passing and after failing runs. The
# release workflow runs it on each native archive runner; run it locally
# against any extracted archive:
#
#   scripts/smoke-archive-check.sh DIR    DIR holds polaroid, polaroidd, smoke-test.sh
#
# Each run has only smoke-test.sh's documented tools on PATH, an empty HOME
# and no other environment, as in the release workflow. Scenarios:
#   normal       the platform's own TMPDIR: on macOS the per-user directory
#                (getconf DARWIN_USER_TEMP_DIR, which ends in /), elsewhere
#                unset, so /tmp
#   spaces       a TMPDIR with spaces that ends in /
#   no-start     a polaroidd that cannot start: must fail and still clean up
#   failed-check a polaroid whose health check fails mid-run: must report
#                that failure and still clean up and stop polaroidd
# Before them it proves its own leftover-daemon check: a real polaroidd
# using a directory is found, and a process that only names polaroidd and
# that directory in its arguments (as this checker's own commands do) is not.
# Exits 0 only when every scenario behaves as expected.
set -euo pipefail
die() {
	echo "smoke-archive-check: $*" >&2
	exit 2
}
[[ $# == 1 ]] || die "usage: scripts/smoke-archive-check.sh DIR"
ARCHIVE=$(cd "$1" && pwd)
for f in polaroid polaroidd smoke-test.sh; do [[ -x $ARCHIVE/$f ]] || die "$ARCHIVE/$f is missing or not executable"; done

SCRATCH=$(mktemp -d "${TMPDIR:-/tmp}/smoke-archive-check.XXXXXX")
trap 'rm -rf "$SCRATCH"' EXIT
TOOLS="$SCRATCH/tools"
mkdir -p "$TOOLS"
for t in sh curl mktemp mkdir rm ls cat sed grep head sleep dirname; do
	ln -s "$(command -v "$t")" "$TOOLS/$t"
done

PASSED=0 FAILED=0
pass() {
	PASSED=$((PASSED + 1))
	echo "PASS: $1"
}
fail() {
	FAILED=$((FAILED + 1))
	echo "FAIL: $1"
}

# daemons_using DIR: the PIDs of polaroidd processes whose arguments name DIR.
# A process counts only if its executable is polaroidd (ps comm: the name on
# Linux, the path on macOS), never because its arguments mention it, which
# pgrep -f did, matching the checker's own command line.
daemons_using() {
	local pid comm
	while read -r pid comm; do
		[[ $comm == polaroidd || $comm == */polaroidd ]] || continue
		if ps -o args= -p "$pid" 2>/dev/null | grep -qF -- "$1"; then echo "$pid"; fi
	done < <(ps -A -o pid= -o comm=)
}

probe="$SCRATCH/process probe"
mkdir -p "$probe/home" "$probe/data"
# A shell that is not exec-optimised away, whose arguments name polaroidd and the directory.
sh -c 'sleep 60; :' "polaroidd -addr 127.0.0.1:0 -db $probe/data/smoke.db" &
decoy=$!
HOME="$probe/home" "$ARCHIVE/polaroidd" -addr 127.0.0.1:0 -db "$probe/data/smoke.db" 2>"$probe/daemon.log" &
real=$!
for _ in $(seq 100); do grep -q 'polaroidd listening' "$probe/daemon.log" && break; sleep 0.1; done
if ps -o args= -p "$decoy" | grep -qF -- "$probe"; then pass "process check: the decoy $decoy names polaroidd and the directory in its arguments"; else fail "process check: the decoy is not visible"; fi
found=$(daemons_using "$probe" | tr '\n' ' ')
if [[ $found == "$real " ]]; then pass "process check: finds exactly the real polaroidd $real, not the decoy"; else fail "process check: found [$found], want [$real ]"; fi
kill "$real" "$decoy" 2>/dev/null || true
wait "$real" "$decoy" 2>/dev/null || true
if [[ -z $(daemons_using "$probe") ]]; then pass "process check: nothing found once it has exited"; else fail "process check: still finds a daemon after it exited"; fi

# smoke NAME DIR TMPDIR|-: runs DIR/smoke-test.sh with TMPDIR set (or unset
# for -), then checks HOME stayed empty, the temporary directory it reported
# is gone, and no polaroidd still uses it. Sets RC and OUT.
smoke() {
	local name=$1 dir=$2 tmp=$3 home="$SCRATCH/$1 home" work
	mkdir -p "$home"
	local vars=(PATH="$TOOLS" HOME="$home")
	[[ $tmp == - ]] || vars+=(TMPDIR="$tmp")
	echo "== $name: TMPDIR=${tmp/#-/(unset)}"
	set +e
	OUT=$(cd "$dir" && env -i "${vars[@]}" ./smoke-test.sh 2>&1)
	RC=$?
	set -e
	printf '%s\n' "$OUT" | sed 's/^/   | /'
	work=$(printf '%s\n' "$OUT" | sed -n 's/^temporary directory: //p')
	if [[ -n $work && ! -e $work ]]; then pass "$name: its temporary directory $work was removed"; else fail "$name: temporary directory [$work] left behind or not reported"; fi
	if [[ -z $(ls -A "$home") ]]; then pass "$name: HOME stayed empty"; else fail "$name: wrote to HOME"; fi
	if [[ -n $work && -n $(daemons_using "$work") ]]; then fail "$name: a polaroidd still uses $work"; else pass "$name: no polaroidd is left running"; fi
}
has() { grep -qxF -- "$1" <<<"$OUT"; }
summary_ok() { ! grep -q '^FAIL: ' <<<"$OUT" && grep -qE '^smoke test: passed=[0-9]+ failed=0$' <<<"$OUT"; }

expect_pass() {
	if [[ $RC == 0 ]] && summary_ok && has "PASS: the start-up line names the temporary database"; then
		pass "$1: smoke test passed, including the start-up line check"
	else
		fail "$1: smoke test exit $RC, want 0 with every check passed"
	fi
}

if [[ $(uname -s) == Darwin ]]; then
	normal=$(getconf DARWIN_USER_TEMP_DIR)
	[[ -d $normal ]] || die "getconf DARWIN_USER_TEMP_DIR gave $normal, not a directory"
else
	normal=-
fi
smoke normal "$ARCHIVE" "$normal"
expect_pass normal

spaces="$SCRATCH/tmp with spaces/"
mkdir -p "$spaces"
smoke spaces "$ARCHIVE" "$spaces"
expect_pass spaces
if [[ -z $(ls -A "$spaces") ]]; then pass "spaces: nothing left in TMPDIR"; else fail "spaces: left $(ls -A "$spaces") in TMPDIR"; fi

# The failure scenarios run the same smoke-test.sh beside substituted binaries.
broken="$SCRATCH/no start"
mkdir -p "$broken" "$SCRATCH/no-start tmp"
cp "$ARCHIVE/smoke-test.sh" "$ARCHIVE/polaroid" "$broken/"
# shellcheck disable=SC2016 # the stub's own $1, expanded when it runs
printf '#!/bin/sh\ncase "$1" in -version) exec "%s/polaroidd" -version ;; esac\necho "refusing to start" >&2\nexit 1\n' "$ARCHIVE" >"$broken/polaroidd"
chmod +x "$broken/polaroidd"
smoke no-start "$broken" "$SCRATCH/no-start tmp"
if [[ $RC != 0 ]] && has "FAIL: polaroidd starts" && grep -q '^refusing to start$' <<<"$OUT"; then
	pass "no-start: reported as failed, with the daemon's log"
else
	fail "no-start: exit $RC, want non-zero with 'FAIL: polaroidd starts'"
fi
if [[ -z $(ls -A "$SCRATCH/no-start tmp") ]]; then pass "no-start: nothing left in TMPDIR"; else fail "no-start: files left in TMPDIR"; fi

failing="$SCRATCH/failed check"
mkdir -p "$failing" "$SCRATCH/failed-check tmp"
cp "$ARCHIVE/smoke-test.sh" "$ARCHIVE/polaroidd" "$failing/"
# shellcheck disable=SC2016 # the wrapper's own $@ and $a, expanded when it runs
printf '#!/bin/sh\nfor a in "$@"; do [ "$a" = health ] && { echo "health refused" >&2; exit 1; }; done\nexec "%s/polaroid" "$@"\n' "$ARCHIVE" >"$failing/polaroid"
chmod +x "$failing/polaroid"
smoke failed-check "$failing" "$SCRATCH/failed-check tmp"
if [[ $RC != 0 ]] && has "FAIL: polaroid health succeeds" && has "smoke test: passed=$(grep -c '^PASS: ' <<<"$OUT") failed=1"; then
	pass "failed-check: exactly the failing check reported, exit non-zero"
else
	fail "failed-check: exit $RC, want non-zero with only 'FAIL: polaroid health succeeds'"
fi
if [[ -z $(ls -A "$SCRATCH/failed-check tmp") ]]; then pass "failed-check: nothing left in TMPDIR"; else fail "failed-check: files left in TMPDIR"; fi

echo "smoke-archive-check: passed=$PASSED failed=$FAILED"
[[ $FAILED == 0 ]]
