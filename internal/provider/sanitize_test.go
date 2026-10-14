package provider

import (
	"math/rand/v2"
	"strings"
	"testing"
)

var quickCaps = Capabilities{GFM: true, QuickActions: true}

func TestQuickActionLine(t *testing.T) {
	for line, want := range map[string]bool{
		"/merge": true, "/a": true, "/1": true, "/_x": true, "/é": true,
		"": false, "/": false, "//": false, "// Retry": false, "/* c */": false, "/ x": false, "/-": false,
		" /merge": false, "x/merge": false, "\t/merge": false,
	} {
		if got := QuickActionLine(line); got != want {
			t.Errorf("QuickActionLine(%q) = %v, want %v", line, got, want)
		}
	}
	if !HasQuickActionLine("a\r\n/merge") || !HasQuickActionLine("a\r/x") || HasQuickActionLine("// a\n/* b */\n /c") {
		t.Error("HasQuickActionLine does not split at every line ending")
	}
}

// TestSanitizeBodySuggestionBlocks (X-35): inside a suggestion fence only a
// line that could be a quick action gets the space; outside it every line
// that starts with "/" does, as before. [canary: exempt every line inside
// suggestion fences / keep sanitising "//" lines inside the block]
func TestSanitizeBodySuggestionBlocks(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"doc comment kept",
			"**S**\n\n```suggestion\n// Retry retries.\nfunc Retry() {}\n```\n\n[//]: # (m)",
			"**S**\n\n```suggestion\n// Retry retries.\nfunc Retry() {}\n```\n\n[//]: # (m)"},
		{"block comment and lone slash kept",
			"```suggestion:-0+2\n/* a */\n/\n/-x\n```\n",
			"```suggestion:-0+2\n/* a */\n/\n/-x\n```\n"},
		{"quick action inside the block sanitised",
			"```suggestion\n// ok\n/merge\n/label ~x\n```\n",
			"```suggestion\n// ok\n /merge\n /label ~x\n```\n"},
		{"outside the block every slash line",
			"/close\n// note\n```suggestion\n// keep\n```\n// after\n/merge",
			" /close\n // note\n```suggestion\n// keep\n```\n // after\n /merge"},
		{"other info strings are not suggestion blocks",
			"```go\n// x\n```\n```suggestion foo\n// y\n```\n```suggestions\n// z\n```\n```suggestion:-1\n// w\n```\n",
			"```go\n // x\n```\n```suggestion foo\n // y\n```\n```suggestions\n // z\n```\n```suggestion:-1\n // w\n```\n"},
		{"tilde fence, indented, longer closer",
			"   ~~~~ suggestion:-12+3 \n// a\n~~~\n// b\n~~~~~\n// c",
			"   ~~~~ suggestion:-12+3 \n// a\n~~~\n// b\n~~~~~\n // c"},
		{"four spaces is not a fence",
			"    ```suggestion\n// a\n",
			"    ```suggestion\n // a\n"},
		{"a backtick fence's closer must be as long",
			"````suggestion\n// a\n```\n// b\n````\n// c",
			"````suggestion\n// a\n```\n// b\n````\n // c"},
		{"a closer with an info string does not close",
			"```suggestion\n// a\n```go\n// b\n```\n// c",
			"```suggestion\n// a\n```go\n// b\n```\n // c"},
		{"suggestion text inside another block is content",
			"````markdown\n```suggestion\n// a\n```\n// b\n````\n",
			"````markdown\n```suggestion\n // a\n```\n // b\n````\n"},
		{"unclosed block runs to the end",
			"```suggestion\n// a\n/merge",
			"```suggestion\n// a\n /merge"},
		{"CRLF and CR line endings",
			"```suggestion\r\n// a\r\n/merge\r// b\r```\r/close\r\n",
			"```suggestion\r\n// a\r\n /merge\r// b\r```\r /close\r\n"},
	}
	for _, c := range cases {
		if got := SanitizeBody(quickCaps, c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if got := SanitizeBody(quickCaps, c.want); got != c.want {
			t.Errorf("%s: not idempotent: %q", c.name, got)
		}
		if got := SanitizeBody(Capabilities{GFM: true}, c.in); got != c.in {
			t.Errorf("%s: changed without QuickActions: %q", c.name, got)
		}
	}
}

// TestSanitizeBodyHandWrittenFence: a model's text with a hand-written
// suggestion fence that holds a quick action is still sanitised. [canary:
// exempt every line inside suggestion fences]
func TestSanitizeBodyHandWrittenFence(t *testing.T) {
	in := "**Finding**\n\nTry this:\n```suggestion\n/merge\n```\n"
	if got, want := SanitizeBody(quickCaps, in), "**Finding**\n\nTry this:\n```suggestion\n /merge\n```\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSanitizeBodyOutsideSuggestionsUnchanged: for any body without a
// suggestion fence the result is SanitizeQuickActions' byte for byte,
// whatever fences, slashes and line endings it has.
func TestSanitizeBodyOutsideSuggestionsUnchanged(t *testing.T) {
	parts := []string{"/", "//", "/x", "a", " ", "\n", "\r", "\r\n", "```", "~~~", "```go", "````", "\t", "suggest", "/merge"}
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test input, not security-relevant
	for range 20000 {
		var b strings.Builder
		for range r.IntN(12) {
			b.WriteString(parts[r.IntN(len(parts))])
		}
		s := b.String()
		if strings.Contains(s, "suggestion") {
			continue
		}
		if got, want := SanitizeBody(quickCaps, s), SanitizeQuickActions(s); got != want {
			t.Fatalf("SanitizeBody(%q) = %q, SanitizeQuickActions = %q", s, got, want)
		}
	}
}
