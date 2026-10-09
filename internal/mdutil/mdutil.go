// Package mdutil holds the small markdown-safety helpers shared by the
// renderers: code spans and fenced blocks that untrusted text cannot break
// out of, and escaping of markdown control characters.
package mdutil

import (
	"html"
	"regexp"
	"strings"
)

// CodeSpan wraps s in a markdown code span. The delimiter is one backtick
// longer than the longest backtick run in s, and line breaks become spaces
// so the span cannot be terminated early or break the surrounding line.
func CodeSpan(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	fence := strings.Repeat("`", longestRun(s, '`')+1)
	if s == "" {
		return fence + " " + fence
	}
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

// FenceLen is one more than the longest backtick run in s, at least 3.
func FenceLen(s string) int {
	return max(3, longestRun(s, '`')+1)
}

// WriteFenced writes body inside a backtick fence that the body cannot
// close, with info as the info string (the caller passes a sanitized word
// or ""). Every non-empty line, fence lines included, is prefixed with
// indent.
func WriteFenced(b *strings.Builder, body, info, indent string) {
	fence := strings.Repeat("`", FenceLen(body))
	b.WriteString(indent + fence + info + "\n")
	body = strings.TrimSuffix(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(indent + line + "\n")
	}
	b.WriteString(indent + fence + "\n")
}

func longestRun(s string, c byte) int {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return longest
}

// EscapeControl backslash-escapes the markdown control characters of s that
// can start emphasis, links, code spans, tables or block structure:
// backslash, backtick, asterisk, underscore, square brackets, pipe, tilde,
// and at the start of a line also '#', '+', '-', '=' and a number followed
// by '.' or ')'. It leaves '<', '>' and '&' alone, for callers that
// HTML-escape afterwards (see Escape). Line breaks are kept.
func EscapeControl(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = escapeLine(line)
	}
	return strings.Join(lines, "\n")
}

// Escape is EscapeControl plus backslash escapes for '<', '>' and '&', so no
// HTML tag, autolink, entity or blockquote can come out of s.
func Escape(s string) string {
	s = EscapeControl(s)
	return strings.NewReplacer("<", `\<`, ">", `\>`, "&", `\&`).Replace(s)
}

// Inline is Escape with line breaks folded to spaces, for text that must
// stay on one line (headings, list lead-ins, table cells).
func Inline(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	return Escape(strings.TrimSpace(s))
}

func escapeLine(line string) string {
	var b strings.Builder
	trimmed := strings.TrimLeft(line, " \t")
	b.WriteString(line[:len(line)-len(trimmed)])
	i := 0
	if trimmed != "" {
		switch trimmed[0] {
		case '#', '+', '-', '=':
			b.WriteByte('\\')
		default:
			j := 0
			for j < len(trimmed) && trimmed[j] >= '0' && trimmed[j] <= '9' {
				j++
			}
			if j > 0 && j < len(trimmed) && (trimmed[j] == '.' || trimmed[j] == ')') {
				b.WriteString(trimmed[:j])
				b.WriteByte('\\')
				i = j
			}
		}
	}
	for ; i < len(trimmed); i++ {
		switch c := trimmed[i]; c {
		case '\\', '`', '*', '_', '[', ']', '|', '~':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Literal renders a path or URL as a code span, or as escaped plain text
// when it holds characters that a code span cannot make inert in every
// context (HTML angle brackets, ampersands, pipes).
func Literal(s string) string {
	if strings.ContainsAny(s, "<>&|") {
		return Inline(s)
	}
	return CodeSpan(s)
}

// EscapeGFM escapes one line of model markdown for a GFM provider:
// markdown control characters first, then HTML metacharacters as entities,
// which render as the literal characters in both contexts (pr_review's
// gfmText, without its trimming, which would drop the indentation of a
// continuation line). It moved here from internal/describe/render.
func EscapeGFM(s string) string { return html.EscapeString(EscapeControl(s)) }

// bulletLine matches a list item line: indentation, the marker and the
// space after it, then the item's text.
var bulletLine = regexp.MustCompile(`^([ \t]*)([-*+]|[0-9]{1,9}[.)])([ \t]+)(.*)$`)

// EscapeBullets escapes the model markdown s line by line with esc, keeping
// the marker of a list item line (and its indentation) as it is so that the
// list stays a list. The text after a marker is escaped as the start of a
// line, so an item cannot open a heading or a nested structure. It moved
// here from internal/describe/render, for pr_improve's published text.
func EscapeBullets(s string, esc func(string) string) string {
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(strings.TrimSpace(s))
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if m := bulletLine.FindStringSubmatch(l); m != nil {
			lines[i] = m[1] + m[2] + m[3] + esc(m[4])
			continue
		}
		lines[i] = esc(l)
	}
	return strings.Join(lines, "\n")
}
