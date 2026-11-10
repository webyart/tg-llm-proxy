package bot

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

// miniAppVoiceModel records the one TranscriptionRequest the dispatcher makes, so the test can prove the
// Mini App path forwards the uploaded bytes/filename/MIME to the SAME client the Telegram voice handler
// uses rather than building a second one.
type miniAppVoiceModel struct {
	fakeModel
	transcript string
	err        error
	calls      int
	request    model.TranscriptionRequest
}

func (m *miniAppVoiceModel) Transcribe(ctx context.Context, req model.TranscriptionRequest) (string, error) {
	m.calls++
	m.request = req
	if m.err != nil {
		return "", m.err
	}
	return m.transcript, nil
}

// miniAppWebM is the smallest payload the shared validation accepts: the EBML magic of a WebM container
// followed by padding. A real MediaRecorder clip has the same prefix.
func miniAppWebM(size int) []byte {
	data := []byte{0x1a, 0x45, 0xdf, 0xa3}
	for len(data) < size {
		data = append(data, 0x42)
	}
	return data
}

func newVoiceDispatcher(t *testing.T, cfgOverrides map[string]string, m model.Client) (*Dispatcher, *miniAppVoiceModel) {
	t.Helper()
	cfg := testConfig(t, merge(map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}, cfgOverrides))
	voice, ok := m.(*miniAppVoiceModel)
	if !ok {
		t.Fatal("the test model is not a *miniAppVoiceModel")
	}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, voice, nil, db)
	return d, voice
}

func TestMiniAppTranscribeUsesTheSharedClientAndStoresNothing(t *testing.T) {
	d, voice := newVoiceDispatcher(t, map[string]string{"STRATA_TRANSCRIPTION_MODEL": "voice-model"}, &miniAppVoiceModel{transcript: "  привет из записи  "})
	payload := miniAppWebM(512)

	text, err := d.TranscribeForMiniApp(context.Background(), 500, 11, payload, "voice.webm", "audio/webm;codecs=opus")
	if err != nil {
		t.Fatalf("TranscribeForMiniApp: %v", err)
	}
	if text != "привет из записи" {
		t.Fatalf("transcript = %q, want the trimmed text", text)
	}
	if voice.calls != 1 {
		t.Fatalf("transcribe calls = %d, want 1", voice.calls)
	}
	for name, got := range map[string]string{
		"model": voice.request.Model, "filename": voice.request.FileName, "mime": voice.request.MIME,
	} {
		want := map[string]string{"model": "voice-model", "filename": "voice.webm", "mime": "audio/webm"}[name]
		if got != want {
			t.Errorf("transcription request %s = %q, want %q", name, got, want)
		}
	}
	if !bytes.Equal(voice.request.Data, payload) {
		t.Fatalf("the transcriber received %d bytes, want the %d uploaded ones", len(voice.request.Data), len(payload))
	}

	// The audio is not a message and not a document: only the transcript goes back to the client.
	var messages, documents int
	if err := d.deps.DB.QueryRow("SELECT COUNT(*) FROM messages").Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := d.deps.DB.QueryRow("SELECT COUNT(*) FROM rag_documents").Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || documents != 0 {
		t.Fatalf("audio was stored: messages=%d documents=%d", messages, documents)
	}
	if requests, err := store.CountEvents(context.Background(), d.deps.DB, store.EventCommand); err != nil || requests != 1 {
		t.Fatalf("command audit rows = %d err=%v, want one miniapp_transcribe", requests, err)
	}
}

func TestMiniAppTranscribeRejectsEmptyTranscript(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transcript string
		err        error
	}{
		{name: "whitespace only", transcript: "   \n\t "},
		{name: "invalid UTF-8", transcript: string([]byte{0xff, 0xfe})},
		{name: "provider empty error", err: model.ErrEmptyTranscription},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, voice := newVoiceDispatcher(t, nil, &miniAppVoiceModel{transcript: tc.transcript, err: tc.err})
			_, err := d.TranscribeForMiniApp(context.Background(), 500, 11, miniAppWebM(512), "voice.webm", "audio/webm")
			if !errors.Is(err, ErrMiniAppEmptyTranscription) && !errors.Is(err, ErrMiniAppTranscriptionFailed) {
				t.Fatalf("error = %v, want an empty-transcript refusal", err)
			}
			if voice.calls != 1 {
				t.Fatalf("transcribe calls = %d, want 1", voice.calls)
			}
		})
	}
}

func TestMiniAppTranscribeRejectsOversizeAudio(t *testing.T) {
	d, voice := newVoiceDispatcher(t, map[string]string{"MEDIA_MAX_MB": "1"}, &miniAppVoiceModel{transcript: "x"})
	payload := miniAppWebM(1<<20 + 1)
	if _, err := d.TranscribeForMiniApp(context.Background(), 500, 11, payload, "voice.webm", "audio/webm"); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("oversize error = %v, want ErrAttachmentTooLarge", err)
	}
	if voice.calls != 0 {
		t.Fatalf("the transcriber was called for an oversize clip")
	}
}

func TestMiniAppTranscribeRejectsNonAudio(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		mime string
		data []byte
	}{
		{name: "text file", file: "note.txt", mime: "text/plain", data: []byte(strings.Repeat("hello", 200))},
		{name: "image", file: "photo.png", mime: "image/png", data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 600)...)},
		{name: "signature mismatch", file: "voice.webm", mime: "audio/webm", data: []byte(strings.Repeat("not-webm", 100))},
		{name: "unknown type", file: "clip.bin", mime: "application/octet-stream", data: []byte(strings.Repeat("x", 600))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, voice := newVoiceDispatcher(t, nil, &miniAppVoiceModel{transcript: "no"})
			if _, err := d.TranscribeForMiniApp(context.Background(), 500, 11, tc.data, tc.file, tc.mime); !errors.Is(err, ErrAttachmentType) {
				t.Fatalf("error = %v, want ErrAttachmentType", err)
			}
			if voice.calls != 0 {
				t.Fatalf("the transcriber was called for %s", tc.name)
			}
		})
	}
}

func TestMiniAppTranscribeRejectsShortAudio(t *testing.T) {
	d, voice := newVoiceDispatcher(t, nil, &miniAppVoiceModel{transcript: "no"})
	for _, data := range [][]byte{nil, {0x1a, 0x45, 0xdf, 0xa3}, make([]byte, MiniAppMinAudioBytes-1)} {
		if _, err := d.TranscribeForMiniApp(context.Background(), 500, 11, data, "voice.webm", "audio/webm"); !errors.Is(err, ErrMiniAppAudioTooShort) {
			t.Fatalf("len=%d error = %v, want ErrMiniAppAudioTooShort", len(data), err)
		}
	}
	if voice.calls != 0 {
		t.Fatalf("the transcriber was called for short audio")
	}
}

func TestMiniAppTranscribeEnforcesAllowlistAndRateLimit(t *testing.T) {
	d, voice := newVoiceDispatcher(t, map[string]string{"RATE_LIMIT_PER_MINUTE": "1"}, &miniAppVoiceModel{transcript: "раз"})
	if _, err := d.TranscribeForMiniApp(context.Background(), 500, 99, miniAppWebM(512), "voice.webm", "audio/webm"); !errors.Is(err, ErrMiniAppDenied) {
		t.Fatalf("denied user error = %v, want ErrMiniAppDenied", err)
	}
	if denied, err := store.CountEvents(context.Background(), d.deps.DB, store.EventDenied); err != nil || denied != 1 {
		t.Fatalf("denied audit rows = %d err=%v", denied, err)
	}

	if _, err := d.TranscribeForMiniApp(context.Background(), 500, 11, miniAppWebM(512), "voice.webm", "audio/webm"); err != nil {
		t.Fatalf("first allowed call: %v", err)
	}
	_, err := d.TranscribeForMiniApp(context.Background(), 500, 11, miniAppWebM(512), "voice.webm", "audio/webm")
	var limited *MiniAppRateLimitError
	if !errors.As(err, &limited) || limited.RetryAfter <= 0 {
		t.Fatalf("second call error = %v, want *MiniAppRateLimitError", err)
	}
	if voice.calls != 1 {
		t.Fatalf("transcribe calls = %d, want 1 (the limited call must not reach the client)", voice.calls)
	}
	if events, err := store.CountEvents(context.Background(), d.deps.DB, store.EventLimit); err != nil || events != 1 {
		t.Fatalf("limit audit rows = %d err=%v", events, err)
	}
}
