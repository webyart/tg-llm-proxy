package store

import (
	"context"
	"database/sql"
	"testing"
)

// TestMigration026AddsFirstTokenToAnExistingSchema25 upgrades a database that already carries messages:
// the nullable column arrives NULL for every old row, the old metadata stays intact, and an answer stored
// after the upgrade keeps its measurement.
func TestMigration026AddsFirstTokenToAnExistingSchema25(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 25)

	if err := EnsureChat(ctx, db, 70, 7, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages (chat_id, user_id, role, content, model, latency_ms)
		VALUES (70, 7, 'assistant', 'ответ до миграции', 'old-model', 1200)`); err != nil {
		t.Fatalf("seed a schema-25 answer: %v", err)
	}

	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("upgrade schema 25 to 26: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}

	var old struct {
		model    string
		latency  int64
		firstTok sql.NullInt64
	}
	if err := db.QueryRowContext(ctx, `SELECT model, latency_ms, first_token_ms FROM messages WHERE content = 'ответ до миграции'`).
		Scan(&old.model, &old.latency, &old.firstTok); err != nil {
		t.Fatalf("read the migrated row: %v", err)
	}
	if old.model != "old-model" || old.latency != 1200 {
		t.Fatalf("the migration changed the old answer: %+v", old)
	}
	if old.firstTok.Valid {
		t.Fatalf("the old answer got first_token_ms=%d, want NULL (nothing was measured)", old.firstTok.Int64)
	}

	id, err := Append(ctx, db, Message{
		ChatID: 70, UserID: 7, Role: RoleAssistant, Content: "ответ после миграции",
		Model: "new-model", LatencyMs: 4000, FirstTokenMs: intPtr(800),
	})
	if err != nil {
		t.Fatalf("Append after the upgrade: %v", err)
	}
	var stored sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT first_token_ms FROM messages WHERE id = ?`, id).Scan(&stored); err != nil {
		t.Fatalf("read the new measurement: %v", err)
	}
	if !stored.Valid || stored.Int64 != 800 {
		t.Fatalf("first_token_ms=%v, want 800", stored)
	}
}

// TestFirstTokenLatencyRoundTripAndNull covers the whole read surface that enumerates messages columns:
// an answer with a measured TTFT reads it back through the context, the export and the search paths, while
// an unmeasured answer (nil or a caller's zero) reads back NULL rather than a misleading 0.
func TestFirstTokenLatencyRoundTripAndNull(t *testing.T) {
	ctx := context.Background()
	db := memDB(t)

	measuredID, err := Append(ctx, db, Message{
		ChatID: 11, UserID: 7, Role: RoleAssistant, Content: "измеренный ответ",
		Model: "qwen-test", LatencyMs: 4000, FirstTokenMs: intPtr(800),
	})
	if err != nil {
		t.Fatalf("Append measured: %v", err)
	}
	unmeasuredID, err := Append(ctx, db, Message{
		ChatID: 11, UserID: 7, Role: RoleAssistant, Content: "ответ без измерения",
		Model: "qwen-test", LatencyMs: 1200,
	})
	if err != nil {
		t.Fatalf("Append unmeasured: %v", err)
	}
	zeroID, err := Append(ctx, db, Message{
		ChatID: 11, UserID: 7, Role: RoleAssistant, Content: "нулевой ttft",
		Model: "qwen-test", LatencyMs: 900, FirstTokenMs: intPtr(0),
	})
	if err != nil {
		t.Fatalf("Append zero: %v", err)
	}

	// The column itself stores NULL, not 0: "not measured" and "answered instantly" must stay apart.
	var zeroStored sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT first_token_ms FROM messages WHERE id = ?`, zeroID).Scan(&zeroStored); err != nil {
		t.Fatalf("read the zero row: %v", err)
	}
	if zeroStored.Valid {
		t.Fatalf("Append wrote first_token_ms=%d for a zero measurement, want NULL", zeroStored.Int64)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages (chat_id, user_id, role, content, first_token_ms)
		VALUES (11, 7, 'assistant', 'прямая вставка нуля', 0)`); err == nil {
		t.Fatal("the schema accepted first_token_ms = 0, want the CHECK of migration 026 to reject it")
	}

	byID := func(rows []Message) map[int64]Message {
		t.Helper()
		out := make(map[int64]Message, len(rows))
		for _, row := range rows {
			out[row.ID] = row
		}
		return out
	}

	history, err := HistoryOfChat(ctx, db, 11, 10)
	if err != nil {
		t.Fatalf("HistoryOfChat: %v", err)
	}
	rows := byID(history)
	if got := rows[measuredID].FirstTokenMs; got == nil || *got != 800 {
		t.Errorf("history first_token_ms=%v, want 800", got)
	}
	if got := rows[unmeasuredID].FirstTokenMs; got != nil {
		t.Errorf("history first_token_ms=%v for an unmeasured answer, want nil", *got)
	}
	if got := rows[zeroID].FirstTokenMs; got != nil {
		t.Errorf("history first_token_ms=%v for a zero measurement, want nil", *got)
	}

	recent, err := RecentByChat(ctx, db, 11, 10)
	if err != nil {
		t.Fatalf("RecentByChat: %v", err)
	}
	if got := byID(recent)[measuredID].FirstTokenMs; got == nil || *got != 800 {
		t.Errorf("context first_token_ms=%v, want 800", got)
	}

	exported, err := ExportMessages(ctx, db, 11, 10)
	if err != nil {
		t.Fatalf("ExportMessages: %v", err)
	}
	if got := byID(exported)[measuredID].FirstTokenMs; got == nil || *got != 800 {
		t.Errorf("export first_token_ms=%v, want 800", got)
	}

	var streamed []Message
	if _, _, err := StreamExportMessages(ctx, db, 11, 10, func(MessageExportWindow, bool) error { return nil },
		func(m Message) error { streamed = append(streamed, m); return nil }); err != nil {
		t.Fatalf("StreamExportMessages: %v", err)
	}
	if got := byID(streamed)[measuredID].FirstTokenMs; got == nil || *got != 800 {
		t.Errorf("streamed export first_token_ms=%v, want 800", got)
	}

	found, err := SearchMessages(ctx, db, 11, "измеренный", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("SearchMessages returned %d rows, want 1", len(found))
	}
	if got := found[0].FirstTokenMs; got == nil || *got != 800 {
		t.Errorf("search first_token_ms=%v, want 800", got)
	}
}

// TestMigration026ToleratesAnExistingFirstTokenColumn reproduces a database where the column was added by
// an operator (or by an interrupted release) while version 26 is still missing: the guarded runner must skip
// only the ALTER, keep the data and still record the version instead of failing with "duplicate column".
func TestMigration026ToleratesAnExistingFirstTokenColumn(t *testing.T) {
	ctx := context.Background()
	db, restore := openMemoryAtVersion(t, 25)

	if err := EnsureChat(ctx, db, 70, 7, ""); err != nil {
		t.Fatalf("EnsureChat: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN first_token_ms INTEGER`); err != nil {
		t.Fatalf("add the column manually: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages (chat_id, user_id, role, content, first_token_ms)
		VALUES (70, 7, 'assistant', 'ручная колонка', 500)`); err != nil {
		t.Fatalf("seed the manual column: %v", err)
	}

	restore()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema over an existing first_token_ms: %v", err)
	}
	version, err := SchemaVersion(ctx, db)
	if err != nil || version != len(MigrationSQL) {
		t.Fatalf("SchemaVersion=%d err=%v, want %d", version, err, len(MigrationSQL))
	}
	var stored sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT first_token_ms FROM messages WHERE content = 'ручная колонка'`).Scan(&stored); err != nil {
		t.Fatalf("read the preserved measurement: %v", err)
	}
	if !stored.Valid || stored.Int64 != 500 {
		t.Fatalf("first_token_ms=%v after the guarded migration, want 500", stored)
	}
}

// TestFirstTokenHelperKeepsUnknownAsNull pins the conversion used by the assistant-store paths.
func TestFirstTokenHelperKeepsUnknownAsNull(t *testing.T) {
	if got := firstTokenOrNull(nil); got != nil {
		t.Errorf("nil became %v, want NULL", got)
	}
	if got := firstTokenOrNull(intPtr(0)); got != nil {
		t.Errorf("0 became %v, want NULL", got)
	}
	if got := firstTokenOrNull(intPtr(-5)); got != nil {
		t.Errorf("a negative value became %v, want NULL", got)
	}
	if got := firstTokenOrNull(intPtr(800)); got != int64(800) {
		t.Errorf("800 became %v, want 800", got)
	}
}
