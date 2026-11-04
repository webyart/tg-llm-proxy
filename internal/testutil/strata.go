// успех, timeout, 500, пустой ответ, 500→успех после ретрая»).
//
// /v1/models, 404 on anything else), so a client that passes here passes on the real server too —
// a fake rooted at "/chat/completions" would hide exactly the URL bug this project already hit.
package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// StrataReply — one scripted answer. Replies are consumed in order; once the script is exhausted the
type StrataReply struct {
	Status        int           // 0 means 200
	Content       string        // choices[0].message.content of the OpenAI shape
	NoContent     bool          // answer with a null content, which real providers do send
	EmptyChoices  bool          // answer 200 with choices: []
	TokensIn      int64         // usage.prompt_tokens
	TokensOut     int64         // usage.completion_tokens
	NoUsage       bool          // omit the usage object
	Model         string        // the model name to echo
	Delay         time.Duration // hold the response open — the "timeout" case of
	Models        []string      // the list for GET /v1/models
	ToolCallID    string
	ToolName      string
	ToolArguments string

	// (`"stream": true` in the body), which is how one fake serves both paths of the client. The shapes below
	// `data: [DONE]` at the end, and `usage` inside the stream without `stream_options`.
	StreamContent     []string      // one element per `delta.content` event — the answer arriving by pieces
	StreamReasoning   []string      // `delta.reasoning_content` events: the thinking channel of
	StreamGap         time.Duration // pause BETWEEN the events (a healthy stream measures ~20 ms apart)
	StreamStall       time.Duration // pause before the LAST event — the stall of STREAM_IDLE_TIMEOUT_SECONDS
	StreamGarbage     int           // inject this many `data:` lines that are not JSON (a broken provider)
	StreamKeepalive   bool          // insert a `:` comment line — measured 2–3 per real answer
	StreamNoDone      bool          // do not send `[DONE]`; end by closing the connection instead
	StreamFinish      string        // finish_reason of the last event, "stop" unless set ("length" = cut off)
	StreamUsage       bool          // carry the usage object in the final event of the stream
	StreamContentType string        // content-type to answer — a proxy that ignored the stream answers JSON
}

// messages of one chat against it.
type Strata struct {
	Server                   *httptest.Server
	wantKey                  string
	replies                  []StrataReply
	mu                       sync.Mutex
	chatCalls                int
	getCalls                 int
	otherCall                int // requests that hit neither /v1/chat/completions nor /v1/models
	lastBody                 string
	lastAuth                 string
	transcriptionText        string
	transcriptionModel       string
	transcriptionFilename    string
	transcriptionContentType string
	transcriptionBytes       int
	transcriptionCalls       int
}

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
	mux.HandleFunc("/v1/audio/transcriptions", s.handleTranscription)
	mux.HandleFunc("/v1/models", s.handleModels)
	// Everything else answers 404 — the measured behaviour of the real endpoint without the /v1
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.otherCall++
		s.mu.Unlock()
		http.Error(w, "the fake serves only /v1/chat/completions and /v1/models ", http.StatusNotFound)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

func (s *Strata) BaseURL() string { return s.Server.URL }

func (s *Strata) ChatURL() string   { return s.Server.URL + "/v1/chat/completions" }
func (s *Strata) ModelsURL() string { return s.Server.URL + "/v1/models" }

// the empty-answer case exactly 1).
func (s *Strata) ChatCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chatCalls
}

func (s *Strata) ModelsCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCalls
}

func (s *Strata) OtherCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.otherCall
}

// through it. LastAuthorization is exposed for the assertion "the header is sent" and must never be
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
		http.Error(w, "the fake expects the key of (STRATA_API_KEY)", http.StatusUnauthorized)
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
		http.Error(w, "the fake is broken ", answer.Status)
		return
	}

	var model = answer.Model
	if model == "" {
		model = requestModel(string(raw)) // echo what the client asked for, as a real server does
	}

	// stream shape. A script WITHOUT it answers plain JSON to the same request — that is the proxy bug the
	// client of stream.go refuses to read as an empty model answer.
	if answer.hasStream() && requestWantsStream(string(raw)) {
		s.writeStream(r, w, answer, model)
		return
	}
	var message = map[string]any{"role": "assistant"}
	if answer.NoContent {
		message["content"] = nil // some providers answer null here
	} else {
		message["content"] = answer.Content
	}
	finish := "stop"
	if answer.ToolName != "" {
		message["content"] = nil
		message["tool_calls"] = []any{map[string]any{"id": answer.ToolCallID, "type": "function", "function": map[string]any{"name": answer.ToolName, "arguments": answer.ToolArguments}}}
		finish = "tool_calls"
	}
	var payload = map[string]any{
		"id":      "fake-chat-1",
		"object":  "chat.completion",
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
	}
	if answer.EmptyChoices {
		payload["choices"] = []any{} // «пустой ответ»
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

func (s *Strata) handleModels(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var index = s.getCalls
	s.getCalls++
	s.lastAuth = r.Header.Get("Authorization") // sends the key on both endpoints
	s.mu.Unlock()

	if !s.authorized(r) {
		http.Error(w, "the fake expects the key of (STRATA_API_KEY)", http.StatusUnauthorized)
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
func requestModel(raw string) string {
	var decoded struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return ""
	}
	return decoded.Model
}

// flag the same way the client sets it.
func requestWantsStream(raw string) bool {
	var decoded struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return false
	}
	return decoded.Stream
}

// hasStream — does this reply carry the stream shape at all? A reply that only scripts the plain path stays
func (r StrataReply) hasStream() bool {
	return len(r.StreamContent) > 0 || len(r.StreamReasoning) > 0 || r.StreamUsage || r.StreamNoDone ||
		r.StreamStall > 0 || r.StreamGap > 0 || r.StreamGarbage > 0 || r.StreamKeepalive
}

// data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}
// : keepalive comment                                                    (2–3 per answer)
// data: …{"delta":{"reasoning_content":"Thinking"}}                      (the thinking of this model)
// data: …{"delta":{"content":"Привет"}}                                  (the answer, one event per token)
// data: …{"choices":[],"usage":{"prompt_tokens":79,"completion_tokens":167}}
// data: [DONE]
func (s *Strata) writeStream(r *http.Request, w http.ResponseWriter, answer StrataReply, model string) {
	var contentType = strings.TrimSpace(answer.StreamContentType)
	if contentType == "" {
		contentType = "text/event-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	var flusher, canFlush = w.(http.Flusher)
	var event = func(payload map[string]any) bool {
		var line, err = json.Marshal(payload)
		if err != nil {
			return true // a scripted event that cannot be built is not worth an aborted test
		}
		if _, err = fmt.Fprintf(w, "data: %s\n\n", line); err != nil {
			return false // the client is gone
		}
		if canFlush {
			flusher.Flush() // without a flush the client sees one wall of text at the end, not a stream
		}
		return true
	}
	var comment = func() {
		if _, err := fmt.Fprint(w, ": keepalive of the fake \n\n"); err != nil {
			return
		}
		if canFlush {
			flusher.Flush()
		}
	}
	var wait = func(gap time.Duration) bool {
		if gap <= 0 {
			return true
		}
		select {
		case <-r.Context().Done():
			return false
		case <-time.After(gap):
			return true
		}
	}

	var envelope = func(delta map[string]any, finish any, usage map[string]any, choices []any) map[string]any {
		var payload = map[string]any{
			"id":      "fake-chat-stream-1",
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": choices,
		}
		if delta != nil {
			payload["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}
		}
		if usage != nil {
			payload["usage"] = usage
		}
		return payload
	}

	if !event(envelope(map[string]any{"role": "assistant", "content": ""}, nil, nil, nil)) {
		return
	}
	if answer.StreamKeepalive {
		comment()
	}
	for _, piece := range answer.StreamReasoning {
		if !wait(answer.StreamGap) {
			return
		}
		if !event(envelope(map[string]any{"reasoning_content": piece}, nil, nil, nil)) {
			return
		}
	}
	for i := 0; i < answer.StreamGarbage; i++ {
		if _, err := fmt.Fprint(w, "data: {this is not json }\n\n"); err != nil {
			return
		}
		if canFlush {
			flusher.Flush()
		}
	}
	for _, piece := range answer.StreamContent {
		if !wait(answer.StreamGap) {
			return
		}
		if !event(envelope(map[string]any{"content": piece}, nil, nil, nil)) {
			return
		}
	}
	var finish = answer.StreamFinish
	if finish == "" {
		finish = "stop"
	}
	if answer.StreamStall > 0 {
		if !wait(answer.StreamStall) {
			return // the stall of STREAM_IDLE_TIMEOUT_SECONDS: the client ends the call, nothing to write
		}
	}
	if !event(envelope(map[string]any{}, finish, nil, nil)) {
		return
	}
	if answer.StreamUsage {
		var usage = map[string]any{
			"prompt_tokens":     answer.TokensIn,
			"completion_tokens": answer.TokensOut,
			"total_tokens":      answer.TokensIn + answer.TokensOut,
		}
		if !event(envelope(nil, nil, usage, []any{})) {
			return
		}
	}
	if !answer.StreamNoDone {
		if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
			return
		}
		if canFlush {
			flusher.Flush()
		}
	}
}
