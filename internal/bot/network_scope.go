package bot

import (
	"context"
	"encoding/json"
	"net/url"
	"sync"

	"bot/internal/model"
	"bot/internal/store"
)

type networkApprovalScope struct {
	ChatID int64
	UserID int64
	TaskID int64
	Tool   string
	Origin string
}

type networkApprovalScopeSet struct {
	mu     sync.Mutex
	scopes map[networkApprovalScope]bool
}

var dispatcherNetworkApprovalScopes sync.Map

func networkApprovalScopesFor(d *Dispatcher) *networkApprovalScopeSet {
	set, _ := dispatcherNetworkApprovalScopes.LoadOrStore(d, &networkApprovalScopeSet{scopes: map[networkApprovalScope]bool{}})
	return set.(*networkApprovalScopeSet)
}

func networkScopeExplanation(call model.ToolCall) string {
	if call.Name == "read_url" {
		return " и этот canonical origin"
	}
	if call.Name == "web_search" {
		return " с любым текстом поиска"
	}
	return ""
}

func networkCallOrigin(call model.ToolCall) string {
	if call.Name != "read_url" {
		return ""
	}
	var input struct {
		URL string `json:"url"`
	}
	if json.Unmarshal([]byte(call.Arguments), &input) != nil {
		return ""
	}
	canonical, err := canonicalHTTPSURL(input.URL)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(canonical)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func scopeForNetworkCall(state *toolLoopContinuation, call model.ToolCall) (networkApprovalScope, bool) {
	if state == nil || call.Name != "read_url" && call.Name != "web_search" {
		return networkApprovalScope{}, false
	}
	scope := networkApprovalScope{
		ChatID: state.metadata.event.ChatID,
		UserID: state.metadata.event.UserID,
		TaskID: state.metadata.taskID,
		Tool:   call.Name,
		Origin: networkCallOrigin(call),
	}
	if scope.ChatID == 0 || scope.UserID <= 0 || scope.TaskID < 0 || call.Name == "read_url" && scope.Origin == "" {
		return networkApprovalScope{}, false
	}
	return scope, true
}

func (d *Dispatcher) networkScopeAllows(ctx context.Context, state *toolLoopContinuation, call model.ToolCall) bool {
	if state == nil || state.suppressApprovalScope {
		return false
	}
	scope, ok := scopeForNetworkCall(state, call)
	if !ok {
		return false
	}
	if scope.TaskID == 0 {
		attached := state.approvalScope
		return attached != nil && *attached == scope
	}
	current, found, err := store.CurrentTask(ctx, d.deps.DB, scope.ChatID)
	if err != nil || !found || current.ID != scope.TaskID || current.UserID != scope.UserID || !store.ActiveTaskStatus(current.Status) {
		return false
	}
	set := networkApprovalScopesFor(d)
	set.mu.Lock()
	allowed := set.scopes[scope]
	set.mu.Unlock()
	return allowed
}

func (d *Dispatcher) grantNetworkScope(state *toolLoopContinuation, call model.ToolCall) bool {
	scope, ok := scopeForNetworkCall(state, call)
	if !ok {
		return false
	}
	state.suppressApprovalScope = false
	if scope.TaskID == 0 {
		state.approvalScope = &scope
		return true
	}
	set := networkApprovalScopesFor(d)
	set.mu.Lock()
	set.scopes[scope] = true
	set.mu.Unlock()
	return true
}

func (d *Dispatcher) clearTaskNetworkScopes(chatID int64, taskID int64) {
	set := networkApprovalScopesFor(d)
	set.mu.Lock()
	for scope := range set.scopes {
		if scope.ChatID == chatID && scope.TaskID == taskID {
			delete(set.scopes, scope)
		}
	}
	set.mu.Unlock()
}
