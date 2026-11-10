package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

const providerTestRow = `[{"name":"kimi","base_url":"https://api.moonshot.ai/v1","api_key":"sk-test","models":["kimi-k3.1-pro","kimi-k2.6"],"default_model":"kimi-k3.1-pro"}]`

func TestModelCommandListsSelectsAndResetsProviderModels(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "MODEL_PROVIDERS": providerTestRow})
	var m = &fakeModel{models: []string{cfg.StrataModel, "kimi:kimi-k3.1-pro", "kimi:kimi-k2.6"}, response: model.Response{Content: "ответ"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)

	if err := dispatch(t, d, testEvent(100, 11, 500, "/model")); err != nil {
		t.Fatalf("/model: %v", err)
	}
	var info = r.joined()
	if !strings.Contains(info, "strata:"+cfg.StrataModel) {
		t.Fatalf(" /model must name the primary provider and the model , got %q", info)
	}

	if err := dispatch(t, d, testEvent(100, 11, 501, "/model check")); err != nil {
		t.Fatalf("/model check: %v", err)
	}
	var checked = r.texts()[len(r.texts())-1]
	for _, want := range []string{"kimi:kimi-k3.1-pro", "kimi:kimi-k2.6", cfg.StrataModel} {
		if !strings.Contains(checked, want) {
			t.Fatalf(" /model check must list %q, got %q", want, checked)
		}
	}

	if err := dispatch(t, d, testEvent(100, 11, 502, "/model select kimi:kimi-k2.6")); err != nil {
		t.Fatalf("/model select kimi:kimi-k2.6: %v", err)
	}
	var selected, found, err = store.ChatModel(context.Background(), db, 100)
	if err != nil || !found || selected != "kimi:kimi-k2.6" {
		t.Fatalf("ChatModel=%q found=%v err=%v, want the provider-prefixed id", selected, found, err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "kimi:kimi-k2.6") {
		t.Fatalf("the selection reply must name the provider and the model , got %q", got)
	}

	if err := dispatch(t, d, testEvent(100, 11, 503, "/model")); err != nil {
		t.Fatalf("/model after select: %v", err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "kimi:kimi-k2.6") || !strings.Contains(got, "strata:"+cfg.StrataModel) || !strings.Contains(got, "api.moonshot.ai") {
		t.Fatalf("/model must show the selected provider/model, its endpoint and the default, got %q", got)
	}

	if err := dispatch(t, d, testEvent(100, 11, 504, "/ask вопрос")); err != nil {
		t.Fatalf("/ask with a provider selection: %v", err)
	}
	if got := m.lastRequest().Model; got != "kimi:kimi-k2.6" {
		t.Fatalf("the request model = %q, want the stored provider-prefixed id", got)
	}

	if err := dispatch(t, d, testEvent(100, 11, 505, "/model select kimi:kimi-k9")); err != nil {
		t.Fatalf("/model select of an unlisted provider model: %v", err)
	}
	if !strings.Contains(r.texts()[len(r.texts())-1], "отсутствует") {
		t.Fatalf("an unlisted provider model must be refused, got %q", r.texts()[len(r.texts())-1])
	}
	selected, _, _ = store.ChatModel(context.Background(), db, 100)
	if selected != "kimi:kimi-k2.6" {
		t.Fatalf("a refused selection changed the store to %q", selected)
	}

	if err := dispatch(t, d, testEvent(100, 11, 506, "/model select "+cfg.StrataModel)); err != nil {
		t.Fatalf("/model select of a plain model: %v", err)
	}
	if selected, found, err = store.ChatModel(context.Background(), db, 100); err != nil || !found || selected != cfg.StrataModel {
		t.Fatalf("a plain model selection must keep working: selected=%q found=%v err=%v", selected, found, err)
	}

	if err := dispatch(t, d, testEvent(100, 11, 507, "/model reset")); err != nil {
		t.Fatalf("/model reset: %v", err)
	}
	if _, found, err = store.ChatModel(context.Background(), db, 100); err != nil || found {
		t.Fatalf("/model reset must clear the provider selection, found=%v err=%v", found, err)
	}
	if got := r.texts()[len(r.texts())-1]; !strings.Contains(got, "strata:"+cfg.StrataModel) {
		t.Fatalf("/model reset must name the default provider and model , got %q", got)
	}
}

func TestModelCommandWithoutProvidersKeepsPlainNames(t *testing.T) {
	var cfg = testConfig(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"})
	var m = &fakeModel{models: []string{"qwen2.5-7b", "qwen2.5-14b"}}
	var r = &fakeReplier{}
	var d = newTestDispatcher(t, cfg, m, r, testStore(t))

	if err := dispatch(t, d, testEvent(100, 11, 510, "/model")); err != nil {
		t.Fatalf("/model: %v", err)
	}
	if got := r.texts()[len(r.texts())-1]; strings.Contains(got, "strata:") {
		t.Fatalf("a bot without MODEL_PROVIDERS must keep the old plain output, got %q", got)
	}
	if err := dispatch(t, d, testEvent(100, 11, 511, "/model select qwen2.5-14b")); err != nil {
		t.Fatalf("/model select: %v", err)
	}
	if got := r.texts()[len(r.texts())-1]; strings.Contains(got, "strata:") {
		t.Fatalf("a plain selection must stay plain without providers, got %q", got)
	}
	var selected, found, err = store.ChatModel(context.Background(), d.deps.DB, 100)
	if err != nil || !found || selected != "qwen2.5-14b" {
		t.Fatalf("ChatModel=%q found=%v err=%v", selected, found, err)
	}
}

var _ model.Client = (*fakeModel)(nil)
