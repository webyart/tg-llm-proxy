#!/usr/bin/env bash
# restore-drill.sh — isolated fixture → backup → restore → compare rehearsal without Docker.
#
# This repeats, with plain file paths, what scripts/test-maintenance.ps1 -WithVolumeRestore rehearses on a
# Windows runner: cmd/dbfixture creates a fresh database in a temp directory, cmd/dbbackup copies it,
# cmd/dbrestore materializes the copy in a new directory (the stand-in for a restored volume), and
# cmd/dbinspect -counts compares the original, the restored file and a second restore of the same backup.
# The restored file must open read-write, and nothing outside the mktemp directory is touched. It needs the Go
# toolchain and no Docker daemon, no PowerShell and no Windows tooling.
#
# Usage:
#   bash scripts/restore-drill.sh
#   bash scripts/restore-drill.sh --keep    # keep the temp directory for debugging
#
# Exit: 0 — every check passed; 1 — the first mismatch (or failed tool) stopped the drill.

set -euo pipefail

usage() {
    cat <<'EOF'
usage: bash scripts/restore-drill.sh [-h|--help] [--keep]

Runs the isolated backup/restore rehearsal without Docker:
fixture database → backup → restore into a fresh directory → dbinspect counter
comparison → read-write check → second restore (idempotence).

  -h, --help  show this help and exit
  --keep      keep the mktemp working directory instead of removing it
EOF
}

keep=0
while (($# > 0)); do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        --keep)
            keep=1
            shift
            ;;
        *)
            printf 'restore-drill: unknown argument: %s\n' "$1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)
cd "$repo_root"

if ! command -v go >/dev/null 2>&1; then
    printf 'restore-drill: FAIL — go is not on PATH; run this inside the Go toolchain (CI uses golang:1.27-bookworm)\n' >&2
    exit 1
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/restore-drill.XXXXXX")

cleanup() {
    local status=$1
    if ((keep == 1)); then
        printf 'restore-drill: work directory kept: %s\n' "$tmp" >&2
    else
        rm -rf -- "$tmp" || true
    fi
    return "$status"
}
trap 'cleanup $?' EXIT

pass() {
    printf 'PASS  %s\n' "$*"
}

fail() {
    printf 'FAIL  %s\n' "$*" >&2
    exit 1
}

mkdir -p "$tmp/work/restored-volume" "$tmp/work/second-restore"

source_db=$tmp/work/source.db
backup_db=$tmp/work/backup.db
restored_db=$tmp/work/restored-volume/restored.db
second_db=$tmp/work/second-restore/restored.db
source_counts=$tmp/work/source.counts
restored_counts=$tmp/work/restored.counts
second_counts=$tmp/work/second.counts
counters_diff=$tmp/work/counters.diff

build_tool() {
    local name=$1
    if ! CGO_ENABLED=0 go build -o "$tmp/$name" "./cmd/$name"; then
        fail "go build ./cmd/$name failed"
    fi
}

inspect_counts() {
    local database=$1
    local report=$2
    if ! "$tmp/dbinspect" -path "$database" -counts >"$report"; then
        fail "dbinspect -counts failed for $database"
    fi
}

# The machine-readable block dbinspect -counts prints for one database. The `path=` and journal/open-mode lines
# are facts about the file location and handle, not about the data, so they are excluded exactly as the
# PowerShell rehearsal's `^(integrity|schema_version|last_update_id|bot_state_offset_found|table_|events_)`
# filter does; sorting makes the comparison independent of line order.
filtered_counts() {
    grep -E '^(integrity|schema_version|last_update_id|bot_state_offset_found|table_|events_)' "$1" | sort
}

expect_line() {
    local report=$1
    local line=$2
    local description=$3
    if ! grep -qxF -- "$line" "$report"; then
        fail "$description: expected $line"
    fi
}

compare_counts() {
    local expected=$1
    local actual=$2
    local label=$3
    if ! diff -u <(filtered_counts "$expected") <(filtered_counts "$actual") >"$counters_diff"; then
        printf 'restore-drill: counter difference (%s):\n' "$label" >&2
        cat "$counters_diff" >&2
        fail "$label: counters differ"
    fi
    local count
    count=$(filtered_counts "$expected" | wc -l)
    count=${count//[[:space:]]/}
    pass "$label: $count counters match"
}

printf 'restore-drill: work directory %s\n' "$tmp"

build_tool dbfixture
build_tool dbbackup
build_tool dbrestore
build_tool dbinspect
pass 'build: dbfixture, dbbackup, dbrestore, dbinspect (CGO_ENABLED=0)'

if ! "$tmp/dbfixture" -path "$source_db"; then
    fail 'dbfixture failed'
fi
[[ -s $source_db ]] || fail "fixture database is missing or empty: $source_db"
pass "fixture: $source_db"

if ! "$tmp/dbbackup" -source "$source_db" -dest "$backup_db"; then
    fail 'dbbackup failed'
fi
[[ -s $backup_db ]] || fail "backup file is missing or empty: $backup_db"
pass "backup: $backup_db ($(stat -c '%s' -- "$backup_db") bytes)"

if ! "$tmp/dbrestore" -source "$backup_db" -dest "$restored_db"; then
    fail 'dbrestore failed'
fi
[[ -s $restored_db ]] || fail "restored file is missing or empty: $restored_db"
pass "restore: $backup_db -> $restored_db"

inspect_counts "$source_db" "$source_counts"
inspect_counts "$restored_db" "$restored_counts"

# The fixture itself is checked, not only the source/restore equality: an empty or broken fixture would make a
# vacuous comparison green. schema_version is printed by the current binary and must not be the pre-versioning 0.
expect_line "$source_counts" 'integrity=ok' 'source database'
expect_line "$restored_counts" 'integrity=ok' 'restored database'
expect_line "$source_counts" 'table_messages=4' 'fixture shape'
expect_line "$source_counts" 'table_events=2' 'fixture shape'
expect_line "$source_counts" 'last_update_id=4242' 'fixture shape'
schema_version=$(awk -F= '$1 == "schema_version" {print $2; exit}' "$source_counts") ||
    fail "schema_version is missing from $source_counts"
if [[ ! $schema_version =~ ^[0-9]+$ ]] || ((schema_version == 0)); then
    fail "fixture schema_version is '$schema_version', want a positive integer"
fi
pass "inspect: both files integrity=ok, fixture 4 messages/2 events, schema_version=$schema_version"

compare_counts "$source_counts" "$restored_counts" 'source vs restored'

# The restored volume stand-in: the file must be readable and writable by the user who will run the bot. `<>`
# opens the file O_RDWR without truncating or writing it; ls -l is printed for the ownership/permission record.
if [[ ! -r $restored_db || ! -w $restored_db ]]; then
    fail "restored file is not readable/writable by $(id -un): $restored_db"
fi
if ! ( : <>"$restored_db" ); then
    fail "restored file cannot be opened read-write: $restored_db"
fi
pass "read-write: $(ls -l -- "$restored_db")"

if ! "$tmp/dbrestore" -source "$backup_db" -dest "$second_db"; then
    fail 'second dbrestore of the same backup failed'
fi
inspect_counts "$second_db" "$second_counts"
compare_counts "$source_counts" "$second_counts" 'second restore of the same backup (idempotent)'

if ((keep == 1)); then
    printf 'restore-drill: PASS — isolated fixture → backup → restore → compare rehearsal passed without Docker; work directory kept at %s\n' "$tmp"
else
    printf 'restore-drill: PASS — isolated fixture → backup → restore → compare rehearsal passed without Docker; temp files removed\n'
fi
