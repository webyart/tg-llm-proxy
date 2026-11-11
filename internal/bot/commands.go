// registry a slice so that "v2 расширяется добавлением одной записи (без правок диспетчера)": adding a
// command here is enough — Dispatch resolves names from the index, /help lists it and setMyCommands
package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"bot/internal/i18n"
	"bot/internal/model"
	"bot/internal/store"
)

const (
	CommandStart     = "start"
	CommandHelp      = "help"
	CommandPing      = "ping"
	CommandStatus    = "status"
	CommandModel     = "model"
	CommandStream    = "stream"
	CommandMiniApp   = "miniapp"
	CommandPersona   = "persona"
	CommandLanguage  = "language"
	CommandAsk       = "ask"
	CommandReset     = "reset"
	CommandSkip      = "skip"
	CommandUnskip    = "unskip"
	CommandHistory   = "history"
	CommandStats     = "stats"
	CommandUsage     = "usage"
	CommandWhoAmI    = "whoami"
	CommandAdmin     = "admin"
	CommandLimits    = "limits"
	CommandExport    = "export"
	CommandSearch    = "search"
	CommandDelete    = "delete"
	CommandDocs      = "docs"
	CommandLinks     = "links"
	CommandRemind    = "remind"
	CommandReminders = "reminders"
	CommandTask      = "task"
	CommandTTS       = "tts"
	CommandFeedback  = "feedback"
	// CommandFeedbackAlias — the short spelling of the same handler.
	CommandFeedbackAlias = "fb"
)

// The Mini App still renders these Russian texts directly (miniapp_chat.go); the Telegram command
// surface uses the language-aware catalog in internal/i18n instead.
const (
	msgTimeoutFormat = "Модель не ответила за %d с (%s), попробуй ещё раз."
	msgModelError    = "Модель не ответила: %s. Запрос записан в events(kind='model_error')."
	msgEmptyAnswer   = "Модель вернула пустой ответ — это ошибка, а не ответ. Попробуй ещё раз."
	msgNoRoom        = "Все слоты модели заняты. Подожди немного."
)

// auditBudget — how long the audit of a failed call may take once the call itself gave up. The audit of a
const auditBudget = 3 * time.Second

const typingHeartbeatInterval = 4 * time.Second

type typingSender interface {
	SendTyping(context.Context, int64) error
}

func (d *Dispatcher) startTypingHeartbeat(ctx context.Context, chatID int64) func() {
	return d.startTypingHeartbeatWithInterval(ctx, chatID, typingHeartbeatInterval)
}

func (d *Dispatcher) startTypingHeartbeatWithInterval(ctx context.Context, chatID int64, interval time.Duration) func() {
	sender, ok := d.deps.Replier.(typingSender)
	if !ok || ctx.Err() != nil {
		return func() {}
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	if err := sender.SendTyping(heartbeatCtx, chatID); err != nil {
		cancel()
		d.deps.Logger.Debug("typing feedback is unavailable", "chat_id", chatID, "error", Sanitize(err, d.deps.Token))
		return func() {}
	}
	if interval <= 0 {
		interval = typingHeartbeatInterval
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if err := sender.SendTyping(heartbeatCtx, chatID); err != nil {
					d.deps.Logger.Debug("typing heartbeat stopped", "chat_id", chatID, "error", Sanitize(err, d.deps.Token))
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

func (d *Dispatcher) registry() []Command {
	return []Command{
		{Name: CommandStart, Description: "регистрация чата и список команд", AlwaysAllowed: true, Handler: d.start},
		{Name: CommandHelp, Description: "справка по командам", Handler: d.help},
		{Name: CommandPing, Description: "health бота: pong + uptime", Handler: d.ping},
		{Name: CommandStatus, Description: "стриминг, модель, uptime и ожидание ответа", Handler: d.status},
		{Name: CommandModel, Description: "модель, endpoint, /model check, select и reset", Handler: d.modelInfo},
		{Name: CommandStream, Description: "стриминг для этого чата: on, off, reset", Handler: d.streamControl},
		{Name: CommandMiniApp, Description: "доставка ответа Mini App в Telegram: on, off, reset", Handler: d.miniappControl},
		{Name: CommandPersona, Description: "роль модели для этого чата: пресеты, custom, reset", Handler: d.personaControl},
		{Name: CommandLanguage, Description: "язык интерфейса бота: ru, en, reset", Handler: d.languageControl},
		{Name: CommandAsk, Description: "вопрос модели", NeedsArgs: true, UsesModel: true, Handler: d.ask},
		{Name: CommandReset, Description: "очистить контекст чата", Handler: d.reset},
		{Name: CommandSkip, Description: "не восстанавливать текущий unanswered-вопрос при старте", Handler: d.skip},
		{Name: CommandUnskip, Description: "снова разрешить recovery ранее пропущенного вопроса", Handler: d.unskip},
		{Name: CommandHistory, Description: "последние N сообщений (по умолчанию 5)", Handler: d.history},
		{Name: CommandDocs, Description: "локальная RAG-база документов чата", Handler: d.documents},
		{Name: CommandLinks, Description: "ссылки на потом: add, search, summary, delete", Handler: d.links},
		{Name: CommandRemind, Description: "напоминание: через N минут/часов/дней или каждый день HH:MM", NeedsArgs: true, Handler: d.remind},
		{Name: CommandReminders, Description: "активные напоминания и отмена по id", Handler: d.reminders},
		{Name: CommandTask, Description: "безопасная задача из 1–3 шагов: создать, статус, cancel", Handler: d.task},
		{Name: CommandTTS, Description: "озвучить текст через opt-in TTS", NeedsArgs: true, Handler: d.synthesizeSpeech},
		{Name: CommandExport, Description: "экспорт истории: md или json", Handler: d.exportHistory},
		{Name: CommandSearch, Description: "поиск по истории: /search <текст>", NeedsArgs: true, Handler: d.searchHistory},
		{Name: CommandDelete, Description: "удаление истории с подтверждением", Handler: d.deleteHistory},
		{Name: CommandStats, Description: "счётчики чата: сообщения, токены, ошибки", Handler: d.stats},
		{Name: CommandFeedback, Description: "оценка последнего ответа: up или down", Handler: d.feedback},
		{Name: CommandFeedbackAlias, Description: "то же, что /feedback: up или down", Handler: d.feedback},
		{Name: CommandUsage, Description: "использование модели и инструментов за UTC-сутки", Handler: d.usage},
		{Name: CommandWhoAmI, Description: "user_id и chat_id для TELEGRAM_ALLOWED_USERS", AlwaysAllowed: true, Handler: d.whoami},
		{Name: CommandAdmin, Description: "агрегированные метрики для администратора", Handler: d.admin},
		{Name: CommandLimits, Description: "лимиты чата: показать, model N, network N, minute N, tools, reset", Handler: d.limitsControl},
	}
}

// /start (a group, for example) is registered without losing its title.
func (d *Dispatcher) start(ctx context.Context, ev Event, arg string) error {
	var err = store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle)
	if err != nil {
		return err
	}
	lang := d.chatLanguage(ctx, ev.ChatID)
	var lines = []string{i18n.T(lang, "start.greeting")}
	lines = append(lines, d.helpLinesFor(lang)...)
	if d.deps.Cfg.DenyByDefault {
		lines = append(lines, refuseClosed(ev.UserID, lang))
	}
	return d.sendMenu(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

func (d *Dispatcher) help(ctx context.Context, ev Event, arg string) error {
	if strings.HasPrefix(strings.TrimSpace(arg), "menu:") {
		return d.helpMenu(ctx, ev, strings.TrimSpace(arg))
	}
	return d.sendMenu(ctx, ev.ChatID, strings.Join(d.helpLinesFor(d.chatLanguage(ctx, ev.ChatID)), "\n"))
}

// helpLines keeps the historical Russian rendering for callers without a chat language (the menu
// layer of menu.go). Chat-facing /help and /start use helpLinesFor with the resolved language.
func (d *Dispatcher) helpLines() []string {
	return d.helpLinesFor(i18n.Default)
}

// helpLinesFor renders the registry in lang. The command registry keeps one language-independent
// Description for setMyCommands (run.go) — this is the parallel translation map for /help.
func (d *Dispatcher) helpLinesFor(lang string) []string {
	var lines = []string{}
	var i = 0
	for i < len(d.commands) {
		var cmd = d.commands[i]
		var usage = "/" + cmd.Name
		switch {
		case cmd.Name == CommandRemind:
			usage += i18n.T(lang, "help.remind_suffix")
		case cmd.Name == CommandLanguage:
			usage += i18n.T(lang, "help.language_suffix")
		case cmd.NeedsArgs:
			usage += i18n.T(lang, "help.args_suffix")
		}
		var description = cmd.Description
		if translated, ok := i18n.Lookup(lang, "command."+cmd.Name+".description"); ok {
			description = translated
		}
		lines = append(lines, i18n.T(lang, "help.line", usage, description))
		if cmd.Name == CommandRemind {
			lines = append(lines, i18n.T(lang, "help.remind_example"))
		}
		i++
	}
	lines = append(lines, i18n.T(lang, "help.attachments"))
	return lines
}

func (d *Dispatcher) ping(ctx context.Context, ev Event, arg string) error {
	var up = time.Since(d.created).Round(time.Second)
	lang := d.chatLanguage(ctx, ev.ChatID)
	return d.menuReply(ctx, ev, i18n.T(lang, "ping.pong", up), refreshBackMarkupFor(lang, "menu:ping", "menu:status"))
}

// status — operational diagnostics for an allowed chat without a model call. LastVisibleMessage uses the same
func (d *Dispatcher) status(ctx context.Context, ev Event, arg string) error {
	if strings.TrimSpace(arg) == "menu" {
		return d.showStatusMenu(ctx, ev)
	}
	text, err := d.statusText(ctx, ev)
	if err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, text)
}

func (d *Dispatcher) statusText(ctx context.Context, ev Event) (string, error) {
	lang := d.chatLanguage(ctx, ev.ChatID)
	var rowID, role, err = store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return "", err
	}
	streamEnabled, streamOverride, streamErr := d.streamingEnabled(ctx, ev.ChatID)
	if streamErr != nil {
		return "", streamErr
	}
	var streaming = i18n.T(lang, "status.streaming_off")
	if streamEnabled {
		streaming = i18n.T(lang, "status.streaming_on",
			d.deps.Cfg.StreamEditIntervalMS, d.deps.Cfg.StreamIdleSeconds, d.deps.Cfg.StreamTotalSeconds)
	}
	if streamOverride {
		streaming += i18n.T(lang, "status.override")
	}
	var pending = i18n.T(lang, "status.pending_no")
	if rowID > 0 && role == store.RoleUser {
		pending = i18n.T(lang, "status.pending_yes", rowID)
		var skipped, suppress, skipErr = store.ResumeSkip(ctx, d.deps.DB, ev.ChatID)
		if skipErr != nil {
			return "", skipErr
		}
		if suppress && skipped == rowID {
			pending += i18n.T(lang, "status.pending_skipped")
		}
	}
	var commit = d.deps.BuildCommit
	if commit == "" {
		commit = "dev"
	}
	var selected, modelErr = d.modelName(ctx, ev.ChatID)
	if modelErr != nil {
		return "", modelErr
	}
	_, persona, personaOverride, personaErr := d.effectivePersona(ctx, ev.ChatID)
	if personaErr != nil {
		return "", personaErr
	}
	if personaOverride {
		persona += i18n.T(lang, "status.override")
	}
	var lines = []string{
		i18n.T(lang, "status.title"),
		i18n.T(lang, "status.build", commit),
		i18n.T(lang, "status.uptime", time.Since(d.created).Round(time.Second).String()),
		i18n.T(lang, "status.model", selected),
		i18n.T(lang, "status.persona", persona),
		i18n.T(lang, "status.streaming", streaming),
		i18n.T(lang, "status.pending", pending),
	}
	return strings.Join(lines, "\n"), nil
}

// miniappControl — /miniapp: the per-chat override of MINIAPP_TELEGRAM_FALLBACK. It never calls the model and
// keeps no menu state: the same switch is edited from the Mini App Settings, and this command must always report
// the value that the delivery path would actually use.
func (d *Dispatcher) miniappControl(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	action := strings.ToLower(strings.TrimSpace(arg))
	switch action {
	case "on", "off":
		enabled := action == "on"
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatMiniAppFallback(ctx, d.deps.DB, ev.ChatID, enabled); err != nil {
			return err
		}
		state := i18n.T(lang, "miniapp.state_off")
		if enabled {
			state = i18n.T(lang, "miniapp.state_on")
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "miniapp.saved", state))
	case "reset":
		if err := store.ResetChatMiniAppFallback(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
	case "":
	default:
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "miniapp.usage"))
	}

	enabled := d.deps.Cfg.MiniAppTelegramFallback
	source := i18n.T(lang, "miniapp.source_global")
	if d.deps.DB != nil {
		override, found, err := store.ChatMiniAppFallback(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		if found {
			enabled = override
			source = i18n.T(lang, "miniapp.source_chat")
		}
	}
	state := i18n.T(lang, "miniapp.state_off")
	if enabled {
		state = i18n.T(lang, "miniapp.state_on")
	}
	prefix := ""
	if action == "reset" {
		prefix = i18n.T(lang, "miniapp.reset_prefix")
	}
	return d.reply(ctx, ev.ChatID, prefix+i18n.T(lang, "miniapp.state", state, source))
}

// skip — suppress startup recovery of only the newest visible unanswered question. The row and its history stay
// untouched, and a newer question has another id, so it does not inherit the suppression.
func (d *Dispatcher) skip(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	var rowID, role, err = store.LastVisibleMessage(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if rowID == 0 || role != store.RoleUser {
		text := i18n.T(lang, "skip.none")
		if ev.MenuCallback {
			return d.showMoreMenu(ctx, ev, text)
		}
		return d.reply(ctx, ev.ChatID, text)
	}
	if err := store.SetResumeSkip(ctx, d.deps.DB, ev.ChatID, rowID); err != nil {
		return err
	}
	text := i18n.T(lang, "skip.done", rowID)
	if ev.MenuCallback {
		return d.showMoreMenu(ctx, ev, text)
	}
	return d.reply(ctx, ev.ChatID, text)
}

// unskip removes this chat's suppression without making an immediate model call. Startup still applies its
// newest-visible-question rule, so an older row does not jump ahead of a newer conversation.
func (d *Dispatcher) unskip(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	var questionID, found, err = store.ResumeSkip(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	if !found {
		text := i18n.T(lang, "unskip.none")
		if ev.MenuCallback {
			return d.showMoreMenu(ctx, ev, text)
		}
		return d.reply(ctx, ev.ChatID, text)
	}
	if err := store.ClearResumeSkip(ctx, d.deps.DB, ev.ChatID); err != nil {
		return err
	}
	text := i18n.T(lang, "unskip.done", questionID)
	if ev.MenuCallback {
		return d.showMoreMenu(ctx, ev, text)
	}
	return d.reply(ctx, ev.ChatID, text)
}

// learns the id to put into TELEGRAM_ALLOWED_USERS.
func (d *Dispatcher) whoami(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	var lines = []string{fmt.Sprintf("user_id: %d", ev.UserID), fmt.Sprintf("chat_id: %d", ev.ChatID)}
	if ev.ChatType != "" {
		lines = append(lines, "chat_type: "+ev.ChatType)
	}
	if ev.ChatTitle != "" {
		lines = append(lines, "chat_title: "+ev.ChatTitle)
	}
	if ev.Username != "" {
		lines = append(lines, "username: @"+ev.Username)
	}
	if d.deps.Cfg.Allows(ev.UserID) {
		lines = append(lines, i18n.T(lang, "whoami.allowed"))
	} else if d.deps.Cfg.DenyByDefault {
		lines = append(lines, i18n.T(lang, "whoami.closed", ev.UserID))
	} else {
		lines = append(lines, i18n.T(lang, "whoami.denied", ev.UserID))
	}
	return d.menuReply(ctx, ev, strings.Join(lines, "\n"), simpleBackMarkupFor(lang, "menu:more"))
}

const HistoryDefault = 5

const HistoryMax = 50

// will find when they open.env.
const AllowlistHint = "TELEGRAM_ALLOWED_USERS"

func refuseClosed(userID int64, langs ...string) string {
	return i18n.T(refusalLanguage(langs), "refuse.closed", userID, AllowlistHint)
}

func refuseNotListed(userID int64, langs ...string) string {
	return i18n.T(refusalLanguage(langs), "refuse.not_listed", userID, AllowlistHint)
}

// refusalLanguage keeps the historical Russian default for callers without a chat language while the
// command surface passes the resolved one. The signature stays variadic so the group-mode dispatcher and
// middleware call sites do not change.
func refusalLanguage(langs []string) string {
	if len(langs) > 0 {
		return langs[0]
	}
	return i18n.Default
}

// modelName resolves a per-chat override or STRATA_MODEL when the chat has none.
func (d *Dispatcher) modelName(ctx context.Context, chatID int64) (string, error) {
	selected, found, err := store.ChatModel(ctx, d.deps.DB, chatID)
	if err != nil {
		return "", err
	}
	if found {
		return selected, nil
	}
	return d.deps.Cfg.StrataModel, nil
}

// modelDisplayName names the provider of a selection. The primary endpoint is "strata"; its prefix is shown
// only when MODEL_PROVIDERS is configured, so a bot without named providers keeps the old "/model" output.
func (d *Dispatcher) modelDisplayName(selected string) string {
	if selected == "" || strings.Contains(selected, ":") || len(d.deps.Cfg.ModelProviders) == 0 {
		return selected
	}
	return "strata:" + selected
}

// providerModelAllowed checks a provider-prefixed selection against that provider's configured list. A plain id
// is validated by the catalogue of the default endpoint and needs nothing here; a "strata:" id names the
// default endpoint and accepts any model id, exactly like a plain one.
func (d *Dispatcher) providerModelAllowed(requested string) bool {
	provider, selected, prefixed := strings.Cut(requested, ":")
	if !prefixed || provider == "strata" {
		return true
	}
	for _, configured := range d.deps.Cfg.ModelProviders {
		if configured.Name != provider {
			continue
		}
		for _, model := range configured.Models {
			if strings.TrimSpace(model) == selected {
				return true
			}
		}
		return false
	}
	return false
}

// modelEndpoint names the endpoint that actually serves a selection: the provider's own base for a prefixed id,
// the primary base otherwise. Without MODEL_PROVIDERS this always returns the primary endpoint.
func (d *Dispatcher) modelEndpoint(selected string) string {
	provider, _, prefixed := strings.Cut(selected, ":")
	if !prefixed || provider == "strata" {
		return d.deps.Cfg.StrataBase
	}
	for _, configured := range d.deps.Cfg.ModelProviders {
		if configured.Name == provider {
			return configured.BaseURL
		}
	}
	return d.deps.Cfg.StrataBase
}

// modelInfo — model/endpoint diagnostics and the per-chat select/reset controls.
func (d *Dispatcher) modelInfo(ctx context.Context, ev Event, arg string) error {
	var endpoint = d.deps.Cfg.StrataBase
	var action = strings.TrimSpace(arg)
	lang := d.chatLanguage(ctx, ev.ChatID)
	if strings.HasPrefix(action, "menu") {
		return d.modelMenuControl(ctx, ev, action)
	}
	var control = action
	var requested = ""
	if fields := strings.Fields(action); len(fields) > 0 {
		control = fields[0]
		requested = strings.TrimSpace(action[len(control):])
	}
	if strings.EqualFold(control, "reset") && requested == "" {
		if err := store.ClearChatModel(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "model.reset", d.modelDisplayName(d.deps.Cfg.StrataModel)))
	}
	if strings.EqualFold(control, "check") && requested == "" || strings.EqualFold(control, "select") {
		var names, err = d.deps.Model.Models(ctx)
		if err != nil {
			d.logEvent(ctx, ev.UserID, store.EventModelError, "models:"+Sanitize(err, d.deps.Token))
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "model.check_fail", Sanitize(err, d.deps.Token)))
		}
		if len(names) == 0 {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "model.empty_list", endpoint))
		}
		if strings.EqualFold(control, "check") {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "model.check_result", endpoint, len(names), strings.Join(names, ", ")))
		}
		if requested == "" {
			var current, currentErr = d.modelName(ctx, ev.ChatID)
			if currentErr != nil {
				return currentErr
			}
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "model.select_current",
				d.modelDisplayName(current), len(names), strings.Join(names, ", "), d.modelDisplayName(d.deps.Cfg.StrataModel)))
		}
		var allowed = false
		for _, name := range names {
			if requested == name {
				allowed = true
				break
			}
		}
		// The aggregated catalogue already lists exactly the configured ids; this second check proves the model
		// is in ITS provider's list even if the default endpoint happens to advertise a colliding "name:model".
		if allowed && !d.providerModelAllowed(requested) {
			allowed = false
		}
		if !allowed {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "model.not_found", requested, strings.Join(names, ", ")))
		}
		if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
			return err
		}
		if err := store.SetChatModel(ctx, d.deps.DB, ev.ChatID, requested); err != nil {
			return err
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "model.selected", d.modelDisplayName(requested), d.modelDisplayName(d.deps.Cfg.StrataModel)))
	}
	var selected, err = d.modelName(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	var note = ""
	if selected != d.deps.Cfg.StrataModel {
		note = i18n.T(lang, "model.override_note", d.modelDisplayName(d.deps.Cfg.StrataModel))
	}
	return d.reply(ctx, ev.ChatID, i18n.T(lang, "model.info", d.modelDisplayName(selected), note, d.modelEndpoint(selected)))
}

func (d *Dispatcher) modelMenuControl(ctx context.Context, ev Event, action string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	switch {
	case action == "menu" || action == "menu:check":
		if action == "menu:check" {
			return d.showModelMenu(ctx, ev, i18n.T(lang, "model.menu_checking"))
		}
		return d.showModelMenu(ctx, ev)
	case action == "menu:reset":
		if err := store.ClearChatModel(ctx, d.deps.DB, ev.ChatID); err != nil {
			return err
		}
		return d.showModelMenu(ctx, ev, i18n.T(lang, "model.menu_reset", d.modelDisplayName(d.deps.Cfg.StrataModel)))
	}
	value, ok := strings.CutPrefix(action, "menu:set:")
	index, err := strconv.Atoi(value)
	if !ok || err != nil || index < 0 || index >= menuModelChoiceLimit {
		return d.showModelMenu(ctx, ev, menuExpiredFor(lang))
	}
	names, err := d.deps.Model.Models(ctx)
	if err != nil {
		d.logEvent(ctx, ev.UserID, store.EventModelError, "models:"+Sanitize(err, d.deps.Token))
		return d.showModelMenu(ctx, ev, i18n.T(lang, "model.check_fail", Sanitize(err, d.deps.Token)))
	}
	if index >= len(names) {
		return d.showModelMenu(ctx, ev, menuExpiredFor(lang))
	}
	selected := names[index]
	if selected == "" {
		return d.showModelMenu(ctx, ev, menuExpiredFor(lang))
	}
	if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
		return err
	}
	if err := store.SetChatModel(ctx, d.deps.DB, ev.ChatID, selected); err != nil {
		return err
	}
	return d.showModelMenu(ctx, ev, i18n.T(lang, "model.menu_selected", d.modelDisplayName(selected)))
}

func (d *Dispatcher) showModelMenu(ctx context.Context, ev Event, note ...string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	selected, err := d.modelName(ctx, ev.ChatID)
	if err != nil {
		return err
	}
	text := menuText(i18n.T(lang, "model.menu_current", d.modelDisplayName(selected), d.modelEndpoint(selected)), note)
	names, modelsErr := d.deps.Model.Models(ctx)
	if modelsErr != nil {
		d.logEvent(ctx, ev.UserID, store.EventModelError, "models:"+Sanitize(modelsErr, d.deps.Token))
		text += "\n" + i18n.T(lang, "model.check_fail", Sanitize(modelsErr, d.deps.Token))
		names = nil
	} else if len(note) > 0 && note[0] == i18n.T(lang, "model.menu_checking") {
		text += i18n.T(lang, "model.endpoint_ok", len(names))
	}
	if len(names) > menuModelChoiceLimit {
		text += i18n.T(lang, "model.first_n", menuModelChoiceLimit, len(names))
	}
	return d.renderMenu(ctx, ev, text, modelMenuMarkupFor(lang, names, selected))
}

func (d *Dispatcher) downloadEventAttachments(ctx context.Context, ev Event) ([]DownloadedAttachment, error) {
	if ev.Attachment == nil {
		return nil, nil
	}
	if d.deps.Attachments == nil {
		return nil, ErrAttachmentUnavailable
	}
	attachments := []Attachment{*ev.Attachment}
	attachments = append(attachments, ev.AdditionalAttachments...)
	if len(ev.AdditionalAttachments) > 0 && ev.Attachment.Kind != AttachmentPhoto {
		return nil, ErrAttachmentType
	}
	limit := d.deps.Cfg.MediaMaxBytes
	if limit <= 0 {
		limit = 10 << 20
	}
	files := make([]DownloadedAttachment, 0, len(attachments))
	var total int64
	for i, attachment := range attachments {
		if i > 0 && attachment.Kind != AttachmentPhoto {
			return nil, ErrAttachmentType
		}
		if attachment.Size > limit || attachment.Size > limit-total {
			return nil, ErrAttachmentTooLarge
		}
		file, err := d.deps.Attachments.Download(ctx, attachment)
		if err != nil {
			return nil, err
		}
		size := int64(len(file.Data))
		if size > limit || size > limit-total {
			return nil, ErrAttachmentTooLarge
		}
		total += size
		files = append(files, file)
	}
	return files, nil
}

func speechFormat(attachment Attachment) string {
	mime := strings.ToLower(strings.TrimSpace(strings.Split(attachment.MimeType, ";")[0]))
	switch mime {
	case "audio/ogg", "application/ogg", "audio/opus":
		return "ogg"
	case "audio/mpeg", "audio/mp3", "audio/mpga":
		return "mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/mp4", "video/mp4", "audio/x-m4a", "audio/m4a", "video/quicktime":
		return "mp4"
	case "audio/webm", "video/webm":
		return "webm"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/aac":
		return "aac"
	}
	if mime != "" && mime != "application/octet-stream" {
		return "unsupported"
	}
	switch strings.ToLower(path.Ext(attachment.FileName)) {
	case ".ogg", ".oga", ".opus":
		return "ogg"
	case ".mp3", ".mpeg", ".mpga":
		return "mp3"
	case ".wav":
		return "wav"
	case ".m4a", ".mp4", ".mov":
		return "mp4"
	case ".webm":
		return "webm"
	case ".flac":
		return "flac"
	case ".aac":
		return "aac"
	}
	switch attachment.Kind {
	case AttachmentVoice:
		return "ogg"
	case "video_note", "video-note":
		return "mp4"
	default:
		return ""
	}
}

func detectSpeechFormat(data []byte) string {
	for _, format := range []string{"ogg", "wav", "mp4", "webm", "flac", "aac", "mp3"} {
		if speechSignatureMatches(format, data) {
			return format
		}
	}
	return ""
}

func speechSignatureMatches(format string, data []byte) bool {
	switch format {
	case "ogg":
		return bytes.HasPrefix(data, []byte("OggS"))
	case "mp3":
		return bytes.HasPrefix(data, []byte("ID3")) || len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0
	case "wav":
		return len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && string(data[8:12]) == "WAVE"
	case "mp4":
		return len(data) >= 12 && string(data[4:8]) == "ftyp"
	case "webm":
		return bytes.HasPrefix(data, []byte{0x1a, 0x45, 0xdf, 0xa3})
	case "flac":
		return bytes.HasPrefix(data, []byte("fLaC"))
	case "aac":
		return len(data) >= 2 && data[0] == 0xff && data[1]&0xf6 == 0xf0
	default:
		return false
	}
}

func validSpeechAttachment(attachment Attachment, data []byte) bool {
	format := speechFormat(attachment)
	if format == "unsupported" {
		return false
	}
	if format == "" {
		format = detectSpeechFormat(data)
	}
	return speechSignatureMatches(format, data)
}

func speechRequestMetadata(attachment Attachment, file DownloadedAttachment) (string, string) {
	format := speechFormat(attachment)
	if format == "" || format == "unsupported" {
		format = detectSpeechFormat(file.Data)
	}
	name := strings.TrimSpace(attachment.FileName)
	if name == "" {
		extension := map[string]string{"ogg": ".ogg", "mp3": ".mp3", "wav": ".wav", "mp4": ".mp4", "webm": ".webm", "flac": ".flac", "aac": ".aac"}[format]
		switch attachment.Kind {
		case AttachmentVoice:
			name = "voice" + extension
		case "video_note", "video-note":
			name = "video-note" + extension
		default:
			name = "audio" + extension
		}
	}
	mime := strings.TrimSpace(attachment.MimeType)
	if mime == "" || mime == "application/octet-stream" {
		mime = strings.TrimSpace(file.MIME)
	}
	if mime == "" {
		mime = map[string]string{"ogg": "audio/ogg", "mp3": "audio/mpeg", "wav": "audio/wav", "mp4": "audio/mp4", "webm": "audio/webm", "flac": "audio/flac", "aac": "audio/aac"}[format]
		if attachment.Kind == "video_note" || attachment.Kind == "video-note" {
			mime = "video/mp4"
		}
	}
	return safeAttachmentName(name), mime
}

// responsibility: exactly one assistant row, and only after the final successful Response.
func (d *Dispatcher) ask(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	if ev.UnsupportedMedia {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "ask.unsupported_media"))
	}
	if ev.Recovery && isStoredAttachment(ev.Text) && ev.Attachment == nil {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "ask.attachment_unavailable"))
	}
	var text = strings.TrimSpace(arg)
	originalRequest := text
	if ev.Attachment != nil {
		text = attachmentPrompt(ev)
	}
	if text == "" {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "ask.needs_text"))
	}

	streamer, sender, canStream, streamErr := d.streamingPossible(ctx, ev.ChatID)
	if streamErr != nil {
		return streamErr
	}
	var replyBudget = d.deps.Cfg.ReplyDeadlineSeconds
	if canStream {
		// The stream has its own model budget. The ordinary reply budget remains around it for the context read,
		// SQLite write and Telegram delivery; a shorter outer context must not cut STREAM_TOTAL_DEADLINE_SECONDS.
		replyBudget += d.deps.Cfg.StreamTotalSeconds
	}
	var replyCtx, cancel = context.WithTimeout(ctx, time.Duration(replyBudget)*time.Second)
	defer cancel()
	if !canStream {
		stopTyping := d.startTypingHeartbeat(replyCtx, ev.ChatID)
		defer stopTyping()
	}

	downloaded, downloadErr := d.downloadEventAttachments(replyCtx, ev)
	if downloadErr != nil {
		if errors.Is(downloadErr, context.Canceled) {
			return downloadErr
		}
		noticeCtx, stopNotice := context.WithTimeout(context.WithoutCancel(ctx), auditBudget)
		defer stopNotice()
		d.logEvent(noticeCtx, ev.UserID, store.EventModelError, "attachment:"+attachmentErrorKind(downloadErr))
		return d.reply(noticeCtx, ev.ChatID, attachmentUserErrorFor(lang, downloadErr))
	}

	if isSpeechAttachment(ev.Attachment) {
		if !validSpeechAttachment(*ev.Attachment, downloaded[0].Data) {
			return d.reply(ctx, ev.ChatID, attachmentUserErrorFor(lang, ErrAttachmentType))
		}
		transcriber, ok := d.deps.Model.(model.Transcriber)
		if !ok {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "ask.transcription_unavailable"))
		}
		name, mime := speechRequestMetadata(*ev.Attachment, downloaded[0])
		transcript, transcribeErr := transcriber.Transcribe(replyCtx, model.TranscriptionRequest{
			Model: d.deps.Cfg.TranscriptionModel, FileName: name, MIME: mime, Data: downloaded[0].Data,
		})
		if transcribeErr != nil {
			if errors.Is(transcribeErr, context.Canceled) {
				return transcribeErr
			}
			noticeCtx, stopNotice := context.WithTimeout(context.WithoutCancel(ctx), auditBudget)
			defer stopNotice()
			d.deps.Logger.Error("speech transcription failed", "chat_id", ev.ChatID, "user_id", ev.UserID, "error", Sanitize(transcribeErr, d.deps.Token))
			d.logEvent(noticeCtx, ev.UserID, store.EventModelError, "transcription:"+attachmentErrorKind(transcribeErr))
			return d.reply(noticeCtx, ev.ChatID, i18n.T(lang, "ask.transcription_failed"))
		}
		text = voiceModelPrompt(text, transcript)
		stored := speechStoredMarker(ev.Attachment.Kind) + "\n" + text
		if err := store.ReplacePendingUserContent(replyCtx, d.deps.DB, ev.ChatID, ev.TelegramMessageID, stored); err != nil {
			return err
		}
	}

	documentTruncated := false
	var ragSources []string
	var messages, err = d.BuildMessages(replyCtx, ev.ChatID)
	if err != nil {
		return err
	}
	messages, ragSources, err = d.addRAGContext(replyCtx, ev.ChatID, ev.UserID, text, messages)
	if err != nil {
		return err
	}
	if len(downloaded) > 0 {
		if len(messages) == 0 || messages[len(messages)-1].Role != model.RoleUser {
			return errors.New("the stored attachment question is not the newest model message")
		}
		last := &messages[len(messages)-1]
		last.Content = text
		switch ev.Attachment.Kind {
		case AttachmentPhoto:
			last.ImageURLs = make([]string, 0, len(downloaded))
			for _, file := range downloaded {
				var imageURL string
				imageURL, err = imageDataURI(file)
				if err != nil {
					break
				}
				last.ImageURLs = append(last.ImageURLs, imageURL)
			}
			if err == nil {
				last.ImageURL = last.ImageURLs[0]
			}
		case AttachmentDocument:
			mime := strings.TrimSpace(ev.Attachment.MimeType)
			if mime == "" {
				mime = downloaded[0].MIME
			}
			documentText, extractErr := ExtractDocumentText(ev.Attachment.FileName, mime, downloaded[0].Data)
			if extractErr != nil {
				err = extractErr
			} else {
				name := safeAttachmentName(ev.Attachment.FileName)
				documentTruncated = Runes(text+"\n\nДокумент "+name+":\n"+documentText) > int(d.deps.Cfg.MaxContextChars)
				last.Content = documentModelPrompt(text, name, documentText, int(d.deps.Cfg.MaxContextChars))
			}
		default:
			if isSpeechAttachment(ev.Attachment) {
				last.Content = text
			} else {
				err = ErrAttachmentType
			}
		}
		if err != nil {
			return d.reply(ctx, ev.ChatID, attachmentUserErrorFor(lang, err))
		}
	}
	var selectedModel, modelErr = d.modelName(replyCtx, ev.ChatID)
	if modelErr != nil {
		return modelErr
	}

	// (a client that can stream, a sender that can edit) the answer arrives as ONE message rewritten as the model writes
	// it. The two paths return the same Response and the same invariant holds underneath: exactly one assistant
	var session *streamSession
	var resp model.Response
	if canStream {
		session = d.newStreamSession(replyCtx, ev.ChatID, sender)
	}
	toolCtx := withToolLoopMetadata(replyCtx, toolLoopMetadata{
		event: ev, originalRequest: originalRequest, selectedModel: selectedModel,
		ragSources: ragSources, documentTruncated: documentTruncated,
	})
	resp, modelErr = d.completeWithTools(toolCtx, ev.UserID, model.Request{Model: selectedModel, Messages: messages}, streamer, session)
	return d.finishModelAnswer(ctx, replyCtx, ev, resp, modelErr, selectedModel, ragSources, documentTruncated, session)
}

func (d *Dispatcher) deliverModelResult(ctx context.Context, ev Event, text string) error {
	if ev.MenuCallback {
		lang := d.chatLanguage(ctx, ev.ChatID)
		return d.renderMenu(ctx, ev, text, simpleBackMarkupFor(lang, "menu:links"))
	}
	return d.Send(ctx, ev.ChatID, text)
}

// finishReasonNotice — what the user must know when the provider stopped on its own, not when the bot lost
// the tail. `length` is the context/output limit of the model, `content_filter` is the provider's own filter.
func finishReasonNotice(reason string) string {
	return finishReasonNoticeFor(i18n.Default, reason)
}

// finishReasonNoticeFor is the language-aware form used by the Telegram command surface. The Mini App
// and the task scheduler still call the Russian wrapper, so their output is unchanged.
func finishReasonNoticeFor(lang string, reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length":
		return i18n.T(lang, "ask.finish_length")
	case "content_filter":
		return i18n.T(lang, "ask.finish_content_filter")
	default:
		return ""
	}
}

func (d *Dispatcher) finishModelAnswer(ctx context.Context, replyCtx context.Context, ev Event, resp model.Response, modelErr error, selectedModel string, ragSources []string, documentTruncated bool, session *streamSession) error {
	// The language must stay readable after the reply budget expired: a timeout notice is exactly the
	// moment the request context is already cancelled, and falling back to DEFAULT_LANGUAGE there would
	// answer a chat with an override in the wrong language.
	lang := d.chatLanguage(context.WithoutCancel(ctx), ev.ChatID)
	if errors.Is(modelErr, errNetworkApprovalPending) {
		return nil
	}
	if notice, limited := modelLimitNoticeFor(lang, modelErr); limited {
		if session == nil {
			return d.deliverModelResult(replyCtx, ev, notice)
		}
		return session.deliver(replyCtx, d, notice)
	}
	if modelErr != nil {
		if errors.Is(modelErr, context.Canceled) {
			return modelErr // the process is stopping : nothing to promise the user
		}
		// The audit of a failure must not ride a context that has already expired. Measured 2026-10-31 the
		// timeout row was written only because the model budget (15 с) is shorter than the reply deadline
		// context.WithoutCancel keeps the values of the request and drops its cancellation; auditBudget gives
		var auditCtx, stopAudit = context.WithTimeout(context.WithoutCancel(ctx), auditBudget)
		defer stopAudit()
		// Measured 2026-10-31: STRATA refused the connection (`dial tcp 127.0.0.1:8080: connectex …`) while
		// arriving updates and nothing else — the operator had to open the database to learn that the model
		// was down. One error line per failed call, and the reason goes through Sanitize because a library
		d.deps.Logger.Error("the model did not answer",
			"chat_id", ev.ChatID,
			"user_id", ev.UserID,
			"error", Sanitize(modelErr, d.deps.Token),
		)
		if session != nil && session.sawText() {
			session.broken(auditCtx, d, Sanitize(modelErr, d.deps.Token))
		}
		if errors.Is(modelErr, context.DeadlineExceeded) {
			// The number in the user text and the detail in the events row must name the SAME budget — the
			// one that really expired. Measured 2026-10-31 (STRATA_TIMEOUT_SECONDS=15, TG_REPLY_DEADLINE_SECONDS=30):
			// the row read «budget 30s» while the user was answered «не ответила за 15 с» — one failure described
			// silent and a stream that ran too long are different failures, and neither is STRATA_TIMEOUT_SECONDS.
			var spent = "STRATA_TIMEOUT_SECONDS"
			var seconds = d.deps.Cfg.TimeoutSeconds
			switch {
			case errors.Is(modelErr, model.ErrStreamStalled):
				spent, seconds = "STREAM_IDLE_TIMEOUT_SECONDS", d.deps.Cfg.StreamIdleSeconds
			case errors.Is(modelErr, model.ErrStreamTooLong):
				spent, seconds = "STREAM_TOTAL_DEADLINE_SECONDS", d.deps.Cfg.StreamTotalSeconds
			case replyCtx.Err() != nil:
				spent = "TG_REPLY_DEADLINE_SECONDS"
				seconds = d.deps.Cfg.ReplyDeadlineSeconds
			}
			var detail = fmt.Sprintf("%s %ds spent", spent, seconds)
			if session != nil {
				// was too long» and cannot say whether the model wrote 40 symbols or 4000 before it stopped.
				var edits, skipped, failures = session.stats()
				detail += fmt.Sprintf(", %d runes delivered, %d edits, %d skipped, %d delivery failures",
					Runes(resp.Content), edits, skipped, failures)
			}
			d.logEvent(auditCtx, ev.UserID, store.EventTimeout, detail)
			return d.deliverModelResult(ctx, ev, i18n.T(lang, "ask.timeout", seconds, spent))
		}
		if errors.Is(modelErr, model.ErrEmptyAnswer) {
			d.logEvent(auditCtx, ev.UserID, store.EventModelError, "empty_answer")
			return d.deliverModelResult(ctx, ev, i18n.T(lang, "ask.empty"))
		}
		if errors.Is(modelErr, model.ErrNoRoom) {
			d.logEvent(auditCtx, ev.UserID, store.EventModelError, "no_room")
			return d.deliverModelResult(ctx, ev, i18n.T(lang, "ask.no_room"))
		}
		d.logEvent(auditCtx, ev.UserID, store.EventModelError, Sanitize(modelErr, d.deps.Token))
		return d.deliverModelResult(ctx, ev, i18n.T(lang, "ask.model_error", Sanitize(modelErr, d.deps.Token)))
	}

	var _, writeErr = store.Append(replyCtx, d.deps.DB, store.Message{
		ChatID:    ev.ChatID,
		UserID:    ev.UserID,
		Role:      store.RoleAssistant,
		Content:   SanitizeToolMarkup(resp.Content),
		TokensIn:  resp.TokensIn,
		TokensOut: resp.TokensOut,
		Model:     replyModel(resp.Model, selectedModel),
		LatencyMs: resp.Latency.Milliseconds(),
	})
	if writeErr != nil {
		return writeErr
	}

	//
	// through the fallback of deliver(), and without these numbers this line says «everything is fine» about a
	// stream that never streamed.
	var edits, skipped, failures = 0, 0, 0
	if session != nil {
		edits, skipped, failures = session.stats()
	}
	d.deps.Logger.Info("the model answered",
		"chat_id", ev.ChatID,
		"user_id", ev.UserID,
		"model", replyModel(resp.Model, selectedModel),
		"latency_ms", resp.Latency.Milliseconds(),
		"attempts", resp.Attempts,
		"tokens_in", tokensOrNull(resp.TokensIn),
		"tokens_out", tokensOrNull(resp.TokensOut),
		"streamed", session != nil,
		"edits", edits,
		"edits_skipped_by_cadence", skipped,
		"delivery_failures", failures,
	)

	// A provider that stopped on its own limit says so in finish_reason. Without this notice a truncated or
	// filtered answer was stored and shown exactly like a complete one, and the user could not tell that the
	// model was cut off rather than the bot losing the tail. The assembly itself is shared with the Mini App
	// (answer_format.go), so both clients show the same notices and the Telegram text stays byte-identical.
	if notice := finishReasonNoticeFor(lang, resp.FinishReason); notice != "" {
		d.logEvent(ctx, ev.UserID, store.EventModelError, "finish_reason "+brief(resp.FinishReason, 40))
	}
	var full = formatModelAnswerFor(lang, resp.Content, resp, selectedModel, answerFormatOptions{
		Latency:           true,
		Sources:           ragSources,
		DocumentTruncated: documentTruncated,
	})
	if session == nil {
		return d.deliverModelResult(replyCtx, ev, full)
	}
	// continues as new messages — a chat that followed one message being rewritten must not see it split in two.
	return session.deliver(replyCtx, d, full)
}

func (d *Dispatcher) reset(ctx context.Context, ev Event, arg string) error {
	var hidden, err = store.ClearChat(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	lang := d.chatLanguage(ctx, ev.ChatID)
	text := i18n.T(lang, "reset.done", hidden)
	if ev.MenuCallback {
		return d.showRootMenu(ctx, ev, text)
	}
	return d.reply(ctx, ev.ChatID, text)
}

func (d *Dispatcher) history(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	if strings.TrimSpace(arg) == "menu" {
		return d.showHistoryMenu(ctx, ev)
	}
	var limit = HistoryDefault
	if trimmed := strings.TrimSpace(arg); trimmed != "" {
		var n, err = strconv.Atoi(trimmed)
		if err != nil || n <= 0 {
			return d.menuReply(ctx, ev, i18n.T(lang, "history.usage", HistoryDefault), historyMenuMarkupFor(lang))
		}
		if n > HistoryMax {
			n = HistoryMax
		}
		limit = n
	}
	var rows, err = store.HistoryOfChat(ctx, d.deps.DB, ev.ChatID, limit)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		if ev.MenuCallback {
			return d.showHistoryMenu(ctx, ev, i18n.T(lang, "history.empty"))
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "history.empty"))
	}
	var lines = []string{i18n.T(lang, "history.header", len(rows))}
	var i = 0
	for i < len(rows) {
		var row = rows[i]
		lines = append(lines, i18n.T(lang, "history.line", i+1, row.Role, brief(SanitizeToolMarkup(row.Content), 120)))
		i++
	}
	return d.menuReply(ctx, ev, strings.Join(lines, "\n"), historyMenuMarkupFor(lang))
}

func (d *Dispatcher) stats(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	var stats, err = store.Stats(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	var lines = []string{
		i18n.T(lang, "stats.messages", stats.Messages),
		i18n.T(lang, "stats.tokens", stats.TokensIn, stats.TokensOut),
		i18n.T(lang, "stats.errors", stats.Errors),
	}
	// after the bot had answered «Модель не ответила за 15 с, попробуй ещё раз» this line still read
	// «Ошибок модели: 3» — which reads to an operator as nothing having happened. The line shows up only when
	if stats.Timeouts > 0 {
		lines = append(lines, i18n.T(lang, "stats.timeouts", stats.Timeouts))
	}
	var up, down, feedbackErr = store.MessageFeedbackCounts(ctx, d.deps.DB, ev.ChatID)
	if feedbackErr != nil {
		return feedbackErr
	}
	lines = append(lines, i18n.T(lang, "stats.feedback", up, down))
	if stats.Model != "" {
		lines = append(lines, i18n.T(lang, "stats.model", stats.Model))
	}
	if stats.LastAt != "" {
		lines = append(lines, i18n.T(lang, "stats.last", stats.LastAt))
	}
	return d.menuReply(ctx, ev, strings.Join(lines, "\n"), refreshBackMarkupFor(lang, "menu:stats", "menu:status"))
}

func replyModel(fromResponse string, configured string) string {
	if strings.TrimSpace(fromResponse) != "" {
		return fromResponse
	}
	return configured
}

func tokensOrNull(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func latencyLine(resp model.Response, configured string) string {
	return latencyLineFor(i18n.Default, resp, configured)
}

// latencyLineFor is the language-aware latency footer. The Mini App and the task scheduler still use
// the Russian wrapper. A measured time-to-first-token is shown as «⚡ 0.8s» right before the model
// name; a response without one (a non-streamed answer, a tool-only turn or an old caller) keeps the
// line it had.
func latencyLineFor(lang string, resp model.Response, configured string) string {
	var name = replyModel(resp.Model, configured)
	var ttft, hasTTFT = formatFirstToken(resp.FirstTokenLatency)
	if resp.TokensIn == nil && resp.TokensOut == nil {
		if hasTTFT {
			return i18n.T(lang, "ask.latency_no_tokens_ttft",
				resp.Latency.Round(time.Millisecond), ttft, name, resp.Attempts)
		}
		return i18n.T(lang, "ask.latency_no_tokens",
			resp.Latency.Round(time.Millisecond), name, resp.Attempts)
	}
	var in = int64(0)
	var out = int64(0)
	if resp.TokensIn != nil {
		in = *resp.TokensIn
	}
	if resp.TokensOut != nil {
		out = *resp.TokensOut
	}
	if rate, ok := tokensPerSecond(out, resp.Latency); ok {
		if hasTTFT {
			return i18n.T(lang, "ask.latency_tokens_rate_ttft",
				resp.Latency.Round(time.Millisecond), in, out, rate, ttft, name, resp.Attempts)
		}
		return i18n.T(lang, "ask.latency_tokens_rate",
			resp.Latency.Round(time.Millisecond), in, out, rate, name, resp.Attempts)
	}
	if hasTTFT {
		return i18n.T(lang, "ask.latency_tokens_ttft",
			resp.Latency.Round(time.Millisecond), in, out, ttft, name, resp.Attempts)
	}
	return i18n.T(lang, "ask.latency_tokens",
		resp.Latency.Round(time.Millisecond), in, out, name, resp.Attempts)
}

// formatFirstToken renders a measured TTFT for the footer. Under ten seconds it keeps one decimal of
// a second (0.8s, 4.6s), because that is the resolution a reader compares; longer waits are whole
// seconds. A zero duration means «not measured» and is never rendered, so the footer of a
// non-streamed answer does not grow a meaningless ⚡ 0.0s.
func formatFirstToken(d time.Duration) (string, bool) {
	if d <= 0 {
		return "", false
	}
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds()), true
	}
	return d.Round(time.Second).String(), true
}

// tokensPerSecond renders the generation speed. It needs a positive latency and output count; the caller
// falls back to the token-only footer otherwise.
func tokensPerSecond(out int64, latency time.Duration) (string, bool) {
	if out <= 0 || latency <= 0 {
		return "", false
	}
	rate := float64(out) / latency.Seconds()
	if rate < 10 {
		return fmt.Sprintf("%.1f ток/с", rate), true
	}
	return fmt.Sprintf("%.0f ток/с", rate), true
}

func brief(text string, maxRunes int) string {
	var runes = []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "…"
}

// limitsControl — /limits: show this chat's limits and today's usage, or set/reset them.
//
// Who may write: an administrator anywhere; in a private chat its owner too (the chats.user_id of the
// row). A group is managed by an administrator only — a group member must never be able to lift the
// group's limit just because the group id happens to resemble a user id. Reading /limits is open to
// every allowed user of the chat.
func (d *Dispatcher) limitsControl(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	if d.deps.DB == nil {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "limits.usage"))
	}
	action := strings.TrimSpace(arg)
	if action == "" || strings.EqualFold(action, "menu") {
		return d.replyChatLimits(ctx, ev, lang)
	}
	if !d.canManageChatLimits(ctx, ev) {
		d.logEvent(ctx, ev.UserID, store.EventDenied, "limits")
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "limits.denied"))
	}
	fields := strings.Fields(action)
	switch strings.ToLower(fields[0]) {
	case "reset":
		_, err := store.ResetChatLimits(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		d.logChatEvent(ctx, ev.ChatID, ev.UserID, store.EventCommand, "limits:reset")
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "limits.reset"))
	case "model", "network", "minute":
		if len(fields) != 2 {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "limits.usage"))
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || value < 0 || value > store.ChatLimitMax {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "limits.invalid_number", store.ChatLimitMax))
		}
		limits, _, err := store.GetChatLimits(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		limits.ChatID = ev.ChatID
		switch strings.ToLower(fields[0]) {
		case "model":
			limits.DailyModelLimit = value
		case "network":
			limits.DailyNetworkLimit = value
		case "minute":
			limits.PerMinuteLimit = value
		}
		if err := d.saveChatLimits(ctx, ev, limits); err != nil {
			return err
		}
		d.logChatEvent(ctx, ev.ChatID, ev.UserID, store.EventCommand, "limits:set "+strings.ToLower(fields[0]))
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "limits.saved"))
	case "tools":
		raw := strings.TrimSpace(strings.TrimPrefix(action, fields[0]))
		if strings.EqualFold(raw, "none") {
			raw = ""
		}
		tools, err := store.NormalizeDisabledTools([]string{raw})
		if err != nil {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "limits.invalid_tool", raw, strings.Join(store.ChatTools(), ", ")))
		}
		limits, _, err := store.GetChatLimits(ctx, d.deps.DB, ev.ChatID)
		if err != nil {
			return err
		}
		limits.ChatID = ev.ChatID
		limits.DisabledTools = tools
		if err := d.saveChatLimits(ctx, ev, limits); err != nil {
			return err
		}
		d.logChatEvent(ctx, ev.ChatID, ev.UserID, store.EventCommand, "limits:set tools")
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "limits.saved"))
	default:
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "limits.usage"))
	}
}

// canManageChatLimits is the write permission of /limits. Admin anywhere; the owner of a private chat.
func (d *Dispatcher) canManageChatLimits(ctx context.Context, ev Event) bool {
	if d.deps.Cfg.IsAdmin(ev.UserID) {
		return true
	}
	if ev.ChatID < 0 {
		return false
	}
	owner, found, err := store.ChatOwner(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		d.deps.Logger.Error("the chat owner could not be read", "chat_id", ev.ChatID, "error", Sanitize(err, d.deps.Token))
		return false
	}
	if !found {
		// A private chat that has no row yet: Telegram's private chat id IS the owner's user id, and a
		// negative id was refused above, so this is not a group id mistaken for a user.
		return ev.ChatID == ev.UserID
	}
	return owner == ev.UserID
}

// saveChatLimits ensures the chats row exists (a first /limits on a fresh chat is not stored by the
// dispatcher: only model commands are) and upserts the validated row.
func (d *Dispatcher) saveChatLimits(ctx context.Context, ev Event, limits store.ChatLimits) error {
	if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
		return err
	}
	return store.SetChatLimits(ctx, d.deps.DB, limits)
}

// replyChatLimits renders the /limits report: the source of the limits, today's counters (the same
// events.chat_id rows enforcement reads) and the disabled tools.
func (d *Dispatcher) replyChatLimits(ctx context.Context, ev Event, lang string) error {
	limits, found, err := store.GetChatLimits(ctx, d.deps.DB, ev.ChatID)
	if err != nil {
		return err
	}
	usage, err := store.ChatDailyUsage(ctx, d.deps.DB, ev.ChatID, d.now())
	if err != nil {
		return err
	}
	limitText := func(value int64) string {
		if value == 0 {
			return i18n.T(lang, "limits.unlimited")
		}
		return strconv.FormatInt(value, 10)
	}
	source := i18n.T(lang, "limits.source_default")
	if found {
		source = i18n.T(lang, "limits.source_chat")
	}
	disabled := i18n.T(lang, "limits.tools_none")
	if len(limits.DisabledTools) > 0 {
		disabled = strings.Join(limits.DisabledTools, ", ")
	}
	kind := "private"
	if ev.ChatID < 0 {
		kind = "group"
	}
	lines := []string{
		i18n.T(lang, "limits.title", ev.ChatID, kind),
		i18n.T(lang, "limits.source", source),
		i18n.T(lang, "limits.today", usage.ModelCalls, limitText(limits.DailyModelLimit),
			usage.NetworkToolCalls(), limitText(limits.DailyNetworkLimit)),
		i18n.T(lang, "limits.tools", usage.ReadURL, usage.WebSearch, usage.Calculator, disabled),
		i18n.T(lang, "limits.per_minute", limitText(limits.PerMinuteLimit)),
	}
	return d.reply(ctx, ev.ChatID, strings.Join(lines, "\n"))
}

// modelLimitNoticeFor — the /ask form of modelLimitNotice. The original stays Russian for the Mini App
// and the task scheduler, which are outside this change.
func modelLimitNoticeFor(lang string, err error) (string, bool) {
	switch {
	case errors.Is(err, ErrModelRateLimited):
		return i18n.T(lang, "ask.rate_limited"), true
	case errors.Is(err, ErrModelDailyLimited):
		return i18n.T(lang, "ask.daily_limited"), true
	case errors.Is(err, ErrChatModelDailyLimited):
		// The refusal already carries the sentence in the chat's language; the fallback keeps the
		// Russian constant for a caller that built the error without a language (a unit test).
		if text := chatLimitRefusalText(err); text != "" {
			return text, true
		}
		return i18n.T(lang, "ask.chat_daily_limited", 0), true
	default:
		return "", false
	}
}

// attachmentUserErrorFor is the language-aware form of attachmentUserError for the /ask path.
func attachmentUserErrorFor(lang string, err error) string {
	switch {
	case errors.Is(err, ErrAttachmentTooLarge), errors.Is(err, ErrRAGDocumentTooLarge):
		return i18n.T(lang, "ask.attachment_too_large")
	case errors.Is(err, ErrRAGDocumentEmpty):
		return i18n.T(lang, "ask.attachment_empty_document")
	case errors.Is(err, ErrVoiceTooLong):
		return i18n.T(lang, "ask.attachment_voice_too_long")
	case errors.Is(err, ErrDocumentNoText):
		return i18n.T(lang, "ask.attachment_no_text")
	case errors.Is(err, ErrDocumentEncrypted):
		return i18n.T(lang, "ask.attachment_encrypted")
	case errors.Is(err, ErrDocumentUnsupported):
		return i18n.T(lang, "ask.attachment_unsupported_doc")
	case errors.Is(err, ErrDocumentCorrupt):
		return i18n.T(lang, "ask.attachment_corrupt")
	case errors.Is(err, ErrAttachmentType):
		return i18n.T(lang, "ask.attachment_type")
	case errors.Is(err, ErrAttachmentUnavailable):
		return i18n.T(lang, "ask.attachment_unavailable_file")
	default:
		return i18n.T(lang, "ask.attachment_read_failed")
	}
}
