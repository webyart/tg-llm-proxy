// Config — the configuration of the proxy. One field per row of the §7 table
// "Конфигурация (ENV)", so the loader and the plan cannot diverge any more (§10
// День 1: "internal/config: ENV → Config, validation, understandable messages
// about errors"; §16 change 10 lists exactly the rows that plan v1.1 added to §7).
// Every value comes from the process ENV — the config file is gone (§12 security:
// the token is only in the ENV, ".env" is listed in ".gitignore").

package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
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
	// The four rows of the §15 «стриминг ответов» (§16 change 34), placed next to the budgets they belong to.
	"STREAMING_ENABLED",
	"STREAM_EDIT_INTERVAL_MS",
	"STREAM_IDLE_TIMEOUT_SECONDS",
	"STREAM_TOTAL_DEADLINE_SECONDS",
	"DB_PATH",
	"CONTEXT_MESSAGES",
	"MAX_CONTEXT_CHARS",
	"MAX_INPUT_CHARS",
	"SYSTEM_PROMPT",
	"TELEGRAM_ALLOWED_USERS",
	"RATE_LIMIT_PER_MINUTE",
	"MAX_CONCURRENT_MODEL_REQUESTS",
	"PROXY_FREE_TEXT",
	"MAX_MESSAGE_CHUNK",
	"PARSE_MODE",
	"HEALTH_PORT",
	"LOG_LEVEL",
}

// HasRow — is the name one of the rows of the §7 table (19 since plan v1.1, 23 since §16 change 34 added the
// streaming rows)? The §16 change 29 rename is pinned from both sides with it: TELEGRAM_ALLOWED_USERS is a row,
// ALLOWED_USER_IDS is not one any more (a test checks both, because `Env` is what PrintMissing walks and what a
// reader of §7 expects to find).
func HasRow(name string) bool {
	var i = 0
	for i < len(Env) {
		if Env[i] == name {
			return true
		}
		i += 1
	}
	return false
}

// The defaults of the §7 table. §16 change 8 fixes the invariant of the URL join:
// the joined URL carries EXACTLY ONE "/v1", never "/v1/v1/chat/completions".
// §16 change 26 records the measurement that made the direction of the rule
// obvious: a real OpenAI-compatible STRATA answers POST /v1/chat/completions with
// 200 and POST /chat/completions with 404, so the suffix is part of the endpoint,
// not noise to be cut. §16 change 9 makes DB_PATH default to the local
// "./data/bot.db", because the compose default "/data/bot.db" is not a valid path
// for run.ps1 on Windows.
const DefaultBaseURL = "http://host.docker.internal:8000"

// V1Path — the OpenAI-compatible path prefix of §7 that NormalizeBase pins to
// exactly one occurrence (scripts/strata-check.ps1 repeats the same rule, §9).
const V1Path = "/v1"
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

// The four §15 rows of §16 change 34. STREAMING_ENABLED defaults to FALSE: the streaming path rewrites one
// Telegram message instead of sending one, which is a visible change of behaviour for every chat, so the
// operator turns it on deliberately (§12 надёжность: a feature that changes the UX of every answer arrives
// behind a row, not behind a deploy). The other three are the budgets of one streamed answer, and the numbers
// come from the measurement of §14: ~48 SSE events per second, ~20 ms apart, and an answer of the size the
// live volume holds measured 1m56s. A sustained one-second cadence reached a real Telegram retry_after=120
// during the §11 live walk, so the default is five seconds; five minutes is the ceiling of the whole answer.
const DefaultStreaming = false
const DefaultStreamEditIntervalMS = int64(5000)
const DefaultStreamIdleSeconds = int64(30)
const DefaultStreamTotalSeconds = int64(300)

// The bounds of the §15 rows. The floor of the edit interval is what Telegram tolerates: editing one message
// more than once a second is how a bot reaches the flood of §10 День 4, and 250 ms is the shortest interval
// this project is willing to write into a table. The ceiling of the interval is a minute of silence in a chat
// that is waiting for an answer, which is not streaming any more.
const StreamEditIntervalMin = int64(250)
const StreamEditIntervalMax = int64(60000)

// The bounds that the plan fixes by name. §12 UX: the reply limit of Telegram is
// 4096 SYMBOLS, so MAX_MESSAGE_CHUNK cannot pass it (§16 change 7: кириллица costs
// two bytes, therefore the cut goes by runes and never by bytes). §16 change 11
// gives HEALTH_PORT to the new module internal/health (GET /healthz, the HEALTHCHECK
// of compose and smoke.ps1). §12 security keeps TELEGRAM_ALLOWED_USERS a bounded list.
const ChunkMax = int64(4096)
const PortMax = int64(65535)
const TGBigID = int64(9223372036854)
const UserIDsMax = 1024

// Config — one resolved configuration of the §7 table. The field names follow the
// rows of §7 (BotToken, StrataKey, ..., LogLevel), so a rename can never happen
// again: the loader fills exactly these fields and §10 День 1 DoD echoes them back.
// ChatURL and ModelsURL are not rows of §7 — the §7 URL rule joins them from
// StrataBase through NormalizeBase (§16 change 8: exactly one "/v1", §16 change 26:
// the suffix is what a real OpenAI-compatible server actually serves). BotToken
// and StrataKey are secrets: §12 security forbids logging them and demands the
// leak check before a commit (§14.1 item 1: the token of BotFather never enters
// the chat or a commit).
type Config struct {
	BotToken             string
	StrataKey            string
	StrataBase           string
	ChatURL              string
	ModelsURL            string
	StrataModel          string
	TimeoutSeconds       int64
	ReplyDeadlineSeconds int64
	// The four §15 rows of §16 change 34. Streaming turns the answer of /ask into one message that is
	// rewritten as the model writes; StreamEditIntervalMS is the cadence of those rewrites; StreamIdleSeconds
	// is the pause between two SSE events past which the stream is considered dead; StreamTotalSeconds is the
	// ceiling of one streamed answer. The semantics of TimeoutSeconds and ReplyDeadlineSeconds do NOT change
	// (§7 rows keep their meaning): the stream has budgets of its own.
	Streaming             bool
	StreamEditIntervalMS  int64
	StreamIdleSeconds     int64
	StreamTotalSeconds    int64
	DBPath                string
	ContextMessages       int64
	MaxContextChars       int64
	MaxInputChars         int64
	SystemPrompt          string
	UserIDs               []int64
	DenyByDefault         bool
	RateLimitPerMinute    int64
	MaxConcurrentRequests int64
	FreeText              bool
	MaxMessageChunk       int64
	ParseMode             string
	HealthPort            int64
	LogLevel              string
}

// NormalizeBase — the §7 URL rule (§16 change 8, §16 change 26): the base the user
// wrote is normalized so the endpoint it points at is reached EXACTLY ONCE through
// "/v1". Three shapes are accepted and all produce one working URL:
//
//	http://strata:8080          → http://strata:8080/v1
//	http://strata:8080/v1       → http://strata:8080/v1      (the §16 change 9 compose value)
//	http://strata:8080/v1/v1/   → http://strata:8080/v1      (the "/v1/v1" bug of §16 change 8)
//	http://host:8000/gateway    → http://host:8000/gateway/v1 (a proxied prefix stays)
//
// Measured on the real endpoint of §14 item 5 (§16 change 26): POST {base}/v1/chat/completions
// → 200, POST {base}/chat/completions → 404, GET {base}/v1/models → 200 with the model list,
// GET {base}/models → 404. Cutting the suffix therefore produced a URL the server rejects.
// scripts/strata-check.ps1 repeats this rule with the same three shapes.
func NormalizeBase(base string) string {
	var cut = strings.TrimSuffix(strings.TrimSpace(base), "/")
	for strings.HasSuffix(cut, V1Path) {
		cut = strings.TrimSuffix(strings.TrimSuffix(cut, V1Path), "/")
	}
	if cut == "" {
		return V1Path
	}
	return cut + V1Path
}

// JoinURL — the URL joins of §7: ChatURL = Join(base, "/chat/completions") and
// ModelsURL = Join(base, "/models") over NormalizeBase, so a base written with or
// without "/v1" gives the same working pair and never "/v1/v1/..." (§16 change 8,
// §16 change 26).
func JoinURL(base string, path string) string {
	return NormalizeBase(base) + path
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

// RenamedRows — an ENV name that WAS a row of the §7 table and is not one any more. §16 change 28 made
// `TELEGRAM_ALLOWED_USERS` an alias of `ALLOWED_USER_IDS`; §16 change 29 reversed that: the name a real
// `.env` carries is `TELEGRAM_ALLOWED_USERS`, so THAT is the row of §7 and `ALLOWED_USER_IDS` left the
// loader. A name that leaves the table still has to be named when it carries a value — an old file with
// `ALLOWED_USER_IDS=…` must not become a silently closed bot (the exact failure §16 change 28 was about).
// So the value is NOT applied: one row, one spelling, nothing to merge, and the start stops with exit
// code 1 on the message below (§7: the loader reads exactly the 23 names of the table).
var RenamedRows = map[string]string{
	"ALLOWED_USER_IDS": "TELEGRAM_ALLOWED_USERS",
}

// RenamedOrder — the fixed scan order of RenamedRows: a map iteration would put the problems of a start in
// a different order on every run (§12 reliability).
var RenamedOrder = []string{"ALLOWED_USER_IDS"}

// Renamed — name every renamed row that still carries a value and point at the name that replaced it.
// The value itself is never printed, so an old row that happens to look secret-shaped cannot leak through
// this message (§12 security).
func Renamed(raw map[string]string) []string {
	var found = []string{}
	var i = 0
	for i < len(RenamedOrder) {
		var name = RenamedOrder[i]
		if strings.TrimSpace(raw[name]) != "" {
			found = Push(found, fmt.Sprintf("ENV %s is not a row of the §7 table any more: §16 change 29 renamed the row to %s, so the value of %s is ignored — write the allowlist under the new name (§7: the loader reads exactly the 23 names of the table)", name, RenamedRows[name], name))
		}
		i += 1
	}
	return found
}

// RowIsSet — does the §7 row carry a value? PrintMissing uses it (§10 День 1 DoD: the name of every unset
// §7 variable is printed, and a wrong «not set» line is worse than no line). Since §16 change 29 the row
// has exactly one spelling, so this is one lookup and nothing to look through.
func RowIsSet(raw map[string]string, name string) bool {
	return strings.TrimSpace(raw[name]) != ""
}

// UserIDs — the TELEGRAM_ALLOWED_USERS row: the chat IDs through a comma (§7 table, §16 change 29 — this
// name is the row itself, `ALLOWED_USER_IDS` of §16 change 28 is gone from the loader and is refused by
// name through Renamed).
// An empty row means the closed bot, deny by default: it answers only /start
// and /whoami and writes events(kind='denied') (§7 behavior, §16 change 13 —
// otherwise a fresh user could not learn his own id and enter the allowlist).
// A malformed entry is named with the ENV name it was read from, the good entries are still kept
// (§12 reliability); a repeated id is kept once, because a list that names the same `user_id` twice must
// not be counted twice by `Allows`.
func UserIDs(raw map[string]string) ([]int64, string) {
	var ids = []int64{}
	var bad = []string{}

	var name = "TELEGRAM_ALLOWED_USERS"
	var parts = strings.Split(strings.TrimSpace(raw[name]), ",")
	var i = 0
	for i < len(parts) {
		var part = strings.TrimSpace(parts[i])
		if part != "" {
			var value, err = strconv.ParseInt(part, 10, 64)
			if err != nil || value <= 0 {
				bad = Push(bad, fmt.Sprintf("the entry %s of ENV %s is not a positive chat id (§7 table)", part, name))
			} else if HasUserID(ids, value) {
				// The same id named twice in one row — kept once, said nothing about: `Allows` must not
				// count it twice, and a duplicate in a list of ids is not a mistake worth a line of output.
			} else if len(ids) < UserIDsMax {
				ids = append(ids, value)
			} else {
				bad = Push(bad, fmt.Sprintf("ENV %s is longer than %d entries (§12 security keeps the allowlist bounded)", name, UserIDsMax))
			}
		}
		i += 1
	}
	if len(bad) == 0 {
		return ids, ""
	}
	return ids, fmt.Sprintf("ENV %s has %d malformed entries: %s; the good entries are kept (§12 reliability)", name, len(bad), strings.Join(bad, "; "))
}

// HasUserID — is the id already in the allowlist? The linear scan is fine here: §12 security bounds the
// list at UserIDsMax entries, and the loader runs once at the start (§10 День 1).
func HasUserID(ids []int64, id int64) bool {
	var i = 0
	for i < len(ids) {
		if ids[i] == id {
			return true
		}
		i += 1
	}
	return false
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
// errors"; §7: the loader reads exactly the 23 rows of the table).
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
// the 23 names above are real rows; §16 change 25.1 points the rest at §15).
// The value is never applied and never logged, so a secret-shaped legacy row
// cannot leak through this message (§12 security).
func Legacy(raw map[string]string) []string {
	var found = []string{}
	var i = 0
	for i < len(LegacyRows) {
		var name = LegacyRows[i]
		if strings.TrimSpace(raw[name]) != "" {
			found = Push(found, fmt.Sprintf("ENV %s is not a row of the §7 table of v1.1 (it is a row of the v1.0 §7; §16 change 25.1 moved it to the §15 backlog together with the token budgets, the deep link and the summary/embedding models): the value is ignored, the loader reads only the 23 rows of §7", name))
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

	// §7 v1.1 reads only the 23 rows of the table; §16 change 25.1 names the leftover
	// v1.0 rows so that a stale .env or compose file cannot pretend to configure the bot.
	var stale = Legacy(raw)
	var staleIndex = 0
	for staleIndex < len(stale) {
		problems = Push(problems, stale[staleIndex])
		staleIndex += 1
	}

	// §16 change 29: `ALLOWED_USER_IDS` used to be the name of the allowlist row and is not one any more.
	// A row that left the table is refused by name too, exactly like a v1.0 leftover: a stale `.env` must
	// not leave the bot closed while looking configured (§12 reliability).
	var renamed = Renamed(raw)
	var renamedIndex = 0
	for renamedIndex < len(renamed) {
		problems = Push(problems, renamed[renamedIndex])
		renamedIndex += 1
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
	cfg.StrataBase = NormalizeBase(base)
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

	// The four §15 rows of §16 change 34. A stream needs budgets of its own because the two existing ones
	// answer a different question: «how long may ONE non-streamed call take» and «how long may the user wait
	// before the answer starts». Measured on §14: the first SSE event of a healthy stream arrives in 4–6 ms
	// while the answer itself can take minutes — a stall and a long answer cannot share one number.
	var streaming, badStreaming = Flag(raw, "STREAMING_ENABLED", DefaultStreaming)
	cfg.Streaming = streaming
	problems = Push(problems, badStreaming)

	var editInterval, badEditInterval = Num(raw, "STREAM_EDIT_INTERVAL_MS", DefaultStreamEditIntervalMS, StreamEditIntervalMin, StreamEditIntervalMax)
	cfg.StreamEditIntervalMS = editInterval
	problems = Push(problems, badEditInterval)

	var streamIdle, badStreamIdle = Num(raw, "STREAM_IDLE_TIMEOUT_SECONDS", DefaultStreamIdleSeconds, 1, 3600)
	cfg.StreamIdleSeconds = streamIdle
	problems = Push(problems, badStreamIdle)

	var streamTotal, badStreamTotal = Num(raw, "STREAM_TOTAL_DEADLINE_SECONDS", DefaultStreamTotalSeconds, 10, 3600)
	cfg.StreamTotalSeconds = streamTotal
	problems = Push(problems, badStreamTotal)
	if streamTotal <= streamIdle {
		problems = Push(problems, "ENV STREAM_TOTAL_DEADLINE_SECONDS is not greater than STREAM_IDLE_TIMEOUT_SECONDS: a stall detector may not outlive the answer it watches (§7 rows, §15)")
	}
	// The promise «сообщение не зависает» of §12 UX is bounded by TG_REPLY_DEADLINE_SECONDS: the placeholder of
	// a streamed answer has to be posted while that budget is still open. A stream allowed to stay silent for
	// the whole idle budget, with a reply deadline shorter than it, contradicts the promise by configuration —
	// and the measurement of §14 (first event in 4–6 ms) says such a case is a wrong table, not a slow model.
	if streaming && deadline <= streamIdle {
		problems = Push(problems, "ENV TG_REPLY_DEADLINE_SECONDS is not greater than STREAM_IDLE_TIMEOUT_SECONDS while STREAMING_ENABLED=true: the answer must START arriving inside the reply budget of §7, and a stream that may stay silent for the whole idle budget breaks that promise by configuration (§12 UX, §15)")
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

// Allows — the §7 allowlist rule: an empty TELEGRAM_ALLOWED_USERS means a closed bot (DenyByDefault of
// LoadFrom), so nobody is allowed until the id is registered. §16 change 13 keeps /start and /whoami
// outside this check — without them a user of a closed bot could never learn their own id.
func (c Config) Allows(userID int64) bool {
	if len(c.UserIDs) == 0 {
		return false
	}
	var i = 0
	for i < len(c.UserIDs) {
		if c.UserIDs[i] == userID {
			return true
		}
		i++
	}
	return false
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
