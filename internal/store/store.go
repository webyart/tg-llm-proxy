// whitelist of dsn.go, the `sql.Open → db.Ping() → PRAGMA journal_mode/foreign_keys` sequence that
//
//  1. `sql.Open` says nothing about a bad DSN — the validation lives in the driver's connect, so
//     an error and fails on every query of that connection afterwards). Open therefore Pings
//     immediately and reads the two pragms back; a typo would otherwise reach the first handled
//     update and repeat on every query.
//  2. `foreign_keys` is per-connection and `journal_mode` is stored in the file itself, so
//     both come from the DSN, and Open verifies what the database really answers.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

// closes the only connection on returning it and a shared-cache memory database disappears
const (
	MaxOpenConns = 4
	MaxIdleConns = 2
)

// EnableStrictPragmas turns on sqlite.StrictPragmas — the second line of defence behind the
// into every connection at Connect, so it has to be set in main BEFORE the first sql.Open). It
// only rejects a `_pragma` value that is more than one SQL statement, which is exactly the %3B
func EnableStrictPragmas() bool {
	return sqlite.StrictPragmas(true)
}

func StrictPragmasEnabled() bool {
	return sqlite.StrictPragmasEnabled()
}

// pragms → the schema. It returns a ready *sql.DB or an error that quotes the DSN, because a
// rejected DSN is a configuration problem and the path is the thing the operator has to fix
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn, err := BuildDSN(path)
	if err != nil {
		return nil, err
	}
	if err := prepareParent(path); err != nil {
		return nil, err
	}
	return openDSN(ctx, dsn, path, IsMemory(path))
}

// hands one database to everyone in the process, so parallel tests of one package collide), which is
func OpenMemory(ctx context.Context, name string) (*sql.DB, error) {
	dsn, err := MemoryDSN(name)
	if err != nil {
		return nil, err
	}
	return openDSN(ctx, dsn, name, true)
}

func openDSN(ctx context.Context, dsn, describe string, memory bool) (*sql.DB, error) {
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("store.Open: sql.Open rejected the DSN %q (%w) —: the driver validates the DSN in connect, so this is normally silence here and an error on the first Ping", dsn, err)
	}
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)

	// handled update, where it would repeat on every query of the connection.
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: the first Ping rejected the DSN %q (%w) — spike 11: sql.Open stays silent, the file may already exist but it is NOT in WAL ", dsn, err)
	}

	if err := verifyPragmas(ctx, db, memory); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: the DSN %q did not produce the pragmas of (%w)", dsn, err)
	}

	if err := ApplySchema(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: the schema of could not be applied to %q (%w)", describe, err)
	}
	// The search index is a runtime artifact of the schema (see ensureSearchIndexes): the FTS table comes from
	// a migration, the triggers and the one-time rebuild do not fit the lexical statement splitter.
	if err := ensureSearchIndexes(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: %w", err)
	}
	return db, nil
}

// prepareParent — SQLite does not create a missing parent directory: it answers SQLITE_CANTOPEN
func prepareParent(path string) error {
	if IsMemory(path) {
		return nil
	}
	// filepath.Dir, so the parent of `sub\bot.db` would be computed as the CWD while the file itself
	// lands in sub/ — the directory SQLite needs would never be created.
	slash := strings.ReplaceAll(path, "\\", "/")
	dir := filepath.Dir(slash)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store.Open: the directory %q of the database cannot be created (%w) —: in compose the named volume bot-data:/data provides it", dir, err)
	}
	return nil
}

// silently unavailable in memory, which is why WAL and busy_timeout are tested on a file only).
// propagate it to the pool.
func verifyPragmas(ctx context.Context, db *sql.DB, memory bool) error {
	want := strings.ToLower(RuntimeJournalMode)
	if memory {
		want = "memory"
	}

	mode, err := Pragma(ctx, db, JournalModePragma)
	if err != nil {
		return fmt.Errorf("PRAGMA %s could not be read: %w", JournalModePragma, err)
	}
	if !IsAllowed("_"+JournalModePragma, mode) || strings.ToLower(mode) != want {
		return fmt.Errorf("PRAGMA journal_mode = %q, expects %q for this database form", mode, want)
	}

	keys, err := Pragma(ctx, db, ForeignKeysPragma)
	if err != nil {
		return fmt.Errorf("PRAGMA %s could not be read: %w", ForeignKeysPragma, err)
	}
	if !IsAllowed("_"+ForeignKeysPragma, keys) {
		return fmt.Errorf("PRAGMA foreign_keys = %q, needs 1: without it REFERENCES of are not enforced and the guard uq_messages_chat_tg_id has no parent check", keys)
	}
	return nil
}

// Pragma — one pragma of the whitelist read back as text (`PRAGMA journal_mode` → "wal",
// `PRAGMA foreign_keys` → 1). The DSN spelling of the name (`_journal_mode`) is accepted for
// convenience and cut: `PRAGMA _journal_mode` answers nothing, because the '_' prefix belongs to the
// nothing — which is exactly why a typo must be refused before the query).
func Pragma(ctx context.Context, db *sql.DB, name string) (string, error) {
	if !IsPragma(name) {
		return "", fmt.Errorf("store.Pragma: %q is not in the whitelist of, refusing to ask the database about it", name)
	}
	sqlName := strings.TrimPrefix(strings.TrimSpace(name), "_")
	var value string
	var raw any
	if err := db.QueryRowContext(ctx, "PRAGMA "+sqlName).Scan(&raw); err != nil {
		return "", err
	}
	switch v := raw.(type) {
	case nil:
		value = ""
	case []byte:
		value = string(v)
	case string:
		value = v
	default:
		// PRAGMA foreign_keys and PRAGMA busy_timeout answer as INTEGER; journal_mode answers as
		value = fmt.Sprintf("%v", v)
	}
	return value, nil
}

// ApplySchema bootstraps schema_migrations, then applies each unapplied embedded migration in its own
// transaction. The initial schema remains idempotent so a pre-versioning database is adopted at version 1
// without changing its rows; later starts skip that version instead of replaying its statements.
//
// Two processes may open the same file at the same time (the bot and dbprune/ragembed). The applied check
// before the loop is only a fast path: every pending migration is re-checked inside BEGIN IMMEDIATE, so a
// migration another process has just committed is skipped instead of replayed. The state is also re-guarded
// inside that transaction, because a newer release could have migrated the file after the read-only check.
func ApplySchema(ctx context.Context, db *sql.DB) error {
	// BEGIN IMMEDIATE serializes the bootstrap too: two concurrent CREATE TABLE IF NOT EXISTS statements
	// already race on sqlite_master, and the loser would otherwise fail to start.
	if err := bootstrapSchemaMigrations(ctx, db); err != nil {
		return err
	}
	// A database written by a newer release is refused before a single migration is applied: the
	// embedded history stops at the version this binary was built with, and running the old code
	// against a newer schema is how a rollback corrupts data instead of failing loudly.
	if err := GuardSchemaVersion(ctx, db); err != nil {
		return err
	}
	applied, err := appliedSchemaVersions(ctx, db)
	if err != nil {
		return err
	}
	for index, migration := range MigrationSQL {
		version := index + 1
		if applied[version] {
			continue
		}
		statements, err := Statements(migration)
		if err != nil {
			return err
		}
		if err := applyMigration(ctx, db, version, statements); err != nil {
			return err
		}
	}
	return nil
}

// bootstrapSchemaMigrations creates the version table under the write lock. The table is created inside
// a transaction that is committed before any migration runs; the migration transactions then only have to
// serialize their own work.
func bootstrapSchemaMigrations(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("schema_migrations bootstrap could not begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("schema_migrations bootstrap failed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("schema_migrations bootstrap could not commit: %w", err)
	}
	return nil
}

func appliedSchemaVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("the applied schema versions could not be read: %w", err)
	}
	defer rows.Close()
	applied := make(map[int]bool, len(MigrationSQL))
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("an applied schema version could not be read: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("the applied schema versions could not be read: %w", err)
	}
	return applied, nil
}

// applyMigration runs one migration under BEGIN IMMEDIATE. The version check and the future-schema guard
// are repeated inside the transaction: the read-only fast path of ApplySchema is not authoritative once
// another process can write. A concurrent process that committed this version already is skipped, which is
// what makes a non-idempotent statement of a historical migration safe to meet twice.
func applyMigration(ctx context.Context, db *sql.DB, version int, statements []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %d could not begin: %w", version, err)
	}
	defer tx.Rollback()

	supported := len(MigrationSQL)
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("the schema version could not be re-read for migration %d: %w", version, err)
	}
	if current > supported {
		return fmt.Errorf("%w: the database is at version %d, this binary supports %d", ErrSchemaDowngrade, current, supported)
	}
	var applied int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&applied); err != nil {
		return fmt.Errorf("schema version %d could not be re-checked: %w", version, err)
	}
	if applied != 0 {
		// Another process won the race; commit nothing and leave its rows untouched.
		return nil
	}

	for i, statement := range statements {
		skip, err := migrationStatementAlreadyApplied(ctx, tx, version, statement)
		if err != nil {
			return fmt.Errorf("statement %d of migration %d could not be checked (%w); text: %s", i+1, version, err, firstLine(statement))
		}
		if skip {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("statement %d of migration %d failed (%w); text: %s", i+1, version, err, firstLine(statement))
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("migration %d could not be recorded: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %d could not commit: %w", version, err)
	}
	return nil
}

// migrationStatementAlreadyApplied is the small per-migration guard mechanism. 011 was written before the
// write-transaction re-check existed and its ALTER TABLE ADD COLUMN is not idempotent; a database where an
// operator (or an interrupted older release) already added the column must still reach the index statement.
// The historical file is deliberately not rewritten: the guard is a property of execution, and the check is
// pragma_table_info rather than a parsing guess.
func migrationStatementAlreadyApplied(ctx context.Context, tx *sql.Tx, version int, statement string) (bool, error) {
	guard, ok := migrationStatementGuards[version]
	if !ok {
		return false, nil
	}
	return guard(ctx, tx, statement)
}

type migrationStatementGuard func(context.Context, *sql.Tx, string) (bool, error)

var migrationStatementGuards = map[int]migrationStatementGuard{
	// 011_message_user_attribution.sql: ALTER TABLE messages ADD COLUMN user_id …
	11: addColumnIfMissingGuard("messages", "user_id"),
	// 020_chat_limits.sql: ALTER TABLE events ADD COLUMN chat_id (the CREATE TABLE part is IF NOT EXISTS).
	20: addColumnIfMissingGuard("events", "chat_id"),
	// 021_task_status.sql: ALTER TABLE tasks ADD COLUMN status_message_id (nullable, no other statement).
	21: addColumnIfMissingGuard("tasks", "status_message_id"),
	// 023_chat_cost.sql: ALTER TABLE chat_limits ADD COLUMN daily_cost_limit_micros (one statement).
	23: addColumnIfMissingGuard("chat_limits", "daily_cost_limit_micros"),
}

// addColumnIfMissingGuard skips exactly one `ALTER TABLE <table> ADD COLUMN <column> …` statement when
// pragma_table_info says the column is already there. Other statements of the migration (the index of 011)
// still run. The comparison is deliberately narrow: only the three identifiers are matched, and the column
// name is checked as a whole token so `user_id_old` cannot satisfy it.
func addColumnIfMissingGuard(table, column string) migrationStatementGuard {
	prefix := strings.ToUpper("ALTER TABLE " + table + " ADD COLUMN " + column)
	return func(ctx context.Context, tx *sql.Tx, statement string) (bool, error) {
		normalized := strings.ToUpper(strings.Join(strings.Fields(statement), " "))
		if !strings.HasPrefix(normalized, prefix) {
			return false, nil
		}
		if len(normalized) > len(prefix) {
			next := normalized[len(prefix)]
			if next != ' ' && next != '\t' && next != '(' {
				return false, nil
			}
		}
		var present int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&present); err != nil {
			return false, err
		}
		return present > 0, nil
	}
}

// Statements — the split of a migration text into single statements. It is lexical, on purpose:
// the ';' inside `-- … ; …` comments of 001_init.sql must not cut anything, and the ';' of the
// semicolon separator inside one query). Quoting rules follow SQLite's tokenizer: ' " ` quote with
// the doubled character as the only escape, [...] quotes with no escape, -- runs to the end of the
// line, /* runs to */.
func Statements(sqlText string) ([]string, error) {
	var out []string
	var buf strings.Builder

	var lineComment, blockComment, quoted bool
	var quote byte

	for i := 0; i < len(sqlText); {
		c := sqlText[i]

		switch {
		case lineComment:
			buf.WriteByte(c)
			if c == '\n' {
				lineComment = false
			}
			i++
			continue
		case blockComment:
			buf.WriteByte(c)
			if c == '*' && i+1 < len(sqlText) && sqlText[i+1] == '/' {
				buf.WriteByte(sqlText[i+1])
				i += 2
				blockComment = false
				continue
			}
			i++
			continue
		case quoted:
			buf.WriteByte(c)
			if c == quote {
				// A doubled quote character escapes itself; [...] has no escape.
				if quote != ']' && i+1 < len(sqlText) && sqlText[i+1] == quote {
					buf.WriteByte(sqlText[i+1])
					i += 2
					continue
				}
				quoted = false
			}
			i++
			continue
		}

		switch c {
		case '-':
			if i+1 < len(sqlText) && sqlText[i+1] == '-' {
				lineComment = true
				buf.WriteString("--")
				i += 2
				continue
			}
			buf.WriteByte(c)
			i++
		case '/':
			if i+1 < len(sqlText) && sqlText[i+1] == '*' {
				blockComment = true
				buf.WriteString("/*")
				i += 2
				continue
			}
			buf.WriteByte(c)
			i++
		case '\'', '"', '`', '[':
			quoted = true
			quote = c
			if c == '[' {
				quote = ']'
			}
			buf.WriteByte(c)
			i++
		case ';':
			statement := strings.TrimSpace(buf.String())
			buf.Reset()
			i++
			if statement == "" {
				continue
			}
			if !hasCode(statement) {
				continue // a fragment of comments only is not a statement
			}
			out = append(out, statement)
		default:
			buf.WriteByte(c)
			i++
		}
	}

	if quoted {
		// An unterminated quote would reach the driver as one malformed statement; naming it here
		// is cheaper than a sqlite syntax error with no line number.
		return nil, fmt.Errorf("the migration text ends inside a quoted token ")
	}
	if trailing := strings.TrimSpace(buf.String()); trailing != "" && hasCode(trailing) {
		out = append(out, trailing)
	}
	return out, nil
}

// hasCode — whether a statement fragment holds anything but comments and whitespace. A migration
// that ends with a comment after the last ';' must not send a comment-only fragment to the driver.
func hasCode(fragment string) bool {
	return codeText(fragment) != ""
}

// codeText — the code of a fragment with comments removed and string literals replaced by a
// placeholder, so a ';' that lives in a comment or inside a literal is not mistaken for a statement
// separator. 001_init.sql has both: `… to the pool;` inside a comment and the ';' of
// `-- the Telegram message id; NULL for model answers`.
func codeText(fragment string) string {
	var rest strings.Builder
	var lineComment, blockComment, quoted bool
	var quote byte

	for i := 0; i < len(fragment); {
		c := fragment[i]
		switch {
		case lineComment:
			if c == '\n' {
				lineComment = false
			}
			i++
			continue
		case blockComment:
			if c == '*' && i+1 < len(fragment) && fragment[i+1] == '/' {
				i += 2
				blockComment = false
				continue
			}
			i++
			continue
		case quoted:
			if c == quote {
				if quote != ']' && i+1 < len(fragment) && fragment[i+1] == quote {
					i += 2
					continue
				}
				quoted = false
				rest.WriteByte('x') // the literal is replaced, not kept: its bytes are not code
			}
			i++
			continue
		}

		switch c {
		case '-':
			if i+1 < len(fragment) && fragment[i+1] == '-' {
				lineComment = true
				i += 2
				continue
			}
			rest.WriteByte(c)
			i++
		case '/':
			if i+1 < len(fragment) && fragment[i+1] == '*' {
				blockComment = true
				i += 2
				continue
			}
			rest.WriteByte(c)
			i++
		case '\'', '"', '`', '[':
			quoted = true
			quote = c
			if c == '[' {
				quote = ']'
			}
			i++
		default:
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				rest.WriteByte(c)
			}
			i++
		}
	}
	return rest.String()
}

// the log stays readable, and the texts of the model never enter it).
func firstLine(statement string) string {
	if i := strings.IndexByte(statement, '\n'); i >= 0 {
		return strings.TrimSpace(statement[:i])
	}
	return strings.TrimSpace(statement)
}
