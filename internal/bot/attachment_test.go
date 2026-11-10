package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot/internal/model"
)

type fakeAttachmentDownloader struct {
	data  []byte
	mime  string
	err   error
	calls int
}

func (f *fakeAttachmentDownloader) Download(ctx context.Context, attachment Attachment) (DownloadedAttachment, error) {
	f.calls++
	return DownloadedAttachment{Data: f.data, MIME: f.mime}, f.err
}

func TestPhotoAttachmentReachesModelWithoutStoredBinary(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "на фото тест"}}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, nil)
	d.deps.Attachments = &fakeAttachmentDownloader{data: []byte("jpeg-data"), mime: "image/jpeg"}
	ev := testEvent(160, 11, 1200, "Что на фото?")
	ev.Attachment = &Attachment{Kind: AttachmentPhoto, FileID: "photo-id"}

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	req := m.lastRequest()
	last := req.Messages[len(req.Messages)-1]
	if last.Content != "Что на фото?" || !strings.HasPrefix(last.ImageURL, "data:image/jpeg;base64,") {
		t.Fatalf("last model message = %+v, want caption and image data URI", last)
	}
	if !strings.Contains(r.joined(), "на фото тест") {
		t.Fatalf("answer = %q", r.joined())
	}
}

func TestTextDocumentReachesModel(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "документ прочитан"}}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, nil)
	d.deps.Attachments = &fakeAttachmentDownloader{data: []byte("первая строка\nвторая строка"), mime: "text/plain"}
	ev := testEvent(161, 11, 1201, "Сделай краткую выжимку")
	ev.Attachment = &Attachment{Kind: AttachmentDocument, FileID: "doc-id", FileName: "notes.txt", MimeType: "text/plain"}

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	last := m.lastRequest().Messages[len(m.lastRequest().Messages)-1]
	if !strings.Contains(last.Content, "Сделай краткую выжимку") || !strings.Contains(last.Content, "первая строка\nвторая строка") {
		t.Fatalf("document request = %+v", last)
	}
	if last.ImageURL != "" {
		t.Fatal("text document must not be encoded as an image")
	}
}

func TestAttachmentValidationAndFailuresDoNotCallModel(t *testing.T) {
	for _, tc := range []struct {
		name       string
		attachment Attachment
		downloaded DownloadedAttachment
		err        error
		want       string
	}{
		{name: "too large", attachment: Attachment{Kind: AttachmentPhoto}, err: ErrAttachmentTooLarge, want: "слишком большое"},
		{name: "unavailable", attachment: Attachment{Kind: AttachmentPhoto}, err: ErrAttachmentUnavailable, want: "повторно"},
		{name: "invalid utf8", attachment: Attachment{Kind: AttachmentDocument, FileName: "bad.txt", MimeType: "text/plain"}, downloaded: DownloadedAttachment{Data: []byte{0xff, 0xfe}, MIME: "text/plain"}, want: "UTF-8"},
		{name: "binary document", attachment: Attachment{Kind: AttachmentDocument, FileName: "bad.txt", MimeType: "text/plain"}, downloaded: DownloadedAttachment{Data: []byte("a\x00b"), MIME: "text/plain"}, want: "текстов"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
			m := &fakeModel{response: model.Response{Content: "не должно быть"}}
			r := &fakeReplier{}
			d := newTestDispatcher(t, cfg, m, r, nil)
			d.deps.Attachments = &fakeAttachmentDownloader{data: tc.downloaded.Data, mime: tc.downloaded.MIME, err: tc.err}
			ev := testEvent(162, 11, 1202, "проверь")
			ev.Attachment = &tc.attachment
			if err := dispatch(t, d, ev); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if m.callCount() != 0 {
				t.Fatalf("model calls = %d, want 0", m.callCount())
			}
			if !strings.Contains(r.joined(), tc.want) {
				t.Fatalf("reply = %q, want substring %q", r.joined(), tc.want)
			}
		})
	}
}

func TestUnsupportedMediaGetsReadableReply(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "не должно быть"}}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, nil)
	ev := testEvent(163, 11, 1203, "")
	ev.UnsupportedMedia = true
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.callCount() != 0 || !strings.Contains(r.joined(), "фото") || !strings.Contains(r.joined(), "текстовый документ") {
		t.Fatalf("calls=%d reply=%q", m.callCount(), r.joined())
	}
}

func TestStartupAsksToResendAttachmentInsteadOfCallingModel(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "не должно быть"}, delay: 2 * time.Second}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, nil)
	downloader := &fakeAttachmentDownloader{data: []byte("image"), mime: "image/jpeg"}
	d.deps.Attachments = downloader
	ev := testEvent(164, 11, 1204, "описание")
	ev.Attachment = &Attachment{Kind: AttachmentPhoto, FileID: "photo-id"}

	interruptDispatch(t, d, m, ev)

	callsBeforeRestart := m.callCount()
	restarted := newTestDispatcher(t, cfg, m, r, nil)
	answered, pending, err := restarted.ResumeInterruptedQuestions(context.Background())
	if err != nil {
		t.Fatalf("ResumeInterruptedQuestions: %v", err)
	}
	if answered != 0 || pending != 1 || m.callCount() != callsBeforeRestart {
		t.Fatalf("answered=%d pending=%d model calls=%d", answered, pending, m.callCount())
	}
	if !strings.Contains(r.joined(), "отправь вложение повторно") {
		t.Fatalf("reply = %q", r.joined())
	}
	answered, pending, err = restarted.ResumeInterruptedQuestions(context.Background())
	if err != nil || answered != 0 || pending != 0 {
		t.Fatalf("suppressed attachment replay: answered=%d pending=%d err=%v", answered, pending, err)
	}
}

// TestDocumentIsTextAcceptsATextualExtensionWithAVendorMIME — browsers and file managers report .csv as
// application/vnd.ms-excel and .md as application/x-markdown. The extension is explicit and the bytes already
// passed the UTF-8/control check, so an unknown vendor MIME must not reject a text document.
func TestDocumentIsTextAcceptsATextualExtensionWithAVendorMIME(t *testing.T) {
	for _, test := range []struct {
		name string
		mime string
		data string
		want bool
	}{
		{name: "report.csv", mime: "application/vnd.ms-excel", data: "name,value\none,1\n", want: true},
		{name: "notes.md", mime: "application/x-markdown", data: "# notes\nbody\n", want: true},
		{name: "notes.md", mime: "application/octet-stream", data: "# notes\nbody\n", want: true},
		{name: "data.json", mime: "application/vnd.vendor+json", data: `{"a":1}`, want: true},
		{name: "image.png", mime: "image/png", data: "\x89PNG\r\n\x1a\n\x00\x00", want: false},
		{name: "tool.exe", mime: "application/x-msdownload", data: "MZ\x00\x00binary", want: false},
	} {
		t.Run(test.name+"/"+test.mime, func(t *testing.T) {
			if got := documentIsText(test.name, test.mime, []byte(test.data)); got != test.want {
				t.Fatalf("documentIsText(%q, %q) = %v, want %v", test.name, test.mime, got, test.want)
			}
		})
	}
}
