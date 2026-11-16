package bot

import (
	"context"
	"strings"
	"testing"

	"bot/internal/model"
	"bot/internal/store"
)

// ragFixture stores one RAG document and returns the dispatcher over a fresh in-memory database.
func ragFixture(t *testing.T, env map[string]string, seed string, documents map[string]string) (*Dispatcher, context.Context) {
	t.Helper()
	var cfg = testConfig(t, env)
	var d = newTestDispatcher(t, cfg, nil, nil, testStoreNamed(t, seed))
	var ctx = context.Background()
	for name, content := range documents {
		if _, _, err := store.PutRAGDocument(ctx, d.deps.DB, 100, 11, name, content); err != nil {
			t.Fatalf("PutRAGDocument(%s): %v", name, err)
		}
	}
	return d, ctx
}

func ragMessageText(messages []model.Message) string {
	for _, message := range messages {
		if message.Role == model.RoleSystem && strings.Contains(message.Content, "Источник:") {
			return message.Content
		}
	}
	return ""
}

// TestRAGContextDeduplicatesIdenticalChunks — the same text retrieved from two documents used to be
// inserted twice and paid for twice. It is now one fragment, and its sources still list both documents.
func TestRAGContextDeduplicatesIdenticalChunks(t *testing.T) {
	var d, ctx = ragFixture(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}, "dedupe", map[string]string{
		"one.txt": "Aurora deployment uses a local runbook.",
		"two.txt": "Aurora deployment uses a local runbook.",
	})
	var messages = []model.Message{
		{Role: model.RoleSystem, Content: "persona"},
		{Role: model.RoleUser, Content: "Aurora deployment?"},
	}
	var out, sources, err = d.addRAGContext(ctx, 100, 11, "Aurora deployment", messages)
	if err != nil {
		t.Fatalf("addRAGContext: %v", err)
	}
	var text = ragMessageText(out)
	if text == "" {
		t.Fatalf("no RAG message in %+v", out)
	}
	if count := strings.Count(text, "Aurora deployment uses a local runbook."); count != 1 {
		t.Fatalf("the identical chunk appears %d times in the RAG block:\n%s", count, text)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %v, want both document names", sources)
	}
}

// TestRAGContextSkipsTheInlineDocument — an /ask attachment carries the whole document in the newest
// user message, so retrieving its chunks again is duplicate document context. A chunk that is only
// reachable through the inline document leaves no RAG block at all (no header without fragments).
func TestRAGContextSkipsTheInlineDocument(t *testing.T) {
	var d, ctx = ragFixture(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}, "inline", map[string]string{
		"inline.pdf": "Aurora inline document text.",
		"other.txt":  "Aurora other document text.",
	})
	var messages = []model.Message{
		{Role: model.RoleSystem, Content: "persona"},
		{Role: model.RoleUser, Content: "Aurora?"},
	}
	var out, sources, err = d.addRAGContextExcluding(ctx, 100, 11, "Aurora", messages, "inline.pdf")
	if err != nil {
		t.Fatalf("addRAGContextExcluding: %v", err)
	}
	var text = ragMessageText(out)
	if strings.Contains(text, "inline document") {
		t.Fatalf("the inline document was retrieved a second time:\n%s", text)
	}
	if !strings.Contains(text, "other document") {
		t.Fatalf("the other document was lost:\n%s", text)
	}
	if len(sources) != 1 || sources[0] != "other.txt" {
		t.Fatalf("sources = %v, want only other.txt", sources)
	}

	// Only the inline document matches: nothing new, so no RAG message and no usage event.
	var solo, soloCtx = ragFixture(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}, "solo", map[string]string{
		"inline.pdf": "Aurora inline document text.",
	})
	var unchanged, none, soloErr = solo.addRAGContextExcluding(soloCtx, 100, 11, "Aurora", messages, "inline.pdf")
	if soloErr != nil {
		t.Fatalf("addRAGContextExcluding: %v", soloErr)
	}
	if len(none) != 0 || len(unchanged) != len(messages) {
		t.Fatalf("an all-duplicate retrieval changed the request: messages=%+v sources=%v", unchanged, none)
	}
}

// TestDocumentAttachmentWithCacheFriendlyRAGStillReachesTheModel — with the volatile RAG block at the
// tail, the attachment must still be written into the newest USER message, not into whatever message
// happens to be last. The old guard compared the last message with RoleUser and would have failed the
// whole answer for an opt-in flag that only changes prompt order.
func TestDocumentAttachmentWithCacheFriendlyRAGStillReachesTheModel(t *testing.T) {
	var cfg = testConfig(t, map[string]string{
		"TELEGRAM_ALLOWED_USERS": "11", "PROMPT_CACHE_FRIENDLY": "true", "MAX_CONTEXT_CHARS": "4096",
	})
	var m = &fakeModel{response: model.Response{Content: "ответ по документу"}}
	var r = &fakeReplier{}
	var db = testStore(t)
	var d = newTestDispatcher(t, cfg, m, r, db)
	d.deps.Attachments = &fakeAttachmentDownloader{data: []byte("Aurora attached document text"), mime: "text/plain"}
	if _, _, err := store.PutRAGDocument(context.Background(), db, 100, 11, "other.txt", "Aurora deployment uses a local runbook."); err != nil {
		t.Fatalf("PutRAGDocument: %v", err)
	}
	var ev = testEvent(100, 11, 1331, "Сделай выжимку по Aurora")
	ev.Attachment = &Attachment{Kind: AttachmentDocument, FileID: "doc", FileName: "attached.txt", MimeType: "text/plain"}
	if err := dispatch(t, d, ev); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var messages = m.lastRequest().Messages
	var userIndex = -1
	for index, message := range messages {
		if message.Role == model.RoleUser {
			userIndex = index
		}
	}
	if userIndex < 0 || !strings.Contains(messages[userIndex].Content, "Документ attached.txt:") {
		t.Fatalf("the attachment did not reach the newest user message: %+v", contents(messages))
	}
	if userIndex != len(messages)-2 || messages[len(messages)-1].Role != model.RoleSystem {
		t.Fatalf("RAG is not the trailing message after the attachment question: %+v", contents(messages))
	}
	if !strings.Contains(ragMessageText(messages), "other.txt") {
		t.Fatalf("the RAG block is missing: %+v", contents(messages))
	}
}

// TestPromptCacheFriendlyMovesOnlyRAGToTheTail — the persona and the tool instruction stay the stable
// prefix; only the volatile RAG block moves. With the row off (the default) the old position is kept,
// so existing answers and tests are unchanged unless an operator opts in.
func TestPromptCacheFriendlyMovesOnlyRAGToTheTail(t *testing.T) {
	var messages = []model.Message{
		{Role: model.RoleSystem, Content: "persona"},
		{Role: model.RoleUser, Content: "Aurora?"},
	}
	var run = func(t *testing.T, env map[string]string, seed string) []model.Message {
		t.Helper()
		var d, ctx = ragFixture(t, env, seed, map[string]string{"handbook.txt": "Aurora deployment uses a local runbook."})
		var out, sources, err = d.addRAGContext(ctx, 100, 11, "Aurora deployment", messages)
		if err != nil || len(sources) != 1 {
			t.Fatalf("addRAGContext sources=%v err=%v", sources, err)
		}
		return out
	}
	var off = run(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11"}, "cache-off")
	if len(off) != 3 || off[0].Content != "persona" || off[1].Role != model.RoleSystem || off[2].Content != "Aurora?" {
		t.Fatalf("default order changed: %+v", contents(off))
	}
	var on = run(t, map[string]string{"TELEGRAM_ALLOWED_USERS": "11", "PROMPT_CACHE_FRIENDLY": "true"}, "cache-on")
	if len(on) != 3 || on[0].Content != "persona" || on[1].Content != "Aurora?" || on[2].Role != model.RoleSystem {
		t.Fatalf("cache-friendly order = %+v, want persona, question, RAG", contents(on))
	}
	if !strings.Contains(ragMessageText(on), "Источник:") {
		t.Fatalf("the trailing message is not the RAG block: %+v", contents(on))
	}
}
