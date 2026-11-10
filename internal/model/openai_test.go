// 500, пустой ответ, 500→успех после ретрая — ровно одна строка assistant в БД)».
//
// the handler, and a client aimed at a wrong path gets the 404 the real server gives.
package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bot/internal/store"
	"bot/internal/testutil"
)

func clientFor(t *testing.T, fake *testutil.Strata, opts Options) *OpenAI {
	t.Helper()
	if opts.ChatURL == "" {
		opts.ChatURL = fake.ChatURL()
	}
	if opts.ModelsURL == "" {
		opts.ModelsURL = fake.ModelsURL()
	}
	if opts.Model == "" {
		opts.Model = "qwen-test"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Second
	}
	if opts.Attempts <= 0 {
		opts.Attempts = DefaultAttempts
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 10 * time.Millisecond
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 2
	}
	var c, err = New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func oneRequest(text string) Request {
	return Request{Messages: []Message{
		{Role: RoleSystem, Content: "system prompt of"},
		{Role: RoleUser, Content: text},
	}}
}

// TestCompleteSuccess — the "успех" case: the answer, the model name and the usage land in the
func TestCompleteSuccess(t *testing.T) {
	var fake = testutil.NewStrata(t, "secret-key", testutil.StrataReply{
		Content: "ответ модели", Model: "qwen-echo", TokensIn: 11, TokensOut: 22,
	})
	var c = clientFor(t, fake, Options{APIKey: "secret-key"})

	var res, err = c.Complete(context.Background(), oneRequest("вопрос"))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.Content != "ответ модели" {
		t.Errorf("Content = %q, want the answer of the fake", res.Content)
	}
	if res.Model != "qwen-echo" {
		t.Errorf("Model = %q, want the model the endpoint echoed ", res.Model)
	}
	if res.TokensIn == nil || *res.TokensIn != 11 || res.TokensOut == nil || *res.TokensOut != 22 {
		t.Errorf("tokens = %v/%v, want 11/22 from usage ", res.TokensIn, res.TokensOut)
	}
	if res.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 ", res.Attempts)
	}
	if res.Latency <= 0 {
		t.Error("Latency must cover the whole call ")
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d chat requests, want 1", fake.ChatCalls())
	}
	if fake.LastAuthorization() != "Bearer secret-key" {
		t.Error("Authorization was not sent as Bearer ") // the value is not printed
	}

	var sent = decodeBody(t, fake.LastRequestBody())
	if sent.Model != "qwen-echo" && sent.Model != "qwen-test" {
		t.Errorf("the request carried model %q", sent.Model)
	}
	if len(sent.Messages) != 2 || sent.Messages[0].Role != RoleSystem || sent.Messages[1].Content != "вопрос" {
		t.Errorf("the request carried %v, want the system prompt of then the user text ", sent.Messages)
	}
	if sent.Stream {
		t.Error("stream must be false in v1 ")
	}
}

type wireBody struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func decodeBody(t *testing.T, raw string) wireBody {
	t.Helper()
	var body wireBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("the client sent something that is not JSON: %v", err)
	}
	return body
}

func TestPhotoUsesMultimodalContentAndTextStaysString(t *testing.T) {
	fake := testutil.NewStrata(t, "", testutil.StrataReply{Content: "ok"})
	c := clientFor(t, fake, Options{})
	req := oneRequest("обычный вопрос")
	req.Messages = append(req.Messages, Message{Role: RoleUser, Content: "что на фото?", ImageURL: "data:image/jpeg;base64,amZw"})
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(fake.LastRequestBody()), &body); err != nil {
		t.Fatalf("request JSON: %v", err)
	}
	var ordinary string
	if err := json.Unmarshal(body.Messages[1].Content, &ordinary); err != nil || ordinary != "обычный вопрос" {
		t.Fatalf("ordinary content = %s, err=%v", body.Messages[1].Content, err)
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(body.Messages[2].Content, &parts); err != nil {
		t.Fatalf("multimodal content: %v", err)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[0].Text != "что на фото?" || parts[1].Type != "image_url" || parts[1].ImageURL.URL != "data:image/jpeg;base64,amZw" {
		t.Fatalf("multimodal parts = %+v", parts)
	}
}

func TestToWireSendsAllImages(t *testing.T) {
	wire := toWire([]Message{
		{Role: RoleUser, Content: "обычный текст"},
		{Role: RoleUser, Content: "альбом", ImageURL: "legacy", ImageURLs: []string{"first", "", "second"}},
	})
	ordinary, ok := wire[0].Content.(string)
	if !ok || ordinary != "обычный текст" {
		t.Fatalf("ordinary content = %#v, want a string", wire[0].Content)
	}
	parts, ok := wire[1].Content.([]chatContentPart)
	if !ok {
		t.Fatalf("album content has type %T, want content parts", wire[1].Content)
	}
	if len(parts) != 4 || parts[0].Type != "text" || parts[0].Text != "альбом" {
		t.Fatalf("album parts = %+v", parts)
	}
	wantURLs := []string{"legacy", "first", "second"}
	for i, want := range wantURLs {
		part := parts[i+1]
		if part.Type != "image_url" || part.ImageURL == nil || part.ImageURL.URL != want {
			t.Errorf("image part %d = %+v, want URL %q", i, part, want)
		}
	}
}

func TestEmptyAnswerIsAnError(t *testing.T) {
	for name, reply := range map[string]testutil.StrataReply{
		"no choices":    {EmptyChoices: true},
		"null content":  {NoContent: true},
		"blank content": {Content: "   "},
	} {
		var fake = testutil.NewStrata(t, "", reply)
		var c = clientFor(t, fake, Options{})

		var _, err = c.Complete(context.Background(), oneRequest("вопрос"))
		if !errors.Is(err, ErrEmptyAnswer) {
			t.Errorf("%s: err = %v, want ErrEmptyAnswer ", name, err)
		}
		if fake.ChatCalls() != 1 {
			t.Errorf("%s: %d requests, want 1 — an empty answer is not retried ",
				name, fake.ChatCalls())
		}
	}
}

// what this pins down: the retry happens INSIDE Complete, so the caller stores ONE assistant row and
// дубли в истории»).
func TestRetryAfterServerError(t *testing.T) {
	var fake = testutil.NewStrata(t, "",
		testutil.StrataReply{Status: 500},
		testutil.StrataReply{Content: "успех после ретрая", Model: "qwen-echo", TokensIn: 5, TokensOut: 7},
	)
	var c = clientFor(t, fake, Options{Attempts: DefaultAttempts, Backoff: 10 * time.Millisecond})

	var res, err = c.Complete(context.Background(), oneRequest("вопрос"))
	if err != nil {
		t.Fatalf("Complete after a 500: %v ", err)
	}
	if res.Content != "успех после ретрая" {
		t.Errorf("Content = %q, want the answer of the second attempt", res.Content)
	}
	if res.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2 ", res.Attempts)
	}
	if fake.ChatCalls() != 2 {
		t.Errorf("the fake got %d requests, want 2 (one retry, not more)", fake.ChatCalls())
	}

	// День 4 calls Append once with the Response of Complete, so a successful call — retried or not —
	// produces exactly one assistant row.
	var db, openErr = store.OpenMemory(context.Background(), "model_retry_"+t.Name())
	if openErr != nil {
		t.Fatalf("OpenMemory: %v", openErr)
	}
	defer db.Close()

	var in = *res.TokensIn
	var out = *res.TokensOut
	if _, appendErr := store.Append(context.Background(), db, store.Message{
		ChatID: 1, UserID: 2, Role: store.RoleAssistant, Content: res.Content,
		TokensIn: &in, TokensOut: &out, Model: res.Model, LatencyMs: res.Latency.Milliseconds(),
	}); appendErr != nil {
		t.Fatalf("Append of the retried answer: %v", appendErr)
	}
	var rows, queryErr = store.HistoryOfChat(context.Background(), db, 1, 10)
	if queryErr != nil {
		t.Fatalf("HistoryOfChat: %v", queryErr)
	}
	if len(rows) != 1 || rows[0].Role != store.RoleAssistant || rows[0].Content != "успех после ретрая" {
		t.Errorf("the retried call produced %d rows: %+v ", len(rows), rows)
	}
	if rows[0].Model != "qwen-echo" || rows[0].TokensOut == nil || *rows[0].TokensOut != 7 {
		t.Errorf("the stored row lost its metadata: %+v ", rows[0])
	}
}

// events(kind='model_error')». The fake stays broken (its last reply repeats), so Complete must fail
// after exactly STRATA_MAX_ATTEMPTS attempts, and the caller writes no message row at all.
func TestEveryAttemptFailsWritesOnlyEvents(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{Status: 500})
	var c = clientFor(t, fake, Options{Attempts: 2, Backoff: 5 * time.Millisecond})

	var _, err = c.Complete(context.Background(), oneRequest("вопрос"))
	if err == nil {
		t.Fatal("Complete succeeded against a server that only answers 500")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Errorf("err = %v, want an APIError with status 500 ", err)
	}
	if fake.ChatCalls() != 2 {
		t.Errorf("the fake got %d requests, want 2 ", fake.ChatCalls())
	}

	var db, openErr = store.OpenMemory(context.Background(), "model_allfail_"+t.Name())
	if openErr != nil {
		t.Fatalf("OpenMemory: %v", openErr)
	}
	defer db.Close()

	// event row only. The client itself never touches the database — hence the zero first.
	var events, countErr = store.CountEvents(context.Background(), db, store.EventModelError)
	if countErr != nil {
		t.Fatalf("CountEvents: %v", countErr)
	}
	if events != 0 {
		t.Errorf("the failed call already wrote %d events (the client does not touch SQLite)", events)
	}
	if err := store.LogEvent(context.Background(), db, 2, store.EventModelError, apiErr.Error()); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}
	events, countErr = store.CountEvents(context.Background(), db, store.EventModelError)
	if countErr != nil || events != 1 {
		t.Errorf("events(model_error) = %d err=%v, want 1 ", events, countErr)
	}
	var rows, queryErr = store.HistoryOfChat(context.Background(), db, 1, 10)
	if queryErr != nil || len(rows) != 0 {
		t.Errorf("a failed model call stored %d messages (err %v) — only events may be written ", len(rows), queryErr)
	}
}

// TestClientErrorIsNotRetried — a 4xx repeats nothing: a wrong key or a wrong path cannot succeed on
// the second attempt. 429 is the one exception and has a test of its own: the provider named a wait.
func TestClientErrorIsNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404} {
		var fake = testutil.NewStrata(t, "", testutil.StrataReply{Status: status})
		var c = clientFor(t, fake, Options{Attempts: 3, Backoff: 5 * time.Millisecond})

		var _, err = c.Complete(context.Background(), oneRequest("вопрос"))
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != status {
			t.Errorf("status %d: err = %v, want an APIError with the same status", status, err)
			continue
		}
		if apiErr.Retryable() {
			t.Errorf("status %d reported Retryable — retries only 5xx and network errors", status)
		}
		if fake.ChatCalls() != 1 {
			t.Errorf("status %d: the fake got %d requests, want 1", status, fake.ChatCalls())
		}
	}
}

func TestMissingKeyIsRejectedByTheEndpoint(t *testing.T) {
	var fake = testutil.NewStrata(t, "expected-key")
	var c = clientFor(t, fake, Options{}) // no APIKey of

	var _, err = c.Complete(context.Background(), oneRequest("вопрос"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Errorf("err = %v, want a 401 from the endpoint ", err)
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("%d requests, want 1 ", fake.ChatCalls())
	}
}

// the budget of STRATA_TIMEOUT_SECONDS is 120ms, so the FIRST attempt already spends the budget: a
func TestTimeoutEndsTheBudget(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{Content: "поздно", Delay: 400 * time.Millisecond})
	var c = clientFor(t, fake, Options{Timeout: 120 * time.Millisecond, Attempts: 2, Backoff: 5 * time.Millisecond})

	var start = time.Now()
	var _, err = c.Complete(context.Background(), oneRequest("вопрос"))
	var spent = time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded ", err)
	}
	if spent > 300*time.Millisecond {
		t.Errorf("the call took %v with a 120ms budget: the timeout must end the whole budget, not one attempt", spent)
	}
	if fake.ChatCalls() != 1 {
		t.Errorf("the fake got %d requests, want 1 ", fake.ChatCalls())
	}
	if !strings.Contains(err.Error(), TelegramBudgetNotice) {
		t.Errorf("the error text does not name the budget: %v ", err)
	}
}

// must be honoured, so День 4 can cap the whole turn.
func TestDeadlineOfTheHandlerWins(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{Content: "ответ", Delay: 300 * time.Millisecond})
	var c = clientFor(t, fake, Options{Timeout: 5 * time.Second, Attempts: 2})

	var ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	var start = time.Now()
	var _, err = c.Complete(ctx, oneRequest("вопрос"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline of the caller ", err)
	}
	if spent := time.Since(start); spent > 250*time.Millisecond {
		t.Errorf("the call ran %v although the handler gave 100ms ", spent)
	}
}

// Is(), which makes it satisfy net.Error, so the network branch of Retryable would retry a call whose time is
func TestSpentBudgetIsNotRetryable(t *testing.T) {
	for _, err := range []error{
		context.DeadlineExceeded,
		context.Canceled,
		fmt.Errorf("%w: %s", context.DeadlineExceeded, TelegramBudgetNotice),
		fmt.Errorf("%w: %s", ErrStreamStalled, StreamNotice),
		fmt.Errorf("%w: %s", ErrStreamTooLong, StreamNotice),
	} {
		if Retryable(err) {
			t.Errorf("Retryable(%v) = true — a spent budget is not the network class ", err)
		}
	}
}

// ECONNREFUSED on both attempts, and the call reports the failure after exactly two attempts.
func TestNetworkErrorIsRetriedOnce(t *testing.T) {
	var c = clientForNoFake(t, closedPort(t), Options{Attempts: 2, Backoff: 5 * time.Millisecond, Timeout: 2 * time.Second})

	var _, err = c.Complete(context.Background(), oneRequest("вопрос"))
	if err == nil {
		t.Fatal("Complete succeeded against a port nobody listens on ")
	}
	if !Retryable(err) {
		t.Errorf("Retryable(%v) = false — retries the network class too", err)
	}
	if !strings.Contains(err.Error(), "after 2 attempts") {
		t.Errorf("the error does not say how many attempts were made: %v ", err)
	}
}

// change 26: the /v1 suffix is part of the endpoint).
func TestModels(t *testing.T) {
	var fake = testutil.NewStrata(t, "k", testutil.StrataReply{Models: []string{"qwen-test", "llama-3"}})
	var c = clientFor(t, fake, Options{APIKey: "k"})

	var models, err = c.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0] != "qwen-test" || models[1] != "llama-3" {
		t.Errorf("Models = %v, want the two ids of the fake ", models)
	}
	if fake.ModelsCalls() != 1 {
		t.Errorf("the fake got %d GET requests, want 1 ", fake.ModelsCalls())
	}
	if fake.LastAuthorization() != "Bearer k" {
		t.Error("GET /models must carry the key of too")
	}
}

func TestModelsTimeout(t *testing.T) {
	var started = make(chan struct{})
	var server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	var c, newErr = New(Options{
		ChatURL: server.URL + "/v1/chat/completions", ModelsURL: server.URL + "/v1/models", Timeout: 50 * time.Millisecond,
	})
	if newErr != nil {
		t.Fatalf("New: %v", newErr)
	}

	var start = time.Now()
	var _, err = c.Models(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Models error = %v, want context.DeadlineExceeded", err)
	}
	if spent := time.Since(start); spent > 500*time.Millisecond {
		t.Fatalf("Models took %v with a 50ms budget", spent)
	}
	select {
	case <-started:
	default:
		t.Fatal("the hanging server never received the Models request")
	}
}

// the /v1 prefix is missing. A client aimed at a path that already carries /v1 in the wrong place
// retry — repeating a path the server does not serve only wastes the budget.
func TestWrongPathIsA404NotARetry(t *testing.T) {
	var fake = testutil.NewStrata(t, "")
	var c = clientFor(t, fake, Options{ChatURL: fake.BaseURL() + "/chat/completions", Attempts: 2, Backoff: 5 * time.Millisecond})

	var _, err = c.Complete(context.Background(), oneRequest("вопрос"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Errorf("err = %v, want the 404 of", err)
	}
	if fake.ChatCalls() != 0 {
		t.Errorf("the chat endpoint got %d requests, want 0 (the URL of the client does not name it)", fake.ChatCalls())
	}
	if fake.OtherCalls() != 1 {
		t.Errorf("the server got %d unmatched requests, want 1 (a wrong URL is not retryable)", fake.OtherCalls())
	}
	if !strings.Contains(apiErr.Error(), "404") {
		t.Errorf("the text of APIError does not name the status: %v ", apiErr)
	}
}

// resource). Two parallel calls against a slow fake run side by side when the gate is 2, and the
// third waits for a free slot.
func TestConcurrencyGate(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{Content: "ответ", Delay: 200 * time.Millisecond})
	var c = clientFor(t, fake, Options{MaxConcurrent: 2, Timeout: 3 * time.Second, Attempts: 1})

	var ready = make(chan error, 3)
	var start = time.Now()
	for i := 0; i < 3; i++ {
		go func() {
			var _, err = c.Complete(context.Background(), oneRequest("вопрос"))
			ready <- err
		}()
	}
	for i := 0; i < 3; i++ {
		if err := <-ready; err != nil {
			t.Errorf("a gated call failed: %v", err)
		}
	}
	if spent := time.Since(start); spent < 300*time.Millisecond {
		t.Errorf("3 calls of 200ms with a gate of 2 finished in %v — the gate did not hold the third back ", spent)
	}
	if fake.ChatCalls() != 3 {
		t.Errorf("the fake got %d requests, want 3", fake.ChatCalls())
	}
}

// so a caller that gives up while waiting must get an error, and the slot must return to the pool
// afterwards (a leaked slot would block the bot forever).
func TestGateReleasesAfterUse(t *testing.T) {
	var fake = testutil.NewStrata(t, "", testutil.StrataReply{Content: "ответ", Delay: 300 * time.Millisecond})
	var c = clientFor(t, fake, Options{MaxConcurrent: 1, Timeout: 2 * time.Second, Attempts: 1})

	var hold = make(chan error, 1)
	go func() {
		var _, err = c.Complete(context.Background(), oneRequest("занята"))
		hold <- err
	}()
	// let the first call occupy the only slot
	testutil.Eventually(t, 2*time.Second, func() bool { return fake.ChatCalls() == 1 },
		func() string { return "the first request never reached the fake" })

	var ctx, cancel = context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := c.Complete(ctx, oneRequest("второй")); err == nil {
		t.Error("the second call succeeded while the only slot was busy ")
	}
	if err := <-hold; err != nil {
		t.Errorf("the first call failed: %v", err)
	}
	// With the slot free again the same client answers normally — the gate releases, it does not leak.
	if _, err := c.Complete(context.Background(), oneRequest("после")); err != nil {
		t.Errorf("the client stayed blocked after the slot was released: %v ", err)
	}
}

// closed right away, which is the reliable way to get ECONNREFUSED on Windows.
func closedPort(t *testing.T) string {
	t.Helper()
	var ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var addr = ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return "http://" + addr + "/v1/chat/completions"
}

// clientForNoFake — the defaults of clientFor for an endpoint that is not the fake.
func clientForNoFake(t *testing.T, chatURL string, opts Options) *OpenAI {
	t.Helper()
	opts.ChatURL = chatURL
	opts.ModelsURL = strings.TrimSuffix(chatURL, "/chat/completions") + "/models"
	if opts.Model == "" {
		opts.Model = "qwen-test"
	}
	if opts.Attempts <= 0 {
		opts.Attempts = DefaultAttempts
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 5 * time.Millisecond
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 2
	}
	var c, err = New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}
