package bot

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"bot/internal/model"
	"bot/internal/store"
)

const (
	msgTTSDisabled    = "Озвучивание отключено. Включи TTS_ENABLED=true и перезапусти бота."
	msgTTSNoAssistant = "В этом чате ещё нет ответа ассистента для озвучивания."
	msgTTSSpeedUsage  = "Использование: /tts speed — текущая скорость; /tts speed 0.75|1|1.25|1.5 — изменить."
	ttsDefaultSpeed   = 1.0
)

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

func parseTTSSpeed(value string) (float64, bool) {
	switch value {
	case "0.75":
		return 0.75, true
	case "1":
		return 1, true
	case "1.25":
		return 1.25, true
	case "1.5":
		return 1.5, true
	default:
		return 0, false
	}
}

func formatTTSSpeed(speed float64) string {
	return strconv.FormatFloat(speed, 'f', -1, 64)
}

func ttsSpeedStateKey(chatID int64) string {
	return "tts_speed:" + strconv.FormatInt(chatID, 10)
}

func TTSSpeed(ctx context.Context, db *sql.DB, chatID int64) (float64, error) {
	value, found, err := store.GetState(ctx, db, ttsSpeedStateKey(chatID))
	if err != nil {
		return 0, err
	}
	if !found {
		return ttsDefaultSpeed, nil
	}
	speed, ok := parseTTSSpeed(value)
	if !ok {
		return 0, fmt.Errorf("bot_state %s contains an unsupported TTS speed", ttsSpeedStateKey(chatID))
	}
	return speed, nil
}

func SetTTSSpeed(ctx context.Context, db *sql.DB, chatID int64, value string) (float64, error) {
	speed, ok := parseTTSSpeed(value)
	if !ok {
		return 0, model.ErrTTSSpeed
	}
	if err := store.SetState(ctx, db, ttsSpeedStateKey(chatID), value); err != nil {
		return 0, err
	}
	return speed, nil
}

func (d *Dispatcher) ttsSpeed(ctx context.Context, chatID int64) (float64, error) {
	return TTSSpeed(ctx, d.deps.DB, chatID)
}

func (d *Dispatcher) setTTSSpeed(ctx context.Context, chatID int64, value string) (float64, error) {
	return SetTTSSpeed(ctx, d.deps.DB, chatID, value)
}

func (d *Dispatcher) ttsSpeedControl(ctx context.Context, ev Event, arg string) error {
	requested := strings.TrimSpace(arg)
	if requested != "" {
		speed, err := d.setTTSSpeed(ctx, ev.ChatID, requested)
		if err != nil {
			if errors.Is(err, model.ErrTTSSpeed) {
				return d.reply(ctx, ev.ChatID, msgTTSSpeedUsage)
			}
			return err
		}
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Скорость озвучивания для этого чата: %s×. Настройка сохранена.", formatTTSSpeed(speed)))
	}
	speed, err := d.ttsSpeed(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Текущая скорость озвучивания для этого чата: %s×.\n%s", formatTTSSpeed(speed), msgTTSSpeedUsage))
}

func (d *Dispatcher) latestAssistantAnswer(ctx context.Context, chatID int64) (string, error) {
	var content string
	err := d.deps.DB.QueryRowContext(ctx, `
		SELECT content FROM messages
		WHERE chat_id = ? AND role = ?
		ORDER BY id DESC LIMIT 1`, chatID, store.RoleAssistant).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(content), nil
}

func (d *Dispatcher) showTTSSpeedMenu(ctx context.Context, ev Event, note ...string) error {
	speed, err := d.ttsSpeed(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	rows := make([][]models.InlineKeyboardButton, 0, 3)
	for _, value := range []string{"0.75", "1", "1.25", "1.5"} {
		label := value + "×"
		parsed, _ := parseTTSSpeed(value)
		if parsed == speed {
			label = "• " + label
		}
		rows = append(rows, menuRow(menuButton(label, "menu:tts:speed:"+value)))
	}
	rows = append(rows, menuRow(backButton("menu:more")))
	text := menuText(fmt.Sprintf("Скорость озвучивания для этого чата: %s×.", formatTTSSpeed(speed)), note)
	return d.renderMenu(ctx, ev, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (d *Dispatcher) ttsSpeedMenuControl(ctx context.Context, ev Event, action string) error {
	value, ok := strings.CutPrefix(action, "menu:tts:speed:")
	if !ok {
		return d.showTTSSpeedMenu(ctx, ev, menuExpiredText)
	}
	if _, valid := parseTTSSpeed(value); !valid {
		return d.showTTSSpeedMenu(ctx, ev, menuExpiredText)
	}
	speed, err := d.setTTSSpeed(ctx, ev.ChatID, value)
	if err != nil {
		return err
	}
	return d.showTTSSpeedMenu(ctx, ev, fmt.Sprintf("Скорость сохранена: %s×.", formatTTSSpeed(speed)))
}

func (d *Dispatcher) synthesizeSpeech(ctx context.Context, ev Event, arg string) error {
	if !d.deps.Cfg.TTSEnabled {
		return d.reply(ctx, ev.ChatID, msgTTSDisabled)
	}
	text := strings.TrimSpace(arg)
	if fields := strings.Fields(text); len(fields) > 0 && strings.EqualFold(fields[0], "speed") {
		return d.ttsSpeedControl(ctx, ev, strings.TrimSpace(text[len(fields[0]):]))
	}
	if strings.EqualFold(text, "last") {
		answer, err := d.latestAssistantAnswer(ctx, ev.ChatID)
		if err != nil {
			return err
		}
		if answer == "" {
			return d.reply(ctx, ev.ChatID, msgTTSNoAssistant)
		}
		text = answer
	}
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
	speed, err := d.ttsSpeed(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	audio, err := func() ([]byte, error) {
		stopTyping := d.startTypingHeartbeat(ctx, ev.ChatID)
		defer stopTyping()
		return d.deps.TTS.Synthesize(ctx, text, speed)
	}()
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
		d.deps.Logger.Error("TTS audio delivery failed", "chat_id", ev.ChatID)
		return d.reply(ctx, ev.ChatID, "Не удалось доставить аудио в Telegram. Попробуй ещё раз позже.")
	}
	return nil
}
