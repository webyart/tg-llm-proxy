package bot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"

	"bot/internal/model"
	"bot/internal/store"
)

type fakeSynthesizer struct {
	mu     sync.Mutex
	calls  int
	inputs []string
	speeds []float64
	data   []byte
	err    error
}

func (f *fakeSynthesizer) Synthesize(ctx context.Context, text string, speed float64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.inputs = append(f.inputs, text)
	f.speeds = append(f.speeds, speed)
	return append([]byte(nil), f.data...), f.err
}

func (f *fakeSynthesizer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestTTSCommandSendsAudioWithoutModelOrDocument(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TTS_ENABLED": "true"})
	r := &fakeReplier{}
	m := &fakeModel{response: model.Response{Content: "must not be used"}}
	d := newTestDispatcher(t, cfg, m, r, nil)
	synth := &fakeSynthesizer{data: []byte("ID3-audio")}
	d.deps.TTS = synth
	if err := dispatch(t, d, testEvent(100, 11, 600, "/tts Привет")); err != nil {
		t.Fatal(err)
	}
	if synth.callCount() != 1 || len(synth.inputs) != 1 || synth.inputs[0] != "Привет" || len(synth.speeds) != 1 || synth.speeds[0] != 1 {
		t.Fatalf("synthesizer calls = %d inputs = %q speeds = %v", synth.callCount(), synth.inputs, synth.speeds)
	}
	if m.callCount() != 0 || len(r.audio) != 1 || len(r.documents) != 0 {
		t.Fatalf("model calls = %d, audio = %d, documents = %d", m.callCount(), len(r.audio), len(r.documents))
	}
	if r.audio[0].Name != "speech.mp3" || r.audio[0].MIME != "audio/mpeg" || string(r.audio[0].Data) != "ID3-audio" {
		t.Fatalf("audio = %+v", r.audio[0])
	}
}

func TestTTSDisabledAndOversizeDoNoWork(t *testing.T) {
	for _, test := range []struct {
		name     string
		enabled  bool
		text     string
		maxChars string
		want     string
	}{
		{name: "disabled", text: "hello", want: "TTS_ENABLED=true"},
		{name: "too long", enabled: true, text: "four", maxChars: "3", want: "максимум 3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			overrides := map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}
			if test.enabled {
				overrides["TTS_ENABLED"] = "true"
			}
			if test.maxChars != "" {
				overrides["TTS_MAX_CHARS"] = test.maxChars
			}
			r := &fakeReplier{}
			m := &fakeModel{}
			d := newTestDispatcher(t, testConfig(t, overrides), m, r, nil)
			synth := &fakeSynthesizer{data: []byte("audio")}
			d.deps.TTS = synth
			if err := dispatch(t, d, testEvent(100, 11, 601, "/tts "+test.text)); err != nil {
				t.Fatal(err)
			}
			if synth.callCount() != 0 || m.callCount() != 0 || len(r.audio) != 0 || !strings.Contains(r.joined(), test.want) {
				t.Fatalf("synth = %d, model = %d, audio = %d, reply = %q", synth.callCount(), m.callCount(), len(r.audio), r.joined())
			}
		})
	}
}

type sendOnlyReplier struct {
	texts []string
}

func (r *sendOnlyReplier) Send(ctx context.Context, chatID int64, text string) error {
	r.texts = append(r.texts, text)
	return nil
}

func TestTTSBasicReplierRemainsSourceCompatible(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TTS_ENABLED": "true"})
	r := &sendOnlyReplier{}
	d := newTestDispatcher(t, cfg, nil, r, nil)
	synth := &fakeSynthesizer{data: []byte("audio")}
	d.deps.TTS = synth
	if err := dispatch(t, d, testEvent(100, 11, 602, "/tts hello")); err != nil {
		t.Fatal(err)
	}
	if synth.callCount() != 0 || !strings.Contains(strings.Join(r.texts, "\n"), "не поддерживается") {
		t.Fatalf("synth = %d, replies = %q", synth.callCount(), r.texts)
	}
}

func TestTTSFailureIsBoundedAndRedacted(t *testing.T) {
	const privateInput = "private text that must not escape"
	const privateError = "http://private-upstream/v1/audio/speech private response body"
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TTS_ENABLED": "true"})
	r := &fakeReplier{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	d := newTestDispatcher(t, cfg, nil, r, nil, logger)
	d.deps.TTS = &fakeSynthesizer{err: errors.New(privateError)}
	if err := dispatch(t, d, testEvent(100, 11, 603, "/tts "+privateInput)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.joined(), "Не удалось озвучить текст") || len(r.joined()) > 200 {
		t.Fatalf("reply = %q", r.joined())
	}
	for _, output := range []string{r.joined(), logs.String()} {
		if strings.Contains(output, privateInput) || strings.Contains(output, privateError) || strings.Contains(output, "private-upstream") {
			t.Fatalf("output leaked TTS details: %q", output)
		}
	}
}

func TestTTSPendingInputCompletionCancelAndRestart(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TTS_ENABLED": "true", "PROXY_FREE_TEXT": "false"})
	r := &menuEditReplier{}
	m := &fakeModel{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	synth := &fakeSynthesizer{data: []byte("audio")}
	d.deps.TTS = synth
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:more"); err != nil {
		t.Fatal(err)
	}
	if !hasMenuCallback(r.lastEdit().markup, "menu:tts") {
		t.Fatalf("enabled More menu = %+v", r.lastEdit())
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:tts"); err != nil {
		t.Fatal(err)
	}
	pending, consumed := d.pendingEvent(testEvent(100, 11, 701, "текст из меню"))
	if !consumed || pending.Text != "/tts текст из меню" {
		t.Fatalf("pending = %+v, consumed = %v", pending, consumed)
	}
	if err := dispatch(t, d, pending); err != nil {
		t.Fatal(err)
	}
	if len(r.audio) != 1 || m.callCount() != 0 {
		t.Fatalf("audio = %d, model = %d", len(r.audio), m.callCount())
	}
	if _, consumed := d.pendingEvent(testEvent(100, 11, 702, "не TTS")); consumed {
		t.Fatal("completed TTS input remained pending")
	}

	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:tts"); err != nil {
		t.Fatal(err)
	}
	cancel := findMenuCallback(r.lastEdit().markup, "menu:input:cancel:")
	if cancel == "" {
		t.Fatal("TTS prompt has no cancellation")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, cancel); err != nil {
		t.Fatal(err)
	}
	if _, consumed := d.pendingEvent(testEvent(100, 11, 703, "не TTS")); consumed {
		t.Fatal("cancelled TTS input remained pending")
	}

	if err := dispatchMenuData(t, d, 100, 11, 700, "menu:tts"); err != nil {
		t.Fatal(err)
	}
	restarted := newTestDispatcher(t, cfg, m, r, db)
	if _, consumed := restarted.pendingEvent(testEvent(100, 11, 704, "не TTS")); consumed {
		t.Fatal("pending TTS input survived restart")
	}
}

func TestTelegramReplierSendAudioUsesSendAudio(t *testing.T) {
	var method string
	var filename string
	var data []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = strings.TrimPrefix(r.URL.Path, "/bot123:test/")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		file, header, err := r.FormFile("audio")
		if err != nil {
			t.Errorf("audio part: %v", err)
		} else {
			filename = header.Filename
			data, _ = io.ReadAll(file)
			_ = file.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":1,"chat":{"id":100,"type":"private"}}}`))
	}))
	defer server.Close()
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe(), tgbot.WithHTTPClient(time.Second, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	r := &telegramReplier{bot: tg}
	if err := r.SendAudio(context.Background(), 100, Audio{Name: "speech.mp3", MIME: "audio/mpeg", Data: []byte("ID3-audio")}); err != nil {
		t.Fatal(err)
	}
	if method != "sendAudio" || filename != "speech.mp3" || string(data) != "ID3-audio" {
		t.Fatalf("method = %q, filename = %q, data = %q", method, filename, data)
	}
}

func TestTTSLastUsesLatestAssistantAnswerWithoutModel(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TTS_ENABLED": "true"})
	db := testStore(t)
	for _, message := range []store.Message{
		{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "old question"},
		{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "old answer"},
		{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "new question"},
		{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "  newest answer  "},
		{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "later follow-up"},
		{ChatID: 200, UserID: 11, Role: store.RoleAssistant, Content: "other chat answer"},
	} {
		if _, err := store.Append(context.Background(), db, message); err != nil {
			t.Fatal(err)
		}
	}
	r := &fakeReplier{}
	m := &fakeModel{response: model.Response{Content: "must not be used"}}
	d := newTestDispatcher(t, cfg, m, r, db)
	synth := &fakeSynthesizer{data: []byte("ID3-last")}
	d.deps.TTS = synth
	if err := dispatch(t, d, testEvent(100, 11, 800, "/tts last")); err != nil {
		t.Fatal(err)
	}
	if synth.callCount() != 1 || len(synth.inputs) != 1 || synth.inputs[0] != "newest answer" {
		t.Fatalf("synthesizer calls = %d inputs = %q", synth.callCount(), synth.inputs)
	}
	if len(synth.speeds) != 1 || synth.speeds[0] != 1 || len(r.audio) != 1 || string(r.audio[0].Data) != "ID3-last" || m.callCount() != 0 {
		t.Fatalf("speeds = %v, audio = %+v, model calls = %d", synth.speeds, r.audio, m.callCount())
	}
}

func TestTTSLastWithoutAssistantDoesNoWork(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TTS_ENABLED": "true"})
	db := testStore(t)
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleUser, Content: "question"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: 200, UserID: 11, Role: store.RoleAssistant, Content: "other chat answer"}); err != nil {
		t.Fatal(err)
	}
	r := &fakeReplier{}
	m := &fakeModel{}
	d := newTestDispatcher(t, cfg, m, r, db)
	synth := &fakeSynthesizer{data: []byte("audio")}
	d.deps.TTS = synth
	if err := dispatch(t, d, testEvent(100, 11, 801, "/tts last")); err != nil {
		t.Fatal(err)
	}
	if synth.callCount() != 0 || m.callCount() != 0 || len(r.audio) != 0 || !strings.Contains(r.joined(), "нет ответа ассистента") {
		t.Fatalf("synth = %d, model = %d, audio = %d, reply = %q", synth.callCount(), m.callCount(), len(r.audio), r.joined())
	}
}

func TestTTSLastHonorsCharacterLimit(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TTS_ENABLED": "true", "TTS_MAX_CHARS": "3"})
	db := testStore(t)
	if _, err := store.Append(context.Background(), db, store.Message{ChatID: 100, UserID: 11, Role: store.RoleAssistant, Content: "four"}); err != nil {
		t.Fatal(err)
	}
	r := &fakeReplier{}
	m := &fakeModel{}
	d := newTestDispatcher(t, cfg, m, r, db)
	synth := &fakeSynthesizer{data: []byte("audio")}
	d.deps.TTS = synth
	if err := dispatch(t, d, testEvent(100, 11, 802, "/tts last")); err != nil {
		t.Fatal(err)
	}
	if synth.callCount() != 0 || m.callCount() != 0 || len(r.audio) != 0 || !strings.Contains(r.joined(), "максимум 3") {
		t.Fatalf("synth = %d, model = %d, audio = %d, reply = %q", synth.callCount(), m.callCount(), len(r.audio), r.joined())
	}
}

func TestTTSSpeedValidationPersistenceAndChatIsolation(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TTS_ENABLED": "true"})
	db := testStore(t)
	r := &fakeReplier{}
	m := &fakeModel{}
	d := newTestDispatcher(t, cfg, m, r, db)
	synth := &fakeSynthesizer{data: []byte("audio")}
	d.deps.TTS = synth
	if err := dispatch(t, d, testEvent(100, 11, 810, "/tts speed")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.texts()[len(r.texts())-1], "1×") {
		t.Fatalf("default speed reply = %q", r.joined())
	}
	for i, invalid := range []string{"0.5", "1.0", "2", "fast", "1.25 extra"} {
		if err := dispatch(t, d, testEvent(100, 11, int64(811+i), "/tts speed "+invalid)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.texts()[len(r.texts())-1], "0.75|1|1.25|1.5") {
			t.Fatalf("invalid speed %q reply = %q", invalid, r.texts()[len(r.texts())-1])
		}
	}
	for i, valid := range []string{"0.75", "1", "1.25", "1.5"} {
		if err := dispatch(t, d, testEvent(100, 11, int64(820+i), "/tts speed "+valid)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.texts()[len(r.texts())-1], valid+"×") {
			t.Fatalf("valid speed %q reply = %q", valid, r.texts()[len(r.texts())-1])
		}
	}
	if err := dispatch(t, d, testEvent(100, 11, 830, "/tts speed 1.25")); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, d, testEvent(200, 11, 831, "/tts speed")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.texts()[len(r.texts())-1], "1×") {
		t.Fatalf("other chat inherited speed: %q", r.texts()[len(r.texts())-1])
	}
	if err := dispatch(t, d, testEvent(200, 11, 832, "/tts speed 0.75")); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, d, testEvent(100, 11, 833, "/tts first")); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, d, testEvent(200, 11, 834, "/tts second")); err != nil {
		t.Fatal(err)
	}
	if len(synth.speeds) != 2 || synth.speeds[0] != 1.25 || synth.speeds[1] != 0.75 || m.callCount() != 0 {
		t.Fatalf("speeds = %v, model calls = %d", synth.speeds, m.callCount())
	}

	restarted := newTestDispatcher(t, cfg, m, r, db)
	restarted.deps.TTS = synth
	if err := dispatch(t, restarted, testEvent(100, 11, 835, "/tts speed")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.texts()[len(r.texts())-1], "1.25×") {
		t.Fatalf("speed did not survive restart: %q", r.texts()[len(r.texts())-1])
	}
}

func TestTTSSpeedMenuShowsAndSetsCurrentSpeed(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "TTS_ENABLED": "true"})
	r := &menuEditReplier{}
	d := newTestDispatcher(t, cfg, nil, r, nil)
	if err := dispatchMenuData(t, d, 100, 11, 850, "menu:more"); err != nil {
		t.Fatal(err)
	}
	if !hasMenuCallback(r.lastEdit().markup, "menu:tts:last") || !hasMenuCallback(r.lastEdit().markup, "menu:tts:speed") {
		t.Fatalf("More menu has no TTS shortcuts: %+v", r.lastEdit())
	}
	if err := dispatchMenuData(t, d, 100, 11, 850, "menu:tts:speed"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "1×") || !hasMenuCallback(r.lastEdit().markup, "menu:tts:speed:0.75") || !hasMenuCallback(r.lastEdit().markup, "menu:tts:speed:1.5") {
		t.Fatalf("speed menu = %+v", r.lastEdit())
	}
	if err := dispatchMenuData(t, d, 100, 11, 850, "menu:tts:speed:1.5"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastEdit().text, "1.5×") {
		t.Fatalf("updated speed menu = %+v", r.lastEdit())
	}
	speed, err := d.ttsSpeed(context.Background(), 100)
	if err != nil || speed != 1.5 {
		t.Fatalf("speed = %v, err = %v", speed, err)
	}
}
