package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// UsageRange is the window of the usage analytics series. The daily fields of /api/usage stay as
// they were; a range only adds buckets and totals that cover a longer (or shorter) window.
type UsageRange string

const (
	UsageRangeHour  UsageRange = "hour"
	UsageRangeDay   UsageRange = "day"
	UsageRangeWeek  UsageRange = "week"
	UsageRangeMonth UsageRange = "month"
)

const (
	// MaxUsageSeriesBuckets caps the number of points a chart has to render. The four public ranges
	// fit (24/7/8/12); the cap exists so a future range cannot turn the response into an unbounded
	// list, and Truncated tells the client when the oldest buckets were dropped.
	MaxUsageSeriesBuckets = 32
	// MaxUsageSeriesWindow caps how far back the grouped queries may read. The longest public range
	// (12 monthly buckets) is under a year; the cap is deliberately above it.
	MaxUsageSeriesWindow = 400 * 24 * time.Hour
)

// UsageToolNames is the fixed tool vocabulary of the usage events. Any other detail is not a tool
// call and is deliberately ignored, so a stray event cannot grow the chart.
func UsageToolNames() []string {
	return []string{"read_url", "web_search", "calculator", "rag"}
}

func ParseUsageRange(value string) (UsageRange, bool) {
	switch UsageRange(value) {
	case UsageRangeHour, UsageRangeDay, UsageRangeWeek, UsageRangeMonth:
		return UsageRange(value), true
	}
	return "", false
}

type UsageSeriesBucket struct {
	Start      time.Time
	ModelCalls int64
	TokensIn   int64
	TokensOut  int64
	Tools      map[string]int64
	ToolsTotal int64
}

type UsageSeriesModel struct {
	Model     string
	Calls     int64
	TokensIn  int64
	TokensOut int64
}

type UsageSeries struct {
	Range      UsageRange
	Buckets    []UsageSeriesBucket
	Models     []UsageSeriesModel
	Tools      map[string]int64
	ModelCalls int64
	TokensIn   int64
	TokensOut  int64
	ToolsTotal int64
	Truncated  bool
}

type usageSeriesSpec struct {
	starts    []time.Time
	end       time.Time
	bucketSQL string
	keyLayout string
}

// usageSeriesSpecFor builds the UTC bucket boundaries. Every public range ends with the bucket that
// contains now:
//
//	hour  — 24 hourly buckets, the last one is the current hour;
//	day   — 7 daily buckets, the last one is today (UTC);
//	week  — 8 ISO-like weekly buckets starting on Monday, the last one is the current week;
//	month — 12 calendar-month buckets, the last one is the current month.
func usageSeriesSpecFor(now time.Time, rng UsageRange) (usageSeriesSpec, error) {
	now = now.UTC()
	switch rng {
	case UsageRangeHour:
		anchor := now.Truncate(time.Hour)
		starts := make([]time.Time, 24)
		for index := range starts {
			starts[index] = anchor.Add(time.Duration(index-23) * time.Hour)
		}
		return usageSeriesSpec{
			starts:    starts,
			end:       anchor.Add(time.Hour),
			bucketSQL: `strftime('%Y-%m-%d %H:00:00', created_at)`,
			keyLayout: "2006-01-02 15:00:00",
		}, nil
	case UsageRangeDay:
		anchor := utcDayStart(now)
		starts := make([]time.Time, 7)
		for index := range starts {
			starts[index] = anchor.AddDate(0, 0, index-6)
		}
		return usageSeriesSpec{
			starts:    starts,
			end:       anchor.AddDate(0, 0, 1),
			bucketSQL: `date(created_at)`,
			keyLayout: "2006-01-02",
		}, nil
	case UsageRangeWeek:
		anchor := utcWeekStart(now)
		starts := make([]time.Time, 8)
		for index := range starts {
			starts[index] = anchor.AddDate(0, 0, (index-7)*7)
		}
		return usageSeriesSpec{
			starts:    starts,
			end:       anchor.AddDate(0, 0, 7),
			bucketSQL: `date(created_at, 'weekday 0', '-6 days')`,
			keyLayout: "2006-01-02",
		}, nil
	case UsageRangeMonth:
		anchor := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		starts := make([]time.Time, 12)
		for index := range starts {
			starts[index] = anchor.AddDate(0, index-11, 0)
		}
		return usageSeriesSpec{
			starts:    starts,
			end:       anchor.AddDate(0, 1, 0),
			bucketSQL: `strftime('%Y-%m-01', created_at)`,
			keyLayout: "2006-01-02",
		}, nil
	default:
		return usageSeriesSpec{}, fmt.Errorf("unknown usage range %q", rng)
	}
}

func utcWeekStart(value time.Time) time.Time {
	day := utcDayStart(value)
	offset := (int(day.Weekday()) + 6) % 7 // Monday = 0, Sunday = 6
	return day.AddDate(0, 0, -offset)
}

// UserUsageSeries aggregates one user's model calls, tokens and tool calls over the requested UTC
// window. It runs one grouped messages query and one grouped events query regardless of the number
// of buckets; no bucket is scanned separately. The messages query supplies tokens per model, the
// events query supplies model_call counts and tool counts.
func UserUsageSeries(ctx context.Context, db *sql.DB, userID int64, now time.Time, rng UsageRange) (UsageSeries, error) {
	result := UsageSeries{
		Range:   rng,
		Buckets: []UsageSeriesBucket{},
		Models:  []UsageSeriesModel{},
		Tools:   newUsageToolCounts(),
	}
	spec, err := usageSeriesSpecFor(now, rng)
	if err != nil {
		return result, err
	}
	spec, result.Truncated = capUsageSeriesSpec(spec)
	if len(spec.starts) == 0 {
		return result, nil
	}

	indexes := make(map[string]int, len(spec.starts))
	for index, start := range spec.starts {
		indexes[start.Format(spec.keyLayout)] = index
		result.Buckets = append(result.Buckets, UsageSeriesBucket{Start: start, Tools: newUsageToolCounts()})
	}
	startText := spec.starts[0].Format("2006-01-02 15:04:05")
	endText := spec.end.Format("2006-01-02 15:04:05")

	modelIndex := make(map[string]int)
	modelAt := func(name string) *UsageSeriesModel {
		if index, ok := modelIndex[name]; ok {
			return &result.Models[index]
		}
		result.Models = append(result.Models, UsageSeriesModel{Model: name})
		modelIndex[name] = len(result.Models) - 1
		return &result.Models[len(result.Models)-1]
	}

	messageRows, err := db.QueryContext(ctx, `
		SELECT `+spec.bucketSQL+` AS bucket, COALESCE(model, ''),
		       COALESCE(SUM(tokens_in), 0), COALESCE(SUM(tokens_out), 0)
		FROM messages
		WHERE user_id = ? AND role = ? AND created_at >= ? AND created_at < ?
		GROUP BY bucket, model`, userID, RoleAssistant, startText, endText)
	if err != nil {
		return result, fmt.Errorf("usage series messages: %w", err)
	}
	for messageRows.Next() {
		var key, model string
		var tokensIn, tokensOut int64
		if err := messageRows.Scan(&key, &model, &tokensIn, &tokensOut); err != nil {
			messageRows.Close()
			return result, fmt.Errorf("usage series message row: %w", err)
		}
		index, ok := indexes[key]
		if !ok {
			continue
		}
		entry := modelAt(model)
		entry.TokensIn += tokensIn
		entry.TokensOut += tokensOut
		result.Buckets[index].TokensIn += tokensIn
		result.Buckets[index].TokensOut += tokensOut
	}
	if err := messageRows.Err(); err != nil {
		messageRows.Close()
		return result, fmt.Errorf("usage series message rows: %w", err)
	}
	if err := messageRows.Close(); err != nil {
		return result, fmt.Errorf("usage series message close: %w", err)
	}

	// One grouped events query supplies both the model_call counts and the tool counts. Detail is
	// either a model id (model_call) or one of the fixed tool names (usage); nothing else is read.
	eventRows, err := db.QueryContext(ctx, `
		SELECT `+spec.bucketSQL+` AS bucket, kind,
			CASE WHEN kind = 'model_call' AND instr(COALESCE(detail, ''), ':') > 0
				THEN substr(detail, instr(detail, ':') + 1) ELSE COALESCE(detail, '') END, COUNT(*)
		FROM events
		WHERE user_id = ? AND kind IN (?, ?) AND created_at >= ? AND created_at < ?
		GROUP BY bucket, kind, detail`, userID, EventModelCall, EventUsage, startText, endText)
	if err != nil {
		return result, fmt.Errorf("usage series events: %w", err)
	}
	for eventRows.Next() {
		var key, kind, detail string
		var count int64
		if err := eventRows.Scan(&key, &kind, &detail, &count); err != nil {
			eventRows.Close()
			return result, fmt.Errorf("usage series event row: %w", err)
		}
		index, ok := indexes[key]
		if !ok {
			continue
		}
		switch kind {
		case EventModelCall:
			modelAt(detail).Calls += count
			result.Buckets[index].ModelCalls += count
		case EventUsage:
			if _, known := result.Tools[detail]; !known {
				continue
			}
			result.Buckets[index].Tools[detail] += count
			result.Buckets[index].ToolsTotal += count
			result.Tools[detail] += count
			result.ToolsTotal += count
		}
	}
	if err := eventRows.Err(); err != nil {
		eventRows.Close()
		return result, fmt.Errorf("usage series event rows: %w", err)
	}
	if err := eventRows.Close(); err != nil {
		return result, fmt.Errorf("usage series event close: %w", err)
	}

	sort.Slice(result.Models, func(i, j int) bool { return result.Models[i].Model < result.Models[j].Model })
	for index := range result.Buckets {
		result.ModelCalls += result.Buckets[index].ModelCalls
		result.TokensIn += result.Buckets[index].TokensIn
		result.TokensOut += result.Buckets[index].TokensOut
	}
	return result, nil
}

// capUsageSeriesSpec enforces the two hard limits of a series. Public ranges are already below both,
// so the flag is false for them; the helper keeps the invariant explicit and testable.
func capUsageSeriesSpec(spec usageSeriesSpec) (usageSeriesSpec, bool) {
	truncated := false
	if len(spec.starts) > MaxUsageSeriesBuckets {
		spec.starts = spec.starts[len(spec.starts)-MaxUsageSeriesBuckets:]
		truncated = true
	}
	for len(spec.starts) > 1 && spec.end.Sub(spec.starts[0]) > MaxUsageSeriesWindow {
		spec.starts = spec.starts[1:]
		truncated = true
	}
	return spec, truncated
}

func newUsageToolCounts() map[string]int64 {
	counts := make(map[string]int64, 4)
	for _, name := range UsageToolNames() {
		counts[name] = 0
	}
	return counts
}
