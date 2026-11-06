package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"bot/internal/model"
)

const msgTTSDisabled = "Озвучивание отключено. Включи TTS_ENABLED=true и перезапусти бота."

type Audio struct {
	Name string
	Data []byte
	MIME string
}

type AudioSender interface {
	SendAudio(ctx context.Context, chatID int64, audio Audio) error
}

func (r *telegramReplier) SendAudio(ctx context.Context, chatID int64, audio Audio) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached ")
	}
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(audio.Name), "\\", "/"))
	if name == "" || name == "." || name == "/" {
		return errors.New("audio name is empty")
	}
	if len(audio.Data) == 0 || len(audio.Data) > model.TTSMaxResponseBytes {
		return errors.New("audio is empty or exceeds 20 MiB")
	}
	mime := strings.TrimSpace(audio.MIME)
	if mime != "audio/mpeg" && mime != "audio/mp3" {
		return errors.New("audio must be MP3")
	}
	_, err := r.bot.SendAudio(ctx, &tgbot.SendAudioParams{
		ChatID: chatID,
		Audio:  &models.InputFileUpload{Filename: name, Data: bytes.NewReader(audio.Data)},
	})
	return err
}

func (d *Dispatcher) synthesizeSpeech(ctx context.Context, ev Event, arg string) error {
	if !d.deps.Cfg.TTSEnabled {
		return d.reply(ctx, ev.ChatID, msgTTSDisabled)
	}
	text := strings.TrimSpace(arg)
	if text == "" {
		return d.beginPendingInput(ctx, ev, CommandTTS, "menu:more", fmt.Sprintf("Отправь текст для озвучивания одним сообщением (до %d символов). Ввод действует 2 минуты.", d.deps.Cfg.TTSMaxChars))
	}
	maxChars := d.deps.Cfg.TTSMaxChars
	if maxChars <= 0 {
		maxChars = 1000
	}
	if Runes(text) > int(maxChars) {
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Текст слишком длинный для озвучивания: максимум %d символов.", maxChars))
	}
	sender, ok := d.deps.Replier.(AudioSender)
	if !ok {
		return d.reply(ctx, ev.ChatID, "Отправка аудио не поддерживается этим Telegram-адаптером.")
	}
	if d.deps.TTS == nil {
		return d.reply(ctx, ev.ChatID, "Озвучивание не настроено в этом клиенте.")
	}
	audio, err := d.deps.TTS.Synthesize(ctx, text)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		if errors.Is(err, model.ErrTTSTimeout) || errors.Is(err, context.DeadlineExceeded) {
			return d.reply(ctx, ev.ChatID, fmt.Sprintf("Сервис озвучивания не ответил за %d с. Попробуй ещё раз позже.", d.deps.Cfg.TTSTimeoutSeconds))
		}
		return d.reply(ctx, ev.ChatID, "Не удалось озвучить текст. Попробуй ещё раз позже.")
	}
	if err := sender.SendAudio(ctx, ev.ChatID, Audio{Name: "speech.mp3", Data: audio, MIME: "audio/mpeg"}); err != nil {
		return fmt.Errorf("telegram sendAudio: %w", err)
	}
	return nil
}
