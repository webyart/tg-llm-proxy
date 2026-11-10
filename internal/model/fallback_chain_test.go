package model

import (
	"context"
	"strings"
	"testing"
)

type chainFake struct {
	name    string
	content string
	err     error
	deltas  []string
	calls   int
	seen    []string
}

func (f *chainFake) Complete(_ context.Context, req Request) (Response, error) {
	f.calls++
	f.seen = append(f.seen, req.Model)
	if f.err != nil {
		return Response{}, f.err
	}
	return Response{Content: f.content, Model: f.name}, nil
}

func (f *chainFake) Models(context.Context) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return []string{f.name}, nil
}

func (f *chainFake) Stream(_ context.Context, req Request, handler StreamHandler) (Response, error) {
	f.calls++
	f.seen = append(f.seen, req.Model)
	for _, delta := range f.deltas {
		if handler.Delta != nil {
			if err := handler.Delta(delta); err != nil {
				return Response{}, err
			}
		}
	}
	if f.err != nil {
		return Response{}, f.err
	}
	return Response{Content: f.content, Model: f.name}, nil
}

func chainTestError(status int) error { return &APIError{Status: status} }

func TestFallbackChainTriesReservesInOrder(t *testing.T) {
	var primary = &chainFake{name: "primary", err: chainTestError(503)}
	var first = &chainFake{name: "first", err: chainTestError(429)}
	var second = &chainFake{name: "second", content: "reserve answer"}
	var chain = NewFallbackChain(primary, []Reserve{{Model: "reserve-1", Client: first}, {Model: "reserve-2", Client: second}},
		ChainOptions{PrimaryModel: "primary"})
	var response, err = chain.Complete(context.Background(), Request{Model: "primary", Messages: []Message{{Role: "user", Content: "q"}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Content != "reserve answer" || primary.calls != 1 || first.calls != 1 || second.calls != 1 {
		t.Fatalf("response=%+v calls=%d/%d/%d, want the third provider to answer once", response, primary.calls, first.calls, second.calls)
	}
	if second.seen[0] != "reserve-2" {
		t.Fatalf("the reserve request model = %q, want reserve-2", second.seen[0])
	}
}

func TestFallbackChainStopsOnANonRetryableError(t *testing.T) {
	var primary = &chainFake{name: "primary", err: chainTestError(400)}
	var reserve = &chainFake{name: "reserve", content: "should not run"}
	var chain = NewFallbackChain(primary, []Reserve{{Model: "reserve", Client: reserve}}, ChainOptions{PrimaryModel: "primary"})
	if _, err := chain.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("Complete returned nil on a 400")
	}
	if reserve.calls != 0 {
		t.Fatalf("a 400 was replayed at the reserve (%d calls)", reserve.calls)
	}
}

func TestFallbackChainNamesEveryModelWhenAllFail(t *testing.T) {
	var primary = &chainFake{name: "primary", err: chainTestError(503)}
	var reserve = &chainFake{name: "reserve", err: chainTestError(502)}
	var chain = NewFallbackChain(primary, []Reserve{{Model: "reserve-model", Client: reserve}}, ChainOptions{PrimaryModel: "primary"})
	var _, err = chain.Complete(context.Background(), Request{})
	if err == nil {
		t.Fatal("Complete returned nil although every model failed")
	}
	for _, want := range []string{"primary", "reserve-model"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not name %q", err, want)
		}
	}
}

func TestFallbackChainStreamNeverReplaysAfterADelta(t *testing.T) {
	var primary = &chainFake{name: "primary", deltas: []string{"часть ответа"}, err: chainTestError(503)}
	var reserve = &chainFake{name: "reserve", content: "дубль"}
	var chain = NewFallbackChain(primary, []Reserve{{Model: "reserve", Client: reserve}}, ChainOptions{PrimaryModel: "primary"})
	var streamer, ok = chain.(Streamer)
	if !ok {
		t.Fatal("the chain lost the Streamer interface")
	}
	var _, err = streamer.Stream(context.Background(), Request{}, StreamHandler{Delta: func(string) error { return nil }})
	if err == nil {
		t.Fatal("Stream returned nil although the primary failed after a delta")
	}
	if reserve.calls != 0 {
		t.Fatalf("the reserve was streamed after text reached the caller (%d calls)", reserve.calls)
	}
}

func TestFallbackChainStreamFallsBackBeforeADelta(t *testing.T) {
	var primary = &chainFake{name: "primary", err: chainTestError(503)}
	var reserve = &chainFake{name: "reserve", content: "ответ резерва"}
	var chain = NewFallbackChain(primary, []Reserve{{Model: "reserve", Client: reserve}}, ChainOptions{PrimaryModel: "primary"})
	var streamer = chain.(Streamer)
	var response, err = streamer.Stream(context.Background(), Request{}, StreamHandler{Delta: func(string) error { return nil }})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if response.Content != "ответ резерва" || reserve.calls != 1 {
		t.Fatalf("response=%+v reserve calls=%d, want one reserve attempt", response, reserve.calls)
	}
}

func TestFallbackChainModelsFallsBack(t *testing.T) {
	var primary = &chainFake{name: "primary", err: chainTestError(503)}
	var reserve = &chainFake{name: "reserve"}
	var chain = NewFallbackChain(primary, []Reserve{{Model: "reserve", Client: reserve}}, ChainOptions{PrimaryModel: "primary"})
	var models, err = chain.Models(context.Background())
	if err != nil || len(models) != 1 || models[0] != "reserve" {
		t.Fatalf("Models = %v, %v", models, err)
	}
}

func TestFallbackChainWithoutUsableReservesReturnsThePrimary(t *testing.T) {
	var primary = &chainFake{name: "primary", content: "answer"}
	var chain = NewFallbackChain(primary, []Reserve{{Model: "", Client: &chainFake{}}}, ChainOptions{PrimaryModel: "primary"})
	if chain != primary {
		t.Fatal("an empty chain still wrapped the primary")
	}
	if _, err := chain.Complete(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
}
