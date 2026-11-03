package store

import (
	"context"
	"database/sql"
	"fmt"
)

type AdminSummary struct {
	Chats       int64
	Messages    int64
	Events      int64
	Commands    int64
	ModelErrors int64
	Timeouts    int64
	Pending     int
	LastActive  string
}

func AdminStats(ctx context.Context, db *sql.DB) (AdminSummary, error) {
	var stats AdminSummary
	queries := []struct {
		target *int64
		sql    string
	}{
		{&stats.Chats, `SELECT COUNT(*) FROM chats`},
		{&stats.Messages, `SELECT COUNT(*) FROM messages`},
		{&stats.Events, `SELECT COUNT(*) FROM events`},
		{&stats.Commands, `SELECT COUNT(*) FROM events WHERE kind = 'command'`},
		{&stats.ModelErrors, `SELECT COUNT(*) FROM events WHERE kind = 'model_error'`},
		{&stats.Timeouts, `SELECT COUNT(*) FROM events WHERE kind = 'timeout'`},
	}
	for _, query := range queries {
		if err := db.QueryRowContext(ctx, query.sql).Scan(query.target); err != nil {
			return stats, fmt.Errorf("admin stats query: %w", err)
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_active_at), '') FROM chats`).Scan(&stats.LastActive); err != nil {
		return stats, fmt.Errorf("admin last activity: %w", err)
	}
	pending, err := UnansweredQuestions(ctx, db, 100)
	if err != nil {
		return stats, fmt.Errorf("admin pending questions: %w", err)
	}
	stats.Pending = len(pending)
	return stats, nil
}
