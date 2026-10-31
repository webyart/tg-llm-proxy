// inspect.go — the read-only side of §5, the layer `scripts/db_inspect.ps1` reads the chat history
// through. sqlite3 CLI is not installed on this host (§2), so the reader has to be a Go tool — and a Go
// tool that opens the database of a RUNNING bot must not be able to change it.
//
// Three measured facts of §5 decide the shape of the DSN here:
//
//	(1) `mode=ro` is a URI parameter of SQLite, not a driver key; on an existing file it opens fine and
//	    every write answers `attempt to write a readonly database (8)` (§5 case D, re-measured on
//	    modernc.org/sqlite v1.61.0). On a MISSING file the same parameter answers SQLITE_CANTOPEN and
//	    creates nothing — which is why OpenReadOnly stats the path first and says what that means.
//	(2) `mode=ro` together with `cache=shared` is `SQLITE_CANTOPEN: illegal argument` (§5 case D), so this
//	    DSN carries no cache=shared — unlike MemoryDSN of §11, which cannot work without it.
//	(3) `_txlock=immediate`, the key that saves the runtime pool from the WAL upgrade deadlock, makes even
//	    a SELECT take the write lock. A reader must not do that to a live bot, so it is absent here — and so
//	    is `_journal_mode`, because journal_mode is a property of the FILE (§5): a tool that only came to
//	    look must not be the thing that re-modes a database.
//
// dsn.go says «never add mode=ro». That rule governs BuildDSN, the DSN the bot runs on, where mode=ro
// would break writes and refuse to create the file. This file is the second, maintenance-side open §5
// describes separately («Maintenance-DSN — отдельный, он не равен эталонному»): the same DB_PATH of §7, a
// deliberately weaker connection, and no migration.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ReadOnlyBusyTimeout — how long the reader waits for a writer before it gives up. The runtime number of
// §5 (5000 ms) is reused rather than 0: §5 measured that a maintenance open with `_busy_timeout=0` answers
// SQLITE_BUSY the instant a writer holds the lock, which for a viewer means «fail on every /ask». It is a
// constant and not a fourth §7 row because §7 has no row for the maintenance side (same reasoning as
// MaxOpenConns of §11).
const ReadOnlyBusyTimeout = RuntimeBusyTimeout

// ReadOnlyDSN — the §5 maintenance-side DSN over the file at path: the path rules of §5 are the rules of
// BuildDSN (an empty path, a path with ?, # or a percent sign, a `file:` prefix and control bytes are all
// refused there), checked by calling BuildDSN for the verdict only; the query this function emits is the
// reader's own: mode=ro + the busy timeout, and nothing else.
//
// A memory path is refused: `mode=memory` under mode=ro would open a FRESH empty database and present it
// as «the bot's history is empty» — the loudest kind of lie this tool could tell (§12 reliability).
// readOnlyPathGuard — the path rules of §5 for any read-only open: a memory path and a query-bearing path are
// refused the same way, and the message names the function that refused (§9: the operator has to see which
// open was rejected). Extracted because the plain and the immutable reader are two opens over one path.
func readOnlyPathGuard(caller, path string) error {
	if IsMemory(path) {
		return fmt.Errorf("store.%s: %q is a memory database (§5: a read-only open of mode=memory creates a fresh EMPTY database — the history of a running process is not readable this way)", caller, path)
	}
	if _, buildErr := BuildDSN(path); buildErr != nil {
		// The message of BuildDSN already names the §5 rule and quotes the path; it is wrapped so the
		// operator sees which open was refused (§9: db_inspect and the bot read the same DB_PATH).
		return fmt.Errorf("store.%s: %w (§5: the same path rules as the runtime DSN)", caller, buildErr)
	}
	return nil
}

func ReadOnlyDSN(path string) (string, error) {
	if guardErr := readOnlyPathGuard("ReadOnlyDSN", path); guardErr != nil {
		return "", guardErr
	}
	return "file:" + EscapePath(path) + "?mode=ro&_busy_timeout=" + ReadOnlyBusyTimeout, nil
}

// OpenReadOnly — the reader's handle: ReadOnlyDSN → sql.Open → Ping. It does NOT apply the schema and does
// NOT verify the runtime pragmas: both would write to the file, and §5 keeps the file's journal_mode a
// once-per-database decision. A missing file is reported as a missing file rather than as the driver's
// SQLITE_CANTOPEN, because what the operator needs to hear is «there is no database here yet».
//
// This open is the strict one: it is what the tests of §11 use and what a writable path always gets. The
// reader that also copes with a WAL file on a read-only mount is OpenReadOnlySnapshot below.
func OpenReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	dsn, dsnErr := ReadOnlyDSN(path)
	if dsnErr != nil {
		return nil, dsnErr
	}
	return openReader(ctx, path, dsn, "OpenReadOnly")
}

// WALSideFile — the -wal of §5 next to a database file: SQLite names it by appending "-wal". Whether that file
// exists decides what a reader is allowed to see (§5: a committed transaction lives in the WAL until the
// checkpoint moves it into the main file).
func WALSideFile(path string) string { return path + "-wal" }

// WALSideFilePresent — is the WAL of §5 sitting next to the file? Absent means the main file holds every
// committed page: SQLite deletes the -wal only after moving its pages into the main file, and store.Close of §5
// is what checkpoints it (measured 2026-10-31 in the container of §8.1: after `docker compose stop`,
// the named volume held bot.db alone — 36 KB, no -wal and no -shm).
func WALSideFilePresent(path string) bool {
	_, statErr := os.Stat(WALSideFile(path))
	return statErr == nil
}

// ReadOnlyImmutableDSN — the second reader DSN of §5: the same mode=ro and the same busy timeout, plus the
// SQLite URI parameter `immutable=1`. The spelling matters: the underscore is the driver's namespace, and
// `_immutable=1` is not this parameter — measured in the container on 2026-10-31, where `_immutable=1` kept
// answering the same error as the plain open while `immutable=1` opened the file.
//
// WHY A SECOND READER EXISTS. mode=ro on a WAL database needs the -shm of §5: the reader either attaches the
// existing shared memory or creates it. On a read-only mount that creation is impossible, and SQLite answers
// `unable to open database file (14)` — SQLITE_CANTOPEN — for a file that is plainly there and readable. The
// shape is not hypothetical: reading the named volume of §8.1 with `-v tg-llm-proxy_bot-data:/data:ro` hits it
// exactly after a clean stop, when the -wal and the -shm are gone. The operator asked for the history of a
// container that is not running, and «cannot open» is not the honest answer.
//
// WHAT THIS HANDLE CANNOT SEE. It never touches the WAL machinery, so a committed transaction still sitting in
// a -wal file stays invisible — and PRAGMA journal_mode answers "delete" through it even for a WAL file (both
// measured in the container on 2026-10-31). That is why this DSN is not the default and why the open is
// reported by name: see OpenReadOnlySnapshot and ReadOnlyMode.
func ReadOnlyImmutableDSN(path string) (string, error) {
	if guardErr := readOnlyPathGuard("ReadOnlyImmutableDSN", path); guardErr != nil {
		return "", guardErr
	}
	return "file:" + EscapePath(path) + "?mode=ro&immutable=1&_busy_timeout=" + ReadOnlyBusyTimeout, nil
}

// ReadOnlyMode — which of the two read-only opens the reader ended up with. The name belongs in the report and
// is not decoration: the two handles differ in what they can see and in what PRAGMA journal_mode answers, so a
// number taken through one of them has to say which one (§12 reliability).
type ReadOnlyMode string

const (
	// ReadOnlyModePlain — mode=ro: the accurate reader, it attaches the WAL of §5 and is what a writable path
	// (the host file of scripts/db_inspect.ps1, scripts/test-restart.ps1) always gets.
	ReadOnlyModePlain ReadOnlyMode = "mode=ro"
	// ReadOnlyModeImmutable — mode=ro+immutable: the snapshot reader of a WAL file whose -wal is absent, on a
	// mount that forbids creating the -shm. Complete for that file (nothing is left in a WAL that does not
	// exist), but blind to a live writer and wrong about journal_mode.
	ReadOnlyModeImmutable ReadOnlyMode = "mode=ro+immutable"
)

// OpenReadOnlySnapshot — the read-only open of §9 that both a script and a human can rely on in either shape
// of the file: the accurate plain open when it works, the immutable one when the WAL side file of §5 is absent
// and the mount refuses the plain one. The second open is offered ONLY with no -wal next to the file, and that
// condition is not a detail: while a -wal exists it can hold committed pages, and an immutable read would
// report an old history as the current one (§12 reliability: an incomplete history must not look complete).
// With no -wal file the main file IS the whole state (§5).
//
// The mode is part of the contract: cmd/dbinspect prints it beside every number read through that handle,
// because `journal_mode=delete` taken through an immutable open of a WAL file is a fact about the handle, not
// about the database.
func OpenReadOnlySnapshot(ctx context.Context, path string) (*sql.DB, ReadOnlyMode, error) {
	db, plainErr := OpenReadOnly(ctx, path)
	if plainErr == nil {
		return db, ReadOnlyModePlain, nil
	}
	if WALSideFilePresent(path) {
		return nil, "", fmt.Errorf("store.OpenReadOnlySnapshot: %w — and %s exists, so the immutable open of §5 is not offered here: it reads the main file only and would hide the pages of §5 that have not reached it yet", plainErr, WALSideFile(path))
	}

	snapshot, snapshotErr := openReadOnlyImmutable(ctx, path)
	if snapshotErr != nil {
		return nil, "", fmt.Errorf("store.OpenReadOnlySnapshot: the plain reader failed: %w; the immutable reader of the same file failed too: %s", plainErr, snapshotErr)
	}
	return snapshot, ReadOnlyModeImmutable, nil
}

// openReadOnlyImmutable — the immutable handle over the path: ReadOnlyImmutableDSN plus the same one-connection
// body as the plain reader. Package-private on purpose: the decision to use it belongs to
// OpenReadOnlySnapshot, which is the place that knows about the -wal.
func openReadOnlyImmutable(ctx context.Context, path string) (*sql.DB, error) {
	dsn, dsnErr := ReadOnlyImmutableDSN(path)
	if dsnErr != nil {
		return nil, dsnErr
	}
	return openReader(ctx, path, dsn, "openReadOnlyImmutable")
}

// openReader — the body the two read-only opens share: refuse a missing file by name (§5 case D), keep one
// connection (a viewer is not a server — the pool of 4 of §11 exists for parallel model calls, and a reader
// holding four handles only adds read locks a live writer has to route around), and treat Ping as the real
// check (§5 spike 11: sql.Open is silent).
func openReader(ctx context.Context, path, dsn, caller string) (*sql.DB, error) {
	if _, statErr := os.Stat(path); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf(`store.%s: "%s" does not exist (§5 case D: mode=ro never creates a file — the bot has not started with this DB_PATH yet, or §7 points at another path)`, caller, path)
		}
		return nil, fmt.Errorf(`store.%s: "%s" cannot be inspected: %w`, caller, path, statErr)
	}

	db, openErr := sql.Open(sqliteDriver, dsn)
	if openErr != nil {
		return nil, fmt.Errorf("store.%s: the reader DSN %s was rejected: %w (§5: a bad DSN is a configuration problem, the text names it)", caller, dsn, openErr)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if pingErr := db.PingContext(ctx); pingErr != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store.%s: the reader DSN %s opens a handle that does not answer: %w (§5 spike 11: sql.Open is silent, the Ping is the real check)", caller, dsn, pingErr)
	}
	return db, nil
}

// ChatRow — one row of §5 `chats` with the message count beside it: the thing a human actually wants to
// know about a chat. ContextResetAt is the /reset mark of §5 (the rows before it stay in the file and stop
// entering the context), so the counter next to it is deliberately the FULL count, not the context window.
type ChatRow struct {
	ChatID         int64
	UserID         int64
	Title          string
	CreatedAt      string
	LastActiveAt   string
	ContextResetAt string
	Messages       int64
}

// ListChats — the chats of §5 ordered by the last activity; chatID ≤ 0 means every chat, a positive chatID is
// the `-chat` filter of the viewer, applied in SQL rather than in Go so that `LIMIT` counts the chats the
// operator asked about and not the newest neighbours of an old chat (§9 scripts/db_inspect.ps1 -Chat).
// created_at / last_active_at are returned exactly as stored: datetime('now') in UTC with no timezone mark
// (README «Часовой пояс»), because a viewer that silently converted to local time would make a log line and a
// DB row impossible to line up.
func ListChats(ctx context.Context, db *sql.DB, chatID int64, limit int) ([]ChatRow, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store.ListChats: the limit is %d, want a positive number of chats (§9 db_inspect -limit)", limit)
	}
	rows, queryErr := db.QueryContext(ctx, `
		SELECT c.chat_id, c.user_id, COALESCE(c.title, ''), c.created_at, c.last_active_at,
		       COALESCE(c.context_reset_at, ''),
		       (SELECT COUNT(*) FROM messages m WHERE m.chat_id = c.chat_id)
		  FROM chats c
		 WHERE ? <= 0 OR c.chat_id = ?
		 ORDER BY c.last_active_at DESC, c.chat_id DESC
		 LIMIT ?`, chatID, chatID, limit)
	if queryErr != nil {
		return nil, fmt.Errorf("store.ListChats: %w (§5 chats)", queryErr)
	}
	defer rows.Close()

	var found = []ChatRow{}
	for rows.Next() {
		var row ChatRow
		if scanErr := rows.Scan(&row.ChatID, &row.UserID, &row.Title, &row.CreatedAt, &row.LastActiveAt,
			&row.ContextResetAt, &row.Messages); scanErr != nil {
			return nil, fmt.Errorf("store.ListChats: %w (§5 chats)", scanErr)
		}
		found = append(found, row)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("store.ListChats: %w (§5 chats)", iterErr)
	}
	return found, nil
}

// RecentMessage — one row of §5 `messages` for the viewer: the columns HistoryOfChat reads plus the service
// columns an operator wants when an answer looked wrong (model, latency, the token counts, the Telegram
// message id that guards against a repeated send). Content comes back whole — §12 security keeps the SECRET
// values out of the logs, a chat text is user data the operator already owns; truncating is the script's job.
// TokensIn/TokensOut/TelegramMessageID use -1 for a NULL column (§5 makes them nullable), because a viewer
// that printed 0 would claim «the model used zero tokens» where the truth is «nobody counted». LatencyMS is
// -1 only if a row was written outside Append: Append stores the plain int64 of Message, so a user row
// really holds 0 in the file and the reader shows that 0 (§12 reliability: a viewer reports the file).
type RecentMessage struct {
	ID                int64
	ChatID            int64
	Role              string
	Content           string
	TokensIn          int64
	TokensOut         int64
	Model             string
	LatencyMS         int64
	TelegramMessageID int64
	CreatedAt         string
}

// RecentMessages — the newest rows of §5 `messages`, one chat or all of them (chatID <= 0). The order is
// id DESC and not created_at DESC: datetime('now') has SECOND resolution (§5), so the rows of one answer
// tie and shuffle; the AUTOINCREMENT id is the only order that matches how they were written.
func RecentMessages(ctx context.Context, db *sql.DB, chatID int64, limit int) ([]RecentMessage, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store.RecentMessages: the limit is %d, want a positive number of messages (§9 db_inspect -limit)", limit)
	}
	var statement = `
		SELECT m.id, m.chat_id, m.role, m.content,
		       COALESCE(m.tokens_in, -1), COALESCE(m.tokens_out, -1),
		       COALESCE(m.model, ''), COALESCE(m.latency_ms, -1),
		       COALESCE(m.telegram_message_id, -1), m.created_at
		  FROM messages m`
	var args = []any{}
	if chatID > 0 {
		statement += ` WHERE m.chat_id = ?`
		args = append(args, chatID)
	}
	statement += ` ORDER BY m.id DESC LIMIT ?`
	args = append(args, limit)

	rows, queryErr := db.QueryContext(ctx, statement, args...)
	if queryErr != nil {
		return nil, fmt.Errorf("store.RecentMessages: %w (§5 messages)", queryErr)
	}
	defer rows.Close()

	var found = []RecentMessage{}
	for rows.Next() {
		var row RecentMessage
		if scanErr := rows.Scan(&row.ID, &row.ChatID, &row.Role, &row.Content, &row.TokensIn, &row.TokensOut,
			&row.Model, &row.LatencyMS, &row.TelegramMessageID, &row.CreatedAt); scanErr != nil {
			return nil, fmt.Errorf("store.RecentMessages: %w (§5 messages)", scanErr)
		}
		found = append(found, row)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("store.RecentMessages: %w (§5 messages)", iterErr)
	}
	return found, nil
}

// EventRow — one row of the §5 `events` table (denied / flood / model_error / rate_limit …). UserID is 0
// where the column is NULL: an event can be written before the sender is known (the allowlist check of §6
// runs before EnsureChat), so the script prints 0 as «—» rather than as a real account.
type EventRow struct {
	ID        int64
	UserID    int64
	Kind      string
	Detail    string
	CreatedAt string
}

// RecentEvents — the newest service events, id DESC (same second-resolution reasoning as RecentMessages).
// This is the function that answers the question a quiet bot always raises: «was it the allowlist, the rate
// limit or STRATA?» — all three land in one table with different kinds (§6, §10 День 4 DoD).
func RecentEvents(ctx context.Context, db *sql.DB, limit int) ([]EventRow, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store.RecentEvents: the limit is %d, want a positive number of events (§9 db_inspect -limit)", limit)
	}
	rows, queryErr := db.QueryContext(ctx, `
		SELECT e.id, COALESCE(e.user_id, 0), e.kind, COALESCE(e.detail, ''), e.created_at
		  FROM events e
		 ORDER BY e.id DESC
		 LIMIT ?`, limit)
	if queryErr != nil {
		return nil, fmt.Errorf("store.RecentEvents: %w (§5 events)", queryErr)
	}
	defer rows.Close()

	var found = []EventRow{}
	for rows.Next() {
		var row EventRow
		if scanErr := rows.Scan(&row.ID, &row.UserID, &row.Kind, &row.Detail, &row.CreatedAt); scanErr != nil {
			return nil, fmt.Errorf("store.RecentEvents: %w (§5 events)", scanErr)
		}
		found = append(found, row)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("store.RecentEvents: %w (§5 events)", iterErr)
	}
	return found, nil
}

// KindCount — how many events of one kind exist. Sorted by count DESC, so the interesting kind lands on the
// first line instead of wherever the alphabet put it.
type KindCount struct {
	Kind  string
	Count int64
}

// EventCounts — the `events` table grouped by kind: the one-glance health picture of §6 / §10 День 4
// (`denied` growing = the allowlist of §7 is wrong, `model_error` growing = STRATA, `flood` = Telegram 429).
func EventCounts(ctx context.Context, db *sql.DB) ([]KindCount, error) {
	rows, queryErr := db.QueryContext(ctx, `
		SELECT e.kind, COUNT(*) FROM events e GROUP BY e.kind ORDER BY COUNT(*) DESC, e.kind`)
	if queryErr != nil {
		return nil, fmt.Errorf("store.EventCounts: %w (§5 events)", queryErr)
	}
	defer rows.Close()

	var found = []KindCount{}
	for rows.Next() {
		var row KindCount
		if scanErr := rows.Scan(&row.Kind, &row.Count); scanErr != nil {
			return nil, fmt.Errorf("store.EventCounts: %w (§5 events)", scanErr)
		}
		found = append(found, row)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("store.EventCounts: %w (§5 events)", iterErr)
	}
	return found, nil
}

// StateEntry — one row of the §5 `bot_state` table (today: last_update_id, the offset that must survive a
// restart). A viewer that cannot see the offset cannot tell a stuck poller from a quiet chat (§13).
type StateEntry struct {
	Key   string
	Value string
}

// StateEntries — the whole of `bot_state` ordered by key. The table is the service state of the process and
// holds a couple of rows, so reading all of it beats guessing which keys exist.
func StateEntries(ctx context.Context, db *sql.DB) ([]StateEntry, error) {
	rows, queryErr := db.QueryContext(ctx, `SELECT key, value FROM bot_state ORDER BY key`)
	if queryErr != nil {
		return nil, fmt.Errorf("store.StateEntries: %w (§5 bot_state)", queryErr)
	}
	defer rows.Close()

	var found = []StateEntry{}
	for rows.Next() {
		var row StateEntry
		if scanErr := rows.Scan(&row.Key, &row.Value); scanErr != nil {
			return nil, fmt.Errorf("store.StateEntries: %w (§5 bot_state)", scanErr)
		}
		found = append(found, row)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("store.StateEntries: %w (§5 bot_state)", iterErr)
	}
	return found, nil
}

// TableNames — the user tables of the file through sqlite_master. The `sqlite_%` filter drops SQLite's own
// bookkeeping (sqlite_sequence appears the moment AUTOINCREMENT exists, §5 messages): it has a row count and
// no meaning to a reader.
func TableNames(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, queryErr := db.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		 WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		 ORDER BY name`)
	if queryErr != nil {
		return nil, fmt.Errorf("store.TableNames: %w (§5 sqlite_master)", queryErr)
	}
	defer rows.Close()

	var found = []string{}
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return nil, fmt.Errorf("store.TableNames: %w (§5 sqlite_master)", scanErr)
		}
		found = append(found, name)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("store.TableNames: %w (§5 sqlite_master)", iterErr)
	}
	return found, nil
}

// TableCount — a table and how many rows it holds.
type TableCount struct {
	Name string
	Rows int64
}

// TableCounts — COUNT(*) over every table of TableNames. The name comes from sqlite_master and never from a
// caller, and it is still re-checked by isTableName before it is quoted into the statement: an identifier
// cannot be a bound parameter, so this is the one place in the file that builds SQL text instead of using a
// placeholder, and §12 security says a built string needs a whitelist (the §5 `PRAGMA table_info` case: a
// table literally named `x"); DROP …` must not become two statements).
func TableCounts(ctx context.Context, db *sql.DB) ([]TableCount, error) {
	names, nameErr := TableNames(ctx, db)
	if nameErr != nil {
		return nil, nameErr
	}
	var counts = []TableCount{}
	for _, name := range names {
		if !isTableName(name) {
			return nil, fmt.Errorf("store.TableCounts: the table name %q is not a plain identifier (§12 security: an identifier cannot be a parameter, so an unusual name is refused instead of quoted)", name)
		}
		var rows int64
		if queryErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+name+`"`).Scan(&rows); queryErr != nil {
			return nil, fmt.Errorf("store.TableCounts: %q: %w (§5, the tables of migrations/001_init.sql)", name, queryErr)
		}
		counts = append(counts, TableCount{Name: name, Rows: rows})
	}
	return counts, nil
}

// isTableName — the identifier whitelist of TableCounts: a letter or underscore first, then letters, digits
// and underscores. Every table of migrations/001_init.sql satisfies it; anything carrying a quote, a
// bracket, a space or a semicolon never reaches the statement.
func isTableName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// IntegrityCheck — `PRAGMA integrity_check` of the §15 backlog as an on-demand read rather than a scheduled
// job. The pragma is a pure read, which is why it is legal on a mode=ro handle, and it answers one line per
// finding: the single word "ok" for a sound file. The text is returned untouched for the caller to print —
// §10 День 2 DoD («не молчать о битом файле») means the script shows the body and leaves with a non-zero
// code when it is not "ok"; guessing here would hide the exact lines SQLite produced.
func IntegrityCheck(ctx context.Context, db *sql.DB) (string, error) {
	var lines = []string{}
	rows, queryErr := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if queryErr != nil {
		return "", fmt.Errorf("store.IntegrityCheck: %w (§15: the pragma is a read, so a mode=ro handle can run it)", queryErr)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if scanErr := rows.Scan(&line); scanErr != nil {
			return "", fmt.Errorf("store.IntegrityCheck: %w (§15)", scanErr)
		}
		lines = append(lines, line)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return "", fmt.Errorf("store.IntegrityCheck: %w (§15)", iterErr)
	}
	return strings.Join(lines, "\n"), nil
}
