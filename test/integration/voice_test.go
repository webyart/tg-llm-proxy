//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"bot/internal/bot"
	"bot/internal/store"
	"bot/internal/testutil"
)

func TestVoiceOverFakeTelegramAndStrata(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "ответ на транскрипцию", Model: "fake-model"})
	strata.SetTranscription("распознанная фраза")
	cfg := integrationEnv(t, path, strata.BaseURL(), nil)
	db := openStore(t, path)
	startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))

	audio := []byte("OggS-test-audio")
	tgAPI.AddFile("voice-file", "voice/message.ogg", "audio/ogg", audio)
	tgAPI.PushVoice(301, chatID, userID, "voice-file", 3)
	waitFor(t, "voice answer", func() bool { return strings.Contains(tgAPI.Joined(), "ответ на транскрипцию") })
	if tgAPI.Calls("getFile") != 1 || tgAPI.Calls("download") != 1 || strata.TranscriptionCalls() != 1 || strata.ChatCalls() != 1 {
		t.Fatalf("getFile=%d download=%d transcribe=%d chat=%d", tgAPI.Calls("getFile"), tgAPI.Calls("download"), strata.TranscriptionCalls(), strata.ChatCalls())
	}
	if strata.LastTranscriptionModel() != "qwen-test" || strata.LastTranscriptionBytes() != len(audio) {
		t.Fatalf("transcription model=%q bytes=%d", strata.LastTranscriptionModel(), strata.LastTranscriptionBytes())
	}
	if !strings.Contains(strata.LastRequestBody(), "распознанная фраза") {
		t.Fatalf("chat request lost transcript: %s", strata.LastRequestBody())
	}
	rows, err := store.RecentByChat(context.Background(), db, chatID, 10)
	if err != nil || len(rows) != 2 || !strings.Contains(rows[0].Content, "распознанная фраза") || strings.Contains(rows[0].Content, "OggS") {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestVoiceDurationLimitRunsBeforeDownload(t *testing.T) {
	ctx := context.Background()
	path := dbFile(t)
	tgAPI := testutil.NewTelegram(t, secretToken)
	strata := testutil.NewStrata(t, secretStrataKey, testutil.StrataReply{Content: "не должно быть"})
	cfg := integrationEnv(t, path, strata.BaseURL(), nil)
	db := openStore(t, path)
	startBot(t, ctx, cfg, db, newClient(t, cfg, strata), tgAPI, bot.Options{ServerURL: tgAPI.URL(), PollTimeout: 2 * time.Second}, storedOffset(t, db))

	tgAPI.AddFile("long-voice", "voice/long.ogg", "audio/ogg", []byte("OggS-audio"))
	tgAPI.PushVoice(302, chatID, userID, "long-voice", bot.VoiceMaxSeconds+1)
	waitFor(t, "voice duration rejection", func() bool { return strings.Contains(tgAPI.Joined(), "5 минут") })
	if tgAPI.Calls("getFile") != 0 || tgAPI.Calls("download") != 0 || strata.TranscriptionCalls() != 0 || strata.ChatCalls() != 0 {
		t.Fatalf("long voice triggered network/model work: getFile=%d download=%d transcribe=%d chat=%d", tgAPI.Calls("getFile"), tgAPI.Calls("download"), strata.TranscriptionCalls(), strata.ChatCalls())
	}
}
