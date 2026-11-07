package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"bot/internal/store"
)

const (
	menuModelChoiceLimit    = 12
	menuDocumentChoiceLimit = 20
	pendingInputTTL         = 2 * time.Minute
	menuExpiredText         = "Это меню истекло или уже использовано. Открой /help и попробуй заново."
)

var menuCommandCallbacks = map[string]string{
	"menu:ask":                   "/help menu:ask",
	"menu:reset":                 "/reset",
	"menu:history":               "/history menu",
	"menu:history:5":             "/history 5",
	"menu:history:20":            "/history 20",
	"menu:search":                "/help menu:search",
	"menu:export:md":             "/export md",
	"menu:export:json":           "/export json",
	"menu:delete":                "/delete",
	"menu:docs":                  "/docs menu",
	"menu:docs:add":              "/docs menu:add",
	"menu:docs:search":           "/docs menu:search",
	"menu:docs:export":           "/docs export",
	"menu:docs:clear":            "/docs menu:clear",
	"menu:links":                 "/links menu",
	"menu:links:search":          "/links menu:search",
	"menu:reminders":             "/reminders menu",
	"menu:reminder:new":          "/reminders menu:new",
	"menu:reminder:new:10m":      "/reminders menu:new:10m",
	"menu:reminder:new:1h":       "/reminders menu:new:1h",
	"menu:reminder:new:tomorrow": "/reminders menu:new:tomorrow",
	"menu:reminder:failed":       "/reminders menu:failed",
	"menu:task":                  "/task menu",
	"menu:task:cancel":           "/task menu:cancel",
	"menu:task:resume":           "/task menu:resume",
	"menu:task:clear":            "/task menu:clear",
	"menu:task:new":              "/task menu:new",
	"menu:tts":                   "/help menu:tts",
	"menu:tts:last":              "/tts last",
	"menu:tts:speed":             "/help menu:tts:speed",
	"menu:tts:speed:0.75":        "/help menu:tts:speed:0.75",
	"menu:tts:speed:1":           "/help menu:tts:speed:1",
	"menu:tts:speed:1.25":        "/help menu:tts:speed:1.25",
	"menu:tts:speed:1.5":         "/help menu:tts:speed:1.5",
	"menu:settings":              "/help menu:settings",
	"menu:model":                 "/model menu",
	"menu:model:check":           "/model menu:check",
	"menu:model:reset":           "/model menu:reset",
	"menu:persona":               "/persona menu",
	"menu:persona:custom":        "/persona menu:custom",
	"menu:persona:reset":         "/persona menu:reset",
	"menu:stream":                "/stream menu",
	"menu:stream:on":             "/stream menu:on",
	"menu:stream:off":            "/stream menu:off",
	"menu:stream:reset":          "/stream menu:reset",
	"menu:status":                "/status menu",
	"menu:stats":                 "/stats",
	"menu:usage":                 "/usage menu",
	"menu:ping":                  "/ping",
	"menu:help":                  "/help menu:help",
	"menu:more":                  "/help menu:more",
	"menu:skip":                  "/skip",
	"menu:unskip":                "/unskip",
	"menu:whoami":                "/whoami",
	"menu:admin":                 "/admin",
	"menu:root":                  "/help menu:root",
}

func menuButton(text string, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

func menuRow(buttons ...models.InlineKeyboardButton) []models.InlineKeyboardButton {
	return buttons
}

func backButton(destination string) models.InlineKeyboardButton {
	return menuButton("← Назад", destination)
}

// menuMarkup is the root inline menu shown by /start and /help.
func (d *Dispatcher) menuMarkup() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Спросить", "menu:ask"), menuButton("Новый контекст", "menu:reset")),
		menuRow(menuButton("История", "menu:history"), menuButton("Документы", "menu:docs")),
		menuRow(menuButton("Настройки", "menu:settings"), menuButton("Статус", "menu:status")),
		menuRow(menuButton("Помощь", "menu:help"), menuButton("Ещё", "menu:more")),
	}}
}

func historyMenuMarkup() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Последние 5", "menu:history:5"), menuButton("Последние 20", "menu:history:20")),
		menuRow(menuButton("Поиск", "menu:search")),
		menuRow(menuButton("Экспорт MD", "menu:export:md"), menuButton("Экспорт JSON", "menu:export:json")),
		menuRow(menuButton("Удалить историю", "menu:delete")),
		menuRow(backButton("menu:root")),
	}}
}

func settingsMenuMarkup() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Модель", "menu:model")),
		menuRow(menuButton("Persona", "menu:persona")),
		menuRow(menuButton("Стриминг", "menu:stream")),
		menuRow(backButton("menu:root")),
	}}
}

func modelMenuMarkup(names []string, current string) models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{}
	for index, name := range names {
		if index >= menuModelChoiceLimit {
			break
		}
		label := brief(name, 40)
		if name == current {
			label = "• " + label
		}
		rows = append(rows, menuRow(menuButton(label, fmt.Sprintf("menu:model:set:%d", index))))
	}
	rows = append(rows,
		menuRow(menuButton("Проверить endpoint", "menu:model:check")),
		menuRow(menuButton("Сбросить модель", "menu:model:reset")),
		menuRow(backButton("menu:settings")),
	)
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func statusMenuMarkup() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Обновить", "menu:status")),
		menuRow(menuButton("Статистика", "menu:stats"), menuButton("Использование", "menu:usage")),
		menuRow(menuButton("Ping", "menu:ping")),
		menuRow(backButton("menu:root")),
	}}
}

func usageMenuMarkup() models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Обновить", "menu:usage")),
		menuRow(backButton("menu:status")),
	}}
}

func simpleBackMarkup(destination string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{menuRow(backButton(destination))}}
}

func (d *Dispatcher) sendMenu(ctx context.Context, chatID int64, text string) error {
	if sender, ok := d.deps.Replier.(interface {
		SendMenu(context.Context, int64, string, models.InlineKeyboardMarkup) error
	}); ok {
		return sender.SendMenu(ctx, chatID, Escape(text, d.deps.Cfg.ParseMode), d.menuMarkup())
	}
	return d.reply(ctx, chatID, text)
}

func (d *Dispatcher) renderMenu(ctx context.Context, ev Event, text string, markup models.InlineKeyboardMarkup) error {
	if ev.MenuCallback {
		editor, ok := d.deps.Replier.(interface {
			EditMenu(context.Context, int64, int64, string, models.InlineKeyboardMarkup) error
		})
		if !ok {
			return errors.New("telegram menu editor is unavailable")
		}
		if ev.TelegramMessageID <= 0 {
			return errors.New("telegram menu callback has no message id")
		}
		if err := editor.EditMenu(ctx, ev.ChatID, ev.TelegramMessageID, Escape(text, d.deps.Cfg.ParseMode), markup); err != nil {
			return errors.New(Sanitize(err, d.deps.Token))
		}
		return nil
	}
	if sender, ok := d.deps.Replier.(interface {
		SendMenu(context.Context, int64, string, models.InlineKeyboardMarkup) error
	}); ok {
		return sender.SendMenu(ctx, ev.ChatID, Escape(text, d.deps.Cfg.ParseMode), markup)
	}
	return d.reply(ctx, ev.ChatID, text)
}

func (d *Dispatcher) menuReply(ctx context.Context, ev Event, text string, markup models.InlineKeyboardMarkup) error {
	if ev.MenuCallback {
		return d.renderMenu(ctx, ev, text, markup)
	}
	return d.reply(ctx, ev.ChatID, text)
}

func refreshBackMarkup(refresh string, destination string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Обновить", refresh)),
		menuRow(backButton(destination)),
	}}
}

func (r *telegramReplier) SendMenuWithID(ctx context.Context, chatID int64, text string, markup models.InlineKeyboardMarkup) (int64, error) {
	if r.bot == nil {
		return 0, errors.New("the telegram bot is not attached ")
	}
	attempt := 0
	for {
		params := &tgbot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: parseModeOf(r.cfg.ParseMode), ReplyMarkup: markup}
		message, err := r.bot.SendMessage(ctx, params)
		if err == nil {
			if message == nil || message.ID == 0 {
				return 0, errors.New("telegram menu response has no message id")
			}
			return int64(message.ID), nil
		}
		if isParseEntitiesError(err) && params.ParseMode != "" {
			params.Text = plainForParseFallback(text, r.cfg.ParseMode)
			params.ParseMode = ""
			message, fallbackErr := r.bot.SendMessage(ctx, params)
			if fallbackErr == nil {
				r.logger.Warn("telegram rejected formatted text; delivered it as plain text", "chat_id", chatID)
				if message == nil || message.ID == 0 {
					return 0, errors.New("telegram menu response has no message id")
				}
				return int64(message.ID), nil
			}
			err = fallbackErr
		}
		var flood *tgbot.TooManyRequestsError
		if !errors.As(err, &flood) || flood.RetryAfter <= 0 || attempt >= 1 {
			return 0, err
		}
		attempt++
		sleep := time.Duration(flood.RetryAfter) * time.Second
		if sleep > FloodSleepMax {
			sleep = FloodSleepMax
		}
		r.d.logEvent(ctx, r.chatUser(chatID), store.EventFlood, fmt.Sprintf("retry_after %d s", flood.RetryAfter))
		r.logger.Warn("telegram answered 429, the chat waits", "chat_id", chatID, "retry_after", flood.RetryAfter)
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(sleep):
		}
	}
}

func (r *telegramReplier) EditMenu(ctx context.Context, chatID int64, messageID int64, text string, markup models.InlineKeyboardMarkup) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached ")
	}
	params := &tgbot.EditMessageTextParams{
		ChatID: chatID, MessageID: int(messageID), Text: text, ParseMode: parseModeOf(r.cfg.ParseMode), ReplyMarkup: markup,
	}
	_, err := r.bot.EditMessageText(ctx, params)
	if isParseEntitiesError(err) && params.ParseMode != "" {
		params.Text = plainForParseFallback(text, r.cfg.ParseMode)
		params.ParseMode = ""
		_, err = r.bot.EditMessageText(ctx, params)
	}
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		return nil
	}
	return err
}

func (r *telegramReplier) RemoveMenu(ctx context.Context, chatID int64, messageID int64) error {
	if r.bot == nil {
		return errors.New("the telegram bot is not attached ")
	}
	_, err := r.bot.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
		ChatID:      chatID,
		MessageID:   int(messageID),
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}},
	})
	return err
}

func validMenuToken(token string) bool {
	if len(token) != 32 {
		return false
	}
	for _, char := range token {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validMenuNumber(value string, maximum int64) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	return err == nil && number >= 0 && number <= maximum
}

func menuCallbackText(data string) (string, bool) {
	if command, ok := menuCommandCallbacks[data]; ok {
		return command, true
	}
	for _, preset := range personaPresets {
		if data == "menu:persona:set:"+preset.name {
			return "/persona menu:set:" + preset.name, true
		}
	}
	if value, ok := strings.CutPrefix(data, "menu:model:set:"); ok {
		if validMenuNumber(value, menuModelChoiceLimit-1) {
			return "/model menu:set:" + value, true
		}
		return "", false
	}
	if value, ok := strings.CutPrefix(data, "menu:doc:delete:"); ok {
		if validMenuNumber(value, int64(^uint64(0)>>1)) && value != "0" {
			return "/docs menu:delete:" + value, true
		}
		return "", false
	}
	if value, ok := strings.CutPrefix(data, "menu:link:summary:"); ok {
		if validMenuNumber(value, int64(^uint64(0)>>1)) && value != "0" {
			return "/links menu:summary:" + value, true
		}
		return "", false
	}
	if value, ok := strings.CutPrefix(data, "menu:link:delete:"); ok {
		if validMenuNumber(value, int64(^uint64(0)>>1)) && value != "0" {
			return "/links menu:delete:" + value, true
		}
	}
	for _, action := range []string{"confirm", "cancel"} {
		if token, ok := strings.CutPrefix(data, "menu:link:save:"+action+":"); ok {
			if validMenuToken(token) {
				return "/links menu:save:" + action + ":" + token, true
			}
			return "", false
		}
		if token, ok := strings.CutPrefix(data, "menu:link:delete:"+action+":"); ok {
			if validMenuToken(token) {
				return "/links menu:delete:" + action + ":" + token, true
			}
			return "", false
		}
	}
	for _, action := range []string{"cancel", "retry", "delete"} {
		if value, ok := strings.CutPrefix(data, "menu:reminder:"+action+":"); ok {
			if validMenuNumber(value, int64(^uint64(0)>>1)) && value != "0" {
				return "/reminders menu:" + action + ":" + value, true
			}
			return "", false
		}
	}
	for _, action := range []string{"confirm", "cancel"} {
		if token, ok := strings.CutPrefix(data, "menu:doc:"+action+":"); ok {
			if validMenuToken(token) {
				return "/docs menu:" + action + ":" + token, true
			}
			return "", false
		}
	}
	if token, ok := strings.CutPrefix(data, "menu:input:cancel:"); ok && validMenuToken(token) {
		return "/help menu:input:cancel:" + token, true
	}
	for _, action := range []string{"confirm", "scope", "cancel"} {
		if token, ok := strings.CutPrefix(data, "menu:network:"+action+":"); ok {
			if validMenuToken(token) {
				return "/help menu:network:" + action + ":" + token, true
			}
			return "", false
		}
	}
	return "", false
}

func (d *Dispatcher) callbackText(data string) (string, bool) {
	if len(data) == 0 || len(data) > 64 {
		return "", false
	}
	if text, ok := menuCallbackText(data); ok {
		return text, true
	}
	if strings.HasPrefix(data, "cmd:") {
		name := strings.TrimPrefix(data, "cmd:")
		if _, ok := d.index[name]; ok {
			return "/" + name, true
		}
		return "", false
	}
	for _, action := range []string{"confirm", "cancel"} {
		prefix := "delete:" + action
		if data == prefix {
			return "/delete callback " + action + ":", true
		}
		if token, ok := strings.CutPrefix(data, prefix+":"); ok {
			if validMenuToken(token) {
				return "/delete callback " + action + ":" + token, true
			}
			return "", false
		}
	}
	if strings.HasPrefix(data, "persona:") {
		action := strings.TrimPrefix(data, "persona:")
		if action == "reset" || action == "custom" {
			return "/persona " + action, true
		}
		for _, preset := range personaPresets {
			if action == preset.name {
				return "/persona " + action, true
			}
		}
	}
	return "", false
}

func (d *Dispatcher) callbackEventOf(upd *models.Update) (Event, bool) {
	if upd == nil || upd.CallbackQuery == nil || upd.CallbackQuery.Message.Message == nil {
		return Event{}, false
	}
	text, ok := d.callbackText(strings.TrimSpace(upd.CallbackQuery.Data))
	if !ok {
		return Event{}, false
	}
	callback := upd.CallbackQuery
	message := callback.Message.Message
	ev := Event{
		UpdateID:          upd.ID,
		TelegramMessageID: int64(message.ID),
		ChatID:            message.Chat.ID,
		ChatType:          string(message.Chat.Type),
		ChatTitle:         chatTitle(message.Chat),
		Text:              text,
		MenuCallback:      true,
	}
	if callback.From.ID != 0 {
		ev.UserID = callback.From.ID
		ev.Username = callback.From.Username
	}
	return ev, true
}

func (d *Dispatcher) handleCallback(ctx context.Context, tg *tgbot.Bot, upd *models.Update) {
	if upd == nil || upd.CallbackQuery == nil {
		return
	}
	ev, ok := d.callbackEventOf(upd)
	if !ok {
		if tg != nil && upd.CallbackQuery.ID != "" {
			params := &tgbot.AnswerCallbackQueryParams{CallbackQueryID: upd.CallbackQuery.ID, Text: menuExpiredText, ShowAlert: true}
			if strings.HasPrefix(strings.TrimSpace(upd.CallbackQuery.Data), "delete:") {
				params.Text = "Это меню удаления истекло или уже использовано. Отправь /delete заново."
			}
			if _, err := tg.AnswerCallbackQuery(ctx, params); err != nil {
				d.deps.Logger.Error("answerCallbackQuery failed", "error", Sanitize(err, d.deps.Token))
			}
		}
		return
	}
	if tg != nil && upd.CallbackQuery.ID != "" {
		if _, err := tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: upd.CallbackQuery.ID}); err != nil {
			d.deps.Logger.Error("answerCallbackQuery failed", "error", Sanitize(err, d.deps.Token))
		}
	}
	d.noteChatUser(ev.ChatID, ev.UserID)
	if err := d.Dispatch(ctx, ev); err != nil {
		return
	}
}

func menuText(title string, note []string) string {
	if len(note) > 0 && strings.TrimSpace(note[0]) != "" {
		return note[0] + "\n\n" + title
	}
	return title
}

func (d *Dispatcher) showRootMenu(ctx context.Context, ev Event, note ...string) error {
	return d.renderMenu(ctx, ev, menuText("Главное меню. Выбери раздел:", note), d.menuMarkup())
}

func (d *Dispatcher) showHistoryMenu(ctx context.Context, ev Event, note ...string) error {
	return d.renderMenu(ctx, ev, menuText("История: просмотр, поиск, экспорт и удаление.", note), historyMenuMarkup())
}

func (d *Dispatcher) showStatusMenu(ctx context.Context, ev Event) error {
	text, err := d.statusText(ctx, ev)
	if err != nil {
		return err
	}
	return d.renderMenu(ctx, ev, text, statusMenuMarkup())
}

func (d *Dispatcher) showMoreMenu(ctx context.Context, ev Event, note ...string) error {
	rows := [][]models.InlineKeyboardButton{}
	rowID, role, err := store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	skipped, suppressed, err := store.ResumeSkip(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if rowID > 0 && role == store.RoleUser && (!suppressed || skipped != rowID) {
		rows = append(rows, menuRow(menuButton("Пропустить восстановление", "menu:skip")))
	}
	if suppressed {
		rows = append(rows, menuRow(menuButton("Возобновить восстановление", "menu:unskip")))
	}
	rows = append(rows, menuRow(menuButton("Напоминания", "menu:reminders")))
	rows = append(rows, menuRow(menuButton("Задача", "menu:task")))
	if d.deps.Cfg.MiniAppEnabled {
		button, ticketErr := d.miniAppButton(ev)
		if ticketErr != nil {
			return ticketErr
		}
		rows = append(rows, menuRow(button))
	}
	if d.deps.Cfg.TTSEnabled {
		rows = append(rows, menuRow(menuButton("Озвучить текст", "menu:tts"), menuButton("Последний ответ", "menu:tts:last")))
		speed, speedErr := d.ttsSpeed(ctx, ev.ChatID)
		if speedErr != nil {
			return speedErr
		}
		rows = append(rows, menuRow(menuButton("Скорость TTS: "+formatTTSSpeed(speed)+"×", "menu:tts:speed")))
	}
	rows = append(rows, menuRow(menuButton("Кто я", "menu:whoami")))
	if d.deps.Cfg.IsAdmin(ev.UserID) {
		rows = append(rows, menuRow(menuButton("Админ", "menu:admin")))
	}
	rows = append(rows, menuRow(backButton("menu:root")))
	return d.renderMenu(ctx, ev, menuText("Дополнительные команды:", note), models.InlineKeyboardMarkup{InlineKeyboard: rows})
}

func (d *Dispatcher) helpMenu(ctx context.Context, ev Event, action string) error {
	switch action {
	case "menu:root":
		return d.showRootMenu(ctx, ev)
	case "menu:help":
		return d.renderMenu(ctx, ev, strings.Join(d.helpLines(), "\n"), simpleBackMarkup("menu:root"))
	case "menu:settings":
		return d.renderMenu(ctx, ev, "Настройки текущего чата:", settingsMenuMarkup())
	case "menu:more":
		return d.showMoreMenu(ctx, ev)
	case "menu:ask":
		return d.beginPendingInput(ctx, ev, CommandAsk, "menu:root", "Отправь вопрос одним сообщением. Он будет обработан как /ask. Ввод действует 2 минуты.")
	case "menu:search":
		return d.beginPendingInput(ctx, ev, CommandSearch, "menu:history", "Отправь текст для поиска по истории. Ввод действует 2 минуты.")
	case "menu:tts":
		return d.synthesizeSpeech(ctx, ev, "")
	case "menu:tts:speed":
		return d.showTTSSpeedMenu(ctx, ev)
	default:
		if strings.HasPrefix(action, "menu:tts:speed:") {
			return d.ttsSpeedMenuControl(ctx, ev, action)
		}
		if token, ok := strings.CutPrefix(action, "menu:input:cancel:"); ok && validMenuToken(token) {
			return d.cancelPendingInputMenu(ctx, ev, token)
		}
		if strings.HasPrefix(action, "menu:network:") {
			return d.networkApprovalControl(ctx, ev, action)
		}
		return d.showRootMenu(ctx, ev, menuExpiredText)
	}
}

const pendingLinkSearch = "links_search"

type pendingInputKey struct {
	ChatID int64
	UserID int64
	Action string
}

type pendingInput struct {
	Token     string
	MessageID int64
	Origin    string
	ExpiresAt time.Time
}

type pendingInputSet struct {
	mu      sync.Mutex
	pending map[pendingInputKey]pendingInput
}

var dispatcherPendingInputs sync.Map

func pendingInputsFor(d *Dispatcher) *pendingInputSet {
	set, _ := dispatcherPendingInputs.LoadOrStore(d, &pendingInputSet{pending: map[pendingInputKey]pendingInput{}})
	return set.(*pendingInputSet)
}

func (d *Dispatcher) beginPendingInput(ctx context.Context, ev Event, action string, origin string, prompt string) error {
	token, err := newDeleteConfirmationToken()
	if err != nil {
		return err
	}
	pending := pendingInput{Token: token, MessageID: ev.TelegramMessageID, Origin: origin, ExpiresAt: time.Now().Add(pendingInputTTL)}
	set := pendingInputsFor(d)
	set.mu.Lock()
	now := time.Now()
	for key, existing := range set.pending {
		if !now.Before(existing.ExpiresAt) || key.ChatID == ev.ChatID && key.UserID == ev.UserID {
			delete(set.pending, key)
		}
	}
	set.pending[pendingInputKey{ChatID: ev.ChatID, UserID: ev.UserID, Action: action}] = pending
	set.mu.Unlock()
	markup := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton("Отмена", "menu:input:cancel:"+token)),
	}}
	if err := d.renderMenu(ctx, ev, prompt, markup); err != nil {
		set.mu.Lock()
		delete(set.pending, pendingInputKey{ChatID: ev.ChatID, UserID: ev.UserID, Action: action})
		set.mu.Unlock()
		return err
	}
	return nil
}

func (d *Dispatcher) pendingEvent(ev Event) (Event, bool) {
	if ev.Attachment != nil || ev.UnsupportedMedia || strings.TrimSpace(ev.Text) == "" {
		return ev, false
	}
	if _, _, isCommand := Normalize(ev.Text, d.deps.BotName); isCommand {
		return ev, false
	}
	set := pendingInputsFor(d)
	set.mu.Lock()
	defer set.mu.Unlock()
	now := time.Now()
	for key, pending := range set.pending {
		if key.ChatID != ev.ChatID || key.UserID != ev.UserID {
			continue
		}
		delete(set.pending, key)
		if !now.Before(pending.ExpiresAt) {
			return ev, false
		}
		text := strings.TrimSpace(ev.Text)
		if choice, ok := strings.CutPrefix(key.Action, pendingReminderActionPrefix); ok {
			ev.Text = "/reminders menu:create:" + choice + " " + text
			return ev, true
		}
		switch key.Action {
		case CommandAsk:
			ev.Text = "/ask " + text
		case CommandSearch:
			ev.Text = "/search " + text
		case pendingLinkSearch:
			ev.Text = "/links search " + text
		case pendingDocumentSearch:
			ev.Text = "/docs search " + text
		case CommandPersona:
			ev.Text = "/persona custom " + text
		case CommandTTS:
			ev.Text = "/tts " + text
		case CommandTask:
			ev.Text = "/task " + text
		default:
			return ev, false
		}
		return ev, true
	}
	return ev, false
}

func (d *Dispatcher) cancelPendingInputMenu(ctx context.Context, ev Event, token string) error {
	set := pendingInputsFor(d)
	set.mu.Lock()
	for key, pending := range set.pending {
		if pending.Token != token {
			continue
		}
		if !time.Now().Before(pending.ExpiresAt) {
			delete(set.pending, key)
			set.mu.Unlock()
			return d.showRootMenu(ctx, ev, menuExpiredText)
		}
		if key.ChatID != ev.ChatID || key.UserID != ev.UserID {
			set.mu.Unlock()
			return d.renderMenu(ctx, ev, "Это меню ввода принадлежит другому пользователю или чату.", simpleBackMarkup("menu:root"))
		}
		if pending.MessageID != 0 && pending.MessageID != ev.TelegramMessageID {
			set.mu.Unlock()
			return d.renderMenu(ctx, ev, "Это меню ввода недействительно для этого сообщения.", simpleBackMarkup("menu:root"))
		}
		delete(set.pending, key)
		set.mu.Unlock()
		switch pending.Origin {
		case "menu:history":
			return d.showHistoryMenu(ctx, ev, "Ввод отменён.")
		case "menu:links":
			return d.showLinksMenu(ctx, ev, "Поиск отменён.")
		case "menu:docs":
			return d.showDocumentsMenu(ctx, ev, "Поиск отменён.")
		case "menu:persona":
			return d.showPersonaMenu(ctx, ev, "Ввод отменён.")
		case "menu:reminders":
			return d.showReminderMenu(ctx, ev, "Ввод напоминания отменён.")
		case "menu:task":
			return d.showTaskMenu(ctx, ev, "Ввод новой задачи отменён.")
		case "menu:more":
			return d.showMoreMenu(ctx, ev)
		default:
			return d.showRootMenu(ctx, ev, "Ввод отменён.")
		}
	}
	set.mu.Unlock()
	return d.showRootMenu(ctx, ev, menuExpiredText)
}
