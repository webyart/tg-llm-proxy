package bot

import (
	"context"
	"strings"

	"bot/internal/i18n"
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

type PersonaPresetInfo struct {
	Name        string
	Prompt      string
	Description string
}

func PersonaPresetDefinitions() []PersonaPresetInfo {
	presets := make([]PersonaPresetInfo, 0, len(personaPresets))
	for _, preset := range personaPresets {
		presets = append(presets, PersonaPresetInfo{Name: preset.name, Prompt: preset.prompt, Description: preset.description})
	}
	return presets
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
	return personaMarkupFor(i18n.Default)
}

func personaMarkupFor(lang string) models.InlineKeyboardMarkup {
	labels := []struct {
		text string
		data string
	}{
		{personaPresetName(lang, "assistant"), "persona:assistant"},
		{personaPresetName(lang, "coder"), "persona:coder"},
		{personaPresetName(lang, "writer"), "persona:writer"},
		{personaPresetName(lang, "translator"), "persona:translator"},
		{i18n.T(lang, "persona.custom_button"), "persona:custom"},
		{i18n.T(lang, "persona.reset_button"), "persona:reset"},
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
	lang := d.chatLanguage(ctx, chatID)
	if sender, ok := d.deps.Replier.(interface {
		SendMenu(context.Context, int64, string, models.InlineKeyboardMarkup) error
	}); ok {
		return sender.SendMenu(ctx, chatID, Escape(text, d.deps.Cfg.ParseMode), personaMarkupFor(lang))
	}
	return d.reply(ctx, chatID, text)
}

func personaMenuMarkup() models.InlineKeyboardMarkup {
	return personaMenuMarkupFor(i18n.Default)
}

func personaMenuMarkupFor(lang string) models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{}
	for _, preset := range personaPresets {
		rows = append(rows, menuRow(menuButton(personaPresetDescription(lang, preset), "menu:persona:set:"+preset.name)))
	}
	rows = append(rows,
		menuRow(menuButton(i18n.T(lang, "persona.custom_button"), "menu:persona:custom")),
		menuRow(menuButton(i18n.T(lang, "persona.reset_button"), "menu:persona:reset")),
		menuRow(backButtonFor(lang, "menu:settings")),
	)
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// personaPresetName and personaPresetDescription localize only the display strings. The stored preset
// identity stays preset.name ("assistant", "coder", ...), which is what callbacks and chat_persona use.
func personaPresetName(lang string, name string) string {
	if translated, ok := i18n.Lookup(lang, "persona.preset."+name+".name"); ok {
		return translated
	}
	return name
}

func personaPresetDescription(lang string, preset personaPreset) string {
	if translated, ok := i18n.Lookup(lang, "persona.preset."+preset.name+".description"); ok {
		return translated
	}
	return preset.description
}

func (d *Dispatcher) showPersonaMenu(ctx context.Context, ev Event, note ...string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	_, current, override, err := d.effectivePersona(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	source := i18n.T(lang, "persona.source_global")
	if override {
		source = i18n.T(lang, "persona.source_chat")
	}
	text := menuText(i18n.T(lang, "persona.current", current, source), note)
	return d.renderMenu(ctx, ev, text, personaMenuMarkupFor(lang))
}

func (d *Dispatcher) personaMenuControl(ctx context.Context, ev Event, action string, lower string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	switch lower {
	case "menu":
		return d.showPersonaMenu(ctx, ev)
	case "menu:custom":
		return d.beginPendingInput(ctx, ev, CommandPersona, "menu:persona", i18n.T(lang, "persona.custom_hint"))
	case "menu:reset":
		if err := store.ClearChatPersona(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		return d.showPersonaMenu(ctx, ev, i18n.T(lang, "persona.menu_reset"))
	}
	name, ok := strings.CutPrefix(lower, "menu:set:")
	if !ok {
		return d.showPersonaMenu(ctx, ev, menuExpiredFor(lang))
	}
	for _, preset := range personaPresets {
		if name != preset.name {
			continue
		}
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatPersona(ctx, d.deps.DB, ev.ChatID, preset.prompt); err != nil {
			return err
		}
		return d.showPersonaMenu(ctx, ev, i18n.T(lang, "persona.menu_selected", personaPresetName(lang, preset.name)))
	}
	return d.showPersonaMenu(ctx, ev, menuExpiredText)
}

func (d *Dispatcher) personaControl(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	action := strings.TrimSpace(arg)
	lower := strings.ToLower(action)
	if ev.MenuCallback && !strings.HasPrefix(lower, "menu") {
		switch lower {
		case "reset":
			return d.personaMenuControl(ctx, ev, "menu:reset", "menu:reset")
		case "custom":
			return d.personaMenuControl(ctx, ev, "menu:custom", "menu:custom")
		default:
			for _, preset := range personaPresets {
				if lower == preset.name {
					action := "menu:set:" + preset.name
					return d.personaMenuControl(ctx, ev, action, action)
				}
			}
			return d.renderMenu(ctx, ev, i18n.T(lang, "persona.menu_unknown"), simpleBackMarkupFor(lang, "menu:persona"))
		}
	}
	if strings.HasPrefix(lower, "menu") {
		return d.personaMenuControl(ctx, ev, action, lower)
	}
	if lower == "reset" {
		if err := store.ClearChatPersona(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "persona.reset"))
	}
	if lower == "custom" || strings.HasPrefix(lower, "custom ") || strings.HasPrefix(lower, "custom\n") {
		prompt := strings.TrimSpace(action[len("custom"):])
		if prompt == "" {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "persona.custom_needs_text"))
		}
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatPersona(ctx, d.deps.DB, ev.ChatID, prompt); err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "persona.custom_saved"))
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
				return d.reply(ctx, ev.ChatID, i18n.T(lang, "persona.selected", personaPresetName(lang, preset.name)))
			}
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "persona.unknown"))
	}

	_, current, override, err := d.effectivePersona(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	source := i18n.T(lang, "persona.source_global")
	if override {
		source = i18n.T(lang, "persona.source_chat")
	}
	lines := []string{i18n.T(lang, "persona.current", current, source), "", i18n.T(lang, "persona.presets_header")}
	for _, preset := range personaPresets {
		lines = append(lines, i18n.T(lang, "persona.preset_line", preset.name, personaPresetDescription(lang, preset)))
	}
	lines = append(lines, i18n.T(lang, "persona.custom_line"), i18n.T(lang, "persona.reset_line"))
	return d.sendPersonaMenu(ctx, ev.ChatID, strings.Join(lines, "\n"))
}
