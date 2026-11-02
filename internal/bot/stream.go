// stream.go — the §15 «стриминг ответов» on the Telegram side (§16 change 34). The model emits ~48 SSE events
// per second, so a literal edit-per-token would be a flood. Even a sustained one-second cadence reached a real
// retry_after=120 in the §11 live walk; STREAM_EDIT_INTERVAL_MS therefore defaults to five seconds, and the
// chunking logic of reply.go is reused rather than rewritten (§10 День 4 flood).
//
// Three rules of the existing design survive the stream, and each is the reason for one branch below:
//
//   - §12 UX «экранирование до разбиения» (escape, then cut). A PARTIAL answer cannot be escaped into valid
//     markup: MarkdownV2 and HTML both need balanced entities, and an unbalanced edit is the 400 Telegram
//     answers «can't parse entities». So the interim edits go as PLAIN text and only the final edit carries the
//     parse mode of §7. The user sees `*текст*` while it arrives and formatted text when it ends — that is the
//     honest reading of the same §7 row, not a second formatting mode.
//   - §7 MAX_MESSAGE_CHUNK bounds ONE message. The placeholder can only ever hold the head of a long answer;
//     when the answer is longer, the tail goes as new messages through the ordinary path of reply.go, so a
//     streamed answer of 6034 runes (the measurement of §14) still arrives complete.
//   - §10 День 3 writes NO assistant row for a failed call. A stream that broke halfway delivered text the user
//     already read, and it still writes no assistant row: the history of §5 would otherwise be built on an
//     answer the model never finished. The events row of §5 is written as before, and the placeholder message
//     says out loud that it is broken, so nothing in the chat looks like a finished answer.
package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

// The texts of the streaming placeholders, in the language of §6. The counter is in RUNES because that is the
// unit Telegram measures its limits in (§12 UX) and because the provider's token counts arrive only with the
// last event of the stream, so a placeholder may never promise «осталось N токенов».
const (
	// StreamPlaceholder — the first thing a chat sees of a streamed answer (§15).
	StreamPlaceholder = "Модель думает…"
	msgStreamThinking = "Модель думает: %d символов размышлений, %s (§15)"
	// msgStreamSuffix is a SUFFIX with a template of its own because it is the ANSWER that gets cut: an edit
	// longer than MAX_MESSAGE_CHUNK (§7) is the 400 Telegram answers «too long», so partialView reserves the room
	// of the markers before it decides how much of the answer fits into the message.
	msgStreamSuffix = "\n\n… ответ пишется, %d символов (§15)"
	// msgStreamCutMarker — the rest of the answer does not fit one message; it arrives after the stream ends.
	msgStreamCutMarker = "\n… (дальше — следующим сообщением)"
	// msgStreamBroken — the stream stopped with part of the answer already on the screen. The template is short
	// on purpose: it has to fit ONE message of MAX_MESSAGE_CHUNK together with the reason, and the loader of §7
	// puts the floor of that row at 64 symbols.
	msgStreamBroken = "\n\n… (прервано: %s — в истории этого ответа нет, §15)"
)

// fitNotice — a notice whose TEMPLATE must survive, with `part` shortened so the whole fits maxRunes runes
// (§7 MAX_MESSAGE_CHUNK, §16 change 7: the cut goes by runes). The part that gets cut is the reason of the
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

// StreamSender — the outgoing side of §15 as an optional capability of the Replier of §6, for the same reason
// model.Streamer is optional: the plain sender of every other answer of §6 does not need it, and the fakes of
// §11 that only record texts must keep compiling. The real implementation is the telegramReplier of run.go.
//
// `parse` carries the parse mode of §7 on the FINAL edit only — see the header of this file.
type StreamSender interface {
	// SendStreamMessage posts the placeholder and returns the id of the message every later edit goes to.
	SendStreamMessage(ctx context.Context, chatID int64, text string) (int64, error)
	// EditStreamMessage rewrites that message.
	EditStreamMessage(ctx context.Context, chatID int64, messageID int64, text string, parse bool) error
}

// streamSession — one streamed answer of one chat. The callbacks of the stream run on the single goroutine
// that reads the SSE body (model.StreamHandler says so), so this struct needs no lock for its own counters;
// what it does share is the Replier, which every sender of §10 День 4 already guards with the per-chat lock.
type streamSession struct {
	ctx      context.Context
	chatID   int64
	sender   StreamSender
	interval time.Duration
	maxRunes int
	start    time.Time

	messageID int64
	posted    bool
	answer    strings.Builder

	edits        int
	skipped      int // rewrites the cadence of §7 refused
	postFailures int // the placeholder never arrived
	editFailures int // rewrites Telegram refused
	lastEdit     time.Time
	muted        bool // the outgoing side of this chat gave up on the growing message (§15)
}

// deliveryFailureBudget — how many refused Telegram calls this chat may collect before the session stops
// rewriting the message. The number is small because a stream produces ~48 events per second (§14) and every
// one of them would otherwise spend a Bot API request that fails: that is the flood of §10 День 4 paid for
// nothing, on top of the GPU slot of §12. Three is enough to tell a transient Telegram error from a chat that
// is not receiving anything, and muting is not the end of the answer — deliver() still sends the whole text the
// way §10 День 3 always did.
const deliveryFailureBudget = 3

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
// A missing capability is the plain path of §10 День 3; a store failure is returned instead of silently changing mode.
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

func (d *Dispatcher) streamControl(ctx context.Context, ev Event, arg string) error {
	action := strings.ToLower(strings.TrimSpace(arg))
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
		// A configuration that skipped the row still gets the §7 default rather than editing on every token:
		// the loader would not produce this value, but a Deps built by hand can (§12 надёжность: a bounded
		// cadence is not something a caller should have to remember).
		interval = time.Duration(config.DefaultStreamEditIntervalMS) * time.Millisecond
	}
	return &streamSession{
		ctx:      ctx,
		chatID:   chatID,
		sender:   sender,
		interval: interval,
		maxRunes: int(d.deps.Cfg.MaxMessageChunk),
		start:    time.Now(),
	}
}

// handler — the callbacks of §15. Neither of them stops the stream over a delivery problem. model.StreamHandler
// gives Progress no error for exactly that reason, and Delta takes one only for the caller of §15 (the retry
// guard of the client), never for a Telegram edit this chat happened to refuse: the GPU slot of §12 is spent
// writing an answer, and throwing it away because one rewrite failed is the worse failure (§12 надёжность).
// A chat that refuses everything goes muted instead — see muteIfBroken — and the answer leaves through the
// ordinary path of §10 День 3 at the end of the stream.
func (s *streamSession) handler() model.StreamHandler {
	return model.StreamHandler{
		Progress: func(info model.StreamInfo) { s.progress(info) },
		Delta: func(text string) error {
			s.answer.WriteString(text)
			s.refresh()
			return nil
		},
	}
}

// progress — the model produces events and none of them is answer text yet. §12 UX «сообщение не зависает» is
// the reason this exists: the measured answer of §14 spent ~200 events thinking before its first content token,
// which on the plain path of §10 День 3 is a chat that shows nothing for a minute.
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
	s.edit(s.ctx, s.partialView(), false)
}

// refresh — an answer token arrived. The first delta posts the message when no Progress got there first (a
// model that does not think out loud, which is what the non-reasoning deployment of §14.2 item 6 is).
func (s *streamSession) refresh() {
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
	s.edit(s.ctx, s.partialView(), false)
}

// partialView — what the chat may see of an unfinished answer: its head inside the budget of one message, the
// marker of §7 MAX_MESSAGE_CHUNK when the rest does not fit, and the counter of §15 in runes (§12 UX: the
// limit of Telegram is measured in symbols, and the provider's token counts only arrive with the last event).
// The two markers are reserved their room BEFORE the answer is cut, which is what keeps every interim edit
// inside the limit of one message — a rewrite Telegram refuses for length is a rewrite the user never sees.
func (s *streamSession) partialView() string {
	var total = Runes(s.answer.String())
	var suffix = fmt.Sprintf(msgStreamSuffix, total)
	var marker = ""
	if total > s.maxRunes {
		marker = msgStreamCutMarker
	}
	// The markers are worth more than the head they decorate, and of the two, the counter («it is still being
	// written») is worth more than the note about where the rest goes: at the floor of MAX_MESSAGE_CHUNK (§7,
	// 64 symbols) two notices and a head do not fit together, and a rewrite past the limit is the 400 Telegram
	// answers «too long» — a rewrite the user never sees at all (§12 UX).
	if Runes(marker)+Runes(suffix) >= s.maxRunes {
		marker = ""
	}
	if Runes(suffix) >= s.maxRunes {
		suffix = ""
	}
	return s.head(Runes(suffix)+Runes(marker)) + marker + suffix
}

// head — the part of the answer the placeholder can carry once `reserve` symbols of the message are given to the
// markers; the cut goes by RUNES (§16 change 7: кириллица costs two bytes, so the cut never goes by bytes). An
// empty head is a legal answer: at the floor of MAX_MESSAGE_CHUNK (§7, 64 symbols) the markers alone can fill
// the message, and sending more than the limit is the 400 Telegram answers «too long» rather than a reason to
// grow the message.
func (s *streamSession) head(reserve int) string {
	var text = s.answer.String()
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

// due — the cadence of §7 STREAM_EDIT_INTERVAL_MS. The client of §15 gates its Progress callback by the same
// row; this gate is the one that matters, because it is the edit rate into one Telegram chat that §10 День 4
// flood is about.
func (s *streamSession) due() bool {
	return time.Since(s.lastEdit) >= s.interval
}

// post — the placeholder of §15. The first failure is not fatal: the next event tries again, and a transient
// Telegram error must not throw away a working model answer (§12 надёжность). Enough failures in a row and the
// session goes muted: the answer keeps accumulating and leaves through d.Send at the end of the stream.
func (s *streamSession) post() {
	var text = StreamPlaceholder
	if s.answer.Len() > 0 {
		text = s.partialView()
	}
	var id, err = s.sender.SendStreamMessage(s.ctx, s.chatID, text)
	if err != nil {
		s.postFailures++
		s.muteIfBroken()
		return
	}
	s.messageID, s.posted, s.lastEdit = id, true, time.Now()
	s.edits++
}

// muteIfBroken — the point past which this chat is not receiving the growing message. Muting stops the calls,
// not the answer: the deltas keep accumulating in s.answer and deliver() sends the finished text as the ordinary
// messages of §10 День 3, so a stream Telegram refused from the first placeholder still reaches the user.
func (s *streamSession) muteIfBroken() {
	if s.postFailures+s.editFailures >= deliveryFailureBudget {
		s.muted = true
	}
}

// edit — one rewrite of the placeholder. A failed rewrite does not end the answer: §15 is an improvement of
// §12 UX, and the ordinary answer of §10 День 3 is still reachable at the end of this call (deliver sends what
// the chat missed). The failure is counted so the log line of §10 День 7 says whether the stream really streamed.
func (s *streamSession) edit(ctx context.Context, text string, parse bool) error {
	if !s.posted || s.messageID == 0 {
		return fmt.Errorf("there is no message to edit (§15: the placeholder never arrived)")
	}
	var err = s.sender.EditStreamMessage(ctx, s.chatID, s.messageID, text, parse)
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
// The text is escaped ONCE and cut by the rules of reply.go (§16 change 7: «экранирование до разбиения»), which
// is the same helper d.Send uses, so the chunking of §12 UX cannot drift into a second implementation here. The
// final edit carries the parse mode of §7 (`parse=true`): while the answer was arriving it could not, because a
// half-open MarkdownV2 entity is the 400 Telegram answers «can't parse entities» (§12 UX).
//
// A stream that never got a message into the chat (every post failed, or the model produced no event before its
// budget ran out) delivers through d.Send: the user gets the answer of §10 День 3 even though §15 failed.
func (s *streamSession) deliver(ctx context.Context, d *Dispatcher, full string) error {
	var chunks = d.chunksOf(full)
	if len(chunks) == 0 {
		return nil // an empty answer is the error of §10 День 3 and never reaches this call
	}
	if !s.posted || s.messageID == 0 {
		return d.Send(ctx, s.chatID, full)
	}
	if err := s.edit(ctx, chunks[0], true); err != nil {
		// The placeholder stays as it was: the tail below still has to reach the chat, and the caller of §12
		// reports the delivery failure through the log, not by losing the answer.
		d.deps.Logger.Warn("the streamed answer could not be finalized",
			"chat_id", s.chatID, "edits", s.edits, "edit_failures", s.editFailures,
			"error", Sanitize(err, d.deps.Token),
		)
	}
	var i = 1
	for i < len(chunks) {
		if err := d.deps.Replier.Send(ctx, s.chatID, chunks[i]); err != nil {
			return err
		}
		i++
	}
	return nil
}

// broken — the stream stopped with part of the answer already on the screen. §10 День 3 writes no assistant row
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
// of the counter: the TEMPLATE of §15 is what answers «is this an answer?», so it stays whole and the reason of
// §7 is what gets cut (fitNotice). The head of the answer shrinks to whatever the notice leaves, because an edit
// Telegram refuses for length leaves the user with the placeholder of «Модель думает…» forever.
func (s *streamSession) brokenView(reason string) string {
	var notice = fitNotice(msgStreamBroken, reason, s.maxRunes)
	var runes = []rune(s.answer.String())
	var keep = s.maxRunes - Runes(notice)
	if keep < 0 {
		keep = 0
	}
	if len(runes) > keep {
		runes = runes[:keep]
	}
	return strings.TrimRight(string(runes), " \t\n\r") + notice
}

// sawText — did the user already read part of this answer? It decides what the failure branch of §12 UX says:
// «модель не ответила» next to a wall of text is the two-numbers-one-failure trap of §16 change 34, so the ask
// handler names the budget AND the number of symbols that did arrive.
func (s *streamSession) sawText() bool { return s.answer.Len() > 0 }

// stats — the numbers of the log line of §10 День 7. A streamed answer that produced zero edits is a stream
// that did not stream, and the operator should be able to see that without opening the database of §5.
func (s *streamSession) stats() (edits int, skipped int, failures int) {
	return s.edits, s.skipped, s.editFailures + s.postFailures
}
