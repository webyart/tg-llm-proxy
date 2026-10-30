// store.go — the SQLite layer of §10 День 2: store.Open with the reference DSN of §5 through the
// whitelist of dsn.go, the `sql.Open → db.Ping() → PRAGMA journal_mode/foreign_keys` sequence that
// §5 demands, the connection pool of §11 and the schema of §5 applied on every start.
//
// The two measured facts of §5 that shape Open:
//  1. `sql.Open` says nothing about a bad DSN — the validation lives in the driver's connect, so
//     the error surfaces on the first `db.Ping()` (§5 spike 11: `_journal_mode=666` opens without
//     an error and fails on every query of that connection afterwards). Open therefore Pings
//     immediately and reads the two pragms back; a typo would otherwise reach the first handled
//     update and repeat on every query.
//  2. `foreign_keys` is per-connection and `journal_mode` is stored in the file itself, so
//     neither can be set from inside the migration (§5 «Прагмы здесь намеренно отсутствуют»):
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

// The pool of §11: MaxOpenConns(4) and MaxIdleConns(2). Idle must never be 0 — with 0 the pool
// closes the only connection on returning it and a shared-cache memory database disappears
// (`no such table`, §5 spike 9). The numbers are constants rather than ENV rows: §7 has no row
// for the pool, and §11 fixes this shape.
const (
	MaxOpenConns = 4
	MaxIdleConns = 2
)

// EnableStrictPragmas turns on sqlite.StrictPragmas — the second line of defence behind the
// whitelist (§5: it is process-wide, off by default, NOT a DSN parameter, and the driver copies it
// into every connection at Connect, so it has to be set in main BEFORE the first sql.Open). It
// only rejects a `_pragma` value that is more than one SQL statement, which is exactly the %3B
// ATTACH case of §5; it does not replace the whitelist. Returns the previous setting.
func EnableStrictPragmas() bool {
	return sqlite.StrictPragmas(true)
}

// StrictPragmasEnabled reports the process-wide switch (§5: the driver exposes it for the tests).
func StrictPragmasEnabled() bool {
	return sqlite.StrictPragmasEnabled()
}

// Open — the §10 День 2 sequence: BuildDSN → sql.Open → the pool → db.PingContext → the two
// pragms → the schema. It returns a ready *sql.DB or an error that quotes the DSN, because a
// rejected DSN is a configuration problem and the path is the thing the operator has to fix
// (§5: «падать на старте с текстом DSN»). The database path carries no secret, only DB_PATH (§7).
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

// OpenMemory — the §11 test pool of §5: `file:mem<name>?mode=memory&cache=shared` through the same
// whitelist, the same Ping and the same schema. The name has to be unique per test (§5: cache=shared
// hands one database to everyone in the process, so parallel tests of one package collide), which is
// why Open(path) alone cannot cover the Store level of §11.
func OpenMemory(ctx context.Context, name string) (*sql.DB, error) {
	dsn, err := MemoryDSN(name)
	if err != nil {
		return nil, err
	}
	return openDSN(ctx, dsn, name, true)
}

// openDSN — the §10 День 2 sequence: sql.Open → the pool of §11 → db.PingContext → the two pragms →
// the schema of §5. The error quotes the DSN, because a rejected DSN is a configuration problem and
// the string is what the operator has to fix (§5: «падать на старте с текстом DSN»). A database path
// carries no secret (§7 lists DB_PATH as not a secret).
func openDSN(ctx context.Context, dsn, describe string, memory bool) (*sql.DB, error) {
	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("store.Open: sql.Open rejected the DSN %q (%w) — §5: the driver validates the DSN in connect, so this is normally silence here and an error on the first Ping", dsn, err)
	}
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)

	// §5: the earliest point a bad DSN can surface. Failing here keeps the error out of the first
	// handled update, where it would repeat on every query of the connection.
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: the first Ping rejected the DSN %q (%w) — §5 spike 11: sql.Open stays silent, the file may already exist but it is NOT in WAL (§10 День 2 DoD)", dsn, err)
	}

	if err := verifyPragmas(ctx, db, memory); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: the DSN %q did not produce the pragmas of §5 (%w)", dsn, err)
	}

	if err := ApplySchema(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: the schema of §5 could not be applied to %q (%w)", describe, err)
	}
	return db, nil
}

// prepareParent — SQLite does not create a missing parent directory: it answers SQLITE_CANTOPEN
// and leaves no file (§5 spike 5: an unparsable/absent location is «the file was not found»). The
// §7 local default is ./data/bot.db and data/ is not tracked by git (§4), so the local run.ps1 path
// needs this; compose hands bot-data:/data as a named volume (§8.1), where MkdirAll is a no-op.
func prepareParent(path string) error {
	if IsMemory(path) {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store.Open: the directory %q of the database cannot be created (%w) — §8.1: in compose the named volume bot-data:/data provides it", dir, err)
	}
	return nil
}

// verifyPragmas — §5: Open checks what the database answers, not what the DSN claimed. A file
// database must report wal; a shared-cache memory database reports memory (§5 table row: WAL is
// silently unavailable in memory, which is why WAL and busy_timeout are tested on a file only).
// foreign_keys must be 1 on this connection, because §5 says one Exec in the migration does not
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
		return fmt.Errorf("PRAGMA journal_mode = %q, §5 expects %q for this database form", mode, want)
	}

	keys, err := Pragma(ctx, db, ForeignKeysPragma)
	if err != nil {
		return fmt.Errorf("PRAGMA %s could not be read: %w", ForeignKeysPragma, err)
	}
	if !IsAllowed("_"+ForeignKeysPragma, keys) {
		return fmt.Errorf("PRAGMA foreign_keys = %q, §5 needs 1: without it REFERENCES of §5 are not enforced and the guard uq_messages_chat_tg_id has no parent check", keys)
	}
	return nil
}

// Pragma — one pragma of the whitelist read back as text (`PRAGMA journal_mode` → "wal",
// `PRAGMA foreign_keys` → 1). The DSN spelling of the name (`_journal_mode`) is accepted for
// convenience and cut: `PRAGMA _journal_mode` answers nothing, because the '_' prefix belongs to the
// DSN key, not to SQL (§5 spike 11 versus spike 17). The name goes through the whitelist first so a
// typo cannot turn into a silent empty result (§5: an unknown pragma name is not an error, it is
// nothing — which is exactly why a typo must be refused before the query).
func Pragma(ctx context.Context, db *sql.DB, name string) (string, error) {
	if !IsPragma(name) {
		return "", fmt.Errorf("store.Pragma: %q is not in the whitelist of §5, refusing to ask the database about it", name)
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
		// TEXT (§5 spike 17). Scanning into any keeps both readable without a second code path.
		value = fmt.Sprintf("%v", v)
	}
	return value, nil
}

// ApplySchema — the migration set of §5 on every start (§10 День 2: «репозитории … применяются на
// каждом старте»). The statements go one by one: the sqlite driver refuses a ';' inside a single
// query (§5: `invalid semicolon separator in query`), so the schema text cannot be Execed whole.
// Every clause of 001_init.sql carries IF NOT EXISTS, so a repeated start is a no-op (§5: одна
// идемпотентная миграция).
func ApplySchema(ctx context.Context, db *sql.DB) error {
	for _, migration := range MigrationSQL {
		statements, err := Statements(migration)
		if err != nil {
			return err
		}
		for i, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("statement %d of the migration of §5 failed (%w); text: %s", i+1, err, firstLine(statement))
			}
		}
	}
	return nil
}

// Statements — the split of a migration text into single statements. It is lexical, on purpose:
// the ';' inside `-- … ; …` comments of 001_init.sql must not cut anything, and the ';' of the
// CHECK clause would be a cut if string literals were not tracked (§5: the driver rejects a
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
		return nil, fmt.Errorf("the migration text ends inside a quoted token (§5: the tokenizer would swallow the rest as one statement)")
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

// firstLine — the first line of a statement for the error text: the schema of §5 is short, but a
// full statement in an error message would push the whole CREATE TABLE into the log (§12 security:
// the log stays readable, and the texts of the model never enter it).
func firstLine(statement string) string {
	if i := strings.IndexByte(statement, '\n'); i >= 0 {
		return strings.TrimSpace(statement[:i])
	}
	return strings.TrimSpace(statement)
}
