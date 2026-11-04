//go:build livecheck

package bot

import (
	"context"
	"encoding/json"
	"testing"

	"bot/internal/model"
)

func TestLiveURLReaderRealNetwork(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		wantResult bool
	}{
		{name: "public https site", url: "https://example.com/", wantResult: true},
		{name: "cloud metadata link-local", url: "https://169.254.169.254/latest/meta-data/", wantResult: false},
		{name: "plain http", url: "http://example.com/", wantResult: false},
		{name: "dns name resolving to loopback", url: "https://localhost/", wantResult: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arguments, _ := json.Marshal(map[string]string{"url": tc.url})
			payload, err := executeURLReader(context.Background(), model.ToolCall{Name: "read_url", Arguments: string(arguments)})
			if err != nil {
				t.Fatalf("top-level error=%v", err)
			}
			var got map[string]string
			if err := json.Unmarshal([]byte(payload), &got); err != nil {
				t.Fatalf("payload=%s err=%v", payload, err)
			}
			if tc.wantResult && got["result"] == "" {
				t.Fatalf("expected a result, payload=%s", payload)
			}
			if !tc.wantResult && got["result"] != "" {
				t.Fatalf("expected rejection, payload=%s", payload)
			}
			t.Logf("outcome=%q error=%q resultLength=%d", got["result"], got["error"], len(got["result"]))
		})
	}
}
