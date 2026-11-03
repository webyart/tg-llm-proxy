// registry a slice so that "v2 расширяется добавлением одной записи (без правок диспетчера)": adding a
// command here is enough — Dispatch resolves names from the index, /help lists it and setMyCommands
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

const (
	CommandStart   = "start"
	CommandHelp    = "help"
	CommandPing    = "ping"
	CommandStatus  = "status"
	CommandModel   = "model"
	CommandStream  = "stream"
	CommandPersona = "persona"
	CommandAsk     = "ask"
	CommandReset   = "reset"
	CommandSkip    = "skip"
	CommandUnskip  = "unskip"
	CommandHistory = "history"
	CommandStats   = "stats"
	CommandWhoAmI  = "whoami"
	CommandAdmin   = "admin"
)

// answer to be exact («Модель не ответила за 60 с, попробуй ещё раз»).
const (
	msgAskNeedsText = "Нужен текст вопроса: /ask <текст>."
	// budgets (STRATA_TIMEOUT_SECONDS for one model call, TG_REPLY_DEADLINE_SECONDS for the whole answer), and
	// citing a fixed one in the text made the sentence disagree with the events row (see the ask handler).
	msgTimeoutFormat  = "Модель не ответила за %d с (%s), попробуй ещё раз."
	msgModelError     = "Модель не ответила: %s. Запрос записан в events(kind='model_error')."
	msgEmptyAnswer    = "Модель вернула пустой ответ — это ошибка, а не ответ. Попробуй ещё раз."
	msgNoRoom         = "Все слоты модели заняты. Подожди немного."
	msgModelCheckFail = "STRATA не отвечает: %s."
	msgHistoryEmpty   = "История чата пуста."
	msgResetFormat    = "Контекст очищен: модель больше не видит %d сообщений. История осталась — /history."
)

// auditBudget — how long the audit of a failed call may take once the call itself gave up. The audit of a
const auditBudget = 3 * time.Second

func (d *Dispatcher) registry() []Command {
	return []Command{
		{Name: CommandStart, Description: "регистрация чата и список команд", AlwaysAllowed: true, Handler: d.start},
		{Name: CommandHelp, Description: "справка по командам", Handler: d.help},
		{Name: CommandPing, Description: "health бота: pong + uptime", Handler: d.ping},
		{Name: CommandStatus, Description: "стриминг, модель, uptime и ожидание ответа", Handler: d.status},
		{Name: CommandModel, Description: "модель, endpoint, /model check, select и reset", Handler: d.modelInfo},
		{Name: CommandStream, Description: "стриминг для этого чата: on, off, reset", Handler: d.streamControl},
		{Name: CommandPersona, Description: "роль модели для этого чата: пресеты, custom, reset", Handler: d.personaControl},
		{Name: CommandAsk, Description: "вопрос модели", NeedsArgs: true, UsesModel: true, Handler: d.ask},
		{Name: CommandReset, Description: "очистить контекст чата", Handler: d.reset},
		{Name: CommandSkip, Description: "не восстанавливать текущий unanswered-вопрос при старте", Handler: d.skip},
		{Name: CommandUnskip, Description: "снова разрешить recovery ранее пропущенного вопроса", Handler: d.unskip},
		{Name: CommandHistory, Description: "последние N сообщений (по умолчанию 5)", Handler: d.history},
		{Name: CommandStats, Description: "счётчики чата: сообщения, токены, ошибки", Handler: d.stats},
		{Name: CommandWhoAmI, Description: "user_id и chat_id для TELEGRAM_ALLOWED_USERS", AlwaysAllowed: true, Handler: d.whoami},
		{Name: CommandAdmin, Description: "агрегированные метрики для администратора", Handler: d.admin},
	}
}

// /start (a group, for example) is registered without losing its title.
func (d *Dispatcher) start(ctx context.Context, ev Event, arg string) error {
	var err = store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle)
	if err != nil {
		return err
	}
	var lines = []string{"Привет! Я прокси к локальной модели STRATA. Команды:"}
	lines = append(lines, d.helpLines()...)
	if d.deps.Cfg.DenyByDefault {
		lines = append(lines, refuseClosed(ev.UserID))
	}
	return d.sendMenu(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

func (d *Dispatcher) help(ctx context.Context, ev Event, arg string) error {
	return d.sendMenu(ctx, ev.ChatID, strings.Join(d.helpLines(), "\n"))
}

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
	lines = append(lines, "Можно отправить фото JPEG/PNG или текстовый документ UTF-8 с подписью-вопросом.")
	return lines
}

func (d *Dispatcher) ping(ctx context.Context, ev Event, arg string) error {
	var up = time.Since(d.created).Round(time.Second)
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("pong %s ", up))
}

// status — operational diagnostics for an allowed chat without a model call. LastVisibleMessage uses the same
func (d *Dispatcher) status(ctx context.Context, ev Event, arg string) error {
	var rowID, role, err = store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	streamEnabled, streamOverride, streamErr := d.streamingEnabled(ctx, ev.ChatID)
	if streamErr != nil {
		return streamErr
	}
	var streaming = "выключен"
	if streamEnabled {
		streaming = fmt.Sprintf("включён — правки каждые %d мс, idle %d с, total %d с",
			d.deps.Cfg.StreamEditIntervalMS, d.deps.Cfg.StreamIdleSeconds, d.deps.Cfg.StreamTotalSeconds)
	}
	if streamOverride {
		streaming += " (override этого чата)"
	}
	var pending = "нет"
	if rowID > 0 && role == store.RoleUser {
		pending = fmt.Sprintf("да — сообщение id=%d", rowID)
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
	_, persona, personaOverride, personaErr := d.effectivePersona(ctx, ev.ChatID)
	if personaErr != nil {
		return personaErr
	}
	if personaOverride {
		persona += " (override этого чата)"
	}
	var lines = []string{
		"Состояние бота:",
		"Build: " + commit,
		"Uptime: " + time.Since(d.created).Round(time.Second).String(),
		"Модель: " + selected,
		"Persona: " + persona,
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
		return d.reply(ctx, ev.ChatID, "Нет ожидающего ответа вопроса: пропускать нечего.")
	}
	if err := store.SetResumeSkip(ctx, d.deps.DB, ev.ChatID, rowID); err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf(
		"Автовосстановление вопроса id=%d отключено при следующих запусках. История сохранена; следующий вопрос будет обрабатываться как обычно.", rowID))
}

// unskip removes this chat's suppression without making an immediate model call. Startup still applies its
// newest-visible-question rule, so an older row does not jump ahead of a newer conversation.
func (d *Dispatcher) unskip(ctx context.Context, ev Event, arg string) error {
	var questionID, found, err = store.ResumeSkip(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if !found {
		return d.reply(ctx, ev.ChatID, "Нет отключённого автовосстановления для этого чата: изменений не требуется.")
	}
	if err := store.ClearResumeSkip(ctx, d.deps.DB, ev.ChatID); err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf(
		"Автовосстановление вопроса id=%d снова разрешено. Модель сейчас не вызывается: попытка будет сделана при следующем перезапуске, если этот вопрос останется самым новым без ответа и не превысит RECOVERY_MAX_AGE_HOURS.", questionID))
}

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
	if d.deps.Cfg.Allows(ev.UserID) {
		lines = append(lines, "в allowlist: да ")
	} else if d.deps.Cfg.DenyByDefault {
		lines = append(lines, "в allowlist: нет — бот закрыт, добавь "+strconv.FormatInt(ev.UserID, 10)+" в "+AllowlistHint+" и перезапусти ")
	} else {
		lines = append(lines, "в allowlist: нет — добавь "+strconv.FormatInt(ev.UserID, 10)+" в "+AllowlistHint+" и перезапусти ")
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

const HistoryDefault = 5

const HistoryMax = 50

// will find when they open.env.
const AllowlistHint = "TELEGRAM_ALLOWED_USERS"

func refuseClosed(userID int64) string {
	return fmt.Sprintf("Бот закрыт: TELEGRAM_ALLOWED_USERS пуст. Твой user_id — %d, впиши его в %s и перезапусти бот.", userID, AllowlistHint)
}

func refuseNotListed(userID int64) string {
	return fmt.Sprintf("Тебя нет в TELEGRAM_ALLOWED_USERS. Твой user_id — %d, добавь его в %s.", userID, AllowlistHint)
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
			return d.reply(ctx, ev.ChatID, fmt.Sprintf("STRATA отвечает, но список моделей пуст. Endpoint: %s", endpoint))
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
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Модель: %s%s\nEndpoint: %s \n/model check — проверка доступности; /model select <id>, /model reset.", selected, note, endpoint))
}

// responsibility: exactly one assistant row, and only after the final successful Response.
func (d *Dispatcher) ask(ctx context.Context, ev Event, arg string) error {
	if ev.UnsupportedMedia {
		return d.reply(ctx, ev.ChatID, "Этот тип вложения пока не поддерживается. Отправь фото или небольшой текстовый документ.")
	}
	if ev.Recovery && isStoredAttachment(ev.Text) && ev.Attachment == nil {
		return d.reply(ctx, ev.ChatID, "Вложение недоступно после перезапуска, поэтому отправь вложение повторно вместе с вопросом.")
	}
	var text = strings.TrimSpace(arg)
	if ev.Attachment != nil {
		text = attachmentPrompt(ev)
	}
	if text == "" {
		return d.reply(ctx, ev.ChatID, msgAskNeedsText)
	}

	streamer, sender, canStream, streamErr := d.streamingPossible(ctx, ev.ChatID)
	if streamErr != nil {
		return streamErr
	}
	var replyBudget = d.deps.Cfg.ReplyDeadlineSeconds
	if canStream {
		// The stream has its own model budget. The ordinary reply budget remains around it for the context read,
		// SQLite write and Telegram delivery; a shorter outer context must not cut STREAM_TOTAL_DEADLINE_SECONDS.
		replyBudget += d.deps.Cfg.StreamTotalSeconds
	}
	var replyCtx, cancel = context.WithTimeout(ctx, time.Duration(replyBudget)*time.Second)
	defer cancel()

	var downloaded *DownloadedAttachment
	if ev.Attachment != nil {
		if d.deps.Attachments == nil {
			return d.reply(ctx, ev.ChatID, attachmentUserError(ErrAttachmentUnavailable))
		}
		file, downloadErr := d.deps.Attachments.Download(replyCtx, *ev.Attachment)
		if downloadErr != nil {
			if errors.Is(downloadErr, context.Canceled) {
				return downloadErr
			}
			noticeCtx, stopNotice := context.WithTimeout(context.WithoutCancel(ctx), auditBudget)
			defer stopNotice()
			d.logEvent(noticeCtx, ev.UserID, store.EventModelError, "attachment:"+attachmentErrorKind(downloadErr))
			return d.reply(noticeCtx, ev.ChatID, attachmentUserError(downloadErr))
		}
		downloaded = &file
	}

	var messages, err = d.BuildMessages(replyCtx, ev.ChatID)
	if err != nil {
		return err
	}
	if downloaded != nil {
		if len(messages) == 0 || messages[len(messages)-1].Role != model.RoleUser {
			return errors.New("the stored attachment question is not the newest model message")
		}
		last := &messages[len(messages)-1]
		last.Content = text
		switch ev.Attachment.Kind {
		case AttachmentPhoto:
			last.ImageURL, err = imageDataURI(*downloaded)
		case AttachmentDocument:
			if !documentIsText(ev.Attachment.FileName, ev.Attachment.MimeType, downloaded.Data) {
				err = ErrAttachmentType
			} else {
				last.Content = documentModelPrompt(text, safeAttachmentName(ev.Attachment.FileName), string(downloaded.Data), int(d.deps.Cfg.MaxContextChars))
			}
		default:
			err = ErrAttachmentType
		}
		if err != nil {
			return d.reply(ctx, ev.ChatID, attachmentUserError(err))
		}
	}
	var selectedModel, modelErr = d.modelName(replyCtx, ev.ChatID)
	if modelErr != nil {
		return modelErr
	}

	// (a client that can stream, a sender that can edit) the answer arrives as ONE message rewritten as the model writes
	// it. The two paths return the same Response and the same invariant holds underneath: exactly one assistant
	var session *streamSession
	var resp model.Response
	if canStream {
		session = d.newStreamSession(replyCtx, ev.ChatID, sender)
		resp, modelErr = streamer.Stream(replyCtx, model.Request{Model: selectedModel, Messages: messages}, session.handler())
	} else {
		resp, modelErr = d.deps.Model.Complete(replyCtx, model.Request{Model: selectedModel, Messages: messages})
	}
	if modelErr != nil {
		if errors.Is(modelErr, context.Canceled) {
			return modelErr // the process is stopping : nothing to promise the user
		}
		// The audit of a failure must not ride a context that has already expired. Measured 2026-10-31 the
		// timeout row was written only because the model budget (15 с) is shorter than the reply deadline
		// context.WithoutCancel keeps the values of the request and drops its cancellation; auditBudget gives
		var auditCtx, stopAudit = context.WithTimeout(context.WithoutCancel(ctx), auditBudget)
		defer stopAudit()
		// Measured 2026-10-31: STRATA refused the connection (`dial tcp 127.0.0.1:8080: connectex …`) while
		// arriving updates and nothing else — the operator had to open the database to learn that the model
		// was down. One error line per failed call, and the reason goes through Sanitize because a library
		d.deps.Logger.Error("the model did not answer",
			"chat_id", ev.ChatID,
			"user_id", ev.UserID,
			"error", Sanitize(modelErr, d.deps.Token),
		)
		if session != nil && session.sawText() {
			session.broken(auditCtx, d, Sanitize(modelErr, d.deps.Token))
		}
		if errors.Is(modelErr, context.DeadlineExceeded) {
			// The number in the user text and the detail in the events row must name the SAME budget — the
			// one that really expired. Measured 2026-10-31 (STRATA_TIMEOUT_SECONDS=15, TG_REPLY_DEADLINE_SECONDS=30):
			// the row read «budget 30s» while the user was answered «не ответила за 15 с» — one failure described
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

	//
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

	var full = resp.Content + "\n\n" + latencyLine(resp, selectedModel)
	if session == nil {
		return d.Send(replyCtx, ev.ChatID, full)
	}
	// continues as new messages — a chat that followed one message being rewritten must not see it split in two.
	return session.deliver(replyCtx, d, full)
}

func (d *Dispatcher) reset(ctx context.Context, ev Event, arg string) error {
	var hidden, err = store.ClearChat(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf(msgResetFormat, hidden))
}

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
	var lines = []string{fmt.Sprintf("Последние %d сообщений :", len(rows))}
	var i = 0
	for i < len(rows) {
		var row = rows[i]
		lines = append(lines, fmt.Sprintf("%d. %s: %s", i+1, row.Role, brief(row.Content, 120)))
		i++
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

func (d *Dispatcher) stats(ctx context.Context, ev Event, arg string) error {
	var stats, err = store.Stats(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	var lines = []string{
		fmt.Sprintf("Сообщений: %d ", stats.Messages),
		fmt.Sprintf("Токенов: %d входящих, %d исходящих ", stats.TokensIn, stats.TokensOut),
		fmt.Sprintf("Ошибок модели: %d ", stats.Errors),
	}
	// after the bot had answered «Модель не ответила за 15 с, попробуй ещё раз» this line still read
	// «Ошибок модели: 3» — which reads to an operator as nothing having happened. The line shows up only when
	if stats.Timeouts > 0 {
		lines = append(lines, fmt.Sprintf("Таймаутов модели: %d ", stats.Timeouts))
	}
	if stats.Model != "" {
		lines = append(lines, "Модель: "+stats.Model)
	}
	if stats.LastAt != "" {
		lines = append(lines, "Последнее сообщение: "+stats.LastAt+" ")
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

func replyModel(fromResponse string, configured string) string {
	if strings.TrimSpace(fromResponse) != "" {
		return fromResponse
	}
	return configured
}

func tokensOrNull(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

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

func brief(text string, maxRunes int) string {
	var runes = []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "…"
}
