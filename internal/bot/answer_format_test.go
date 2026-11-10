package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot/internal/model"
)

// TestFinishReasonNoticeAndLatencyAppearInBothClients is the parity test of the shared finalizer:
// the same model Response must produce the same truncation warning and latency line whether the
// answer is delivered by Telegram or by the Mini App.
func TestFinishReasonNoticeAndLatencyAppearInBothClients(t *testing.T) {
	const answer = "обрезанный ответ"
	response := model.Response{
		Content:      answer,
		Model:        "fake-model",
		FinishReason: "length",
		Latency:      12 * time.Millisecond,
		Attempts:     1,
	}
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11",
		"STREAMING_ENABLED":      "false",
	})

	replier := &fakeReplier{}
	telegram := newTestDispatcher(t, cfg, &fakeModel{response: response}, replier,
		testStoreNamed(t, t.Name()+"-telegram"))
	if err := dispatch(t, telegram, testEvent(100, 11, 1, "/ask вопрос")); err != nil {
		t.Fatalf("Telegram dispatch: %v", err)
	}

	log := &miniAppEventLog{}
	miniApp := newTestDispatcher(t, cfg, &fakeModel{response: response}, &fakeReplier{},
		testStoreNamed(t, t.Name()+"-miniapp"))
	if err := miniApp.AnswerForMiniApp(context.Background(), 11, 11, "вопрос", log.emit); err != nil {
		t.Fatalf("AnswerForMiniApp: %v", err)
	}
	done, ok := log.done()
	if !ok {
		t.Fatal("the Mini App answer ended without a done event")
	}

	notice := finishReasonNotice("length")
	if notice == "" {
		t.Fatal("finishReasonNotice(length) is empty")
	}
	for name, text := range map[string]string{
		"Telegram": replier.joined(),
		"Mini App": done.Delta,
	} {
		if !strings.Contains(text, notice) {
			t.Errorf("%s output misses the finish_reason notice: %q", name, text)
		}
		if got := strings.Count(text, notice); got != 1 {
			t.Errorf("%s output carries the finish_reason notice %d times: %q", name, got, text)
		}
		if !strings.Contains(text, "⏱") {
			t.Errorf("%s output misses the latency line: %q", name, text)
		}
		if !strings.Contains(text, answer) {
			t.Errorf("%s output misses the answer: %q", name, text)
		}
	}
}

// TestFormatModelAnswerDoesNotDuplicateFinishReasonNotice — a streamed continuation (or any
// content that already ends with the warning) must keep one copy instead of gaining a second one.
func TestFormatModelAnswerDoesNotDuplicateFinishReasonNotice(t *testing.T) {
	notice := finishReasonNotice("length")
	content := "ответ\n\n" + notice
	got := formatModelAnswer(content, model.Response{
		Content:      content,
		Model:        "fake-model",
		FinishReason: "length",
		Latency:      time.Millisecond,
		Attempts:     1,
	}, "fake-model", answerFormatOptions{Latency: true})
	if count := strings.Count(got, notice); count != 1 {
		t.Fatalf("notice appears %d times, want 1: %q", count, got)
	}
	if !strings.HasPrefix(got, content) {
		t.Fatalf("the existing notice was moved: %q", got)
	}
}

// TestFormatModelAnswerKeepsTelegramAssembly — the shared function must reproduce the Telegram
// finalizer byte for byte for an ordinary answer without notices or sources.
func TestFormatModelAnswerKeepsTelegramAssembly(t *testing.T) {
	response := model.Response{
		Content:  "ответ",
		Model:    "fake-model",
		Latency:  1500 * time.Microsecond,
		Attempts: 1,
	}
	want := response.Content + "\n\n" + latencyLine(response, "fake-model")
	if got := formatModelAnswer(response.Content, response, "fake-model", answerFormatOptions{Latency: true}); got != want {
		t.Fatalf("formatted answer = %q, want %q", got, want)
	}
}

// TestFormatModelAnswerPrefixesDocumentTruncationOnce — the document truncation notice is also
// shared, so the Mini App can show it if it ever gains document context, without duplicating a
// marker the model already repeated.
func TestFormatModelAnswerPrefixesDocumentTruncationOnce(t *testing.T) {
	content := documentTruncationNotice + "\n\nответ"
	got := formatModelAnswer(content, model.Response{Content: content, Latency: time.Millisecond, Attempts: 1},
		"fake-model", answerFormatOptions{Latency: true, DocumentTruncated: true})
	if count := strings.Count(got, documentTruncationNotice); count != 1 {
		t.Fatalf("document truncation notice appears %d times, want 1: %q", count, got)
	}
}
