// Group reply threading. In a private chat an answer is simply the next message; in a group several
// conversations share one chat id, so an unanchored answer is ambiguous. Every group answer therefore
// quotes the update it answers with Bot API reply_parameters, and a supergroup forum answer stays in
// the topic of the question (message_thread_id). Private chats never carry a target, so their wire
// behaviour is unchanged.
package bot

import (
	"context"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// groupReplyTarget is the outgoing anchor of one group update: the message the bot answers and the
// forum topic it belongs to. Both are zero when the transport has nothing to anchor to.
type groupReplyTarget struct {
	MessageID int64
	ThreadID  int64
}

type groupReplyTargetKey struct{}

// withGroupReplyTarget puts the reply anchor of a group update on the context used by the whole
// answer. All sends of that answer (the ordinary reply, a stream placeholder, a menu, an exported
// document, typing feedback) read it from there, so none of them needs an extra parameter and a
// private dispatch never gets one.
func withGroupReplyTarget(ctx context.Context, ev Event) context.Context {
	if !isGroupChat(ev) || ev.TelegramMessageID <= 0 && ev.MessageThreadID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, groupReplyTargetKey{}, groupReplyTarget{
		MessageID: ev.TelegramMessageID,
		ThreadID:  ev.MessageThreadID,
	})
}

func groupReplyTargetFrom(ctx context.Context) (groupReplyTarget, bool) {
	target, ok := ctx.Value(groupReplyTargetKey{}).(groupReplyTarget)
	return target, ok && (target.MessageID > 0 || target.ThreadID > 0)
}

// replyParameters — nil when there is no message to quote, so an ordinary send keeps its old shape.
// AllowSendingWithoutReply is true because the quoted update may be deleted between admission and
// delivery; the answer should still arrive.
func (t groupReplyTarget) replyParameters() *models.ReplyParameters {
	if t.MessageID <= 0 {
		return nil
	}
	return &models.ReplyParameters{MessageID: int(t.MessageID), AllowSendingWithoutReply: true}
}

func (t groupReplyTarget) applyToMessage(params *tgbot.SendMessageParams) {
	params.MessageThreadID = int(t.ThreadID)
	params.ReplyParameters = t.replyParameters()
}

func (t groupReplyTarget) applyToDocument(params *tgbot.SendDocumentParams) {
	params.MessageThreadID = int(t.ThreadID)
	params.ReplyParameters = t.replyParameters()
}

func (t groupReplyTarget) applyToAudio(params *tgbot.SendAudioParams) {
	params.MessageThreadID = int(t.ThreadID)
	params.ReplyParameters = t.replyParameters()
}

func (t groupReplyTarget) applyToChatAction(params *tgbot.SendChatActionParams) {
	params.MessageThreadID = int(t.ThreadID)
}
