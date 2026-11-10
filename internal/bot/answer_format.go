package bot

import (
	"strings"

	"bot/internal/model"
)

// documentTruncationNotice is the prefix Telegram has always shown when MAX_CONTEXT_CHARS cut the
// document before the model saw it. It lives here because both clients now assemble it through the
// same finalizer instead of one of them silently dropping it.
const documentTruncationNotice = "Внимание: документ был обрезан по MAX_CONTEXT_CHARS перед отправкой модели."

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
	full := content
	if opts.Latency {
		full += "\n\n" + latencyLine(resp, selectedModel)
	}
	// A provider that stopped on its own limit (length) or filter (content_filter) must say so. A
	// response whose content already ends with the notice keeps the single copy it has: streamed
	// continuations of the same answer must not grow a second warning on every resume.
	if notice := finishReasonNotice(resp.FinishReason); notice != "" && !answerEndsWith(content, notice) {
		full = notice + "\n\n" + full
	}
	if len(opts.Sources) > 0 {
		full += "\n\nИсточники: " + strings.Join(opts.Sources, ", ")
	}
	if opts.DocumentTruncated && !answerStartsWith(content, documentTruncationNotice) {
		full = documentTruncationNotice + "\n\n" + full
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
