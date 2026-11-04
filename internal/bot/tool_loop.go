package bot

import (
	"context"
	"errors"

	"bot/internal/model"
	"bot/internal/store"
)

const calculatorRoundLimit = 2

func (d *Dispatcher) callModel(ctx context.Context, req model.Request, streamer model.Streamer, session *streamSession) (model.Response, error) {
	d.activeModelRequests.Add(1)
	defer d.activeModelRequests.Add(-1)
	if session != nil {
		return streamer.Stream(ctx, req, session.handler())
	}
	return d.deps.Model.Complete(ctx, req)
}

func (d *Dispatcher) completeWithTools(ctx context.Context, userID int64, req model.Request, streamer model.Streamer, session *streamSession) (model.Response, error) {
	req.Tools = calculatorTools(d.deps.Cfg.ToolsEnabled)
	for round := 0; round < calculatorRoundLimit; round++ {
		resp, err := d.callModel(ctx, req, streamer, session)
		if err != nil || len(resp.ToolCalls) == 0 {
			return resp, err
		}
		if len(resp.ToolCalls) > 2 {
			return model.Response{}, errors.New("model requested more than two calculator calls in one round")
		}
		if session != nil && session.sawText() {
			return model.Response{}, errors.New("tool call followed streamed answer text")
		}
		messages := append([]model.Message(nil), req.Messages...)
		messages = append(messages, model.Message{Role: model.RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, call := range resp.ToolCalls {
			result, err := executeCalculator(call)
			if err != nil {
				return model.Response{}, err
			}
			messages = append(messages, model.Message{Role: model.RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: result})
			d.logEvent(ctx, userID, store.EventUsage, "calculator")
		}
		req.Messages = messages
	}
	return model.Response{}, errors.New("model exceeded the calculator round limit")
}
