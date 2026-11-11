package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestChatTimezoneCRUDAndFallback(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if err := EnsureChat(ctx, db, 70, 7, "private"); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}

	if timezone, found, err := GetChatTimezone(ctx, db, 70); err != nil || found || timezone != "" {
		t.Fatalf("empty GetChatTimezone = %q found=%v err=%v", timezone, found, err)
	}
	if err := SetChatTimezone(ctx, db, 70, "Asia/Yekaterinburg"); err != nil {
		t.Fatalf("SetChatTimezone: %v", err)
	}
	timezone, found, err := GetChatTimezone(ctx, db, 70)
	if err != nil || !found || timezone != "Asia/Yekaterinburg" {
		t.Fatalf("GetChatTimezone = %q found=%v err=%v", timezone, found, err)
	}
	var updatedAt string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(updated_at, '') FROM chat_timezone WHERE chat_id = 70`).Scan(&updatedAt); err != nil || updatedAt == "" {
		t.Fatalf("updated_at = %q err=%v, want a SQLite datetime", updatedAt, err)
	}

	if err := SetChatTimezone(ctx, db, 70, "Europe/Berlin"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if timezone, _, _ = GetChatTimezone(ctx, db, 70); timezone != "Europe/Berlin" {
		t.Fatalf("overwritten timezone = %q", timezone)
	}

	if err := DeleteChatTimezone(ctx, db, 70); err != nil {
		t.Fatalf("DeleteChatTimezone: %v", err)
	}
	if timezone, found, err = GetChatTimezone(ctx, db, 70); err != nil || found || timezone != "" {
		t.Fatalf("after delete = %q found=%v err=%v", timezone, found, err)
	}
	// Reset is idempotent: a second delete does not fail.
	if err := DeleteChatTimezone(ctx, db, 70); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestChatTimezoneValidation(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx, t.Name())
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer db.Close()
	if err := EnsureChat(ctx, db, 70, 7, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	for _, invalid := range []string{"", "   ", "Mars/Olympus_Mons", "Local", "../etc/passwd", "/etc/passwd", strings.Repeat("a", 65)} {
		if err := SetChatTimezone(ctx, db, 70, invalid); err == nil {
			t.Errorf("SetChatTimezone accepted %q", invalid)
		}
	}
	if timezone, found, _ := GetChatTimezone(ctx, db, 70); found || timezone != "" {
		t.Fatalf("an invalid write stored %q", timezone)
	}
	if _, _, err := NormalizeTimezone("UTC"); err != nil {
		t.Fatalf("UTC must be valid: %v", err)
	}
}

// TestMigration024AddsChatTimezoneToAnExistingSchema23 upgrades a database with live chats and messages: the
// new table must arrive empty, the old rows must be untouched and a subsequent Set must work.
func TestMigration024AddsChatTimezoneToAnExistingSchema23(t *testing.T) {
	ctx := context.Background()
	all := MigrationSQL
	MigrationSQL = append([]string(nil), all[:23]...)
	db, err := OpenMemory(ctx, "migration-024-"+t.Name())
	if err != nil {
		MigrationSQL = all
		t.Fatalf("OpenMemory at 23: %v", err)
	}
	defer db.Close()
	MigrationSQL = all
	t.Cleanup(func() { MigrationSQL = all })

	version, err := SchemaVersion(ctx, db)
	if err != nil || version != 23 {
		t.Fatalf("SchemaVersion=%d err=%v, want 23", version, err)
	}
	if err := EnsureChat(ctx, db, 70, 7, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages (chat_id, user_id, role, content, created_at)
		VALUES (70, 7, 'user', 'kept', '2026-11-11 10:00:00')`); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("upgrade to 24: %v", err)
	}
	version, err = SchemaVersion(ctx, db)
	if err != nil || version != len(all) {
		t.Fatalf("SchemaVersion=%d err=%v, want %d", version, err, len(all))
	}
	var kept int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE chat_id = 70 AND content = 'kept'`).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("old message count=%d err=%v", kept, err)
	}
	if timezone, found, err := GetChatTimezone(ctx, db, 70); err != nil || found || timezone != "" {
		t.Fatalf("fresh 024 row: timezone=%q found=%v err=%v", timezone, found, err)
	}
	if err := SetChatTimezone(ctx, db, 70, "Asia/Yekaterinburg"); err != nil {
		t.Fatalf("Set after upgrade: %v", err)
	}
	location, err := time.LoadLocation("Asia/Yekaterinburg")
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := time.Date(2026, 11, 11, 12, 0, 0, 0, location).Zone(); offset != 5*60*60 {
		t.Fatalf("Yekaterinburg offset = %d", offset)
	}
}
