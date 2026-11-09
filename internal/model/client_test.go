package model

import (
	"strings"
	"testing"
)

// TestAPIErrorKeepsTheProviderBodyOfA200 — LM Studio (and some other OpenAI-compatible servers) answer 200 with
// an error object while a model is loading. "200: client error" hid that reason; the body must reach the person
// and the log, while a 4xx keeps its short class text and does not echo a provider body.
func TestAPIErrorKeepsTheProviderBodyOfA200(t *testing.T) {
	var text = (&APIError{Status: 200, Body: "model qwen/qwen3.8-27b is still loading"}).Error()
	if !strings.Contains(text, "still loading") {
		t.Fatalf("the 200 body is not visible: %q", text)
	}
	if !strings.Contains(text, "200") {
		t.Fatalf("the 200 status is not named: %q", text)
	}
	var rejected = (&APIError{Status: 400, Body: "the prompt is too long"}).Error()
	if strings.Contains(rejected, "the prompt is too long") || !strings.Contains(rejected, "client error") {
		t.Fatalf("a 400 must keep the class text and not echo the body: %q", rejected)
	}
}
