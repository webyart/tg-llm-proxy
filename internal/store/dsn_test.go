// TestBuildDSNRejectsPragmaKey, TestBuildDSNPathForms, TestBuildDSNRejectsDangerousPath
package store

import (
	"context"
	"os"
	"strings"
	"testing"
)

const ReferenceDSN = "file:/data/bot.db?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=1&_synchronous=NORMAL&_txlock=immediate&_defensive=1&_dqs=0&_pragma=journal_size_limit(16777216)&_pragma=wal_autocheckpoint(1000)"

func TestBuildDSNReferenceDSN(t *testing.T) {
	got, err := BuildDSN("/data/bot.db")
	if err != nil {
		t.Fatalf("BuildDSN of the compose path failed: %v", err)
	}
	if got != ReferenceDSN {
		t.Fatalf("BuildDSN must emit the reference DSN of byte for byte\n got: %s\nwant: %s", got, ReferenceDSN)
	}
}

func TestBuildDSNWhitelist(t *testing.T) {
	got, err := BuildDSN("/data/bot.db")
	if err != nil {
		t.Fatal(err)
	}
	queryPart, ok := strings.CutPrefix(got, "file:/data/bot.db?")
	if !ok {
		t.Fatalf("the DSN is not file:<escaped path>?<query>: %s", got)
	}
	keys := strings.Split(queryPart, "&")
	if len(keys) != len(RuntimeShorthands)+len(RuntimePragmas) {
		t.Fatalf("the DSN carries %d keys, the whitelist has %d (%v)", len(keys), len(RuntimeShorthands)+len(RuntimePragmas), keys)
	}
	pragmas := 0
	for _, key := range keys {
		name, value, ok := strings.Cut(key, "=")
		if !ok {
			t.Fatalf("DSN key %q has no value", key)
		}
		if name == "_pragma" {
			pragmas++
			if !IsPragmaValue(value) {
				t.Fatalf("_pragma=%q is not one of the two pinned statements of", value)
			}
			continue
		}
		if !IsKey(name) {
			t.Fatalf("DSN key %q is outside the whitelist of — an unknown _key is ignored silently, so a typo here is invisible ", name)
		}
		if !IsAllowed(name, value) {
			t.Fatalf("DSN pair %s=%s is outside the whitelist of", name, value)
		}
	}
	if pragmas != len(RuntimePragmas) {
		t.Fatalf("%d _pragma keys, pins exactly %d", pragmas, len(RuntimePragmas))
	}
	// with no file at all), immutable/nolock break write and WAL, cache=private hides the schema,
	// and _auto_vacuum is a decision of the file header, not of a connection.
	for _, forbidden := range []string{"mode=ro", "immutable", "nolock", "cache=private", "_auto_vacuum", "_query_only", "_transaction_exclusive"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("the runtime DSN must never carry %q : %s", forbidden, got)
		}
	}
}

// ATTACHes an arbitrary file. BuildDSN must refuse the signs that could reach the query part, and
// anything it does accept must land in the escaped PATH, never in the query — the query is always
// exactly the pinned one, so no caller-shaped text can become SQL.
func TestBuildDSNRejectsPragmaKey(t *testing.T) {
	pinned := strings.TrimPrefix(ReferenceDSN, "file:/data/bot.db?")
	payloads := []string{
		"_pragma=foreign_keys(1)%3BATTACH 'x.db' AS x", // the case B payload: % is refused
		"db?_pragma=foreign_keys(1)",                   // ? would hijack the query part
		"db#_pragma=x(1)",                              // # is the URI fragment
		"_pragma=journal_mode(WAL)",                    // a legal FILE NAME: it must stay in the path
		"_bogus_key=1",
		"_journal_size_limit=262144",
	}
	for _, payload := range payloads {
		dsn, err := BuildDSN(payload)
		if err != nil {
			continue // refused outright — nothing reached the driver
		}
		pathPart, ok := strings.CutSuffix(dsn, "?"+pinned)
		if !ok {
			t.Fatalf("BuildDSN(%q) produced %q: the query is not the pinned one of", payload, dsn)
		}
		// Percent-escapes are expected here — they are the escaping, not an injection.
		if strings.ContainsAny(pathPart, "?#") {
			t.Fatalf("BuildDSN(%q) left a raw URI sign in the path (%q): the name is not escaped ", payload, pathPart)
		}
		if !strings.HasPrefix(pathPart, "file:") {
			t.Fatalf("BuildDSN(%q) did not normalise the path into file: form : %q", payload, pathPart)
		}
	}
	// The payload is not a pragma the whitelist knows either, so store.Pragma would refuse it too.
	for _, name := range []string{"_pragma", "_bogus_key", "_journal_size_limit"} {
		if IsPragma(name) {
			t.Fatalf("IsPragma(%q) = true, keeps this name outside the whitelist", name)
		}
	}
	// End-to-end: a payload-shaped NAME opens as an ordinary file, WAL intact, exactly one file.
	dir := t.TempDir()
	db, err := Open(context.Background(), dir+"/_pragma=journal_mode(WAL).db")
	if err != nil {
		t.Fatalf("Open of the payload-shaped name failed: %v", err)
	}
	mode, err := Pragma(context.Background(), db, JournalModePragma)
	db.Close()
	if err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q for the payload-shaped name, wants wal", mode)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the payload-shaped name produced %d entries, expects exactly the one database file", len(entries))
	}

	// so the directory of the database stays empty (ATTACH would have created y.db in it).
	guardDir := t.TempDir()
	if _, err := Open(context.Background(), guardDir+"/x.db?_pragma=foreign_keys(1)%3BATTACH 'y.db' AS y"); err == nil {
		t.Fatalf("Open accepted a payload that carries a query ")
	}
	guardEntries, err := os.ReadDir(guardDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(guardEntries) != 0 {
		t.Fatalf("the rejected payload left %d files in the database directory ", len(guardEntries))
	}
}

func TestBuildDSNRejectsDangerousPath(t *testing.T) {
	for _, path := range []string{
		"",
		"   ",
		"file:/data/bot.db",
		"FILE:/data/bot.db",
		"quest?ion.db",
		"hash#tag.db",
		"pct%20.db",
		"data/bot.db?_pragma=foreign_keys(1)",
		"bot.db\x00evil",
	} {
		if _, err := BuildDSN(path); err == nil {
			t.Fatalf("BuildDSN accepted the dangerous path %q ", path)
		}
	}
	if _, err := BuildDSN(":memory:"); err != nil {
		t.Fatalf(":memory: is a legal form : %v", err)
	}
}

// constants and the reference string must fail HERE, not in production.
func TestDSNSetConsistency(t *testing.T) {
	// No duplicate shorthand key: a duplicate would emit the same key twice and let the alias win.
	seen := map[string]bool{}
	for _, k := range RuntimeShorthands {
		if k.Name == "" || k.Value == "" {
			t.Fatalf("the pinned pair %q=%q has an empty side", k.Name, k.Value)
		}
		if !strings.HasPrefix(k.Name, "_") {
			t.Fatalf("the DSN key %q must carry the '_' prefix ", k.Name)
		}
		if seen[k.Name] {
			t.Fatalf("the DSN key %q is pinned twice", k.Name)
		}
		seen[k.Name] = true
	}

	// `_pragma` is the only repeated key, and only through the two pinned statements.
	pragmaStatements := map[string]bool{}
	for _, k := range RuntimePragmas {
		if k.Name != "_pragma" {
			t.Fatalf("RuntimePragmas carries %s, every entry of is a _pragma value", k.Name)
		}
		if pragmaStatements[k.Value] {
			t.Fatalf("the _pragma statement %q is pinned twice", k.Value)
		}
		pragmaStatements[k.Value] = true
		if strings.ContainsAny(k.Value, ";%") {
			t.Fatalf("the pinned _pragma %q carries a ';' or a '%%' ", k.Value)
		}
		if !strings.HasSuffix(k.Value, ")") || !strings.Contains(k.Value, "(") {
			t.Fatalf("the pinned _pragma %q is not the pragma(args) form of", k.Value)
		}
	}

	// The reference string and the constants describe the same set, key by key.
	keys := strings.Split(strings.TrimPrefix(ReferenceDSN, "file:/data/bot.db?"), "&")
	if len(keys) != len(RuntimeShorthands)+len(RuntimePragmas) {
		t.Fatalf("the reference DSN carries %d keys, the constants %d", len(keys), len(RuntimeShorthands)+len(RuntimePragmas))
	}
	for _, key := range keys {
		name, value, _ := strings.Cut(key, "=")
		if !IsKey(name) {
			t.Fatalf("the reference key %q is outside IsKey ", name)
		}
		if !IsAllowed(name, value) {
			t.Fatalf("the reference pair %s=%s is outside IsAllowed", name, value)
		}
	}

	// are connection settings, not PRAGMAs).
	for _, key := range DSNOnlyKeys {
		bare := strings.TrimPrefix(key, "_")
		for _, pragma := range ReadablePragmas {
			if bare == pragma {
				t.Fatalf("%s is listed as a DSN-only key and as a readable pragma at once ", key)
			}
		}
		if IsPragma(key) {
			t.Fatalf("IsPragma(%q) = true, says this key answers nothing as a PRAGMA", key)
		}
	}
	for _, pragma := range ReadablePragmas {
		if !IsPragma(pragma) {
			t.Fatalf("IsPragma(%q) = false, the list of readable pragms contradicts itself", pragma)
		}
		if strings.HasPrefix(pragma, "_") || pragma == "" {
			t.Fatalf("the readable pragma name %q must be the SQL spelling ", pragma)
		}
	}

	// exist in memory), and MemoryDSN emits exactly that set.
	memory := AllowedShorthands()
	if len(memory) != len(RuntimeShorthands)-1 {
		t.Fatalf("AllowedShorthands has %d keys, expects %d", len(memory), len(RuntimeShorthands)-1)
	}
	dsn, err := MemoryDSN("consistency")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dsn, "&"+query(memory)) {
		t.Fatalf("MemoryDSN did not emit AllowedShorthands: %s", dsn)
	}
	for _, k := range memory {
		if !strings.Contains(dsn, k.Name+"="+k.Value) {
			t.Fatalf("MemoryDSN lost the pinned pair %s=%s: %s", k.Name, k.Value, dsn)
		}
	}
}

// pragms a connection answers to. `_journal_size_limit` is not a key (spike 25: silently ignored,
// journal_size_limit stays -1) while `journal_size_limit` is a real pragma; `txlock`, `defensive`
// and `dqs` are connection settings that answer nothing as a PRAGMA.
func TestWhitelistLayers(t *testing.T) {
	for _, key := range []string{"_journal_mode", "_busy_timeout", "_foreign_keys", "_synchronous", "_txlock", "_defensive", "_dqs", "_pragma"} {
		if !IsKey(key) {
			t.Fatalf("IsKey(%q) = false, pins this key in the reference DSN", key)
		}
	}
	for _, notKey := range []string{"_journal_size_limit", "_wal_autocheckpoint", "_bogus_key", "_query_only", "journal_mode", ""} {
		if IsKey(notKey) {
			t.Fatalf("IsKey(%q) = true, the DSN whitelist of does not carry it", notKey)
		}
	}
	for _, pragma := range []string{"journal_mode", "foreign_keys", "busy_timeout", "synchronous", "journal_size_limit", "wal_autocheckpoint", "_journal_mode", "_busy_timeout"} {
		if !IsPragma(pragma) {
			t.Fatalf("IsPragma(%q) = false, a connection answers PRAGMA %s ", pragma, strings.TrimPrefix(pragma, "_"))
		}
	}
	for _, notPragma := range []string{"txlock", "defensive", "dqs", "_txlock", "_defensive", "_dqs", "_journal_size_limit", "_pragma", "journal_mode(WAL)"} {
		if IsPragma(notPragma) {
			t.Fatalf("IsPragma(%q) = true — this is not a PRAGMA a connection answers ", notPragma)
		}
	}
	allowed := []struct{ name, value string }{
		{"_journal_mode", "WAL"}, {"_journal_mode", "wal"}, {"_journal_mode", "memory"},
		{"_busy_timeout", "5000"}, {"_foreign_keys", "1"}, {"_synchronous", "NORMAL"}, {"_synchronous", "normal"}, {"_synchronous", "1"},
		{"_txlock", "immediate"}, {"_defensive", "1"}, {"_dqs", "0"},
		{"_pragma", "journal_size_limit(16777216)"}, {"_pragma", "wal_autocheckpoint(1000)"},
	}
	for _, pair := range allowed {
		if !IsAllowed(pair.name, pair.value) {
			t.Fatalf("IsAllowed(%q, %q) = false, the reference DSN of carries exactly this pair", pair.name, pair.value)
		}
	}
	refused := []struct{ name, value string }{
		{"_journal_mode", "666"}, {"_journal_mode", "delete"}, {"_foreign_keys", "0"}, {"_foreign_keys", "yes"},
		{"_busy_timeout", "0"}, {"_busy_timeout", "5001"}, {"_synchronous", "FULL"}, {"_txlock", "deferred"},
		{"_defensive", "0"}, {"_dqs", "1"}, {"_journal_size_limit", "16777216"}, {"journal_mode", "WAL"},
		{"_pragma", "journal_mode(WAL)"}, {"_pragma", "foreign_keys(1);ATTACH 'x.db' AS x"},
		{"_pragma", "journal_size_limit(16777216) "}, {"_pragma", ""},
	}
	for _, pair := range refused {
		if IsAllowed(pair.name, pair.value) {
			t.Fatalf("IsAllowed(%q, %q) = true, refuses it: %s", pair.name, pair.value, "the runtime DSN is a closed list of pinned pairs")
		}
	}
	if len(RuntimeShorthands) != 7 {
		t.Fatalf("the runtime DSN carries %d shorthands, pins 7", len(RuntimeShorthands))
	}
	memory := AllowedShorthands()
	if len(memory) != 6 {
		t.Fatalf("the memory DSN carries %d shorthands, pins 6 (no journal_mode)", len(memory))
	}
	for _, k := range memory {
		if k.Name == "_"+JournalModePragma {
			t.Fatalf("the memory DSN must not carry journal_mode ")
		}
		if !IsKey(k.Name) || !IsAllowed(k.Name, k.Value) {
			t.Fatalf("AllowedShorthands leaked %s=%s outside the whitelist", k.Name, k.Value)
		}
	}
	for _, key := range DSNOnlyKeys {
		if !IsKey(key) || IsPragma(key) {
			t.Fatalf("%s must be a DSN key that is not a PRAGMA ", key)
		}
	}
}

func TestMemoryDSN(t *testing.T) {
	dsn, err := MemoryDSN("dsn")
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{"file:memdsn?", "mode=memory", "cache=shared", "_foreign_keys=1", "_txlock=immediate", "_defensive=1", "_dqs=0", "_pragma=journal_size_limit(16777216)"} {
		if !strings.Contains(dsn, must) {
			t.Fatalf("MemoryDSN lost %q: %s", must, dsn)
		}
	}
	if strings.Contains(dsn, "_journal_mode") {
		t.Fatalf("the memory DSN carries journal_mode : %s", dsn)
	}
	for _, bad := range []string{"", "mem two", "a?b", "a#b", "x%y"} {
		if _, err := MemoryDSN(bad); err == nil {
			t.Fatalf("MemoryDSN(%q) accepted a name that would land in the URI path", bad)
		}
	}
}

func TestEscapePath(t *testing.T) {
	cases := map[string]string{
		"/data/bot.db":        "/data/bot.db",      // the reference shape keeps its separators
		`C:\dir\sub\bot.db`:   "C:/dir/sub/bot.db", // spike 7: both forms open the same file
		"dir with space/x.db": "dir%20with%20space/x.db",
		"база.db":             "%D0%B1%D0%B0%D0%B7%D0%B0.db",
		"[brackets]/x.db":     "%5Bbrackets%5D/x.db",
		"paren(x).db":         "paren%28x%29.db",
	}
	for in, want := range cases {
		if got := EscapePath(in); got != want {
			t.Fatalf("EscapePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// brackets, кириллица) opens ONE file after Open + Ping, in WAL.
func TestBuildDSNPathForms(t *testing.T) {
	// process and t.Chdir is allowed once per test.
	for _, form := range []string{"bot.db", `sub\bot.db`, "with space.db", "[brackets].db", "кириллица.db", "~tilde.db"} {
		dir := t.TempDir()
		path := dir + "/" + form
		if _, err := BuildDSN(path); err != nil {
			t.Fatalf("BuildDSN(%q) failed: %v", path, err)
		}
		db, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("Open of the path form %q failed: %v", form, err)
		}
		mode, err := Pragma(context.Background(), db, JournalModePragma)
		if err != nil {
			t.Fatalf("PRAGMA journal_mode of %q: %v", form, err)
		}
		if mode != "wal" {
			t.Fatalf("path form %q opened with journal_mode=%q, wants wal", form, mode)
		}
		db.Close()
	}
}

// the process, so the relative form is opened from a temporary directory and must land there.
func TestBuildDSNPathFormsRelative(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	db, err := Open(context.Background(), "./rel.db")
	if err != nil {
		t.Fatalf("Open of ./rel.db failed: %v", err)
	}
	defer db.Close()
	if _, err := os.Stat(dir + "/rel.db"); err != nil {
		t.Fatalf("the relative path did not land in the CWD : %v", err)
	}
}
