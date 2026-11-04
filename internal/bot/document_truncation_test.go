package bot

import (
	"strings"
	"testing"

	"bot/internal/model"
)

func TestDocumentTruncationIsVisibleToUser(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "MAX_CONTEXT_CHARS": "256"})
	m := &fakeModel{response: model.Response{Content: "краткий ответ"}}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, nil)
	d.deps.Attachments = &fakeAttachmentDownloader{data: []byte(strings.Repeat("текст документа\n", 100)), mime: "text/plain"}
	ev := testEvent(195, 11, 1330, "Сделай выжимку")
	ev.Attachment = &Attachment{Kind: AttachmentDocument, FileID: "doc", FileName: "long.txt", MimeType: "text/plain"}
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !strings.Contains(r.joined(), "документ был обрезан по MAX_CONTEXT_CHARS") {
		t.Fatalf("reply=%q, want truncation notice", r.joined())
	}
	last := m.lastRequest().Messages[len(m.lastRequest().Messages)-1]
	if !strings.Contains(last.Content, "документ обрезан по MAX_CONTEXT_CHARS") || Runes(last.Content) > 256 {
		t.Fatalf("model document was not bounded: %q", last.Content)
	}
}
