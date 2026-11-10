// per second, so a literal edit-per-token would be a flood. Even a sustained one-second cadence reached a real
//
// Three rules of the existing design survive the stream, and each is the reason for one branch below:
//
//	markup: MarkdownV2 and HTML both need balanced entities, and an unbalanced edit is the 400 Telegram
//	answers «can't parse entities». So the interim edits go as PLAIN text and only the final edit carries the
//	when the answer is longer, the tail goes as new messages through the ordinary path of reply.go, so a
//	says out loud that it is broken, so nothing in the chat looks like a finished answer.
package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/config"
	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
)

// last event of the stream, so a placeholder may never promise «осталось N токенов».
const (
	StreamPlaceholder    = "Модель думает…"
	streamStageReadURL   = "Читаю страницу…"
	streamStageWebSearch = "Ищу информацию…"
	msgStreamThinking    = "Модель думает: %d символов размышлений, %s "
	// msgStreamSuffix is a SUFFIX with a template of its own because it is the ANSWER that gets cut: an edit
	// of the markers before it decides how much of the answer fits into the message.
	msgStreamSuffix = "\n\n… ответ пишется, %d символов "
	// msgStreamCutMarker — the rest of the answer does not fit one message; it arrives after the stream ends.
	msgStreamCutMarker = "\n… (дальше — следующим сообщением)"
	// msgStreamBroken — the stream stopped with part of the answer already on the screen. The template is short
	// puts the floor of that row at 64 symbols.
	msgStreamBroken = "\n\n… (прервано: %s — в истории этого ответа нет)"
	// msgAnswerBelow replaces a placeholder that cannot be deleted (or whose deletion failed) when the answer
	// is sent as a separate message below a newer question.
	msgAnswerBelow = "(ответ ниже)"
)

// fitNotice — a notice whose TEMPLATE must survive, with `part` shortened so the whole fits maxRunes runes
// failure, never the sentence that tells the user whether they are looking at an answer: a library error is
// allowed to be long, the meaning of the message may not.
func fitNotice(template string, part string, maxRunes int) string {
	var fixed = Runes(fmt.Sprintf(template, ""))
	var budget = maxRunes - fixed
	if budget <= 0 {
		return fmt.Sprintf(template, "")
	}
	if Runes(part) > budget {
		part = strings.TrimRight(string([]rune(part)[:budget]), " \t\n\r")
	}
	return fmt.Sprintf(template, part)
}

type StreamSender interface {
	// SendStreamMessage posts the placeholder and returns the id of the message every later edit goes to.
	SendStreamMessage(ctx context.Context, chatID int64, text string) (int64, error)
	// EditStreamMessage rewrites that message.
	EditStreamMessage(ctx context.Context, chatID int64, messageID int64, text string, parse bool) error
}

// draftSender — a StreamSender that can also stream through Bot API draft messages. Drafts exist only in
// private chats, so a session asks for this capability only when the chat id is positive (groups and
// channels have negative ids).
type draftSender interface {
	SendDraft(ctx context.Context, chatID int64, draftID, text string) error
}

// streamSession — one streamed answer of one chat. The callbacks of the stream run on the single goroutine
// that reads the SSE body (model.StreamHandler says so), so this struct needs no lock for its own counters;
type streamSession struct {
	ctx        context.Context
	chatID     int64
	sender     StreamSender
	draft      draftSender // nil unless this is a private chat and the sender supports drafts
	draftID    string
	dispatcher *Dispatcher
	interval   time.Duration
	maxRunes   int
	start      time.Time

	messageID      int64
	messageIDs     []int64
	posted         bool
	drafted        bool // a draft reached the chat: the chat has seen this answer, but no real message exists
	answer         strings.Builder
	answerRunes    int
	rendered       bool
	renderedAnswer string
	renderedChunks []string
	stageText      string

	edits        int
	skipped      int // rewrites the cadence of refused
	postFailures int // the placeholder never arrived
	editFailures int // rewrites Telegram refused
	lastEdit     time.Time
	muted        bool // the outgoing side of this chat gave up on the growing message
	// turn is the number of the update that started this answer. deliver compares it with the chat's newest
	// number: a larger one means the next question was already admitted, so its message sits below the
	// placeholder (or draft) and editing that placeholder would leave the answer above the question it answers.
	turn int64
}

// deliveryFailureBudget — how many refused Telegram calls this chat may collect before the session stops
// is not receiving anything, and muting is not the end of the answer — deliver() still sends the whole text the
const (
	deliveryFailureBudget = 3
	maxStreamChunks       = 64
)

func (d *Dispatcher) streamingEnabled(ctx context.Context, chatID int64) (bool, bool, error) {
	enabled := d.deps.Cfg.Streaming
	if d.deps.DB == nil {
		return enabled, false, nil
	}
	override, found, err := store.ChatStreaming(ctx, d.deps.DB, chatID)
	if err != nil {
		return false, false, err
	}
	if found {
		enabled = override
	}
	return enabled, found, nil
}

// streamingPossible — the effective per-chat setting, a client that can stream, and a sender that can edit.
func (d *Dispatcher) streamingPossible(ctx context.Context, chatID int64) (model.Streamer, StreamSender, bool, error) {
	enabled, _, err := d.streamingEnabled(ctx, chatID)
	if err != nil || !enabled {
		return nil, nil, false, err
	}
	var streamer, canStream = d.deps.Model.(model.Streamer)
	if !canStream {
		return nil, nil, false, nil
	}
	var sender, canEdit = d.deps.Replier.(StreamSender)
	if !canEdit {
		return nil, nil, false, nil
	}
	return streamer, sender, true, nil
}

func (d *Dispatcher) showStreamMenu(ctx context.Context, ev Event, note ...string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	enabled, override, err := d.streamingEnabled(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	state := i18n.T(lang, "stream.state_off")
	if enabled {
		state = i18n.T(lang, "stream.state_on")
	}
	source := i18n.T(lang, "stream.source_global")
	if override {
		source = i18n.T(lang, "stream.source_chat")
	}
	text := menuText(i18n.T(lang, "stream.menu", state, source), note)
	markup := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Включить", "menu:stream:on"), menuButton("Выключить", "menu:stream:off")),
		menuRow(menuButton("Сбросить", "menu:stream:reset")),
		menuRow(backButton("menu:settings")),
	}}
	return d.renderMenu(ctx, ev, text, markup)
}

func (d *Dispatcher) streamMenuControl(ctx context.Context, ev Event, action string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	switch action {
	case "menu":
		return d.showStreamMenu(ctx, ev)
	case "menu:on", "menu:off":
		enabled := action == "menu:on"
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatStreaming(ctx, d.deps.DB, ev.ChatID, enabled); err != nil {
			return err
		}
		return d.showStreamMenu(ctx, ev, i18n.T(lang, "stream.menu_saved"))
	case "menu:reset":
		if err := store.ClearChatStreaming(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		return d.showStreamMenu(ctx, ev, i18n.T(lang, "stream.menu_reset"))
	default:
		return d.showStreamMenu(ctx, ev, menuExpiredText)
	}
}

func (d *Dispatcher) streamControl(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	action := strings.ToLower(strings.TrimSpace(arg))
	if strings.HasPrefix(action, "menu") {
		return d.streamMenuControl(ctx, ev, action)
	}
	switch action {
	case "on", "off":
		enabled := action == "on"
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatStreaming(ctx, d.deps.DB, ev.ChatID, enabled); err != nil {
			return err
		}
		state := i18n.T(lang, "stream.state_off")
		if enabled {
			state = i18n.T(lang, "stream.state_on")
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "stream.saved", state))
	case "reset":
		if err := store.ClearChatStreaming(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
	case "":
	default:
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "stream.usage"))
	}

	enabled, override, err := d.streamingEnabled(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	state := i18n.T(lang, "stream.state_off")
	if enabled {
		state = i18n.T(lang, "stream.state_on")
	}
	source := i18n.T(lang, "stream.source_global")
	if override {
		source = i18n.T(lang, "stream.source_chat")
	}
	return d.reply(ctx, ev.ChatID, i18n.T(lang, "stream.state", state, source))
}

func (d *Dispatcher) newStreamSession(ctx context.Context, chatID int64, sender StreamSender) *streamSession {
	var interval = time.Duration(d.deps.Cfg.StreamEditIntervalMS) * time.Millisecond
	if interval <= 0 {
		// cadence is not something a caller should have to remember).
		interval = time.Duration(config.DefaultStreamEditIntervalMS) * time.Millisecond
	}
	var draft draftSender
	if chatID > 0 && d.deps.Cfg.StreamDrafts {
		// Drafts are private-chat only, opt-in (STREAM_DRAFTS_ENABLED) and a sender opts in by implementing
		// SendDraft. A draft keeps the position where the answer started, so it is off unless asked for.
		draft, _ = sender.(draftSender)
	}
	return &streamSession{
		ctx:        ctx,
		chatID:     chatID,
		sender:     sender,
		draft:      draft,
		draftID:    fmt.Sprintf("%d-%d", chatID, time.Now().UnixNano()),
		dispatcher: d,
		interval:   interval,
		maxRunes:   int(d.deps.Cfg.MaxMessageChunk),
		start:      time.Now(),
		turn:       d.chatTurn(chatID),
	}
}

// A chat that refuses everything goes muted instead — see muteIfBroken — and the answer leaves through the
func (s *streamSession) handler() model.StreamHandler {
	return model.StreamHandler{
		Progress: func(info model.StreamInfo) { s.progress(info) },
		Delta: func(text string) error {
			runes := Runes(text)
			if runes > model.StreamAnswerLimit-s.answerRunes {
				return fmt.Errorf("%w: answer content exceeds %d runes", model.ErrStreamLimit, model.StreamAnswerLimit)
			}
			s.answer.WriteString(text)
			s.answerRunes += runes
			return s.refresh()
		},
	}
}

func (s *streamSession) progress(info model.StreamInfo) {
	if s.muted {
		s.skipped++
		return
	}
	if !s.streaming() {
		s.post()
		return
	}
	if !s.due() {
		s.skipped++
		return
	}
	var elapsed = time.Since(s.start).Round(time.Second)
	if s.answer.Len() == 0 {
		s.editOrDraft(fmt.Sprintf(msgStreamThinking, info.ReasoningRunes, elapsed), false)
		return
	}
	chunks, err := s.answerChunks()
	if err != nil {
		s.muted = true
		return
	}
	s.rotateChunks(chunks)
	s.editOrDraft(s.partialViewChunks(chunks), false)
}

// refresh — an answer token arrived. The first delta posts the message when no Progress got there first (a
func (s *streamSession) refresh() error {
	if s.muted {
		s.skipped++
		return nil
	}
	if !s.streaming() {
		s.post()
		return nil
	}
	if !s.due() {
		s.skipped++
		return nil
	}
	chunks, err := s.answerChunks()
	if err != nil {
		return err
	}
	s.rotateChunks(chunks)
	s.editOrDraft(s.partialViewChunks(chunks), false)
	return nil
}

// streaming — the chat has already seen part of this answer: a real placeholder was posted, or a draft
// reached it. The final answer is not counted here; it leaves through deliver() only.
func (s *streamSession) streaming() bool { return s.posted || s.drafted }

func (s *streamSession) answerChunks() ([]string, error) {
	answer := s.answer.String()
	if s.rendered && answer == s.renderedAnswer {
		return s.renderedChunks, nil
	}
	chunks := s.dispatcher.chunksOf(answer)
	if len(chunks) > maxStreamChunks {
		return nil, fmt.Errorf("%w: Telegram output exceeds %d chunks", model.ErrStreamLimit, maxStreamChunks)
	}
	s.rendered = true
	s.renderedAnswer = answer
	s.renderedChunks = chunks
	return chunks, nil
}

func (s *streamSession) rotate() {
	chunks, err := s.answerChunks()
	if err != nil {
		s.muted = true
		return
	}
	s.rotateChunks(chunks)
}

func (s *streamSession) rotateChunks(chunks []string) {
	if s.draft != nil {
		// A draft is one growing view: the answer above one message is cut by partialView, and the
		// final pieces are sent as ordinary messages by deliver().
		return
	}
	for len(s.messageIDs) < len(chunks) && len(s.messageIDs) < maxStreamChunks && !s.muted {
		index := len(s.messageIDs)
		s.postText(plainForParseFallback(chunks[index], s.dispatcher.deps.Cfg.ParseMode))
	}
}

// partialView — what the chat may see of an unfinished answer: its head inside the budget of one message, the
// limit of Telegram is measured in symbols, and the provider's token counts only arrive with the last event).
// The two markers are reserved their room BEFORE the answer is cut, which is what keeps every interim edit
// inside the limit of one message — a rewrite Telegram refuses for length is a rewrite the user never sees.
func (s *streamSession) partialView() string {
	return s.partialViewChunks(s.answerChunksForView())
}

func (s *streamSession) answerChunksForView() []string {
	chunks, err := s.answerChunks()
	if err != nil {
		return s.dispatcher.chunksOf(s.answer.String())
	}
	return chunks
}

func (s *streamSession) partialViewChunks(chunks []string) string {
	var total = s.answerRunes
	var suffix = fmt.Sprintf(msgStreamSuffix, total)
	var marker = ""
	if total > s.maxRunes {
		marker = msgStreamCutMarker
	}
	// The markers are worth more than the head they decorate, and of the two, the counter («it is still being
	// 64 symbols) two notices and a head do not fit together, and a rewrite past the limit is the 400 Telegram
	if Runes(marker)+Runes(suffix) >= s.maxRunes {
		marker = ""
	}
	if Runes(suffix) >= s.maxRunes {
		suffix = ""
	}
	return s.headOf(s.currentAnswerChunks(chunks), Runes(suffix)+Runes(marker)) + marker + suffix
}

// head — the part of the answer the placeholder can carry once `reserve` symbols of the message are given to the
// the message, and sending more than the limit is the 400 Telegram answers «too long» rather than a reason to
// grow the message.
func (s *streamSession) currentAnswer() string {
	return s.currentAnswerChunks(s.answerChunksForView())
}

func (s *streamSession) currentAnswerChunks(chunks []string) string {
	if len(chunks) == 0 {
		return s.answer.String()
	}
	return plainForParseFallback(chunks[len(chunks)-1], s.dispatcher.deps.Cfg.ParseMode)
}

func (s *streamSession) head(reserve int) string {
	return s.headOf(s.currentAnswer(), reserve)
}

func (s *streamSession) headOf(text string, reserve int) string {
	var runes = []rune(text)
	var room = s.maxRunes - reserve
	if room < 0 {
		room = 0
	}
	var head = cutRunesToUnits(runes, room)
	if len(head) == len(runes) {
		return text
	}
	return strings.TrimRight(string(head), " \t\n\r")
}

// removePlaceholder drops the message this session was growing, when the sender can delete it. A draft-only
// session has nothing to delete: the draft expires on its own after the answer is delivered as a real message.
//
// RemoveMenu is not enough here: it only strips the inline keyboard and leaves the truncated interim text
// above the answer that now leaves as a new message. The optional messageDeleter capability (menu.go) of
// the sender that posted the placeholder removes the message; a sender without it, or a refused deletion,
// falls back to the ordinary edit path and points the placeholder at the answer below.
func (s *streamSession) removePlaceholder(ctx context.Context, d *Dispatcher) {
	if !s.posted || s.messageID == 0 {
		return
	}
	if remover, ok := s.sender.(messageDeleter); ok {
		var err = remover.DeleteMessage(ctx, s.chatID, s.messageID)
		if err == nil {
			return
		}
		d.deps.Logger.Debug("the stream placeholder could not be deleted; pointing it at the answer instead",
			"chat_id", s.chatID, "error", Sanitize(err, d.deps.Token))
	}
	if err := s.edit(ctx, msgAnswerBelow, false); err != nil {
		d.deps.Logger.Debug("the stream placeholder could not be replaced either",
			"chat_id", s.chatID, "error", Sanitize(err, d.deps.Token))
	}
}

// flood is about.
func (s *streamSession) due() bool {
	return time.Since(s.lastEdit) >= s.interval
}

// session goes muted: the answer keeps accumulating and leaves through d.Send at the end of the stream.
func (s *streamSession) post() {
	text := StreamPlaceholder
	if s.answer.Len() > 0 {
		text = s.partialView()
	}
	if s.draft != nil {
		s.draftText(text)
		return
	}
	s.postText(text)
}

func (s *streamSession) stage(text string) {
	if s.muted || text == "" || s.stageText == text {
		return
	}
	s.stageText = text
	if s.draft != nil {
		s.draftText(text)
		return
	}
	if !s.posted {
		s.postText(text)
		return
	}
	_ = s.edit(s.ctx, text, false)
}

// editOrDraft — one interim view: a draft when the session streams through Bot API drafts, an edit of the
// real placeholder otherwise.
func (s *streamSession) editOrDraft(text string, parse bool) {
	if s.draft != nil {
		s.draftText(text)
		return
	}
	_ = s.edit(s.ctx, text, parse)
}

// draftText sends one interim view as a Bot API draft. A draft is not a message: it expires on its own and
// the final answer still leaves through deliver() as ordinary sendMessage calls. The FIRST refusal disables
// drafts for the rest of the session and posts a real placeholder instead, so a Bot API without drafts
// degrades to the edit path instead of losing the answer.
func (s *streamSession) draftText(text string) {
	if err := s.draft.SendDraft(s.ctx, s.chatID, s.draftID, text); err != nil {
		s.draft = nil
		s.dispatcher.deps.Logger.Warn("the draft update was refused, the stream falls back to editing a placeholder",
			"chat_id", s.chatID, "error", Sanitize(err, s.dispatcher.deps.Token))
		s.postText(text)
		return
	}
	s.drafted = true
	s.lastEdit = time.Now()
}

func (s *streamSession) postText(text string) {
	id, err := s.sender.SendStreamMessage(s.ctx, s.chatID, text)
	if err != nil {
		s.postFailures++
		s.muteIfBroken()
		return
	}
	s.messageID, s.posted, s.lastEdit = id, true, time.Now()
	s.messageIDs = append(s.messageIDs, id)
	s.edits++
}

// muteIfBroken — the point past which this chat is not receiving the growing message. Muting stops the calls,
// not the answer: the deltas keep accumulating in s.answer and deliver() sends the finished text as the ordinary
func (s *streamSession) muteIfBroken() {
	if s.postFailures+s.editFailures >= deliveryFailureBudget {
		s.muted = true
	}
}

func (s *streamSession) edit(ctx context.Context, text string, parse bool) error {
	if !s.posted || s.messageID == 0 {
		return fmt.Errorf("there is no message to edit")
	}
	return s.editMessage(ctx, s.messageID, text, parse)
}

func (s *streamSession) editMessage(ctx context.Context, messageID int64, text string, parse bool) error {
	err := s.sender.EditStreamMessage(ctx, s.chatID, messageID, text, parse)
	if err != nil {
		s.editFailures++
		s.muteIfBroken()
		return err
	}
	s.edits++
	s.lastEdit = time.Now()
	return nil
}

// deliver — the answer is over: the placeholder becomes the real answer and the rest of it goes as new messages.
//
// A stream that never got a message into the chat (every post failed, or the model produced no event before its
func (s *streamSession) deliver(ctx context.Context, d *Dispatcher, full string) error {
	var chunks = d.chunksOf(full)
	if len(chunks) > maxStreamChunks {
		return fmt.Errorf("%w: Telegram output exceeds %d chunks", model.ErrStreamLimit, maxStreamChunks)
	}
	if len(chunks) == 0 {
		return nil // an empty answer is the error of and never reaches this call
	}
	if s.turn > 0 && d.chatTurn(s.chatID) > s.turn {
		// A newer update was admitted while this answer was being produced: the next question is already in
		// the chat, below the placeholder (or draft) this session created. Editing that placeholder would put
		// the finished answer ABOVE the question it answers, so the answer leaves as a new message — it lands
		// below the newer question, which is where a reader expects it — and the placeholder is dropped.
		s.removePlaceholder(ctx, d)
		return d.Send(ctx, s.chatID, full)
	}
	if !s.posted || len(s.messageIDs) == 0 {
		// A draft-only session has no real message to edit, so the whole answer — chunked exactly like any
		// other reply — leaves through the ordinary Send path; the draft expires or is replaced by its head.
		return d.Send(ctx, s.chatID, full)
	}
	var firstErr error
	for index, chunk := range chunks {
		var err error
		if index < len(s.messageIDs) {
			err = s.editMessage(ctx, s.messageIDs[index], chunk, true)
		} else {
			err = d.deps.Replier.Send(ctx, s.chatID, chunk)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		d.deps.Logger.Warn("one or more streamed messages could not be finalized",
			"chat_id", s.chatID, "edits", s.edits, "edit_failures", s.editFailures,
			"error", Sanitize(firstErr, d.deps.Token),
		)
	}
	return firstErr
}

// for a failed call, and this branch keeps that rule visible in the chat: the message says it is unfinished, so
// nothing in the conversation looks like an answer the model finished.
func (s *streamSession) broken(ctx context.Context, d *Dispatcher, reason string) {
	if !s.posted || s.messageID == 0 {
		return
	}
	if err := s.edit(ctx, s.brokenView(reason), false); err != nil {
		d.deps.Logger.Warn("the broken stream could not be marked in the chat",
			"chat_id", s.chatID, "error", Sanitize(err, d.deps.Token))
	}
}

// brokenView — the same fit of one message that partialView guarantees, with the notice of the breakage instead
// Telegram refuses for length leaves the user with the placeholder of «Модель думает…» forever.
func (s *streamSession) brokenView(reason string) string {
	var notice = fitNotice(msgStreamBroken, reason, s.maxRunes)
	var runes = cutRunesToUnits([]rune(s.currentAnswer()), s.maxRunes-unitsOf([]rune(notice)))
	return strings.TrimRight(string(runes), " \t\n\r") + notice
}

func (s *streamSession) sawText() bool { return s.answer.Len() > 0 }

func (s *streamSession) stats() (edits int, skipped int, failures int) {
	return s.edits, s.skipped, s.editFailures + s.postFailures
}
