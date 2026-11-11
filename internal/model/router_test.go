package model

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// routerFake is a Client without Stream, so a router built on it must not satisfy Streamer either.
type routerFake struct {
	name      string
	models    []string
	modelsErr error
	content   string
	err       error
	calls     int
	seen      []string
}

func (f *routerFake) Complete(_ context.Context, req Request) (Response, error) {
	f.calls++
	f.seen = append(f.seen, req.Model)
	if f.err != nil {
		return Response{}, f.err
	}
	return Response{Content: f.content, Model: f.name}, nil
}

func (f *routerFake) Models(context.Context) ([]string, error) {
	if f.modelsErr != nil {
		return nil, f.modelsErr
	}
	return append([]string{}, f.models...), nil
}

type routerStreamFake struct {
	*routerFake
	deltas []string
}

func (f *routerStreamFake) Stream(_ context.Context, req Request, handler StreamHandler) (Response, error) {
	var response, err = f.Complete(context.Background(), req)
	for _, delta := range f.deltas {
		if handler.Delta != nil {
			if deltaErr := handler.Delta(delta); deltaErr != nil {
				return Response{}, deltaErr
			}
		}
	}
	return response, err
}

func providerRouter(t *testing.T, def Client, providers ...Provider) Client {
	t.Helper()
	return NewRouter(RouterOptions{Default: def, DefaultModel: "qwen", Providers: providers})
}

func TestRouterRoutesDefaultAndPrefixedModels(t *testing.T) {
	for name, testCase := range map[string]struct {
		model      string
		wantClient string
		wantModel  string
		wantErr    string
	}{
		"no prefix":        {"", "default", "", ""},
		"plain model":      {"qwen-alt", "default", "qwen-alt", ""},
		"strata prefix":    {"strata:qwen-alt", "default", "qwen-alt", ""},
		"provider model":   {"kimi:kimi-k2", "kimi", "kimi-k2", ""},
		"provider default": {"kimi:", "kimi", "kimi-k3", ""},
		"unlisted model":   {"kimi:kimi-k9", "", "", "not listed"},
		"unknown provider": {"nope:kimi-k2", "", "", "unknown model provider"},
	} {
		var def = &routerFake{name: "default", content: "default answer"}
		var kimi = &routerFake{name: "kimi", content: "kimi answer"}
		var router = providerRouter(t, def, Provider{Name: "kimi", Models: []string{"kimi-k3", "kimi-k2"}, DefaultModel: "kimi-k3", Client: kimi})
		var response, err = router.Complete(context.Background(), Request{Model: testCase.model})
		if testCase.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("%s: err = %v, want %q", name, err, testCase.wantErr)
			}
			if def.calls != 0 || kimi.calls != 0 {
				t.Fatalf("%s: a refused id must not call a provider: default=%d kimi=%d", name, def.calls, kimi.calls)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: Complete: %v", name, err)
		}
		var gotClient = "default"
		if kimi.calls > 0 {
			gotClient = "kimi"
		}
		if gotClient != testCase.wantClient {
			t.Fatalf("%s: served by %q, want %q", name, gotClient, testCase.wantClient)
		}
		var seen = def.seen
		if gotClient == "kimi" {
			seen = kimi.seen
		}
		if len(seen) != 1 || seen[0] != testCase.wantModel {
			t.Fatalf("%s: request model = %v, want %q", name, seen, testCase.wantModel)
		}
		if response.Content == "" {
			t.Fatalf("%s: empty answer", name)
		}
	}
}

func TestRouterModelsAggregatesGroupedAndDeduplicated(t *testing.T) {
	var def = &routerFake{name: "default", models: []string{"qwen", "kimi:kimi-k3"}}
	var kimi = &routerFake{name: "kimi"}
	var zeta = &routerFake{name: "zeta"}
	var router = providerRouter(t, def,
		Provider{Name: "zeta", Models: []string{"z1", "z2"}, Client: zeta},
		Provider{Name: "kimi", Models: []string{"kimi-k3", "kimi-k2"}, Client: kimi},
	)
	var models, err = router.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	var want = []string{"qwen", "kimi:kimi-k3", "kimi:kimi-k2", "zeta:z1", "zeta:z2"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("Models = %v, want %v", models, want)
	}
}

func TestRouterModelsKeepsProvidersWhenDefaultCatalogueFails(t *testing.T) {
	var def = &routerFake{name: "default", modelsErr: &APIError{Status: 503}}
	var kimi = &routerFake{name: "kimi"}
	var router = providerRouter(t, def, Provider{Name: "kimi", Models: []string{"kimi-k3"}, Client: kimi})
	var models, err = router.Models(context.Background())
	if err != nil {
		t.Fatalf("Models of a failed default catalogue: %v", err)
	}
	if !reflect.DeepEqual(models, []string{"kimi:kimi-k3"}) {
		t.Fatalf("Models = %v, want the configured provider list", models)
	}
}

func TestRouterFallsBackToDefaultOnceOnRetryableError(t *testing.T) {
	var def = &routerFake{name: "default", content: "default answer"}
	var kimi = &routerFake{name: "kimi", err: &APIError{Status: 503}}
	var logs bytes.Buffer
	var router = NewRouter(RouterOptions{
		Default:      def,
		DefaultModel: "qwen",
		Providers:    []Provider{{Name: "kimi", Models: []string{"kimi-k3"}, Client: kimi}},
		Logger:       slog.New(slog.NewTextHandler(&logs, nil)),
	})
	var response, err = router.Complete(context.Background(), Request{Model: "kimi:kimi-k3"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Content != "default answer" {
		t.Fatalf("answer = %q, want the default model", response.Content)
	}
	if kimi.calls != 1 || def.calls != 1 {
		t.Fatalf("calls provider=%d default=%d, want one each", kimi.calls, def.calls)
	}
	if len(def.seen) != 1 || def.seen[0] != "qwen" {
		t.Fatalf("the fallback request model = %v, want qwen", def.seen)
	}
	if !strings.Contains(logs.String(), "selected provider") {
		t.Fatalf("the fallback was not logged: %q", logs.String())
	}
}

func TestRouterDoesNotFallBackOnNonRetryableError(t *testing.T) {
	var def = &routerFake{name: "default", content: "should not run"}
	var kimi = &routerFake{name: "kimi", err: &APIError{Status: 401}}
	var router = providerRouter(t, def, Provider{Name: "kimi", Models: []string{"kimi-k3"}, Client: kimi})
	var _, err = router.Complete(context.Background(), Request{Model: "kimi:kimi-k3"})
	if err == nil {
		t.Fatal("Complete returned nil on a 401")
	}
	if def.calls != 0 {
		t.Fatalf("a 401 was replayed on the default model (%d calls)", def.calls)
	}
}

func TestRouterStreamerRoutesAndFallsBackBeforeADelta(t *testing.T) {
	var def = &routerStreamFake{routerFake: &routerFake{name: "default", content: "default stream"}}
	var kimi = &routerStreamFake{routerFake: &routerFake{name: "kimi", err: &APIError{Status: 503}}}
	var router = providerRouter(t, def, Provider{Name: "kimi", Models: []string{"kimi-k3"}, Client: kimi})
	var streamer, ok = router.(Streamer)
	if !ok {
		t.Fatal("the router lost the Streamer interface")
	}
	var response, err = streamer.Stream(context.Background(), Request{Model: "kimi:kimi-k3"}, StreamHandler{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if response.Content != "default stream" || kimi.calls != 1 || def.calls != 1 {
		t.Fatalf("response=%+v provider=%d default=%d", response, kimi.calls, def.calls)
	}
	if len(def.seen) != 1 || def.seen[0] != "qwen" {
		t.Fatalf("the fallback stream model = %v, want qwen", def.seen)
	}
}

func TestRouterStreamNeverReplaysAfterADelta(t *testing.T) {
	var def = &routerStreamFake{routerFake: &routerFake{name: "default", content: "duplicate"}}
	var kimi = &routerStreamFake{routerFake: &routerFake{name: "kimi", err: &APIError{Status: 503}}, deltas: []string{"часть"}}
	var router = providerRouter(t, def, Provider{Name: "kimi", Models: []string{"kimi-k3"}, Client: kimi})
	var streamer = router.(Streamer)
	var got []string
	var _, err = streamer.Stream(context.Background(), Request{Model: "kimi:kimi-k3"}, StreamHandler{Delta: func(text string) error {
		got = append(got, text)
		return nil
	}})
	if err == nil {
		t.Fatal("Stream returned nil although the provider failed after a delta")
	}
	if def.calls != 0 {
		t.Fatalf("the default model was streamed after text reached the caller (%d calls)", def.calls)
	}
	if !reflect.DeepEqual(got, []string{"часть"}) {
		t.Fatalf("deltas = %v, want the provider text once", got)
	}
}

func TestRouterStreamerCapabilityFollowsTheDefault(t *testing.T) {
	var nonStreaming = &routerFake{name: "default"}
	var providerStream = &routerStreamFake{routerFake: &routerFake{name: "kimi", content: "kimi"}}
	var router = providerRouter(t, nonStreaming, Provider{Name: "kimi", Models: []string{"kimi-k3"}, Client: providerStream})
	if _, ok := router.(Streamer); ok {
		t.Fatal("a router over a non-streaming default must not implement Streamer")
	}

	var streamingDefault = &routerStreamFake{routerFake: &routerFake{name: "default", content: "default"}}
	var plainProvider = &routerFake{name: "kimi", content: "kimi"}
	router = providerRouter(t, streamingDefault, Provider{Name: "kimi", Models: []string{"kimi-k3"}, Client: plainProvider})
	var streamer, ok = router.(Streamer)
	if !ok {
		t.Fatal("a router over a streaming default must implement Streamer")
	}
	if _, err := streamer.Stream(context.Background(), Request{Model: "kimi:kimi-k3"}, StreamHandler{}); err == nil || !strings.Contains(err.Error(), "cannot stream") {
		t.Fatalf("a non-streaming provider must refuse Stream clearly, got %v", err)
	}
}

func TestRouterWithoutProvidersReturnsTheDefaultUntouched(t *testing.T) {
	var def = &routerFake{name: "default"}
	if got := NewRouter(RouterOptions{Default: def}); got != Client(def) {
		t.Fatalf("NewRouter wrapped a client with no providers: %T", got)
	}
}

func TestRouterReportsUnlistedModelError(t *testing.T) {
	var def = &routerFake{name: "default"}
	var kimi = &routerFake{name: "kimi"}
	var router = providerRouter(t, def, Provider{Name: "kimi", Models: []string{"kimi-k3"}, Client: kimi})
	var _, err = router.Complete(context.Background(), Request{Model: "kimi:kimi-k2"})
	if err == nil {
		t.Fatal("an unlisted model must be refused")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("a routing error must not look like a provider response: %v", err)
	}
}

type transcribingFake struct {
	*chainFake
	text string
}

func (f *transcribingFake) Transcribe(context.Context, TranscriptionRequest) (string, error) {
	return f.text, nil
}

func TestRouterKeepsTheDefaultTranscriber(t *testing.T) {
	var def = &transcribingFake{chainFake: &chainFake{name: "default"}, text: "привет"}
	var router = NewRouter(RouterOptions{
		Default:      def,
		DefaultModel: "primary",
		Providers:    []Provider{{Name: "kimi", Models: []string{"kimi-k3"}, Client: &chainFake{name: "kimi"}}},
	})
	var transcriber, ok = router.(Transcriber)
	if !ok {
		t.Fatal("router lost the Transcriber interface")
	}
	var text, err = transcriber.Transcribe(context.Background(), TranscriptionRequest{})
	if err != nil || text != "привет" {
		t.Fatalf("Transcribe = %q, %v", text, err)
	}
}

func TestRouterKeepsStreamAndTranscribeTogether(t *testing.T) {
	var def = &streamTranscribingFake{transcribingFake: &transcribingFake{chainFake: &chainFake{name: "default"}, text: "ok"}}
	var router = NewRouter(RouterOptions{
		Default:   def,
		Providers: []Provider{{Name: "kimi", Models: []string{"kimi-k3"}, Client: &chainFake{name: "kimi"}}},
	})
	if _, ok := router.(Transcriber); !ok {
		t.Fatal("router lost Transcriber")
	}
	if _, ok := router.(Streamer); !ok {
		t.Fatal("router lost Streamer")
	}
}

type streamTranscribingFake struct {
	*transcribingFake
}

func (f *streamTranscribingFake) Stream(ctx context.Context, req Request, handler StreamHandler) (Response, error) {
	return Response{Content: "stream"}, nil
}
