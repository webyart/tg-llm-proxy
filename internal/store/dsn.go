//	`_pragma` from an untrusted source is arbitrary SQL — `_pragma=foreign_keys(1)%3BATTACH
//	'x.db' AS x` really runs both operators (the raw ';' is cut earlier by net/url, but %3B
//	caller add a key.
//	exists, the connection still opens, the pragma keeps its default), so a typo in a key name
//	is invisible both in the error and in the pragmas. The set is pinned as constants and
//	checked by TestDSNSetConsistency, not «by eye».
//
// with the path substituted:
//
//	file:/data/bot.db?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1&_synchronous=NORMAL&_txlock=immediate&_defensive=1&_dqs=0&_pragma=journal_size_limit(16777216)&_pragma=wal_autocheckpoint(1000)
//
// upgrade-deadlock (spikes 19/23/28), `_defensive=1&_dqs=0` refuses double-meaning constructions
// (ATTACH through %3B, a double quote used as a string literal), and the two `_pragma` values are
// the only way to reach journal_size_limit / wal_autocheckpoint from a DSN — no shorthand exists
// for them (`_journal_size_limit=262144` is not a key and leaves journal_size_limit=-1,
// spikes 25/28).
package store

import (
	"fmt"
	"net/url"
	"strings"
)

// sql.Open("sqlite", dsn), no C toolchain on this host).
const sqliteDriver = "sqlite"

// PRAGMA journal_mode).
const (
	JournalModePragma       = "journal_mode"
	ForeignKeysPragma       = "foreign_keys"
	BusyTimeoutPragma       = "busy_timeout"
	SynchronousPragma       = "synchronous"
	TxlockPragma            = "txlock"
	DefensivePragma         = "defensive"
	DQSPragma               = "dqs"
	JournalSizeLimitPragma  = "journal_size_limit"
	WalAutoCheckpointPragma = "wal_autocheckpoint"
)

// separate concern that never comes from DB_PATH.
const (
	RuntimeJournalMode       = "WAL"
	RuntimeBusyTimeout       = "5000"
	RuntimeForeignKeys       = "1"
	RuntimeSynchronous       = "NORMAL"
	RuntimeTxlock            = "immediate"
	RuntimeDefensive         = "1"
	RuntimeDQS               = "0"
	RuntimeJournalSizeLimit  = "16777216"
	RuntimeWalAutoCheckpoint = "1000"
)

// Key — one DSN query parameter BuildDSN is allowed to emit.
type Key struct {
	Name  string
	Value string
}

// the reference string (BuildDSN must produce it byte for byte). The order in the DSN does not
// busy_timeout and auto_vacuum are applied first whatever their position, then the `_pragma`
// values, then the rest of the shorthands, _query_only last.
var RuntimeShorthands = []Key{
	{"_" + JournalModePragma, RuntimeJournalMode},
	{"_" + BusyTimeoutPragma, RuntimeBusyTimeout},
	{"_" + ForeignKeysPragma, RuntimeForeignKeys},
	{"_" + SynchronousPragma, RuntimeSynchronous},
	{"_" + TxlockPragma, RuntimeTxlock},
	{"_" + DefensivePragma, RuntimeDefensive},
	{"_" + DQSPragma, RuntimeDQS},
}

// explicit that the whitelist is a closed list and not «_pragma is forbidden»: these two pragms
// have no DSN shorthand.
var RuntimePragmas = []Key{
	{"_pragma", JournalSizeLimitPragma + "(" + RuntimeJournalSizeLimit + ")"},
	{"_pragma", WalAutoCheckpointPragma + "(" + RuntimeWalAutoCheckpoint + ")"},
}

// «in-memory file::memory:?cache=shared&_journal_mode=WAL»).
func AllowedShorthands() []Key {
	out := make([]Key, 0, len(RuntimeShorthands))
	for _, k := range RuntimeShorthands {
		if k.Name == "_"+JournalModePragma {
			continue
		}
		out = append(out, k)
	}
	return out
}

// `_journal_mode`, `_busy_timeout`, `_foreign_keys`, `_synchronous` — shorthands the driver turns
// `_txlock`, `_defensive`, `_dqs` — driver/SQLite connection settings (BEGIN mode,
// sqlite3_db_config), NOT pragmas: `PRAGMA txlock` answers nothing, so they are verified through
// journal_size_limit / wal_autocheckpoint — pragms with NO shorthand. `_journal_size_limit=262144`
// which is exactly why they travel as the two pinned `_pragma=` statements.
var (
	// ReadablePragmas — pragma names a connection answers to `PRAGMA <name>`.
	ReadablePragmas = []string{
		JournalModePragma,
		ForeignKeysPragma,
		BusyTimeoutPragma,
		SynchronousPragma,
		JournalSizeLimitPragma,
		WalAutoCheckpointPragma,
	}

	DSNOnlyKeys = []string{
		"_" + TxlockPragma,
		"_" + DefensivePragma,
		"_" + DQSPragma,
	}

	shorthandByKey = map[string]Key{}
)

func init() {
	for _, k := range RuntimeShorthands {
		shorthandByKey[k.Name] = k
	}
}

// IsKey — whether a DSN key name is one of the pinned keys (the seven shorthands plus `_pragma`,
// whose value IsPragmaValue checks separately). A name outside this set would be ignored silently
// a typo is invisible in both the error and the pragmas — hence the closed set.
func IsKey(name string) bool {
	if name == "_pragma" {
		return true
	}
	_, ok := shorthandByKey[name]
	return ok
}

// IsPragma — whether a name is a pragma a connection answers to. The '_' form is accepted only when
// `journal_size_limit` is a real pragma, and `txlock`/`defensive`/`dqs` answer nothing as a PRAGMA.
func IsPragma(name string) bool {
	bare := strings.TrimSpace(name)
	if strings.HasPrefix(bare, "_") {
		key, ok := shorthandByKey[bare]
		if !ok {
			return false
		}
		bare = strings.TrimPrefix(key.Name, "_")
	}
	for _, pragma := range ReadablePragmas {
		if bare == pragma {
			return true
		}
	}
	return false
}

// «build-pragmaty typos»). The value must equal the pinned value of that exact DSN key; the two
// synchronous (NORMAL is 1 in the driver enum, and SQLite reports `PRAGMA synchronous` as the
// integer 1, measured on this host). Everything else is refused before the driver can swallow it:
// `_journal_mode=666` is rejected by the driver, but `_journal_size_limit=262144` is not rejected
func IsAllowed(name string, value string) bool {
	key := strings.TrimSpace(name)
	if key == "_pragma" {
		// The statement is compared exactly: the driver would accept a trailing comment, but the
		// whitelist promises one of two constants.
		return IsPragmaValue(value)
	}
	pinned, ok := shorthandByKey[key]
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case strings.ToLower(pinned.Value):
		return true
	case "memory":
		return key == "_"+JournalModePragma
	case "1":
		return key == "_"+SynchronousPragma
	default:
		return false
	}
}

// IsPragmaValue — a `_pragma=` statement against the two pinned constants. The comparison is
// against the constant, so nothing caller-shaped reaches the driver as SQL.
func IsPragmaValue(statement string) bool {
	for _, k := range RuntimePragmas {
		if statement == k.Value {
			return true
		}
	}
	return false
}

// Nothing here is escaped: the query is a compile-time constant, and escaping it would turn
// journal_size_limit(16777216) into percent noise that only the driver can read back.
func query(shorthands []Key) string {
	parts := make([]string, 0, len(shorthands)+len(RuntimePragmas))
	for _, k := range shorthands {
		parts = append(parts, k.Name+"="+k.Value)
	}
	for _, k := range RuntimePragmas {
		parts = append(parts, k.Name+"="+k.Value)
	}
	return strings.Join(parts, "&")
}

// the separator is normalised to '/' and url.PathEscape is applied to every path SEGMENT (escaping
// the whole string would escape the '/' of the separators too and `file:/data/bot.db` would lose the
// open the SAME file, so '\\' is treated as a separator on every platform: filepath.ToSlash would
// backslashes would survive as %5C in the file name. A space, `[brackets]`, `~`, кириллица, the ':'
// of a Windows drive and any stray URI sign reach SQLite percent-encoded (SQLite percent-decodes the
// URI path, spike 7).
func EscapePath(path string) string {
	slash := strings.ReplaceAll(path, "\\", "/")
	segments := strings.Split(slash, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// with `cache=shared` and never handed to the driver as a bare `:memory:`, which creates a FILE
// named ":memory:" — spike 5).
func IsMemory(path string) bool {
	return path == ":memory:"
}

// `file:` + EscapePath(filepath.ToSlash(path)) + "?" + the pinned query, and refuses:
//
//	(a) an empty path — `file:?…` opens nothing usable;
//	(b) a path holding ?, # or a percent sign — # truncates the name at the URI fragment (spike 8
//	    made the file `hash` out of `hash#tag.db`), a raw percent is decoded twice (`pct%20.db`
//	    became `pct .db`), and ? hijacks the query part where _pragma lives (`quest?ion.db` opens
//	    in no form at all);
//	(c) a `file:` prefix — then the caller owns the URI parameters (mode, cache, vfs) and the
//	(d) control bytes — SQLite receives the name as a C string.
//
// SQLITE_CANTOPEN with no file created at all, immutable/nolock break write and WAL, cache=private
// hides the shared schema).
func BuildDSN(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("store.BuildDSN: the database path is empty ( rule (a): an empty path cannot be normalised into file:)")
	}
	if strings.ContainsAny(path, "\x00\r\n\t") {
		return "", fmt.Errorf("store.BuildDSN: the database path %q contains control characters ", path)
	}
	if IsMemory(path) {
		return MemoryDSN("bot")
	}
	if strings.HasPrefix(strings.ToLower(path), "file:") {
		return "", fmt.Errorf("store.BuildDSN: the path %q starts with file: ( rule (c)): a file:-path hands the URI parameters mode, cache and vfs to the caller and bypasses the whitelist", path)
	}
	if strings.ContainsAny(path, "?#%") {
		return "", fmt.Errorf("store.BuildDSN: the path %q holds ?, # or a percent sign ", path)
	}
	return "file:" + EscapePath(path) + "?" + query(RuntimeShorthands), nil
}

// shorthands without journal_mode. cache=shared is obligatory: without it the pool loses the table
// cache=shared is dangerous between parallel tests of one package, everyone sees one database).
func MemoryDSN(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("store.MemoryDSN: the memory database needs a name, cache=shared would hand every test the same database ")
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return "", fmt.Errorf("store.MemoryDSN: the memory database name %q is not a bare identifier ", name)
	}
	return "file:mem" + name + "?mode=memory&cache=shared&" + query(AllowedShorthands()), nil
}
