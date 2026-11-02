// writes.
//
// under cmd/, and this one really does live there — `cmd/dbinspect` — because a second `package main` cannot
// sit next to the root one. It is built on demand by scripts/db_inspect.ps1 (`go build -o bin\dbinspect.exe
// business inside the container of a bot.
//
// What it guarantees, and what it refuses:
//
//	                   `-v …:/data:ro` after a clean stop). Either way no statement of this program can change
//	                   to the file) — but the mode is PRINTED, because a journal_mode read through an immutable
//	No migration, no PRAGMA write, no _txlock: a viewer does not take the write lock of a live bot.
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
func die(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dbinspect: "+format+"\n", args...)
	os.Exit(code)
}

func main() {
	var path = flag.String("path", "", "the database file to read  — required")
	var limit = flag.Int("limit", 20, "how many rows of each list to print ")
	var chat = flag.Int64("chat", 0, "restrict the chats and messages sections to one chat_id ; 0 means every chat")
	var integrity = flag.Bool("integrity", false, "run PRAGMA integrity_check of and exit 2 when the file is not sound")
	var counts = flag.Bool("counts", false, "print the machine-readable block of key=value numbers instead of the human report ; integrity_check is always part of this block")
	var timeout = flag.Duration("timeout", 15*time.Second, "how long the whole read may take ")
	flag.Parse()

	if strings.TrimSpace(*path) == "" {
		die(1, "-path is required ")
	}
	if *limit <= 0 {
		die(1, "-limit is %d, want a positive number ", *limit)
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

// section is printed even when it is empty: «0 rows» is information, a missing section is not.
func run(ctx context.Context, path string, chatID int64, limit int, integrity bool, counts bool) error {
	db, mode, openErr := store.OpenReadOnlySnapshot(ctx, path)
	if openErr != nil {
		return &exitError{1, openErr.Error()}
	}
	defer db.Close()

	info, statErr := os.Stat(path)
	if statErr != nil {
		return &exitError{1, fmt.Sprintf(`the file "%s" disappeared while opening it: %v`, path, statErr)}
	}

	// about this connection. foreign_keys/busy_timeout are deliberately NOT printed: they describe the
	// reader's own connection and would read as «the bot has foreign_keys off», which is a lie.
	var journal, journalErr = store.Pragma(ctx, db, "journal_mode")
	if counts {
		if journalErr != nil {
			return &exitError{1, "journal_mode: " + journalErr.Error()}
		}
		return countsReport(os.Stdout, ctx, db, path, journal, mode)
	}
	if journalErr != nil {
		journal = "не прочитан: " + journalErr.Error()
	}

	fmt.Printf("Database \n")
	fmt.Printf("  path          %s\n", path)
	fmt.Printf("  size          %s, modified %s\n", humanBytes(info.Size()), info.ModTime().Format(time.RFC3339))
	fmt.Printf("  open mode     %s\n", modeNote(mode))
	fmt.Printf("  journal_mode  %s %s\n", journal, journalCaveat(mode))
	var schemaVersion, schemaErr = store.SchemaVersion(ctx, db)
	var schemaText = "0 (pre-versioning backup)"
	if schemaErr != nil {
		schemaText = "не прочитан: " + schemaErr.Error()
	} else if schemaVersion > 0 {
		schemaText = fmt.Sprint(schemaVersion)
	}
	fmt.Printf("  schema_version %s\n", schemaText)
	fmt.Println()

	return sections(ctx, db, chatID, limit, integrity)
}

// countsReport — the machine-readable twin of the human report: one `key=value` line per number. A PowerShell
// the same file instead of parsing a table it did not design; the numbers come from the SAME store functions
// the human sections call, because a check that re-implements a query drifts from the query the operator
func countsReport(w io.Writer, ctx context.Context, db *sql.DB, path, journal string, mode store.ReadOnlyMode) error {
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
	var schemaVersion, schemaErr = store.SchemaVersion(ctx, db)
	if schemaErr != nil {
		return &exitError{1, schemaErr.Error()}
	}

	fmt.Fprintf(w, "path=%s\n", path)
	// taken through the immutable open reports journal_mode of the handle ("delete" even for a WAL file,
	// measured in the container on 2026-10-31), and that is the one difference a script must not average away
	fmt.Fprintf(w, "open_mode=%s\n", countKey(string(mode)))
	fmt.Fprintf(w, "wal_side_file=%s\n", map[bool]string{true: "present", false: "absent"}[store.WALSideFilePresent(path)])
	fmt.Fprintf(w, "journal_mode=%s\n", countKey(journal))
	fmt.Fprintf(w, "integrity=%s\n", countKey(verdict))
	fmt.Fprintf(w, "schema_version=%d\n", schemaVersion)
	fmt.Fprintf(w, "last_update_id=%d\n", offset)
	// found=false means the bot_state row does not exist yet — a fresh database, where the poller starts from
	fmt.Fprintf(w, "bot_state_offset_found=%t\n", found)
	for _, table := range tables {
		fmt.Fprintf(w, "table_%s=%d\n", countKey(table.Name), table.Rows)
	}
	for _, kind := range kinds {
		fmt.Fprintf(w, "events_%s=%d\n", countKey(kind.Kind), kind.Count)
	}
	if verdict != "ok" {
		// The counts were read, so a broken file is a bad ANSWER and not an unreadable file: exit 2 is the
		return &exitError{2, fmt.Sprintf("integrity_check answered %q  — make a backup before touching it", firstLine(verdict))}
	}
	return nil
}

// countKey — a key of the machine block, and the value of a word-like field (journal_mode, integrity) put
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

// modeNote — the open mode in the human report. The plain open is the normal case and needs one line; the
// immutable open needs the reason and the limit of what it can see, because a reader who is not told may take
// number, not in a manual nobody reads).
func modeNote(mode store.ReadOnlyMode) string {
	if mode != store.ReadOnlyModeImmutable {
		return "mode=ro "
	}
	var side = store.WALSideFile("bot.db")
	return "mode=ro&immutable=1 (self-contained snapshot after a clean stop; no " + side + " file present)"
}

// journalCaveat — the second half of the journal_mode line, empty for the plain open.
func journalCaveat(mode store.ReadOnlyMode) string {
	if mode != store.ReadOnlyModeImmutable {
		return ""
	}
	return " — measured through the immutable handle: it answers \"delete\" for a WAL file too , so this line says nothing about the journal mode of the file"
}

// sections — the five reports over the open handle, in the order an operator asks questions: is the poller
// alive (bot_state), is anything being refused (events by kind), which chats exist, what was said, and is the
func sections(ctx context.Context, db *sql.DB, chatID int64, limit int, integrity bool) error {
	state, stateErr := store.StateEntries(ctx, db)
	if stateErr != nil {
		return &exitError{1, stateErr.Error()}
	}
	fmt.Println("bot_state ")
	if len(state) == 0 {
		fmt.Println("  ")
	}
	for _, row := range state {
		fmt.Printf("  %-16s %s\n", row.Key, row.Value)
	}
	fmt.Println()

	kinds, kindErr := store.EventCounts(ctx, db)
	if kindErr != nil {
		return &exitError{1, kindErr.Error()}
	}
	fmt.Println("events by kind ")
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
		fmt.Printf("chats (chat_id=%d, up to %d)\n", chatID, limit)
	} else {
		fmt.Printf("chats (up to %d)\n", limit)
	}
	if len(chats) == 0 {
		if chatID > 0 {
			fmt.Printf("  (empty — chat_id=%d is not registered)\n", chatID)
		} else {
			fmt.Println("  (empty — no chat registered)")
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
		fmt.Printf("messages (chat_id=%d, up to %d)\n", chatID, limit)
	} else {
		fmt.Printf("messages (every chat, up to %d)\n", limit)
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
	fmt.Printf("events (up to %d)\n", limit)
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
	fmt.Println("integrity_check ")
	for _, line := range strings.Split(verdict, "\n") {
		fmt.Printf("  %s\n", line)
	}
	if verdict != "ok" {
		return &exitError{2, fmt.Sprintf("integrity_check answered %q  — make a backup of before touching it", firstLine(verdict))}
	}
	return nil
}

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
	fmt.Printf("  /reset marks : %s\n",
		strings.Join(marks, ", "))
}

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
		fmt.Printf("  models in these rows: %s \n", strings.Join(names, ", "))
	}
}

func tokenPair(row store.RecentMessage) string {
	if row.TokensIn < 0 && row.TokensOut < 0 {
		return "—"
	}
	return fmt.Sprintf("%d→%d", row.TokensIn, row.TokensOut)
}

// dash — the readable NULL of the reader: -1 is what store.RecentMessage/EventRow carry for a NULL column of
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
	_ = t.w.Flush()
}

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
func firstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return text[:idx]
	}
	return text
}
