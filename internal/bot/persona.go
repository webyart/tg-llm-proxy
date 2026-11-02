package bot

import (
	"context"
	"fmt"
	"strings"

	"bot/internal/store"

	"github.com/go-telegram/bot/models"
)

type personaPreset struct {
	name        string
	prompt      string
	description string
}

var personaPresets = []personaPreset{
	{name: "assistant", prompt: "You are a helpful assistant. Answer in the user's language.", description: "универсальный помощник"},
	{name: "coder", prompt: "You are a senior software engineer. Give correct, practical answers, readable code, and concise explanations in the user's language.", description: "программирование, код и архитектура"},
	{name: "writer", prompt: "You are a professional writer and editor. Write clear, natural, well-structured text in the user's language.", description: "тексты, редактура и структура"},
	{name: "translator", prompt: "You are a professional translator. Preserve meaning, tone, and formatting; translate into the language requested by the user.", description: "перевод с сохранением смысла и формата"},
}

func (d *Dispatcher) effectivePersona(ctx context.Context, chatID int64) (string, string, bool, error) {
	if d.deps.DB == nil {
		return d.deps.Cfg.SystemPrompt, "default", false, nil
	}
	prompt, found, err := store.ChatPersona(ctx, d.deps.DB, chatID)
	if err != nil {
		return "", "", false, err
	}
	if !found {
		return d.deps.Cfg.SystemPrompt, "default", false, nil
	}
	name := "custom"
	for _, preset := range personaPresets {
		if prompt == preset.prompt {
			name = preset.name
			break
		}
	}
	return prompt, name, true, nil
}

func personaMarkup() models.InlineKeyboardMarkup {
	labels := []struct {
		text string
		data string
	}{
		{"Assistant", "persona:assistant"},
		{"Coder", "persona:coder"},
		{"Writer", "persona:writer"},
		{"Translator", "persona:translator"},
		{"Своя роль", "persona:custom"},
		{"Сбросить", "persona:reset"},
	}
	rows := make([][]models.InlineKeyboardButton, 0, (len(labels)+1)/2)
	for index := 0; index < len(labels); index += 2 {
		row := []models.InlineKeyboardButton{{Text: labels[index].text, CallbackData: labels[index].data}}
		if index+1 < len(labels) {
			row = append(row, models.InlineKeyboardButton{Text: labels[index+1].text, CallbackData: labels[index+1].data})
		}
		rows = append(rows, row)
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (d *Dispatcher) sendPersonaMenu(ctx context.Context, chatID int64, text string) error {
	if sender, ok := d.deps.Replier.(interface {
		SendMenu(context.Context, int64, string, models.InlineKeyboardMarkup) error
	}); ok {
		return sender.SendMenu(ctx, chatID, Escape(text, d.deps.Cfg.ParseMode), personaMarkup())
	}
	return d.reply(ctx, chatID, text)
}

func (d *Dispatcher) personaControl(ctx context.Context, ev Event, arg string) error {
	action := strings.TrimSpace(arg)
	lower := strings.ToLower(action)
	if lower == "reset" {
		if err := store.ClearChatPersona(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, "Для этого чата восстановлен глобальный SYSTEM_PROMPT. Изменение применится к следующему запросу.")
	}
	if lower == "custom" || strings.HasPrefix(lower, "custom ") || strings.HasPrefix(lower, "custom\n") {
		prompt := strings.TrimSpace(action[len("custom"):])
		if prompt == "" {
			return d.reply(ctx, ev.ChatID, "После custom нужен текст роли: /persona custom <системный промпт>.")
		}
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatPersona(ctx, d.deps.DB, ev.ChatID, prompt); err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, "Пользовательская persona сохранена для этого чата и будет применяться со следующего запроса. /persona reset — возврат к SYSTEM_PROMPT.")
	}
	if action != "" {
		for _, preset := range personaPresets {
			if lower == preset.name {
				if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
					return err
				}
				if err := store.SetChatPersona(ctx, d.deps.DB, ev.ChatID, preset.prompt); err != nil {
					return err
				}
				return d.reply(ctx, ev.ChatID, fmt.Sprintf("Для этого чата выбрана persona %s. Изменение применится к следующему запросу. /persona reset — возврат к SYSTEM_PROMPT.", preset.name))
			}
		}
		return d.reply(ctx, ev.ChatID, "Неизвестная persona. Используйте /persona, чтобы увидеть список, или /persona custom <текст>.")
	}

	_, current, override, err := d.effectivePersona(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	source := "глобальный SYSTEM_PROMPT"
	if override {
		source = "настройка этого чата"
	}
	lines := []string{fmt.Sprintf("Текущая persona: %s (%s).", current, source), "", "Доступные пресеты:"}
	for _, preset := range personaPresets {
		lines = append(lines, fmt.Sprintf("/persona %s — %s", preset.name, preset.description))
	}
	lines = append(lines, "/persona custom <текст> — своя роль", "/persona reset — вернуть глобальный SYSTEM_PROMPT")
	return d.sendPersonaMenu(ctx, ev.ChatID, strings.Join(lines, "\n"))
}
