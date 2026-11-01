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
	CommandStatus  = "status"
	CommandModel   = "model"
	CommandAsk     = "ask"
	CommandReset   = "reset"
	CommandSkip    = "skip"
	CommandUnskip  = "unskip"
	CommandHistory = "history"
	CommandStats   = "stats"
	CommandWhoAmI  = "whoami"
)

// The user-facing texts of §6 and §12 UX. They live in one place because §12 requires the timeout
// answer to be exact («Модель не ответила за 60 с, попробуй ещё раз»).
const (
	msgAskNeedsText = "Нужен текст вопроса: /ask <текст> (§6)."
	// The §12 exact text, with the number AND the name of the budget that really expired: §7 keeps two
	// budgets (STRATA_TIMEOUT_SECONDS for one model call, TG_REPLY_DEADLINE_SECONDS for the whole answer), and
	// citing a fixed one in the text made the sentence disagree with the events row (see the ask handler).
	msgTimeoutFormat  = "Модель не ответила за %d с, попробуй ещё раз (§7 %s)."
	msgModelError     = "Модель не ответила: %s. Запрос записан в events(kind='model_error') (§10 День 3)."
	msgEmptyAnswer    = "Модель вернула пустой ответ — это ошибка, а не ответ (§10 День 3). Попробуй ещё раз."
	msgNoRoom         = "Все слоты модели заняты (§7 MAX_CONCURRENT_MODEL_REQUESTS). Подожди немного."
	msgModelCheckFail = "STRATA не отвечает: %s (§14.2 readiness)."
	msgHistoryEmpty   = "История чата пуста (§6 /history)."
	msgResetFormat    = "Контекст очищен: модель больше не видит %d сообщений (§5 /reset). История осталась — /history."
)

// auditBudget — how long the audit of a failed call may take once the call itself gave up. The audit of a
// timeout is written after a budget has expired, and an expired context would take the events row of §5 with
// it (see the detached context in the ask handler); the value stays small so nothing waits forever (§9).
const auditBudget = 3 * time.Second

// registry — the §6 table in the order Telegram should show it (§10 День 4 setMyCommands).
func (d *Dispatcher) registry() []Command {
	return []Command{
		{Name: CommandStart, Description: "регистрация чата и список команд", AlwaysAllowed: true, Handler: d.start},
		{Name: CommandHelp, Description: "справка по командам", Handler: d.help},
		{Name: CommandPing, Description: "health бота: pong + uptime", Handler: d.ping},
		{Name: CommandStatus, Description: "стриминг, модель, uptime и ожидание ответа", Handler: d.status},
		{Name: CommandModel, Description: "модель, endpoint, /model check, select и reset", Handler: d.modelInfo},
		{Name: CommandAsk, Description: "вопрос модели", NeedsArgs: true, UsesModel: true, Handler: d.ask},
		{Name: CommandReset, Description: "очистить контекст чата", Handler: d.reset},
		{Name: CommandSkip, Description: "не восстанавливать текущий unanswered-вопрос при старте", Handler: d.skip},
		{Name: CommandUnskip, Description: "снова разрешить recovery ранее пропущенного вопроса", Handler: d.unskip},
		{Name: CommandHistory, Description: "последние N сообщений (по умолчанию 5)", Handler: d.history},
		{Name: CommandStats, Description: "счётчики чата: сообщения, токены, ошибки", Handler: d.stats},
		{Name: CommandWhoAmI, Description: "user_id и chat_id для TELEGRAM_ALLOWED_USERS", AlwaysAllowed: true, Handler: d.whoami},
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
		lines = append(lines, refuseClosed(ev.UserID))
	}
	return d.sendMenu(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

// help — §6 «список команд из реестра».
func (d *Dispatcher) help(ctx context.Context, ev Event, arg string) error {
	return d.sendMenu(ctx, ev.ChatID, strings.Join(d.helpLines(), "\n"))
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

// status — operational diagnostics for an allowed chat without a model call. LastVisibleMessage uses the same
// definition of an unanswered question as startup recovery, including the /reset window of §5.
func (d *Dispatcher) status(ctx context.Context, ev Event, arg string) error {
	var rowID, role, err = store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	var streaming = "выключен"
	if d.deps.Cfg.Streaming {
		streaming = fmt.Sprintf("включён — правки каждые %d мс, idle %d с, total %d с",
			d.deps.Cfg.StreamEditIntervalMS, d.deps.Cfg.StreamIdleSeconds, d.deps.Cfg.StreamTotalSeconds)
	}
	var pending = "нет"
	if rowID > 0 && role == store.RoleUser {
		pending = fmt.Sprintf("да — сообщение §5 id=%d", rowID)
		var skipped, suppress, skipErr = store.ResumeSkip(ctx, d.deps.DB, ev.ChatID)
		if skipErr != nil {
			return skipErr
		}
		if suppress && skipped == rowID {
			pending += "; автовосстановление отключено (/skip)"
		}
	}
	var commit = d.deps.BuildCommit
	if commit == "" {
		commit = "dev"
	}
	var selected, modelErr = d.modelName(ctx, ev.ChatID)
	if modelErr != nil {
		return modelErr
	}
	var lines = []string{
		"Состояние бота (§6 /status):",
		"Build: " + commit,
		"Uptime: " + time.Since(d.created).Round(time.Second).String(),
		"Модель: " + selected,
		"Стриминг: " + streaming,
		"Последний вопрос чата ждёт ответа: " + pending,
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

// skip — suppress startup recovery of only the newest visible unanswered question. The row and its history stay
// untouched, and a newer question has another id, so it does not inherit the suppression.
func (d *Dispatcher) skip(ctx context.Context, ev Event, arg string) error {
	var rowID, role, err = store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if rowID == 0 || role != store.RoleUser {
		return d.reply(ctx, ev.ChatID, "Нет ожидающего ответа вопроса: пропускать нечего (§6 /skip).")
	}
	if err := store.SetResumeSkip(ctx, d.deps.DB, ev.ChatID, rowID); err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf(
		"Автовосстановление вопроса §5 id=%d отключено при следующих запусках. История сохранена; следующий вопрос будет обрабатываться как обычно (§6 /skip).", rowID))
}

// unskip removes this chat's suppression without making an immediate model call. Startup still applies its
// newest-visible-question rule, so an older row does not jump ahead of a newer conversation.
func (d *Dispatcher) unskip(ctx context.Context, ev Event, arg string) error {
	var questionID, found, err = store.ResumeSkip(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if !found {
		return d.reply(ctx, ev.ChatID, "Нет отключённого автовосстановления для этого чата: изменений не требуется (§6 /unskip).")
	}
	if err := store.ClearResumeSkip(ctx, d.deps.DB, ev.ChatID); err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf(
		"Автовосстановление вопроса §5 id=%d снова разрешено. Модель сейчас не вызывается: попытка будет сделана при следующем перезапуске, если этот вопрос останется самым новым без ответа (§6 /unskip).", questionID))
}

// whoami — §6, and §16 change 13: allowed even when the bot is closed, because this is how a user
// learns the id to put into TELEGRAM_ALLOWED_USERS.
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
	// §6 /whoami exists so that a user can learn the id of §7; it would be half an answer without saying
	// whether that id is already in the allowlist and under which ENV name to add it (§16 change 13;
	// §16 change 29 made the name of that row TELEGRAM_ALLOWED_USERS, so there is exactly one to name).
	if d.deps.Cfg.Allows(ev.UserID) {
		lines = append(lines, "в allowlist: да (§7 TELEGRAM_ALLOWED_USERS)")
	} else if d.deps.Cfg.DenyByDefault {
		lines = append(lines, "в allowlist: нет — бот закрыт, добавь "+strconv.FormatInt(ev.UserID, 10)+" в "+AllowlistHint+" и перезапусти (§7, §16 change 13)")
	} else {
		lines = append(lines, "в allowlist: нет — добавь "+strconv.FormatInt(ev.UserID, 10)+" в "+AllowlistHint+" и перезапусти (§7)")
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

// HistoryDefault — the N of §6 `/history [N]` when the argument is absent.
const HistoryDefault = 5

// HistoryMax — the cap of /history: §6 lists "последние N сообщений", and an unbounded N would put
// the whole history into one Telegram message of 4096 symbols (§12 UX).
const HistoryMax = 50

// AllowlistHint — §12 UX: a refused user must see under WHICH ENV name to write the id. §16 change 29
// made TELEGRAM_ALLOWED_USERS the row of §7, so the hint names exactly one thing — the name the operator
// will find when they open .env.
const AllowlistHint = "TELEGRAM_ALLOWED_USERS"

// refuseClosed — the text of §7 «deny by default» for a closed bot (TELEGRAM_ALLOWED_USERS empty, §7).
// Shared by /start (§6) and the allowlist middleware, so the two can never describe the same situation
// differently (§12 UX, §16 change 13).
func refuseClosed(userID int64) string {
	return fmt.Sprintf("Бот закрыт: TELEGRAM_ALLOWED_USERS пуст (§7 deny by default). Твой user_id — %d, впиши его в %s и перезапусти бот.", userID, AllowlistHint)
}

// refuseNotListed — the text for a user who is known but absent from the allowlist (§7). The id is
// printed because that is the only way to learn it without a second bot (§16 change 13).
func refuseNotListed(userID int64) string {
	return fmt.Sprintf("Тебя нет в TELEGRAM_ALLOWED_USERS (§7). Твой user_id — %d, добавь его в %s.", userID, AllowlistHint)
}

// modelName resolves a per-chat override or STRATA_MODEL when the chat has none.
func (d *Dispatcher) modelName(ctx context.Context, chatID int64) (string, error) {
	selected, found, err := store.ChatModel(ctx, d.deps.DB, chatID)
	if err != nil {
		return "", err
	}
	if found {
		return selected, nil
	}
	return d.deps.Cfg.StrataModel, nil
}

// modelInfo — model/endpoint diagnostics and the per-chat select/reset controls.
func (d *Dispatcher) modelInfo(ctx context.Context, ev Event, arg string) error {
	var endpoint = d.deps.Cfg.StrataBase
	var action = strings.TrimSpace(arg)
	if action == "reset" {
		if err := store.ClearChatModel(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, "Для этого чата восстановлена модель по умолчанию: "+d.deps.Cfg.StrataModel)
	}
	if strings.EqualFold(action, "check") || strings.HasPrefix(action, "select") {
		var names, err = d.deps.Model.Models(ctx)
		if err != nil {
			d.logEvent(ctx, ev.UserID, store.EventModelError, "models:"+Sanitize(err, d.deps.Token))
			return d.reply(ctx, ev.ChatID, fmt.Sprintf(msgModelCheckFail, Sanitize(err, d.deps.Token)))
		}
		if len(names) == 0 {
			return d.reply(ctx, ev.ChatID, fmt.Sprintf("STRATA отвечает, но список моделей пуст (§14.2 /models). Endpoint: %s", endpoint))
		}
		if action == "check" {
			return d.reply(ctx, ev.ChatID, fmt.Sprintf("Endpoint: %s\nМодели (%d): %s", endpoint, len(names), strings.Join(names, ", ")))
		}
		var requested = strings.TrimSpace(strings.TrimPrefix(action, "select"))
		if requested == "" {
			var current, currentErr = d.modelName(ctx, ev.ChatID)
			if currentErr != nil {
				return currentErr
			}
			return d.reply(ctx, ev.ChatID, fmt.Sprintf("Текущая модель чата: %s\nДоступны (%d): %s\n/model select <id>, /model reset — вернуть %s",
				current, len(names), strings.Join(names, ", "), d.deps.Cfg.StrataModel))
		}
		var allowed = false
		for _, name := range names {
			if requested == name {
				allowed = true
				break
			}
		}
		if !allowed {
			return d.reply(ctx, ev.ChatID, fmt.Sprintf("Модель %q отсутствует в GET /models. Доступны: %s", requested, strings.Join(names, ", ")))
		}
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatModel(ctx, d.deps.DB, ev.ChatID, requested); err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, "Для этого чата выбрана модель: "+requested+". /model reset — вернуть "+d.deps.Cfg.StrataModel)
	}
	var selected, err = d.modelName(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	var note = ""
	if selected != d.deps.Cfg.StrataModel {
		note = " (override; default " + d.deps.Cfg.StrataModel + ")"
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Модель: %s%s\nEndpoint: %s (§7 STRATA_BASE_URL)\n/model check — проверка доступности; /model select <id>, /model reset.", selected, note, endpoint))
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

	var streamer, sender, canStream = d.streamingPossible()
	var replyBudget = d.deps.Cfg.ReplyDeadlineSeconds
	if canStream {
		// The stream has its own model budget. The ordinary reply budget remains around it for the context read,
		// SQLite write and Telegram delivery; a shorter outer context must not cut STREAM_TOTAL_DEADLINE_SECONDS.
		replyBudget += d.deps.Cfg.StreamTotalSeconds
	}
	var replyCtx, cancel = context.WithTimeout(ctx, time.Duration(replyBudget)*time.Second)
	defer cancel()

	var messages, err = d.BuildMessages(replyCtx, ev.ChatID)
	if err != nil {
		return err
	}
	var selectedModel, modelErr = d.modelName(replyCtx, ev.ChatID)
	if modelErr != nil {
		return modelErr
	}

	// §15 (§16 change 34): with STREAMING_ENABLED=true and everything it needs in place (a client that can
	// stream, a sender that can edit) the answer arrives as ONE message that is rewritten as the model writes
	// it. The two paths return the same Response and the same invariant holds underneath: exactly one assistant
	// row of §5, written after the final answer, never per token (§10 День 3).
	var session *streamSession
	var resp model.Response
	if canStream {
		session = d.newStreamSession(replyCtx, ev.ChatID, sender)
		resp, modelErr = streamer.Stream(replyCtx, model.Request{Model: selectedModel, Messages: messages}, session.handler())
	} else {
		resp, modelErr = d.deps.Model.Complete(replyCtx, model.Request{Model: selectedModel, Messages: messages})
	}
	if modelErr != nil {
		// §10 День 3: a failed call writes NO assistant row — only the event of §5 and the notice.
		if errors.Is(modelErr, context.Canceled) {
			return modelErr // the process is stopping (the signal ctx of §10 День 1): nothing to promise the user
		}
		// The audit of a failure must not ride a context that has already expired. Measured 2026-10-31 the
		// timeout row was written only because the model budget (15 с) is shorter than the reply deadline
		// (30 с): with the budgets inverted it is replyCtx that expires first, and writing the events row of §5
		// through it would drop the row in exactly the case §12 «один сбой STRATA ≠ падение бота» exists for.
		// context.WithoutCancel keeps the values of the request and drops its cancellation; auditBudget gives
		// the write a deadline of its own (§9 «no unbounded work»), and logEvent of dispatch.go reports a failed
		// write to the log instead of hiding it (§12 security: the reason goes through Sanitize there too).
		var auditCtx, stopAudit = context.WithTimeout(context.WithoutCancel(ctx), auditBudget)
		defer stopAudit()
		// §12 надёжность / §10 День 7: the reason of a failure used to exist only in the events table of §5.
		// Measured 2026-10-31: STRATA refused the connection (`dial tcp 127.0.0.1:8080: connectex …`) while
		// the user was answered and the row was written, and `Get-Content -Tail` of the §12 log showed three
		// arriving updates and nothing else — the operator had to open the database to learn that the model
		// was down. One error line per failed call, and the reason goes through Sanitize because a library
		// error is allowed to carry the URL of §7 with the token of Telegram inside (§12 security).
		d.deps.Logger.Error("the model did not answer",
			"chat_id", ev.ChatID,
			"user_id", ev.UserID,
			"error", Sanitize(modelErr, d.deps.Token),
		)
		// §15: part of the answer can already be on the screen. The message that carries it says so out loud,
		// because §10 День 3 writes NO assistant row for a failed call and a half answer that looks finished is
		// worse than no answer (§12 UX). The reason is not cut here: brokenView of stream.go fits the notice into
		// ONE message of MAX_MESSAGE_CHUNK (§7), and only the part of the reason past that limit disappears.
		if session != nil && session.sawText() {
			session.broken(auditCtx, d, Sanitize(modelErr, d.deps.Token))
		}
		if errors.Is(modelErr, context.DeadlineExceeded) {
			// The number in the user text and the detail in the events row must name the SAME budget — the
			// one that really expired. Measured 2026-10-31 (STRATA_TIMEOUT_SECONDS=15, TG_REPLY_DEADLINE_SECONDS=30):
			// the row read «budget 30s» while the user was answered «не ответила за 15 с» — one failure described
			// by two different numbers in the two places an operator compares (the events table of §5 and the
			// chat of §12 UX). replyCtx.Err() tells them apart: nil means the model call gave up first.
			// §15 added two more budgets that can expire (STREAM_IDLE_TIMEOUT_SECONDS,
			// STREAM_TOTAL_DEADLINE_SECONDS of §7), and their sentinels say which one it was — a stream that went
			// silent and a stream that ran too long are different failures, and neither is STRATA_TIMEOUT_SECONDS.
			var spent = "STRATA_TIMEOUT_SECONDS"
			var seconds = d.deps.Cfg.TimeoutSeconds
			switch {
			case errors.Is(modelErr, model.ErrStreamStalled):
				spent, seconds = "STREAM_IDLE_TIMEOUT_SECONDS", d.deps.Cfg.StreamIdleSeconds
			case errors.Is(modelErr, model.ErrStreamTooLong):
				spent, seconds = "STREAM_TOTAL_DEADLINE_SECONDS", d.deps.Cfg.StreamTotalSeconds
			case replyCtx.Err() != nil:
				spent = "TG_REPLY_DEADLINE_SECONDS"
				seconds = d.deps.Cfg.ReplyDeadlineSeconds
			}
			var detail = fmt.Sprintf("%s %ds spent", spent, seconds)
			if session != nil {
				// §15: how much of the answer the user actually read. Without it the events row says «the answer
				// was too long» and cannot say whether the model wrote 40 symbols or 4000 before it stopped.
				var edits, skipped, failures = session.stats()
				detail += fmt.Sprintf(", %d runes delivered, %d edits, %d skipped, %d delivery failures",
					Runes(resp.Content), edits, skipped, failures)
			}
			d.logEvent(auditCtx, ev.UserID, store.EventTimeout, detail)
			return d.reply(ctx, ev.ChatID, fmt.Sprintf(msgTimeoutFormat, seconds, spent))
		}
		if errors.Is(modelErr, model.ErrEmptyAnswer) {
			d.logEvent(auditCtx, ev.UserID, store.EventModelError, "empty_answer")
			return d.reply(ctx, ev.ChatID, msgEmptyAnswer)
		}
		if errors.Is(modelErr, model.ErrNoRoom) {
			d.logEvent(auditCtx, ev.UserID, store.EventModelError, "no_room")
			return d.reply(ctx, ev.ChatID, msgNoRoom)
		}
		d.logEvent(auditCtx, ev.UserID, store.EventModelError, Sanitize(modelErr, d.deps.Token))
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
		Model:     replyModel(resp.Model, selectedModel),
		LatencyMs: resp.Latency.Milliseconds(),
	})
	if writeErr != nil {
		return writeErr
	}

	// §10 День 7 «логи slog (JSON) с chat_id, latency_ms, model»: the answered question is the one line an
	// operator greps for. The TEXT stays out of it (§12 security) — the question and the answer are rows of
	// §5, stdout only carries the measurement. A usage the provider did not return stays `null` rather than
	// becoming a confident zero, which is what the footer of §6 says too («токены не возвращены»).
	//
	// §15 adds the delivery side: an answer that arrived in 4 seconds and produced zero edits reached the chat
	// through the fallback of deliver(), and without these numbers this line says «everything is fine» about a
	// stream that never streamed.
	var edits, skipped, failures = 0, 0, 0
	if session != nil {
		edits, skipped, failures = session.stats()
	}
	d.deps.Logger.Info("the model answered",
		"chat_id", ev.ChatID,
		"user_id", ev.UserID,
		"model", replyModel(resp.Model, selectedModel),
		"latency_ms", resp.Latency.Milliseconds(),
		"attempts", resp.Attempts,
		"tokens_in", tokensOrNull(resp.TokensIn),
		"tokens_out", tokensOrNull(resp.TokensOut),
		"streamed", session != nil,
		"edits", edits,
		"edits_skipped_by_cadence", skipped,
		"delivery_failures", failures,
	)

	// §6: «ответ модели + ⏱ latency, tokens». The footer travels in the same text so the chunking of
	// §12 UX applies to it too.
	var full = resp.Content + "\n\n" + latencyLine(resp, selectedModel)
	if session == nil {
		return d.Send(replyCtx, ev.ChatID, full)
	}
	// §15: the message the chat watched grow takes the first chunk, and only the tail of MAX_MESSAGE_CHUNK
	// continues as new messages — a chat that followed one message being rewritten must not see it split in two.
	return session.deliver(replyCtx, d, full)
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
	// A timeout is a kind of its own in §5, so it is NOT inside the counter above. Measured 2026-10-31: right
	// after the bot had answered «Модель не ответила за 15 с, попробуй ещё раз» this line still read
	// «Ошибок модели: 3» — which reads to an operator as nothing having happened. The line shows up only when
	// a timeout happened, the way the two optional lines below do (§6 /stats: messages, tokens, errors).
	if stats.Timeouts > 0 {
		lines = append(lines, fmt.Sprintf("Таймаутов модели: %d (§5 events kind='timeout', §7 budgets)", stats.Timeouts))
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
