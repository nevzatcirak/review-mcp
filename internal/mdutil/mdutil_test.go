package mdutil

import (
	"strings"
	"testing"
)

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":          "plain text",
		"a*b_c[d]e|f~g`h":     "a\\*b\\_c\\[d\\]e\\|f\\~g\\`h",
		"<b>x</b> & y":        `\<b\>x\</b\> \& y`,
		"# heading":           `\# heading`,
		"- item\n+ item":      "\\- item\n\\+ item",
		"1. one\n2) two":      "1\\. one\n2\\) two",
		"12 apples":           "12 apples",
		"back\\slash":         "back\\\\slash",
		"line1\r\nline2":      "line1\nline2",
		"  - indented":        "  \\- indented",
		"> quote":             `\> quote`,
		"already \\* escaped": "already \\\\\\* escaped",
	} {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeControlKeepsAngleBrackets(t *testing.T) {
	if got := EscapeControl("<a> & *"); got != `<a> & \*` {
		t.Errorf("EscapeControl = %q", got)
	}
}

func TestInlineFoldsLines(t *testing.T) {
	if got := Inline(" a\nb|c \r\n"); got != `a b\|c` {
		t.Errorf("Inline = %q", got)
	}
}

func TestWriteFencedAdapts(t *testing.T) {
	var b strings.Builder
	WriteFenced(&b, "x\n````\ny", "go", "  ")
	want := "  `````go\n  x\n  ````\n  y\n  `````\n"
	if b.String() != want {
		t.Errorf("WriteFenced = %q, want %q", b.String(), want)
	}
}

func TestCodeSpan(t *testing.T) {
	if got := CodeSpan("a`b"); got != "``a`b``" {
		t.Errorf("CodeSpan = %q", got)
	}
	if got := CodeSpan("`a"); got != "`` `a ``" {
		t.Errorf("CodeSpan = %q", got)
	}
}

func TestCodeSpanKeepsStrippableSpaces(t *testing.T) {
	// " a " would render as "a" between plain delimiters.
	if got := CodeSpan(" a "); got != "`  a  `" {
		t.Errorf("CodeSpan = %q", got)
	}
	if got := CodeSpan("  "); got != "`  `" {
		t.Errorf("CodeSpan = %q", got)
	}
}

func TestEscapeKeepingCodeSpans(t *testing.T) {
	for _, c := range []struct {
		name, in string
		esc      func(string) string
		want     string
	}{
		{"span kept", "Raise `retries` to 3", Escape, "Raise `retries` to 3"},
		{"two spans", "`a` and `b`*", Escape, "`a` and `b`\\*"},
		{"unmatched backtick", "a ` b*", Escape, "a \\` b\\*"},
		{"unmatched after a span", "`a` b ` c", Escape, "`a` b \\` c"},
		{"longer run is not a closer", "`a``b", Escape, "\\`a\\`\\`b"},
		{"inert content", "x `<script>|*[x](y)` y", Escape, "x `<script>|*[x](y)` y"},
		{"inert content GFM", "x `<b>&` <i>", EscapeGFM, "x `<b>&` &lt;i&gt;"},
		{"inert content control", "`<a>` <a>", EscapeControl, "`<a>` <a>"},
		{"double backticks", "use ``a`b`` here", Escape, "use ``a`b`` here"},
		{"delimiter adapts", "```a`b```", Escape, "``a`b``"},
		{"heading-like span at line start", "`# x` y", Escape, "`# x` y"},
		{"list-like span at line start", "`- a`", Escape, "`- a`"},
		{"line start rule before a span", "# `x`", Escape, "\\# `x`"},
		{"ordered start before a span", "1. `x`", Escape, "1\\. `x`"},
		{"no line-start rule after a span", "`x` - y # 2. z", Escape, "`x` - y # 2. z"},
		{"indentation kept", "  - `x` y", Escape, "  \\- `x` y"},
		{"CRLF", "`a`\r\n# b\r`c`", Escape, "`a`\n\\# b\n`c`"},
		{"no span across lines", "a `b\nc` d", Escape, "a \\`b\nc\\` d"},
		{"one-space stripping", "`` `x` ``", Escape, "`` `x` ``"},
		{"one-space stripping plain", "` a `", Escape, "`a`"},
		{"only spaces kept", "`  `", Escape, "`  `"},
		{"backtick in spaces", "`` ` ``", Escape, "`` ` ``"},
		{"two spaces on each side", "`  a  `", Escape, "`  a  `"},
		{"escaped backtick is text", "\\`a` b`", Escape, "\\\\\\`a` b`"},
		{"backslash before a span", "a\\\\`b`", Escape, "a\\\\\\\\`b`"},
	} {
		if got := EscapeKeepingCodeSpans(c.in, c.esc); got != c.want {
			t.Errorf("%s: EscapeKeepingCodeSpans(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// Text without a code span is escaped exactly as before.
func TestEscapeKeepingCodeSpansWithoutSpans(t *testing.T) {
	for _, in := range []string{
		"plain", "# h", "  - a\n+ b", "1. x\r\n2) y", "<b>&</b> | ~ _", "a ` b", "`a\nb`", "back\\slash", "",
	} {
		for name, esc := range map[string]func(string) string{"Escape": Escape, "EscapeControl": EscapeControl, "EscapeGFM": EscapeGFM} {
			if got, want := EscapeKeepingCodeSpans(in, esc), esc(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(in)); got != want {
				t.Errorf("%s: EscapeKeepingCodeSpans(%q) = %q, want %q", name, in, got, want)
			}
		}
	}
}

func TestEscapeKeepingCodeSpansWithBullets(t *testing.T) {
	keep := func(l string) string { return EscapeKeepingCodeSpans(l, EscapeGFM) }
	in := "- Raise `retries` <now>\n  - `# x` stays\n- # no heading"
	want := "- Raise `retries` &lt;now&gt;\n  - `# x` stays\n- \\# no heading"
	if got := EscapeBullets(in, keep); got != want {
		t.Errorf("EscapeBullets = %q, want %q", got, want)
	}
}

func TestEscapeCellKeepingCodeSpans(t *testing.T) {
	if got := EscapeCellKeepingCodeSpans("a|b `c|d` e", Escape); got != "a\\|b `c\\|d` e" {
		t.Errorf("EscapeCellKeepingCodeSpans = %q", got)
	}
}

func TestInlineKeepingCodeSpans(t *testing.T) {
	if got := InlineKeepingCodeSpans(" a `b|<c>`\r\n# d "); got != "a `b|<c>` # d" {
		t.Errorf("InlineKeepingCodeSpans = %q", got)
	}
	// The line breaks are folded first, so a span may span them.
	if got := InlineKeepingCodeSpans("`a\nb`"); got != "`a b`" {
		t.Errorf("InlineKeepingCodeSpans = %q", got)
	}
}
