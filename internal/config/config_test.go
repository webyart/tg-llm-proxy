// ("config-валидация (в т.ч. склейка URL base+/chat/completions)").
//
// answers POST {base}/v1/chat/completions with 200 and POST {base}/chat/completions with 404,
// so a base written either way has to produce the ONE url the server actually serves, and

package config

import (
	"strings"
	"testing"
)

// TestNormalizeBaseShapes — every shape an operator can realistically write for
// STRATA_BASE_URL lands on exactly one "/v1": the bare host, the host with the
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
			t.Errorf("NormalizeBase(%q) = %q keeps the doubled /v1 of", c.in, got)
		}
	}
}

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

	// diverge, otherwise .env.example and compose would configure two different servers.
	var bare = JoinURL("http://strata:8080", "/chat/completions")
	if bare != chat {
		t.Errorf("the base without /v1 joins to %q, want the same endpoint %q", bare, chat)
	}
}

// TestLoadFromURLPair — the loader fills StrataBase/ChatURL/ModelsURL from one row, so a
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

func TestLoadFromDefaults(t *testing.T) {
	var cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "1:secret"})
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
	if cfg.StrataBase != DefaultBaseURL+V1Path {
		t.Errorf("StrataBase = %q, want the default %q", cfg.StrataBase, DefaultBaseURL+V1Path)
	}
	if cfg.DBPath != DefaultDBPath {
		t.Errorf("DBPath = %q, want %q ", cfg.DBPath, DefaultDBPath)
	}
	if cfg.ReminderTimezone != DefaultReminderTimezone || cfg.ReminderLocation == nil {
		t.Errorf("reminder timezone = %q/%v, want UTC", cfg.ReminderTimezone, cfg.ReminderLocation)
	}
	if cfg.TimeoutSeconds != DefaultTimeoutSeconds || cfg.ReplyDeadlineSeconds != DefaultDeadlineSeconds {
		t.Errorf("timeouts = %d/%d, want %d/%d", cfg.TimeoutSeconds, cfg.ReplyDeadlineSeconds,
			DefaultTimeoutSeconds, DefaultDeadlineSeconds)
	}
	if cfg.MediaMaxBytes != DefaultMediaMaxMB*1024*1024 {
		t.Errorf("MediaMaxBytes = %d, want %d", cfg.MediaMaxBytes, DefaultMediaMaxMB*1024*1024)
	}
	if cfg.SpeechMaxSeconds != DefaultSpeechMaxSeconds {
		t.Errorf("SpeechMaxSeconds = %d, want %d", cfg.SpeechMaxSeconds, DefaultSpeechMaxSeconds)
	}
	if !cfg.DenyByDefault {
		t.Error("an empty TELEGRAM_ALLOWED_USERS must leave the bot closed ")
	}
}

func TestLoadFromMediaLimits(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"MEDIA_MAX_MB":       "25",
		"SPEECH_MAX_SECONDS": "600",
	})
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
	if cfg.MediaMaxBytes != 25*1024*1024 {
		t.Errorf("MediaMaxBytes = %d, want %d", cfg.MediaMaxBytes, 25*1024*1024)
	}
	if cfg.SpeechMaxSeconds != 600 {
		t.Errorf("SpeechMaxSeconds = %d, want 600", cfg.SpeechMaxSeconds)
	}

	tests := []struct {
		name  string
		env   string
		value string
	}{
		{"media below minimum", "MEDIA_MAX_MB", "0"},
		{"media above maximum", "MEDIA_MAX_MB", "101"},
		{"media not an integer", "MEDIA_MAX_MB", "bad"},
		{"speech below minimum", "SPEECH_MAX_SECONDS", "0"},
		{"speech above maximum", "SPEECH_MAX_SECONDS", "3601"},
		{"speech not an integer", "SPEECH_MAX_SECONDS", "bad"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, problems := LoadFrom(map[string]string{
				"TELEGRAM_BOT_TOKEN": "1:secret",
				test.env:             test.value,
			})
			if len(problems) != 1 || !strings.Contains(problems[0], test.env) {
				t.Errorf("problems = %v, want one naming %s", problems, test.env)
			}
			if cfg.MediaMaxBytes != DefaultMediaMaxMB*1024*1024 {
				t.Errorf("MediaMaxBytes = %d, want default %d", cfg.MediaMaxBytes, DefaultMediaMaxMB*1024*1024)
			}
			if cfg.SpeechMaxSeconds != DefaultSpeechMaxSeconds {
				t.Errorf("SpeechMaxSeconds = %d, want default %d", cfg.SpeechMaxSeconds, DefaultSpeechMaxSeconds)
			}
		})
	}
}

// and keeps the good values, so a single typo cannot blind the whole configuration.
func TestLoadFromNamesEveryBrokenRow(t *testing.T) {
	var raw = map[string]string{
		"STRATA_BASE_URL":           "http://strata:8080",
		"STRATA_TIMEOUT_SECONDS":    "not-a-number",
		"TG_REPLY_DEADLINE_SECONDS": "10",
		"MAX_MESSAGE_CHUNK":         "5000",
		"TELEGRAM_ALLOWED_USERS":    "11, oops, 22",
		"MAX_INPUT_TOKENS":          "4096", // a v1.0 row,
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
	if cfg.StrataBase != "http://strata:8080/v1" {
		t.Errorf("StrataBase = %q, the good row must stay resolved", cfg.StrataBase)
	}
	// to the default instead of producing a message Telegram will reject.
	if cfg.MaxMessageChunk != DefaultMaxChunk {
		t.Errorf("MaxMessageChunk = %d, want the default %d", cfg.MaxMessageChunk, DefaultMaxChunk)
	}
	// The malformed entry of the allowlist is named, the numeric ones are kept.
	if len(cfg.UserIDs) != 2 {
		t.Errorf("UserIDs = %v, want the two good ids kept ", cfg.UserIDs)
	}
}

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
		t.Errorf("the invariant of is not named: %v", problems)
	}
}

// TestLoadFromTokenShape — the token of BotFather has the form <digits>:<secret>
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

// that carries the allowlist under this name configures the bot. Before the rename the loader knew only
// ALLOWED_USER_IDS, so a real user_id looked registered while the bot stayed closed (deny by default) — the
func TestRowOfTheAllowlistIsTelegramsName(t *testing.T) {
	var raw = map[string]string{
		"TELEGRAM_BOT_TOKEN":     "12345:test",
		"TELEGRAM_ALLOWED_USERS": "666244095",
	}
	var cfg, problems = LoadFrom(raw)
	if len(problems) > 0 {
		t.Fatalf("the row of is not a problem : %v", problems)
	}
	if cfg.DenyByDefault {
		t.Fatalf("DenyByDefault = true, want the bot open through its own row ")
	}
	if !cfg.Allows(666244095) {
		t.Errorf("Allows(666244095) = false, want true: the row filled nothing ")
	}
	if cfg.Allows(42) {
		t.Error("Allows(42) = true, want false: one id in the row must not open the whole world")
	}
	if !RowIsSet(raw, "TELEGRAM_ALLOWED_USERS") {
		t.Error("RowIsSet(TELEGRAM_ALLOWED_USERS) = false, want true: the row carries a value ")
	}
	if !HasRow("TELEGRAM_ALLOWED_USERS") {
		t.Error("the name is not a row of Env, so the table and the loader disagree ")
	}
}

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
		t.Fatalf("the old name is not refused by name: %v ", problems)
	}
	if !points {
		t.Errorf("the problem does not point at the name that replaced it: %v ", problems)
	}
	if HasRow("ALLOWED_USER_IDS") {
		t.Error("ALLOWED_USER_IDS is still a row of Env — the rename left it in the table ")
	}
	if !cfg.DenyByDefault || len(cfg.UserIDs) != 0 {
		t.Errorf("the value of a row that is not a row must not be applied: %v / DenyByDefault=%v ", cfg.UserIDs, cfg.DenyByDefault)
	}
	if RowIsSet(raw, "ALLOWED_USER_IDS") && RowIsSet(raw, "TELEGRAM_ALLOWED_USERS") {
		t.Error("RowIsSet answered true for a name the loader does not read ")
	}
}

func TestEnvIsTheRowsOfTheTable(t *testing.T) {
	if len(Env) != 77 {
		t.Errorf("Env holds %d names, want 77 including per-user daily limits, HEALTH_LISTEN_HOST/HEALTH_PROBE_PORT/METRICS_LISTEN_HOST, STREAM_DRAFTS_ENABLED, the four RAG_EMBEDDINGS_* rows, STRATA_FALLBACK*, STRATA_FALLBACKS and MODEL_PROVIDERS", len(Env))
	}
	var seen = map[string]bool{}
	var i = 0
	for i < len(Env) {
		if seen[Env[i]] {
			t.Errorf("Env names %s twice — the rename left both spellings in the table ", Env[i])
		}
		seen[Env[i]] = true
		i += 1
	}
	if !seen["TELEGRAM_ALLOWED_USERS"] {
		t.Error("TELEGRAM_ALLOWED_USERS is not in Env, so the allowlist of has no row ")
	}
	// name must not refuse them.
	for _, name := range []string{"STREAMING_ENABLED", "STREAM_EDIT_INTERVAL_MS", "STREAM_IDLE_TIMEOUT_SECONDS", "STREAM_TOTAL_DEADLINE_SECONDS", "MEDIA_MAX_MB", "SPEECH_MAX_SECONDS", "TTS_ENABLED", "TTS_MODEL", "TTS_VOICE", "TTS_MAX_CHARS", "TTS_TIMEOUT_SECONDS", "STRATA_FALLBACK_MODEL", "STRATA_FALLBACK_BASE_URL"} {
		if !seen[name] || !HasRow(name) {
			t.Errorf("%s is not a row of Env", name)
		}
	}
	if len(RenamedOrder) != 1 || RenamedRows[RenamedOrder[0]] != "TELEGRAM_ALLOWED_USERS" {
		t.Error("RenamedRows does not point the one old name at the row that replaced it ")
	}
}

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
			t.Errorf("UserIDs[%d] = %d, want %d ", i, ids[i], want[i])
		}
		i += 1
	}
}

// read from, otherwise the operator fixes the wrong line of .env.
func TestAllowlistNamesTheRowOfABadEntry(t *testing.T) {
	var ids, note = UserIDs(map[string]string{"TELEGRAM_ALLOWED_USERS": "666244095, oops"})
	if len(ids) != 1 || ids[0] != 666244095 {
		t.Errorf("UserIDs = %v, want the good entry kept ", ids)
	}
	if !strings.Contains(note, "TELEGRAM_ALLOWED_USERS") {
		t.Errorf("the problem does not name the row it came from: %q", note)
	}
	if strings.Contains(note, "oops, oops") {
		t.Errorf("the bad entry is named twice: %q", note)
	}
}

// bounds named in the problem when a row is out of them, and the two cross-row rules the table cannot leave
// open — a stall detector that outlives the answer it watches, and a reply budget shorter than the silence the
// stream is allowed to keep.
func TestStreamRowsOfFifteen(t *testing.T) {
	var cfg, problems = LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "123:abc"})
	if len(problems) != 0 {
		t.Fatalf("a plain configuration reported problems: %v", problems)
	}
	if cfg.Streaming != DefaultStreaming {
		t.Errorf("STREAMING_ENABLED = %v, want the flag off by default ", cfg.Streaming)
	}
	if cfg.StreamEditIntervalMS != DefaultStreamEditIntervalMS || cfg.StreamIdleSeconds != DefaultStreamIdleSeconds || cfg.StreamTotalSeconds != DefaultStreamTotalSeconds {
		t.Errorf("stream budgets = %d/%d/%d, want the defaults %d/%d/%d",
			cfg.StreamEditIntervalMS, cfg.StreamIdleSeconds, cfg.StreamTotalSeconds,
			DefaultStreamEditIntervalMS, DefaultStreamIdleSeconds, DefaultStreamTotalSeconds)
	}

	var bad, badProblems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":      "123:abc",
		"STREAM_EDIT_INTERVAL_MS": "100", "STREAM_IDLE_TIMEOUT_SECONDS": "0", "STREAM_TOTAL_DEADLINE_SECONDS": "99999",
	})
	if bad.StreamEditIntervalMS != DefaultStreamEditIntervalMS || bad.StreamIdleSeconds != DefaultStreamIdleSeconds || bad.StreamTotalSeconds != DefaultStreamTotalSeconds {
		t.Errorf("out-of-bounds stream rows were applied: %d/%d/%d ",
			bad.StreamEditIntervalMS, bad.StreamIdleSeconds, bad.StreamTotalSeconds)
	}
	if len(badProblems) != 3 {
		t.Errorf("three out-of-bounds rows reported %d problem(s): %v ", len(badProblems), badProblems)
	}

	var crossed, crossedProblems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":          "123:abc",
		"STREAM_IDLE_TIMEOUT_SECONDS": "400", "STREAM_TOTAL_DEADLINE_SECONDS": "300",
	})
	if !hasProblemNamed(crossedProblems, "STREAM_TOTAL_DEADLINE_SECONDS is not greater") {
		t.Errorf("idle 400s against a total of 300s reported: %v ", crossedProblems)
	}
	if crossed.StreamTotalSeconds != 300 || crossed.StreamIdleSeconds != 400 {
		t.Errorf("the cross-row rule changed values (%d/%d) instead of reporting the contradiction", crossed.StreamTotalSeconds, crossed.StreamIdleSeconds)
	}

	// 4–6 ms, so this rule fires on a wrong table, not on a slow model.
	var on, onProblems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"STREAMING_ENABLED":  "true", "STREAM_IDLE_TIMEOUT_SECONDS": "120", "TG_REPLY_DEADLINE_SECONDS": "90",
	})
	if !hasProblemNamed(onProblems, "TG_REPLY_DEADLINE_SECONDS is not greater than STREAM_IDLE_TIMEOUT_SECONDS") {
		t.Errorf("a stream that may stay silent 120s inside a 90s reply budget reported: %v ", onProblems)
	}
	if !on.Streaming {
		t.Error("STREAMING_ENABLED=true was not applied ")
	}
	// so a configuration that only pretends to stream is not a problem of the path it does not take.
	var off, offProblems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"STREAMING_ENABLED":  "false", "STREAM_IDLE_TIMEOUT_SECONDS": "120", "TG_REPLY_DEADLINE_SECONDS": "90",
	})
	if hasProblemNamed(offProblems, "STREAM_IDLE_TIMEOUT_SECONDS") {
		t.Errorf("the idle rule fired with the stream switched off: %v ", offProblems)
	}
	if off.Streaming {
		t.Error("STREAMING_ENABLED=false must leave the plain path of ")
	}
}

// EVERY broken row into one list, so a test asserts on the whole list it got and not on its first item.
func hasProblemNamed(problems []string, phrase string) bool {
	var i = 0
	for i < len(problems) {
		if strings.Contains(problems[i], phrase) {
			return true
		}
		i += 1
	}
	return false
}

// TestLoadFromFallbackChain — STRATA_FALLBACKS is an ordered JSON list; a reserve without base_url shares the
// primary endpoint, and the per-entry timeout defaults to the single-reserve default.
func TestLoadFromFallbackChain(t *testing.T) {
	var cfg, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN": "1:secret",
		"STRATA_FALLBACKS":   `[{"model":"kimi-k3.1-pro","base_url":"https://api.moonshot.ai/v1","api_key":"sk-test","timeout_seconds":90},{"model":"local-reserve"}]`,
	})
	if len(problems) > 0 {
		t.Fatalf("LOAD PROBLEMS: %v", problems)
	}
	if len(cfg.FallbackProviders) != 2 {
		t.Fatalf("FallbackProviders = %d, want 2", len(cfg.FallbackProviders))
	}
	var first = cfg.FallbackProviders[0]
	if first.Model != "kimi-k3.1-pro" || first.APIKey != "sk-test" || first.TimeoutSeconds != 90 {
		t.Fatalf("first provider = %+v", first)
	}
	if !strings.HasSuffix(first.ChatURL, "/chat/completions") || !strings.HasPrefix(first.ChatURL, "https://api.moonshot.ai/v1") {
		t.Fatalf("first ChatURL = %q", first.ChatURL)
	}
	var second = cfg.FallbackProviders[1]
	if second.Model != "local-reserve" || second.ChatURL != cfg.ChatURL || second.TimeoutSeconds != DefaultFallbackTimeoutSeconds {
		t.Fatalf("second provider = %+v (primary ChatURL %q)", second, cfg.ChatURL)
	}
}

func TestLoadFromFallbackChainRejectsConflictsAndBadJSON(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"both rows": {"STRATA_FALLBACK_MODEL": "old", "STRATA_FALLBACKS": `[{"model":"new"}]`},
		"bad json":  {"STRATA_FALLBACKS": `[{"model":`},
		"empty":     {"STRATA_FALLBACKS": `[]`},
		"no model":  {"STRATA_FALLBACKS": `[{"base_url":"https://example.test/v1"}]`},
		"bad url":   {"STRATA_FALLBACKS": `[{"model":"m","base_url":"not-a-url"}]`},
		"timeout":   {"STRATA_FALLBACKS": `[{"model":"m","timeout_seconds":1}]`},
	} {
		env["TELEGRAM_BOT_TOKEN"] = "1:secret"
		var _, problems = LoadFrom(env)
		if len(problems) == 0 {
			t.Errorf("%s: no problems reported", name)
			continue
		}
		var joined = strings.Join(problems, " ")
		if !strings.Contains(joined, "STRATA_FALLBACKS") {
			t.Errorf("%s: problems do not name STRATA_FALLBACKS: %v", name, problems)
		}
	}
}

func TestLoadFromTaskBudgets(t *testing.T) {
	cfg, problems := LoadFrom(map[string]string{"TELEGRAM_BOT_TOKEN": "1:secret"})
	if len(problems) > 0 {
		t.Fatalf("defaults: %v", problems)
	}
	if cfg.TaskModelTimeoutSeconds != DefaultTaskModelTimeoutSeconds || cfg.TaskStepDeadlineSeconds != DefaultTaskStepDeadlineSeconds {
		t.Fatalf("task budgets = %d/%d, want %d/%d", cfg.TaskModelTimeoutSeconds, cfg.TaskStepDeadlineSeconds,
			DefaultTaskModelTimeoutSeconds, DefaultTaskStepDeadlineSeconds)
	}
	_, problems = LoadFrom(map[string]string{
		"TELEGRAM_BOT_TOKEN":         "1:secret",
		"TASK_MODEL_TIMEOUT_SECONDS": "300",
		"TASK_STEP_DEADLINE_SECONDS": "300",
	})
	var joined = strings.Join(problems, " ")
	if !strings.Contains(joined, "TASK_MODEL_TIMEOUT_SECONDS") {
		t.Fatalf("equal task budgets were accepted: %v", problems)
	}
}
