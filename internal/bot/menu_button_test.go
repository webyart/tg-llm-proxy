package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"bot/internal/config"
	tgbot "github.com/go-telegram/bot"
)

func TestPublishMenuButtonPayload(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  config.Config
		want map[string]any
	}{
		{
			name: "Mini App enabled",
			cfg:  config.Config{MiniAppEnabled: true, MiniAppPublicURL: "https://example.test/miniapp"},
			want: map[string]any{
				"type":    "web_app",
				"text":    "Меню",
				"web_app": map[string]any{"url": "https://example.test/miniapp"},
			},
		},
		{
			name: "Mini App disabled",
			cfg:  config.Config{MiniAppPublicURL: "https://example.test/miniapp"},
			want: map[string]any{"type": "commands"},
		},
		{
			name: "Mini App URL missing",
			cfg:  config.Config{MiniAppEnabled: true},
			want: map[string]any{"type": "commands"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/setChatMenuButton") {
					t.Errorf("request path = %q", r.URL.Path)
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Errorf("ParseMultipartForm: %v", err)
				}
				if chatID := r.FormValue("chat_id"); chatID != "" {
					t.Errorf("chat_id = %q, want a global menu button", chatID)
				}
				if err := json.Unmarshal([]byte(r.FormValue("menu_button")), &got); err != nil {
					t.Errorf("menu_button JSON: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			}))
			defer server.Close()
			tg, err := tgbot.New("123:test-token", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe(), tgbot.WithHTTPClient(time.Second, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			d := &Dispatcher{deps: Deps{Cfg: test.cfg, Token: "123:test-token"}}
			if err := d.PublishMenuButton(context.Background(), tg); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("menu_button = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestPublishMenuButtonErrorIsSanitized(t *testing.T) {
	const token = "123:secret-token-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"bad menu button"}`))
	}))
	defer server.Close()
	tg, err := tgbot.New(token, tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe(), tgbot.WithHTTPClient(time.Second, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{deps: Deps{Token: token}}
	err = d.PublishMenuButton(context.Background(), tg)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("PublishMenuButton error = %v", err)
	}
}
