package bot

import (
	"context"
	"strings"

	"bot/internal/model"
)

// toolMarkupPrefixes are the opening tags of the textual tool-call form. A stream that starts with one of them is
// a tool call the tool loop will parse, so its deltas must not reach the screen first.
var toolMarkupPrefixes = []string{"<tool_call", "<function=", "<parameter="}

// textualToolStreamGuard suppresses a streamed textual tool call. The model client still accumulates the full
// content, so runToolLoop can parse and execute it; only the user-visible deltas are held back. Normal answers are
// passed through as soon as their prefix stops matching the markup (including a short "<" that turns out to be
// ordinary text).
type textualToolStreamGuard struct {
	inner model.Streamer
}

func (g *textualToolStreamGuard) Stream(ctx context.Context, req model.Request, handler model.StreamHandler) (model.Response, error) {
	if g == nil || g.inner == nil {
		return model.Response{}, nil
	}
	var held strings.Builder
	var decided bool
	var suppressed bool
	var guarded = handler
	if handler.Delta != nil {
		guarded.Delta = func(text string) error {
			if suppressed {
				return nil
			}
			if decided {
				return handler.Delta(text)
			}
			held.WriteString(text)
			var trimmed = strings.TrimLeft(held.String(), " \t\r\n")
			if trimmed == "" {
				return nil
			}
			var lower = strings.ToLower(trimmed)
			var markup, undecided bool
			for _, prefix := range toolMarkupPrefixes {
				if strings.HasPrefix(lower, prefix) {
					markup = true
					break
				}
				if strings.HasPrefix(prefix, lower) || (strings.HasPrefix(lower, "<") && !strings.Contains(lower, ">")) {
					undecided = true
				}
			}
			if markup {
				suppressed = true
				decided = true
				held.Reset()
				return nil
			}
			if undecided {
				return nil
			}
			decided = true
			var buffered = held.String()
			held.Reset()
			return handler.Delta(buffered)
		}
	}
	var response, err = g.inner.Stream(ctx, req, guarded)
	if !decided && held.Len() > 0 && handler.Delta != nil {
		_ = handler.Delta(held.String())
	}
	return response, err
}
