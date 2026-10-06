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
