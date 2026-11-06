package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

type DailyModelUsage struct {
	Model     string
	Calls     int64
	TokensIn  int64
	TokensOut int64
}

type DailyToolUsage struct {
	ReadURL    int64
	WebSearch  int64
	Calculator int64
	RAG        int64
}

type DailyUsage struct {
	Models []DailyModelUsage
	Tools  DailyToolUsage
}

func (usage DailyUsage) ModelCalls() int64 {
	var total int64
	for _, model := range usage.Models {
		total += model.Calls
	}
	return total
}

func (usage DailyUsage) NetworkToolCalls() int64 {
	return usage.Tools.ReadURL + usage.Tools.WebSearch
}

func UserDailyUsage(ctx context.Context, db *sql.DB, userID int64, now time.Time) (DailyUsage, error) {
	usage := DailyUsage{Models: []DailyModelUsage{}}
	dayStart := utcDayStart(now)
	dayEnd := dayStart.Add(24 * time.Hour)
	start := dayStart.Format("2006-01-02 15:04:05")
	end := dayEnd.Format("2006-01-02 15:04:05")

	rows, err := db.QueryContext(ctx, `
		SELECT COALESCE(m.model, ''), COUNT(*), COALESCE(SUM(m.tokens_in), 0), COALESCE(SUM(m.tokens_out), 0)
		FROM messages m
		JOIN chats c ON c.chat_id = m.chat_id
		WHERE c.user_id = ? AND m.role = ? AND m.created_at >= ? AND m.created_at < ?
		GROUP BY m.model
		ORDER BY m.model`, userID, RoleAssistant, start, end)
	if err != nil {
		return usage, fmt.Errorf("daily model usage: %w", err)
	}
	for rows.Next() {
		var model DailyModelUsage
		if err := rows.Scan(&model.Model, &model.Calls, &model.TokensIn, &model.TokensOut); err != nil {
			rows.Close()
			return usage, fmt.Errorf("daily model usage row: %w", err)
		}
		usage.Models = append(usage.Models, model)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return usage, fmt.Errorf("daily model usage rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return usage, fmt.Errorf("daily model usage close: %w", err)
	}

	callRows, err := db.QueryContext(ctx, `
		SELECT COALESCE(detail, ''), COUNT(*)
		FROM events
		WHERE user_id = ? AND kind = ? AND created_at >= ? AND created_at < ?
		GROUP BY detail
		ORDER BY detail`, userID, EventModelCall, start, end)
	if err != nil {
		return usage, fmt.Errorf("daily model calls: %w", err)
	}
	modelCalls := []DailyModelUsage{}
	var totalCalls int64
	for callRows.Next() {
		var model DailyModelUsage
		if err := callRows.Scan(&model.Model, &model.Calls); err != nil {
			callRows.Close()
			return usage, fmt.Errorf("daily model call row: %w", err)
		}
		totalCalls += model.Calls
		modelCalls = append(modelCalls, model)
	}
	if err := callRows.Err(); err != nil {
		callRows.Close()
		return usage, fmt.Errorf("daily model call rows: %w", err)
	}
	if err := callRows.Close(); err != nil {
		return usage, fmt.Errorf("daily model calls close: %w", err)
	}
	if totalCalls > 0 {
		indexes := map[string]int{}
		for index := range usage.Models {
			usage.Models[index].Calls = 0
			indexes[usage.Models[index].Model] = index
		}
		for _, calls := range modelCalls {
			if index, ok := indexes[calls.Model]; ok {
				usage.Models[index].Calls = calls.Calls
			} else {
				usage.Models = append(usage.Models, calls)
			}
		}
		sort.Slice(usage.Models, func(i, j int) bool { return usage.Models[i].Model < usage.Models[j].Model })
	}

	toolRows, err := db.QueryContext(ctx, `
		SELECT detail, COUNT(*)
		FROM events
		WHERE user_id = ? AND kind = ? AND detail IN ('read_url', 'web_search', 'calculator', 'rag')
			AND created_at >= ? AND created_at < ?
		GROUP BY detail`, userID, EventUsage, start, end)
	if err != nil {
		return usage, fmt.Errorf("daily tool usage: %w", err)
	}
	defer toolRows.Close()
	for toolRows.Next() {
		var detail string
		var count int64
		if err := toolRows.Scan(&detail, &count); err != nil {
			return usage, fmt.Errorf("daily tool usage row: %w", err)
		}
		switch detail {
		case "read_url":
			usage.Tools.ReadURL = count
		case "web_search":
			usage.Tools.WebSearch = count
		case "calculator":
			usage.Tools.Calculator = count
		case "rag":
			usage.Tools.RAG = count
		}
	}
	if err := toolRows.Err(); err != nil {
		return usage, fmt.Errorf("daily tool usage rows: %w", err)
	}
	return usage, nil
}

func DailyModelCalls(ctx context.Context, db *sql.DB, userID int64, now time.Time) (int64, error) {
	usage, err := UserDailyUsage(ctx, db, userID, now)
	if err != nil {
		return 0, err
	}
	return usage.ModelCalls(), nil
}

func DailyNetworkToolCalls(ctx context.Context, db *sql.DB, userID int64, now time.Time) (int64, error) {
	usage, err := UserDailyUsage(ctx, db, userID, now)
	if err != nil {
		return 0, err
	}
	return usage.NetworkToolCalls(), nil
}

func utcDayStart(value time.Time) time.Time {
	year, month, day := value.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
