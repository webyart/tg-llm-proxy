package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"bot/internal/store"
)

// TestHistoryExposesStoredFirstTokenLatency is the Mini App reload contract: the history row of a streamed
// answer carries the same first_token_ms the live done event had, and a row where nothing was measured has
// no field at all (omitempty) instead of a zero that would render «⚡ 0.0s».
func TestHistoryExposesStoredFirstTokenLatency(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)

	var ttft = int64(800)
	measured, err := store.Append(context.Background(), app.db, store.Message{
		ChatID: testChatID, UserID: testUserID, Role: store.RoleAssistant,
		Content: "стриминговый ответ", Model: "qwen-test", LatencyMs: 4000, FirstTokenMs: &ttft,
	})
	if err != nil {
		t.Fatalf("Append measured: %v", err)
	}
	unmeasured, err := store.Append(context.Background(), app.db, store.Message{
		ChatID: testChatID, UserID: testUserID, Role: store.RoleAssistant,
		Content: "ответ без измерения", Model: "qwen-test", LatencyMs: 1200,
	})
	if err != nil {
		t.Fatalf("Append unmeasured: %v", err)
	}

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/history?limit=10", bearer)
	if status != http.StatusOK {
		t.Fatalf("history status=%d body=%s", status, body)
	}
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode history: %v body=%s", err, body)
	}
	byID := make(map[int64]map[string]any, len(payload.Items))
	for _, item := range payload.Items {
		id, ok := item["id"].(float64)
		if !ok {
			t.Fatalf("history item without a numeric id: %+v", item)
		}
		byID[int64(id)] = item
	}

	measuredItem, ok := byID[measured]
	if !ok {
		t.Fatalf("the measured answer is missing from %s", body)
	}
	if got, ok := measuredItem["first_token_ms"].(float64); !ok || got != 800 {
		t.Errorf("first_token_ms = %v, want 800", measuredItem["first_token_ms"])
	}
	if latency, ok := measuredItem["latency_ms"].(float64); !ok || latency != 4000 {
		t.Errorf("latency_ms = %v, want 4000", measuredItem["latency_ms"])
	}

	unmeasuredItem, ok := byID[unmeasured]
	if !ok {
		t.Fatalf("the unmeasured answer is missing from %s", body)
	}
	if value, present := unmeasuredItem["first_token_ms"]; present {
		t.Errorf("first_token_ms = %v on an unmeasured answer, want the field to be absent", value)
	}

	var decoded struct {
		Items []historyItem `json:"items"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode typed history: %v", err)
	}
	for _, item := range decoded.Items {
		if item.ID == unmeasured && item.FirstTokenMs != nil {
			t.Errorf("typed FirstTokenMs = %v for the unmeasured answer, want nil", *item.FirstTokenMs)
		}
	}
}
