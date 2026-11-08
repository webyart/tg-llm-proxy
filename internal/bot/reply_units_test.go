package bot

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
)

// TestChunkMeasuresUTF16Units is the regression test for the 4096-unit limit: 2000 emoji are 2000 runes
// but 4000 UTF-16 units, so a rune-sized chunk of 3500 fit "fine" in Go and was rejected by Telegram.
func TestChunkMeasuresUTF16Units(t *testing.T) {
	var text = strings.Repeat("😀", 2000)
	var chunks = Chunk(text, 3500)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want at least 2: 4000 UTF-16 units do not fit one 3500-unit message", len(chunks))
	}
	for index, chunk := range chunks {
		if units := MessageUnits(chunk); units > 3500 {
			t.Fatalf("chunk %d carries %d UTF-16 units, budget is 3500", index, units)
		}
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is not valid UTF-8: the cut split a surrogate pair", index)
		}
	}
	if joined := strings.Join(chunks, ""); joined != text {
		t.Fatalf("chunks lost text: %d units in, %d units out", MessageUnits(text), MessageUnits(joined))
	}
}

// TestChunkOfBMPTextIsUnchanged keeps the common case stable: for text inside the BMP a rune IS a unit, so
// the split of a Cyrillic answer must be exactly what it was before the change.
func TestChunkOfBMPTextIsUnchanged(t *testing.T) {
	var text = strings.Repeat("привет", 1000) // 6000 runes, all one unit each
	var chunks = Chunk(text, 3500)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	for index, chunk := range chunks {
		if units := MessageUnits(chunk); units > 3500 {
			t.Fatalf("chunk %d carries %d units", index, units)
		}
	}
	if joined := strings.Join(chunks, ""); joined != text {
		t.Fatal("chunks lost text")
	}
}

// TestChunkNeverEmitsMoreThanTheBudgetInMixedText covers the awkward case: one long unbroken word made of
// astral characters cannot be cut on whitespace, so the hard cut has to be unit-accurate.
func TestChunkNeverEmitsMoreThanTheBudgetInMixedText(t *testing.T) {
	var text = strings.Repeat("𝕬", 100) + strings.Repeat("x", 10) + strings.Repeat("😀", 300)
	var chunks = Chunk(text, 64)
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	for index, chunk := range chunks {
		if units := MessageUnits(chunk); units > 64 {
			t.Fatalf("chunk %d carries %d units, budget is 64", index, units)
		}
	}
}

// transportReplier — a telegramReplier over a bot whose every call is answered with one fixed Telegram
// error, so the 400 classification can be tested without a network.
func transportReplier(t *testing.T, status int, description string) *telegramReplier {
	t.Helper()
	var client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body = fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q}`, status, description)
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	var bot, err = tgbot.New("12345:test-token-not-a-secret",
		tgbot.WithHTTPClient(time.Second, client),
		tgbot.WithSkipGetMe(),
		tgbot.WithServerURL("http://telegram.test"))
	if err != nil {
		t.Fatalf("tgbot.New: %v", err)
	}
	return &telegramReplier{
		bot:    bot,
		cfg:    testConfig(t, nil),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		users:  map[int64]int64{},
	}
}

// TestEditStreamMessageTreatsNotModifiedAsSuccess: Telegram answers this 400 when an edit repeats the
// text already in the message. It used to count as a failed delivery, and three of them muted the stream.
func TestEditStreamMessageTreatsNotModifiedAsSuccess(t *testing.T) {
	var replier = transportReplier(t, http.StatusBadRequest, "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message")
	if err := replier.EditStreamMessage(context.Background(), 42, 7, "тот же текст", false); err != nil {
		t.Fatalf("EditStreamMessage on «message is not modified» = %v, want nil", err)
	}
}

// TestEditStreamMessageKeepsRealErrors keeps the classification honest: a genuinely missing message is
// still a failure the stream has to know about.
func TestEditStreamMessageKeepsRealErrors(t *testing.T) {
	var replier = transportReplier(t, http.StatusBadRequest, "Bad Request: message to edit not found")
	if err := replier.EditStreamMessage(context.Background(), 42, 7, "текст", false); err == nil {
		t.Fatal("EditStreamMessage lost a real delivery error")
	}
}
