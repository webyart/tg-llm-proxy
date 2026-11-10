package webapp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"bot/internal/store"
)

func appendWebappAssistant(t *testing.T, app *App, chatID int64, userID int64, content string, model string, tokensIn int64, tokensOut int64) {
	t.Helper()
	if _, err := store.Append(context.Background(), app.db, store.Message{
		ChatID: chatID, UserID: userID, Role: store.RoleAssistant, Content: content, Model: model,
		TokensIn: &tokensIn, TokensOut: &tokensOut,
	}); err != nil {
		t.Fatal(err)
	}
}

func appendWebappAssistantAt(t *testing.T, app *App, chatID int64, userID int64, content string, model string, tokensIn int64, tokensOut int64, createdAt string) {
	t.Helper()
	id, err := store.Append(context.Background(), app.db, store.Message{
		ChatID: chatID, UserID: userID, Role: store.RoleAssistant, Content: content, Model: model,
		TokensIn: &tokensIn, TokensOut: &tokensOut,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(context.Background(), `UPDATE messages SET created_at = ? WHERE id = ?`, createdAt, id); err != nil {
		t.Fatal(err)
	}
}

func insertWebappEventAt(t *testing.T, app *App, userID int64, kind string, detail string, createdAt string) {
	t.Helper()
	if _, err := app.db.ExecContext(context.Background(),
		`INSERT INTO events (user_id, kind, detail, created_at) VALUES (?, ?, ?, ?)`,
		userID, kind, detail, createdAt); err != nil {
		t.Fatal(err)
	}
}

func TestUsageReturnsContentFreeAggregatesLimitsAndRemaining(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	app.cfg.UserDailyModelLimit = 3
	app.cfg.UserDailyNetworkToolLimit = 2
	ctx := context.Background()
	privateQuery := "private query text must not leave the store"
	privateAnswer := "private answer text must not leave the store"
	if _, err := store.Append(ctx, app.db, store.Message{ChatID: testChatID, UserID: testUserID, Role: store.RoleUser, Content: privateQuery}); err != nil {
		t.Fatal(err)
	}
	appendWebappAssistant(t, app, testChatID, testUserID, privateAnswer, "model-a", 10, 20)
	appendWebappAssistant(t, app, testChatID+1, testUserID+1, "other user private answer", "model-a", 1000, 2000)
	for index := 0; index < 2; index++ {
		if err := store.LogEvent(ctx, app.db, testUserID, store.EventModelCall, "model-a"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"read_url", "web_search", "calculator", "rag", privateQuery} {
		if err := store.LogEvent(ctx, app.db, testUserID, store.EventUsage, tool); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.LogEvent(ctx, app.db, testUserID+1, store.EventModelCall, "model-a"); err != nil {
		t.Fatal(err)
	}
	var messagesBefore, eventsBefore int
	if err := app.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM messages), (SELECT COUNT(*) FROM events)`).Scan(&messagesBefore, &eventsBefore); err != nil {
		t.Fatal(err)
	}

	status, body, _ := apiRequest(t, server, http.MethodGet, "/api/usage", bearer)
	if status != http.StatusOK {
		t.Fatalf("usage status=%d body=%s", status, body)
	}
	var decoded usageResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Date == "" || len(decoded.Models) != 1 {
		t.Fatalf("usage response=%+v", decoded)
	}
	model := decoded.Models[0]
	if model.Model != "model-a" || model.Calls != 2 || model.TokensIn != 10 || model.TokensOut != 20 {
		t.Fatalf("model usage=%+v", model)
	}
	if decoded.Tools.ReadURL != 1 || decoded.Tools.WebSearch != 1 || decoded.Tools.Calculator != 1 || decoded.Tools.RAG != 1 {
		t.Fatalf("tool usage=%+v", decoded.Tools)
	}
	if decoded.Limits.Model.Used != 2 || decoded.Limits.Model.Limit != 3 || decoded.Limits.Model.Remaining == nil || *decoded.Limits.Model.Remaining != 1 {
		t.Fatalf("model limit=%+v", decoded.Limits.Model)
	}
	if decoded.Limits.NetworkTools.Used != 2 || decoded.Limits.NetworkTools.Limit != 2 || decoded.Limits.NetworkTools.Remaining == nil || *decoded.Limits.NetworkTools.Remaining != 0 {
		t.Fatalf("network limit=%+v", decoded.Limits.NetworkTools)
	}
	for _, forbidden := range []string{privateQuery, privateAnswer, "other user private answer", `"content"`, `"goal"`, `"detail"`} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("usage response contains %q: %s", forbidden, body)
		}
	}

	app.cfg.UserDailyModelLimit = 0
	app.cfg.UserDailyNetworkToolLimit = 0
	status, body, _ = apiRequest(t, server, http.MethodGet, "/api/usage", bearer)
	if status != http.StatusOK || !strings.Contains(string(body), `"remaining":null`) {
		t.Fatalf("unlimited usage status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Limits.Model.Remaining != nil || decoded.Limits.NetworkTools.Remaining != nil {
		t.Fatalf("unlimited limits=%+v", decoded.Limits)
	}
	status, body, _ = apiRequest(t, server, http.MethodPost, "/api/usage", bearer)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("POST usage status=%d body=%s", status, body)
	}
	var messagesAfter, eventsAfter int
	if err := app.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM messages), (SELECT COUNT(*) FROM events)`).Scan(&messagesAfter, &eventsAfter); err != nil {
		t.Fatal(err)
	}
	if messagesAfter != messagesBefore || eventsAfter != eventsBefore {
		t.Fatalf("read-only usage wrote rows: messages %d/%d events %d/%d", messagesBefore, messagesAfter, eventsBefore, eventsAfter)
	}
}

func TestUsageRangesReturnUTCBucketsModelsAndTools(t *testing.T) {
	app, server := newTestApp(t)
	app.cfg.UserDailyModelLimit = 5
	bearer := launchSession(t, app, server)
	now := time.Now().UTC()
	createdAt := now.Format("2006-01-02 15:04:05")
	privateQuery := "private range query text must not leave the store"
	appendWebappAssistantAt(t, app, testChatID, testUserID, privateQuery, "series-model", 7, 9, createdAt)
	appendWebappAssistantAt(t, app, testChatID+1, testUserID+1, "other user range answer", "series-model", 1000, 2000, createdAt)
	for index := 0; index < 2; index++ {
		insertWebappEventAt(t, app, testUserID, store.EventModelCall, "series-model", createdAt)
	}
	for _, tool := range []string{"read_url", "web_search", "web_search", "calculator", "calculator", "calculator", "rag", "rag", "rag", "rag", "private tool detail"} {
		insertWebappEventAt(t, app, testUserID, store.EventUsage, tool, createdAt)
	}
	insertWebappEventAt(t, app, testUserID, store.EventCommand, "read_url", createdAt)
	insertWebappEventAt(t, app, testUserID+1, store.EventModelCall, "series-model", createdAt)
	insertWebappEventAt(t, app, testUserID+1, store.EventUsage, "read_url", createdAt)

	tests := []struct {
		rangeName string
		buckets   int
		step      func(time.Time) time.Time
		aligned   func(time.Time) bool
	}{
		{rangeName: "hour", buckets: 24, step: func(start time.Time) time.Time { return start.Add(time.Hour) }, aligned: func(start time.Time) bool {
			return start.Minute() == 0 && start.Second() == 0
		}},
		{rangeName: "day", buckets: 7, step: func(start time.Time) time.Time { return start.AddDate(0, 0, 1) }, aligned: func(start time.Time) bool {
			return start.Hour() == 0 && start.Minute() == 0 && start.Second() == 0
		}},
		{rangeName: "week", buckets: 8, step: func(start time.Time) time.Time { return start.AddDate(0, 0, 7) }, aligned: func(start time.Time) bool {
			return start.Weekday() == time.Monday && start.Hour() == 0 && start.Minute() == 0 && start.Second() == 0
		}},
		{rangeName: "month", buckets: 12, step: func(start time.Time) time.Time { return start.AddDate(0, 1, 0) }, aligned: func(start time.Time) bool {
			return start.Day() == 1 && start.Hour() == 0 && start.Minute() == 0 && start.Second() == 0
		}},
	}
	for _, test := range tests {
		t.Run(test.rangeName, func(t *testing.T) {
			status, body, _ := apiRequest(t, server, http.MethodGet, "/api/usage?range="+test.rangeName, bearer)
			if status != http.StatusOK {
				t.Fatalf("usage status=%d body=%s", status, body)
			}
			var decoded usageResponse
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Range != test.rangeName {
				t.Errorf("range=%q, want %q", decoded.Range, test.rangeName)
			}
			if decoded.Truncated {
				t.Error("public range reported truncation")
			}
			if len(decoded.Series) != test.buckets {
				t.Fatalf("series buckets=%d, want %d", len(decoded.Series), test.buckets)
			}
			var previous time.Time
			for index, bucket := range decoded.Series {
				if !strings.HasSuffix(bucket.Start, "Z") {
					t.Errorf("bucket %d start=%q, want an RFC3339 UTC instant", index, bucket.Start)
				}
				start, err := time.Parse(time.RFC3339, bucket.Start)
				if err != nil {
					t.Fatalf("bucket %d start=%q: %v", index, bucket.Start, err)
				}
				if start.Location() != time.UTC {
					t.Errorf("bucket %d start=%q, want UTC", index, bucket.Start)
				}
				if !test.aligned(start) {
					t.Errorf("bucket %d start=%s is not aligned to the %s range", index, start, test.rangeName)
				}
				if !previous.IsZero() && !start.Equal(test.step(previous)) {
					t.Errorf("bucket %d start=%s, want %s after %s", index, start, test.step(previous), previous)
				}
				previous = start
				if len(bucket.Tools) != 4 {
					t.Errorf("bucket %d tools=%v, want four fixed keys", index, bucket.Tools)
				}
			}
			if decoded.Totals.ModelCalls != 2 || decoded.Totals.TokensIn != 7 || decoded.Totals.TokensOut != 9 || decoded.Totals.ToolCalls != 10 {
				t.Errorf("totals=%+v, want calls=2 tokens=7/9 tools=10", decoded.Totals)
			}
			if len(decoded.Totals.Models) != 1 {
				t.Fatalf("range models=%+v, want one series-model", decoded.Totals.Models)
			}
			if got := decoded.Totals.Models[0]; got.Model != "series-model" || got.Calls != 2 || got.TokensIn != 7 || got.TokensOut != 9 {
				t.Errorf("range model=%+v, want 2 calls and 7/9 tokens", got)
			}
			wantTools := map[string]int64{"read_url": 1, "web_search": 2, "calculator": 3, "rag": 4}
			for name, want := range wantTools {
				if got := decoded.Totals.Tools[name]; got != want {
					t.Errorf("range tool %s=%d, want %d", name, got, want)
				}
			}
			if _, ok := decoded.Totals.Tools["private tool detail"]; ok {
				t.Errorf("unknown usage detail leaked into range tools: %v", decoded.Totals.Tools)
			}
			seriesCalls, seriesIn, seriesOut, seriesTools := int64(0), int64(0), int64(0), int64(0)
			pointFound := false
			for _, bucket := range decoded.Series {
				seriesCalls += bucket.ModelCalls
				seriesIn += bucket.TokensIn
				seriesOut += bucket.TokensOut
				seriesTools += bucket.ToolsTotal
				if bucket.ModelCalls == 2 && bucket.TokensIn == 7 && bucket.TokensOut == 9 {
					pointFound = true
				}
			}
			if seriesCalls != 2 || seriesIn != 7 || seriesOut != 9 || seriesTools != 10 {
				t.Errorf("series sums=%d/%d/%d/%d, want 2/7/9/10", seriesCalls, seriesIn, seriesOut, seriesTools)
			}
			if !pointFound {
				t.Error("no bucket carries the inserted message and model calls")
			}
			// The daily fields stay in place and still describe today.
			if decoded.Date == "" || decoded.Limits.Model.Limit != 5 {
				t.Errorf("daily fields changed: date=%q limits=%+v", decoded.Date, decoded.Limits)
			}
			for _, forbidden := range []string{privateQuery, "other user range answer", "private tool detail", `"detail"`} {
				if strings.Contains(string(body), forbidden) {
					t.Errorf("usage range response contains %q: %s", forbidden, body)
				}
			}
		})
	}
}

func TestUsageRejectsUnknownOrRepeatedRanges(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for _, query := range []string{
		"/api/usage?range=year",
		"/api/usage?range=",
		"/api/usage?range=day&range=week",
		"/api/usage?other=day",
	} {
		status, body, _ := apiRequest(t, server, http.MethodGet, query, bearer)
		if status != http.StatusBadRequest || !strings.Contains(string(body), `"invalid_query"`) {
			t.Errorf("%s: status=%d body=%s, want 400 invalid_query", query, status, body)
		}
	}
}

func TestUsageEmptyDatabaseReturnsZeroBucketsForEveryRange(t *testing.T) {
	app, server := newTestApp(t)
	bearer := launchSession(t, app, server)
	for rangeName, buckets := range map[string]int{"hour": 24, "day": 7, "week": 8, "month": 12} {
		status, body, _ := apiRequest(t, server, http.MethodGet, "/api/usage?range="+rangeName, bearer)
		if status != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", rangeName, status, body)
		}
		var decoded usageResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.Series) != buckets {
			t.Errorf("%s: buckets=%d, want %d", rangeName, len(decoded.Series), buckets)
		}
		if decoded.Totals.ModelCalls != 0 || decoded.Totals.TokensIn != 0 || decoded.Totals.TokensOut != 0 || decoded.Totals.ToolCalls != 0 {
			t.Errorf("%s: non-zero totals: %+v", rangeName, decoded.Totals)
		}
		if decoded.Totals.Models == nil || len(decoded.Totals.Models) != 0 {
			t.Errorf("%s: range models=%v, want an empty array", rangeName, decoded.Totals.Models)
		}
		if len(decoded.Totals.Tools) != 4 {
			t.Errorf("%s: range tools=%v, want four zero keys", rangeName, decoded.Totals.Tools)
		}
		for index, bucket := range decoded.Series {
			if bucket.ModelCalls != 0 || bucket.TokensIn != 0 || bucket.TokensOut != 0 || bucket.ToolsTotal != 0 {
				t.Errorf("%s: bucket %d is non-zero: %+v", rangeName, index, bucket)
			}
			if len(bucket.Tools) != 4 {
				t.Errorf("%s: bucket %d tools=%v, want four zero keys", rangeName, index, bucket.Tools)
			}
		}
		if strings.Contains(string(body), `"series":null`) || strings.Contains(string(body), `"models":null`) {
			t.Errorf("%s: null collection in %s", rangeName, body)
		}
	}
}
