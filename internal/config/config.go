// Config — the configuration of the proxy. One field per row of the §7 table
// "Конфигурация (ENV)", so the loader and the plan cannot diverge any more (§10
// День 1: "internal/config: ENV → Config, validation, understandable messages
// about errors"; §16 change 10 lists exactly the rows that plan v1.1 added to §7).
// Every value comes from the process ENV — the config file is gone (§12 security:
// the token is only in the ENV, ".env" is listed in ".gitignore").

package config
import (
 "os"
 "strconv"
 "strings"
 "fmt"
)



// Env — the ENV names of the §7 table in table order. The loader reads the whole
// set once and names every unset variable, not only the first one (§7 behavior:
// the absence of TELEGRAM_BOT_TOKEN → a fall on the start with a clear error;
// §10 День 1 DoD: "go build passes; with an empty token — a clear error and the
// code of exits 1").
var Env = []string{
		"TELEGRAM_BOT_TOKEN",
		"STRATA_BASE_URL",
		"STRATA_MODEL",
		"STRATA_API_KEY",
		"STRATA_TIMEOUT_SECONDS",
		"TG_REPLY_DEADLINE_SECONDS",
		"DB_PATH",
		"CONTEXT_MESSAGES",
		"MAX_CONTEXT_CHARS",
		"MAX_INPUT_CHARS",
		"SYSTEM_PROMPT",
		"ALLOWED_USER_IDS",
		"RATE_LIMIT_PER_MINUTE",
		"MAX_CONCURRENT_MODEL_REQUESTS",
		"PROXY_FREE_TEXT",
		"MAX_MESSAGE_CHUNK",
		"PARSE_MODE",
		"HEALTH_PORT",
		"LOG_LEVEL",
		}

// The defaults of the §7 table. §16 change 8 keeps STRATA_BASE_URL without the
// "/v1" suffix: the §7 URL rule stores the base as it is and the client joins
// the endpoint paths itself, a stored suffix would give "/v1/v1/chat/completions".
// §16 change 9 makes DB_PATH default to the local "./data/bot.db", because the
// compose default "/data/bot.db" is not a valid path for run.ps1 on Windows.
const DefaultBaseURL = "http://host.docker.internal:8000"
const DefaultDBPath = "./data/bot.db"
const DefaultSystemPrompt = "You are a helpful assistant."
const DefaultTimeoutSeconds = int64(60)
const DefaultDeadlineSeconds = int64(90)
const DefaultContextMessages = int64(20)
const DefaultMaxContextChars = int64(8000)
const DefaultMaxInputChars = int64(4000)
const DefaultRateLimit = int64(20)
const DefaultConcurrent = int64(2)
const DefaultMaxChunk = int64(3500)
const DefaultParseMode = "MarkdownV2"
const DefaultHealthPort = int64(8080)
const DefaultLogLevel = "info"

// The bounds that the plan fixes by name. §12 UX: the reply limit of Telegram is
// 4096 SYMBOLS, so MAX_MESSAGE_CHUNK cannot pass it (§16 change 7: кириллица costs
// two bytes, therefore the cut goes by runes and never by bytes). §16 change 11
// gives HEALTH_PORT to the new module internal/health (GET /healthz, the HEALTHCHECK
// of compose and smoke.ps1). §12 security keeps ALLOWED_USER_IDS a bounded list.
const ChunkMax = int64(4096)
const PortMax = int64(65535)
const TGBigID = int64(9223372036854)
const UserIDsMax = 1024

// Config — one resolved configuration of the §7 table. The field names follow the
// rows of §7 (BotToken, StrataKey, ..., LogLevel), so a rename can never happen
// again: the loader fills exactly these fields and §10 День 1 DoD echoes them back.
// ChatURL and ModelsURL are not rows of §7 — the §7 URL rule joins them from
// StrataBase (§16 change 8: the base is stored without the "/v1" suffix). BotToken
// and StrataKey are secrets: §12 security forbids logging them and demands the
// leak check before a commit (§14.1 item 1: the token of BotFather never enters
// the chat or a commit).
type Config struct {
		BotToken string
		StrataKey string
		StrataBase string
		ChatURL string
		ModelsURL string
		StrataModel string
		TimeoutSeconds int64
		ReplyDeadlineSeconds int64
		DBPath string
		ContextMessages int64
		MaxContextChars int64
		MaxInputChars int64
		SystemPrompt string
		UserIDs []int64
		DenyByDefault bool
		RateLimitPerMinute int64
		MaxConcurrentRequests int64
		FreeText bool
		MaxMessageChunk int64
		ParseMode string
		HealthPort int64
		LogLevel string
		}


// CutBase — the §7 rule "STRATA_BASE_URL is stored WITHOUT the /v1 suffix"
// (§16 change 8: joining a base that already carries /v1 gives the path
// "/v1/v1/chat/completions"; §16 change 9: compose passes
// http://strata:8080/v1). The cut here makes the stored value and the joined
// URLs agree with the §7 table, and scripts/strata-check.ps1 repeats the rule.
func CutBase(base string) string {
 var cut = strings.TrimSuffix(base, "/")
 cut = strings.TrimSuffix(cut, "/v1")
 cut = strings.TrimSuffix(cut, "/")
 return cut
}

// JoinURL — the URL joins of §7: ChatCompletions = Join(base,
// "/chat/completions") and ModelsURL = Join(base, "/models") (§16 change 8).
func JoinURL(base string, path string) string {
 return CutBase(base) + path
}

// Num — one integer row of the §7 table: the default when the variable is not
// set, a clear problem when the value is not an integer or falls outside the
// bounds named for that row (§10 День 1: "internal/config: ENV → Config,
// validation, понятные сообщения об ошибках"). The message carries the ENV
// name and the bounds; it never carries a secret (§12 security).
func Num(raw map[string]string, name string, def int64, min int64, max int64) (int64, string) {
 var text = strings.TrimSpace(raw[name])
 if text == "" {
  return def, ""
 }
 var value, err = strconv.ParseInt(text, 10, 64)
 if err != nil {
  return def, fmt.Sprintf("ENV %s is not an integer (§7 table), the row must be between %d and %d (§10 День 1 validation)", name, min, max)
 }
 if value < min {
  return def, fmt.Sprintf("ENV %s = %d is below the bound %d (§7 table), the default %d is used instead", name, value, min, def)
 }
 if value > max {
  return def, fmt.Sprintf("ENV %s = %d is above the bound %d (§7 table), the default %d is used instead", name, value, max, def)
 }
 return value, ""
}

// Flag — a bool row of the §7 table (§7 has PROXY_FREE_TEXT=true). An unset
// row keeps the default; an unparseable value says so instead of silently
// flipping the switch (§10 День 1 validation).
func Flag(raw map[string]string, name string, def bool) (bool, string) {
 var text = strings.TrimSpace(raw[name])
 if text == "" {
  return def, ""
 }
 var value, err = strconv.ParseBool(text)
 if err != nil {
  return def, fmt.Sprintf("ENV %s is neither true nor false (§7 table), the default %s is used", name, strconv.FormatBool(def))
 }
 return value, ""
}

// Mode — the PARSE_MODE row of the §7 table: only MarkdownV2 | HTML | plain.
// The row decides how the answer is escaped BEFORE it is cut, because the
// escaping marks of MarkdownV2 change the length of the answer (§16 change 7).
func Mode(raw map[string]string) (string, string) {
 var text = strings.TrimSpace(raw["PARSE_MODE"])
 if text == "" {
  return DefaultParseMode, ""
 }
 if strings.EqualFold(text, DefaultParseMode) {
  return DefaultParseMode, ""
 }
 if strings.EqualFold(text, "HTML") {
  return "HTML", ""
 }
 if strings.EqualFold(text, "plain") {
  return "plain", ""
 }
 return DefaultParseMode, fmt.Sprintf("ENV PARSE_MODE is not one of MarkdownV2 | HTML | plain (§7 table), %s is used", DefaultParseMode)
}

// Level — the LOG_LEVEL row of the §7 table: the four levels of the §12 logging
// layer. §12 security keeps the token and the full texts of the model out of
// the logs at LOG_LEVEL=info, so the default is the quiet one.
func Level(raw map[string]string) (string, string) {
 var text = strings.ToLower(strings.TrimSpace(raw["LOG_LEVEL"]))
 if text == "" {
  return DefaultLogLevel, ""
 }
 if text == "error" || text == "warning" || text == "info" || text == "debug" {
  return text, ""
 }
 return DefaultLogLevel, fmt.Sprintf("ENV LOG_LEVEL is not one of error | warning | info | debug (§12 logging), %s is used", DefaultLogLevel)
}




// Push — collect one problem into the list of problems of one load. An empty
// message means "this row is fine" and adds nothing. §12 reliability: a wrong
// row must not hide the other rows, so the loader keeps going and returns the
// whole list at once (§10 День 1: "understandable messages about errors").
func Push(problems []string, msg string) []string {
 if msg == "" {
  return problems
 }
 return append(problems, msg)
}

// UserIDs — the ALLOWED_USER_IDS row: the chat IDs through a comma (§7 table).
// An empty row means the closed bot, deny by default: it answers only /start
// and /whoami and writes events(kind='denied') (§7 behavior, §16 change 13 —
// otherwise a fresh user could not learn his own id and enter the allowlist).
// A malformed entry is named, the good entries are still kept (§12 reliability).
func UserIDs(raw map[string]string) ([]int64, string) {
 var ids = []int64{}
 var bad = []string{}
 var text = strings.TrimSpace(raw["ALLOWED_USER_IDS"])
 if text == "" {
  return ids, ""
 }
 var parts = strings.Split(text, ",")
 var i = 0
 for i < len(parts) {
  var part = strings.TrimSpace(parts[i])
  if part != "" {
   var value, err = strconv.ParseInt(part, 10, 64)
   if err != nil || value <= 0 {
    bad = Push(bad, fmt.Sprintf("the entry %s of ENV ALLOWED_USER_IDS is not a positive chat id (§7 table)", part))
   } else if len(ids) < UserIDsMax {
    ids = append(ids, value)
   } else {
    bad = Push(bad, fmt.Sprintf("ENV ALLOWED_USER_IDS is longer than %d entries (§12 security keeps the allowlist bounded)", UserIDsMax))
   }
  }
  i += 1
 }
 if len(bad) == 0 {
  return ids, ""
 }
 return ids, fmt.Sprintf("ENV ALLOWED_USER_IDS has %d malformed entries: %s; the good entries are kept (§12 reliability)", len(bad), strings.Join(bad, "; "))
}

// DefaultStrataModel — the STRATA_MODEL row of the §7 table. §14.2 item 6 still
// asks which model the host really has (its context limit decides the budget of
// CONTEXT_MESSAGES and MAX_CONTEXT_CHARS), so the row keeps the plan default.
const DefaultStrataModel = "qwen2.5-7b-instruct"
// LegacyRows — the ENV names that the v1.0 §7 promised and that the §7 table of
// v1.1 does not have (§16 change 25.1: they moved to the §15 backlog together with
// the token budgets, the deep link and the summary/embedding models). The loader
// refuses such a row by name instead of ignoring it, because a leftover row in .env
// or in compose otherwise looks like a working configuration and hides the fact that
// the feature does not exist in v1.1 (§10 День 1: "understandable messages about
// errors"; §7: the loader reads exactly the 19 rows of the table).
var LegacyRows = []string{
		"LLM_CHAT_MODEL",
		"LLM_CHAT_COMPLETIONS_URL",
		"LLM_SUMMARY_MODEL",
		"LLM_EMBEDDING_MODEL",
		"LLM_CHAT_LONGTERM",
		"LLM_LONGTERM_MODEL_URL",
		"LLM_CHAT_MODE",
		"LLM_CHAT_DEEP_LINK",
		"LLM_CHAT_DEEP_LINK_DEFAULT",
		"LLM_CHAT_DEEP_LINK_TTL",
		"LLM_CHAT_DEEP_LINK_TIMEOUT",
		"LLM_CHAT_DEEP_LINK_RETRIES",
		"MAX_INPUT_TOKENS",
		"MAX_OUTPUT_TOKENS",
		"CONTEXT_TOKEN_BUDGET",
		"TOKENS_INPUT_SOFT",
		"TOKENS_SUMMARY_TARGET",
		"TOKENS_TRIM_BUDGET",
		"LONGTERM_MEMORY_PATH",
		"LONGTERM_SUMMARY_TTL",
		"LONGTERM_SUMMARY_INTERVAL",
		"DB_DBPATH",
		"DB_DBNAME",
		"PORT",
		"RETRY_TIMES",
		"STREAM_PING_INTERVAL_MS",
		"STREAM_PING_TEXT",
		"EXTRA_QUERY_CHAT",
		"EXTRA_QUERY_EMBEDDING",
		"EXTRA_QUERY_LONGTERM",
		"BOT_DEBUG_STUB",
		"BOT_DEBUG_MODE",
		"BOT_STUB_MODE",
		"BOT_LOG_LEVEL",
		}

// Legacy — name every legacy row that is set in the environment (§7 table: only
// the 19 names above are real rows; §16 change 25.1 points the rest at §15).
// The value is never applied and never logged, so a secret-shaped legacy row
// cannot leak through this message (§12 security).
func Legacy(raw map[string]string) []string {
	var found = []string{}
 var i = 0
 for i < len(LegacyRows) {
  var name = LegacyRows[i]
  if strings.TrimSpace(raw[name]) != "" {
   found = Push(found, fmt.Sprintf("ENV %s is not a row of the §7 table of v1.1 (it is a row of the v1.0 §7; §16 change 25.1 moved it to the §15 backlog together with the token budgets, the deep link and the summary/embedding models): the value is ignored, the loader reads only the 19 rows of §7", name))
  }
  i += 1
 }
 return found
}



// LoadFrom — resolve one §7 configuration out of the raw ENV map, keeping the
// resolved values and the problems apart: every unset or wrong row is named in
// the same run, the loader itself never stops the program (§10 День 1, §12
// reliability). The bounds come from the §7 table and from the §12 requirements
// it fixes: HEALTH_PORT ≤ 65535 (§16 change 11, the module internal/health with
// GET /healthz and the HEALTHCHECK of compose), MAX_MESSAGE_CHUNK ≤ 4096 SYMBOLS
// (§12 UX, §16 change 7: the reply limit of Telegram is measured in symbols),
// CONTEXT_MESSAGES/MAX_CONTEXT_CHARS/MAX_INPUT_CHARS bound the glued history
// (§16 change 10: in v1 the context was bounded only by the message count),
// MAX_CONCURRENT_MODEL_REQUESTS bounds the parallel load of the GPU (§12).
func LoadFrom(raw map[string]string) (Config, []string) {
 var cfg = Config{}
 var problems = []string{}

// §7 v1.1 reads only the 19 rows of the table; §16 change 25.1 names the leftover
// v1.0 rows so that a stale .env or compose file cannot pretend to configure the bot.
 var stale = Legacy(raw)
 var staleIndex = 0
 for staleIndex < len(stale) {
  problems = Push(problems, stale[staleIndex])
  staleIndex += 1
 }

 var token = strings.TrimSpace(raw["TELEGRAM_BOT_TOKEN"])
 if token == "" {
  problems = Push(problems, "ENV TELEGRAM_BOT_TOKEN is not set: the bot cannot start (§7 behavior: the absence of the token falls on the start with a clear error; §10 День 1 DoD: exit code 1)")
 } else if !strings.Contains(token, ":") {
  problems = Push(problems, "ENV TELEGRAM_BOT_TOKEN has no colon: the token of BotFather has the form <digits>:<secret> (§14.1 item 1)")
 }
 cfg.BotToken = token

 var key = strings.TrimSpace(raw["STRATA_API_KEY"])
 cfg.StrataKey = key

 var base = strings.TrimSpace(raw["STRATA_BASE_URL"])
 if base == "" {
  base = DefaultBaseURL
 }
 cfg.StrataBase = CutBase(base)
 cfg.ChatURL = JoinURL(base, "/chat/completions")
 cfg.ModelsURL = JoinURL(base, "/models")

 var model = strings.TrimSpace(raw["STRATA_MODEL"])
 if model == "" {
  model = DefaultStrataModel
 }
 cfg.StrataModel = model

 var timeout, badTimeout = Num(raw, "STRATA_TIMEOUT_SECONDS", DefaultTimeoutSeconds, 1, 3600)
 cfg.TimeoutSeconds = timeout
 problems = Push(problems, badTimeout)

 var deadline, badDeadline = Num(raw, "TG_REPLY_DEADLINE_SECONDS", DefaultDeadlineSeconds, 1, 3600)
 cfg.ReplyDeadlineSeconds = deadline
 problems = Push(problems, badDeadline)
 if deadline <= timeout {
  problems = Push(problems, "ENV TG_REPLY_DEADLINE_SECONDS is not greater than STRATA_TIMEOUT_SECONDS: the total budget of the retries, the timeout and the answer into Telegram must exceed the timeout of one attempt (§7 rows, §10 День 3 invariant)")
 }

 var dbPath = strings.TrimSpace(raw["DB_PATH"])
 if dbPath == "" {
  dbPath = DefaultDBPath
 }
 cfg.DBPath = dbPath

 var messages, badMessages = Num(raw, "CONTEXT_MESSAGES", DefaultContextMessages, 0, 200)
 cfg.ContextMessages = messages
 problems = Push(problems, badMessages)

 var chars, badChars = Num(raw, "MAX_CONTEXT_CHARS", DefaultMaxContextChars, 256, 262144)
 cfg.MaxContextChars = chars
 problems = Push(problems, badChars)

 var input, badInput = Num(raw, "MAX_INPUT_CHARS", DefaultMaxInputChars, 64, 262144)
 cfg.MaxInputChars = input
 problems = Push(problems, badInput)

 var prompt = strings.TrimSpace(raw["SYSTEM_PROMPT"])
 if prompt == "" {
  prompt = DefaultSystemPrompt
 }
 cfg.SystemPrompt = prompt

 var ids, badIDs = UserIDs(raw)
 cfg.UserIDs = ids
 cfg.DenyByDefault = len(ids) == 0
 problems = Push(problems, badIDs)

 var rate, badRate = Num(raw, "RATE_LIMIT_PER_MINUTE", DefaultRateLimit, 0, 1000)
 cfg.RateLimitPerMinute = rate
 problems = Push(problems, badRate)

 var concurrent, badConcurrent = Num(raw, "MAX_CONCURRENT_MODEL_REQUESTS", DefaultConcurrent, 1, 16)
 cfg.MaxConcurrentRequests = concurrent
 problems = Push(problems, badConcurrent)

 // PROXY_FREE_TEXT=true — the §7 row: with it the allowed users may send free
// texts instead of the commands. The default of the table is the true value.
 var free, badFree = Flag(raw, "PROXY_FREE_TEXT", true)
 cfg.FreeText = free
 problems = Push(problems, badFree)

 var chunk, badChunk = Num(raw, "MAX_MESSAGE_CHUNK", DefaultMaxChunk, 64, ChunkMax)
 cfg.MaxMessageChunk = chunk
 problems = Push(problems, badChunk)

 var mode, badMode = Mode(raw)
 cfg.ParseMode = mode
 problems = Push(problems, badMode)

 var port, badPort = Num(raw, "HEALTH_PORT", DefaultHealthPort, 1, PortMax)
 cfg.HealthPort = port
 problems = Push(problems, badPort)

 var level, badLevel = Level(raw)
 cfg.LogLevel = level
 problems = Push(problems, badLevel)

 return cfg, problems
}

// Load — read the whole process ENV once and hand it to LoadFrom (§7: every
// value comes from the process ENV, the config file is gone; §12 security: the
// token of BotFather lives only in the ENV, .env is listed in .gitignore).
func Load() (Config, []string) {
 var raw = map[string]string{}
 var entries = os.Environ()
 var i = 0
 for i < len(entries) {
  var pair = strings.Split(entries[i], "=")
  if len(pair) > 1 {
   raw[pair[0]] = strings.Join(pair[1:], "=")
  }
  i += 1
 }
 return LoadFrom(raw)
}


