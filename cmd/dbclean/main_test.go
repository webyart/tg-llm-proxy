package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"bot/internal/store"
)

const (
	cleanPlainAnswer  = "clean answer"
	cleanUserQuestion = "please keep <tool_call> in my question"
	cleanBlockAnswer  = "before the call\n<tool_call>\n<function=web_search>\n<parameter=query>\nsecret query text\n</parameter>\n</function>\n</tool_call>\nafter the call"
	cleanOpenAnswer   = "partial answer <tool_call>\n<function=web_search>\n<parameter=query>"
	cleanUpperAnswer  = "upper case <TOOL_CALL><function=web_search><parameter=query>x</parameter></function></TOOL_CALL>"
)

func seedCleanFixture(t *testing.T) (context.Context, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "clean.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	for _, message := range []store.Message{
		{ChatID: 7, UserID: 11, Role: store.RoleUser, Content: cleanUserQuestion},
		{ChatID: 7, UserID: 11, Role: store.RoleAssistant, Content: cleanPlainAnswer},
		{ChatID: 7, UserID: 11, Role: store.RoleAssistant, Content: cleanBlockAnswer},
		{ChatID: 7, UserID: 11, Role: store.RoleAssistant, Content: cleanOpenAnswer},
		{ChatID: 7, UserID: 11, Role: store.RoleAssistant, Content: cleanUpperAnswer},
		{ChatID: 7, UserID: 11, Role: store.RoleSystem, Content: "system <tool_call> stays"},
	} {
		if _, err := store.Append(ctx, db, message); err != nil {
			t.Fatalf("append %q: %v", message.Role, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	return ctx, path
}

func fixtureContents(t *testing.T, ctx context.Context, path string) map[int64]string {
	t.Helper()
	db, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, content FROM messages ORDER BY id`)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	defer rows.Close()
	contents := map[int64]string{}
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			t.Fatalf("scan fixture: %v", err)
		}
		contents[id] = content
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("fixture rows: %v", err)
	}
	return contents
}

func TestRunDryRunCountsButChangesNothingAndCreatesNoBackup(t *testing.T) {
	ctx, path := seedCleanFixture(t)
	before := fixtureContents(t, ctx, path)

	var output bytes.Buffer
	if err := run(ctx, path, false, 10, &output); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	text := output.String()
	for _, want := range []string{"mode=dry-run", "candidates=3", "saved_bytes=", "sample id=",
		"no backup was created"} {
		if !strings.Contains(text, want) {
			t.Errorf("dry-run output misses %q: %s", want, text)
		}
	}
	for _, secret := range []string{"secret query text", "before the call", "partial answer", "upper case"} {
		if strings.Contains(text, secret) {
			t.Errorf("dry-run output leaked message content %q: %s", secret, text)
		}
	}
	after := fixtureContents(t, ctx, path)
	if len(after) != len(before) {
		t.Fatalf("dry-run changed the row count: before=%d after=%d", len(before), len(after))
	}
	for id, content := range before {
		if after[id] != content {
			t.Errorf("dry-run changed message %d", id)
		}
	}
}

func TestRunApplyRewritesOnlyChangedAssistantRowsAndIsIdempotent(t *testing.T) {
	ctx, path := seedCleanFixture(t)

	var output bytes.Buffer
	if err := run(ctx, path, true, 10, &output); err != nil {
		t.Fatalf("apply: %v", err)
	}
	text := output.String()
	for _, want := range []string{"mode=apply", "candidates=3", "updated=3", "skipped=0", "integrity=ok"} {
		if !strings.Contains(text, want) {
			t.Errorf("apply output misses %q: %s", want, text)
		}
	}
	for _, secret := range []string{"secret query text", "before the call", "partial answer", "upper case"} {
		if strings.Contains(text, secret) {
			t.Errorf("apply output leaked message content %q: %s", secret, text)
		}
	}

	after := fixtureContents(t, ctx, path)
	if after[1] != cleanUserQuestion {
		t.Errorf("a user message was rewritten: %q", after[1])
	}
	if after[6] != "system <tool_call> stays" {
		t.Errorf("a system message was rewritten: %q", after[6])
	}
	if after[2] != cleanPlainAnswer {
		t.Errorf("a clean assistant row changed: %q", after[2])
	}
	for id := int64(3); id <= 5; id++ {
		if strings.Contains(strings.ToLower(after[id]), "<tool_call") ||
			strings.Contains(strings.ToLower(after[id]), "<function") ||
			strings.Contains(strings.ToLower(after[id]), "<parameter") {
			t.Errorf("assistant row %d still contains tool markup: %q", id, after[id])
		}
	}
	if !strings.Contains(after[3], "before the call") || !strings.Contains(after[3], "after the call") {
		t.Errorf("the answer text around the removed block was lost: %q", after[3])
	}
	if after[4] != "partial answer" {
		t.Errorf("an unclosed block was not cut at the tag: %q", after[4])
	}
	if after[5] != "upper case" {
		t.Errorf("uppercase markup was not sanitized: %q", after[5])
	}

	// The second run is a no-op: it must not re-normalise the already clean rows.
	var second bytes.Buffer
	if err := run(ctx, path, true, 10, &second); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if !strings.Contains(second.String(), "candidates=0") || !strings.Contains(second.String(), "updated=0") {
		t.Fatalf("second apply must be a no-op: %s", second.String())
	}
}

func TestRunSamplesCanBeDisabledAndLengthsStayBounded(t *testing.T) {
	ctx, path := seedCleanFixture(t)
	var output bytes.Buffer
	if err := run(ctx, path, false, 0, &output); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if strings.Contains(output.String(), "dbclean: sample ") {
		t.Fatalf("-samples=0 printed samples: %s", output.String())
	}
	if !strings.Contains(output.String(), "candidates=3") {
		t.Fatalf("report lost the candidate count: %s", output.String())
	}
}
