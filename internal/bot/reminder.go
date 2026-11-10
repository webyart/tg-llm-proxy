package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-telegram/bot/models"

	"bot/internal/i18n"
	"bot/internal/store"
)

const (
	ReminderUsage           = "Использование: /remind через 1 минуту <текст>, /remind через 2 часа <текст> или /remind каждый день 09:00 <текст>."
	RemindersUsage          = "Использование: /reminders, /reminders cancel <id> или /reminders delete <id>."
	MaxReminderDelay        = 3650 * 24 * time.Hour
	reminderFailedListLimit = 5
	reminderTomorrowHour    = 9
)

var ErrReminderSyntax = errors.New("invalid reminder syntax")

func reminderLocation(d *Dispatcher) *time.Location {
	if d.deps.Cfg.ReminderLocation != nil {
		return d.deps.Cfg.ReminderLocation
	}
	return time.UTC
}

func reminderRemainder(input string, words int) (string, bool) {
	rest := strings.TrimSpace(input)
	for i := 0; i < words; i++ {
		cut := strings.IndexFunc(rest, unicode.IsSpace)
		if cut < 0 {
			return "", false
		}
		rest = strings.TrimSpace(rest[cut:])
	}
	return rest, rest != ""
}

// reminderUnit maps a duration word from either supported language to its length. Russian keeps
// its full declension set; English accepts singular and plural forms only.
func reminderUnit(value string) (time.Duration, bool) {
	switch strings.ToLower(value) {
	case "секунду", "секунды", "секунд":
		return time.Second, true
	case "минуту", "минуты", "минут", "minute", "minutes":
		return time.Minute, true
	case "час", "часа", "часов", "hour", "hours":
		return time.Hour, true
	case "день", "дня", "дней", "day", "days":
		return 24 * time.Hour, true
	}
	return 0, false
}

// reminderPrefixWords returns how many leading words describe the schedule, so the reminder text
// is extracted by the same "everything after the time expression, trimmed" rule in both languages.
// "every day at HH:MM" is the only supported form whose time expression is four words long.
func reminderPrefixWords(fields []string) int {
	if len(fields) > 2 && strings.EqualFold(fields[0], "every") && strings.EqualFold(fields[1], "day") && strings.EqualFold(fields[2], "at") {
		return 4
	}
	return 3
}

// reminderDailyClock reports the HH:MM clock of a daily phrase in either language. Validation is
// deliberately left to NextDailyReminder/parseReminderClock so both languages share one clock path.
func reminderDailyClock(fields []string) (string, bool) {
	if len(fields) < 3 {
		return "", false
	}
	switch {
	case strings.EqualFold(fields[0], "каждый") && strings.EqualFold(fields[1], "день"):
		return fields[2], true
	case strings.EqualFold(fields[0], "every") && strings.EqualFold(fields[1], "day"):
		if strings.EqualFold(fields[2], "at") {
			if len(fields) < 4 {
				return "", false
			}
			return fields[3], true
		}
		return fields[2], true
	case strings.EqualFold(fields[0], "daily") && strings.EqualFold(fields[1], "at"):
		return fields[2], true
	}
	return "", false
}

// ParseReminder accepts the Russian and English grammars regardless of the chat language.
func ParseReminder(input string, now time.Time, location *time.Location) (store.Reminder, error) {
	if location == nil {
		location = time.UTC
	}
	fields := strings.Fields(input)
	words := reminderPrefixWords(fields)
	if len(fields) <= words {
		return store.Reminder{}, ErrReminderSyntax
	}
	text, ok := reminderRemainder(input, words)
	if !ok || len([]rune(text)) > store.ReminderMaxTextRunes || strings.ContainsRune(text, '\x00') {
		return store.Reminder{}, store.ErrReminderText
	}

	if strings.EqualFold(fields[0], "через") || strings.EqualFold(fields[0], "in") {
		amount, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || amount <= 0 {
			return store.Reminder{}, ErrReminderSyntax
		}
		unit, ok := reminderUnit(fields[2])
		if !ok {
			return store.Reminder{}, ErrReminderSyntax
		}
		if amount > int64(MaxReminderDelay/unit) {
			return store.Reminder{}, fmt.Errorf("reminder delay exceeds %s", MaxReminderDelay)
		}
		return store.Reminder{Text: text, Cadence: store.ReminderCadenceOnce, NextRunAt: now.Add(time.Duration(amount) * unit).UTC()}, nil
	}

	if clock, ok := reminderDailyClock(fields); ok {
		next, err := NextDailyReminder(now, clock, location)
		if err != nil {
			return store.Reminder{}, err
		}
		return store.Reminder{Text: text, Cadence: store.ReminderCadenceDaily, DailyTime: clock, NextRunAt: next}, nil
	}
	return store.Reminder{}, ErrReminderSyntax
}

func parseReminderClock(value string) (int, int, error) {
	if len(value) != 5 || value[2] != ':' {
		return 0, 0, store.ErrReminderTime
	}
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return 0, 0, store.ErrReminderTime
	}
	return parsed.Hour(), parsed.Minute(), nil
}

func reminderWallClock(day time.Time, hour int, minute int, location *time.Location) time.Time {
	local := day.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, location)
}

func NextDailyReminder(now time.Time, clock string, location *time.Location) (time.Time, error) {
	if location == nil {
		location = time.UTC
	}
	hour, minute, err := parseReminderClock(clock)
	if err != nil {
		return time.Time{}, err
	}
	candidate := reminderWallClock(now, hour, minute, location)
	if !candidate.After(now) {
		candidate = reminderWallClock(now.In(location).AddDate(0, 0, 1), hour, minute, location)
	}
	return candidate.UTC(), nil
}

func AdvanceDailyReminder(previous time.Time, now time.Time, clock string, location *time.Location) (time.Time, error) {
	if location == nil {
		location = time.UTC
	}
	hour, minute, err := parseReminderClock(clock)
	if err != nil {
		return time.Time{}, err
	}
	candidate := reminderWallClock(previous.In(location).AddDate(0, 0, 1), hour, minute, location)
	if !candidate.After(now) {
		candidate = reminderWallClock(now, hour, minute, location)
		if !candidate.After(now) {
			candidate = reminderWallClock(now.In(location).AddDate(0, 0, 1), hour, minute, location)
		}
	}
	return candidate.UTC(), nil
}

func reminderTimezone(d *Dispatcher) string {
	if timezone := strings.TrimSpace(d.deps.Cfg.ReminderTimezone); timezone != "" {
		return timezone
	}
	return reminderLocation(d).String()
}

func reminderSchedule(d *Dispatcher, reminder store.Reminder) string {
	return reminderScheduleFor(i18n.Default, d, reminder)
}

func reminderScheduleFor(lang string, d *Dispatcher, reminder store.Reminder) string {
	location := reminderLocation(d)
	if reminder.Cadence == store.ReminderCadenceDaily {
		return i18n.T(lang, "remind.schedule_daily", reminder.DailyTime, reminder.NextRunAt.In(location).Format("MST"), reminderTimezone(d))
	}
	return fmt.Sprintf("%s (%s)", reminder.NextRunAt.In(location).Format("2006-01-02 15:04 MST"), reminderTimezone(d))
}

func (d *Dispatcher) createReminder(ctx context.Context, ev Event, reminder store.Reminder) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
		return err
	}
	reminder.ChatID = ev.ChatID
	reminder.UserID = ev.UserID
	created, err := store.CreateReminder(ctx, d.deps.DB, reminder)
	if errors.Is(err, store.ErrReminderText) {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "remind.text_invalid", store.ReminderMaxTextRunes))
	}
	if errors.Is(err, store.ErrReminderLimit) {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "remind.limit", store.ReminderMaxActivePerChat))
	}
	if err != nil {
		return err
	}
	return d.reply(ctx, ev.ChatID, i18n.T(lang, "remind.created", created.ID, reminderScheduleFor(lang, d, created), created.Text))
}

func (d *Dispatcher) remind(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	reminder, err := ParseReminder(arg, d.now(), reminderLocation(d))
	if err != nil {
		if errors.Is(err, store.ErrReminderText) {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "remind.text_invalid", store.ReminderMaxTextRunes))
		}
		if errors.Is(err, store.ErrReminderTime) {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "remind.time_invalid", i18n.T(lang, "remind.usage")))
		}
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "remind.usage"))
	}
	return d.createReminder(ctx, ev, reminder)
}

const pendingReminderActionPrefix = "remind:"

func quickReminder(choice string, text string, now time.Time, location *time.Location) (store.Reminder, error) {
	reminder := store.Reminder{Text: text, Cadence: store.ReminderCadenceOnce}
	switch choice {
	case "10m":
		reminder.NextRunAt = now.Add(10 * time.Minute).UTC()
	case "1h":
		reminder.NextRunAt = now.Add(time.Hour).UTC()
	case "tomorrow":
		reminder.NextRunAt = reminderWallClock(now.In(location).AddDate(0, 0, 1), reminderTomorrowHour, 0, location).UTC()
	default:
		return store.Reminder{}, ErrReminderSyntax
	}
	return reminder, nil
}

func (d *Dispatcher) beginReminderCreation(ctx context.Context, ev Event, choice string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	labels := map[string]string{
		"10m":      i18n.T(lang, "remind.quick_10m"),
		"1h":       i18n.T(lang, "remind.quick_1h"),
		"tomorrow": i18n.T(lang, "remind.quick_tomorrow"),
	}
	label, ok := labels[choice]
	if !ok {
		return d.showReminderCreateMenu(ctx, ev, menuExpiredFor(lang))
	}
	prompt := i18n.T(lang, "remind.create_prompt", store.ReminderMaxTextRunes, label, reminderTimezone(d))
	return d.beginPendingInput(ctx, ev, pendingReminderActionPrefix+choice, "menu:reminders", prompt)
}

func (d *Dispatcher) reminders(ctx context.Context, ev Event, arg string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	action := strings.TrimSpace(arg)
	if action == "" {
		return d.sendReminderMenu(ctx, ev)
	}
	if action == "menu" {
		return d.showReminderMenu(ctx, ev)
	}
	if action == "menu:new" {
		return d.showReminderCreateMenu(ctx, ev)
	}
	if action == "menu:failed" {
		return d.showFailedReminderMenu(ctx, ev)
	}
	if choice, ok := strings.CutPrefix(action, "menu:new:"); ok {
		return d.beginReminderCreation(ctx, ev, choice)
	}
	if value, ok := strings.CutPrefix(action, "menu:create:"); ok {
		choice, text, found := strings.Cut(value, " ")
		if !found {
			return d.reply(ctx, ev.ChatID, i18n.T(lang, "reminders.usage"))
		}
		reminder, err := quickReminder(choice, text, d.now(), reminderLocation(d))
		if err != nil {
			return d.showReminderCreateMenu(ctx, ev, menuExpiredFor(lang))
		}
		return d.createReminder(ctx, ev, reminder)
	}
	if value, ok := strings.CutPrefix(action, "menu:cancel:"); ok {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return d.showReminderMenu(ctx, ev, menuExpiredFor(lang))
		}
		return d.cancelReminder(ctx, ev, id, true)
	}
	if value, ok := strings.CutPrefix(action, "menu:retry:"); ok {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return d.showFailedReminderMenu(ctx, ev, menuExpiredFor(lang))
		}
		return d.retryFailedReminder(ctx, ev, id)
	}
	if value, ok := strings.CutPrefix(action, "menu:delete:"); ok {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return d.showFailedReminderMenu(ctx, ev, menuExpiredFor(lang))
		}
		return d.deleteFailedReminder(ctx, ev, id)
	}
	fields := strings.Fields(action)
	if len(fields) == 2 && (strings.EqualFold(fields[0], "cancel") || strings.EqualFold(fields[0], "delete")) {
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && id > 0 {
			return d.cancelReminder(ctx, ev, id, false)
		}
	}
	return d.reply(ctx, ev.ChatID, i18n.T(lang, "reminders.usage"))
}

func (d *Dispatcher) cancelReminder(ctx context.Context, ev Event, id int64, menu bool) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	cancelled, err := store.CancelReminder(ctx, d.deps.DB, ev.ChatID, id, d.now())
	if err != nil {
		return err
	}
	if menu {
		if !cancelled {
			return d.showReminderMenu(ctx, ev, menuExpiredFor(lang))
		}
		return d.showReminderMenu(ctx, ev, i18n.T(lang, "remind.cancelled", id))
	}
	if !cancelled {
		return d.reply(ctx, ev.ChatID, i18n.T(lang, "remind.not_found", id))
	}
	return d.reply(ctx, ev.ChatID, i18n.T(lang, "remind.cancelled", id))
}

func (d *Dispatcher) retryFailedReminder(ctx context.Context, ev Event, id int64) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	retried, err := store.RetryFailedReminder(ctx, d.deps.DB, ev.ChatID, id, d.now())
	if err != nil {
		return err
	}
	if !retried {
		return d.showFailedReminderMenu(ctx, ev, menuExpiredFor(lang))
	}
	return d.showReminderMenu(ctx, ev, i18n.T(lang, "remind.retried", id))
}

func (d *Dispatcher) deleteFailedReminder(ctx context.Context, ev Event, id int64) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	deleted, err := store.DeleteFailedReminder(ctx, d.deps.DB, ev.ChatID, id)
	if err != nil {
		return err
	}
	if !deleted {
		return d.showFailedReminderMenu(ctx, ev, menuExpiredFor(lang))
	}
	return d.showFailedReminderMenu(ctx, ev, i18n.T(lang, "remind.deleted_failed", id))
}

func (d *Dispatcher) reminderMenuContent(ctx context.Context, ev Event, note ...string) (string, models.InlineKeyboardMarkup, error) {
	lang := d.chatLanguage(ctx, ev.ChatID)
	rows, err := store.ListReminders(ctx, d.deps.DB, ev.ChatID, store.ReminderListLimit)
	if err != nil {
		return "", models.InlineKeyboardMarkup{}, err
	}
	text := i18n.T(lang, "reminders.none", i18n.T(lang, "reminders.usage"))
	if len(rows) > 0 {
		lines := []string{i18n.T(lang, "reminders.header", store.ReminderListLimit)}
		for _, reminder := range rows {
			lines = append(lines, i18n.T(lang, "reminders.line", reminder.ID, reminderScheduleFor(lang, d, reminder), brief(reminder.Text, 100)))
		}
		text = strings.Join(lines, "\n")
	}
	return menuText(text, note), reminderMenuMarkupFor(lang, rows), nil
}

func (d *Dispatcher) showReminderMenu(ctx context.Context, ev Event, note ...string) error {
	text, markup, err := d.reminderMenuContent(ctx, ev, note...)
	if err != nil {
		return err
	}
	return d.renderMenu(ctx, ev, text, markup)
}

func (d *Dispatcher) sendReminderMenu(ctx context.Context, ev Event) error {
	text, markup, err := d.reminderMenuContent(ctx, ev)
	if err != nil {
		return err
	}
	if sender, ok := d.deps.Replier.(interface {
		SendMenu(context.Context, int64, string, models.InlineKeyboardMarkup) error
	}); ok {
		return sender.SendMenu(ctx, ev.ChatID, Escape(text, d.deps.Cfg.ParseMode), markup)
	}
	return d.reply(ctx, ev.ChatID, text)
}

func reminderMenuMarkup(reminders []store.Reminder) models.InlineKeyboardMarkup {
	return reminderMenuMarkupFor(i18n.Default, reminders)
}

func reminderMenuMarkupFor(lang string, reminders []store.Reminder) models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{menuRow(menuButton(i18n.T(lang, "reminders.create"), "menu:reminder:new"))}
	for _, reminder := range reminders {
		rows = append(rows, menuRow(menuButton(i18n.T(lang, "reminders.cancel", reminder.ID, brief(reminder.Text, 30)),
			fmt.Sprintf("menu:reminder:cancel:%d", reminder.ID))))
	}
	rows = append(rows,
		menuRow(menuButton(i18n.T(lang, "reminders.failed"), "menu:reminder:failed")),
		menuRow(menuButton(i18n.T(lang, "reminders.refresh"), "menu:reminders")),
		menuRow(backButtonFor(lang, "menu:more")),
	)
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func (d *Dispatcher) showReminderCreateMenu(ctx context.Context, ev Event, note ...string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	text := i18n.T(lang, "reminders.create_menu", reminderTimezone(d))
	markup := models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		menuRow(menuButton(i18n.T(lang, "reminders.quick_10m"), "menu:reminder:new:10m")),
		menuRow(menuButton(i18n.T(lang, "reminders.quick_1h"), "menu:reminder:new:1h")),
		menuRow(menuButton(i18n.T(lang, "reminders.quick_9am"), "menu:reminder:new:tomorrow")),
		menuRow(backButtonFor(lang, "menu:reminders")),
	}}
	return d.renderMenu(ctx, ev, menuText(text, note), markup)
}

func (d *Dispatcher) showFailedReminderMenu(ctx context.Context, ev Event, note ...string) error {
	lang := d.chatLanguage(ctx, ev.ChatID)
	rows, err := store.ListFailedReminders(ctx, d.deps.DB, ev.ChatID, reminderFailedListLimit)
	if err != nil {
		return err
	}
	text := i18n.T(lang, "reminders.no_failed")
	if len(rows) > 0 {
		lines := []string{i18n.T(lang, "reminders.failed_title", reminderFailedListLimit)}
		for _, reminder := range rows {
			lines = append(lines, i18n.T(lang, "reminders.failed_line", reminder.ID, reminder.Attempts, brief(reminder.Text, 100)))
		}
		text = strings.Join(lines, "\n")
	}
	return d.renderMenu(ctx, ev, menuText(text, note), failedReminderMenuMarkupFor(lang, rows))
}

func failedReminderMenuMarkup(reminders []store.Reminder) models.InlineKeyboardMarkup {
	return failedReminderMenuMarkupFor(i18n.Default, reminders)
}

func failedReminderMenuMarkupFor(lang string, reminders []store.Reminder) models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{}
	for _, reminder := range reminders {
		rows = append(rows, menuRow(
			menuButton(i18n.T(lang, "reminders.retry", reminder.ID), fmt.Sprintf("menu:reminder:retry:%d", reminder.ID)),
			menuButton(i18n.T(lang, "reminders.delete", reminder.ID), fmt.Sprintf("menu:reminder:delete:%d", reminder.ID)),
		))
	}
	rows = append(rows,
		menuRow(menuButton(i18n.T(lang, "reminders.refresh"), "menu:reminder:failed")),
		menuRow(backButtonFor(lang, "menu:reminders")),
	)
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
