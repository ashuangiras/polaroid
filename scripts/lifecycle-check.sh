#!/usr/bin/env bash
# lifecycle-check.sh: exercises `polaroid install/start/stop/restart/status/
# uninstall` against the REAL service manager of this machine (launchd on
# macOS, the systemd user manager on Linux), with an isolated installation: a
# temporary HOME whose path has spaces, a service name of its own, a free
# port and a temporary catalog. It never touches the user's installation or
# ~/.polaroid. Run it with `make lifecycle`; it exits non-zero on a failure,
# and with 77 (skipped) when no usable service manager is reachable.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2
for tool in curl jq; do
	command -v "$tool" >/dev/null || { echo "lifecycle-check: $tool is required" >&2; exit 2; }
done
case "$(uname -s)" in
Darwin) KIND=launchd ;;
Linux) KIND=systemd ;;
*) echo "lifecycle-check: SKIPPED: no supported service manager on $(uname -s)"; exit 77 ;;
esac
if [[ $KIND == systemd ]] && ! systemctl --user show-environment >/dev/null 2>&1; then
	echo "lifecycle-check: SKIPPED: the systemd user manager is not reachable from this session"
	exit 77
fi
if [[ $KIND == launchd ]] && ! launchctl print "gui/$(id -u)" >/dev/null 2>&1; then
	echo "lifecycle-check: SKIPPED: no launchd GUI login session for this user"
	exit 77
fi

ROOT="$(mktemp -d)"
REAL_HOME=$HOME
# Keep Go's caches where they are; only the installation moves.
GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)" GOPATH="$(go env GOPATH)"
export GOCACHE GOMODCACHE GOPATH
export HOME="$ROOT/home with spaces"
unset POLAROID_DB
mkdir -p "$HOME"
NAME="polaroid-check-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
PORT=$((20000 + RANDOM % 20000))
ADDR=127.0.0.1:$PORT
URL=http://$ADDR
BIN="$HOME/.local/bin"
LABEL=io.github.ashuangiras.$NAME
UNIT=$NAME.service
PASS=0 FAIL=0 MANUAL=""
[[ $HOME != "$REAL_HOME" && $NAME != polaroid ]] || exit 2

cleanup() {
	[[ -n $MANUAL ]] && kill "$MANUAL" 2>/dev/null
	"$BIN/polaroid" uninstall >/dev/null 2>&1 || true
	if [[ $KIND == launchd ]]; then
		launchctl bootout "gui/$(id -u)/$LABEL" >/dev/null 2>&1 || true
	else
		systemctl --user disable --now "$UNIT" >/dev/null 2>&1 || true
		systemctl --user daemon-reload >/dev/null 2>&1 || true
	fi
	rm -rf "$ROOT"
}
trap cleanup EXIT

check() { # check DESCRIPTION COMMAND...
	local desc=$1
	shift
	if "$@"; then
		PASS=$((PASS + 1))
		echo "PASS: $desc"
	else
		FAIL=$((FAIL + 1))
		echo "FAIL: $desc"
	fi
}
# cli ARGS...: runs a polaroid command; JSON in $OUT, exit status in $RC.
cli() {
	OUT="$("$CLI" "$@" 2>"$ROOT/stderr")"
	RC=$?
	echo "  \$ polaroid $* -> exit $RC: $(tail -n 1 "$ROOT/stderr")"
}
state_is() { [[ $RC == "$1" && "$(jq -r .state <<<"$OUT")" == "$2" ]]; }
field() { jq -r "$1" <<<"$OUT"; }
manager_pid() {
	if [[ $KIND == launchd ]]; then
		launchctl list | awk -F '\t' -v l="$LABEL" '$3 == l && $1 != "-" { print $1 }'
	else
		local p
		p=$(systemctl --user show "$UNIT" --property=MainPID --value 2>/dev/null)
		[[ ${p:-0} != 0 ]] && echo "$p"
	fi
}
# wait_for SECONDS COMMAND...: polls until COMMAND succeeds.
wait_for() {
	local deadline=$((SECONDS + $1))
	shift
	until "$@"; do
		((SECONDS < deadline)) || return 1
		sleep 0.5
	done
}
healthy() { curl -sf "$URL/healthz" >/dev/null; }
no_daemon() { ! pgrep -f "$BIN/polaroidd" >/dev/null; }
record() { curl -sf "$URL/v1/procedures/by-key/lifecycle.check"; }

echo "lifecycle-check: $KIND, service $NAME, HOME \"$HOME\", endpoint $ADDR"
# Builds A ("build 0"), B ("build 1") and a B whose polaroidd reports its
# build but exits 3 when started, from three commits of a snapshot of this
# working tree, so that each reports a revision of its own.
B0="$ROOT/build 0" B1="$ROOT/build 1" BROKEN="$ROOT/build broken" SRC="$ROOT/src"
git ls-files -z -co --exclude-standard | while IFS= read -r -d '' f; do
	[[ -e $f ]] && mkdir -p "$SRC/$(dirname "$f")" && cp "$f" "$SRC/$f"
done
g() { git -C "$SRC" -c user.name=lifecycle -c user.email=lifecycle@example.invalid -c commit.gpgsign=false "$@"; }
build() { (cd "$SRC" && go build -o "$1/" ./cmd/polaroidd ./cmd/polaroid); }
{ g init -q && g add -A && g commit -qm A && build "$B0" &&
	g commit -q --allow-empty -m B && build "$B1" &&
	printf 'package main\n\nimport "os"\n\nfunc init() {\n\tif len(os.Args) < 2 || os.Args[1] != "-version" {\n\t\tos.Exit(3)\n\t}\n}\n' >"$SRC/cmd/polaroidd/broken.go" &&
	g add -A && g commit -qm "broken B" && build "$BROKEN"; } || exit 2
REV0=$("$B0/polaroidd" -version | jq -r .revision) REV1=$("$B1/polaroidd" -version | jq -r .revision)
[[ -n $REV0 && $REV0 != null && $REV0 != "$REV1" ]] || { echo "lifecycle-check: builds 0 and 1 need distinct revisions" >&2; exit 2; }
# Install's staging goes here, so that leftovers can be seen.
export TMPDIR="$ROOT/tmp"
mkdir -p "$TMPDIR"
CLI="$B0/polaroid"

echo "[1] Not installed"
cli status
check "status before install: not-installed, exit 4" state_is 4 not-installed

echo "[2] Install registers login startup, starts the service and waits until it is healthy"
cli install -from "$B0" -addr "$ADDR" -service-name "$NAME"
check "install: exit 0, running" state_is 0 running
check "the binaries are in ~/.local/bin, mode 755" test -x "$BIN/polaroid" -a -x "$BIN/polaroidd"
CLI="$BIN/polaroid"
PID=$(manager_pid)
check "the manager runs the managed process, which status reports" test -n "$PID" -a "$PID" == "$(field .pid)"
check "the process is the installed polaroidd, by absolute path" bash -c "ps -o command= -p $PID | grep -qF -- '$BIN/polaroidd -addr $ADDR'"
if [[ $KIND == launchd ]]; then
	check "the definition is in LaunchAgents and is a valid property list" plutil -lint "$HOME/Library/LaunchAgents/$LABEL.plist"
else
	check "the unit is enabled for login (default.target)" bash -c "[[ \$(systemctl --user is-enabled '$UNIT') == enabled ]]"
fi
curl -sf -X POST -H 'Content-Type: application/json' "$URL/v1/procedures" \
	-d '{"canonical_key":"lifecycle.check","version":{"philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}}' >/dev/null
BEFORE="$(record)"
check "a record was written through the managed service" test -n "$BEFORE"
DB="$HOME/.polaroid/data/polaroid.db"
check "the catalog is the default one, private: data 700, database 600" bash -c "[[ \$(stat -c %a '$HOME/.polaroid/data' 2>/dev/null || stat -f %Lp '$HOME/.polaroid/data') == 700 && \$(stat -c %a '$DB' 2>/dev/null || stat -f %Lp '$DB') == 600 ]]"

echo "[3] Repeated install changes nothing"
cli install -from "$B0"
check "repeat: exit 0, unchanged, same process" bash -c "[[ $RC == 0 && '$(field .action)' == 'unchanged; already running' && '$(field .pid)' == '$PID' ]]"

echo "[4] An unexpected exit is restarted by the manager"
kill -KILL "$PID"
check "after SIGKILL, the manager starts a new process within 30s" wait_for 30 bash -c "p=\$($(declare -f manager_pid); KIND=$KIND LABEL=$LABEL UNIT=$UNIT manager_pid); [[ -n \$p && \$p != $PID ]] && curl -sf $URL/healthz >/dev/null"
cli status
check "status: running again, a new process" bash -c "[[ $RC == 0 && '$(field .state)' == running && '$(field .pid)' != '$PID' ]]"
check "the record survived the crash" test "$(record)" == "$BEFORE"

echo "[5] An explicit stop stays stopped"
cli stop
check "stop: exit 0, stopped" state_is 0 stopped
sleep 15 # longer than launchd's ThrottleInterval and systemd's RestartSec
check "15s later: no managed process and no listener" bash -c "! curl -sf $URL/healthz >/dev/null && [[ -z \$($(declare -f manager_pid); KIND=$KIND LABEL=$LABEL UNIT=$UNIT manager_pid) ]]"
check "no polaroidd from the installation runs" no_daemon
cli status
check "status: stopped, exit 3" state_is 3 stopped
cli stop
check "a second stop: exit 0, stopped" state_is 0 stopped

echo "[6] Start, repeated start, restart"
cli start
check "start: exit 0, running" state_is 0 running
P1=$(field .pid)
cli start
check "a second start: already running, same process" bash -c "[[ $RC == 0 && '$(field .action)' == 'already running' && '$(field .pid)' == '$P1' ]]"
cli restart
check "restart: exit 0, running, a new process" bash -c "[[ $RC == 0 && '$(field .state)' == running && '$(field .pid)' != '$P1' ]]"

echo "[7] A process the installation did not start is reported, never claimed or stopped"
cli stop
"$B0/polaroidd" -addr "$ADDR" -db "$ROOT/manual.db" 2>"$ROOT/manual.log" &
MANUAL=$!
wait_for 10 healthy
cli status
check "status: stopped, with the manual process named as the endpoint's holder" bash -c "[[ $RC == 3 && '$(field .conflict.pid)' == '$MANUAL' ]]"
cli start
check "start refuses: exit 1, conflict" bash -c "[[ $RC == 1 ]] && grep -q 'held by another process' '$ROOT/stderr'"
check "the manual process still serves" healthy
check "the manual process is not the managed one" test -z "$(manager_pid)"
kill "$MANUAL"
wait "$MANUAL" 2>/dev/null
MANUAL=""

echo "[8] A daemon that cannot start is reported failed and left stopped"
mkdir -p "$ROOT/a directory"
cli install -from "$B0" -db "$ROOT/a directory"
check "install with an unusable database: exit 1, failed" state_is 1 failed
check "the failure names the diagnostics" bash -c "grep -q 'see ' '$ROOT/stderr' || [[ '$(field .detail)' == *see* ]]"
sleep 12
check "12s later it is not being restarted" bash -c "[[ -z \$($(declare -f manager_pid); KIND=$KIND LABEL=$LABEL UNIT=$UNIT manager_pid) ]]"
cli status
check "status: failed, exit 1, last start failed" bash -c "[[ $RC == 1 && '$(field .detail)' == *'last start failed'* ]]"
cli install -from "$B0" -db "$DB"
check "install with the catalog again: running" state_is 0 running
check "the record is intact" test "$(record)" == "$BEFORE"

echo "[9] Upgrade: stop, replace, restart, verify"
P2=$(field .pid)
cli install -from "$B1"
check "upgrade: exit 0, upgraded, running, a new process" bash -c "[[ $RC == 0 && '$(field .action)' == upgraded && '$(field .state)' == running && '$(field .pid)' != '$P2' ]]"
check "the installed polaroidd is build 1" cmp -s "$BIN/polaroidd" "$B1/polaroidd"
check "build 0 is kept for recovery" cmp -s "$HOME/.local/state/polaroid/previous/polaroidd" "$B0/polaroidd"
check "the record is intact" test "$(record)" == "$BEFORE"

# is_build0: the installed files, their record and the running daemon are build 0.
is_build0() {
	local pid ok=0
	cmp -s "$BIN/polaroidd" "$B0/polaroidd" && cmp -s "$BIN/polaroid" "$B0/polaroid" || ok=1
	[[ $("$BIN/polaroid" version | jq -r .revision) == "$REV0" && $("$BIN/polaroidd" -version | jq -r .revision) == "$REV0" ]] || ok=1
	[[ $(jq -r .build.revision "$HOME/.local/state/polaroid/install.json") == "$REV0" ]] || ok=1
	[[ $(jq -r '.files[] | select(.role == "polaroidd") | .sha256' "$HOME/.local/state/polaroid/install.json") == "$(sha256 "$B0/polaroidd")" ]] || ok=1
	[[ $(jq -r '.files[] | select(.role == "polaroid") | .sha256' "$HOME/.local/state/polaroid/install.json") == "$(sha256 "$B0/polaroid")" ]] || ok=1
	pid=$(manager_pid)
	if [[ $KIND == systemd ]]; then
		cmp -s "/proc/$pid/exe" "$B0/polaroidd" || ok=1
	else
		[[ $(grep 'polaroidd listening' "$HOME/Library/Logs/Polaroid/polaroidd.log" | tail -n 1) == *"revision=$REV0"* ]] || ok=1
	fi
	return $ok
}
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d ' ' -f 1; }
no_staging() { [[ -z $(ls -A "$TMPDIR") && $(ls -A "$HOME/.local/state/polaroid" | sort | tr '\n' ' ') == "install.json previous " ]]; }

echo "[10] Recovery with the documented command restores the previous build on the same catalog"
P3=$(field .pid)
cli install -from "$HOME/.local/state/polaroid/previous"
check "recovery: exit 0, running, a new process" bash -c "[[ $RC == 0 && '$(field .state)' == running && '$(field .pid)' != '$P3' ]]"
check "binaries, reported revisions, record hashes and the running daemon are build 0" is_build0
check "previous/ now holds build 1" cmp -s "$HOME/.local/state/polaroid/previous/polaroidd" "$B1/polaroidd"
check "the record is intact" test "$(record)" == "$BEFORE"
check "no staging is left behind" no_staging

echo "[11] After an upgrade that fails to start, recovery through a symlink with spaces to previous/"
failed_with_recovery() { [[ $RC == 1 && $(field .state) == failed && $(field .detail) == *"polaroid install -from $HOME/.local/state/polaroid/previous"* ]]; }
cli install -from "$BROKEN"
check "the failing upgrade: exit 1, failed, with the recovery command" failed_with_recovery
ln -s "$HOME/.local/state/polaroid/previous" "$ROOT/recovery link"
cli install -from "$ROOT/recovery link"
check "recovery: exit 0, running" state_is 0 running
check "binaries, reported revisions, record hashes and the running daemon are build 0" is_build0
check "previous/ now holds the failed build" cmp -s "$HOME/.local/state/polaroid/previous/polaroidd" "$BROKEN/polaroidd"
check "the record is intact" test "$(record)" == "$BEFORE"
check "no staging is left behind" no_staging

echo "[12] Diagnostics are private and bounded"
if [[ $KIND == launchd ]]; then
	LOG="$HOME/Library/Logs/Polaroid/polaroidd.log"
	check "the log is mode 600 in a 700 directory, and has the start-up line" bash -c "[[ \$(stat -f %Lp '$LOG') == 600 && \$(stat -f %Lp '$(dirname "$LOG")') == 700 ]] && grep -q 'polaroidd listening' '$LOG'"
else
	if journalctl --user -u "$UNIT" -n 50 --no-pager 2>/dev/null | grep -q 'polaroidd listening'; then
		check "the journal has the start-up line" true
	else
		echo "NOTE: the user journal is not readable here; journal diagnostics were not checked"
	fi
fi

echo "[13] Backup and restore of the managed catalog, coordinated with the real manager (ADR-0030)"
BACKUPS="$HOME/.polaroid/backups"
mode_of() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
after() { curl -sf "$URL/v1/procedures/by-key/lifecycle.after" >/dev/null; }
cli backup
BK=$(field .path)
check "backup of the running managed catalog: exit 0, managed source, default directory" bash -c "[[ $RC == 0 && '$BK' == '$BACKUPS'/polaroid-* && '$(field .metadata.source.managed)' == true && '$(field .metadata.source.path)' == '$DB' ]]"
check "backups are private: directory 700, database 600" bash -c "[[ \$($(declare -f mode_of); mode_of '$BACKUPS') == 700 && \$($(declare -f mode_of); mode_of '$BK/polaroid.db') == 600 ]]"
curl -sf -X POST -H 'Content-Type: application/json' "$URL/v1/procedures" \
	-d '{"canonical_key":"lifecycle.after","version":{"philosophy":"p","method":"m","contract":{},"instructions":{},"revision_reason":"r"}}' >/dev/null
check "a record written after the backup is served" after
cli restore -plan "$BK"
check "restore -plan: ready, managed, running service, requires -replace" bash -c "[[ $RC == 0 && '$(field .ready)' == true && '$(field .destination.managed)' == true && '$(field .service.state)' == running && '$(field .requires_replace)' == true ]]"
P4=$(manager_pid)
cli restore -replace "$BK"
RECOVERY=$(field .recovery_backup)
check "restore -replace while running: restored, the service running again as a new process" bash -c "[[ $RC == 0 && '$(field .outcome)' == restored && '$(field .service.state)' == running && -n '$(manager_pid)' && '$(manager_pid)' != '$P4' ]]"
check "the restored service is healthy" healthy
check "the record written after the backup is gone" bash -c "! curl -sf '$URL/v1/procedures/by-key/lifecycle.after' >/dev/null"
check "the record from before the backup is intact" test "$(record)" == "$BEFORE"
check "the recovery backup is in the default directory and holds the replaced catalog" bash -c "[[ '$RECOVERY' == '$BACKUPS'/polaroid-*-pre-restore* ]] && '$CLI' inspect-backup '$RECOVERY' | jq -e '.valid and .records.procedures == 2' >/dev/null"
check "the catalog is private and no restore leftovers remain" bash -c "[[ \$($(declare -f mode_of); mode_of '$DB') == 600 && -z \$(ls -A '$(dirname "$DB")' | grep -Ev '^polaroid\.db(-wal|-shm)?\$') ]]"
cli stop
SUM=$(sha256 "$DB")
"$B0/polaroidd" -addr "$ADDR" -db "$ROOT/manual.db" 2>"$ROOT/manual.log" &
MANUAL=$!
wait_for 10 healthy
cli restore -replace "$RECOVERY"
check "restore refuses while an unowned process holds the endpoint: exit 3, unchanged" bash -c "[[ $RC == 3 && '$(field .outcome)' == unchanged && '$(sha256 "$DB")' == '$SUM' ]]"
kill "$MANUAL"
wait "$MANUAL" 2>/dev/null
MANUAL=""
cli restore -replace "$RECOVERY"
check "restore -replace while stopped: restored, the service left stopped" bash -c "[[ $RC == 0 && '$(field .outcome)' == restored && '$(field .plan.service.state)' == stopped && '$(field .service)' == null && -z '$(manager_pid)' ]] && ! curl -sf $URL/healthz >/dev/null"
cli start
check "after start, the recovery backup's record is back" bash -c "[[ $RC == 0 ]] && curl -sf '$URL/v1/procedures/by-key/lifecycle.after' >/dev/null"
check "the record from before the backup is intact" test "$(record)" == "$BEFORE"

echo "[14] Uninstall keeps the catalog; reinstall serves it"
cli uninstall
check "uninstall: exit 0, not-installed" state_is 0 not-installed
check "binaries, definition and state are gone" bash -c "[[ ! -e '$BIN/polaroid' && ! -e '$BIN/polaroidd' && ! -e '$HOME/.local/state/polaroid' && ! -e '$HOME/Library/LaunchAgents/$LABEL.plist' && ! -e '$HOME/.config/systemd/user/$UNIT' ]]"
check "the service is no longer known to the manager" test -z "$(manager_pid)"
check "the catalog is kept" test -s "$DB"
CLI="$B0/polaroid"
cli uninstall
check "a second uninstall: exit 0" test "$RC" == 0
cli install -from "$B0" -addr "$ADDR" -service-name "$NAME"
check "reinstall: running" state_is 0 running
check "the record is served again" test "$(record)" == "$BEFORE"
CLI="$BIN/polaroid"
cli uninstall
check "final uninstall: exit 0" test "$RC" == 0

echo "lifecycle-check: $KIND passed=$PASS failed=$FAIL"
((FAIL == 0))
