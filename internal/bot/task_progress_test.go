package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"bot/internal/store"
	"bot/internal/testutil"
)

type statusMessage struct {
	chatID    int64
	messageID int64
	text      string
}

// recordingStatusReplier is the optional transport capability of live task progress, with the plain-send
// behaviour of fakeReplier behind it. It records the single created message and every rewrite.
type recordingStatusReplier struct {
	*fakeReplier
	mu         sync.Mutex
	nextID     int64
	statusSent []statusMessage
	edits      []statusMessage
	sendErr    error
	editErr    error
}

func newTaskStatusReplier() *recordingStatusReplier {
	return &recordingStatusReplier{fakeReplier: &fakeReplier{}, nextID: 500}
}

func (r *recordingStatusReplier) SendTaskStatus(ctx context.Context, chatID int64, text string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sendErr != nil {
		return 0, r.sendErr
	}
	r.nextID++
	message := statusMessage{chatID: chatID, messageID: r.nextID, text: text}
	r.statusSent = append(r.statusSent, message)
	r.fakeReplier.mu.Lock()
	r.fakeReplier.sent = append(r.fakeReplier.sent, sentMessage{chatID: chatID, text: text})
	r.fakeReplier.mu.Unlock()
	return message.messageID, nil
}

func (r *recordingStatusReplier) EditTaskStatus(ctx context.Context, chatID int64, messageID int64, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.editErr != nil {
		return r.editErr
	}
	r.edits = append(r.edits, statusMessage{chatID: chatID, messageID: messageID, text: text})
	return nil
}

func (r *recordingStatusReplier) statusMessages() []statusMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]statusMessage{}, r.statusSent...)
}

func (r *recordingStatusReplier) editMessages() []statusMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]statusMessage{}, r.edits...)
}

func TestTaskProgressCreatesAndReusesOneStatusMessage(t *testing.T) {
	m := taskTextModel(validTaskPlanTwo, "first result", "second result", "consolidated answer")
	r := newTaskStatusReplier()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 1101, "/task compare two ideas")); err != nil {
		t.Fatal(err)
	}

	sent := r.statusMessages()
	if len(sent) != 1 {
		t.Fatalf("creation sent %d status messages, want exactly one: %+v", len(sent), sent)
	}
	created, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found {
		t.Fatalf("CurrentTask found=%v err=%v", found, err)
	}
	if created.StatusMessageID != sent[0].messageID {
		t.Fatalf("stored status_message_id=%d, want the creation message %d", created.StatusMessageID, sent[0].messageID)
	}
	if !strings.Contains(sent[0].text, "Задача #1 создана") || !strings.Contains(sent[0].text, "Gather the relevant facts") {
		t.Fatalf("creation message lost the plan: %q", sent[0].text)
	}

	processTasks(t, d, 3)
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusCompleted {
		t.Fatalf("completed task=%+v found=%v err=%v", current, found, err)
	}
	edits := r.editMessages()
	if len(edits) < 2 {
		t.Fatalf("status message was edited %d time(s), want a step transition and the final line: %+v", len(edits), edits)
	}
	for _, edit := range edits {
		if edit.messageID != sent[0].messageID {
			t.Fatalf("an edit targeted message %d instead of the one status message %d: %+v", edit.messageID, sent[0].messageID, edits)
		}
	}
	first := edits[0].text
	for _, want := range []string{"Задача #1", "шаг 1/2", "Gather the relevant facts", "идёт", "осталось", "токены"} {
		if !strings.Contains(first, want) {
			t.Errorf("first progress edit misses %q: %q", want, first)
		}
	}
	final := edits[len(edits)-1].text
	if !strings.Contains(final, "Задача #1 завершена (2/2)") {
		t.Errorf("final status line misses the completion: %q", final)
	}
	if strings.Count(final, "\n") < 1 {
		t.Errorf("final completion must be a separate line appended to the last progress line: %q", final)
	}
	// The creation message is the only status send; the final answer is a normal separate reply.
	if len(r.statusMessages()) != 1 {
		t.Fatalf("the task sent %d status messages, want no new messages on step transitions", len(r.statusMessages()))
	}
	if !strings.Contains(r.joined(), "consolidated answer") {
		t.Fatalf("the final answer was not delivered: %q", r.joined())
	}
}

func TestTaskProgressEditsAreThrottledButTerminalIsAlwaysDelivered(t *testing.T) {
	ctx := context.Background()
	r := newTaskStatusReplier()
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), nil, r, testStore(t))
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	task := store.Task{
		ID: 7, ChatID: 100, UserID: 11, Status: store.TaskStatusRunning, NextStep: 0,
		StatusMessageID: 900, CreatedAt: now.Add(-30 * time.Second),
	}
	plan := TaskPlan{Steps: []TaskPlanStep{{Description: "first"}, {Description: "second"}}}

	d.refreshTaskStatus(ctx, &task, plan, 0, 0, now, false)
	if got := len(r.editMessages()); got != 1 {
		t.Fatalf("first transition edits=%d, want 1", got)
	}
	d.refreshTaskStatus(ctx, &task, plan, 0, 1, now.Add(time.Second), false)
	if got := len(r.editMessages()); got != 1 {
		t.Fatalf("an edit inside the 15 s window was delivered: edits=%d", got)
	}
	d.refreshTaskStatus(ctx, &task, plan, 1, 1, now.Add(16*time.Second), false)
	if got := len(r.editMessages()); got != 2 {
		t.Fatalf("an edit after the window was not delivered: edits=%d", got)
	}
	task.Status = store.TaskStatusCompleted
	task.NextStep = 2
	d.finishTaskStatus(ctx, task, plan, "Задача #7 завершена (2/2).", now.Add(17*time.Second))
	if got := len(r.editMessages()); got != 3 {
		t.Fatalf("the terminal line was throttled: edits=%d", got)
	}
	if !strings.Contains(r.editMessages()[2].text, "Задача #7 завершена (2/2).") {
		t.Fatalf("terminal edit text=%q", r.editMessages()[2].text)
	}
}

func TestTaskProgressEditFailureDoesNotFailTheTask(t *testing.T) {
	m := taskTextModel(validTaskPlanOne, "step result", "final answer")
	r := newTaskStatusReplier()
	r.editErr = errors.New("telegram rejected the edit")
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 1102, "/task one bounded step")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusCompleted {
		t.Fatalf("a failed progress edit changed the task: task=%+v found=%v err=%v", current, found, err)
	}
	if !strings.Contains(r.joined(), "final answer") {
		t.Fatalf("the final answer was lost after an edit failure: %q", r.joined())
	}
	if len(r.statusMessages()) != 1 || len(r.editMessages()) != 0 {
		t.Fatalf("status sends=%d edits=%d", len(r.statusMessages()), len(r.editMessages()))
	}
}

func TestTaskProgressStatusSendFailureFallsBackToAReplayReply(t *testing.T) {
	m := taskTextModel(validTaskPlanOne, "step result", "final answer")
	r := newTaskStatusReplier()
	r.sendErr = errors.New("telegram send rejected")
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 1103, "/task one bounded step")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.joined(), "Задача #1 создана") {
		t.Fatalf("a failed status send did not fall back to the ordinary reply: %q", r.joined())
	}
	processTasks(t, d, 2)
	current, found, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || !found || current.Status != store.TaskStatusCompleted {
		t.Fatalf("task=%+v found=%v err=%v", current, found, err)
	}
	if current.StatusMessageID != 0 {
		t.Fatalf("a failed send stored a status message id %d", current.StatusMessageID)
	}
}

func TestTaskProgressKeepsOneStatusMessageWithTheLiveScheduler(t *testing.T) {
	m := taskTextModel(validTaskPlanOne, "step result", "final answer")
	r := newTaskStatusReplier()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}), m, r, db)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.RunTaskScheduler(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("task scheduler did not stop")
		}
	}()

	if err := dispatch(t, d, testEvent(100, 11, 1104, "/task one live step")); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 2*time.Second, func() bool {
		current, found, err := store.CurrentTask(context.Background(), db, 100)
		return err == nil && found && current.Status == store.TaskStatusCompleted
	}, func() string { return "task did not complete under the live scheduler" })

	sent := r.statusMessages()
	if len(sent) != 1 {
		t.Fatalf("the live scheduler created %d status messages, want exactly one: %+v", len(sent), sent)
	}
	current, _, err := store.CurrentTask(context.Background(), db, 100)
	if err != nil || current.StatusMessageID != sent[0].messageID {
		t.Fatalf("stored status_message_id=%d, want %d (err=%v)", current.StatusMessageID, sent[0].messageID, err)
	}
	for _, edit := range r.editMessages() {
		if edit.messageID != sent[0].messageID {
			t.Fatalf("an edit targeted message %d instead of the creation message %d", edit.messageID, sent[0].messageID)
		}
	}
}
