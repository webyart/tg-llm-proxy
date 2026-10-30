// strata.go — the fake STRATA of §11 («Фейковый STRATA обязателен: прототип можно тестировать,
// когда сервер недоступен») and of the §10 День 3 DoD («интеграционный тест против httptest-фейка:
// успех, timeout, 500, пустой ответ, 500→успех после ретрая»).
//
// The fake serves the paths the REAL endpoint serves (§16 change 26: /v1/chat/completions and
// /v1/models, 404 on anything else), so a client that passes here passes on the real server too —
// a fake rooted at "/chat/completions" would hide exactly the URL bug this project already hit.
package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// StrataReply — one scripted answer. Replies are consumed in order; once the script is exhausted the
// LAST reply repeats, which is how "the server stays broken" is expressed (§10 День 3 case "500",
// §10 День 6 «остановка STRATA»).
type StrataReply struct {
	Status       int           // 0 means 200
	Content      string        // choices[0].message.content of the OpenAI shape
	NoContent    bool          // answer with a null content, which real providers do send
	EmptyChoices bool          // answer 200 with choices: [] (§10 День 3 «пустой ответ»)
	TokensIn     int64         // usage.prompt_tokens
	TokensOut    int64         // usage.completion_tokens
	NoUsage      bool          // omit the usage object (§5 keeps tokens_in/tokens_out NULL)
	Model        string        // the model name to echo (§6 /model)
	Delay        time.Duration // hold the response open — the "timeout" case of §10 День 3
	Models       []string      // the list for GET /v1/models (§6 /model check)
}

// Strata — the running fake. The counters live under a mutex because §10 День 6 runs parallel
// messages of one chat against it.
type Strata struct {
	Server    *httptest.Server
	wantKey   string
	replies   []StrataReply
	mu        sync.Mutex
	chatCalls int
	getCalls  int
	otherCall int // requests that hit neither /v1/chat/completions nor /v1/models (§16 change 26 404)
	lastBody  string
	lastAuth  string
}

// NewStrata — start the fake. An empty wantKey means the endpoint needs no Authorization (the §7 row
// STRATA_API_KEY may be empty); a non-empty key makes the fake answer 401 without the header, which
// is what the real endpoint did when this project probed it.
func NewStrata(t *testing.T, wantKey string, replies ...StrataReply) *Strata {
	t.Helper()
	if len(replies) == 0 {
		replies = []StrataReply{{Content: "ответ фейка", Model: "fake-model", TokensIn: 10, TokensOut: 20}}
	}
	var s = &Strata{wantKey: wantKey, replies: replies}
	var mux = http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/models", s.handleModels)
	// Everything else answers 404 — the measured behaviour of the real endpoint without the /v1
	// prefix (§16 change 26), so a client that joins the wrong URL fails here as well.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.otherCall++
		s.mu.Unlock()
		http.Error(w, "the fake serves only /v1/chat/completions and /v1/models (§16 change 26)", http.StatusNotFound)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

// BaseURL — what belongs in STRATA_BASE_URL of §7. The client normalizes it to exactly one /v1
// (§16 change 26), so the bare server URL is the honest value for a test configuration.
func (s *Strata) BaseURL() string { return s.Server.URL }

// ChatURL / ModelsURL — the two joins of §7 over that base.
func (s *Strata) ChatURL() string   { return s.Server.URL + "/v1/chat/completions" }
func (s *Strata) ModelsURL() string { return s.Server.URL + "/v1/models" }

// ChatCalls — how many chat requests reached the fake (§10 День 3: the retry case expects exactly 2,
// the empty-answer case exactly 1).
func (s *Strata) ChatCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chatCalls
}

// ModelsCalls — how many GET /models requests arrived (§6 /model check counts one).
func (s *Strata) ModelsCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls
}

// OtherCalls — requests that reached neither endpoint: the 404 of §16 change 26, which is how a wrong
// STRATA_BASE_URL of §7 shows itself. A client that retries such a request is caught by the test of
// §10 День 3 («4xx не ретраится») through this counter.
func (s *Strata) OtherCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.otherCall
}

// LastRequestBody — the JSON the client sent last: §11 asserts the model name and the glued context
// through it. LastAuthorization is exposed for the assertion "the header is sent" and must never be
// printed (§12 security).
func (s *Strata) LastRequestBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastBody
}

func (s *Strata) LastAuthorization() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAuth
}

func (s *Strata) reply(i int) StrataReply {
	if i >= len(s.replies) {
		return s.replies[len(s.replies)-1]
	}
	return s.replies[i]
}

func (s *Strata) authorized(r *http.Request) bool {
	if s.wantKey == "" {
		return true
	}
	return r.Header.Get("Authorization") == "Bearer "+s.wantKey
}

// handleChat — the scripted answer. The Delay waits on the REQUEST context, so a client whose budget
// of §10 День 3 ran out does not leave the fake serving a response nobody reads: the timeout case
// stays fast instead of a sleeping test.
func (s *Strata) handleChat(w http.ResponseWriter, r *http.Request) {
	var raw, readErr = io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if readErr != nil {
		http.Error(w, "the request body did not arrive", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	var index = s.chatCalls
	s.chatCalls++
	s.lastBody = string(raw)
	s.lastAuth = r.Header.Get("Authorization")
	s.mu.Unlock()

	if !s.authorized(r) {
		http.Error(w, "the fake expects the key of §7 (STRATA_API_KEY)", http.StatusUnauthorized)
		return
	}

	var answer = s.reply(index)
	if answer.Delay > 0 {
		select {
		case <-r.Context().Done():
			return // the budget is spent — there is nothing to answer
		case <-time.After(answer.Delay):
		}
	}
	if answer.Status >= 400 {
		http.Error(w, "the fake is broken (§10 День 3: 500, retryable)", answer.Status)
		return
	}

	var model = answer.Model
	if model == "" {
		model = requestModel(string(raw)) // echo what the client asked for, as a real server does
	}
	var message = map[string]any{"role": "assistant"}
	if answer.NoContent {
		message["content"] = nil // some providers answer null here
	} else {
		message["content"] = answer.Content
	}
	var payload = map[string]any{
		"id":      "fake-chat-1",
		"object":  "chat.completion",
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop"}},
	}
	if answer.EmptyChoices {
		payload["choices"] = []any{} // §10 День 3 «пустой ответ»
	}
	if !answer.NoUsage {
		payload["usage"] = map[string]any{
			"prompt_tokens":     answer.TokensIn,
			"completion_tokens": answer.TokensOut,
			"total_tokens":      answer.TokensIn + answer.TokensOut,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload) // an already-gone client is not a failure of the fake
}

// handleModels — GET /v1/models in the OpenAI envelope of §6 (`/model check`).
func (s *Strata) handleModels(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var index = s.getCalls
	s.getCalls++
	s.lastAuth = r.Header.Get("Authorization") // §7 sends the key on both endpoints (§6 /model check)
	s.mu.Unlock()

	if !s.authorized(r) {
		http.Error(w, "the fake expects the key of §7 (STRATA_API_KEY)", http.StatusUnauthorized)
		return
	}
	var ids = s.reply(index).Models
	if len(ids) == 0 {
		ids = []string{"fake-model"}
	}
	var data = make([]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]any{"id": id, "object": "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// requestModel — the model name the client sent, read out of the request body. A malformed body gives
// an empty name, so the assertion of the test says what is wrong (§12 UX).
func requestModel(raw string) string {
	var decoded struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return ""
	}
	return decoded.Model
}
