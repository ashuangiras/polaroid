#!/usr/bin/env bash
# migrate-catalog.sh SOURCE [POLAROID_HOME]
#
# Moves an existing Polaroid catalog (a SQLite database file) into a Polaroid
# home, by default the per-user one, ~/.polaroid (ADR-0025):
#
#   1. refuses an occupied destination (<home>/data/polaroid.db or its -wal,
#      -shm or -journal file), reporting what is there; nothing is replaced or
#      merged;
#   2. refuses a source that a process still has open (checked with lsof when
#      available): stop polaroidd first, so that no write is lost and no second
#      writable catalog remains;
#   3. backs the source up into <home>/backups/ with SQLite's online backup,
#      which includes committed transactions still in the -wal file (opening
#      the source may fold its -wal into the main file; committed content does
#      not change), as a single file in rollback-journal mode;
#   4. checks the backup (integrity_check, foreign_key_check);
#   5. restores the backup into the destination with mode 0600 (directories it
#      creates are 0700; existing ones are not changed), checks it, and moves it
#      into place without replacing anything;
#   6. confirms that the source, the backup and the destination dump to the
#      same SQL, and prints the tables' row counts.
#
# The source and the backup are kept. Restart polaroidd without -db and
# POLAROID_DB (or with -db <home>/data/polaroid.db). To roll back, stop it and
# start it with -db SOURCE: the source is unchanged, but it lacks anything
# written to the destination since. Exit status: 0 migrated, 1 failed, 2 usage,
# 3 destination occupied.
set -euo pipefail
umask 077

die() { echo "migrate-catalog: $*" >&2; exit 1; }
usage() { echo "usage: scripts/migrate-catalog.sh SOURCE [POLAROID_HOME]" >&2; exit 2; }
[[ $# -eq 1 || $# -eq 2 ]] || usage
command -v sqlite3 >/dev/null || die "the sqlite3 CLI is required"

src=$1
if [[ $# -eq 2 ]]; then
	home=$2
else
	[[ ${HOME:-} == /* ]] || die "HOME is not an absolute path; pass the Polaroid home explicitly"
	home=$HOME/.polaroid
fi
[[ $home == /* ]] || die "the Polaroid home must be an absolute path: $home"
for p in "$src" "$home"; do
	[[ $p != *"'"* && $p != *$'\n'* ]] || die "paths with quotes or newlines are not supported: $p"
done
dest=$home/data/polaroid.db
[[ -f $src ]] || die "no database file at $src"

# The sqlite3 CLI reads with a normal connection: a read-only one cannot open
# a WAL-mode file whose -wal file is absent.
summary() { # summary FILE: user_version, then "table=rows" for every table
	local tables t out
	out="user_version=$(sqlite3 "$1" 'PRAGMA user_version')"
	tables=$(sqlite3 "$1" "SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	for t in $tables; do
		out+=" $t=$(sqlite3 "$1" "SELECT count(*) FROM \"$t\"")"
	done
	printf '%s\n' "$out"
}
dump_digest() { sqlite3 "$1" .dump | shasum -a 256 | cut -d' ' -f1; }
mode_of() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; } # GNU, then BSD
check_db() { # check_db FILE: integrity and foreign keys
	[[ "$(sqlite3 "$1" 'PRAGMA integrity_check')" == ok ]] || die "integrity_check failed for $1"
	[[ -z "$(sqlite3 "$1" 'PRAGMA foreign_key_check')" ]] || die "foreign_key_check failed for $1"
}
copy_db() { # copy_db FROM TO: SQLite online backup into a new single-file database
	sqlite3 "$1" ".backup '$2'" || die "backup of $1 into $2 failed"
	[[ "$(sqlite3 "$2" 'PRAGMA journal_mode = DELETE')" == delete ]] || die "could not make $2 a single file"
	rm -f "$2-shm" # left by the switch; nothing else has the new file open
}

occupied=()
for f in "$dest" "$dest-wal" "$dest-shm" "$dest-journal"; do
	[[ -e $f || -L $f ]] && occupied+=("$f")
done
if ((${#occupied[@]} > 0)); then
	echo "migrate-catalog: the destination is occupied; nothing was changed:" >&2
	for f in "${occupied[@]}"; do
		echo "  $f ($(wc -c <"$f" | tr -d ' ') bytes, modified $(date -r "$f" '+%Y-%m-%dT%H:%M:%S%z'))" >&2
	done
	[[ -f $dest ]] && echo "  contents: $(summary "$dest" 2>&1 || true)" >&2
	echo "  Inspect it and decide by hand; this script never replaces or merges a catalog." >&2
	exit 3
fi
if command -v lsof >/dev/null; then
	if pids=$(lsof -t -- "$src" "$src-wal" 2>/dev/null) && [[ -n $pids ]]; then
		die "the source is open by process $(tr '\n' ' ' <<<"$pids")- stop polaroidd first"
	fi
else
	echo "migrate-catalog: lsof is not available; make sure no polaroidd uses $src" >&2
fi

mkdir -p "$home/backups" "$home/data"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
backup=$home/backups/polaroid-$stamp.db
[[ ! -e $backup ]] || die "backup $backup already exists"
copy_db "$src" "$backup"
check_db "$backup"

tmp=$home/data/.polaroid.db.migrating.$$
trap 'rm -f "$tmp" "$tmp-wal" "$tmp-shm" "$tmp-journal"' EXIT
copy_db "$backup" "$tmp"
check_db "$tmp"
[[ "$(mode_of "$tmp")" == 600 ]] || die "the restored file has mode $(mode_of "$tmp"), want 600"
src_digest=$(dump_digest "$src") backup_digest=$(dump_digest "$backup") tmp_digest=$(dump_digest "$tmp")
[[ $src_digest == "$backup_digest" && $backup_digest == "$tmp_digest" ]] ||
	die "dumps differ: source $src_digest, backup $backup_digest, destination $tmp_digest"
# A hard link never replaces an existing file.
ln "$tmp" "$dest" || die "could not move the catalog into $dest"
rm -f "$tmp"
trap - EXIT

echo "source       $src (kept)"
echo "backup       $backup (kept, integrity ok)"
echo "destination  $dest (mode $(mode_of "$dest"), integrity ok)"
echo "dump sha256  $tmp_digest (source, backup and destination)"
echo "records      $(summary "$dest")"
