package bot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"bot/internal/model"
	"bot/internal/store"
)

type blockingAdmissionModel struct {
	started     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
	mu          sync.Mutex
	questions   []string
	calls       int
}

func newBlockingAdmissionModel() *blockingAdmissionModel {
	return &blockingAdmissionModel{started: make(chan struct{}), release: make(chan struct{})}
}

func (m *blockingAdmissionModel) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	m.mu.Lock()
	m.calls++
	if len(req.Messages) > 0 {
		m.questions = append(m.questions, req.Messages[len(req.Messages)-1].Content)
	}
	m.mu.Unlock()
	m.once.Do(func() { close(m.started) })
	select {
	case <-m.release:
		return model.Response{Content: "ответ", Model: "fake-model"}, nil
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
}

func (m *blockingAdmissionModel) Models(context.Context) ([]string, error) { return nil, nil }

func (m *blockingAdmissionModel) unblock() {
	m.releaseOnce.Do(func() { close(m.release) })
}

func (m *blockingAdmissionModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *blockingAdmissionModel) asked() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.questions...)
}

func waitAdmission(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("bounded admission condition was not reached within 5s")
}

func waitAdmissionGroup(t *testing.T, group *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded admission wait group did not finish within 5s")
	}
}

func awaitAdmissionSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("bounded admission signal did not arrive within 5s")
	}
}

func TestUpdateFloodBoundsExecutionAndKeepsAcceptedFIFO(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS":        "11",
		"MAX_CONCURRENT_MODEL_REQUESTS": "1",
		"RATE_LIMIT_PER_MINUTE":         "0",
	})
	m := newBlockingAdmissionModel()
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	t.Cleanup(m.unblock)
	baseline := runtime.NumGoroutine()

	var accepted sync.WaitGroup
	accepted.Add(1)
	go func() {
		defer accepted.Done()
		if err := d.Dispatch(context.Background(), testEvent(100, 11, 1, "вопрос 1")); err != nil {
			t.Errorf("first Dispatch: %v", err)
		}
	}()
	awaitAdmissionSignal(t, m.started)
	for id := int64(2); id <= perChatQueueCapacity+1; id++ {
		accepted.Add(1)
		go func(id int64) {
			defer accepted.Done()
			if err := d.Dispatch(context.Background(), testEvent(100, 11, id, fmt.Sprintf("вопрос %d", id))); err != nil {
				t.Errorf("queued Dispatch(%d): %v", id, err)
			}
		}(id)
		waitAdmission(t, func() bool { return d.deps.Locks.Waiting(100) == int(id-1) })
	}

	var rejected sync.WaitGroup
	for id := int64(perChatQueueCapacity + 2); id <= 1000; id++ {
		rejected.Add(1)
		go func(id int64) {
			defer rejected.Done()
			if err := d.Dispatch(context.Background(), testEvent(100, 11, id, "избыточный вопрос")); err != nil {
				t.Errorf("rejected Dispatch(%d): %v", id, err)
			}
		}(id)
	}
	waitAdmissionGroup(t, &rejected)
	if got := d.activeHandlers.Load(); got != 1 {
		t.Fatalf("active handlers = %d, want 1", got)
	}
	if got := m.callCount(); got != 1 {
		t.Fatalf("model calls before release = %d, want 1", got)
	}
	if got := r.count(); got != 1 || !strings.Contains(r.joined(), "очередь заполнена") {
		t.Fatalf("coalesced overload replies = %d, text = %q", got, r.joined())
	}
	if got := d.deps.Locks.Waiting(100); got != perChatQueueCapacity {
		t.Fatalf("per-chat waiters = %d, want %d", got, perChatQueueCapacity)
	}
	if got := runtime.NumGoroutine(); got > baseline+perChatQueueCapacity+4 {
		t.Fatalf("goroutines during flood = %d, baseline = %d, want no more than %d", got, baseline, baseline+perChatQueueCapacity+4)
	}

	m.unblock()
	waitAdmissionGroup(t, &accepted)
	if got := m.callCount(); got != perChatQueueCapacity+1 {
		t.Fatalf("accepted model calls = %d, want %d", got, perChatQueueCapacity+1)
	}
	questions := m.asked()
	for i, question := range questions {
		want := fmt.Sprintf("вопрос %d", i+1)
		if question != want {
			t.Fatalf("accepted FIFO question %d = %q, want %q (all: %v)", i, question, want, questions)
		}
	}
	offset, found, err := store.LastUpdateID(context.Background(), db)
	if err != nil || !found || offset != 2000 {
		t.Fatalf("high-water offset = %d found=%v err=%v, want 2000", offset, found, err)
	}
	waitAdmission(t, func() bool { return runtime.NumGoroutine() <= baseline+2 })
}

func TestQueueDeadlineRejectsBeforeModelSideEffects(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "MAX_CONCURRENT_MODEL_REQUESTS": "1"})
	m := newBlockingAdmissionModel()
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, testStore(t))
	t.Cleanup(m.unblock)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_ = d.Dispatch(context.Background(), testEvent(100, 11, 1, "первый"))
	}()
	awaitAdmissionSignal(t, m.started)
	if err := d.Dispatch(context.Background(), testEvent(100, 11, 2, "второй")); err != nil {
		t.Fatalf("expired queued Dispatch must be handled as overload: %v", err)
	}
	if got := m.callCount(); got != 1 {
		t.Fatalf("model calls = %d, want only the first", got)
	}
	if got := r.count(); got != 1 || !strings.Contains(r.joined(), "очередь заполнена") {
		t.Fatalf("queue deadline replies = %d, text = %q", got, r.joined())
	}
	m.unblock()
	awaitAdmissionSignal(t, firstDone)
}

func TestDeniedAndBootstrapRepliesUseSeparateBoundedWindows(t *testing.T) {
	cfg := testConfig(t, nil)
	clock := &fakeClockForBot{t: time.Unix(1700000000, 0)}
	r := &fakeReplier{}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, r, db)
	d.deniedReplies.WithClock(clock.Now)
	d.bootstrapReplies.WithClock(clock.Now)

	if err := d.Dispatch(context.Background(), testEvent(100, 99, 1, "/ask секрет")); err != nil {
		t.Fatal(err)
	}
	if err := d.Dispatch(context.Background(), testEvent(100, 99, 2, "/reset")); err != nil {
		t.Fatal(err)
	}
	if r.count() != 1 {
		t.Fatalf("denied replies = %d, want one coalesced refusal", r.count())
	}
	if denied, _ := store.CountEvents(context.Background(), db, store.EventDenied); denied != 1 {
		t.Fatalf("denied audit rows = %d, want 1", denied)
	}
	if commands, _ := store.CountEvents(context.Background(), db, store.EventCommand); commands != 0 {
		t.Fatalf("denied messages wrote %d command events before authorization", commands)
	}

	for _, text := range []string{"/whoami", "/start", "/whoami", "/start"} {
		if err := d.Dispatch(context.Background(), testEvent(100, 99, 3, text)); err != nil {
			t.Fatal(err)
		}
	}
	if r.count() != 3 {
		t.Fatalf("bootstrap replies = %d, want /whoami and /start once each", r.count())
	}
	clock.Advance(bootstrapReplyWindow)
	for _, text := range []string{"/whoami", "/start"} {
		if err := d.Dispatch(context.Background(), testEvent(100, 99, 4, text)); err != nil {
			t.Fatal(err)
		}
	}
	if r.count() != 5 {
		t.Fatalf("bootstrap replies after their window = %d, want 5", r.count())
	}
	clock.Advance(deniedReplyWindow - bootstrapReplyWindow)
	if err := d.Dispatch(context.Background(), testEvent(100, 99, 5, "/ask ещё")); err != nil {
		t.Fatal(err)
	}
	if r.count() != 6 {
		t.Fatalf("denied reply after its independent window = %d, want 6", r.count())
	}
}

type fakeClockForBot struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClockForBot) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClockForBot) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestUnauthorizedAlbumFloodHasNoTimersOrContentState(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	m := &fakeModel{response: model.Response{Content: "не должно быть"}}
	r := &fakeReplier{}
	downloader := &albumAttachmentDownloader{files: map[string]DownloadedAttachment{}}
	db := testStore(t)
	d := newTestDispatcher(t, cfg, m, r, db)
	d.deps.Attachments = downloader
	baseline := runtime.NumGoroutine()

	for id := int64(1); id <= 1000; id++ {
		ev := testEvent(100, 99, id, "секрет")
		ev.Attachment = &Attachment{Kind: AttachmentPhoto, FileID: strconv.FormatInt(id, 10)}
		ev.MediaGroupID = "unauthorized-" + strconv.FormatInt(id, 10)
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Fatalf("unauthorized Dispatch(%d): %v", id, err)
		}
	}
	d.albumMu.Lock()
	albums := len(d.albums)
	d.albumMu.Unlock()
	if albums != 0 || m.callCount() != 0 || len(downloader.callIDs()) != 0 {
		t.Fatalf("unauthorized work left albums=%d model calls=%d downloads=%d", albums, m.callCount(), len(downloader.callIDs()))
	}
	if r.count() != 1 {
		t.Fatalf("denied flood replies = %d, want one coalesced response", r.count())
	}
	counts, err := store.TableCounts(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]int64{}
	for _, count := range counts {
		rows[count.Name] = count.Rows
	}
	if rows["chats"] != 0 || rows["messages"] != 0 || rows["events"] != 1 || rows["bot_state"] != 1 {
		t.Fatalf("unauthorized state rows: chats=%d messages=%d events=%d bot_state=%d", rows["chats"], rows["messages"], rows["events"], rows["bot_state"])
	}
	if denied, _ := store.CountEvents(context.Background(), db, store.EventDenied); denied != 1 {
		t.Fatalf("denied audit rows = %d, want 1", denied)
	}
	if commands, _ := store.CountEvents(context.Background(), db, store.EventCommand); commands != 0 {
		t.Fatalf("denied command audit rows = %d, want 0", commands)
	}
	if offset, found, err := store.LastUpdateID(context.Background(), db); err != nil || !found || offset != 2000 {
		t.Fatalf("offset = %d found=%v err=%v, want 2000", offset, found, err)
	}
	waitAdmission(t, func() bool { return runtime.NumGoroutine() <= baseline+2 })
}

func TestAlbumCompletionUsesTheSameBoundedAdmission(t *testing.T) {
	cfg := testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS":        "11",
		"MAX_CONCURRENT_MODEL_REQUESTS": "1",
		"RATE_LIMIT_PER_MINUTE":         "0",
	})
	m := newBlockingAdmissionModel()
	r := &fakeReplier{}
	d := newTestDispatcher(t, cfg, m, r, testStore(t))
	files := map[string]DownloadedAttachment{}
	for id := 0; id < perChatQueueCapacity+2; id++ {
		files[strconv.Itoa(id)] = DownloadedAttachment{Data: []byte("photo"), MIME: "image/jpeg"}
	}
	downloader := &albumAttachmentDownloader{files: files}
	d.deps.Attachments = downloader
	t.Cleanup(m.unblock)

	for id := 0; id < perChatQueueCapacity+2; id++ {
		ev := testEvent(100, 11, int64(id+1), "альбом")
		ev.Attachment = &Attachment{Kind: AttachmentPhoto, FileID: strconv.Itoa(id)}
		ev.MediaGroupID = "album-" + strconv.Itoa(id)
		if err := d.Dispatch(context.Background(), ev); err != nil {
			t.Fatalf("collect album %d: %v", id, err)
		}
	}
	awaitAdmissionSignal(t, m.started)
	waitAdmission(t, func() bool { return d.deps.Locks.Waiting(100) == perChatQueueCapacity && r.count() == 1 })
	if got := d.activeHandlers.Load(); got != 1 {
		t.Fatalf("active album handlers = %d, want 1", got)
	}
	if got := m.callCount(); got != 1 || len(downloader.callIDs()) != 1 {
		t.Fatalf("before release model calls=%d downloads=%d, want one each", got, len(downloader.callIDs()))
	}
	m.unblock()
	waitAdmission(t, func() bool { return m.callCount() == perChatQueueCapacity+1 })
	if got := len(downloader.callIDs()); got != perChatQueueCapacity+1 {
		t.Fatalf("accepted album downloads = %d, want %d", got, perChatQueueCapacity+1)
	}
}

func TestNewTelegramProcessUpdateDoesNotSpawnPerUpdateGoroutine(t *testing.T) {
	var sends atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
			http.Error(w, "unexpected method", http.StatusNotFound)
			return
		}
		sends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":100,"type":"private"},"text":"pong"}}`))
	}))
	defer server.Close()
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "MAX_CONCURRENT_MODEL_REQUESTS": "1"})
	d := newTestDispatcher(t, cfg, nil, &fakeReplier{}, testStore(t))
	started := make(chan struct{})
	release := make(chan struct{})
	cmd := d.index[CommandPing]
	cmd.Handler = func(ctx context.Context, ev Event, arg string) error {
		close(started)
		<-release
		return d.reply(ctx, ev.ChatID, "pong")
	}
	d.index[CommandPing] = cmd
	tg, err := d.NewTelegram(Options{ServerURL: server.URL, SkipGetMe: true, Workers: 1, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewTelegram: %v", err)
	}
	returned := make(chan struct{})
	go func() {
		tg.ProcessUpdate(context.Background(), &models.Update{ID: 1, Message: &models.Message{
			ID: 1, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}, From: &models.User{ID: 11}, Text: "/ping",
		}})
		close(returned)
	}()
	awaitAdmissionSignal(t, started)
	select {
	case <-returned:
		t.Fatal("ProcessUpdate returned while its handler was blocked; per-update async handling is still enabled")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("ProcessUpdate did not return after its handler completed")
	}
	if sends.Load() != 1 {
		t.Fatalf("fake Telegram sends = %d, want 1", sends.Load())
	}
}

func TestUnauthorizedCallbackCannotMutatePersona(t *testing.T) {
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	db := testStore(t)
	d := newTestDispatcher(t, cfg, nil, &fakeReplier{}, db)
	d.handleCallback(context.Background(), nil, &models.Update{ID: 1, CallbackQuery: &models.CallbackQuery{
		ID: "denied", Data: "persona:coder", From: models.User{ID: 99},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: 1, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}}},
	}})
	if _, found, err := store.ChatPersona(context.Background(), db, 100); err != nil || found {
		t.Fatalf("unauthorized callback persona found=%v err=%v", found, err)
	}
}
