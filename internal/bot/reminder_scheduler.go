package bot

import (
	"context"
	"errors"
	"time"

	"bot/internal/store"
)

const (
	ReminderPollInterval    = time.Second
	ReminderBatchLimit      = 20
	ReminderClaimLease      = 30 * time.Second
	ReminderSendTimeout     = 10 * time.Second
	ReminderMaxSendAttempts = 5
	ReminderRetryBase       = 2 * time.Second
	ReminderRetryMax        = time.Minute
)

func ReminderRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := ReminderRetryBase
	for i := 1; i < attempt && delay < ReminderRetryMax; i++ {
		delay *= 2
	}
	if delay > ReminderRetryMax {
		return ReminderRetryMax
	}
	return delay
}

func (d *Dispatcher) RunReminderScheduler(ctx context.Context) {
	ticker := time.NewTicker(ReminderPollInterval)
	defer ticker.Stop()
	d.processDueReminders(ctx, d.now(), ReminderBatchLimit)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			d.processDueReminders(ctx, now, ReminderBatchLimit)
		}
	}
}

func (d *Dispatcher) ProcessDueReminders(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > ReminderBatchLimit {
		limit = ReminderBatchLimit
	}
	processed := 0
	for i := 0; i < limit; i++ {
		if ctx.Err() != nil {
			return processed, ctx.Err()
		}
		reminder, found, err := store.ClaimNextReminder(ctx, d.deps.DB, now, ReminderClaimLease)
		if err != nil {
			return processed, err
		}
		if !found {
			return processed, nil
		}
		processed++
		if !d.deps.Cfg.Allows(reminder.UserID) {
			if _, err := store.CancelClaimedReminder(ctx, d.deps.DB, reminder, now); err != nil {
				return processed, err
			}
			d.deps.Logger.Info("reminder cancelled because its owner is no longer allowed", "reminder_id", reminder.ID, "chat_id", reminder.ChatID, "user_id", reminder.UserID)
			continue
		}

		sendCtx, cancel := context.WithTimeout(ctx, ReminderSendTimeout)
		err = d.sendPlainReminder(sendCtx, reminder.ChatID, "Напоминание: "+reminder.Text)
		cancel()
		if err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return processed, ctx.Err()
			}
			exhausted := reminder.Attempts >= ReminderMaxSendAttempts
			var dailyNext time.Time
			if exhausted && reminder.Cadence == store.ReminderCadenceDaily {
				dailyNext, err = AdvanceDailyReminder(reminder.NextRunAt, now, reminder.DailyTime, reminderLocation(d))
				if err != nil {
					return processed, err
				}
			}
			if _, retryErr := store.RetryReminder(ctx, d.deps.DB, reminder, now, now.Add(ReminderRetryDelay(reminder.Attempts)), exhausted, dailyNext); retryErr != nil {
				return processed, retryErr
			}
			d.deps.Logger.Warn("reminder delivery failed", "reminder_id", reminder.ID, "chat_id", reminder.ChatID,
				"attempt", reminder.Attempts, "exhausted", exhausted, "error", Sanitize(err, d.deps.Token))
			continue
		}

		var dailyNext time.Time
		if reminder.Cadence == store.ReminderCadenceDaily {
			dailyNext, err = AdvanceDailyReminder(reminder.NextRunAt, now, reminder.DailyTime, reminderLocation(d))
			if err != nil {
				return processed, err
			}
		}
		if _, err := store.CompleteReminder(ctx, d.deps.DB, reminder, now, dailyNext); err != nil {
			return processed, err
		}
	}
	return processed, nil
}

func (d *Dispatcher) processDueReminders(ctx context.Context, now time.Time, limit int) {
	if _, err := d.ProcessDueReminders(ctx, now, limit); err != nil && !errors.Is(err, context.Canceled) {
		d.deps.Logger.Error("reminder scheduler pass failed", "error", Sanitize(err, d.deps.Token))
	}
}

func (d *Dispatcher) sendPlainReminder(ctx context.Context, chatID int64, text string) error {
	if sender, ok := d.deps.Replier.(interface {
		SendPlain(context.Context, int64, string) error
	}); ok {
		return sender.SendPlain(ctx, chatID, text)
	}
	return d.deps.Replier.Send(ctx, chatID, text)
}
