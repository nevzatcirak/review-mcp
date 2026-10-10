package provider

import "testing"

func TestEscapeNamespace(t *testing.T) {
	cases := map[string]string{
		"octo":           "octo",
		"~jdoe":          "~jdoe",
		"o x":            "o%20x",
		"group/sub/team": "group/sub/team",
		"a b/c?d/e%f":    "a%20b/c%3Fd/e%25f",
	}
	for in, want := range cases {
		if got := EscapeNamespace(in); got != want {
			t.Errorf("EscapeNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeQuickActions(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"plain":             "plain",
		"a/b\nc/d":          "a/b\nc/d",
		"/x":                " /x",
		"a\n/b":             "a\n /b",
		"a\r/b":             "a\r /b",
		"a\r\n/b":           "a\r\n /b",
		"a\n/\n/":           "a\n /\n /",
		"a\n //already":     "a\n //already",
		"a\n\n/b\n/c\r/d\n": "a\n\n /b\n /c\r /d\n",
	}
	for in, want := range cases {
		if got := SanitizeQuickActions(in); got != want {
			t.Errorf("SanitizeQuickActions(%q) = %q, want %q", in, got, want)
		}
		// Idempotent: a body can pass through the sanitiser twice.
		if got := SanitizeQuickActions(want); got != want {
			t.Errorf("SanitizeQuickActions(%q) is not idempotent: %q", want, got)
		}
	}
}

func TestSanitizeBodyFollowsTheCapability(t *testing.T) {
	const body = "/close\ntext\n/reopen"
	if got := SanitizeBody(Capabilities{GFM: true}, body); got != body {
		t.Errorf("without QuickActions the body changed: %q", got)
	}
	if got, want := SanitizeBody(Capabilities{QuickActions: true}, body), " /close\ntext\n /reopen"; got != want {
		t.Errorf("with QuickActions: %q, want %q", got, want)
	}
}

// TestNativeSuggestionStyle: the style counts only with SuggestionBlocks.
func TestNativeSuggestionStyle(t *testing.T) {
	for _, c := range []struct {
		caps Capabilities
		want SuggestionStyle
	}{
		{Capabilities{}, SuggestionStyleNone},
		{Capabilities{SuggestionStyle: SuggestionStyleRange}, SuggestionStyleNone},
		{Capabilities{SuggestionBlocks: true, SuggestionStyle: SuggestionStyleRange}, SuggestionStyleRange},
		{Capabilities{SuggestionBlocks: true, SuggestionStyle: SuggestionStyleOffset}, SuggestionStyleOffset},
		{Capabilities{SuggestionBlocks: true}, SuggestionStyleNone},
	} {
		if got := c.caps.NativeSuggestionStyle(); got != c.want {
			t.Errorf("%+v: NativeSuggestionStyle = %q, want %q", c.caps, got, c.want)
		}
	}
}
