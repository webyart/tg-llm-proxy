package bot

import (
	"context"
	"errors"
	"testing"

	"bot/internal/store"
)

// TestLinksAddSpendsTheDailyNetworkQuota — /links add reads the page, so it must reserve the same daily
// network budget as read_url and web_search. The second add over the limit is refused before any fetch.
func TestLinksAddSpendsTheDailyNetworkQuota(t *testing.T) {
	fixture := newSavedLinkTestServer(t)
	cfg := testConfig(t, map[string]string{
		"URL_READER_ENABLED": "true", "USER_DAILY_NETWORK_TOOL_LIMIT": "1",
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, nil, db)
	d.deps.URLReader = fixture.client
	ctx := context.Background()

	if _, _, err := d.CreateLink(ctx, 194, 11, fixture.url+"/start"); err != nil {
		t.Fatalf("the first /links add must fit the quota: %v", err)
	}
	var lookedUp = fixture.resolver.calls
	if _, _, err := d.CreateLink(ctx, 194, 11, fixture.url+"/start#second"); !errors.Is(err, errLinkDailyLimit) {
		t.Fatalf("the second /links add error = %v, want errLinkDailyLimit", err)
	}
	if fixture.resolver.calls != lookedUp {
		t.Fatalf("the refused add reached the network: %d DNS lookups after %d", fixture.resolver.calls, lookedUp)
	}
	used, err := store.DailyQuotaUsed(ctx, db, 11, d.now().UTC(), store.DailyQuotaNetworkTools)
	if err != nil || used != 1 {
		t.Fatalf("daily network quota after one successful add = %d err=%v, want 1", used, err)
	}
}

// TestLinksAddReleasesTheQuotaWhenTheReadFails — a failed read did not buy a page, so it must not spend the
// user's daily budget.
func TestLinksAddReleasesTheQuotaWhenTheReadFails(t *testing.T) {
	fixture := newSavedLinkTestServer(t)
	cfg := testConfig(t, map[string]string{
		"URL_READER_ENABLED": "true", "USER_DAILY_NETWORK_TOOL_LIMIT": "1",
	})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, nil, db)
	d.deps.URLReader = fixture.client
	ctx := context.Background()

	if _, _, err := d.CreateLink(ctx, 195, 12, fixture.url+"/error"); err == nil {
		t.Fatal("the failing read must return an error")
	}
	used, err := store.DailyQuotaUsed(ctx, db, 12, d.now().UTC(), store.DailyQuotaNetworkTools)
	if err != nil || used != 0 {
		t.Fatalf("a failed read spent the daily quota: used=%d err=%v", used, err)
	}
	if _, _, err := d.CreateLink(ctx, 195, 12, fixture.url+"/start"); err != nil {
		t.Fatalf("the refunded quota must allow a later successful add: %v", err)
	}
}
