package bot

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

type albumAttachmentDownloader struct {
	mu    sync.Mutex
	files map[string]DownloadedAttachment
	calls []string
}

func (f *albumAttachmentDownloader) Download(ctx context.Context, attachment Attachment) (DownloadedAttachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, attachment.FileID)
	file, ok := f.files[attachment.FileID]
	if !ok {
		return DownloadedAttachment{}, fmt.Errorf("unknown file %q", attachment.FileID)
	}
	return file, nil
}

func (f *albumAttachmentDownloader) callIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestMultiPhotoAttachmentsReachOneModelMessage(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "описание альбома"}}
	d := newTestDispatcher(t, cfg, m, &fakeReplier{}, nil)
	downloader := &albumAttachmentDownloader{files: map[string]DownloadedAttachment{
		"first":  {Data: []byte("first-jpeg"), MIME: "image/jpeg"},
		"second": {Data: []byte("second-png"), MIME: "image/png"},
		"third":  {Data: []byte("third-jpeg"), MIME: "image/jpeg"},
	}}
	d.deps.Attachments = downloader
	ev := testEvent(170, 11, 1300, "Сравни фотографии")
	ev.Attachment = &Attachment{Kind: AttachmentPhoto, FileID: "first"}
	ev.AdditionalAttachments = []Attachment{
		{Kind: AttachmentPhoto, FileID: "second"},
		{Kind: AttachmentPhoto, FileID: "third"},
	}

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.callCount() != 1 {
		t.Fatalf("model calls = %d, want 1", m.callCount())
	}
	last := m.lastRequest().Messages[len(m.lastRequest().Messages)-1]
	if last.Content != "Сравни фотографии" || len(last.ImageURLs) != 3 {
		t.Fatalf("model message = %+v", last)
	}
	if last.ImageURL != last.ImageURLs[0] {
		t.Fatalf("ImageURL = %q, want first ImageURLs entry %q", last.ImageURL, last.ImageURLs[0])
	}
	for i, want := range []struct {
		data []byte
		mime string
	}{{[]byte("first-jpeg"), "image/jpeg"}, {[]byte("second-png"), "image/png"}, {[]byte("third-jpeg"), "image/jpeg"}} {
		imageURL, err := imageDataURI(DownloadedAttachment{Data: want.data, MIME: want.mime})
		if err != nil {
			t.Fatalf("imageDataURI: %v", err)
		}
		if last.ImageURLs[i] != imageURL {
			t.Errorf("ImageURLs[%d] = %q, want %q", i, last.ImageURLs[i], imageURL)
		}
	}
	if got := strings.Join(downloader.callIDs(), ","); got != "first,second,third" {
		t.Fatalf("download order = %q", got)
	}
}

func TestMultiPhotoHonorsCombinedMediaLimit(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	cfg.MediaMaxBytes = 5
	m := &fakeModel{response: model.Response{Content: "не должно быть"}}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, nil)
	d.deps.Attachments = &albumAttachmentDownloader{files: map[string]DownloadedAttachment{
		"first":  {Data: []byte("123"), MIME: "image/jpeg"},
		"second": {Data: []byte("456"), MIME: "image/jpeg"},
	}}
	ev := testEvent(171, 11, 1301, "Два фото")
	ev.Attachment = &Attachment{Kind: AttachmentPhoto, FileID: "first"}
	ev.AdditionalAttachments = []Attachment{{Kind: AttachmentPhoto, FileID: "second"}}

	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if m.callCount() != 0 || !strings.Contains(r.joined(), "слишком большое") {
		t.Fatalf("model calls = %d, reply = %q", m.callCount(), r.joined())
	}
}

func TestPhotoAlbumUsesOneModelCallAndPersistsEveryOffset(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "один ответ на альбом"}}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	d.deps.Attachments = &albumAttachmentDownloader{files: map[string]DownloadedAttachment{
		"one":   {Data: []byte("one"), MIME: "image/jpeg"},
		"two":   {Data: []byte("two"), MIME: "image/jpeg"},
		"three": {Data: []byte("three"), MIME: "image/jpeg"},
	}}
	events := []Event{
		testEvent(172, 11, 1310, ""),
		testEvent(172, 11, 1311, "Подпись ко всему альбому"),
		testEvent(172, 11, 1312, ""),
	}
	for i, id := range []string{"one", "two", "three"} {
		events[i].Attachment = &Attachment{Kind: AttachmentPhoto, FileID: id}
		events[i].MediaGroupID = "album-1"
	}

	for _, ev := range events[:2] {
		if err := dispatch(t, d, ev); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
	}
	if err := dispatch(t, d, events[1]); err != nil {
		t.Fatalf("duplicate Dispatch: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if m.callCount() != 0 {
		t.Fatalf("album reached model before debounce: %d calls", m.callCount())
	}
	if err := dispatch(t, d, events[2]); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		offset, found, err := store.LastUpdateID(context.Background(), db)
		if err != nil {
			t.Fatalf("LastUpdateID: %v", err)
		}
		if m.callCount() == 1 && r.count() == 1 && found && offset == events[2].UpdateID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("album was not completed: calls=%d replies=%d offset=%d found=%v", m.callCount(), r.count(), offset, found)
		}
		time.Sleep(10 * time.Millisecond)
	}
	last := m.lastRequest().Messages[len(m.lastRequest().Messages)-1]
	if last.Content != "Подпись ко всему альбому" || len(last.ImageURLs) != 3 || last.ImageURL != last.ImageURLs[0] {
		t.Fatalf("album model message = %+v", last)
	}
	rows, err := store.RecentByChat(context.Background(), db, 172, 10)
	if err != nil || len(rows) != 2 || rows[0].Role != store.RoleUser || rows[1].Role != store.RoleAssistant {
		t.Fatalf("album rows = %+v, err=%v", rows, err)
	}
	if err := dispatch(t, d, events[2]); err != nil {
		t.Fatalf("completed album redelivery: %v", err)
	}
	time.Sleep(albumDebounce + 100*time.Millisecond)
	if m.callCount() != 1 || r.count() != 1 {
		t.Fatalf("completed album redelivery produced calls=%d replies=%d", m.callCount(), r.count())
	}
}
