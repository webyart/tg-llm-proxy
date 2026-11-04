package bot

import (
	"context"
	"strconv"
	"strings"
	"time"

	"bot/internal/store"
)

const (
	albumDebounce  = 400 * time.Millisecond
	albumMaxPhotos = 10
)

type albumKey struct {
	chatID       int64
	mediaGroupID string
}

type albumBatch struct {
	events []Event
	timer  *time.Timer
}

func isPhotoAlbum(ev Event) bool {
	return !ev.Recovery && ev.MediaGroupID != "" && ev.Attachment != nil && ev.Attachment.Kind == AttachmentPhoto
}

func (d *Dispatcher) collectAlbum(ctx context.Context, ev Event) {
	key := albumKey{chatID: ev.ChatID, mediaGroupID: ev.MediaGroupID}
	d.albumMu.Lock()
	batch := d.albums[key]
	if batch == nil {
		if d.deps.DB != nil {
			_, processed, err := store.GetState(ctx, d.deps.DB, albumStateKey(key))
			if err != nil {
				d.albumMu.Unlock()
				d.deps.Logger.Error("album dedup state could not be read", "chat_id", ev.ChatID, "error", Sanitize(err, d.deps.Token))
				return
			}
			if processed {
				d.albumMu.Unlock()
				return
			}
		}
		batch = &albumBatch{}
		d.albums[key] = batch
	}
	for _, collected := range batch.events {
		if ev.UpdateID > 0 && ev.UpdateID == collected.UpdateID ||
			ev.TelegramMessageID > 0 && ev.TelegramMessageID == collected.TelegramMessageID {
			d.albumMu.Unlock()
			return
		}
	}
	batch.events = append(batch.events, ev)
	if strings.TrimSpace(batch.events[0].Text) == "" && strings.TrimSpace(ev.Text) != "" {
		batch.events[0].Text = ev.Text
	}
	if len(batch.events) == albumMaxPhotos {
		delete(d.albums, key)
		if batch.timer != nil {
			batch.timer.Stop()
		}
		d.albumMu.Unlock()
		go d.flushCollectedAlbum(ctx, batch.events)
		return
	}
	if batch.timer == nil {
		batch.timer = time.AfterFunc(albumDebounce, func() {
			d.flushAlbum(ctx, key, batch)
		})
	} else {
		batch.timer.Reset(albumDebounce)
	}
	d.albumMu.Unlock()
}

func (d *Dispatcher) flushAlbum(ctx context.Context, key albumKey, batch *albumBatch) {
	d.albumMu.Lock()
	if d.albums[key] != batch {
		d.albumMu.Unlock()
		return
	}
	delete(d.albums, key)
	events := batch.events
	d.albumMu.Unlock()
	d.flushCollectedAlbum(ctx, events)
}

func (d *Dispatcher) flushCollectedAlbum(ctx context.Context, events []Event) {
	if len(events) == 0 {
		return
	}
	ev := events[0]
	ev.AdditionalAttachments = nil
	for _, collected := range events[1:] {
		if collected.Attachment != nil {
			ev.AdditionalAttachments = append(ev.AdditionalAttachments, *collected.Attachment)
		}
	}
	if err := d.dispatchEvent(ctx, ev, false); err == nil && d.deps.DB != nil {
		if err := store.SetState(ctx, d.deps.DB, albumStateKey(albumKey{chatID: ev.ChatID, mediaGroupID: ev.MediaGroupID}), "done"); err != nil {
			d.deps.Logger.Error("album dedup state was not stored", "chat_id", ev.ChatID, "error", Sanitize(err, d.deps.Token))
		}
	}
	for _, collected := range events {
		d.persistOffset(ctx, collected.UpdateID)
	}
}

func albumStateKey(key albumKey) string {
	return "album_done_" + strconv.FormatInt(key.chatID, 10) + "_" + key.mediaGroupID
}
