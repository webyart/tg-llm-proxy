package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestToolCallDecodeAndRoundTrip(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`{"model":"qwen","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"calculator","arguments":"{\"expression\":\"2+2\"}"}}]},"finish_reason":"tool_calls"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"model":"qwen","choices":[{"message":{"role":"assistant","content":"4"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client, err := New(Options{ChatURL: server.URL + "/v1/chat/completions", Model: "qwen", Attempts: 1, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tools := []ToolDefinition{{Name: "calculator", Description: "arithmetic", Parameters: map[string]any{"type": "object"}}}
	first, err := client.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "2+2"}}, Tools: tools})
	if err != nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "calculator" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	messages := []Message{
		{Role: RoleUser, Content: "2+2"},
		{Role: RoleAssistant, ToolCalls: first.ToolCalls},
		{Role: RoleTool, ToolCallID: "call-1", ToolName: "calculator", Content: `{"result":4}`},
	}
	if _, err := client.Complete(context.Background(), Request{Messages: messages, Tools: tools}); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if !strings.Contains(bodies[0], `"tools":[{"type":"function"`) || !strings.Contains(bodies[1], `"tool_call_id":"call-1"`) || !strings.Contains(bodies[1], `"name":"calculator"`) {
		t.Fatalf("tool wire bodies=%q", bodies)
	}
}

func TestPromptCharsMatchesTheWireBody(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"qwen","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	client, err := New(Options{ChatURL: server.URL + "/v1/chat/completions", Model: "qwen", Attempts: 1, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	messages := []Message{
		{Role: RoleSystem, Content: "инструкция"},
		{Role: RoleUser, Content: "вопрос 2+2"},
	}
	tools := []ToolDefinition{{Name: "calculator", Description: "arithmetic", Parameters: map[string]any{"type": "object", "properties": map[string]any{"expression": map[string]any{"type": "string"}}}}}
	if _, err := client.Complete(context.Background(), Request{Messages: messages, Tools: tools}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var sent struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := 0
	for _, message := range sent.Messages {
		want += len([]rune(message.Content))
	}
	want += len(sent.Tools)
	if got := PromptChars(messages, tools); got != want {
		t.Fatalf("PromptChars = %d, want %d (messages + tool JSON sent on the wire)", got, want)
	}
	// A request without tools must not invent schema bytes.
	if got := PromptChars(messages, nil); got != len([]rune("инструкция"))+len([]rune("вопрос 2+2")) {
		t.Fatalf("PromptChars without tools = %d", got)
	}
}

func TestStreamCollectsFragmentedToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"calculator","arguments":"{\"expression\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"2+2\"}"}}]},"finish_reason":"tool_calls"}]}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := New(Options{ChatURL: server.URL + "/v1/chat/completions", Model: "qwen", Attempts: 1, Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	response, err := client.Stream(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "2+2"}}}, StreamHandler{})
	if err != nil || len(response.ToolCalls) != 1 || response.ToolCalls[0].Arguments != `{"expression":"2+2"}` {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}
