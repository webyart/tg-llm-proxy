package i18n

import (
	"sort"
	"testing"
)

// TestCatalogsHaveTheSameKeys is the guard against a half-translated catalog: every stable message ID
// must exist in every supported language. A key that exists only in RU would silently fall back to
// Russian in the English interface, which is exactly the bug this test makes impossible to ship.
func TestCatalogsHaveTheSameKeys(t *testing.T) {
	var base = catalogs[Default]
	if len(base) == 0 {
		t.Fatal("the default catalog is empty")
	}
	for lang, messages := range catalogs {
		if lang == Default {
			continue
		}
		var missing []string
		for key := range base {
			if _, ok := messages[key]; !ok {
				missing = append(missing, key)
			}
		}
		var extra []string
		for key := range messages {
			if _, ok := base[key]; !ok {
				extra = append(extra, key)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s has %d keys of the %s catalog missing: %v", lang, len(missing), Default, missing)
		}
		if len(extra) > 0 {
			t.Errorf("%s has %d keys that %s does not have: %v", lang, len(extra), Default, extra)
		}
		if len(messages) != len(base) {
			t.Errorf("%s has %d keys, %s has %d", lang, len(messages), Default, len(base))
		}
	}
}

// TestTFormatsPlaceholders checks that the same stable ID formats both languages through printf rather
// than by concatenating fragments: the argument order is fixed by the template.
func TestTFormatsPlaceholders(t *testing.T) {
	if got := T(EN, "help.line", "/ping", "bot health"); got != "/ping — bot health" {
		t.Fatalf("T(EN, help.line) = %q", got)
	}
	if got := T(RU, "help.line", "/ping", "health"); got != "/ping — health" {
		t.Fatalf("T(RU, help.line) = %q", got)
	}
	if got := T(EN, "ask.timeout", 15, "STRATA_TIMEOUT_SECONDS"); got != "The model did not answer within 15 s (STRATA_TIMEOUT_SECONDS), try again." {
		t.Fatalf("T(EN, ask.timeout) = %q", got)
	}
}

// TestTTMissingEnglishKeyFallsBackToRussian removes one key from the English catalog for the duration
// of the test. The real catalogs pass TestCatalogsHaveTheSameKeys, so this is the only way to exercise
// the fallback branch.
func TestTMissingEnglishKeyFallsBackToRussian(t *testing.T) {
	key := "status.title"
	original, ok := catalogs[EN][key]
	if !ok {
		t.Fatalf("the test fixture expects %q in EN", key)
	}
	delete(catalogs[EN], key)
	t.Cleanup(func() { catalogs[EN][key] = original })

	if got := T(EN, key); got != ruMessages[key] {
		t.Fatalf("T(EN, %q) with no English key = %q, want the Russian fallback %q", key, got, ruMessages[key])
	}
}

// TestTUnknownLanguageFallsBackToDefault checks the other fallback dimension: an unsupported language
// code renders the default catalog instead of an empty string.
func TestTUnknownLanguageFallsBackToDefault(t *testing.T) {
	if got := T("de", "status.title"); got != ruMessages["status.title"] {
		t.Fatalf("T(de, status.title) = %q, want the Russian fallback", got)
	}
	if got := T("", "status.model", "m"); got != "Модель: m" {
		t.Fatalf("T(\"\", status.model) = %q", got)
	}
}

// TestTUnknownKeyStaysVisible makes a typo loud: the stable ID is returned, not an empty message.
func TestTUnknownKeyStaysVisible(t *testing.T) {
	if got := T(EN, "no.such.key"); got != "no.such.key" {
		t.Fatalf("T(EN, no.such.key) = %q, want the key itself", got)
	}
}

// TestNormalize covers the values that can arrive from config or SQLite.
func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":     Default,
		"ru":   RU,
		" RU ": RU,
		"en":   EN,
		"EN":   EN,
		"de":   Default,
	}
	for input, want := range cases {
		if got := Normalize(input); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}
