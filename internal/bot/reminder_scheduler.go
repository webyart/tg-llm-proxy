package bot

import (
	"context"
	"errors"
	"sync"
	"time"

	"bot/internal/store"
)

const (
	ReminderPollInterval    = time.Second
	ReminderBatchLimit      = 20
	reminderWorkerLimit     = 4
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
	passDone := make(chan struct{})
	running := false
	pending := false
	startPass := func(now time.Time) {
		if ctx.Err() != nil {
			return
		}
		if running {
			pending = true
			return
		}
		running = true
		done := make(chan struct{})
		passDone = done
		go func() {
			d.processDueReminders(ctx, now, ReminderBatchLimit)
			close(done)
		}()
	}
	startPass(d.now())
	for {
		select {
		case <-ctx.Done():
			if running {
				<-passDone
			}
			return
		case <-passDone:
			running = false
			if pending {
				pending = false
				startPass(d.now())
			}
		case now := <-ticker.C:
			startPass(now)
		}
	}
}

func (d *Dispatcher) ProcessDueReminders(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 || limit > ReminderBatchLimit {
		limit = ReminderBatchLimit
	}
	type reminderJob struct {
		index    int
		reminder store.Reminder
	}
	jobs := make(chan reminderJob)
	errs := make([]error, limit)
	workerCount := reminderWorkerLimit
	if limit < workerCount {
		workerCount = limit
	}
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			for job := range jobs {
				errs[job.index] = d.processClaimedReminder(ctx, job.reminder, now)
			}
		}()
	}

	processed := 0
	var claimErr error
claimLoop:
	for processed < limit {
		if ctx.Err() != nil {
			claimErr = ctx.Err()
			break
		}
		reminder, found, err := store.ClaimNextReminder(ctx, d.deps.DB, now, ReminderClaimLease)
		if err != nil {
			claimErr = err
			break
		}
		if !found {
			break
		}
		job := reminderJob{index: processed, reminder: reminder}
		processed++
		select {
		case jobs <- job:
		case <-ctx.Done():
			claimErr = ctx.Err()
			break claimLoop
		}
	}
	close(jobs)
	workers.Wait()
	if claimErr != nil {
		return processed, claimErr
	}
	for _, err := range errs {
		if err != nil {
			return processed, err
		}
	}
	return processed, nil
}

func (d *Dispatcher) processClaimedReminder(ctx context.Context, reminder store.Reminder, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !d.deps.Cfg.Allows(reminder.UserID) {
		if _, err := store.CancelClaimedReminder(ctx, d.deps.DB, reminder, now); err != nil {
			return err
		}
		d.deps.Logger.Info("reminder cancelled because its owner is no longer allowed", "reminder_id", reminder.ID, "chat_id", reminder.ChatID, "user_id", reminder.UserID)
		return nil
	}

	sendCtx, cancel := context.WithTimeout(ctx, ReminderSendTimeout)
	err := d.sendPlainReminder(sendCtx, reminder.ChatID, "Напоминание: "+reminder.Text)
	cancel()
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return ctx.Err()
		}
		exhausted := reminder.Attempts >= ReminderMaxSendAttempts
		var dailyNext time.Time
		if exhausted && reminder.Cadence == store.ReminderCadenceDaily {
			dailyNext, err = AdvanceDailyReminder(reminder.NextRunAt, now, reminder.DailyTime, reminderLocation(d))
			if err != nil {
				return err
			}
		}
		if _, retryErr := store.RetryReminder(ctx, d.deps.DB, reminder, now, now.Add(ReminderRetryDelay(reminder.Attempts)), exhausted, dailyNext); retryErr != nil {
			return retryErr
		}
		d.deps.Logger.Warn("reminder delivery failed", "reminder_id", reminder.ID, "chat_id", reminder.ChatID,
			"attempt", reminder.Attempts, "exhausted", exhausted, "error", Sanitize(err, d.deps.Token))
		return nil
	}

	var dailyNext time.Time
	if reminder.Cadence == store.ReminderCadenceDaily {
		dailyNext, err = AdvanceDailyReminder(reminder.NextRunAt, now, reminder.DailyTime, reminderLocation(d))
		if err != nil {
			return err
		}
	}
	_, err = store.CompleteReminder(ctx, d.deps.DB, reminder, now, dailyNext)
	return err
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
