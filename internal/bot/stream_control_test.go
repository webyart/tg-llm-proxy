package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/store"
)

func TestStreamControlReportsAndResetsChatOverride(t *testing.T) {
	r := &streamReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, streamConfig(t, map[string]string{"STREAMING_ENABLED": "false"}), &fakeStreamModel{}, r, db)

	if err := dispatch(t, d, testEvent(100, 11, 3000, "/stream")); err != nil {
		t.Fatalf("/stream status: %v", err)
	}
	if got := r.lastEdit(); got != "" {
		t.Fatalf("status command must not edit messages, got %q", got)
	}
	texts := r.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "выключен") || !strings.Contains(texts[0], "общий STREAMING_ENABLED") {
		t.Fatalf("default status is wrong: %q", texts)
	}
	if err := dispatch(t, d, testEvent(100, 11, 3001, "/stream on")); err != nil {
		t.Fatalf("/stream on: %v", err)
	}
	enabled, found, err := store.ChatStreaming(context.Background(), db, 100)
	if err != nil || !found || !enabled {
		t.Fatalf("override=%v found=%v err=%v, want on", enabled, found, err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 3002, "/status")); err != nil {
		t.Fatalf("/status: %v", err)
	}
	texts = r.texts()
	if !strings.Contains(texts[len(texts)-1], "Стриминг: включён") || !strings.Contains(texts[len(texts)-1], "override этого чата") {
		t.Fatalf("status does not show the effective override: %q", texts[len(texts)-1])
	}
	if err := dispatch(t, d, testEvent(100, 11, 3003, "/stream reset")); err != nil {
		t.Fatalf("/stream reset: %v", err)
	}
	if _, found, err := store.ChatStreaming(context.Background(), db, 100); err != nil || found {
		t.Fatalf("reset left override found=%v err=%v", found, err)
	}
	texts = r.texts()
	if !strings.Contains(texts[len(texts)-1], "выключен") || !strings.Contains(texts[len(texts)-1], "общий STREAMING_ENABLED") {
		t.Fatalf("reset status is wrong: %q", texts[len(texts)-1])
	}
}

func TestChatStreamingOverrideSelectsModelPath(t *testing.T) {
	cases := []struct {
		name       string
		global     string
		action     string
		wantStream bool
	}{
		{name: "on overrides global off", global: "false", action: "on", wantStream: true},
		{name: "off overrides global on", global: "true", action: "off", wantStream: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeStreamModel{steps: []streamStep{{content: "ответ"}}}
			r := &streamReplier{}
			d := newTestDispatcher(t, streamConfig(t, map[string]string{"STREAMING_ENABLED": tc.global}), m, r, testStore(t))
			if err := dispatch(t, d, testEvent(101, 11, 3010, "/stream "+tc.action)); err != nil {
				t.Fatalf("/stream %s: %v", tc.action, err)
			}
			if err := dispatch(t, d, testEvent(101, 11, 3011, "/ask вопрос")); err != nil {
				t.Fatalf("/ask: %v", err)
			}
			got := m.streamed()
			if tc.wantStream && got != 1 {
				t.Fatalf("stream calls=%d, want 1", got)
			}
			if !tc.wantStream && got != 0 {
				t.Fatalf("stream calls=%d, want plain path", got)
			}
			if len(assistantRows(t, d.deps.DB, 101)) != 1 {
				t.Fatal("both paths must write exactly one assistant row")
			}
		})
	}
}
