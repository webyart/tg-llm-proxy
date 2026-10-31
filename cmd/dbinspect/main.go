// Command dbinspect — the reader behind `scripts/db_inspect.ps1` (§9). sqlite3 CLI is not installed on this
// host (§2 «sqlite3 CLI | absent | expected (§5) → use scripts/db_inspect.ps1 (Go-tool)»), so the only way
// to look at the chat history of §5 without a Telegram account is a Go tool over the same file the bot
// writes.
//
// §4 deviation (the same one README «§4 deviation» records for the bot): the plan draws the entry points
// under cmd/, and this one really does live there — `cmd/dbinspect` — because a second `package main` cannot
// sit next to the root one. It is built on demand by scripts/db_inspect.ps1 (`go build -o bin\dbinspect.exe
// ./cmd/dbinspect`) and is deliberately NOT part of the runtime image of §8.1: a maintenance reader has no
// business inside the container of a bot.
//
// What it guarantees, and what it refuses:
//
//	store.OpenReadOnly — mode=ro, so no statement of this program can change the database of a running bot
//	                   (§5 case D), and journal_mode is left alone (§5: it belongs to the file).
//	No migration, no PRAGMA write, no _txlock: a viewer does not take the write lock of a live bot.
//	Nothing here prints a value of §7 — the database holds chat texts (§12 security forbids tokens and full
//	model texts in the LOGS, and this output is a human reading a file, not a log).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"bot/internal/store"
)

// exitError — a message plus the exit code the script should leave with. Three codes, because the script has
// to tell the three failures apart (§9: a missing DB_PATH is not a corrupted database):
//
//	1 — the database could not be opened or read (nothing was printed about its content),
//	2 — the database opened and answered, and the answer is bad (integrity_check ≠ ok),
//	0 — everything fine.
type exitError struct {
	code int
	text string
}

func (e *exitError) Error() string { return e.text }

// die — the one exit path: the message goes to stderr so that a piped human still sees the report on stdout,
// and §12 reliability keeps the reason specific enough to act on.
func die(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dbinspect: "+format+"\n", args...)
	os.Exit(code)
}

func main() {
	var path = flag.String("path", "", "the database file to read (§7 DB_PATH; the script takes it from .env) — required")
	var limit = flag.Int("limit", 20, "how many rows of each list to print (§9 -limit)")
	var chat = flag.Int64("chat", 0, "restrict the chats and messages sections to one chat_id (§5 chats.chat_id); 0 means every chat")
	var integrity = flag.Bool("integrity", false, "run PRAGMA integrity_check of §15 and exit 2 when the file is not sound")
	var counts = flag.Bool("counts", false, "print the machine-readable block of key=value numbers instead of the human report (scripts/test-restart.ps1 of §9 compares two such blocks across a kill/restart); integrity_check is always part of this block")
	var timeout = flag.Duration("timeout", 15*time.Second, "how long the whole read may take (the reader itself waits 5 s per lock, §5 ReadOnlyBusyTimeout)")
	flag.Parse()

	if strings.TrimSpace(*path) == "" {
		die(1, "-path is required (§9: scripts/db_inspect.ps1 passes DB_PATH of §7; the local default is .\\data\\bot.db)")
	}
	if *limit <= 0 {
		die(1, "-limit is %d, want a positive number (§9 -limit)", *limit)
	}

	var ctx, cancel = context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if err := run(ctx, *path, *chat, *limit, *integrity, *counts); err != nil {
		var code = 1
		var text = err.Error()
		if coded, ok := err.(*exitError); ok {
			code, text = coded.code, coded.text
		}
		die(code, "%s", text)
	}
}

// run — the report. One read-only handle for every section (§5: a viewer holds one connection), and each
// section is printed even when it is empty: «0 rows» is information, a missing section is not.
func run(ctx context.Context, path string, chatID int64, limit int, integrity bool, counts bool) error {
	db, openErr := store.OpenReadOnly(ctx, path)
	if openErr != nil {
		return &exitError{1, openErr.Error()}
	}
	defer db.Close()

	info, statErr := os.Stat(path)
	if statErr != nil {
		return &exitError{1, fmt.Sprintf(`the file "%s" disappeared while opening it: %v`, path, statErr)}
	}

	// journal_mode is a property of the FILE (§5), so a read of it says something about the database, not
	// about this connection. foreign_keys/busy_timeout are deliberately NOT printed: they describe the
	// reader's own connection and would read as «the bot has foreign_keys off», which is a lie.
	var journal, journalErr = store.Pragma(ctx, db, "journal_mode")
	if counts {
		// The machine block is read by a script that asserts `journal_mode=wal` (§5: WAL is what makes the
		// hard kill of §10 День 4 survivable). A reader that cannot name it must say so — printing an empty
		// value would let the script compare two empty strings and call it a pass (§12 reliability).
		if journalErr != nil {
			return &exitError{1, "journal_mode: " + journalErr.Error()}
		}
		return countsReport(os.Stdout, ctx, db, path, journal)
	}
	if journalErr != nil {
		journal = "не прочитан: " + journalErr.Error()
	}

	fmt.Printf("Database (§5, read-only through mode=ro — §9 scripts/db_inspect.ps1)\n")
	fmt.Printf("  path          %s\n", path)
	fmt.Printf("  size          %s, modified %s\n", humanBytes(info.Size()), info.ModTime().Format(time.RFC3339))
	fmt.Printf("  journal_mode  %s (§5: belongs to the file; the viewer never changes it)\n", journal)
	fmt.Println()

	return sections(ctx, db, chatID, limit, integrity)
}

// countsReport — the machine-readable twin of the human report: one `key=value` line per number. A PowerShell
// check (scripts/test-restart.ps1, §10 День 4 «kill → рестарт → офсет не потерян») compares two snapshots of
// the same file instead of parsing a table it did not design; the numbers come from the SAME store functions
// the human sections call, because a check that re-implements a query drifts from the query the operator
// reads (§12 reliability). Unlike the human report, integrity_check always runs here: the whole point of
// comparing snapshots across a hard kill is to see that the file is still sound (§5 WAL, §12.1.4).
func countsReport(w io.Writer, ctx context.Context, db *sql.DB, path, journal string) error {
	var offset, found, offsetErr = store.LastUpdateID(ctx, db)
	if offsetErr != nil {
		return &exitError{1, offsetErr.Error()}
	}
	var tables, tableErr = store.TableCounts(ctx, db)
	if tableErr != nil {
		return &exitError{1, tableErr.Error()}
	}
	var kinds, kindErr = store.EventCounts(ctx, db)
	if kindErr != nil {
		return &exitError{1, kindErr.Error()}
	}
	var verdict, integrityErr = store.IntegrityCheck(ctx, db)
	if integrityErr != nil {
		return &exitError{1, integrityErr.Error()}
	}

	fmt.Fprintf(w, "path=%s\n", path)
	fmt.Fprintf(w, "journal_mode=%s\n", countKey(journal))
	fmt.Fprintf(w, "integrity=%s\n", countKey(verdict))
	fmt.Fprintf(w, "last_update_id=%d\n", offset)
	// found=false means the bot_state row does not exist yet — a fresh database, where the poller starts from
	// 0 (§5). The script has to tell «offset 0» from «no offset row at all», so both numbers are printed.
	fmt.Fprintf(w, "bot_state_offset_found=%t\n", found)
	for _, table := range tables {
		fmt.Fprintf(w, "table_%s=%d\n", countKey(table.Name), table.Rows)
	}
	for _, kind := range kinds {
		fmt.Fprintf(w, "events_%s=%d\n", countKey(kind.Kind), kind.Count)
	}
	if verdict != "ok" {
		// The counts were read, so a broken file is a bad ANSWER and not an unreadable file: exit 2 is the
		// code scripts/db_inspect.ps1 already reserves for it (§10 День 2 DoD: о битом файле нельзя молчать).
		return &exitError{2, fmt.Sprintf("integrity_check answered %q (§15) — make a backup before touching it", firstLine(verdict))}
	}
	return nil
}

// countKey — a key of the machine block, and the value of a word-like field (journal_mode, integrity) put
// through the same filter. §5 keeps `events.kind` free TEXT on purpose (messages.go: a new refusal reason
// must not need a migration), so a kind could carry a space or a dot, and the reader of this block greps
// `^[a-z0-9_]+=`. Everything outside that alphabet becomes "_"; no value is ever dropped.
func countKey(name string) string {
	var out strings.Builder
	for _, symbol := range name {
		switch {
		case symbol >= 'a' && symbol <= 'z', symbol >= 'A' && symbol <= 'Z', symbol >= '0' && symbol <= '9', symbol == '_':
			out.WriteRune(symbol)
		default:
			out.WriteRune('_')
		}
	}
	return out.String()
}

// sections — the five reports over the open handle, in the order an operator asks questions: is the poller
// alive (bot_state), is anything being refused (events by kind), which chats exist, what was said, and is the
// file physically sound (§15). Every section prints even when empty: «(empty)» is information, a missing
// section is not (§12 reliability).
func sections(ctx context.Context, db *sql.DB, chatID int64, limit int, integrity bool) error {
	state, stateErr := store.StateEntries(ctx, db)
	if stateErr != nil {
		return &exitError{1, stateErr.Error()}
	}
	fmt.Println("bot_state (§5 — the offset that must survive a restart)")
	if len(state) == 0 {
		fmt.Println("  (empty — no poll offset stored yet, §5 getUpdates starts from 0)")
	}
	for _, row := range state {
		fmt.Printf("  %-16s %s\n", row.Key, row.Value)
	}
	fmt.Println()

	kinds, kindErr := store.EventCounts(ctx, db)
	if kindErr != nil {
		return &exitError{1, kindErr.Error()}
	}
	fmt.Println("events by kind (§6 — denied = allowlist of §7, flood = Telegram 429, model_error = STRATA)")
	if len(kinds) == 0 {
		fmt.Println("  (empty — nothing refused, rate-limited or broken yet)")
	}
	for _, row := range kinds {
		fmt.Printf("  %-16s %d\n", row.Kind, row.Count)
	}
	fmt.Println()

	chats, chatErr := store.ListChats(ctx, db, chatID, limit)
	if chatErr != nil {
		return &exitError{1, chatErr.Error()}
	}
	if chatID > 0 {
		fmt.Printf("chats (§5, newest activity first, chat_id=%d, up to %d)\n", chatID, limit)
	} else {
		fmt.Printf("chats (§5, newest activity first, up to %d)\n", limit)
	}
	if len(chats) == 0 {
		if chatID > 0 {
			fmt.Printf("  (empty — chat_id=%d is not registered, §5: /start of §6 creates the row)\n", chatID)
		} else {
			fmt.Println("  (empty — no chat has ever said /start, §6)")
		}
	}
	var chatTable = newTable("chat_id", "user_id", "messages", "last_active_at (UTC)", "title")
	for _, row := range chats {
		chatTable.write(row.ChatID, row.UserID, row.Messages, row.LastActiveAt, brief(row.Title, 40))
	}
	chatTable.flush()
	resetMarks(chats)
	fmt.Println()

	messages, msgErr := store.RecentMessages(ctx, db, chatID, limit)
	if msgErr != nil {
		return &exitError{1, msgErr.Error()}
	}
	if chatID > 0 {
		fmt.Printf("messages (§5, newest first, chat_id=%d, up to %d)\n", chatID, limit)
	} else {
		fmt.Printf("messages (§5, newest first, every chat, up to %d)\n", limit)
	}
	if len(messages) == 0 {
		fmt.Println("  (empty — no message stored for this filter)")
	}
	var msgTable = newTable("id", "chat_id", "role", "tokens", "ms", "tg_id", "created_at (UTC)", "content")
	for _, row := range messages {
		msgTable.write(row.ID, row.ChatID, row.Role, tokenPair(row), row.LatencyMS, dash(row.TelegramMessageID),
			row.CreatedAt, brief(row.Content, 70))
	}
	msgTable.flush()
	modelOf(messages)
	fmt.Println()

	events, eventErr := store.RecentEvents(ctx, db, limit)
	if eventErr != nil {
		return &exitError{1, eventErr.Error()}
	}
	fmt.Printf("events (§5, newest first, up to %d)\n", limit)
	if len(events) == 0 {
		fmt.Println("  (empty)")
	}
	var eventTable = newTable("id", "user_id", "kind", "created_at (UTC)", "detail")
	for _, row := range events {
		// The detail of an event IS the answer to «why» — a dial error, the text of a 5xx, the budget spent.
		// The content of a message is clipped because it is long by nature; the reason of a failure is not,
		// and clipping it at 60 runes is what made the STRATA failure of 2026-10-31 unreadable
		// (`… chat/completions": dial tcp …` — the address and the reason were exactly what followed).
		eventTable.write(row.ID, dash(row.UserID), row.Kind, row.CreatedAt, brief(row.Detail, 240))
	}
	eventTable.flush()

	if !integrity {
		return nil
	}
	verdict, integrityErr := store.IntegrityCheck(ctx, db)
	if integrityErr != nil {
		return &exitError{1, integrityErr.Error()}
	}
	fmt.Println()
	fmt.Println("integrity_check (§15, on demand — the scheduled form is v2)")
	for _, line := range strings.Split(verdict, "\n") {
		fmt.Printf("  %s\n", line)
	}
	if verdict != "ok" {
		return &exitError{2, fmt.Sprintf("integrity_check answered %q (§10 День 2 DoD: о битом файле нельзя молчать) — make a backup of §15 before touching it", firstLine(verdict))}
	}
	return nil
}

// resetMarks — one line about the /reset marks of §5 when any printed chat has one. It is a note rather than a
// table column for the same reason as modelOf: the column would be «—» on almost every row of an ordinary
// database, while the rare case is exactly the one an operator needs to see («the history is in the file, the
// context just starts after context_reset_at»).
func resetMarks(chats []store.ChatRow) {
	var marks = []string{}
	for _, row := range chats {
		if row.ContextResetAt != "" {
			marks = append(marks, fmt.Sprintf("%d@%s", row.ChatID, row.ContextResetAt))
		}
	}
	if len(marks) == 0 {
		return
	}
	fmt.Printf("  /reset marks (§5 chats.context_reset_at, the rows before it are stored but out of the context): %s\n",
		strings.Join(marks, ", "))
}

// modelOf — the model names that appear in the printed rows (§7 STRATA_MODEL). A footer instead of a column:
// the same model repeats on every row and a 20-column-wide table would push the content off the screen.
func modelOf(messages []store.RecentMessage) {
	var seen = map[string]bool{}
	var names = []string{}
	for _, row := range messages {
		if row.Model != "" && !seen[row.Model] {
			seen[row.Model] = true
			names = append(names, row.Model)
		}
	}
	if len(names) > 0 {
		fmt.Printf("  models in these rows: %s (§7 STRATA_MODEL)\n", strings.Join(names, ", "))
	}
}

// tokenPair — «in→out» of §5, «—» where both columns are NULL: a user row carries no token counts, and
// printing 0→0 there would report a measurement that never happened (§12 reliability).
func tokenPair(row store.RecentMessage) string {
	if row.TokensIn < 0 && row.TokensOut < 0 {
		return "—"
	}
	return fmt.Sprintf("%d→%d", row.TokensIn, row.TokensOut)
}

// dash — the readable NULL of the reader: -1 is what store.RecentMessage/EventRow carry for a NULL column of
// §5, and «—» is what a human should see instead of a plausible-looking id of 0.
func dash(value int64) string {
	if value < 0 {
		return "—"
	}
	return strconv.FormatInt(value, 10)
}

// table — the tabwriter wrapper of one section. Aligned columns are the difference between a readable report
// and a wall of numbers; the two leading spaces keep the table off the section title's edge.
type table struct {
	w *tabwriter.Writer
}

func newTable(headers ...string) *table {
	var t = &table{w: tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)}
	var cells = make([]any, 0, len(headers))
	for _, header := range headers {
		cells = append(cells, header)
	}
	t.write(cells...)
	return t
}

// write — one row. Values go through fmt.Sprint so the columns accept int64 and string alike; a newline
// inside a chat text (§5 content is free text) would break the row, so it is flattened here — the reader
// shows a table, the full text is one /history or one SELECT away.
func (t *table) write(values ...any) {
	var cells = make([]string, 0, len(values))
	for _, v := range values {
		cells = append(cells, strings.ReplaceAll(fmt.Sprint(v), "\n", " "))
	}
	fmt.Fprintln(t.w, strings.Join(cells, "\t"))
}

func (t *table) flush() {
	// tabwriter buffers until Flush; an error here means stdout itself is gone, which is the shell's
	// problem (§9: the script pipes this into a file or a pager), so it is not worth a code path.
	_ = t.w.Flush()
}

// brief — a text cut to max RUNES with an ellipsis. §16 change 7 is the reason this counts runes: кириллица
// is two bytes in UTF-8, so a byte-wise cut lands in the middle of a letter and prints garbage.
func brief(text string, max int) string {
	var runes = []rune(strings.TrimSpace(text))
	if len(runes) <= max {
		if len(runes) == 0 {
			return "—"
		}
		return string(runes)
	}
	return string(runes[:max-1]) + "…"
}

// humanBytes — the file size the way §8.1 speaks about it (bot.db is a few dozen KiB in a fresh volume and
// grows with the chat history of §5).
func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	var value = float64(size)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s (%d bytes)", value, suffix, size)
		}
	}
	return fmt.Sprintf("%.1f PiB (%d bytes)", value/unit, size)
}

// firstLine — the first line of a multi-line SQLite verdict: the exit message has to stay one line for
// scripts (§9 scripts/*.ps1 read $LASTEXITCODE and the last line of output, not a paragraph).
func firstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return text[:idx]
	}
	return text
}
