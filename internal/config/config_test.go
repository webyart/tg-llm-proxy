// config_test.go — the §11 "Unit" row for internal/config: the ENV → Config resolution,
// the validation messages that §10 День 1 DoD demands ("понятные сообщения об ошибках"),
// and above all the URL join of §7 — the row §11 names explicitly
// ("config-валидация (в т.ч. склейка URL base+/chat/completions)").
//
// The URL tests are the regression net for §16 change 26: the real endpoint of §14 item 5
// answers POST {base}/v1/chat/completions with 200 and POST {base}/chat/completions with 404,
// so a base written either way has to produce the ONE url the server actually serves, and
// never the "/v1/v1/..." double prefix of §16 change 8.

package config

import (
	"strings"
	"testing"
)

// TestNormalizeBaseShapes — every shape an operator can realistically write for
// STRATA_BASE_URL lands on exactly one "/v1": the bare host, the host with the
// suffix (§16 change 9 passes http://strata:8080/v1 from compose), the doubled
// suffix of the §16 change 8 bug, a trailing slash, and a proxied prefix.
func TestNormalizeBaseShapes(t *testing.T) {
	var cases = []struct {
		in   string
		want string
	}{
		{"http://host.docker.internal:8000", "http://host.docker.internal:8000/v1"},
		{"http://host.docker.internal:8000/", "http://host.docker.internal:8000/v1"},
		{"http://strata:8080/v1", "http://strata:8080/v1"},
		{"http://strata:8080/v1/", "http://strata:8080/v1"},
		{"http://strata:8080/v1/v1", "http://strata:8080/v1"},
		{"http://strata:8080/v1/v1/", "http://strata:8080/v1"},
		{"http://gateway.example.com/llm", "http://gateway.example.com/llm/v1"},
		{"http://gateway.example.com/llm/v1", "http://gateway.example.com/llm/v1"},
		{"  http://host:8000  ", "http://host:8000/v1"},
	}
	for _, c := range cases {
		var got = NormalizeBase(c.in)
		if got != c.want {
			t.Errorf("NormalizeBase(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "/v1/v1") {
			t.Errorf("NormalizeBase(%q) = %q keeps the doubled /v1 of §16 change 8", c.in, got)
		}
	}
}

// TestJoinURLPair — the two joins of §7 over one base: ChatURL ends with
// /chat/completions, ModelsURL ends with /models, and neither carries /v1/v1.
func TestJoinURLPair(t *testing.T) {
	var base = "http://strata:8080/v1"
	var chat = JoinURL(base, "/chat/completions")
	var models = JoinURL(base, "/models")
	if chat != "http://strata:8080/v1/chat/completions" {
		t.Errorf("ChatURL = %q, want http://strata:8080/v1/chat/completions", chat)
	}
	if models != "http://strata:8080/v1/models" {
		t.Errorf("ModelsURL = %q, want http://strata:8080/v1/models", models)
	}

	// The same pair from a base without the suffix: the two spellings of §7 must not
	// diverge, otherwise .env.example and compose would configure two different servers.
	var bare = JoinURL("http://strata:8080", "/chat/completions")
	if bare != chat {
		t.Errorf("the base without /v1 joins to %q, want the same endpoint %q", bare, chat)
	}
}

// TestLoadFromURLPair — the loader fills StrataBase/ChatURL/ModelsURL from one row, so a
// stale "/v1/v1" in the ENV cannot survive into the client (§16 change 8, §16 change 26).
func TestLoadFromURLPair(t *testing.T) {
	var cfg, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "123456:AA_fake_for_the_test",
		"STRATA_BASE_URL":    "http://strata:8080/v1/v1/",
	})
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
	if cfg.StrataBase != "http://strata:8080/v1" {
		t.Errorf("StrataBase = %q, want http://strata:8080/v1", cfg.StrataBase)
	}
	if cfg.ChatURL != "http://strata:8080/v1/chat/completions" {
		t.Errorf("ChatURL = %q", cfg.ChatURL)
	}
	if cfg.ModelsURL != "http://strata:8080/v1/models" {
		t.Errorf("ModelsURL = %q", cfg.ModelsURL)
	}
}

// TestLoadFromDefaults — an unset row keeps the §7 default instead of becoming a problem:
// §16 change 24 collects problems, and a default is not one.
func TestLoadFromDefaults(t *testing.T) {
	var cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "1:secret"})
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
	if cfg.StrataBase != DefaultBaseURL+V1Path {
		t.Errorf("StrataBase = %q, want the §7 default %q", cfg.StrataBase, DefaultBaseURL+V1Path)
	}
	if cfg.DBPath != DefaultDBPath {
		t.Errorf("DBPath = %q, want %q (§16 change 9: a local path for run.ps1)", cfg.DBPath, DefaultDBPath)
	}
	if cfg.TimeoutSeconds != DefaultTimeoutSeconds || cfg.ReplyDeadlineSeconds != DefaultDeadlineSeconds {
		t.Errorf("timeouts = %d/%d, want %d/%d", cfg.TimeoutSeconds, cfg.ReplyDeadlineSeconds,
			DefaultTimeoutSeconds, DefaultDeadlineSeconds)
	}
	if !cfg.DenyByDefault {
		t.Error("an empty TELEGRAM_ALLOWED_USERS must leave the bot closed (§16 change 13)")
	}
}

// TestLoadFromNamesEveryBrokenRow — §7.2 and §16 change 24: one run names every broken row
// and keeps the good values, so a single typo cannot blind the whole configuration.
func TestLoadFromNamesEveryBrokenRow(t *testing.T) {
	var raw = map[string]string{
		"STRATA_BASE_URL":           "http://strata:8080",
		"STRATA_TIMEOUT_SECONDS":    "not-a-number",
		"TG_REPLY_DEADLINE_SECONDS": "10",
		"MAX_MESSAGE_CHUNK":         "5000",
		"TELEGRAM_ALLOWED_USERS":    "11, oops, 22",
		"MAX_INPUT_TOKENS":          "4096", // a v1.0 row, §16 change 25.1
	}
	var cfg, problems = LoadFrom(raw)
	var want = []string{
		"TELEGRAM_BOT_TOKEN",
		"STRATA_TIMEOUT_SECONDS",
		"TG_REPLY_DEADLINE_SECONDS",
		"MAX_MESSAGE_CHUNK",
		"TELEGRAM_ALLOWED_USERS",
		"MAX_INPUT_TOKENS",
	}
	for _, name := range want {
		var found = false
		for _, p := range problems {
			if strings.Contains(p, name) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the problem list does not name %s: %v", name, problems)
		}
	}
	// The good rows stay: the base of §7 survives the unrelated typos.
	if cfg.StrataBase != "http://strata:8080/v1" {
		t.Errorf("StrataBase = %q, the good row must stay resolved", cfg.StrataBase)
	}
	// A row above the Telegram limit of 4096 SYMBOLS (§12 UX, §16 change 7) falls back
	// to the default instead of producing a message Telegram will reject.
	if cfg.MaxMessageChunk != DefaultMaxChunk {
		t.Errorf("MaxMessageChunk = %d, want the default %d", cfg.MaxMessageChunk, DefaultMaxChunk)
	}
	// The malformed entry of the allowlist is named, the numeric ones are kept.
	if len(cfg.UserIDs) != 2 {
		t.Errorf("UserIDs = %v, want the two good ids kept (§12 reliability)", cfg.UserIDs)
	}
}

// TestLoadFromDeadlineMustExceedTimeout — the §10 День 3 invariant: the total budget of
// retries + timeout + the answer into Telegram must be greater than the timeout of one attempt.
func TestLoadFromDeadlineMustExceedTimeout(t *testing.T) {
	var raw = map[string]string{
		"TELEGRAM_BOT_TOKEN":        "1:secret",
		"STRATA_TIMEOUT_SECONDS":    "60",
		"TG_REPLY_DEADLINE_SECONDS": "60",
	}
	var _, problems = LoadFrom(raw)
	var found = false
	for _, p := range problems {
		if strings.Contains(p, "is not greater than STRATA_TIMEOUT_SECONDS") {
			found = true
		}
	}
	if !found {
		t.Errorf("the invariant of §10 День 3 is not named: %v", problems)
	}
}

// TestLoadFromTokenShape — the token of BotFather has the form <digits>:<secret>
// (§14.1 item 1); a value without a colon is named instead of being passed to the API.
func TestLoadFromTokenShape(t *testing.T) {
	var _, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "nodotsnosecret"})
	var found = false
	for _, p := range problems {
		if strings.Contains(p, "TELEGRAM_BOT_TOKEN has no colon") {
			found = true
		}
	}
	if !found {
		t.Errorf("the shape of the token is not named: %v", problems)
	}
}

// TestRowOfTheAllowlistIsTelegramsName — §16 change 29: TELEGRAM_ALLOWED_USERS IS the row of §7, so a .env
// that carries the allowlist under this name configures the bot. Before the rename the loader knew only
// ALLOWED_USER_IDS, so a real user_id looked registered while the bot stayed closed (deny by default) — the
// failure §12 reliability hates most: a configuration that seems set and is not.
func TestRowOfTheAllowlistIsTelegramsName(t *testing.T) {
	var raw = map[string]string{
		"TELEGRAM_BOT_TOKEN":     "12345:test",
		"TELEGRAM_ALLOWED_USERS": "666244095",
	}
	var cfg, problems = LoadFrom(raw)
	if len(problems) > 0 {
		t.Fatalf("the row of §7 is not a problem (§16 change 29): %v", problems)
	}
	if cfg.DenyByDefault {
		t.Fatalf("DenyByDefault = true, want the bot open through its own row (§7, §16 change 29)")
	}
	if !cfg.Allows(666244095) {
		t.Errorf("Allows(666244095) = false, want true: the row filled nothing (§7 allowlist)")
	}
	if cfg.Allows(42) {
		t.Error("Allows(42) = true, want false: one id in the row must not open the whole world")
	}
	if !RowIsSet(raw, "TELEGRAM_ALLOWED_USERS") {
		t.Error("RowIsSet(TELEGRAM_ALLOWED_USERS) = false, want true: PrintMissing would lie about an unset row (§12 reliability)")
	}
	if !HasRow("TELEGRAM_ALLOWED_USERS") {
		t.Error("the name is not a row of Env, so the §7 table and the loader disagree (§10 День 1 DoD)")
	}
}

// TestOldNameOfTheRowIsRefusedByName — §16 change 29 removed ALLOWED_USER_IDS from the loader, and a row
// that left the §7 table must still be named when it carries a value: an old .env must not become a
// silently closed bot (§12 reliability). The value is not applied — one row, one spelling.
func TestOldNameOfTheRowIsRefusedByName(t *testing.T) {
	var raw = map[string]string{
		"TELEGRAM_BOT_TOKEN": "12345:test",
		"ALLOWED_USER_IDS":   "666244095",
	}
	var cfg, problems = LoadFrom(raw)
	var named = false
	var points = false
	var i = 0
	for i < len(problems) {
		if strings.Contains(problems[i], "ALLOWED_USER_IDS") {
			named = true
			if strings.Contains(problems[i], "TELEGRAM_ALLOWED_USERS") {
				points = true
			}
		}
		i += 1
	}
	if !named {
		t.Fatalf("the old name is not refused by name: %v (§12 reliability)", problems)
	}
	if !points {
		t.Errorf("the problem does not point at the name that replaced it: %v (§16 change 29)", problems)
	}
	if HasRow("ALLOWED_USER_IDS") {
		t.Error("ALLOWED_USER_IDS is still a row of Env — the rename left it in the table (§16 change 29)")
	}
	if !cfg.DenyByDefault || len(cfg.UserIDs) != 0 {
		t.Errorf("the value of a row that is not a row must not be applied: %v / DenyByDefault=%v (§7)", cfg.UserIDs, cfg.DenyByDefault)
	}
	if RowIsSet(raw, "ALLOWED_USER_IDS") && RowIsSet(raw, "TELEGRAM_ALLOWED_USERS") {
		t.Error("RowIsSet answered true for a name the loader does not read (§16 change 29)")
	}
}

// TestAllowlistCountsADuplicateOnce — the same id named twice inside one row is one entry: §12 security
// bounds the list, and `Allows` must not be able to count an id twice (§7 table).
func TestAllowlistCountsADuplicateOnce(t *testing.T) {
	var ids, note = UserIDs(map[string]string{"TELEGRAM_ALLOWED_USERS": "11,22,11"})
	if note != "" {
		t.Fatalf("UserIDs returned a problem for a repeated id: %q", note)
	}
	var want = []int64{11, 22}
	if len(ids) != len(want) {
		t.Fatalf("UserIDs = %v, want %v (the duplicate counted once)", ids, want)
	}
	var i = 0
	for i < len(want) {
		if ids[i] != want[i] {
			t.Errorf("UserIDs[%d] = %d, want %d (the order of the row is kept, §12 reliability)", i, ids[i], want[i])
		}
		i += 1
	}
}

// TestAllowlistNamesTheRowOfABadEntry — §12 reliability: a malformed id is named under the ENV name it was
// read from, otherwise the operator fixes the wrong line of .env.
func TestAllowlistNamesTheRowOfABadEntry(t *testing.T) {
	var ids, note = UserIDs(map[string]string{"TELEGRAM_ALLOWED_USERS": "666244095, oops"})
	if len(ids) != 1 || ids[0] != 666244095 {
		t.Errorf("UserIDs = %v, want the good entry kept (§12 reliability)", ids)
	}
	if !strings.Contains(note, "TELEGRAM_ALLOWED_USERS") {
		t.Errorf("the problem does not name the row it came from: %q", note)
	}
	if strings.Contains(note, "oops, oops") {
		t.Errorf("the bad entry is named twice: %q", note)
	}
}
