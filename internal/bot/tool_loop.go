package bot

import (
	"context"
	"errors"

	"bot/internal/model"
	"bot/internal/store"
)

const toolRoundLimit = 2

func (d *Dispatcher) callModel(ctx context.Context, req model.Request, streamer model.Streamer, session *streamSession) (model.Response, error) {
	d.activeModelRequests.Add(1)
	defer d.activeModelRequests.Add(-1)
	if session != nil {
		return streamer.Stream(ctx, req, session.handler())
	}
	return d.deps.Model.Complete(ctx, req)
}

func (d *Dispatcher) completeWithTools(ctx context.Context, userID int64, req model.Request, streamer model.Streamer, session *streamSession) (model.Response, error) {
	req.Tools = append(calculatorTools(d.deps.Cfg.ToolsEnabled), urlReaderTools(d.deps.Cfg.URLReaderEnabled)...)
	req.Tools = append(req.Tools, webSearchTools(d.deps.Cfg.WebSearchEnabled)...)
	for round := 0; round < toolRoundLimit; round++ {
		resp, err := d.callModel(ctx, req, streamer, session)
		if err != nil || len(resp.ToolCalls) == 0 {
			return resp, err
		}
		if len(resp.ToolCalls) > 2 {
			return model.Response{}, errors.New("model requested more than two tool calls in one round")
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
