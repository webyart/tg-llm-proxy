// День 1: "internal/config: ENV → Config, validation, understandable messages
// the token is only in the ENV, ".env" is listed in ".gitignore").

package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// the absence of TELEGRAM_BOT_TOKEN → a fall on the start with a clear error;
// code of exits 1").
var Env = []string{
	"TELEGRAM_BOT_TOKEN",
	"STRATA_BASE_URL",
	"STRATA_MODEL",
	"STRATA_TRANSCRIPTION_MODEL",
	"TRANSCRIPTION_BASE_URL",
	"MEDIA_MAX_MB",
	"SPEECH_MAX_SECONDS",
	"TTS_ENABLED",
	"TTS_MODEL",
	"TTS_VOICE",
	"TTS_MAX_CHARS",
	"TTS_TIMEOUT_SECONDS",
	"STRATA_API_KEY",
	"STRATA_TIMEOUT_SECONDS",
	"TG_REPLY_DEADLINE_SECONDS",
	"RECOVERY_MAX_AGE_HOURS",
	"STREAMING_ENABLED",
	"STREAM_EDIT_INTERVAL_MS",
	"STREAM_IDLE_TIMEOUT_SECONDS",
	"STREAM_TOTAL_DEADLINE_SECONDS",
	"WEBHOOK_ENABLED",
	"WEBHOOK_PUBLIC_URL",
	"WEBHOOK_SECRET_TOKEN",
	"WEBHOOK_LISTEN_HOST",
	"WEBHOOK_PORT",
	"WEBHOOK_PATH",
	"DB_PATH",
	"REMINDER_TIMEZONE",
	"CONTEXT_MESSAGES",
	"MAX_CONTEXT_CHARS",
	"MAX_INPUT_CHARS",
	"RAG_MAX_DOCUMENTS",
	"RAG_RESULT_LIMIT",
	"RAG_MAX_CONTEXT_CHARS",
	"SYSTEM_PROMPT",
	"TELEGRAM_ALLOWED_USERS",
	"TELEGRAM_ADMIN_USERS",
	"RATE_LIMIT_PER_MINUTE",
	"MAX_CONCURRENT_MODEL_REQUESTS",
	"PROXY_FREE_TEXT",
	"TOOLS_ENABLED",
	"URL_READER_ENABLED",
	"WEB_SEARCH_ENABLED",
	"WEB_SEARCH_BASE_URL",
	"WEB_SEARCH_TIMEOUT_SECONDS",
	"WEB_SEARCH_MAX_RESULTS",
	"MAX_MESSAGE_CHUNK",
	"PARSE_MODE",
	"HEALTH_PORT",
	"LOG_LEVEL",
}

// ALLOWED_USER_IDS is not one any more (a test checks both, because `Env` is what PrintMissing walks and what a
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

// the joined URL carries EXACTLY ONE "/v1", never "/v1/v1/chat/completions".
// obvious: a real OpenAI-compatible STRATA answers POST /v1/chat/completions with
// 200 and POST /chat/completions with 404, so the suffix is part of the endpoint,
// "./data/bot.db", because the compose default "/data/bot.db" is not a valid path
// for run.ps1 on Windows.
const DefaultBaseURL = "http://host.docker.internal:8000"

const V1Path = "/v1"
const DefaultDBPath = "./data/bot.db"
const DefaultSystemPrompt = "You are a helpful assistant."
const DefaultTranscriptionModel = "Systran/faster-whisper-small"
const DefaultMediaMaxMB = int64(10)
const DefaultSpeechMaxSeconds = int64(300)
const DefaultTTSEnabled = false
const DefaultTTSModel = "speaches-ai/piper-ru_RU-ruslan-medium"
const DefaultTTSVoice = "ruslan"
const DefaultTTSMaxChars = int64(1000)
const DefaultTTSTimeoutSeconds = int64(60)
const DefaultToolsEnabled = true
const DefaultTimeoutSeconds = int64(60)
const DefaultDeadlineSeconds = int64(90)
const DefaultRecoveryMaxAgeHours = int64(24)
const DefaultContextMessages = int64(20)
const DefaultMaxContextChars = int64(8000)
const DefaultMaxInputChars = int64(4000)
const DefaultRAGMaxDocuments = int64(20)
const DefaultRAGResultLimit = int64(3)
const DefaultRAGMaxContextChars = int64(2000)
const DefaultRateLimit = int64(20)
const DefaultConcurrent = int64(2)
const DefaultMaxChunk = int64(3500)
const DefaultParseMode = "MarkdownV2"
const DefaultHealthPort = int64(8080)
const DefaultLogLevel = "info"
const DefaultReminderTimezone = "UTC"

// Telegram message instead of sending one, which is a visible change of behaviour for every chat, so the
// behind a row, not behind a deploy). The other three are the budgets of one streamed answer, and the numbers
// live volume holds measured 1m56s. A sustained one-second cadence reached a real Telegram retry_after=120
const DefaultStreaming = false
const DefaultStreamEditIntervalMS = int64(5000)
const DefaultStreamIdleSeconds = int64(30)
const DefaultStreamTotalSeconds = int64(900)

// this project is willing to write into a table. The ceiling of the interval is a minute of silence in a chat
// that is waiting for an answer, which is not streaming any more.
const StreamEditIntervalMin = int64(250)
const StreamEditIntervalMax = int64(60000)

// gives HEALTH_PORT to the new module internal/health (GET /healthz, the HEALTHCHECK
const ChunkMax = int64(4096)
const PortMax = int64(65535)
const TGBigID = int64(9223372036854)
const UserIDsMax = 1024

// the suffix is what a real OpenAI-compatible server actually serves). BotToken
// the chat or a commit).
type Config struct {
	BotToken             string
	StrataKey            string
	StrataBase           string
	ChatURL              string
	ModelsURL            string
	StrataModel          string
	TranscriptionModel   string
	TranscriptionBase    string
	TranscriptionURL     string
	TTSURL               string
	TTSEnabled           bool
	TTSModel             string
	TTSVoice             string
	TTSMaxChars          int64
	TTSTimeoutSeconds    int64
	MediaMaxBytes        int64
	SpeechMaxSeconds     int64
	TimeoutSeconds       int64
	ReplyDeadlineSeconds int64
	RecoveryMaxAgeHours  int64
	// rewritten as the model writes; StreamEditIntervalMS is the cadence of those rewrites; StreamIdleSeconds
	// is the pause between two SSE events past which the stream is considered dead; StreamTotalSeconds is the
	// ceiling of one streamed answer. The semantics of TimeoutSeconds and ReplyDeadlineSeconds do NOT change
	Streaming               bool
	StreamEditIntervalMS    int64
	StreamIdleSeconds       int64
	StreamTotalSeconds      int64
	WebhookEnabled          bool
	WebhookPublicURL        string
	WebhookSecretToken      string
	WebhookHost             string
	WebhookPort             int64
	WebhookPath             string
	DBPath                  string
	ReminderTimezone        string
	ReminderLocation        *time.Location
	ContextMessages         int64
	MaxContextChars         int64
	MaxInputChars           int64
	RAGMaxDocuments         int64
	RAGResultLimit          int64
	RAGMaxContextChars      int64
	SystemPrompt            string
	UserIDs                 []int64
	AdminIDs                []int64
	DenyByDefault           bool
	RateLimitPerMinute      int64
	MaxConcurrentRequests   int64
	FreeText                bool
	ToolsEnabled            bool
	URLReaderEnabled        bool
	WebSearchEnabled        bool
	WebSearchBaseURL        string
	WebSearchTimeoutSeconds int64
	WebSearchMaxResults     int64
	MaxMessageChunk         int64
	ParseMode               string
	HealthPort              int64
	LogLevel                string
}

// wrote is normalized so the endpoint it points at is reached EXACTLY ONCE through
// "/v1". Three shapes are accepted and all produce one working URL:
//
//	http://strata:8080          → http://strata:8080/v1
//	http://host:8000/gateway    → http://host:8000/gateway/v1 (a proxied prefix stays)
//
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

// ModelsURL = Join(base, "/models") over NormalizeBase, so a base written with or
func JoinURL(base string, path string) string {
	return NormalizeBase(base) + path
}

func validateStrataBase(base string) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Host == "" {
		return "ENV STRATA_BASE_URL must be a valid absolute HTTP(S) URL"
	}
	if parsed.User != nil {
		return "ENV STRATA_BASE_URL must not contain userinfo or credentials"
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "ENV STRATA_BASE_URL must use http or https"
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(base, "#") {
		return "ENV STRATA_BASE_URL must not contain query or fragment components"
	}
	if parsed.Scheme == "http" && !localStrataHost(parsed.Hostname()) {
		return "ENV STRATA_BASE_URL must use HTTPS for a non-local endpoint"
	}
	return ""
}

func baseWithoutQueryOrFragment(base string) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func localStrataHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "localhost" || host == "host.docker.internal" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return !strings.Contains(host, ".")
}

// set, a clear problem when the value is not an integer or falls outside the
// validation, понятные сообщения об ошибках"). The message carries the ENV
func Num(raw map[string]string, name string, def int64, min int64, max int64) (int64, string) {
	var text = strings.TrimSpace(raw[name])
	if text == "" {
		return def, ""
	}
	var value, err = strconv.ParseInt(text, 10, 64)
	if err != nil {
		return def, fmt.Sprintf("ENV %s is not an integer , the row must be between %d and %d ", name, min, max)
	}
	if value < min {
		return def, fmt.Sprintf("ENV %s = %d is below the bound %d , the default %d is used instead", name, value, min, def)
	}
	if value > max {
		return def, fmt.Sprintf("ENV %s = %d is above the bound %d , the default %d is used instead", name, value, max, def)
	}
	return value, ""
}

// row keeps the default; an unparseable value says so instead of silently
func Flag(raw map[string]string, name string, def bool) (bool, string) {
	var text = strings.TrimSpace(raw[name])
	if text == "" {
		return def, ""
	}
	var value, err = strconv.ParseBool(text)
	if err != nil {
		return def, fmt.Sprintf("ENV %s is neither true nor false , the default %s is used", name, strconv.FormatBool(def))
	}
	return value, ""
}

// The row decides how the answer is escaped BEFORE it is cut, because the
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
	return DefaultParseMode, fmt.Sprintf("ENV PARSE_MODE is not one of MarkdownV2 | HTML | plain , %s is used", DefaultParseMode)
}

// the logs at LOG_LEVEL=info, so the default is the quiet one.
func Level(raw map[string]string) (string, string) {
	var text = strings.ToLower(strings.TrimSpace(raw["LOG_LEVEL"]))
	if text == "" {
		return DefaultLogLevel, ""
	}
	if text == "error" || text == "warning" || text == "info" || text == "debug" {
		return text, ""
	}
	return DefaultLogLevel, fmt.Sprintf("ENV LOG_LEVEL is not one of error | warning | info | debug , %s is used", DefaultLogLevel)
}

// Push — collect one problem into the list of problems of one load. An empty
// row must not hide the other rows, so the loader keeps going and returns the
func Push(problems []string, msg string) []string {
	if msg == "" {
		return problems
	}
	return append(problems, msg)
}

// loader. A name that leaves the table still has to be named when it carries a value — an old file with
// So the value is NOT applied: one row, one spelling, nothing to merge, and the start stops with exit
var RenamedRows = map[string]string{
	"ALLOWED_USER_IDS": "TELEGRAM_ALLOWED_USERS",
}

// RenamedOrder — the fixed scan order of RenamedRows: a map iteration would put the problems of a start in
var RenamedOrder = []string{"ALLOWED_USER_IDS"}

// Renamed — name every renamed row that still carries a value and point at the name that replaced it.
// The value itself is never printed, so an old row that happens to look secret-shaped cannot leak through
func Renamed(raw map[string]string) []string {
	var found = []string{}
	var i = 0
	for i < len(RenamedOrder) {
		var name = RenamedOrder[i]
		if strings.TrimSpace(raw[name]) != "" {
			found = Push(found, fmt.Sprintf("ENV %s is not a row of the table any more: renamed the row to %s, so the value of %s is ignored — write the allowlist under the new name ", name, RenamedRows[name], name))
		}
		i += 1
	}
	return found
}

// has exactly one spelling, so this is one lookup and nothing to look through.
func RowIsSet(raw map[string]string, name string) bool {
	return strings.TrimSpace(raw[name]) != ""
}

// name through Renamed).
// An empty row means the closed bot, deny by default: it answers only /start
// otherwise a fresh user could not learn his own id and enter the allowlist).
// A malformed entry is named with the ENV name it was read from, the good entries are still kept
// not be counted twice by `Allows`.
func UserIDs(raw map[string]string) ([]int64, string) {
	return userIDs(raw, "TELEGRAM_ALLOWED_USERS")
}

func AdminUserIDs(raw map[string]string) ([]int64, string) {
	return userIDs(raw, "TELEGRAM_ADMIN_USERS")
}

func userIDs(raw map[string]string, name string) ([]int64, string) {
	var ids = []int64{}
	var bad = []string{}
	var parts = strings.Split(strings.TrimSpace(raw[name]), ",")
	var i = 0
	for i < len(parts) {
		var part = strings.TrimSpace(parts[i])
		if part != "" {
			var value, err = strconv.ParseInt(part, 10, 64)
			if err != nil || value <= 0 {
				bad = Push(bad, fmt.Sprintf("the entry %s of ENV %s is not a positive chat id ", part, name))
			} else if HasUserID(ids, value) {
				// The same id named twice in one row — kept once, said nothing about: `Allows` must not
				// count it twice, and a duplicate in a list of ids is not a mistake worth a line of output.
			} else if len(ids) < UserIDsMax {
				ids = append(ids, value)
			} else {
				bad = Push(bad, fmt.Sprintf("ENV %s is longer than %d entries ", name, UserIDsMax))
			}
		}
		i += 1
	}
	if len(bad) == 0 {
		return ids, ""
	}
	return ids, fmt.Sprintf("ENV %s has %d malformed entries: %s; the good entries are kept ", name, len(bad), strings.Join(bad, "; "))
}

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

// asks which model the host really has (its context limit decides the budget of
// CONTEXT_MESSAGES and MAX_CONTEXT_CHARS), so the row keeps the plan default.
const DefaultStrataModel = "qwen2.5-7b-instruct"

// the token budgets, the deep link and the summary/embedding models). The loader
// refuses such a row by name instead of ignoring it, because a leftover row in .env
// or in compose otherwise looks like a working configuration and hides the fact that
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

// The value is never applied and never logged, so a secret-shaped legacy row
func Legacy(raw map[string]string) []string {
	var found = []string{}
	var i = 0
	for i < len(LegacyRows) {
		var name = LegacyRows[i]
		if strings.TrimSpace(raw[name]) != "" {
			found = Push(found, fmt.Sprintf("ENV %s is not a row of the table of v1.1 : the value is ignored, the loader reads only the 29 rows of", name))
		}
		i += 1
	}
	return found
}

// resolved values and the problems apart: every unset or wrong row is named in
// GET /healthz and the HEALTHCHECK of compose), MAX_MESSAGE_CHUNK ≤ 4096 SYMBOLS
// CONTEXT_MESSAGES/MAX_CONTEXT_CHARS/MAX_INPUT_CHARS bound the glued history
func LoadFrom(raw map[string]string) (Config, []string) {
	var cfg = Config{}
	var problems = []string{}

	// v1.0 rows so that a stale .env or compose file cannot pretend to configure the bot.
	var stale = Legacy(raw)
	var staleIndex = 0
	for staleIndex < len(stale) {
		problems = Push(problems, stale[staleIndex])
		staleIndex += 1
	}

	// A row that left the table is refused by name too, exactly like a v1.0 leftover: a stale `.env` must
	var renamed = Renamed(raw)
	var renamedIndex = 0
	for renamedIndex < len(renamed) {
		problems = Push(problems, renamed[renamedIndex])
		renamedIndex += 1
	}

	var token = strings.TrimSpace(raw["TELEGRAM_BOT_TOKEN"])
	if token == "" {
		problems = Push(problems, "ENV TELEGRAM_BOT_TOKEN is not set: the bot cannot start ")
	} else if !strings.Contains(token, ":") {
		problems = Push(problems, "ENV TELEGRAM_BOT_TOKEN has no colon: the token of BotFather has the form <digits>:<secret> ")
	}
	cfg.BotToken = token

	var key = strings.TrimSpace(raw["STRATA_API_KEY"])
	cfg.StrataKey = key

	var base = strings.TrimSpace(raw["STRATA_BASE_URL"])
	if base == "" {
		base = DefaultBaseURL
	}
	endpointBase := baseWithoutQueryOrFragment(base)
	cfg.StrataBase = NormalizeBase(endpointBase)
	cfg.ChatURL = JoinURL(endpointBase, "/chat/completions")
	cfg.ModelsURL = JoinURL(endpointBase, "/models")
	problems = Push(problems, validateStrataBase(base))
	transcriptionBase := strings.TrimSpace(raw["TRANSCRIPTION_BASE_URL"])
	if transcriptionBase == "" {
		transcriptionBase = base
	}
	transcriptionEndpointBase := baseWithoutQueryOrFragment(transcriptionBase)
	cfg.TranscriptionBase = NormalizeBase(transcriptionEndpointBase)
	cfg.TranscriptionURL = JoinURL(transcriptionEndpointBase, "/audio/transcriptions")
	if problem := validateStrataBase(transcriptionBase); problem != "" {
		problems = Push(problems, strings.Replace(problem, "STRATA_BASE_URL", "TRANSCRIPTION_BASE_URL", 1))
	}

	var model = strings.TrimSpace(raw["STRATA_MODEL"])
	if model == "" {
		model = DefaultStrataModel
	}
	cfg.StrataModel = model
	cfg.TranscriptionModel = strings.TrimSpace(raw["STRATA_TRANSCRIPTION_MODEL"])
	if cfg.TranscriptionModel == "" {
		cfg.TranscriptionModel = DefaultTranscriptionModel
	}

	mediaMaxMB, badMediaMaxMB := Num(raw, "MEDIA_MAX_MB", DefaultMediaMaxMB, 1, 100)
	cfg.MediaMaxBytes = mediaMaxMB * 1024 * 1024
	problems = Push(problems, badMediaMaxMB)

	speechMaxSeconds, badSpeechMaxSeconds := Num(raw, "SPEECH_MAX_SECONDS", DefaultSpeechMaxSeconds, 1, 3600)
	cfg.SpeechMaxSeconds = speechMaxSeconds
	problems = Push(problems, badSpeechMaxSeconds)
	problems = append(problems, loadTTS(raw, &cfg)...)

	var timeout, badTimeout = Num(raw, "STRATA_TIMEOUT_SECONDS", DefaultTimeoutSeconds, 1, 3600)
	cfg.TimeoutSeconds = timeout
	problems = Push(problems, badTimeout)

	var deadline, badDeadline = Num(raw, "TG_REPLY_DEADLINE_SECONDS", DefaultDeadlineSeconds, 1, 3600)
	cfg.ReplyDeadlineSeconds = deadline
	problems = Push(problems, badDeadline)
	if deadline <= timeout {
		problems = Push(problems, "ENV TG_REPLY_DEADLINE_SECONDS is not greater than STRATA_TIMEOUT_SECONDS: the total budget of the retries, the timeout and the answer into Telegram must exceed the timeout of one attempt ")
	}

	recoveryMaxAge, badRecoveryMaxAge := Num(raw, "RECOVERY_MAX_AGE_HOURS", DefaultRecoveryMaxAgeHours, 0, 8760)
	cfg.RecoveryMaxAgeHours = recoveryMaxAge
	problems = Push(problems, badRecoveryMaxAge)

	// answer a different question: «how long may ONE non-streamed call take» and «how long may the user wait
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
		problems = Push(problems, "ENV STREAM_TOTAL_DEADLINE_SECONDS is not greater than STREAM_IDLE_TIMEOUT_SECONDS: a stall detector may not outlive the answer it watches ")
	}
	// a streamed answer has to be posted while that budget is still open. A stream allowed to stay silent for
	// the whole idle budget, with a reply deadline shorter than it, contradicts the promise by configuration —
	if streaming && deadline <= streamIdle {
		problems = Push(problems, "ENV TG_REPLY_DEADLINE_SECONDS is not greater than STREAM_IDLE_TIMEOUT_SECONDS while STREAMING_ENABLED=true: the answer must START arriving inside the reply budget of, and a stream that may stay silent for the whole idle budget breaks that promise by configuration ")
	}

	var webhookEnabled, badWebhookEnabled = Flag(raw, "WEBHOOK_ENABLED", DefaultWebhookEnabled)
	cfg.WebhookEnabled = webhookEnabled
	problems = Push(problems, badWebhookEnabled)
	problems = append(problems, loadWebhook(raw, &cfg)...)

	var dbPath = strings.TrimSpace(raw["DB_PATH"])
	if dbPath == "" {
		dbPath = DefaultDBPath
	}
	cfg.DBPath = dbPath

	reminderTimezone := strings.TrimSpace(raw["REMINDER_TIMEZONE"])
	if reminderTimezone == "" {
		reminderTimezone = DefaultReminderTimezone
	}
	reminderLocation, reminderLocationErr := time.LoadLocation(reminderTimezone)
	if reminderLocationErr != nil {
		problems = Push(problems, fmt.Sprintf("ENV REMINDER_TIMEZONE = %q could not be loaded: %v; UTC is used instead", reminderTimezone, reminderLocationErr))
		reminderTimezone = DefaultReminderTimezone
		reminderLocation, _ = time.LoadLocation(DefaultReminderTimezone)
	}
	cfg.ReminderTimezone = reminderTimezone
	cfg.ReminderLocation = reminderLocation

	var messages, badMessages = Num(raw, "CONTEXT_MESSAGES", DefaultContextMessages, 0, 200)
	cfg.ContextMessages = messages
	problems = Push(problems, badMessages)

	var chars, badChars = Num(raw, "MAX_CONTEXT_CHARS", DefaultMaxContextChars, 256, 262144)
	cfg.MaxContextChars = chars
	problems = Push(problems, badChars)

	var input, badInput = Num(raw, "MAX_INPUT_CHARS", DefaultMaxInputChars, 64, 262144)
	cfg.MaxInputChars = input
	problems = Push(problems, badInput)
	ragDocuments, badRAGDocuments := Num(raw, "RAG_MAX_DOCUMENTS", DefaultRAGMaxDocuments, 1, 100)
	cfg.RAGMaxDocuments = ragDocuments
	problems = Push(problems, badRAGDocuments)
	ragResults, badRAGResults := Num(raw, "RAG_RESULT_LIMIT", DefaultRAGResultLimit, 1, 10)
	cfg.RAGResultLimit = ragResults
	problems = Push(problems, badRAGResults)
	ragContext, badRAGContext := Num(raw, "RAG_MAX_CONTEXT_CHARS", DefaultRAGMaxContextChars, 256, 20000)
	cfg.RAGMaxContextChars = ragContext
	problems = Push(problems, badRAGContext)

	var prompt = strings.TrimSpace(raw["SYSTEM_PROMPT"])
	if prompt == "" {
		prompt = DefaultSystemPrompt
	}
	cfg.SystemPrompt = prompt

	var ids, badIDs = UserIDs(raw)
	cfg.UserIDs = ids
	cfg.DenyByDefault = len(ids) == 0
	problems = Push(problems, badIDs)
	var adminIDs, badAdminIDs = AdminUserIDs(raw)
	cfg.AdminIDs = adminIDs
	problems = Push(problems, badAdminIDs)

	var rate, badRate = Num(raw, "RATE_LIMIT_PER_MINUTE", DefaultRateLimit, 0, 1000)
	cfg.RateLimitPerMinute = rate
	problems = Push(problems, badRate)

	var concurrent, badConcurrent = Num(raw, "MAX_CONCURRENT_MODEL_REQUESTS", DefaultConcurrent, 1, 16)
	cfg.MaxConcurrentRequests = concurrent
	problems = Push(problems, badConcurrent)

	// texts instead of the commands. The default of the table is the true value.
	var free, badFree = Flag(raw, "PROXY_FREE_TEXT", true)
	cfg.FreeText = free
	problems = Push(problems, badFree)
	tools, badTools := Flag(raw, "TOOLS_ENABLED", DefaultToolsEnabled)
	cfg.ToolsEnabled = tools
	problems = Push(problems, badTools)
	urlReader, badURLReader := Flag(raw, "URL_READER_ENABLED", false)
	cfg.URLReaderEnabled = urlReader
	problems = Push(problems, badURLReader)
	webSearch, badWebSearch := Flag(raw, "WEB_SEARCH_ENABLED", false)
	cfg.WebSearchEnabled = webSearch
	problems = Push(problems, badWebSearch)
	problems = append(problems, loadWebSearch(raw, &cfg)...)

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

func (c Config) IsAdmin(userID int64) bool {
	return HasUserID(c.AdminIDs, userID)
}

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
