package prompt

import (
	"strings"
	"unicode"

	"github.com/nevzatcirak/review-mcp/internal/mdutil"
)

// FencedBlock is a header line and body in a code fence the body cannot
// close, without a trailing newline: the shape of every block of untrusted
// text in a prompt (the project guidelines and the repository guidance of
// projctx, the issue of issuectx). It is the one fencing helper they share.
func FencedBlock(header, body string) string {
	var b strings.Builder
	b.WriteString(header + "\n")
	mdutil.WriteFenced(&b, body, "", "")
	return strings.TrimRight(b.String(), "\n")
}

// CleanText sanitizes untrusted text for a prompt block: invalid UTF-8 is
// replaced, line endings become "\n", control characters other than line
// feed and tab and the bidirectional formatting characters are removed,
// trailing space is trimmed from each line, and leading and trailing blank
// lines are dropped.
func CleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	s = strings.Map(func(r rune) rune {
		if (r != '\n' && r != '\t' && unicode.IsControl(r)) || IsBidiControl(r) {
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// IsBidiControl reports whether r is a bidirectional formatting character
// (embedding, override, isolate or mark), which can make text display in an
// order other than the one it is read in.
func IsBidiControl(r rune) bool {
	return r == '\u200e' || r == '\u200f' || r == '\u061c' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}
