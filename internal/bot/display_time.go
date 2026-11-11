package bot

import (
	"context"
	"strings"

	"bot/internal/timefmt"
)

// storedTime renders a timestamp read from a TEXT column. Storage writes UTC without a zone suffix in
// several tables, so the raw value is parsed by timefmt and converted to the effective chat zone. A
// malformed value is returned unchanged rather than replaced by an invented date.
func (d *Dispatcher) storedTime(ctx context.Context, chatID int64, lang string, raw string) string {
	parsed, ok := timefmt.Parse(raw)
	if !ok {
		return strings.TrimSpace(raw)
	}
	return timefmt.Relative(parsed, d.now(), d.chatLocation(ctx, chatID), lang)
}

// storedAbsoluteTime is the absolute form of storedTime for a screen where a relative label would be
// ambiguous (search results, links, exported metadata).
func (d *Dispatcher) storedAbsoluteTime(ctx context.Context, chatID int64, lang string, raw string) string {
	parsed, ok := timefmt.Parse(raw)
	if !ok {
		return strings.TrimSpace(raw)
	}
	return timefmt.DateTimeZoned(parsed, d.chatLocation(ctx, chatID), lang)
}

// zoneAbbreviation is the short zone name shown next to a clock time in a footer.
func (d *Dispatcher) zoneAbbreviation(ctx context.Context, chatID int64) string {
	return timefmt.Zone(d.now(), d.chatLocation(ctx, chatID))
}

// localDate is the calendar date of now in the chat zone; the daily usage window itself stays UTC.
func (d *Dispatcher) localDate(ctx context.Context, chatID int64) string {
	local := d.now().In(d.chatLocation(ctx, chatID))
	return local.Format("02.01.2006")
}
