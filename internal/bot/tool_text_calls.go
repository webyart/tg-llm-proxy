package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"bot/internal/i18n"
	"bot/internal/model"
)

// Some local models answer with the tool call written as text instead of the native OpenAI tool_calls field:
//
//	<tool_call>
//	<function=web_search>
//	<parameter=query>
//	...
//	</parameter>
//	</function>
//	</tool_call>
//
// The tool loop executes only native calls, so before this parser existed the markup was shown to the user as the
// answer. parseTextualToolCalls turns the documented shape into real calls; the caller then runs them through the
// SAME allowlist, normalisation and approval path as native calls. Nothing is executed here.
var (
	textualToolBlock = regexp.MustCompile(`(?is)<tool_call\b[^>]*>(.*?)</tool_call>`)
	textualFunction  = regexp.MustCompile(`(?is)<function\s*=\s*([A-Za-z0-9_.:-]+)\s*>(.*?)</function>`)
	textualParameter = regexp.MustCompile(`(?is)<parameter\s*=\s*([A-Za-z0-9_.:-]+)\s*>(.*?)</parameter>`)
	textualMarkup    = regexp.MustCompile(`(?is)<tool_call\b|<function\s*=|<parameter\s*=`)
)

// textualToolCallLimit caps what one answer can inject; the tool loop itself executes at most two calls per round.
const textualToolCallLimit = 4

const textualToolRetryInstruction = "Your previous message wrote a tool call as text or XML instead of the native tool_calls field. Do not repeat the markup. Either call the tool through the native tool_calls field, using the supplied schema, or answer the user in plain text without any tool-call markup."

// parseTextualToolCalls returns the calls found in content, the text outside the blocks and whether any markup was
// present at all. A markup with zero parsed calls is reported so the caller can retry instead of showing it.
func parseTextualToolCalls(content string) (calls []model.ToolCall, remaining string, markup bool) {
	if !textualMarkup.MatchString(content) {
		return nil, content, false
	}
	markup = true
	for _, block := range textualToolBlock.FindAllStringSubmatch(content, -1) {
		var function = textualFunction.FindStringSubmatch(block[1])
		if function == nil {
			continue
		}
		var name = strings.TrimSpace(function[1])
		if name == "" {
			continue
		}
		var arguments = map[string]string{}
		for _, parameter := range textualParameter.FindAllStringSubmatch(function[2], -1) {
			var key = strings.TrimSpace(parameter[1])
			if key == "" {
				continue
			}
			arguments[key] = strings.TrimSpace(parameter[2])
		}
		var encoded, err = json.Marshal(arguments)
		if err != nil {
			continue
		}
		calls = append(calls, model.ToolCall{
			ID:        fmt.Sprintf("textual-%d", len(calls)+1),
			Name:      name,
			Arguments: string(encoded),
		})
		if len(calls) >= textualToolCallLimit {
			break
		}
	}
	return calls, stripTextualToolCalls(content), true
}

// stripTextualToolCalls removes any tool-call markup from a user-visible answer. It is the last line of defence:
// even a malformed, unparsable call must never reach the chat as raw XML.
func stripTextualToolCalls(content string) string {
	if !textualMarkup.MatchString(content) {
		return content
	}
	var cleaned = textualToolBlock.ReplaceAllString(content, "")
	cleaned = strings.TrimSpace(cleaned)
	// An unclosed block survives the replacement above; cut it and everything after it so a malformed call
	// cannot leak its tags or a half-typed argument into the answer.
	if malformed := textualMarkup.FindStringIndex(cleaned); malformed != nil {
		cleaned = strings.TrimSpace(cleaned[:malformed[0]])
	}
	return cleaned
}

// recoverTextualToolCalls turns a text/XML tool call into a native one and never lets the raw markup reach the
// chat. When the markup is present but unparsable, one corrective model call is made; if it still fails, the
// markup is removed and (when nothing else is left) the user gets a clear retry message instead of XML.
func (d *Dispatcher) recoverTextualToolCalls(ctx context.Context, state *toolLoopContinuation, resp model.Response) model.Response {
	var calls, remaining, markup = parseTextualToolCalls(resp.Content)
	if !markup {
		return resp
	}
	if len(calls) == 0 && !state.textualToolRetry {
		state.textualToolRetry = true
		var retryRequest = state.request
		retryRequest.Messages = append([]model.Message{{Role: model.RoleSystem, Content: textualToolRetryInstruction}}, state.request.Messages...)
		if retryResponse, retryErr := d.callModel(ctx, state.userID, retryRequest, state.streamer, state.session); retryErr == nil {
			resp = retryResponse
			calls, remaining, markup = parseTextualToolCalls(resp.Content)
			if !markup {
				return resp
			}
		}
	}
	if len(calls) > 0 {
		if d.deps.Logger != nil {
			d.deps.Logger.Warn("the model wrote tool calls as text; parsed them into native calls",
				"chat_id", state.metadata.event.ChatID, "calls", len(calls))
		}
		resp.ToolCalls = calls
		resp.Content = remaining
		return resp
	}
	if d.deps.Logger != nil {
		d.deps.Logger.Warn("the model wrote unparsable tool-call markup; removed it from the answer",
			"chat_id", state.metadata.event.ChatID)
	}
	resp.Content = stripTextualToolCalls(resp.Content)
	if resp.Content == "" {
		resp.Content = i18n.T(d.chatLanguage(ctx, state.metadata.event.ChatID), "ask.tool_markup_failed")
	}
	return resp
}

// SanitizeToolMarkup is the exported form of stripTextualToolCalls for the webapp history/API layer: a stored
// answer from before v1.62.2 may still contain raw tool-call markup and must not be shown to a user.
func SanitizeToolMarkup(content string) string { return stripTextualToolCalls(content) }
