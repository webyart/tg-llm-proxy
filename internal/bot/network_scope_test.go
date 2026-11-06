package bot

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bot/internal/model"
	"bot/internal/store"
)

func TestTaskScopedWebSearchPersistsAcrossStepsWithoutBecomingGlobal(t *testing.T) {
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer provider.Close()
	m := &taskScriptModel{results: []taskModelResult{
		{response: model.Response{Content: validTaskPlanTwo}},
		{response: model.Response{ToolCalls: []model.ToolCall{{ID: "first", Name: "web_search", Arguments: `{"query":"first query"}`}}}},
		{response: model.Response{Content: "first scoped result"}},
		{response: model.Response{ToolCalls: []model.ToolCall{{ID: "second", Name: "web_search", Arguments: `{"query":"different second query"}`}}}},
		{response: model.Response{Content: "second scoped result"}},
		{response: model.Response{Content: "scoped final answer"}},
	}}
	r := &networkMenuReplier{}
	db := testStore(t)
	cfg := testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11,12", "WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": provider.URL})
	d := newTestDispatcher(t, cfg, m, r, db)
	if err := dispatch(t, d, testEvent(100, 11, 1100, "/task use scoped search")); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 1)
	markup := r.menus[len(r.menus)-1]
	scope := findMenuCallback(markup, "menu:network:scope:")
	if scope == "" || findMenuCallback(markup, "menu:network:confirm:") == "" || findMenuCallback(markup, "menu:network:cancel:") == "" {
		t.Fatalf("approval buttons=%+v", markup)
	}
	if err := dispatchMenuData(t, d, 100, 12, 700, scope); err != nil {
		t.Fatal(err)
	}
	if err := dispatchMenuData(t, d, 101, 11, 700, scope); err != nil {
		t.Fatal(err)
	}
	if providerCalls.Load() != 0 {
		t.Fatal("wrong user or chat consumed scoped approval")
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, scope); err != nil {
		t.Fatal(err)
	}
	processTasks(t, d, 2)
	if providerCalls.Load() != 2 || m.callCount() != 6 || len(r.menus) != 1 || !strings.Contains(r.joined(), "scoped final answer") {
		t.Fatalf("scoped resume provider=%d calls=%d menus=%d replies=%q", providerCalls.Load(), m.callCount(), len(r.menus), r.joined())
	}
	if err := dispatchMenuData(t, d, 100, 11, 700, scope); err != nil {
		t.Fatal(err)
	}
	if providerCalls.Load() != 2 {
		t.Fatal("scoped approval token was replayable")
	}
}

func TestNetworkScopeCanonicalOriginBindingsAndRestartExpiry(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	r := &networkMenuReplier{}
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "URL_READER_ENABLED": "true"}), nil, r, db)
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created, err := store.CreateTask(ctx, db, store.Task{ChatID: 100, UserID: 11, Goal: "bounded URL work", Model: d.deps.Cfg.StrataModel,
		PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	state := &toolLoopContinuation{userID: 11, metadata: toolLoopMetadata{event: Event{ChatID: 100, UserID: 11}, taskID: created.ID}}
	first := model.ToolCall{ID: "first", Name: "read_url", Arguments: `{"url":"https://Example.com:443/first"}`}
	sameOrigin := model.ToolCall{ID: "second", Name: "read_url", Arguments: `{"url":"https://example.com/other/path?q=1"}`}
	newOrigin := model.ToolCall{ID: "third", Name: "read_url", Arguments: `{"url":"https://other.example/private"}`}
	search := model.ToolCall{ID: "search", Name: "web_search", Arguments: `{"query":"other tool"}`}
	if !d.grantNetworkScope(state, first) || !d.networkScopeAllows(ctx, state, sameOrigin) {
		t.Fatal("canonical origin scope did not authorize the same origin")
	}
	if d.networkScopeAllows(ctx, state, newOrigin) || d.networkScopeAllows(ctx, state, search) {
		t.Fatal("read_url scope authorized a new origin or another tool")
	}
	for _, mutate := range []func(*toolLoopContinuation){
		func(other *toolLoopContinuation) { other.metadata.event.UserID = 12; other.userID = 12 },
		func(other *toolLoopContinuation) { other.metadata.event.ChatID = 101 },
		func(other *toolLoopContinuation) { other.metadata.taskID++ },
	} {
		other := *state
		mutate(&other)
		if d.networkScopeAllows(ctx, &other, sameOrigin) {
			t.Fatal("scope survived a wrong user, chat, or task ID")
		}
	}
	restarted := newTestDispatcher(t, d.deps.Cfg, nil, r, db)
	if restarted.networkScopeAllows(ctx, state, sameOrigin) {
		t.Fatal("scoped approval survived a dispatcher restart")
	}

	claimed, found, err := store.ClaimNextTask(ctx, db, now, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	state.request = model.Request{Model: claimed.Model}
	state.response = model.Response{ToolCalls: []model.ToolCall{newOrigin}}
	state.allowedTools = map[string]bool{"read_url": true}
	state.approvedCall = -1
	state.approvedCalls = map[int]bool{}
	if _, err := d.runToolLoop(ctx, state); !errors.Is(err, errNetworkApprovalPending) {
		t.Fatalf("new origin did not require fresh exact approval: %v", err)
	}
	if len(r.menus) != 1 {
		t.Fatal("new origin did not show an approval menu")
	}
	if _, ok, err := store.CancelTask(ctx, db, 100, now); err != nil || !ok {
		t.Fatalf("cancel ok=%v err=%v", ok, err)
	}
	d.clearTaskNetworkScopes(100, created.ID)
	if d.networkScopeAllows(ctx, state, sameOrigin) {
		t.Fatal("cancelled task retained scoped authorization")
	}
}

func TestNonTaskNetworkScopeLivesOnlyOnOneContinuation(t *testing.T) {
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "WEB_SEARCH_ENABLED": "true", "WEB_SEARCH_BASE_URL": "https://search.example"}), nil, &fakeReplier{}, testStore(t))
	state := &toolLoopContinuation{userID: 11, metadata: toolLoopMetadata{event: Event{ChatID: 100, UserID: 11}}}
	first := model.ToolCall{Name: "web_search", Arguments: `{"query":"first"}`}
	otherQuery := model.ToolCall{Name: "web_search", Arguments: `{"query":"different"}`}
	readURL := model.ToolCall{Name: "read_url", Arguments: `{"url":"https://example.com"}`}
	if !d.grantNetworkScope(state, first) || !d.networkScopeAllows(context.Background(), state, otherQuery) {
		t.Fatal("web_search scope did not cover that tool in the continuation")
	}
	if d.networkScopeAllows(context.Background(), state, readURL) {
		t.Fatal("web_search scope authorized read_url")
	}
	clone := *state
	clone.approvalScope = nil
	if d.networkScopeAllows(context.Background(), &clone, otherQuery) {
		t.Fatal("non-task scope escaped its continuation")
	}
	wrongUser := *state
	wrongUser.userID = 12
	wrongUser.metadata.event.UserID = 12
	if d.networkScopeAllows(context.Background(), &wrongUser, otherQuery) {
		t.Fatal("non-task scope survived a user change")
	}
}

func TestOneTimeApprovedURLReaderRejectsCrossOriginRedirect(t *testing.T) {
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "URL_READER_ENABLED": "true"}), nil, &networkMenuReplier{}, testStore(t))
	state := &toolLoopContinuation{
		userID: 11, metadata: toolLoopMetadata{event: Event{ChatID: 100, UserID: 11}},
		approvedCalls: map[int]bool{0: true},
	}
	assertApprovedURLReaderRejectsCrossOriginRedirect(t, d, state)
}

func TestTaskScopedURLReaderRejectsCrossOriginRedirect(t *testing.T) {
	ctx := context.Background()
	db := testStore(t)
	d := newTestDispatcher(t, testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "URL_READER_ENABLED": "true"}), nil, &networkMenuReplier{}, db)
	if err := store.EnsureChat(ctx, db, 100, 11, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	created, err := store.CreateTask(ctx, db, store.Task{ChatID: 100, UserID: 11, Goal: "bounded URL work", Model: d.deps.Cfg.StrataModel,
		PlanJSON: validTaskPlanOne, DueAt: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	state := &toolLoopContinuation{userID: 11, metadata: toolLoopMetadata{event: Event{ChatID: 100, UserID: 11}, taskID: created.ID}, approvedCalls: map[int]bool{}}
	assertApprovedURLReaderRejectsCrossOriginRedirect(t, d, state)
}

func assertApprovedURLReaderRejectsCrossOriginRedirect(t *testing.T, d *Dispatcher, state *toolLoopContinuation) {
	t.Helper()
	var initialRequests atomic.Int64
	fixture := newURLReaderTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		initialRequests.Add(1)
		w.Header().Set("Location", "https://other.example/private")
		w.WriteHeader(http.StatusFound)
	}), []net.IP{net.ParseIP("93.184.216.34")})
	call := model.ToolCall{ID: "approved-redirect", Name: "read_url", Arguments: `{"url":"` + fixture.url + `/start"}`}
	if state.metadata.taskID > 0 && !d.grantNetworkScope(state, call) {
		t.Fatal("failed to grant task scope")
	}
	ctx := d.networkToolContext(context.Background(), state, 0, call)
	payload, err := executeTestURLReader(ctx, fixture, fixture.url+"/start")
	if err != nil {
		t.Fatal(err)
	}
	if payload["error"] != errURLReaderCrossOriginRedirect.Error() || payload["result"] != "" {
		t.Fatalf("cross-origin redirect payload=%v", payload)
	}
	if initialRequests.Load() != 1 || fixture.resolver.calls != 1 || len(fixture.dialAddresses) != 1 {
		t.Fatalf("redirect origin was fetched: requests=%d DNS=%d dials=%v", initialRequests.Load(), fixture.resolver.calls, fixture.dialAddresses)
	}
}
