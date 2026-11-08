package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bot/internal/config"
	"bot/internal/model"
	"bot/internal/store"
)

// quotaConfig — the daily budget of the test user, with the allowlist entry the dispatcher needs.
func quotaConfig(t *testing.T, modelLimit string) config.Config {
	t.Helper()
	var overrides = map[string]string{}
	overrides["TELEGRAM_ALLOWED_USERS"] = "11"
	overrides["USER_DAILY_MODEL_LIMIT"] = modelLimit
	overrides["USER_DAILY_NETWORK_TOOL_LIMIT"] = "5"
	return testConfig(t, overrides)
}

// TestFailedModelCallReturnsTheDailyQuota: a provider outage used to consume the user's whole day. The
// reservation is returned when nothing reached the user.
func TestFailedModelCallReturnsTheDailyQuota(t *testing.T) {
	var cfg = quotaConfig(t, "2")
	var fake = &fakeModel{err: errors.New("provider is down")}
	var d = newTestDispatcher(t, cfg, fake, &fakeReplier{}, nil)

	for i := 0; i < 4; i++ {
		if err := d.Dispatch(context.Background(), testEvent(900, 11, int64(100+i), "/ask вопрос")); err != nil {
			t.Fatalf("dispatch %d: %v", i+1, err)
		}
	}
	if got := fake.callCount(); got != 4 {
		t.Fatalf("model calls = %d, want 4: the daily limit refused a call that cost nothing", got)
	}
	var used, err = store.DailyQuotaUsed(context.Background(), d.deps.DB, 11, d.now(), store.DailyQuotaModel)
	if err != nil {
		t.Fatalf("DailyQuotaUsed: %v", err)
	}
	if used != 0 {
		t.Fatalf("daily quota used = %d after four failed calls, want 0", used)
	}
}

// TestSuccessfulModelCallKeepsItsReservation keeps the refund honest: an answer that reached the user does
// spend one unit of the daily budget.
func TestSuccessfulModelCallKeepsItsReservation(t *testing.T) {
	var cfg = quotaConfig(t, "2")
	var fake = &fakeModel{response: model.Response{Content: "ответ"}}
	var d = newTestDispatcher(t, cfg, fake, &fakeReplier{}, nil)

	if err := d.Dispatch(context.Background(), testEvent(901, 11, 200, "/ask вопрос")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	var used, err = store.DailyQuotaUsed(context.Background(), d.deps.DB, 11, d.now(), store.DailyQuotaModel)
	if err != nil || used != 1 {
		t.Fatalf("daily quota used = %d err=%v, want 1", used, err)
	}
}

// TestUsageReportsTheQuotaCounters is the second half of the fix: the numbers in /usage must be the ones the
// enforcement reads, not a count of stored messages that can disagree with it.
func TestUsageReportsTheQuotaCounters(t *testing.T) {
	var cfg = quotaConfig(t, "5")
	var d = newTestDispatcher(t, cfg, &fakeModel{}, &fakeReplier{}, nil)
	if _, err := store.ReserveDailyQuota(context.Background(), d.deps.DB, 11, d.now(), store.DailyQuotaModel, 5); err != nil {
		t.Fatalf("ReserveDailyQuota: %v", err)
	}
	if _, err := store.ReserveDailyQuota(context.Background(), d.deps.DB, 11, d.now(), store.DailyQuotaNetworkTools, 5); err != nil {
		t.Fatalf("ReserveDailyQuota network: %v", err)
	}

	var text, err = d.dailyUsageText(context.Background(), 11)
	if err != nil {
		t.Fatalf("dailyUsageText: %v", err)
	}
	for _, want := range []string{
		"Модель: лимит 5; использовано 1; осталось 4",
		"Сетевые инструменты: лимит 5; использовано 1; осталось 4",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("/usage does not report the enforced counters %q:\n%s", want, text)
		}
	}
}
