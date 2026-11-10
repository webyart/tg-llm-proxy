package bot

import (
	"strings"

	"bot/internal/i18n"
	"bot/internal/model"
)

// documentTruncationNotice is the prefix Telegram has always shown when MAX_CONTEXT_CHARS cut the
// document before the model saw it. It lives here because both clients now assemble it through the
// same finalizer instead of one of them silently dropping it.
const documentTruncationNotice = "Внимание: документ был обрезан по MAX_CONTEXT_CHARS перед отправкой модели."

// documentTruncationNoticeFor returns the truncation notice in lang. The constant stays the Russian
// template for the Mini App and for callers that have no chat language.
func documentTruncationNoticeFor(lang string) string {
	return i18n.T(lang, "ask.doc_truncated")
}

// answerFormatOptions selects the optional parts of the final answer. The model content itself is
// always present; each client decides whether it shows the latency footer, the RAG sources and the
// document truncation notice.
type answerFormatOptions struct {
	Latency           bool
	Sources           []string
	DocumentTruncated bool
}

// formatModelAnswer builds the user-visible final text from one successful model Response. It is the
// single finalizer of the Telegram and Mini App paths:
//
//	[document truncation notice]
//	[finish_reason notice]
//	content
//
//	⏱ latency footer
//	Источники: ...
//
// The order is the one Telegram has always used, so the shared function keeps the Telegram output
// byte-identical. Mini App used to deliver only resp.Content and therefore lost the latency footer
// and both notices.
func formatModelAnswer(content string, resp model.Response, selectedModel string, opts answerFormatOptions) string {
	return formatModelAnswerFor(i18n.Default, content, resp, selectedModel, opts)
}

// formatModelAnswerFor is the language-aware finalizer used by the Telegram command surface. The
// Mini App keeps calling formatModelAnswer, so its output is unchanged.
func formatModelAnswerFor(lang string, content string, resp model.Response, selectedModel string, opts answerFormatOptions) string {
	content = stripTextualToolCalls(content)
	full := content
	if opts.Latency {
		full += "\n\n" + latencyLineFor(lang, resp, selectedModel)
	}
	// A provider that stopped on its own limit (length) or filter (content_filter) must say so. A
	// response whose content already ends with the notice keeps the single copy it has: streamed
	// continuations of the same answer must not grow a second warning on every resume.
	if notice := finishReasonNoticeFor(lang, resp.FinishReason); notice != "" && !answerEndsWith(content, notice) {
		full = notice + "\n\n" + full
	}
	if len(opts.Sources) > 0 {
		full += "\n\n" + i18n.T(lang, "ask.sources", strings.Join(opts.Sources, ", "))
	}
	var truncationNotice = documentTruncationNoticeFor(lang)
	if opts.DocumentTruncated && !answerStartsWith(content, truncationNotice) {
		full = truncationNotice + "\n\n" + full
	}
	return full
}

// answerEndsWith reports whether the model content already carries the notice at its end. The
// comparison ignores surrounding whitespace because a streamed answer may end with a newline.
func answerEndsWith(content string, notice string) bool {
	return notice != "" && strings.HasSuffix(strings.TrimSpace(content), notice)
}

// answerStartsWith reports whether the model content already begins with the truncation notice.
func answerStartsWith(content string, notice string) bool {
	return notice != "" && strings.HasPrefix(strings.TrimSpace(content), notice)
}
