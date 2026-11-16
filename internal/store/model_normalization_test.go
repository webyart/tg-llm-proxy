package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestNormalizeModelNameRewritesOnlyTheStaleLiveSelections — discovery changed the model a single-model endpoint
// serves; the persisted chat selection and every live task that copied the old name must follow, while another
// chat's selection, terminal tasks and historical messages stay as they were.
func TestNormalizeModelNameRewritesOnlyTheStaleLiveSelections(t *testing.T) {
	var ctx = context.Background()
	var db = taskTestDB(t)
	var now = time.Now().UTC()
	for _, chatID := range []int64{100, 200, 300, 400, 500, 600, 700, 800, 900} {
		if err := EnsureChat(ctx, db, chatID, 11, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Chat 100 selected the model the endpoint used to serve; chat 200 selected a different one and must keep it.
	if err := SetChatModel(ctx, db, 100, "old-model"); err != nil {
		t.Fatal(err)
	}
	if err := SetChatModel(ctx, db, 200, "other-model"); err != nil {
		t.Fatal(err)
	}

	var newTask = func(chatID int64) Task {
		t.Helper()
		var task, err = CreateTask(ctx, db, Task{ChatID: chatID, UserID: 11, Goal: "live goal", Model: "old-model",
			PlanJSON: testTaskPlan, DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
		if err != nil {
			t.Fatalf("CreateTask(%d): %v", chatID, err)
		}
		return task
	}
	var setStatus = func(task Task, status string) Task {
		t.Helper()
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET status = ? WHERE id = ?`, status, task.ID); err != nil {
			t.Fatalf("status %q for task %d: %v", status, task.ID, err)
		}
		task.Status = status
		return task
	}

	// One active row per chat: the unique index allows no more, and every non-terminal status is covered.
	var planning, err = CreatePlanningTask(ctx, db, Task{ChatID: 300, UserID: 11, Goal: "plan goal", Model: "old-model",
		DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatalf("CreatePlanningTask: %v", err)
	}
	var ready = newTask(400)
	var running = setStatus(newTask(500), TaskStatusRunning)
	var awaiting = setStatus(newTask(600), TaskStatusAwaitingApproval)
	// An awaiting-approval row carries the old model inside its continuation snapshot too; restoring it checks
	// request.Model against tasks.model, so the normalization has to rewrite both.
	const awaitingContinuation = `{"user_id":11,"request":{"Model":"old-model","Messages":[],"Tools":null,"Timeout":0},` +
		`"response":{"Content":"partial"},"allowed_tools":{"calculator":true},"round":0,"approved_call":0,` +
		`"approved_calls":{"0":true},"original_request":"live goal"}`
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET continuation_json = ? WHERE id = ?`, awaitingContinuation, awaiting.ID); err != nil {
		t.Fatalf("continuation for the awaiting task: %v", err)
	}

	// Terminal rows are history: their model must survive the normalization.
	var completed = setStatus(newTask(700), TaskStatusCompleted)
	var failed = setStatus(newTask(800), TaskStatusFailed)
	var cancelled = setStatus(newTask(900), TaskStatusCancelled)

	// A historical message keeps the model that produced it.
	if _, err := Append(ctx, db, Message{ChatID: 100, UserID: 11, Role: RoleAssistant,
		Content: "historical answer", Model: "old-model"}); err != nil {
		t.Fatal(err)
	}

	var counts ModelNormalization
	counts, err = NormalizeModelName(ctx, db, "old-model", "loaded-model")
	if err != nil {
		t.Fatalf("NormalizeModelName: %v", err)
	}
	if counts.ChatSettings != 1 || counts.Tasks != 4 || counts.Continuations != 1 {
		t.Fatalf("normalized counts = %+v, want one chat selection, four active tasks and one continuation", counts)
	}

	var current, found, modelErr = ChatModel(ctx, db, 100)
	if modelErr != nil || !found || current != "loaded-model" {
		t.Fatalf("chat 100 model = %q found=%v err=%v, want the discovered model", current, found, modelErr)
	}
	if current, found, modelErr = ChatModel(ctx, db, 200); modelErr != nil || !found || current != "other-model" {
		t.Fatalf("chat 200 model = %q found=%v err=%v, want the other selection untouched", current, found, modelErr)
	}

	for name, task := range map[string]Task{"planning": planning, "ready": ready, "running": running, "awaiting": awaiting} {
		var stored, ok, readErr = CurrentTask(ctx, db, task.ChatID)
		if readErr != nil || !ok {
			t.Fatalf("%s task: found=%v err=%v", name, ok, readErr)
		}
		if stored.Model != "loaded-model" {
			t.Fatalf("%s task model = %q, want the discovered model", name, stored.Model)
		}
		if stored.Status != task.Status {
			t.Fatalf("%s task status = %q, the normalization changed the state", name, stored.Status)
		}
	}
	for name, task := range map[string]Task{"completed": completed, "failed": failed, "cancelled": cancelled} {
		var stored, ok, readErr = CurrentTask(ctx, db, task.ChatID)
		if readErr != nil || !ok {
			t.Fatalf("%s task: found=%v err=%v", name, ok, readErr)
		}
		if stored.Model != "old-model" {
			t.Fatalf("%s task model = %q, want the historical name kept", name, stored.Model)
		}
	}

	// The pending approval's snapshot must name the same model as its normalized row, and keep every other
	// field (the goal) intact.
	storedAwaiting, ok, readErr := CurrentTask(ctx, db, awaiting.ChatID)
	if readErr != nil || !ok {
		t.Fatalf("awaiting task: found=%v err=%v", ok, readErr)
	}
	var snapshot struct {
		Request struct {
			Model string
		}
		OriginalRequest string `json:"original_request"`
	}
	if err := json.Unmarshal([]byte(storedAwaiting.ContinuationJSON), &snapshot); err != nil {
		t.Fatalf("continuation JSON after normalization: %v", err)
	}
	if snapshot.Request.Model != "loaded-model" {
		t.Fatalf("continuation request model = %q, want the discovered model", snapshot.Request.Model)
	}
	if snapshot.OriginalRequest != "live goal" {
		t.Fatalf("continuation original_request = %q, the normalization changed another field", snapshot.OriginalRequest)
	}

	var messageModel string
	if err := db.QueryRowContext(ctx, `SELECT model FROM messages WHERE chat_id = 100`).Scan(&messageModel); err != nil {
		t.Fatal(err)
	}
	if messageModel != "old-model" {
		t.Fatalf("historical message model = %q, want the historical name kept", messageModel)
	}

	// Idempotent: nothing matches the old name any more.
	if counts, err = NormalizeModelName(ctx, db, "old-model", "loaded-model"); err != nil || counts != (ModelNormalization{}) {
		t.Fatalf("second normalization = %+v err=%v, want a no-op", counts, err)
	}
}

func TestNormalizeModelNameIsANoOpForTheSameOrInvalidName(t *testing.T) {
	var ctx = context.Background()
	var db = taskTestDB(t)
	if err := EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	if err := SetChatModel(ctx, db, 100, "same-model"); err != nil {
		t.Fatal(err)
	}
	if counts, err := NormalizeModelName(ctx, db, "same-model", "same-model"); err != nil || counts != (ModelNormalization{}) {
		t.Fatalf("same-name normalization = %+v err=%v, want no-op", counts, err)
	}
	if _, err := NormalizeModelName(ctx, db, "", "loaded-model"); err == nil {
		t.Fatal("an empty old name was accepted")
	}
	if _, err := NormalizeModelName(ctx, db, "same-model", " "); err == nil {
		t.Fatal("an empty new name was accepted")
	}
	if current, _, err := ChatModel(ctx, db, 100); err != nil || current != "same-model" {
		t.Fatalf("a rejected normalization changed the selection: %q err=%v", current, err)
	}
}

// TestNormalizeContinuationModelRewritesOnlyTheExactOldName — the continuation is a snapshot of the whole request
// loop, so the rewrite must touch request.Model and nothing else, and must not invent a change for a different or
// unparsable snapshot (the restore path already rejects those).
func TestNormalizeContinuationModelRewritesOnlyTheExactOldName(t *testing.T) {
	var rewritten, changed = normalizeContinuationModel(
		`{"user_id":11,"request":{"Model":"old-model","Messages":[{"Role":"user","Content":"hi"}],"Timeout":0},`+
			`"response":{"Content":"partial"},"original_request":"goal"}`, "old-model", "loaded-model")
	if !changed {
		t.Fatal("an exact old model in the continuation was not rewritten")
	}
	var snapshot struct {
		Request struct {
			Model    string
			Messages []struct{ Role, Content string }
		}
		OriginalRequest string `json:"original_request"`
		Response        struct{ Content string }
	}
	if err := json.Unmarshal([]byte(rewritten), &snapshot); err != nil {
		t.Fatalf("rewritten continuation is not valid JSON: %v", err)
	}
	if snapshot.Request.Model != "loaded-model" || snapshot.OriginalRequest != "goal" ||
		snapshot.Response.Content != "partial" || len(snapshot.Request.Messages) != 1 ||
		snapshot.Request.Messages[0].Content != "hi" {
		t.Fatalf("rewritten continuation changed more than the model: %s", rewritten)
	}
	for _, untouched := range []string{
		``,
		`not json`,
		`{}`,
		`{"request":{}}`,
		`{"request":{"Model":"other-model"}}`,
		`{"request":{"Model":5}}`,
	} {
		if got, changed := normalizeContinuationModel(untouched, "old-model", "loaded-model"); changed || got != untouched {
			t.Fatalf("normalizeContinuationModel(%q) = %q changed=%v, want it untouched", untouched, got, changed)
		}
	}
}
