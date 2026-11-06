package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func savedLinkFixture(chatID int64, number int) SavedLink {
	return SavedLink{
		ChatID: chatID, UserID: 11, Title: fmt.Sprintf("Заметка %d", number),
		CanonicalURL: fmt.Sprintf("https://example.com/pages/%d", number), Content: fmt.Sprintf("Содержимое %d", number),
	}
}

func TestSavedLinkMigration8PreservesSchema7Data(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	migrations := MigrationSQL
	MigrationSQL = append([]string(nil), migrations[:7]...)
	defer func() { MigrationSQL = migrations }()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChat(ctx, db, 100, 11, "kept"); err != nil {
		t.Fatal(err)
	}
	MigrationSQL = migrations
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var title string
	if err := db.QueryRowContext(ctx, `SELECT title FROM chats WHERE chat_id = 100`).Scan(&title); err != nil || title != "kept" {
		t.Fatalf("chat title=%q err=%v", title, err)
	}
	var indexes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_saved_links_chat_updated'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("saved link index count=%d err=%v", indexes, err)
	}
}

func TestSavedLinkCRUDReplacementAndChatIsolation(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	firstTime := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	first := savedLinkFixture(100, 1)
	first.CreatedAt = firstTime
	created, replaced, err := PutSavedLink(ctx, db, first)
	if err != nil || replaced || created.ID <= 0 {
		t.Fatalf("created=%+v replaced=%v err=%v", created, replaced, err)
	}
	otherChat := first
	otherChat.ChatID = 101
	other, replaced, err := PutSavedLink(ctx, db, otherChat)
	if err != nil || replaced || other.ID == created.ID {
		t.Fatalf("same URL in another chat: other=%+v replaced=%v err=%v", other, replaced, err)
	}
	replacement := first
	replacement.Title = "Обновлённый заголовок"
	replacement.Content = "Новый текст"
	replacement.UserID = 12
	replacement.CreatedAt = firstTime.Add(time.Hour)
	updated, replaced, err := PutSavedLink(ctx, db, replacement)
	if err != nil || !replaced || updated.ID != created.ID || updated.Title != replacement.Title || updated.Content != replacement.Content || updated.UserID != 12 {
		t.Fatalf("updated=%+v replaced=%v err=%v", updated, replaced, err)
	}
	if !updated.CreatedAt.Equal(firstTime) || !updated.UpdatedAt.Equal(firstTime.Add(time.Hour)) {
		t.Fatalf("replacement timestamps=%s/%s", updated.CreatedAt, updated.UpdatedAt)
	}
	rows, err := ListSavedLinks(ctx, db, 100, 20)
	if err != nil || len(rows) != 1 || rows[0].ID != created.ID {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if _, found, err := GetSavedLink(ctx, db, 101, created.ID); err != nil || found {
		t.Fatalf("cross-chat get found=%v err=%v", found, err)
	}
	if deleted, err := DeleteSavedLink(ctx, db, 101, created.ID); err != nil || deleted {
		t.Fatalf("cross-chat delete=%v err=%v", deleted, err)
	}
	if deleted, err := DeleteSavedLink(ctx, db, 100, created.ID); err != nil || !deleted {
		t.Fatalf("owner delete=%v err=%v", deleted, err)
	}
	if deleted, err := DeleteSavedLink(ctx, db, 100, created.ID); err != nil || deleted {
		t.Fatalf("stale delete=%v err=%v", deleted, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO saved_links (chat_id, user_id, title, canonical_url, content, created_at, updated_at)
		VALUES (999, 11, 'orphan', 'https://example.com/orphan', 'text', '2026-01-01T00:00:00.000000000Z', '2026-01-01T00:00:00.000000000Z')`); err == nil {
		t.Fatal("foreign key allowed an orphan saved link")
	}
}

func TestClearSavedLinksDeletesOnlyCurrentChat(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	for index := 0; index < 3; index++ {
		if _, _, err := PutSavedLink(ctx, db, savedLinkFixture(100, index)); err != nil {
			t.Fatal(err)
		}
	}
	other, _, err := PutSavedLink(ctx, db, savedLinkFixture(101, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, chatID := range []int64{0, 999} {
		deleted, err := ClearSavedLinks(ctx, db, chatID)
		if err != nil || deleted != 0 {
			t.Fatalf("clear chat %d deleted=%d err=%v", chatID, deleted, err)
		}
	}
	deleted, err := ClearSavedLinks(ctx, db, 100)
	if err != nil || deleted != 3 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if rows, err := ListSavedLinks(ctx, db, 100, 20); err != nil || len(rows) != 0 {
		t.Fatalf("current chat rows=%+v err=%v", rows, err)
	}
	if deleted, err := ClearSavedLinks(ctx, db, 100); err != nil || deleted != 0 {
		t.Fatalf("repeat clear deleted=%d err=%v", deleted, err)
	}
	rows, err := ListSavedLinks(ctx, db, 101, 20)
	if err != nil || len(rows) != 1 || rows[0].ID != other.ID {
		t.Fatalf("other chat rows=%+v err=%v", rows, err)
	}
}

func TestSavedLinkSearchIsBoundedUnicodeCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	fixtures := []SavedLink{
		{ChatID: 100, UserID: 11, Title: "Проект АВРОРА", CanonicalURL: "https://example.com/one", Content: "первый"},
		{ChatID: 100, UserID: 11, Title: "Второй", CanonicalURL: "https://example.com/ReadMe", Content: "второй"},
		{ChatID: 100, UserID: 11, Title: "Третий", CanonicalURL: "https://example.com/three", Content: "Съешь ЁЖИК и выпей чаю"},
		{ChatID: 101, UserID: 11, Title: "АВРОРА другого чата", CanonicalURL: "https://example.com/other", Content: "АВРОРА"},
	}
	for _, fixture := range fixtures {
		if _, _, err := PutSavedLink(ctx, db, fixture); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		query  string
		wantID int64
	}{
		{"аврора", 1},
		{"readme", 2},
		{"ёжик", 3},
	} {
		rows, err := SearchSavedLinks(ctx, db, 100, test.query, 20)
		if err != nil || len(rows) != 1 || rows[0].ID != test.wantID {
			t.Fatalf("query %q rows=%+v err=%v", test.query, rows, err)
		}
	}
	if rows, err := SearchSavedLinks(ctx, db, 100, "отсутствует", 20); err != nil || len(rows) != 0 {
		t.Fatalf("no-match rows=%+v err=%v", rows, err)
	}
	for _, query := range []string{"", strings.Repeat("я", SavedLinkSearchMaxRunes+1)} {
		if _, err := SearchSavedLinks(ctx, db, 100, query, 20); !errors.Is(err, ErrSavedLinkQuery) {
			t.Fatalf("invalid query err=%v", err)
		}
	}
}

func TestSavedLinkCapAllowsReplacementAndBoundsList(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	for index := 0; index < SavedLinkMaxPerChat; index++ {
		if _, _, err := PutSavedLink(ctx, db, savedLinkFixture(100, index)); err != nil {
			t.Fatalf("put %d: %v", index, err)
		}
	}
	if _, _, err := PutSavedLink(ctx, db, savedLinkFixture(100, SavedLinkMaxPerChat)); !errors.Is(err, ErrSavedLinkLimit) {
		t.Fatalf("51st link err=%v", err)
	}
	replacement := savedLinkFixture(100, 10)
	replacement.Title = "Обновлено на пределе"
	if _, replaced, err := PutSavedLink(ctx, db, replacement); err != nil || !replaced {
		t.Fatalf("replacement at cap replaced=%v err=%v", replaced, err)
	}
	if _, _, err := PutSavedLink(ctx, db, savedLinkFixture(101, 1)); err != nil {
		t.Fatalf("other chat cap leaked: %v", err)
	}
	rows, err := ListSavedLinks(ctx, db, 100, 1000)
	if err != nil || len(rows) != SavedLinkListLimit {
		t.Fatalf("list length=%d err=%v", len(rows), err)
	}
}

func TestSavedLinkValidationRejectsUnboundedInput(t *testing.T) {
	ctx := context.Background()
	db := reminderTestDB(t)
	valid := savedLinkFixture(100, 1)
	for name, mutate := range map[string]func(*SavedLink){
		"title":   func(link *SavedLink) { link.Title = strings.Repeat("я", SavedLinkMaxTitleRunes+1) },
		"content": func(link *SavedLink) { link.Content = strings.Repeat("я", SavedLinkMaxContentRunes+1) },
		"http":    func(link *SavedLink) { link.CanonicalURL = "http://example.com/" },
		"empty":   func(link *SavedLink) { link.Content = " " },
	} {
		t.Run(name, func(t *testing.T) {
			link := valid
			mutate(&link)
			if _, _, err := PutSavedLink(ctx, db, link); !errors.Is(err, ErrSavedLinkInput) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
