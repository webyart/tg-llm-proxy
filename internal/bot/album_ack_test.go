package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"
	"bot/internal/testutil"
)

// TestPhotoAlbumStorageFailureIsNotAcknowledged — v1.57.1 made a failed append visible for a single message:
// the user hears why, and the offset does not move. An album used to call runBounded(..., false) and then
// persist every offset unconditionally, so one failed append acknowledged the whole album for good.
func TestPhotoAlbumStorageFailureIsNotAcknowledged(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	db := testStore(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER album_append_fails BEFORE INSERT ON messages
		WHEN NEW.chat_id = 193
		BEGIN
			SELECT RAISE(ABORT, 'album append failed');
		END;`); err != nil {
		t.Fatalf("creating the failing trigger: %v", err)
	}
	m := &fakeModel{response: model.Response{Content: "не должно вызываться"}}
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, db)
	d.deps.Attachments = &albumAttachmentDownloader{files: map[string]DownloadedAttachment{
		"photo": {Data: []byte("jpeg"), MIME: "image/jpeg"},
	}}

	ev := testEvent(193, 11, 1930, "Подпись к альбому")
	ev.Attachment = &Attachment{Kind: AttachmentPhoto, FileID: "photo"}
	ev.MediaGroupID = "album-storage-failure"
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	testutil.Eventually(t, 3*time.Second, func() bool { return r.count() != 0 },
		func() string { return "the user was never told the album question was not stored" })
	if r.count() != 1 || !strings.Contains(r.joined(), msgQuestionNotStored) {
		t.Fatalf("the user was not told the album question was not stored: %q", r.joined())
	}
	if m.callCount() != 0 {
		t.Fatalf("a failed append still reached the model: %d calls", m.callCount())
	}
	if _, found, err := store.LastUpdateID(ctx, db); err != nil || found {
		t.Fatalf("a failed album append acknowledged the update: found=%v err=%v", found, err)
	}
	if _, found, err := store.GetState(ctx, db, albumStateKey(albumKey{chatID: 193, mediaGroupID: "album-storage-failure"})); err != nil || found {
		t.Fatalf("a failed album append wrote the done marker: found=%v err=%v", found, err)
	}
}
