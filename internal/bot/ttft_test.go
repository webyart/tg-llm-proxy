package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot/internal/i18n"
	"bot/internal/model"
)

func TestLatencyFooterShowsTTFTBeforeTheModelName(t *testing.T) {
	var in, out = int64(120), int64(300)
	var response = model.Response{
		Model:             "fake-model",
		TokensIn:          &in,
		TokensOut:         &out,
		Latency:           4600 * time.Millisecond,
		FirstTokenLatency: 800 * time.Millisecond,
		Attempts:          1,
	}
	var line = latencyLineFor(i18n.RU, response, "fake-model")
	for _, want := range []string{"⏱ 4.6s", "120→300 токенов", "⚡ 0.8s", "модель fake-model"} {
		if !strings.Contains(line, want) {
			t.Fatalf("RU footer %q misses %q", line, want)
		}
	}
	if strings.Index(line, "⚡") > strings.Index(line, "модель") {
		t.Fatalf("RU footer puts TTFT after the model name: %q", line)
	}
	var english = latencyLineFor(i18n.EN, response, "fake-model")
	if !strings.Contains(english, "⚡ 0.8s") || !strings.Contains(english, "model fake-model") {
		t.Fatalf("EN footer = %q, want the TTFT before the model", english)
	}
}

func TestLatencyFooterWithoutTTFTIsUnchanged(t *testing.T) {
	var response = model.Response{
		Model:    "fake-model",
		Latency:  1500 * time.Microsecond,
		Attempts: 1,
	}
	var line = latencyLineFor(i18n.RU, response, "fake-model")
	if strings.Contains(line, "⚡") {
		t.Fatalf("footer %q grew a TTFT marker for a response that has none", line)
	}
	if !strings.Contains(line, "⏱ 2ms") || !strings.Contains(line, "модель fake-model") {
		t.Fatalf("footer = %q, want the old latency line", line)
	}
}

func TestLatencyFooterCarriesTTFTWithoutTokenCounts(t *testing.T) {
	var response = model.Response{
		Model:             "fake-model",
		Latency:           2 * time.Second,
		FirstTokenLatency: 1200 * time.Millisecond,
		Attempts:          2,
	}
	var line = latencyLineFor(i18n.RU, response, "fake-model")
	if !strings.Contains(line, "⚡ 1.2s") || !strings.Contains(line, "токены не возвращены") {
		t.Fatalf("footer = %q, want TTFT in the no-token variant", line)
	}
}

func TestMiniAppDoneCarriesAnswerStats(t *testing.T) {
	var in, out = int64(12), int64(34)
	var streamed = &fakeStreamModel{
		response: model.Response{FirstTokenLatency: 700 * time.Millisecond, TokensIn: &in, TokensOut: &out},
		steps:    []streamStep{{content: "ответ"}},
	}
	var d = newTestDispatcher(t, streamConfig(t, nil), streamed, &fakeReplier{}, testStore(t))
	var log = &miniAppEventLog{}
	if err := d.AnswerForMiniApp(context.Background(), 11, 11, "вопрос", log.emit); err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
	var done, ok = log.done()
	if !ok || done.Stats == nil {
		t.Fatalf("done = %+v, want stats", done)
	}
	if done.Stats.FirstTokenLatency != 700*time.Millisecond {
		t.Errorf("FirstTokenLatency = %s, want 700ms", done.Stats.FirstTokenLatency)
	}
	if done.Stats.TokensIn == nil || *done.Stats.TokensIn != 12 || done.Stats.TokensOut == nil || *done.Stats.TokensOut != 34 {
		t.Errorf("tokens = %v/%v, want 12/34", done.Stats.TokensIn, done.Stats.TokensOut)
	}
	if done.Stats.TokensPerSecond == nil {
		t.Error("TokensPerSecond is nil for a successful answer with usage")
	}
	if done.Stats.Model != "fake-model" {
		t.Errorf("Model = %q, want fake-model", done.Stats.Model)
	}
	if !strings.Contains(done.Delta, "⚡ 0.7s") {
		t.Errorf("done text = %q, want the TTFT in the footer", done.Delta)
	}
}

func TestMiniAppDoneTTFTIsZeroForANonStreamedAnswer(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STREAMING_ENABLED": "false"})
	var d = newTestDispatcher(t, cfg, &fakeModel{response: model.Response{Content: "ответ"}}, &fakeReplier{}, testStore(t))
	var log = &miniAppEventLog{}
	if err := d.AnswerForMiniApp(context.Background(), 11, 11, "вопрос", log.emit); err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
	var done, ok = log.done()
	if !ok || done.Stats == nil {
		t.Fatalf("done = %+v, want stats", done)
	}
	if done.Stats.FirstTokenLatency != 0 {
		t.Errorf("FirstTokenLatency = %s, want zero without a stream", done.Stats.FirstTokenLatency)
	}
}
