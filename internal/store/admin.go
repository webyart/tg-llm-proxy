package store

import (
	"context"
	"database/sql"
	"fmt"
)

type AdminSummary struct {
	Chats          int64
	Messages       int64
	Events         int64
	Commands       int64
	ModelErrors    int64
	Timeouts       int64
	Pending        int
	RAGUses        int64
	CalculatorUses int64
	URLReaderUses  int64
	WebSearchUses  int64
	RAGDocuments   int64
	LastActive     string
}

func AdminStats(ctx context.Context, db *sql.DB) (AdminSummary, error) {
	var stats AdminSummary
	queries := []struct {
		target *int64
		sql    string
	}{
		{&stats.Chats, `SELECT COUNT(*) FROM chats`},
		{&stats.Messages, `SELECT COUNT(*) FROM messages`},
		{&stats.RAGDocuments, `SELECT COUNT(*) FROM rag_documents`},
	}
	for _, query := range queries {
		if err := db.QueryRowContext(ctx, query.sql).Scan(query.target); err != nil {
			return stats, fmt.Errorf("admin stats query: %w", err)
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COUNT(*) FILTER (WHERE kind = 'command'),
		COUNT(*) FILTER (WHERE kind = 'model_error'),
		COUNT(*) FILTER (WHERE kind = 'timeout'),
		COUNT(*) FILTER (WHERE kind = 'usage' AND detail = 'rag'),
		COUNT(*) FILTER (WHERE kind = 'usage' AND detail = 'calculator'),
		COUNT(*) FILTER (WHERE kind = 'usage' AND detail = 'read_url'),
		COUNT(*) FILTER (WHERE kind = 'usage' AND detail = 'web_search')
	FROM events`).Scan(
		&stats.Events,
		&stats.Commands,
		&stats.ModelErrors,
		&stats.Timeouts,
		&stats.RAGUses,
		&stats.CalculatorUses,
		&stats.URLReaderUses,
		&stats.WebSearchUses,
	); err != nil {
		return stats, fmt.Errorf("admin event stats: %w", err)
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
