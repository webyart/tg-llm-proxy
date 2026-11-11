// The Mini App is a second client of the SAME pipeline the Telegram poller runs, not a second bot.
// AnswerForMiniApp deliberately walks the existing stages one by one instead of calling Dispatch:
//
//	allowlist       d.deps.Cfg.Allows       — the same deny-by-default check as dispatch.go
//	per-minute      d.deps.Limit.Allow      — the same RATE_LIMIT_PER_MINUTE budget as /ask
//	per-chat lock   d.deps.Locks.WithChat   — one answer per chat at a time
//	daily quota     callModel -> d.reserveDailyModelCall, modelWork, d.activeModelRequests
//	RAG context     d.BuildMessages + d.addRAGContext
//	tool loop       d.runToolLoop (network tools stay Telegram-only, see miniAppToolLoopState)
//	history         store.Append(user) BEFORE the call, store.Append(assistant) after success
//
// Dispatch itself cannot be reused because it ends in a Command handler that replies through
// d.deps.Replier — that is Telegram I/O by definition. Going through the chain would also write a
// telegram_message_id for a message Telegram never sent. Keeping the ordering of middleware.go's
// `stored` middleware is what preserves the existing crash guarantee: a process that dies during
// generation leaves the question row unanswered and no assistant row, exactly like /ask.
//
// Visible events carry no secrets: user text is never echoed back as an event, model errors pass
// through Sanitize, and the Telegram token never appears in a user-facing message.
package bot

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"bot/internal/model"
	"bot/internal/ratelimit"
	"bot/internal/store"
)

// MiniAppMaxTextRunes bounds one Mini App message. It is the same 2000-rune bound the web layer
// applies to a stored history item (internal/webapp maxHistoryRunes), so the text a user can send
// never exceeds what the history view will show back to them.
const MiniAppMaxTextRunes = 2000

// The stable stage codes the web layer forwards as `event: stage`. They are protocol values, not
// Russian prose: the frontend owns the translation.
const (
	MiniAppStageThinking  = "thinking"
	MiniAppStageReadURL   = "read_url"
	MiniAppStageWebSearch = "web_search"
)

// MiniAppApprovalEvent is the one-time request the model made for a network action. The token is
// random, bound to this chat/user and single-use; Summary is the bounded, sanitized tool arguments.
// ExpiresInSeconds is what the client shows as a countdown.
type MiniAppApprovalEvent struct {
	Token            string
	Tool             string
	Summary          string
	ExpiresInSeconds int
}

// MiniAppAnswerStats — the numbers behind the final answer, sent with the done event. They describe
// the same Response the latency footer was built from, so a client can show the metrics without
// parsing the footer text. Unknown token counts stay nil instead of becoming a misleading zero.
// FirstTokenLatency is measured for streamed answers only; it is zero when the model cannot stream.
type MiniAppAnswerStats struct {
	Latency           time.Duration
	TokensIn          *int64
	TokensOut         *int64
	TokensPerSecond   *float64
	FirstTokenLatency time.Duration
	Model             string
}

// MiniAppStreamEvent is one visible event of a Mini App answer. Exactly one field is meaningful:
// Delta for a text fragment (and for the final text of Done), Stage for a tool/thinking notice,
// Approval for a pending network decision, Done for the last event, Error for a sanitized failure
// message. MessageID and Stats are set only on Done: the stored assistant row and the answer
// metrics, so the client can rate that exact message and render its numbers.
type MiniAppStreamEvent struct {
	Delta     string
	Stage     string
	Approval  *MiniAppApprovalEvent
	Done      bool
	Error     string
	MessageID int64
	Stats     *MiniAppAnswerStats
}

var (
	// ErrMiniAppDenied — the user is not in TELEGRAM_ALLOWED_USERS (deny-by-default included).
	ErrMiniAppDenied = errors.New("the Mini App user is not allowed to chat")
	// ErrMiniAppEmptyText — an empty (or whitespace-only) message.
	ErrMiniAppEmptyText = errors.New("the Mini App message is empty")
	// ErrMiniAppTextTooLong — more than MiniAppMaxTextRunes runes or invalid UTF-8.
	ErrMiniAppTextTooLong = errors.New("the Mini App message is too long")
	// ErrMiniAppUnavailable — the dispatcher has no model client or database.
	ErrMiniAppUnavailable = errors.New("the Mini App chat is unavailable")
)

// MiniAppRateLimitError is the per-minute budget refusal. RetryAfter is what the HTTP layer puts
// into the Retry-After header.
type MiniAppRateLimitError struct {
	RetryAfter time.Duration
}

func (e *MiniAppRateLimitError) Error() string {
	return fmt.Sprintf("the Mini App per-minute model budget is spent; retry after %s", e.RetryAfter.Round(time.Second))
}

// errMiniAppInternal keeps storage/context failures out of the model wording. The public text for
// all of them is one generic sentence; the detail goes to the log, never to the SSE stream.
var errMiniAppInternal = errors.New("the Mini App chat request failed")

// errMiniAppChatBusy — the per-chat lock wait expired before this request even reached the model.
var errMiniAppChatBusy = errors.New("the chat is still answering the previous request")

// errMiniAppOverloaded — the bounded per-chat queue or the dispatcher's execution slots are full.
// Telegram answers both conditions with msgOverloaded; the Mini App must show the same words.
var errMiniAppOverloaded = errors.New("the Mini App chat is overloaded")

// AnswerForMiniApp runs one message through the shared model pipeline and forwards visible events
// to emit. It returns:
//
//	nil                                  — done event with the final text and stored message id was emitted
//	ErrMiniAppDenied                     — allowlist refusal (nothing emitted)
//	ErrMiniAppEmptyText/TooLong          — input refusal (nothing emitted)
//	*MiniAppRateLimitError               — per-minute refusal (nothing emitted)
//	ErrMiniAppUnavailable                — missing wiring (nothing emitted)
//	context.Canceled                     — the caller went away
//	another error                        — an error event was already emitted when possible
//
// The assistant row is stored before the done event, so a client that receives done can always
// read the answer back from the history endpoints.
func (d *Dispatcher) AnswerForMiniApp(ctx context.Context, chatID, userID int64, text string, emit func(MiniAppStreamEvent) error) error {
	if d == nil || d.deps.DB == nil || d.deps.Model == nil {
		return ErrMiniAppUnavailable
	}
	if emit == nil {
		emit = func(MiniAppStreamEvent) error { return nil }
	}
	if err := d.miniAppAllowed(ctx, userID); err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ErrMiniAppEmptyText
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("%w: the text is not valid UTF-8", ErrMiniAppTextTooLong)
	}
	if Runes(text) > MiniAppMaxTextRunes {
		return fmt.Errorf("%w: at most %d runes are allowed", ErrMiniAppTextTooLong, MiniAppMaxTextRunes)
	}
	if d.deps.Limit != nil && !d.deps.Limit.Allow(userID) {
		wait := d.deps.Limit.RetryAfter(userID)
		if wait <= 0 {
			wait = time.Minute
		}
		d.logEvent(ctx, userID, store.EventLimit, "miniapp_ask")
		return &MiniAppRateLimitError{RetryAfter: wait}
	}

	d.logEvent(ctx, userID, store.EventCommand, "miniapp_ask")

	streamed := false
	forward := func(event MiniAppStreamEvent) error {
		if event.Delta != "" {
			streamed = true
		}
		return emit(event)
	}
	final, assistantID, stats, err := d.answerForMiniApp(ctx, chatID, userID, text, forward)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err // the reader is gone: a second write would only fail again
		}
		if message := d.miniAppErrorText(err); message != "" {
			if emitErr := emit(MiniAppStreamEvent{Error: message}); emitErr != nil {
				return emitErr
			}
		}
		return err
	}
	if !streamed {
		if err := emit(MiniAppStreamEvent{Delta: final}); err != nil {
			d.deliverMiniAppAnswerToTelegram(ctx, chatID, userID, final)
			return err
		}
	}
	var doneErr = emit(MiniAppStreamEvent{Delta: final, Done: true, MessageID: assistantID, Stats: stats})
	if doneErr != nil || ctx.Err() != nil {
		// The window is gone but the answer exists; deliver it where the user will see it.
		d.deliverMiniAppAnswerToTelegram(ctx, chatID, userID, final)
	}
	return doneErr
}

// miniAppAnswerStatsFor turns the final Response into the numbers of the done event. It mirrors the
// latency footer: the rate is output tokens over the whole call, and a provider that returned no
// usage leaves the token fields nil rather than zero.
func miniAppAnswerStatsFor(resp model.Response, selectedModel string) *MiniAppAnswerStats {
	stats := &MiniAppAnswerStats{
		Latency:           resp.Latency,
		TokensIn:          resp.TokensIn,
		TokensOut:         resp.TokensOut,
		FirstTokenLatency: resp.FirstTokenLatency,
		Model:             replyModel(resp.Model, selectedModel),
	}
	if resp.TokensOut != nil && resp.Latency > 0 {
		rate := math.Round(float64(*resp.TokensOut)/resp.Latency.Seconds()*10) / 10
		stats.TokensPerSecond = &rate
	}
	return stats
}

// effectiveMiniAppTelegramFallback resolves the fallback for one chat: an explicit per-chat row wins, and the
// MINIAPP_TELEGRAM_FALLBACK environment row is only the default. A failed read is logged and treated as "no
// override" — the answer still goes to Telegram, and the next read can repair the setting.
func (d *Dispatcher) effectiveMiniAppTelegramFallback(ctx context.Context, chatID int64) bool {
	if d == nil {
		return false
	}
	enabled := d.deps.Cfg.MiniAppTelegramFallback
	if d.deps.DB == nil {
		return enabled
	}
	override, found, err := store.ChatMiniAppFallback(ctx, d.deps.DB, chatID)
	if err != nil {
		if d.deps.Logger != nil {
			d.deps.Logger.Warn("the Mini App Telegram fallback setting could not be read",
				"chat_id", chatID, "error", Sanitize(err, d.deps.Token))
		}
		return enabled
	}
	if found {
		return override
	}
	return enabled
}

// deliverMiniAppAnswerToTelegram sends an already stored Mini App answer to the Telegram chat when the reader
// disappeared before the done event (MINIAPP_TELEGRAM_FALLBACK, on by default; a chat override wins). Best-effort:
// a failed send is logged, never returned, because the answer is already readable from the history.
func (d *Dispatcher) deliverMiniAppAnswerToTelegram(ctx context.Context, chatID, userID int64, text string) {
	if d == nil {
		return
	}
	if strings.TrimSpace(text) == "" || d.deps.Replier == nil {
		return
	}
	// The client context is usually already canceled when this path runs, and the setting read must not inherit
	// that cancellation: otherwise the override could not be read and every chat would fall back to the global row.
	settingCtx, cancelSetting := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	enabled := d.effectiveMiniAppTelegramFallback(settingCtx, chatID)
	cancelSetting()
	if !enabled {
		return
	}
	var sendCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := d.Send(sendCtx, chatID, text); err != nil {
		if d.deps.Logger != nil {
			d.deps.Logger.Warn("the Mini App answer could not be delivered to Telegram",
				"chat_id", chatID, "error", Sanitize(err, d.deps.Token))
		}
		return
	}
	d.logEvent(sendCtx, userID, store.EventCommand, "miniapp_answer_telegram")
}

// ResetContextForMiniApp is the Mini App equivalent of /reset: it hides the chat's context from
// the model with store.ClearChat and keeps the message rows (ClearChat never deletes them).
func (d *Dispatcher) ResetContextForMiniApp(ctx context.Context, chatID, userID int64) error {
	if err := d.miniAppAllowed(ctx, userID); err != nil {
		return err
	}
	err := d.deps.Locks.WithChat(ctx, chatID, func() error {
		_, clearErr := store.ClearChat(ctx, d.deps.DB, chatID)
		return clearErr
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: clear context: %v", errMiniAppInternal, err)
	}
	return nil
}

// SkipPendingForMiniApp is the Mini App equivalent of /skip (skip=true) and /unskip
// (skip=false). It reports whether the stored state actually changed, so the endpoint can answer
// "nothing to skip" without inventing an error. History is never touched.
func (d *Dispatcher) SkipPendingForMiniApp(ctx context.Context, chatID, userID int64, skip bool) (bool, error) {
	if err := d.miniAppAllowed(ctx, userID); err != nil {
		return false, err
	}
	changed := false
	err := d.deps.Locks.WithChat(ctx, chatID, func() error {
		if skip {
			rowID, role, err := store.LastVisibleMessage(ctx, d.deps.DB, chatID)
			if err != nil {
				return err
			}
			if rowID == 0 || role != store.RoleUser {
				return nil
			}
			if err := store.SetResumeSkip(ctx, d.deps.DB, chatID, rowID); err != nil {
				return err
			}
			changed = true
			return nil
		}
		questionID, found, err := store.ResumeSkip(ctx, d.deps.DB, chatID)
		if err != nil || !found {
			return err
		}
		if err := store.ClearResumeSkip(ctx, d.deps.DB, chatID); err != nil {
			return err
		}
		changed = questionID > 0
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, fmt.Errorf("%w: pending recovery control: %v", errMiniAppInternal, err)
	}
	return changed, nil
}

// miniAppAllowed is the allowlist half of admission, shared by every Mini App chat action.
func (d *Dispatcher) miniAppAllowed(ctx context.Context, userID int64) error {
	if d == nil || d.deps.DB == nil {
		return ErrMiniAppUnavailable
	}
	if userID <= 0 || !d.deps.Cfg.Allows(userID) {
		d.logEvent(ctx, userID, store.EventDenied, "miniapp")
		return ErrMiniAppDenied
	}
	return nil
}

func (d *Dispatcher) answerForMiniApp(ctx context.Context, chatID, userID int64, text string, emit func(MiniAppStreamEvent) error) (string, int64, *MiniAppAnswerStats, error) {
	if d.deps.Model == nil {
		return "", 0, nil, ErrMiniAppUnavailable
	}

	// Streaming is decided before admission. The budget is the same pair Telegram uses in
	// commands.go: TG_REPLY_DEADLINE_SECONDS around the whole answer, plus
	// STREAM_TOTAL_DEADLINE_SECONDS when a stream is possible. model.StreamSettings itself is
	// configured inside the model client (main.go), so using the configured streamer is what
	// reuses those timeouts. Like Telegram's runBounded, the budget starts only after the chat
	// lock and the execution slot are owned, so a request that waited in the queue still gets the
	// full answer budget.
	streamer, _ := d.deps.Model.(model.Streamer)
	streaming := false
	if streamer != nil {
		enabled, _, err := d.streamingEnabled(ctx, chatID)
		if err != nil {
			return "", 0, nil, fmt.Errorf("%w: read streaming setting: %v", errMiniAppInternal, err)
		}
		streaming = enabled
	}
	budget := time.Duration(d.deps.Cfg.ReplyDeadlineSeconds) * time.Second
	if streaming {
		budget += time.Duration(d.deps.Cfg.StreamTotalSeconds) * time.Second
	}
	// The approval wait is part of this answer: two network calls per round and two rounds at the
	// approval TTL each must fit before the work context expires, otherwise the 2-minute approval
	// would be cut by a shorter non-streaming reply budget.
	budget += time.Duration(2*toolRoundLimit) * MiniAppApprovalTimeout
	if budget <= 0 {
		budget = time.Minute
	}
	// The answer must survive the client: closing the Mini App cancels the HTTP request, and binding the model
	// work to it used to leave the question stored without an answer. The wait for a network approval still
	// listens to the client context (a closed window must not keep a pending decision alive), so the two
	// contexts are kept apart.
	var clientCtx = ctx
	var baseCtx = context.WithoutCancel(ctx)

	// Admission parity with Telegram's runBounded: the same bounded per-chat queue
	// (perChatQueueCapacity) and the same wait budget (chatQueueTimeout). A full queue and a full
	// set of execution slots are the overloaded condition; an expired wait for the chat lock is
	// the separate "chat is still answering" condition.
	var queueTimeout = d.chatQueueTimeout
	if queueTimeout <= 0 {
		queueTimeout = chatQueueTimeoutFor(d.deps.Cfg.ReplyDeadlineSeconds)
	}
	var queueCtx, cancelQueue = context.WithTimeout(baseCtx, queueTimeout)
	defer cancelQueue()

	var final string
	var assistantID int64
	var stats *MiniAppAnswerStats
	var inChat bool
	err := d.deps.Locks.WithChatBounded(queueCtx, chatID, perChatQueueCapacity, func() error {
		inChat = true

		// The global execution slot is Telegram's active_handlers guard. It is acquired only after
		// this chat owns the lock, and released by the deferred function on every path (success,
		// model error, cancellation and the finalizer's own failure).
		release, slotErr := d.acquireMiniAppExecution(queueCtx)
		if slotErr != nil {
			return slotErr
		}
		defer release()

		workCtx, cancelWork := context.WithTimeout(baseCtx, budget)
		defer cancelWork()

		// The question is stored BEFORE the model call, exactly like the `stored` middleware: a
		// crash during generation leaves an unanswered user row and no assistant row.
		if _, err := store.Append(workCtx, d.deps.DB, store.Message{
			ChatID: chatID, UserID: userID, Role: store.RoleUser, Content: text,
		}); err != nil {
			return fmt.Errorf("%w: store question: %v", errMiniAppInternal, err)
		}
		messages, err := d.BuildMessages(workCtx, chatID)
		if err != nil {
			return fmt.Errorf("%w: build messages: %v", errMiniAppInternal, err)
		}
		messages, ragSources, err := d.addRAGContext(workCtx, chatID, userID, text, messages)
		if err != nil {
			return fmt.Errorf("%w: add RAG context: %v", errMiniAppInternal, err)
		}
		selectedModel, err := d.modelName(workCtx, chatID)
		if err != nil {
			return fmt.Errorf("%w: resolve model: %v", errMiniAppInternal, err)
		}

		metadata := toolLoopMetadata{
			event:           Event{ChatID: chatID, UserID: userID, ChatType: "private", Text: text, Question: text},
			originalRequest: text,
			selectedModel:   selectedModel,
			ragSources:      ragSources,
		}
		var session *streamSession
		var used model.Streamer
		if streaming {
			tracker := &miniAppStageTracker{emit: emit}
			session = d.newStreamSession(workCtx, chatID, &miniAppStreamSender{stages: tracker})
			used = &miniAppStreamer{streamer: streamer, stages: tracker, emit: emit}
		}
		state := d.miniAppToolLoopState(clientCtx, userID, chatID, model.Request{Model: selectedModel, Messages: messages}, metadata, used, session, emit)
		resp, modelErr := d.runToolLoop(withToolLoopMetadata(workCtx, metadata), state)
		if modelErr != nil {
			d.logMiniAppModelError(ctx, userID, modelErr)
			return modelErr
		}
		if strings.TrimSpace(resp.Content) == "" {
			// A 200 with no content is not an answer: keep the "exactly one assistant row, and
			// only for a successful Response" invariant of commands.go.
			d.logMiniAppModelError(ctx, userID, model.ErrEmptyAnswer)
			return model.ErrEmptyAnswer
		}

		// The assistant row is written only after the final successful Response, and before the
		// done event leaves, so a reader can always find the exact text it just received (and its
		// id, which is what the chat feedback buttons rate). The stored row keeps resp.Content,
		// exactly like Telegram's assistant row; the notices belong to the delivered final text.
		rowID, err := store.Append(workCtx, d.deps.DB, store.Message{
			ChatID:    chatID,
			UserID:    userID,
			Role:      store.RoleAssistant,
			Content:   SanitizeToolMarkup(resp.Content),
			TokensIn:  resp.TokensIn,
			TokensOut: resp.TokensOut,
			Model:     replyModel(resp.Model, selectedModel),
			LatencyMs: resp.Latency.Milliseconds(),
		})
		if err != nil {
			return fmt.Errorf("%w: store answer: %v", errMiniAppInternal, err)
		}
		assistantID = rowID
		stats = miniAppAnswerStatsFor(resp, selectedModel)
		// The same finalizer the Telegram path uses: latency, finish_reason notice, RAG sources
		// and the document truncation marker. Without it a finish_reason=length answer looked
		// complete in the Mini App while Telegram warned about it.
		final = formatModelAnswer(resp.Content, resp, selectedModel, answerFormatOptions{
			Latency: true,
			Sources: ragSources,
		})
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ratelimit.ErrChatQueueFull), errors.Is(err, errExecutionSlotBusy):
			return "", 0, nil, fmt.Errorf("%w: %v", errMiniAppOverloaded, err)
		case !inChat && errors.Is(err, context.DeadlineExceeded):
			return "", 0, nil, fmt.Errorf("%w: %v", errMiniAppChatBusy, err)
		}
		return "", 0, nil, err
	}
	return final, assistantID, stats, nil
}

// acquireMiniAppExecution takes one of the dispatcher's global execution slots, the same bounded
// resource Telegram's runBounded guards with active_handlers. The returned function must be called
// on every path; it is what makes a cancelled or failed Mini App answer stop counting as active.
func (d *Dispatcher) acquireMiniAppExecution(ctx context.Context) (func(), error) {
	if d.execution == nil {
		// A Dispatcher built as a literal (only old unit tests do that) has no admission channels.
		// AnswerForMiniApp is always reached through New in production, so this keeps those tests
		// honest without inventing an unbounded slot.
		return func() {}, nil
	}
	select {
	case d.execution <- struct{}{}:
		d.activeHandlers.Add(1)
		return func() {
			d.activeHandlers.Add(-1)
			<-d.execution
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", errExecutionSlotBusy, ctx.Err())
	}
}

// miniAppToolLoopState builds the continuation for runToolLoop with the same tools the Telegram
// path offers: calculator plus read_url/web_search behind URL_READER_ENABLED/WEB_SEARCH_ENABLED.
// A network tool is not executed silently: the continuation gets an approval waiter that asks the
// user over the live SSE stream and blocks this same loop until POST /api/chat/approval (or the TTL,
// or the client disconnect) resolves it.
func (d *Dispatcher) miniAppToolLoopState(clientCtx context.Context, userID int64, chatID int64, req model.Request, metadata toolLoopMetadata, streamer model.Streamer, session *streamSession, emit func(MiniAppStreamEvent) error) *toolLoopContinuation {
	allowed := map[string]bool{
		"calculator": d.deps.Cfg.ToolsEnabled,
		"read_url":   d.deps.Cfg.ToolsEnabled && d.deps.Cfg.URLReaderEnabled,
		"web_search": d.deps.Cfg.ToolsEnabled && d.deps.Cfg.WebSearchEnabled,
	}
	req.Tools = append(calculatorTools(allowed["calculator"]), urlReaderTools(allowed["read_url"])...)
	req.Tools = append(req.Tools, webSearchTools(allowed["web_search"])...)
	if len(req.Tools) > 0 {
		req.Messages = append([]model.Message{{Role: model.RoleSystem, Content: nativeToolInstruction}}, req.Messages...)
	}
	state := &toolLoopContinuation{
		userID: userID, request: req, allowedTools: allowed,
		approvedCall: -1, approvedCalls: map[int]bool{},
		metadata: metadata, streamer: streamer, session: session,
	}
	if allowed["read_url"] || allowed["web_search"] {
		state.approvalWaiter = func(ctx context.Context, call model.ToolCall) (bool, error) {
			return d.awaitMiniAppApproval(clientCtx, emit, chatID, userID, call)
		}
	}
	return state
}

func (d *Dispatcher) logMiniAppModelError(ctx context.Context, userID int64, err error) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditBudget)
	defer cancel()
	kind := store.EventModelError
	if errors.Is(err, context.DeadlineExceeded) {
		kind = store.EventTimeout
	}
	d.logEvent(auditCtx, userID, kind, d.sanitizeMiniAppError(err))
}

// sanitizeMiniAppError removes every secret the config knows about, not only deps.Token: this path
// is reachable without a Telegram dispatcher (the web layer builds its own), so deps.Token may be
// empty while the bot token and the provider key are still configured.
func (d *Dispatcher) sanitizeMiniAppError(err error) string {
	if err == nil {
		return ""
	}
	text := Sanitize(err, d.deps.Token)
	for _, secret := range []string{d.deps.Cfg.BotToken, d.deps.Cfg.StrataKey, d.deps.Cfg.WebhookSecretToken} {
		if secret = strings.TrimSpace(secret); secret != "" {
			text = strings.ReplaceAll(text, secret, "***")
		}
	}
	return text
}

// miniAppErrorText — the safe sentence a Mini App user sees for a failure that happened after
// admission. Provider bodies and storage errors never reach this function's result: the default
// branch runs the error through Sanitize and the model wording the Telegram path already uses.
func (d *Dispatcher) miniAppErrorText(err error) string {
	switch {
	case errors.Is(err, errMiniAppInternal):
		return "Не удалось обработать сообщение. Попробуй ещё раз."
	case errors.Is(err, errMiniAppChatBusy):
		return msgChatBusy
	case errors.Is(err, errMiniAppOverloaded):
		return msgOverloaded
	case errors.Is(err, context.Canceled):
		return ""
	case errors.Is(err, errNetworkApprovalPending):
		return "Сетевое действие подтверждается только в Telegram: открой бота в Telegram и повтори запрос там."
	case errors.Is(err, errMiniAppApprovalTimeout):
		return "Время подтверждения сетевого действия истекло. Запрос не выполнен. Попробуй ещё раз."
	case errors.Is(err, errMiniAppApprovalBusy):
		return "Слишком много одновременных запросов на подтверждение сетевого действия. Попробуй ещё раз позже."
	case errors.Is(err, model.ErrEmptyAnswer):
		return msgEmptyAnswer
	case errors.Is(err, model.ErrNoRoom):
		return msgNoRoom
	case errors.Is(err, context.DeadlineExceeded):
		spent, seconds := "STRATA_TIMEOUT_SECONDS", d.deps.Cfg.TimeoutSeconds
		switch {
		case errors.Is(err, model.ErrStreamStalled):
			spent, seconds = "STREAM_IDLE_TIMEOUT_SECONDS", d.deps.Cfg.StreamIdleSeconds
		case errors.Is(err, model.ErrStreamTooLong):
			spent, seconds = "STREAM_TOTAL_DEADLINE_SECONDS", d.deps.Cfg.StreamTotalSeconds
		}
		return fmt.Sprintf(msgTimeoutFormat, seconds, spent)
	case errors.Is(err, model.ErrStreamLimit):
		return "Ответ модели превысил допустимый размер. Попробуй более короткий вопрос."
	}
	if notice, limited := modelLimitNotice(err); limited {
		return notice
	}
	return fmt.Sprintf(msgModelError, d.sanitizeMiniAppError(err))
}

// miniAppStageTracker deduplicates stage notices across the two observers that can produce them:
// model progress ("thinking") and streamSession.stage through the sender below ("read_url" /
// "web_search").
type miniAppStageTracker struct {
	last string
	emit func(MiniAppStreamEvent) error
}

func (t *miniAppStageTracker) stage(name string) {
	if t == nil || name == "" || t.last == name {
		return
	}
	t.last = name
	_ = t.emit(MiniAppStreamEvent{Stage: name})
}

// miniAppStreamSender is a StreamSender with no Telegram behind it. streamSession writes the
// placeholder and its interim edits here; the sender only notices the stage texts. The raw
// deltas are forwarded by miniAppStreamer below — the same trick keeps callModel/runToolLoop
// untouched while the visible side is SSE instead of a message edit.
type miniAppStreamSender struct {
	stages *miniAppStageTracker
}

func (s *miniAppStreamSender) SendStreamMessage(ctx context.Context, chatID int64, text string) (int64, error) {
	s.observe(text)
	return 1, nil
}

func (s *miniAppStreamSender) EditStreamMessage(ctx context.Context, chatID int64, messageID int64, text string, parse bool) error {
	s.observe(text)
	return nil
}

func (s *miniAppStreamSender) observe(text string) {
	switch text {
	case streamStageReadURL:
		s.stages.stage(MiniAppStageReadURL)
	case streamStageWebSearch:
		s.stages.stage(MiniAppStageWebSearch)
	}
}

// miniAppStreamer wraps the configured model.Streamer (and therefore its StreamSettings budgets)
// so that every delivered delta reaches the SSE emitter before the ordinary streamSession
// handler accumulates it. The inner handler still runs, which is what keeps callModel's
// "did anything reach the user" quota-refund rule and runToolLoop's stage calls working.
type miniAppStreamer struct {
	streamer model.Streamer
	stages   *miniAppStageTracker
	emit     func(MiniAppStreamEvent) error
}

func (s *miniAppStreamer) Stream(ctx context.Context, req model.Request, handler model.StreamHandler) (model.Response, error) {
	inner := handler
	if handler.Progress != nil {
		inner.Progress = func(info model.StreamInfo) {
			handler.Progress(info)
			s.stages.stage(MiniAppStageThinking)
		}
	}
	if handler.Delta != nil {
		inner.Delta = func(text string) error {
			if err := handler.Delta(text); err != nil {
				return err
			}
			// A closed Mini App must not abort a generation that is already paid for: its answer is stored and a
			// reopened window can read it from the history. The event is best-effort by design.
			_ = s.emit(MiniAppStreamEvent{Delta: text})
			return nil
		}
	}
	return s.streamer.Stream(ctx, req, inner)
}
