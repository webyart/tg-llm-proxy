package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

type guardFakeStreamer struct {
	deltas []string
}

func (f *guardFakeStreamer) Stream(ctx context.Context, req model.Request, handler model.StreamHandler) (model.Response, error) {
	for _, delta := range f.deltas {
		if handler.Delta != nil {
			if err := handler.Delta(delta); err != nil {
				return model.Response{}, err
			}
		}
	}
	return model.Response{Content: strings.Join(f.deltas, "")}, nil
}

func collectGuardDeltas(t *testing.T, deltas []string) []string {
	t.Helper()
	var guard = &textualToolStreamGuard{inner: &guardFakeStreamer{deltas: deltas}}
	var seen []string
	if _, err := guard.Stream(context.Background(), model.Request{}, model.StreamHandler{Delta: func(text string) error {
		seen = append(seen, text)
		return nil
	}}); err != nil {
		t.Fatalf("guard Stream: %v", err)
	}
	return seen
}

func TestStreamGuardSuppressesTextualToolMarkup(t *testing.T) {
	var seen = collectGuardDeltas(t, []string{"<tool_call>", "\n<function=web_search>\n", "<parameter=query>q</parameter>", "</function></tool_call>"})
	if len(seen) != 0 {
		t.Fatalf("tool markup reached the stream: %q", seen)
	}
}

func TestStreamGuardPassesNormalText(t *testing.T) {
	var seen = collectGuardDeltas(t, []string{"Обычный ", "ответ"})
	if strings.Join(seen, "") != "Обычный ответ" {
		t.Fatalf("normal text was buffered away: %q", seen)
	}
}

func TestStreamGuardPassesTextStartingWithLessThan(t *testing.T) {
	var seen = collectGuardDeltas(t, []string{"2 < 3", " — верно"})
	if strings.Join(seen, "") != "2 < 3 — верно" {
		t.Fatalf("ordinary '<' was suppressed: %q", seen)
	}
}

func TestToolLoopDropsUnknownTextualCallsInsteadOfFailing(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TOOLS_ENABLED": "true"})
	var m = &scriptedToolModel{responses: []model.Response{{
		Content:  "<tool_call>\n<function=web_extractor>\n<parameter=urls>\nhttps://example.test\n</parameter>\n</function>\n</tool_call>\nПолезный вывод.",
		Attempts: 1,
	}}}
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, nil)
	var response, err = d.completeWithTools(context.Background(), 11, model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "вопрос"}}}, nil, nil)
	if err != nil {
		t.Fatalf("an unknown textual tool failed the whole answer: %v", err)
	}
	if response.Content != "Полезный вывод." || strings.Contains(response.Content, "<tool_call") {
		t.Fatalf("response=%q", response.Content)
	}
}

func TestContextForModelStripsStoredToolMarkup(t *testing.T) {
	var d = &Dispatcher{deps: Deps{}}
	var got = d.forModel(store.Message{Role: store.RoleAssistant, Content: "<tool_call><function=web_search>old</function></tool_call>\nОтвет"})
	if strings.Contains(got, "<tool_call") || got != "Ответ" {
		t.Fatalf("stored markup leaked into the model context: %q", got)
	}
}
