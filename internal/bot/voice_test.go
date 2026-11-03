package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

type fakeVoiceModel struct {
	fakeModel
	transcript      string
	transcribeErr   error
	transcribeCalls int
}

func (m *fakeVoiceModel) Transcribe(ctx context.Context, req model.TranscriptionRequest) (string, error) {
	m.transcribeCalls++
	if m.transcribeErr != nil {
		return "", m.transcribeErr
	}
	return m.transcript, nil
}

func TestVoiceIsTranscribedBeforeModelAndOnlyTextIsStored(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "STRATA_TRANSCRIPTION_MODEL": "voice-model"})
	m := &fakeVoiceModel{fakeModel: fakeModel{response: model.Response{Content: "ответ на голосовое"}}, transcript: "привет голосом"}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	d.deps.Attachments = &fakeAttachmentDownloader{data: []byte("OggS-audio"), mime: "audio/ogg"}
	ev := testEvent(180, 11, 1400, "")
	ev.Attachment = &Attachment{Kind: AttachmentVoice, FileID: "voice-id", Duration: 3}

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.transcribeCalls != 1 || m.callCount() != 1 {
		t.Fatalf("transcribe=%d complete=%d", m.transcribeCalls, m.callCount())
	}
	last := m.lastRequest().Messages[len(m.lastRequest().Messages)-1]
	if !strings.Contains(last.Content, "Транскрипция") || !strings.Contains(last.Content, "привет голосом") || last.ImageURL != "" {
		t.Fatalf("voice model message=%+v", last)
	}
	rows, err := store.RecentByChat(context.Background(), db, 180, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if !strings.Contains(rows[0].Content, "привет голосом") || strings.Contains(rows[0].Content, "OggS") || strings.Contains(rows[0].Content, "ожидает транскрипции") {
		t.Fatalf("stored voice row=%+v", rows[0])
	}
}

func TestVoiceFailureDoesNotCallChatModel(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeVoiceModel{fakeModel: fakeModel{response: model.Response{Content: "не должно быть"}}, transcribeErr: errors.New("bad audio")}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, nil)
	d.deps.Attachments = &fakeAttachmentDownloader{data: []byte("OggS-audio"), mime: "audio/ogg"}
	ev := testEvent(181, 11, 1401, "")
	ev.Attachment = &Attachment{Kind: AttachmentVoice, FileID: "voice-id"}
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.callCount() != 0 || !strings.Contains(r.joined(), "Не удалось распознать") {
		t.Fatalf("complete=%d reply=%q", m.callCount(), r.joined())
	}
}

func TestPendingVoiceRecoveryRequiresFile(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "не должно быть"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	tgID := int64(1402)
	if _, err := store.Append(context.Background(), db, store.Message{
		ChatID: 182, UserID: 11, Role: store.RoleUser, Content: "[Голосовое: ожидает транскрипции]\nОтветь на голосовое сообщение.", TelegramMessageID: &tgID,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	answered, pending, err := d.ResumeInterruptedQuestions(context.Background())
	if err != nil || answered != 0 || pending != 1 || m.callCount() != 0 || !strings.Contains(r.joined(), "отправь вложение повторно") {
		t.Fatalf("answered=%d pending=%d calls=%d reply=%q err=%v", answered, pending, m.callCount(), r.joined(), err)
	}
}
