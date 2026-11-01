// Command dbfixture creates a small file database for scripts/test-maintenance.ps1. It never opens an existing
// file, so acceptance cannot accidentally migrate or modify a real bot database.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"bot/internal/store"
)

func main() {
	var path = flag.String("path", "", "new fixture database path (required)")
	var preVersion = flag.Bool("pre-version", false, "drop schema_migrations to simulate a pre-versioning backup")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "dbfixture: -path is required")
		os.Exit(1)
	}
	if _, err := os.Stat(*path); err == nil {
		fmt.Fprintf(os.Stderr, "dbfixture: refusing to open existing file %q\n", *path)
		os.Exit(1)
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "dbfixture: cannot inspect %q: %v\n", *path, err)
		os.Exit(1)
	}

	ctx := context.Background()
	db, err := store.Open(ctx, *path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbfixture: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	for _, message := range []store.Message{
		{ChatID: 42, UserID: 7, Role: store.RoleUser, Content: "fixture question", TelegramMessageID: func() *int64 { id := int64(700); return &id }()},
		{ChatID: 42, UserID: 7, Role: store.RoleAssistant, Content: "fixture answer", Model: "fixture-model"},
	} {
		if _, err := store.Append(ctx, db, message); err != nil {
			fmt.Fprintf(os.Stderr, "dbfixture: Append: %v\n", err)
			os.Exit(1)
		}
	}
	oldTelegramID := int64(701)
	for _, message := range []store.Message{
		{ChatID: 42, UserID: 7, Role: store.RoleUser, Content: "old fixture question", TelegramMessageID: &oldTelegramID},
		{ChatID: 42, UserID: 7, Role: store.RoleAssistant, Content: "old fixture answer", Model: "fixture-model"},
	} {
		if _, err := store.Append(ctx, db, message); err != nil {
			fmt.Fprintf(os.Stderr, "dbfixture: Append old fixture: %v\n", err)
			os.Exit(1)
		}
	}
	if err := store.SetLastUpdateID(ctx, db, 4242); err != nil {
		fmt.Fprintf(os.Stderr, "dbfixture: SetLastUpdateID: %v\n", err)
		os.Exit(1)
	}
	if err := store.LogEvent(ctx, db, 7, store.EventCommand, "fixture"); err != nil {
		fmt.Fprintf(os.Stderr, "dbfixture: LogEvent: %v\n", err)
		os.Exit(1)
	}
	if err := store.LogEvent(ctx, db, 7, store.EventCommand, "old fixture event"); err != nil {
		fmt.Fprintf(os.Stderr, "dbfixture: LogEvent old: %v\n", err)
		os.Exit(1)
	}
	if _, err := db.ExecContext(ctx, `UPDATE messages SET created_at = '2000-01-01 00:00:00' WHERE content LIKE 'old fixture %'`); err != nil {
		fmt.Fprintf(os.Stderr, "dbfixture: age messages: %v\n", err)
		os.Exit(1)
	}
	if _, err := db.ExecContext(ctx, `UPDATE events SET created_at = '2000-01-01 00:00:00' WHERE detail = 'old fixture event'`); err != nil {
		fmt.Fprintf(os.Stderr, "dbfixture: age event: %v\n", err)
		os.Exit(1)
	}
	if *preVersion {
		if _, err := db.ExecContext(ctx, `DROP TABLE schema_migrations`); err != nil {
			fmt.Fprintf(os.Stderr, "dbfixture: make pre-versioning schema: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("fixture: %s (4 messages, 2 events, offset 4242; one old pair and one old event; pre-version=%t)\n", *path, *preVersion)
}
