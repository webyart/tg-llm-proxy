//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/testutil"
)

func TestAudioAndVideoNoteEndToEnd(t *testing.T) {
	for i, tc := range []struct {
		name     string
		data     []byte
		mime     string
		filename string
		push     func(*testutil.Telegram, int64, int64, int64, string, int)
	}{
		{name: "audio", data: []byte("ID3-audio"), mime: "audio/mpeg", filename: "audio.bin", push: (*testutil.Telegram).PushAudio},
		{name: "video note", data: []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, mime: "video/mp4", filename: "video-note.mp4", push: (*testutil.Telegram).PushVideoNote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := dbFile(t)
			tgAPI := testutil.NewTelegram(t, secretToken)
			strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "speech accepted"})
			strata.SetTranscription("speech transcript")
			cfg := integrationEnv(t, path, strata.BaseURL(), nil)
			db := openStore(t, path)
			startBot(t, t.Context(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))
			tgAPI.AddFile("speech-file", "media/file", tc.mime, tc.data)
			tc.push(tgAPI, int64(401+i), chatID, userID, "speech-file", 2)
			waitFor(t, "speech answer", func() bool { return strings.Contains(tgAPI.Joined(), "speech accepted") })
			if strata.TranscriptionCalls() != 1 || strata.LastTranscriptionContentType() != tc.mime || strata.LastTranscriptionFilename() != tc.filename || !strings.Contains(strata.LastRequestBody(), "speech transcript") {
				t.Fatalf("transcription calls=%d content-type=%q filename=%q", strata.TranscriptionCalls(), strata.LastTranscriptionContentType(), strata.LastTranscriptionFilename())
			}
		})
	}
}

func TestPhotoAlbumProducesOneAnswer(t *testing.T) {
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "album accepted"})
	cfg := integrationEnv(t, path, strata.BaseURL(), nil)
	db := openStore(t, path)
	startBot(t, t.Context(), cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))
	for _, id := range []string{"one", "two", "three"} {
		tgAPI.AddFile(id, "photos/"+id+".jpg", "image/jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0})
	}
	tgAPI.PushPhotoGroup(410, chatID, userID, "one", "album", "")
	tgAPI.PushPhotoGroup(411, chatID, userID, "two", "album", "Сравни фото")
	tgAPI.PushPhotoGroup(412, chatID, userID, "three", "album", "")
	waitFor(t, "album answer", func() bool { return strings.Contains(tgAPI.Joined(), "album accepted") })
	quiet()
	if strata.ChatCalls() != 1 || tgAPI.Calls("getFile") != 3 || len(tgAPI.Sent()) != 1 {
		t.Fatalf("model=%d getFile=%d replies=%d", strata.ChatCalls(), tgAPI.Calls("getFile"), len(tgAPI.Sent()))
	}
	body := strata.LastRequestBody()
	if strings.Count(body, `"type":"image_url"`) != 3 || !strings.Contains(body, "Сравни фото") {
		t.Fatalf("album request is incomplete: images=%d caption=%v body=%s", strings.Count(body, `"type":"image_url"`), strings.Contains(body, "Сравни фото"), body)
	}
}
