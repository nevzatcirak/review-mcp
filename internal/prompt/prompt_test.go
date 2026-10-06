package prompt

import (
	"strings"
	"testing"
	"text/template"
	"time"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// TestMissingKeyFails is the [canary] of spec P4 §4.2: a template that
// references a variable the caller did not set must fail to render.
func TestMissingKeyFails(t *testing.T) {
	set := parse(t, "x", "a={{ .set }} b={{ .unset }}")
	out, err := Execute(set, "x", map[string]any{"set": "1"})
	if err == nil {
		t.Fatalf("rendering with an unset variable succeeded: %q", out)
	}
	if !strings.Contains(err.Error(), "unset") {
		t.Errorf("error does not name the key: %v", err)
	}
	if _, err := Execute(set, "x", map[string]any{"set": "1", "unset": ""}); err != nil {
		t.Errorf("an empty value is set and must render: %v", err)
	}
}

func parse(t *testing.T, name, text string) *template.Template {
	t.Helper()
	set, err := New(name).Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestExecuteDropsOneTrailingNewline(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a", "a"},
		{"a\n", "a"},
		{"a\n\n", "a\n"},
		{"", ""},
	} {
		got, err := Execute(parse(t, "x", tc.in), "x", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("Execute(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTrimFunc(t *testing.T) {
	got, err := Execute(parse(t, "x", "[{{ trim .d }}]"), "x", map[string]any{"d": " \t\x1c a b \n "})
	if err != nil {
		t.Fatal(err)
	}
	if got != "[a b]" {
		t.Errorf("trim = %q", got)
	}
}

func TestDate(t *testing.T) {
	c := fixedClock{time.Date(2026, 3, 9, 23, 59, 0, 0, time.UTC)}
	if got := Date(c); got != "2026-03-09" {
		t.Errorf("Date = %q", got)
	}
	if got := Date(nil); len(got) != len(DateLayout) {
		t.Errorf("Date(nil) = %q", got)
	}
}

func TestWithOutputLanguage(t *testing.T) {
	const instrTR = "Your response MUST be written in the language corresponding to locale code: 'tr-TR'. " +
		"This is crucial. Keep schema control values (such as 'No', 'Yes', 'None', 'false') in their " +
		"original English form and do not translate them."
	for _, tc := range []struct {
		name, extra, lang, want string
	}{
		{"default language", "be brief", "en-US", "be brief"},
		{"default language, other case", "be brief", "EN-us", "be brief"},
		{"empty language means default", "be brief", "", "be brief"},
		{"default, no extra", "", "en-US", ""},
		{"non-English, no extra", "", "tr-TR", instrTR},
		{"non-English, extra", "be brief", "tr-TR", "be brief\n======\n\nIn addition, " + instrTR},
		{"already present", "x " + instrTR, "tr-TR", "x " + instrTR},
		{"whitespace extra counts as set", " ", "tr-TR", " \n======\n\nIn addition, " + instrTR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithOutputLanguage(tc.extra, tc.lang); got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
	if got := LanguageInstruction("de-DE"); !strings.Contains(got, "'de-DE'") {
		t.Errorf("LanguageInstruction = %q", got)
	}
}
