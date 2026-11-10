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

	"bot/internal/i18n"
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
	lang := d.chatLanguage(ctx, ev.ChatID)
	requested := strings.TrimSpace(arg)
	if requested != "" {
		speed, err := d.setTTSSpeed(ctx, ev.ChatID, requested)
		if err != nil {
			if errors.Is(err, model.ErrTTSSpeed) {
				return d.reply(ctx, ev.ChatID, i18n.T(lang, "tts.speed_usage"))
			}
			return err
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "tts.speed_saved", formatTTSSpeed(speed)))
	}
	speed, err := d.ttsSpeed(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, i18n.T(lang, "tts.speed_current", formatTTSSpeed(speed), i18n.T(lang, "tts.speed_usage")))
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
	lang := d.chatLanguage(ctx, ev.ChatID)
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
	rows = append(rows, menuRow(backButtonFor(lang, "menu:more")))
	text := menuText(i18n.T(lang, "tts.speed_menu", formatTTSSpeed(speed)), note)
	return d.renderMenu(ctx, ev, text, models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (d *Dispatcher) ttsSpeedMenuControl(ctx context.Context, ev Event, action string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	value, ok := strings.CutPrefix(action, "menu:tts:speed:")
	if !ok {
		return d.showTTSSpeedMenu(ctx, ev, menuExpiredFor(lang))
	}
	if _, valid := parseTTSSpeed(value); !valid {
		return d.showTTSSpeedMenu(ctx, ev, menuExpiredFor(lang))
	}
	speed, err := d.setTTSSpeed(ctx, ev.ChatID, value)
	if err != nil {
		return err
	}
	return d.showTTSSpeedMenu(ctx, ev, i18n.T(lang, "tts.speed_menu_saved", formatTTSSpeed(speed)))
}

func (d *Dispatcher) ttsActionReply(ctx context.Context, ev Event, text string) error {
	return d.menuReply(ctx, ev, text, simpleBackMarkupFor(d.chatLanguage(ctx, ev.ChatID), "menu:more"))
}

func (d *Dispatcher) synthesizeSpeech(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	if !d.deps.Cfg.TTSEnabled {
		return d.ttsActionReply(ctx, ev, i18n.T(lang, "tts.disabled"))
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
			return d.ttsActionReply(ctx, ev, i18n.T(lang, "tts.no_assistant"))
		}
		text = answer
	}
	if text == "" {
		return d.beginPendingInput(ctx, ev, CommandTTS, "menu:more", i18n.T(lang, "tts.prompt", d.deps.Cfg.TTSMaxChars))
	}
	maxChars := d.deps.Cfg.TTSMaxChars
	if maxChars <= 0 {
		maxChars = 1000
	}
	if Runes(text) > int(maxChars) {
		return d.ttsActionReply(ctx, ev, i18n.T(lang, "tts.too_long", maxChars))
	}
	sender, ok := d.deps.Replier.(AudioSender)
	if !ok {
		return d.ttsActionReply(ctx, ev, i18n.T(lang, "tts.audio_unsupported"))
	}
	if d.deps.TTS == nil {
		return d.ttsActionReply(ctx, ev, i18n.T(lang, "tts.not_configured"))
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
			return d.ttsActionReply(ctx, ev, i18n.T(lang, "tts.timeout", d.deps.Cfg.TTSTimeoutSeconds))
		}
		return d.ttsActionReply(ctx, ev, i18n.T(lang, "tts.failed"))
	}
	if err := sender.SendAudio(ctx, ev.ChatID, Audio{Name: "speech.mp3", Data: audio, MIME: "audio/mpeg"}); err != nil {
		d.deps.Logger.Error("TTS audio delivery failed", "chat_id", ev.ChatID)
		return d.ttsActionReply(ctx, ev, i18n.T(lang, "tts.delivery_failed"))
	}
	return nil
}
