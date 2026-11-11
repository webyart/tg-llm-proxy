package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestClaimPlanningTaskByIDClaimsOnlyTheNamedRow is the per-chat preference of the planner: a worker woken
// for one row claims exactly that row and never another chat's older row. The same lease rules apply: a live
// lease is refused, an expired one is reclaimable, and a terminal row is not claimable at all.
func TestClaimPlanningTaskByIDClaimsOnlyTheNamedRow(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Now().UTC()
	first := seedPlanningTask(t, db, 100, now)
	second := seedPlanningTask(t, db, 101, now)

	claimed, found, err := ClaimPlanningTaskByID(ctx, db, second.ID, now, "instance-b", time.Minute)
	if err != nil || !found || claimed.ID != second.ID || claimed.PlanningOwner != "instance-b" ||
		claimed.PlanningLeaseUntil == nil {
		t.Fatalf("claim by id=%+v found=%v err=%v, want the named second row leased to instance-b", claimed, found, err)
	}
	untouched, found, err := CurrentTask(ctx, db, 100)
	if err != nil || !found || untouched.ID != first.ID || untouched.PlanningOwner != "" || untouched.PlanningLeaseUntil != nil {
		t.Fatalf("the other chat's row=%+v found=%v err=%v, want it untouched", untouched, found, err)
	}

	// A live lease is not claimable by id, however the caller words it.
	if _, found, err := ClaimPlanningTaskByID(ctx, db, second.ID, now.Add(time.Second), "instance-c", time.Minute); err != nil || found {
		t.Fatalf("a live lease was claimed by id: found=%v err=%v", found, err)
	}
	// The only free row is still the first chat's: the fair fallback serves it, not the leased one.
	fair, found, err := ClaimPlanningTaskFair(ctx, db, now.Add(time.Second), "instance-c", time.Minute)
	if err != nil || !found || fair.ID != first.ID || fair.PlanningOwner != "instance-c" {
		t.Fatalf("fair fallback=%+v found=%v err=%v, want the free first row", fair, found, err)
	}
	if saved, err := SaveTaskPlan(ctx, db, first.ID, testTaskPlan, now.Add(2*time.Second)); err != nil || !saved {
		t.Fatalf("SaveTaskPlan changed=%v err=%v", saved, err)
	}
	if _, found, err := ClaimPlanningTaskByID(ctx, db, first.ID, now.Add(3*time.Second), "instance-d", time.Minute); err != nil || found {
		t.Fatalf("a terminal row was claimed by id: found=%v err=%v", found, err)
	}
	if _, found, err := ClaimPlanningTaskByID(ctx, db, 999999, now, "instance-d", time.Minute); err != nil || found {
		t.Fatalf("an unknown row was claimed: found=%v err=%v", found, err)
	}
	if _, _, err := ClaimPlanningTaskByID(ctx, db, 0, now, "instance-d", time.Minute); err == nil {
		t.Fatal("a claim by id without a task was accepted")
	}

	// The lease rules are the shared ones: an expired lease of another owner is reclaimable by id.
	third := seedPlanningTask(t, db, 102, now)
	if _, found, err := ClaimPlanningTaskByID(ctx, db, third.ID, now, "instance-old", 100*time.Millisecond); err != nil || !found {
		t.Fatalf("initial claim found=%v err=%v", found, err)
	}
	time.Sleep(150 * time.Millisecond)
	reclaimed, found, err := ClaimPlanningTaskByID(ctx, db, third.ID, time.Now().UTC(), "instance-new", time.Minute)
	if err != nil || !found || reclaimed.ID != third.ID || reclaimed.PlanningOwner != "instance-new" {
		t.Fatalf("expired lease reclaim=%+v found=%v err=%v", reclaimed, found, err)
	}
}

// TestClaimPlanningTaskFairServesTheLeastRecentlyUpdatedChat pins the documented fallback rule: of the free
// planning rows, the one with the oldest updated_at wins (ties by chat_id then id), and a claim moves the
// served chat to the back by writing updated_at = now. With one active row per chat this is least-recently-
// served order over chats, so no chat can be served twice while another chat waits.
func TestClaimPlanningTaskFairServesTheLeastRecentlyUpdatedChat(t *testing.T) {
	ctx := context.Background()
	db := taskTestDB(t)
	now := time.Now().UTC()
	rows := map[int64]Task{}
	for _, chatID := range []int64{100, 101, 102} {
		rows[chatID] = seedPlanningTask(t, db, chatID, now)
	}
	// Chat 102 has waited longest, then 100, then 101.
	stamps := map[int64]time.Time{
		102: now.Add(-3 * time.Second),
		100: now.Add(-2 * time.Second),
		101: now.Add(-1 * time.Second),
	}
	for chatID, stamp := range stamps {
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = ? WHERE id = ?`,
			formatReminderTime(stamp), rows[chatID].ID); err != nil {
			t.Fatalf("backdate chat %d: %v", chatID, err)
		}
	}

	var order []int64
	for index := 0; index < 3; index++ {
		owner := fmt.Sprintf("instance-%d", index)
		claimed, found, err := ClaimPlanningTaskFair(ctx, db, now, owner, time.Minute)
		if err != nil || !found {
			t.Fatalf("fair claim %d found=%v err=%v", index, found, err)
		}
		order = append(order, claimed.ChatID)
		// Releasing without a terminal transition models an interrupted attempt: the row stays planning,
		// but its claim already moved it behind the chats that have not been served.
		if released, err := ReleasePlanningLease(ctx, db, claimed.ID, owner); err != nil || !released {
			t.Fatalf("release %d changed=%v err=%v", index, released, err)
		}
	}
	want := []int64{102, 100, 101}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("fair service order=%v, want %v", order, want)
		}
	}
}
