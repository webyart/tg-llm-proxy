// commands.go — the registry of §6. §10 День 4 DoD names every row of the §6 table, and §6 wants the
// registry a slice so that "v2 расширяется добавлением одной записи (без правок диспетчера)": adding a
// command here is enough — Dispatch resolves names from the index, /help lists it and setMyCommands
// of run.go publishes it (§10 День 4: "setMyCommands при старте").
package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

// The command names of §6, lowercase because Normalize lowercases before the lookup (§6 п.2).
const (
	CommandStart   = "start"
	CommandHelp    = "help"
	CommandPing    = "ping"
	CommandModel   = "model"
	CommandAsk     = "ask"
	CommandReset   = "reset"
	CommandHistory = "history"
	CommandStats   = "stats"
	CommandWhoAmI  = "whoami"
)

// The user-facing texts of §6 and §12 UX. They live in one place because §12 requires the timeout
// answer to be exact («Модель не ответила за 60 с, попробуй ещё раз»).
const (
	msgAskNeedsText   = "Нужен текст вопроса: /ask <текст> (§6)."
	msgTimeoutFormat  = "Модель не ответила за %d с, попробуй ещё раз (§7 STRATA_TIMEOUT_SECONDS)."
	msgModelError     = "Модель не ответила: %s. Запрос записан в events(kind='model_error') (§10 День 3)."
	msgEmptyAnswer    = "Модель вернула пустой ответ — это ошибка, а не ответ (§10 День 3). Попробуй ещё раз."
	msgNoRoom         = "Все слоты модели заняты (§7 MAX_CONCURRENT_MODEL_REQUESTS). Подожди немного."
	msgModelCheckFail = "STRATA не отвечает: %s (§14.2 readiness)."
	msgHistoryEmpty   = "История чата пуста (§6 /history)."
	msgResetFormat    = "Контекст очищен: модель больше не видит %d сообщений (§5 /reset). История осталась — /history."
)

// registry — the §6 table in the order Telegram should show it (§10 День 4 setMyCommands).
func (d *Dispatcher) registry() []Command {
	return []Command{
		{Name: CommandStart, Description: "регистрация чата и список команд", AlwaysAllowed: true, Handler: d.start},
		{Name: CommandHelp, Description: "справка по командам", Handler: d.help},
		{Name: CommandPing, Description: "health бота: pong + uptime", Handler: d.ping},
		{Name: CommandModel, Description: "модель, endpoint и проверка STRATA (/model check)", Handler: d.modelInfo},
		{Name: CommandAsk, Description: "вопрос модели", NeedsArgs: true, UsesModel: true, Handler: d.ask},
		{Name: CommandReset, Description: "очистить контекст чата", Handler: d.reset},
		{Name: CommandHistory, Description: "последние N сообщений (по умолчанию 5)", Handler: d.history},
		{Name: CommandStats, Description: "счётчики чата: сообщения, токены, ошибки", Handler: d.stats},
		{Name: CommandWhoAmI, Description: "user_id и chat_id для ALLOWED_USER_IDS", AlwaysAllowed: true, Handler: d.whoami},
	}
}

// start — §6 «регистрация чата в SQLite». EnsureChat is the upsert of §5, so a chat that spoke before
// /start (a group, for example) is registered without losing its title.
func (d *Dispatcher) start(ctx context.Context, ev Event, arg string) error {
	var err = store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle)
	if err != nil {
		return err
	}
	var lines = []string{"Привет! Я прокси к локальной модели STRATA (§1). Команды:"}
	lines = append(lines, d.helpLines()...)
	if d.deps.Cfg.DenyByDefault {
		lines = append(lines, fmt.Sprintf(
			"Бот закрыт: ALLOWED_USER_IDS пуст (§7 deny by default). Твой user_id — %d, "+
				"впиши его в ALLOWED_USER_IDS и перезапусти бот.", ev.UserID))
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

// help — §6 «список команд из реестра».
func (d *Dispatcher) help(ctx context.Context, ev Event, arg string) error {
	return d.reply(ctx, ev.ChatID, strings.Join(d.helpLines(), "\n"))
}

// helpLines — one line per registry entry, so /help can never drift from the registry (§6).
func (d *Dispatcher) helpLines() []string {
	var lines = []string{}
	var i = 0
	for i < len(d.commands) {
		var cmd = d.commands[i]
		var usage = "/" + cmd.Name
		if cmd.NeedsArgs {
			usage += " <текст>"
		}
		lines = append(lines, fmt.Sprintf("%s — %s", usage, cmd.Description))
		i++
	}
	return lines
}

// ping — §6 `pong <uptime>`. The uptime of the process is what the health answer of §10 День 5 needs
// too, so the clock starts when the dispatcher is built (§12 надёжность).
func (d *Dispatcher) ping(ctx context.Context, ev Event, arg string) error {
	var up = time.Since(d.created).Round(time.Second)
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("pong %s (§6 /ping)", up))
}

// whoami — §6, and §16 change 13: allowed even when the bot is closed, because this is how a user
// learns the id to put into ALLOWED_USER_IDS.
func (d *Dispatcher) whoami(ctx context.Context, ev Event, arg string) error {
	var lines = []string{fmt.Sprintf("user_id: %d", ev.UserID), fmt.Sprintf("chat_id: %d", ev.ChatID)}
	if ev.ChatType != "" {
		lines = append(lines, "chat_type: "+ev.ChatType)
	}
	if ev.ChatTitle != "" {
		lines = append(lines, "chat_title: "+ev.ChatTitle)
	}
	if ev.Username != "" {
		lines = append(lines, "username: @"+ev.Username)
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

// HistoryDefault — the N of §6 `/history [N]` when the argument is absent.
const HistoryDefault = 5

// HistoryMax — the cap of /history: §6 lists "последние N сообщений", and an unbounded N would put
// the whole history into one Telegram message of 4096 symbols (§12 UX).
const HistoryMax = 50

// modelInfo — §6: имя модели, endpoint, доступность STRATA; `/model check` calls GET {base}/models
// through the same client (§4 internal/health probes the same URL, §2).
func (d *Dispatcher) modelInfo(ctx context.Context, ev Event, arg string) error {
	var endpoint = d.deps.Cfg.StrataBase
	var model = d.deps.Cfg.StrataModel
	if strings.EqualFold(strings.TrimSpace(arg), "check") {
		var names, err = d.deps.Model.Models(ctx)
		if err != nil {
			d.logEvent(ctx, ev.UserID, store.EventModelError, "models:"+Sanitize(err, d.deps.Token))
			return d.reply(ctx, ev.ChatID, fmt.Sprintf(msgModelCheckFail, Sanitize(err, d.deps.Token)))
		}
		if len(names) == 0 {
			return d.reply(ctx, ev.ChatID, fmt.Sprintf("STRATA отвечает, но список моделей пуст (§14.2 /models). Endpoint: %s", endpoint))
		}
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Endpoint: %s\nМодели (%d): %s", endpoint, len(names), strings.Join(names, ", ")))
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Модель: %s\nEndpoint: %s (§7 STRATA_BASE_URL)\n/model check — проверка доступности.", model, endpoint))
}

// ask — the main path of §6. The budget of §7 (TG_REPLY_DEADLINE_SECONDS) is opened HERE, not in the
// adapter: §7 defines it as «ретраи модели + таймаут + отправку ответа», so the queue behind the
// per-chat lock of §12 must not eat it. The write invariant of §10 День 3 is this function's
// responsibility: exactly one assistant row, and only after the final successful Response.
func (d *Dispatcher) ask(ctx context.Context, ev Event, arg string) error {
	var text = strings.TrimSpace(arg)
	if text == "" {
		return d.reply(ctx, ev.ChatID, msgAskNeedsText)
	}

	var replyCtx, cancel = context.WithTimeout(ctx, time.Duration(d.deps.Cfg.ReplyDeadlineSeconds)*time.Second)
	defer cancel()

	var messages, err = d.BuildMessages(replyCtx, ev.ChatID)
	if err != nil {
		return err
	}

	var resp, modelErr = d.deps.Model.Complete(replyCtx, model.Request{Messages: messages})
	if modelErr != nil {
		// §10 День 3: a failed call writes NO assistant row — only the event of §5 and the notice.
		if errors.Is(modelErr, context.Canceled) {
			return modelErr // the process is stopping (§12.1.4): nothing to promise the user
		}
		if errors.Is(modelErr, context.DeadlineExceeded) {
			d.logEvent(replyCtx, ev.UserID, store.EventTimeout, fmt.Sprintf("budget %ds", d.deps.Cfg.ReplyDeadlineSeconds))
			return d.reply(ctx, ev.ChatID, fmt.Sprintf(msgTimeoutFormat, d.deps.Cfg.TimeoutSeconds))
		}
		if errors.Is(modelErr, model.ErrEmptyAnswer) {
			d.logEvent(replyCtx, ev.UserID, store.EventModelError, "empty_answer")
			return d.reply(ctx, ev.ChatID, msgEmptyAnswer)
		}
		if errors.Is(modelErr, model.ErrNoRoom) {
			d.logEvent(replyCtx, ev.UserID, store.EventModelError, "no_room")
			return d.reply(ctx, ev.ChatID, msgNoRoom)
		}
		d.logEvent(replyCtx, ev.UserID, store.EventModelError, Sanitize(modelErr, d.deps.Token))
		return d.reply(ctx, ev.ChatID, fmt.Sprintf(msgModelError, Sanitize(modelErr, d.deps.Token)))
	}

	// The ONE row of §10 День 3 («ответ модели пишется ровно одна запись assistant»).
	var _, writeErr = store.Append(replyCtx, d.deps.DB, store.Message{
		ChatID:    ev.ChatID,
		UserID:    ev.UserID,
		Role:      store.RoleAssistant,
		Content:   resp.Content,
		TokensIn:  resp.TokensIn,
		TokensOut: resp.TokensOut,
		Model:     replyModel(resp.Model, d.deps.Cfg.StrataModel),
		LatencyMs: resp.Latency.Milliseconds(),
	})
	if writeErr != nil {
		return writeErr
	}

	// §10 День 7 «логи slog (JSON) с chat_id, latency_ms, model»: the answered question is the one line an
	// operator greps for. The TEXT stays out of it (§12 security) — the question and the answer are rows of
	// §5, stdout only carries the measurement. A usage the provider did not return stays `null` rather than
	// becoming a confident zero, which is what the footer of §6 says too («токены не возвращены»).
	d.deps.Logger.Info("the model answered",
		"chat_id", ev.ChatID,
		"user_id", ev.UserID,
		"model", replyModel(resp.Model, d.deps.Cfg.StrataModel),
		"latency_ms", resp.Latency.Milliseconds(),
		"attempts", resp.Attempts,
		"tokens_in", tokensOrNull(resp.TokensIn),
		"tokens_out", tokensOrNull(resp.TokensOut),
	)

	// §6: «ответ модели + ⏱ latency, tokens». The footer travels in the same text so the chunking of
	// §12 UX applies to it too.
	return d.Send(replyCtx, ev.ChatID, resp.Content+"\n\n"+latencyLine(resp, d.deps.Cfg.StrataModel))
}

// reset — §6 «очистить контекст чата» + подтверждение. §5 deletes nothing: /history still sees the rows.
func (d *Dispatcher) reset(ctx context.Context, ev Event, arg string) error {
	var hidden, err = store.ClearChat(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf(msgResetFormat, hidden))
}

// history — §6 «последние N сообщений (default 5)». HistoryOfChat includes the rows a /reset hid (§5).
func (d *Dispatcher) history(ctx context.Context, ev Event, arg string) error {
	var limit = HistoryDefault
	if trimmed := strings.TrimSpace(arg); trimmed != "" {
		var n, err = strconv.Atoi(trimmed)
		if err != nil || n <= 0 {
			return d.reply(ctx, ev.ChatID, fmt.Sprintf("/history ждёт число: /history 10 (по умолчанию %d).", HistoryDefault))
		}
		if n > HistoryMax {
			n = HistoryMax
		}
		limit = n
	}
	var rows, err = store.HistoryOfChat(ctx, d.deps.DB, ev.ChatID, limit)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return d.reply(ctx, ev.ChatID, msgHistoryEmpty)
	}
	var lines = []string{fmt.Sprintf("Последние %d сообщений (§6 /history):", len(rows))}
	var i = 0
	for i < len(rows) {
		var row = rows[i]
		lines = append(lines, fmt.Sprintf("%d. %s: %s", i+1, row.Role, brief(row.Content, 120)))
		i++
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

// stats — §6 «число сообщений, токенов, ошибок».
func (d *Dispatcher) stats(ctx context.Context, ev Event, arg string) error {
	var stats, err = store.Stats(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	var lines = []string{
		fmt.Sprintf("Сообщений: %d (§5 messages)", stats.Messages),
		fmt.Sprintf("Токенов: %d входящих, %d исходящих (§5 tokens_in/tokens_out)", stats.TokensIn, stats.TokensOut),
		fmt.Sprintf("Ошибок модели: %d (§5 events kind='model_error')", stats.Errors),
	}
	if stats.Model != "" {
		lines = append(lines, "Модель: "+stats.Model)
	}
	if stats.LastAt != "" {
		lines = append(lines, "Последнее сообщение: "+stats.LastAt+" (UTC — §5 created_at)")
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

// replyModel — the model column of §5 stays filled even when the provider echoes nothing (§5 keeps
// messages.model nullable, but /stats and the footer of §6 want a name).
func replyModel(fromResponse string, configured string) string {
	if strings.TrimSpace(fromResponse) != "" {
		return fromResponse
	}
	return configured
}

// tokensOrNull — the JSON log of §10 День 7 keeps an unknown usage unknown: slog renders nil as `null`,
// which is honest, where a 0 would invent a number the §6 footer refuses to show («токены не возвращены»).
func tokensOrNull(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// latencyLine — the «⏱ latency, tokens» of §6. Latency covers the whole call including the retries
// (model.Response.Latency, §10 День 3), because that is what the user waited for (§12 UX).
func latencyLine(resp model.Response, configured string) string {
	var name = replyModel(resp.Model, configured)
	if resp.TokensIn == nil && resp.TokensOut == nil {
		return fmt.Sprintf("⏱ %s · модель %s · попыток %d (токены не возвращены)",
			resp.Latency.Round(time.Millisecond), name, resp.Attempts)
	}
	var in = int64(0)
	var out = int64(0)
	if resp.TokensIn != nil {
		in = *resp.TokensIn
	}
	if resp.TokensOut != nil {
		out = *resp.TokensOut
	}
	return fmt.Sprintf("⏱ %s · %d→%d токенов · модель %s · попыток %d",
		resp.Latency.Round(time.Millisecond), in, out, name, resp.Attempts)
}

// brief — the one-line form of /history (§12 UX: the reply must fit the chunk of §7).
func brief(text string, maxRunes int) string {
	var runes = []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "…"
}
