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

// streamSession — one streamed answer of one chat. The callbacks of the stream run on the single goroutine
// that reads the SSE body (model.StreamHandler says so), so this struct needs no lock for its own counters;
type streamSession struct {
	ctx        context.Context
	chatID     int64
	sender     StreamSender
	dispatcher *Dispatcher
	interval   time.Duration
	maxRunes   int
	start      time.Time

	messageID      int64
	messageIDs     []int64
	posted         bool
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
	enabled, override, err := d.streamingEnabled(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	state := "выключен"
	if enabled {
		state = "включён"
	}
	source := "общий STREAMING_ENABLED"
	if override {
		source = "настройка этого чата"
	}
	text := menuText(fmt.Sprintf("Стриминг: %s.\nИсточник: %s.", state, source), note)
	markup := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Включить", "menu:stream:on"), menuButton("Выключить", "menu:stream:off")),
		menuRow(menuButton("Сбросить", "menu:stream:reset")),
		menuRow(backButton("menu:settings")),
	}}
	return d.renderMenu(ctx, ev, text, markup)
}

func (d *Dispatcher) streamMenuControl(ctx context.Context, ev Event, action string) error {
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
		return d.showStreamMenu(ctx, ev, "Настройка сохранена для этого чата.")
	case "menu:reset":
		if err := store.ClearChatStreaming(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		return d.showStreamMenu(ctx, ev, "Восстановлен общий STREAMING_ENABLED.")
	default:
		return d.showStreamMenu(ctx, ev, menuExpiredText)
	}
}

func (d *Dispatcher) streamControl(ctx context.Context, ev Event, arg string) error {
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
		state := "выключен"
		if enabled {
			state = "включён"
		}
		return d.reply(ctx, ev.ChatID, "Стриминг для этого чата "+state+". Настройка сохранена и переживёт рестарт. /stream reset — вернуть общий STREAMING_ENABLED.")
	case "reset":
		if err := store.ClearChatStreaming(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
	case "":
	default:
		return d.reply(ctx, ev.ChatID, "Использование: /stream — состояние, /stream on, /stream off, /stream reset — общий STREAMING_ENABLED.")
	}

	enabled, override, err := d.streamingEnabled(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	state := "выключен"
	if enabled {
		state = "включён"
	}
	source := "общий STREAMING_ENABLED"
	if override {
		source = "настройка этого чата"
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Стриминг для этого чата: %s. Источник: %s. /stream on, /stream off, /stream reset.", state, source))
}

func (d *Dispatcher) newStreamSession(ctx context.Context, chatID int64, sender StreamSender) *streamSession {
	var interval = time.Duration(d.deps.Cfg.StreamEditIntervalMS) * time.Millisecond
	if interval <= 0 {
		// cadence is not something a caller should have to remember).
		interval = time.Duration(config.DefaultStreamEditIntervalMS) * time.Millisecond
	}
	return &streamSession{
		ctx:        ctx,
		chatID:     chatID,
		sender:     sender,
		dispatcher: d,
		interval:   interval,
		maxRunes:   int(d.deps.Cfg.MaxMessageChunk),
		start:      time.Now(),
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
	if !s.posted {
		s.post()
		return
	}
	if !s.due() {
		s.skipped++
		return
	}
	var elapsed = time.Since(s.start).Round(time.Second)
	if s.answer.Len() == 0 {
		s.edit(s.ctx, fmt.Sprintf(msgStreamThinking, info.ReasoningRunes, elapsed), false)
		return
	}
	chunks, err := s.answerChunks()
	if err != nil {
		s.muted = true
		return
	}
	s.rotateChunks(chunks)
	s.edit(s.ctx, s.partialViewChunks(chunks), false)
}

// refresh — an answer token arrived. The first delta posts the message when no Progress got there first (a
func (s *streamSession) refresh() error {
	if s.muted {
		s.skipped++
		return nil
	}
	if !s.posted {
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
	s.edit(s.ctx, s.partialViewChunks(chunks), false)
	return nil
}

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
	if len(runes) <= room {
		return text
	}
	return strings.TrimRight(string(runes[:room]), " \t\n\r")
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
	s.postText(text)
}

func (s *streamSession) stage(text string) {
	if s.muted || text == "" || s.stageText == text {
		return
	}
	s.stageText = text
	if !s.posted {
		s.postText(text)
		return
	}
	_ = s.edit(s.ctx, text, false)
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
	if !s.posted || len(s.messageIDs) == 0 {
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
	var runes = []rune(s.currentAnswer())
	var keep = s.maxRunes - Runes(notice)
	if keep < 0 {
		keep = 0
	}
	if len(runes) > keep {
		runes = runes[:keep]
	}
	return strings.TrimRight(string(runes), " \t\n\r") + notice
}

func (s *streamSession) sawText() bool { return s.answer.Len() > 0 }

func (s *streamSession) stats() (edits int, skipped int, failures int) {
	return s.edits, s.skipped, s.editFailures + s.postFailures
}
