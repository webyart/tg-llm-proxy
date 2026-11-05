package bot

import (
	"context"
	"errors"

	"bot/internal/model"
	"bot/internal/store"
)

const (
	toolRoundLimit        = 2
	nativeToolInstruction = "When using a tool, call it only through the native OpenAI tool_calls field. Never write tool calls as text or XML, including <tool_call>, <function=...> and <parameter=...>. Use only exact names from the supplied tool schemas."
)

func (d *Dispatcher) callModel(ctx context.Context, req model.Request, streamer model.Streamer, session *streamSession) (model.Response, error) {
	d.activeModelRequests.Add(1)
	defer d.activeModelRequests.Add(-1)
	if session != nil {
		return streamer.Stream(ctx, req, session.handler())
	}
	return d.deps.Model.Complete(ctx, req)
}

func (d *Dispatcher) completeWithTools(ctx context.Context, userID int64, req model.Request, streamer model.Streamer, session *streamSession) (model.Response, error) {
	allowedTools := map[string]bool{
		"calculator": d.deps.Cfg.ToolsEnabled,
		"read_url":   d.deps.Cfg.ToolsEnabled && d.deps.Cfg.URLReaderEnabled,
		"web_search": d.deps.Cfg.ToolsEnabled && d.deps.Cfg.WebSearchEnabled,
	}
	req.Tools = append(calculatorTools(allowedTools["calculator"]), urlReaderTools(allowedTools["read_url"])...)
	req.Tools = append(req.Tools, webSearchTools(allowedTools["web_search"])...)
	if len(req.Tools) > 0 {
		req.Messages = append([]model.Message{{Role: model.RoleSystem, Content: nativeToolInstruction}}, req.Messages...)
	}
	for round := 0; round < toolRoundLimit; round++ {
		resp, err := d.callModel(ctx, req, streamer, session)
		if err != nil || len(resp.ToolCalls) == 0 {
			return resp, err
		}
		if len(resp.ToolCalls) > 2 {
			return model.Response{}, errors.New("model requested more than two tool calls in one round")
		}
		for _, call := range resp.ToolCalls {
			allowed, known := allowedTools[call.Name]
			if !known {
				return model.Response{}, errors.New("unknown tool")
			}
			if !allowed {
				return model.Response{}, errors.New("tool disabled")
			}
		}
		messages := append([]model.Message(nil), req.Messages...)
		messages = append(messages, model.Message{Role: model.RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, call := range resp.ToolCalls {
			var result string
			var err error
			switch call.Name {
			case "calculator":
				result, err = executeCalculator(call)
			case "read_url":
				result, err = executeURLReader(ctx, call)
			case "web_search":
				result, err = executeWebSearch(ctx, d.deps.Cfg, call)
			default:
				return model.Response{}, errors.New("unknown tool")
			}
			if err != nil {
				return model.Response{}, err
			}
			messages = append(messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: result})
			d.logEvent(ctx, userID, store.EventUsage, call.Name)
		}
		req.Messages = messages
	}
	req.Tools = nil
	return d.callModel(ctx, req, streamer, session)
}
