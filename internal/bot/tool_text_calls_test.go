package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
)

const textualSearchAnswer = `<tool_call>
<function=web_search>
<parameter=query>
MrSotnik Habr 106581 вакансий медианная зарплата 2026
</parameter>
</function>
</tool_call>
<tool_call>
<function=web_search>
<parameter=query>
hh.Индекс IT 2026 количество вакансий
</parameter>
</function>
</tool_call>`

func TestParseTextualToolCallsReadsTheModelMarkup(t *testing.T) {
	var calls, remaining, markup = parseTextualToolCalls(textualSearchAnswer)
	if !markup || len(calls) != 2 || strings.TrimSpace(remaining) != "" {
		t.Fatalf("calls=%+v remaining=%q markup=%v", calls, remaining, markup)
	}
	if calls[0].Name != "web_search" || !strings.Contains(calls[0].Arguments, "MrSotnik") {
		t.Fatalf("first call=%+v", calls[0])
	}
	if calls[0].ID == calls[1].ID {
		t.Fatal("textual calls share an id")
	}
	var withText, text, _ = parseTextualToolCalls("Проверяю источники.\n" + textualSearchAnswer + "\nГотово.")
	if len(withText) != 2 || !strings.Contains(text, "Проверяю") || strings.Contains(text, "<tool_call>") {
		t.Fatalf("text around the calls was lost: %q", text)
	}
	if _, stripped, markup := parseTextualToolCalls("<tool_call><function=calculator>2+2"); !markup || stripped != "" {
		t.Fatalf("malformed markup must be detected and stripped: %q markup=%v", stripped, markup)
	}
	if calls, remaining, markup := parseTextualToolCalls("обычный ответ"); markup || calls != nil || remaining != "обычный ответ" {
		t.Fatal("plain text was treated as markup")
	}
}

func TestToolLoopExecutesTextualToolCallsInsteadOfShowingThem(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TOOLS_ENABLED": "true"})
	var m = &scriptedToolModel{responses: []model.Response{
		{Content: "<tool_call>\n<function=calculator>\n<parameter=expression>\n2+2*3\n</parameter>\n</function>\n</tool_call>", Attempts: 1},
		{Content: "Итог: 8", Attempts: 1},
	}}
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, nil)
	var response, err = d.completeWithTools(context.Background(), 11, model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "посчитай"}}}, nil, nil)
	if err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	if response.Content != "Итог: 8" || len(m.requests) != 2 {
		t.Fatalf("response=%+v calls=%d", response, len(m.requests))
	}
	var result string
	for _, message := range m.requests[1].Messages {
		if message.Role == model.RoleTool {
			result = message.Content
		}
	}
	if !strings.Contains(result, "8") {
		t.Fatalf("the parsed calculator call did not run: tool result=%q", result)
	}
	if strings.Contains(response.Content, "<tool_call") {
		t.Fatalf("raw markup reached the answer: %q", response.Content)
	}
}

func TestToolLoopRetriesOnceOnUnparsableMarkupAndSanitizes(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TOOLS_ENABLED": "true"})
	var m = &scriptedToolModel{responses: []model.Response{
		{Content: "<tool_call>\n<function=calculator>\n<parameter=expression>\n2+2", Attempts: 1},
		{Content: "Обычный ответ без вызова", Attempts: 1},
	}}
	var d = newTestDispatcher(t, cfg, m, &fakeReplier{}, nil)
	var response, err = d.completeWithTools(context.Background(), 11, model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "вопрос"}}}, nil, nil)
	if err != nil {
		t.Fatalf("completeWithTools: %v", err)
	}
	if len(m.requests) != 2 {
		t.Fatalf("model calls=%d, want one corrective retry", len(m.requests))
	}
	var corrective bool
	for _, message := range m.requests[1].Messages {
		if message.Role == model.RoleSystem && strings.Contains(message.Content, "native tool_calls") {
			corrective = true
		}
	}
	if !corrective || strings.Contains(response.Content, "<tool_call") || response.Content != "Обычный ответ без вызова" {
		t.Fatalf("corrective=%v response=%q", corrective, response.Content)
	}
}

func TestFormatModelAnswerStripsLeftoverMarkup(t *testing.T) {
	var resp = model.Response{Content: textualSearchAnswer}
	var got = formatModelAnswer("", resp, "m", answerFormatOptions{})
	if strings.Contains(got, "<tool_call") || strings.Contains(got, "<function=") {
		t.Fatalf("finalizer kept raw markup: %q", got)
	}
}
