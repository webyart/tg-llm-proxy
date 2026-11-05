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

	"bot/internal/store"
)

const (
	ReminderUsage    = "Использование: /remind через 1 минуту <текст>, /remind через 2 часа <текст> или /remind каждый день 09:00 <текст>."
	RemindersUsage   = "Использование: /reminders, /reminders cancel <id> или /reminders delete <id>."
	MaxReminderDelay = 3650 * 24 * time.Hour
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

func ParseReminder(input string, now time.Time, location *time.Location) (store.Reminder, error) {
	if location == nil {
		location = time.UTC
	}
	fields := strings.Fields(input)
	if len(fields) < 4 {
		return store.Reminder{}, ErrReminderSyntax
	}
	text, ok := reminderRemainder(input, 3)
	if !ok || len([]rune(text)) > store.ReminderMaxTextRunes || strings.ContainsRune(text, '\x00') {
		return store.Reminder{}, store.ErrReminderText
	}

	if strings.EqualFold(fields[0], "через") {
		amount, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || amount <= 0 {
			return store.Reminder{}, ErrReminderSyntax
		}
		var unit time.Duration
		switch strings.ToLower(fields[2]) {
		case "секунду", "секунды", "секунд":
			unit = time.Second
		case "минуту", "минуты", "минут":
			unit = time.Minute
		case "час", "часа", "часов":
			unit = time.Hour
		case "день", "дня", "дней":
			unit = 24 * time.Hour
		default:
			return store.Reminder{}, ErrReminderSyntax
		}
		if amount > int64(MaxReminderDelay/unit) {
			return store.Reminder{}, fmt.Errorf("reminder delay exceeds %s", MaxReminderDelay)
		}
		return store.Reminder{Text: text, Cadence: store.ReminderCadenceOnce, NextRunAt: now.Add(time.Duration(amount) * unit).UTC()}, nil
	}

	if strings.EqualFold(fields[0], "каждый") && strings.EqualFold(fields[1], "день") {
		next, err := NextDailyReminder(now, fields[2], location)
		if err != nil {
			return store.Reminder{}, err
		}
		return store.Reminder{Text: text, Cadence: store.ReminderCadenceDaily, DailyTime: fields[2], NextRunAt: next}, nil
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

func (d *Dispatcher) remind(ctx context.Context, ev Event, arg string) error {
	reminder, err := ParseReminder(arg, d.now(), reminderLocation(d))
	if err != nil {
		if errors.Is(err, store.ErrReminderText) {
			return d.reply(ctx, ev.ChatID, fmt.Sprintf("Текст напоминания должен быть непустым и не длиннее %d символов.", store.ReminderMaxTextRunes))
		}
		if errors.Is(err, store.ErrReminderTime) {
			return d.reply(ctx, ev.ChatID, "Время должно быть в формате HH:MM от 00:00 до 23:59. "+ReminderUsage)
		}
		return d.reply(ctx, ev.ChatID, ReminderUsage)
	}
	if err := store.EnsureChat(ctx, d.deps.DB, ev.ChatID, ev.UserID, ev.ChatTitle); err != nil {
		return err
	}
	reminder.ChatID = ev.ChatID
	reminder.UserID = ev.UserID
	created, err := store.CreateReminder(ctx, d.deps.DB, reminder)
	if errors.Is(err, store.ErrReminderLimit) {
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("В чате уже %d активных напоминаний. Сначала отмени одно через /reminders.", store.ReminderMaxActivePerChat))
	}
	if err != nil {
		return err
	}
	schedule := created.NextRunAt.In(reminderLocation(d)).Format("2006-01-02 15:04 MST")
	if created.Cadence == store.ReminderCadenceDaily {
		schedule = "каждый день в " + created.DailyTime + " " + created.NextRunAt.In(reminderLocation(d)).Format("MST")
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Напоминание #%d сохранено: %s. Текст: %s", created.ID, schedule, created.Text))
}

func (d *Dispatcher) reminders(ctx context.Context, ev Event, arg string) error {
	action := strings.TrimSpace(arg)
	if action == "" {
		return d.sendReminderMenu(ctx, ev)
	}
	if action == "menu" {
		return d.showReminderMenu(ctx, ev)
	}
	if value, ok := strings.CutPrefix(action, "menu:cancel:"); ok {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return d.showReminderMenu(ctx, ev, menuExpiredText)
		}
		return d.cancelReminder(ctx, ev, id, true)
	}
	fields := strings.Fields(action)
	if len(fields) == 2 && (strings.EqualFold(fields[0], "cancel") || strings.EqualFold(fields[0], "delete")) {
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil && id > 0 {
			return d.cancelReminder(ctx, ev, id, false)
		}
	}
	return d.reply(ctx, ev.ChatID, RemindersUsage)
}

func (d *Dispatcher) cancelReminder(ctx context.Context, ev Event, id int64, menu bool) error {
	cancelled, err := store.CancelReminder(ctx, d.deps.DB, ev.ChatID, id, d.now())
	if err != nil {
		return err
	}
	if menu {
		if !cancelled {
			return d.showReminderMenu(ctx, ev, menuExpiredText)
		}
		return d.showReminderMenu(ctx, ev, fmt.Sprintf("Напоминание #%d отменено.", id))
	}
	if !cancelled {
		return d.reply(ctx, ev.ChatID, fmt.Sprintf("Активное напоминание #%d в этом чате не найдено.", id))
	}
	return d.reply(ctx, ev.ChatID, fmt.Sprintf("Напоминание #%d отменено.", id))
}

func (d *Dispatcher) reminderMenuContent(ctx context.Context, ev Event, note ...string) (string, models.InlineKeyboardMarkup, error) {
	rows, err := store.ListReminders(ctx, d.deps.DB, ev.ChatID, store.ReminderListLimit)
	if err != nil {
		return "", models.InlineKeyboardMarkup{}, err
	}
	text := "Активных напоминаний нет.\n" + ReminderUsage
	if len(rows) > 0 {
		lines := []string{fmt.Sprintf("Активные напоминания (до %d):", store.ReminderListLimit)}
		for _, reminder := range rows {
			schedule := reminder.NextRunAt.In(reminderLocation(d)).Format("2006-01-02 15:04 MST")
			if reminder.Cadence == store.ReminderCadenceDaily {
				schedule = "каждый день " + reminder.DailyTime + " " + reminder.NextRunAt.In(reminderLocation(d)).Format("MST")
			}
			lines = append(lines, fmt.Sprintf("#%d · %s · %s", reminder.ID, schedule, brief(reminder.Text, 100)))
		}
		text = strings.Join(lines, "\n")
	}
	return menuText(text, note), reminderMenuMarkup(rows), nil
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
	rows := [][]models.InlineKeyboardButton{}
	for _, reminder := range reminders {
		rows = append(rows, menuRow(menuButton(fmt.Sprintf("Отменить #%d · %s", reminder.ID, brief(reminder.Text, 30)),
			fmt.Sprintf("menu:reminder:cancel:%d", reminder.ID))))
	}
	rows = append(rows,
		menuRow(menuButton("Обновить", "menu:reminders")),
		menuRow(backButton("menu:more")),
	)
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
