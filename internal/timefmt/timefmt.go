// Package timefmt renders user-facing dates, clock times and durations.
//
// Storage and every calculation stay in UTC; this package is the display boundary. It is shared by the
// Telegram bot and the Mini App so both clients say «через 12 мин» and «11 ноя, 00:53» instead of a raw
// RFC3339 timestamp. The API keeps machine-readable ISO timestamps: only screens use these functions.
package timefmt

import (
	"fmt"
	"strings"
	"time"
	// Embedded IANA database: the container image ships tzdata, but a scratch-like test stage or a
	// stripped deployment must still load Europe/Berlin and Asia/Yekaterinburg for the chat timezone.
	_ "time/tzdata"
)

// The two interface languages of the bot. The values match internal/i18n so a caller can pass the chat
// language through without a mapping table.
const (
	RU = "ru"
	EN = "en"
)

var monthsRU = [...]string{"янв", "фев", "мар", "апр", "мая", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}
var monthsEN = [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

func language(lang string) string {
	if strings.EqualFold(strings.TrimSpace(lang), EN) {
		return EN
	}
	return RU
}

// languageOf keeps the language optional: an omitted argument means Russian, which is the bot's default.
func languageOf(langs []string) string {
	if len(langs) == 0 {
		return RU
	}
	return language(langs[0])
}

// Duration renders elapsed or total work time. Under a minute it says «~<1 мин» instead of a raw second
// count; under an hour it keeps the m:ss form a reader scans quickly («6:12»); longer values use hours and
// minutes («2 ч 5 мин»). A zero or negative duration is «0:00».
func Duration(value time.Duration, langs ...string) string {
	lang := languageOf(langs)
	if value <= 0 {
		return "0:00"
	}
	if value < time.Minute {
		if lang == EN {
			return "~<1 min"
		}
		return "~<1 мин"
	}
	if value < time.Hour {
		minutes := int64(value / time.Minute)
		seconds := int64((value % time.Minute) / time.Second)
		return fmt.Sprintf("%d:%02d", minutes, seconds)
	}
	hours := int64(value / time.Hour)
	minutes := int64((value % time.Hour) / time.Minute)
	if minutes == 0 {
		if lang == EN {
			return fmt.Sprintf("%d h", hours)
		}
		return fmt.Sprintf("%d ч", hours)
	}
	if lang == EN {
		return fmt.Sprintf("%d h %d min", hours, minutes)
	}
	return fmt.Sprintf("%d ч %d мин", hours, minutes)
}

// ETA renders a remaining-time estimate. The tilde is part of the text: an estimate is never a promise.
// Minutes are rounded up, so «~2 мин» never means «less than two minutes». Zero or negative means «none»
// and is rendered as an em dash, exactly like the task footer did before this package existed.
func ETA(value time.Duration, langs ...string) string {
	lang := languageOf(langs)
	if value <= 0 {
		return "—"
	}
	if value < time.Minute {
		if lang == EN {
			return "~<1 min"
		}
		return "~<1 мин"
	}
	totalMinutes := int64((value + time.Minute - 1) / time.Minute)
	if totalMinutes < 60 {
		if lang == EN {
			return fmt.Sprintf("~%d min", totalMinutes)
		}
		return fmt.Sprintf("~%d мин", totalMinutes)
	}
	hours := totalMinutes / 60
	minutes := totalMinutes % 60
	if minutes == 0 {
		if lang == EN {
			return fmt.Sprintf("~%d h", hours)
		}
		return fmt.Sprintf("~%d ч", hours)
	}
	if lang == EN {
		return fmt.Sprintf("~%d h %d min", hours, minutes)
	}
	return fmt.Sprintf("~%d ч %d мин", hours, minutes)
}

// Relative renders t relative to now in loc: «через 12 мин», «2 ч 5 мин назад», «вчера 18:40» for the
// nearest day, a day count for the nearest week and the absolute «11 ноя, 00:53» beyond that. A zero time
// is empty so an optional timestamp does not print as a date in 0001.
func Relative(t time.Time, now time.Time, loc *time.Location, langs ...string) string {
	lang := languageOf(langs)
	if t.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	reference := now.In(loc)
	diff := local.Sub(reference)
	future := diff > 0
	absolute := diff
	if absolute < 0 {
		absolute = -absolute
	}
	if absolute < time.Minute {
		if future {
			if lang == EN {
				return "in <1 min"
			}
			return "через <1 мин"
		}
		if lang == EN {
			return "just now"
		}
		return "только что"
	}
	if absolute < time.Hour {
		minutes := int64(absolute / time.Minute)
		return relativeMinutes(lang, minutes, future)
	}
	if sameDay(local, reference) {
		return relativeHours(lang, absolute, future)
	}
	switch dayDifference(local, reference) {
	case 1:
		if lang == EN {
			return "tomorrow " + clock(local)
		}
		return "завтра " + clock(local)
	case -1:
		if lang == EN {
			return "yesterday " + clock(local)
		}
		return "вчера " + clock(local)
	}
	days := dayDifference(local, reference)
	if days >= 2 && days <= 6 {
		if lang == EN {
			return fmt.Sprintf("in %d days", days)
		}
		return fmt.Sprintf("через %s", PluralRU(int64(days), "день", "дня", "дней"))
	}
	if days <= -2 && days >= -6 {
		if lang == EN {
			return fmt.Sprintf("%d days ago", -days)
		}
		return fmt.Sprintf("%s назад", PluralRU(int64(-days), "день", "дня", "дней"))
	}
	return DateTime(t, loc, lang)
}

// DateTime renders the absolute form «11 ноя, 00:53» in loc. The zone is deliberately not appended: a
// screen that needs it uses DateTimeZoned, so a table of near times stays narrow.
func DateTime(t time.Time, loc *time.Location, langs ...string) string {
	lang := languageOf(langs)
	if t.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	months := monthsRU[:]
	if lang == EN {
		months = monthsEN[:]
	}
	return fmt.Sprintf("%d %s, %s", local.Day(), months[int(local.Month())-1], clock(local))
}

// DateTimeZoned is DateTime plus the zone abbreviation when the location has one («11 ноя, 00:53 YEKT»).
func DateTimeZoned(t time.Time, loc *time.Location, langs ...string) string {
	base := DateTime(t, loc, langs...)
	if base == "" {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	name, _ := t.In(loc).Zone()
	if name == "" {
		return base
	}
	return base + " " + friendlyZone(name, loc)
}

// Date renders the day of t in loc («11 ноя»).
func Date(t time.Time, loc *time.Location, langs ...string) string {
	lang := languageOf(langs)
	if t.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	months := monthsRU[:]
	if lang == EN {
		months = monthsEN[:]
	}
	return fmt.Sprintf("%d %s", local.Day(), months[int(local.Month())-1])
}

// Clock renders HH:MM of t in loc.
func Clock(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	return clock(t.In(loc))
}

func clock(t time.Time) string {
	return fmt.Sprintf("%02d:%02d", t.Hour(), t.Minute())
}

// Zone returns the abbreviation of loc at t («YEKT», «CET»), or the IANA name when the abbreviation is
// empty. An empty location is UTC.
func Zone(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	if name, _ := t.In(loc).Zone(); name != "" {
		return friendlyZone(name, loc)
	}
	return loc.String()
}

// friendlyZone replaces the numeric abbreviation the current IANA database uses for several Russian and
// Central Asian zones («+05») with the conventional short name a reader recognises. A real abbreviation
// (CET, CEST, EST, UTC) is returned untouched.
func friendlyZone(name string, loc *time.Location) string {
	if name != "" && name[0] != '+' && name[0] != '-' {
		return name
	}
	switch loc.String() {
	case "Asia/Yekaterinburg":
		return "YEKT"
	case "Europe/Moscow":
		return "MSK"
	case "Asia/Almaty":
		return "ALMT"
	case "Asia/Tbilisi":
		return "GET"
	case "America/New_York":
		if name == "-04" {
			return "EDT"
		}
		return "EST"
	case "UTC", "Etc/UTC":
		return "UTC"
	default:
		return name
	}
}

func relativeMinutes(lang string, minutes int64, future bool) string {
	if lang == EN {
		if future {
			return fmt.Sprintf("in %d min", minutes)
		}
		return fmt.Sprintf("%d min ago", minutes)
	}
	if future {
		return fmt.Sprintf("через %d мин", minutes)
	}
	return fmt.Sprintf("%d мин назад", minutes)
}

func relativeHours(lang string, value time.Duration, future bool) string {
	hours := int64(value / time.Hour)
	minutes := int64((value % time.Hour) / time.Minute)
	var body string
	switch {
	case minutes == 0 && lang == EN:
		body = fmt.Sprintf("%d h", hours)
	case minutes == 0:
		body = fmt.Sprintf("%d ч", hours)
	case lang == EN:
		body = fmt.Sprintf("%d h %d min", hours, minutes)
	default:
		body = fmt.Sprintf("%d ч %d мин", hours, minutes)
	}
	if lang == EN {
		if future {
			return "in " + body
		}
		return body + " ago"
	}
	if future {
		return "через " + body
	}
	return body + " назад"
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// dayDifference is the number of calendar days from reference to local, ignoring the clock. It compares
// date triples in UTC so a DST shift of 23 or 25 hours cannot turn «tomorrow» into «today».
func dayDifference(local, reference time.Time) int {
	ly, lm, ld := local.Date()
	ry, rm, rd := reference.Date()
	left := time.Date(ly, lm, ld, 0, 0, 0, 0, time.UTC)
	right := time.Date(ry, rm, rd, 0, 0, 0, 0, time.UTC)
	return int(left.Sub(right) / (24 * time.Hour))
}

// PluralRU picks the Russian noun form for n: 1 → one, 2..4 → few, 0 and 5..20 → many, with the 11..14
// exception. It is exported because the bot's day counts and future screens use the same rule.
func PluralRU(n int64, one string, few string, many string) string {
	mod100 := n % 100
	if mod100 >= 11 && mod100 <= 14 {
		return fmt.Sprintf("%d %s", n, many)
	}
	switch n % 10 {
	case 1:
		return fmt.Sprintf("%d %s", n, one)
	case 2, 3, 4:
		return fmt.Sprintf("%d %s", n, few)
	default:
		return fmt.Sprintf("%d %s", n, many)
	}
}

// Parse turns a stored timestamp into a time. The store writes UTC without a zone suffix in several tables
// (`2006-01-02 15:04:05`), while the API emits RFC3339Nano; both forms and a bare date are accepted. A value
// without a zone is UTC, which is the storage contract. The bool is false for an empty or malformed value so
// a caller can fall back to the raw string rather than inventing a time.
func Parse(raw string) (time.Time, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return parsed.UTC(), true
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if parsed, err := time.ParseInLocation(layout, text, time.UTC); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}
