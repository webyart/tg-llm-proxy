package model

import (
	"context"
	"strings"
	"time"
)

// DiscoverModelName resolves the model name a single-model server actually serves.
//
// STRATA loads one model at a time and may keep the configured name while answering with the loaded one (its
// proxy rewrites the request), or it may reject a name it no longer offers. When the endpoint lists exactly one
// model and it is not the configured one, that model is the truthful choice: using it keeps the primary path
// working instead of sending every request through the fallback. A server with several models or a failed
// listing keeps the configured name — discovery never guesses in an ambiguous case.
//
// It is meant for startup, before the client is serving requests.
func DiscoverModelName(ctx context.Context, client Client, configured string, timeout time.Duration) (string, bool) {
	if client == nil {
		return configured, false
	}
	var probeCtx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()
	var models, err = client.Models(probeCtx)
	if err != nil || len(models) != 1 {
		return configured, false
	}
	var only = strings.TrimSpace(models[0])
	if only == "" || only == configured {
		return configured, false
	}
	return only, true
}
