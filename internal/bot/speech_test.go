package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

type genericSpeechModel struct {
	fakeModel
	transcript      string
	transcribeCalls int
	request         model.TranscriptionRequest
}

func (m *genericSpeechModel) Transcribe(ctx context.Context, req model.TranscriptionRequest) (string, error) {
	m.transcribeCalls++
	m.request = req
	return m.transcript, nil
}

func TestAudioAndVideoNoteUseGenericSpeechTranscription(t *testing.T) {
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	for i, tc := range []struct {
		name       string
		attachment Attachment
		data       []byte
	}{
		{
			name:       "audio mp3",
			attachment: Attachment{Kind: AttachmentAudio, FileID: "audio-id", FileName: "speech.mp3", MimeType: "audio/mpeg"},
			data:       []byte("ID3-audio-payload"),
		},
		{
			name:       "video note mp4",
			attachment: Attachment{Kind: AttachmentVideoNote, FileID: "video-note-id", FileName: "video-note.mp4", MimeType: "video/mp4"},
			data:       mp4,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
			m := &genericSpeechModel{fakeModel: fakeModel{response: model.Response{Content: "ответ по транскрипции"}}, transcript: "распознанная речь"}
			db := testStore(t)
			d := newTestDispatcher(t, cfg, m, &fakeReplier{}, db)
			d.deps.Attachments = &fakeAttachmentDownloader{data: tc.data, mime: tc.attachment.MimeType}
			ev := testEvent(int64(190+i), 11, int64(1320+i), "Ответь спикеру")
			ev.Attachment = &tc.attachment

			if err := dispatch(t, d, ev); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if m.transcribeCalls != 1 || m.callCount() != 1 {
				t.Fatalf("transcribe=%d complete=%d", m.transcribeCalls, m.callCount())
			}
			if m.request.FileName != tc.attachment.FileName || m.request.MIME != tc.attachment.MimeType || string(m.request.Data) != string(tc.data) {
				t.Fatalf("transcription request = %+v", m.request)
			}
			last := m.lastRequest().Messages[len(m.lastRequest().Messages)-1]
			if !strings.Contains(last.Content, "распознанная речь") || last.ImageURL != "" || len(last.ImageURLs) != 0 {
				t.Fatalf("speech model message = %+v", last)
			}
			rows, err := store.RecentByChat(context.Background(), db, ev.ChatID, 10)
			if err != nil || len(rows) != 2 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			if !strings.Contains(rows[0].Content, "распознанная речь") || strings.Contains(rows[0].Content, "ожидает транскрипции") || strings.Contains(rows[0].Content, string(tc.data)) {
				t.Fatalf("stored speech row = %+v", rows[0])
			}
		})
	}
}

func TestSpeechRejectsSignatureThatDoesNotMatchFormat(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &genericSpeechModel{fakeModel: fakeModel{response: model.Response{Content: "не должно быть"}}, transcript: "не должно быть"}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, nil)
	d.deps.Attachments = &fakeAttachmentDownloader{data: []byte("OggS-not-mp3"), mime: "audio/ogg"}
	ev := testEvent(193, 11, 1325, "Проверь")
	ev.Attachment = &Attachment{Kind: AttachmentAudio, FileID: "audio-id", FileName: "speech.mp3", MimeType: "audio/mpeg"}

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.transcribeCalls != 0 || m.callCount() != 0 {
		t.Fatalf("transcribe=%d complete=%d", m.transcribeCalls, m.callCount())
	}
}
