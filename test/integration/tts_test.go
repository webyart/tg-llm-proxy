//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

type capturedAudio struct {
	chatID   int64
	filename string
	data     []byte
}

type audioTelegramProxy struct {
	mu    sync.Mutex
	audio []capturedAudio
}

func (p *audioTelegramProxy) add(chatID int64, filename string, data []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.audio = append(p.audio, capturedAudio{chatID: chatID, filename: filename, data: append([]byte(nil), data...)})
}

func (p *audioTelegramProxy) all() []capturedAudio {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]capturedAudio(nil), p.audio...)
}

func TestTTSOverFakeTelegramAndSpeaches(t *testing.T) {
	const input = "Приватный текст для озвучивания"
	var speechCalls atomic.Int64
	var speechRequest struct {
		Model          string  `json:"model"`
		Input          string  `json:"input"`
		Voice          string  `json:"voice"`
		ResponseFormat string  `json:"response_format"`
		Speed          float64 `json:"speed"`
		SampleRate     int     `json:"sample_rate"`
	}
	speech := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		speechCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/speech" {
			t.Errorf("speech request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&speechRequest); err != nil {
			t.Errorf("speech JSON: %v", err)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3-integration-audio"))
	}))
	defer speech.Close()

	tgAPI := testutil.NewTelegram(t, secretToken)
	target, err := url.Parse(tgAPI.URL())
	if err != nil {
		t.Fatal(err)
	}
	capture := &audioTelegramProxy{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendAudio") {
			httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, r)
			return
		}
		if err := r.ParseMultipartForm(21 << 20); err != nil {
			t.Errorf("sendAudio multipart: %v", err)
		}
		chatID, err := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
		if err != nil {
			t.Errorf("sendAudio chat_id: %v", err)
		}
		file, header, err := r.FormFile("audio")
		if err != nil {
			t.Errorf("sendAudio file: %v", err)
			http.Error(w, "audio required", http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(io.LimitReader(file, 20<<20+1))
		_ = file.Close()
		if err != nil {
			t.Errorf("sendAudio data: %v", err)
		}
		capture.add(chatID, header.Filename, data)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{
			"message_id": 1, "date": time.Now().Unix(), "chat": map[string]any{"id": chatID, "type": "private"},
		}})
	}))
	defer proxy.Close()

	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "must not be called"})
	path := dbFile(t)
	cfg := integrationEnv(t, path, strata.BaseURL(), map[string]string{
		"TRANSCRIPTION_BASE_URL": speech.URL,
		"TTS_ENABLED":            "true",
		"TTS_TIMEOUT_SECONDS":    "2",
	})
	db := openStore(t, path)
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: chatID, UserID: userID, Role: store.RoleAssistant, Content: input}); err != nil {
		t.Fatal(err)
	}
	run := startBot(t, context.Background(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: proxy.URL, PollTimeout: 2 * time.Second}, 0)
	tgAPI.PushText(501, chatID, userID, "/tts speed 1.25")
	waitFor(t, "TTS speed confirmation", func() bool { return tgAPI.Calls("sendMessage") == 1 })
	tgAPI.PushText(502, chatID, userID, "/tts last")
	waitFor(t, "TTS sendAudio", func() bool { return len(capture.all()) == 1 })

	audio := capture.all()[0]
	if audio.chatID != chatID || audio.filename != "speech.mp3" || string(audio.data) != "ID3-integration-audio" {
		t.Fatalf("audio = %+v", audio)
	}
	if speechRequest.Model != "speaches-ai/piper-ru_RU-ruslan-medium" || speechRequest.Input != input || speechRequest.Voice != "ruslan" || speechRequest.ResponseFormat != "mp3" || speechRequest.Speed != 1.25 || speechRequest.SampleRate != 22050 {
		t.Fatalf("speech request = %+v", speechRequest)
	}
	if speechCalls.Load() != 1 || strata.ChatCalls() != 0 || tgAPI.Calls("sendDocument") != 0 || tgAPI.Calls("sendMessage") != 1 {
		t.Fatalf("speech = %d, chat = %d, documents = %d, messages = %d", speechCalls.Load(), strata.ChatCalls(), tgAPI.Calls("sendDocument"), tgAPI.Calls("sendMessage"))
	}
	for _, output := range []string{tgAPI.Joined(), run.logs.String()} {
		if strings.Contains(output, input) || strings.Contains(output, speech.URL) {
			t.Fatalf("TTS text or URL leaked: %q", output)
		}
	}
	run.stop()
}
