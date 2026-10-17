package issueref

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDescriptionBytes is how much of the description is scanned; the rest is
// left out, cut at a character boundary.
const MaxDescriptionBytes = 10 * 1024

// cleanLine returns s without control characters (a line break or a tab
// becomes a space), as one line.
func cleanLine(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}

// cleanDescription returns the part of the description that is scanned: at
// most MaxDescriptionBytes of it, line breaks as "\n", without control
// characters other than line breaks and tabs, and with every fenced code
// block (its fences included) left out.
func cleanDescription(s string) string {
	s = cutBytes(s, MaxDescriptionBytes)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
	return withoutFences(s)
}

// cutBytes returns at most n bytes of s, cut at a character boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// withoutFences replaces every line of a fenced code block, its opening and
// closing fences included, with an empty line. A fence is three or more
// backticks or tildes after at most three spaces; it is closed by a fence of
// the same character that is at least as long and carries nothing else. A
// block that is never closed runs to the end of s (CommonMark).
func withoutFences(s string) string {
	lines := strings.Split(s, "\n")
	var fence string
	for i, l := range lines {
		f, info := fenceOf(l)
		switch {
		case fence == "" && f != "":
			fence = f
		case fence != "" && f != "" && f[0] == fence[0] && len(f) >= len(fence) && info == "":
			fence = ""
		case fence == "":
			continue
		}
		lines[i] = ""
	}
	return strings.Join(lines, "\n")
}

// fenceOf returns the fence that line opens or closes ("" when it is not a
// fence line) and the text after it, trimmed. A backtick fence whose info
// string holds a backtick is not a fence (CommonMark).
func fenceOf(line string) (fence, info string) {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent > 3 {
		return "", ""
	}
	rest := line[indent:]
	if rest == "" || (rest[0] != '`' && rest[0] != '~') {
		return "", ""
	}
	n := len(rest) - len(strings.TrimLeft(rest, rest[:1]))
	if n < 3 {
		return "", ""
	}
	info = strings.TrimSpace(rest[n:])
	if rest[0] == '`' && strings.Contains(info, "`") {
		return "", ""
	}
	return rest[:n], info
}
