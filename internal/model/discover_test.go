package model

import (
	"context"
	"errors"
	"testing"
	"time"
)

// listClient is a Client stub: the only method discovery uses is Models.
type listClient struct {
	models []string
	err    error
}

func (c *listClient) Complete(context.Context, Request) (Response, error) {
	return Response{}, errors.New("not used")
}

func (c *listClient) Models(context.Context) ([]string, error) { return c.models, c.err }

// TestDiscoverModelNameAdoptsTheOnlyListedModel — a single-model STRATA can answer with a model other than the
// configured name (or reject that name). Adopting the only listed model keeps the primary path working.
func TestDiscoverModelNameAdoptsTheOnlyListedModel(t *testing.T) {
	var name, changed = DiscoverModelName(context.Background(), &listClient{models: []string{"loaded-model"}}, "configured", time.Second)
	if !changed || name != "loaded-model" {
		t.Fatalf("discovered=%q changed=%v, want the only listed model", name, changed)
	}
}

func TestDiscoverModelNameKeepsTheConfiguredName(t *testing.T) {
	for _, test := range []struct {
		name     string
		client   Client
		changed  bool
		expected string
	}{
		{name: "same model", client: &listClient{models: []string{"configured"}}, expected: "configured"},
		{name: "several models", client: &listClient{models: []string{"a", "b"}}, expected: "configured"},
		{name: "listing failed", client: &listClient{err: errors.New("down")}, expected: "configured"},
		{name: "empty listing", client: &listClient{models: nil}, expected: "configured"},
		{name: "nil client", client: nil, expected: "configured"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var name, changed = DiscoverModelName(context.Background(), test.client, "configured", time.Second)
			if changed != test.changed || name != test.expected {
				t.Fatalf("discovered=%q changed=%v, want %q changed=%v", name, changed, test.expected, test.changed)
			}
		})
	}
}
