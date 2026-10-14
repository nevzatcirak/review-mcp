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
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") || stripsSpace(s) {
		return fence + " " + s + " " + fence
	}
	return fence + s + fence
}

// stripsSpace reports a code span content that CommonMark would strip one
// space from on each side: it starts and ends with a space and is not only
// spaces.
func stripsSpace(s string) bool {
	return len(s) >= 2 && s[0] == ' ' && s[len(s)-1] == ' ' && strings.Trim(s, " ") != ""
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

// EscapeKeepingCodeSpans escapes the model markdown s with esc (EscapeControl,
// Escape or EscapeGFM, or one of them per line through EscapeBullets) and
// keeps its well-formed inline code spans as code spans (X-30). Each line is
// scanned on its own, so a span never crosses a line break: a span is an
// opening run of N backticks closed by the next run of exactly N backticks
// on the same line, and a run without such a closer (or one whose first
// backtick is backslash-escaped) is text. A span is emitted again with
// CodeSpan of its content, after CommonMark's one-space stripping, so its
// delimiter is safe whatever the content holds; the content itself is
// neither backslash- nor HTML-escaped (a code span shows it verbatim).
//
// The text between spans is escaped exactly as esc escapes the whole line:
// the text before the first span gets esc as it is (so its line-start rules
// apply), and the text after a span is escaped as the middle of a line
// (escapeMid). A line without a span is esc of the line.
func EscapeKeepingCodeSpans(s string, esc func(string) string) string {
	return escapeKeepingCodeSpans(s, esc, false)
}

// EscapeCellKeepingCodeSpans is EscapeKeepingCodeSpans for one line of text
// in a GFM pipe-table cell: a '|' splits a cell even inside a code span, so
// the pipes of a span's content are backslash-escaped too, which GFM removes
// again before it parses the span.
func EscapeCellKeepingCodeSpans(s string, esc func(string) string) string {
	return escapeKeepingCodeSpans(s, esc, true)
}

// InlineKeepingCodeSpans is Inline for model prose: line breaks folded to
// spaces, then Escape with the model's code spans kept
// (EscapeKeepingCodeSpans). It is not for a table cell.
func InlineKeepingCodeSpans(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	return EscapeKeepingCodeSpans(strings.TrimSpace(s), Escape)
}

// EscapeHTMLKeepingCodeSpans is EscapeKeepingCodeSpans for text inside an
// HTML element that does not render markdown (the <summary> of a collapsible
// block): the text between spans is escaped with html.EscapeString, and each
// well-formed code span (X-30) becomes <code>content</code> with the content
// escaped the same way.
func EscapeHTMLKeepingCodeSpans(s string) string {
	return escapeLines(s, html.EscapeString, false, func(content string) string {
		return "<code>" + html.EscapeString(content) + "</code>"
	})
}

func escapeKeepingCodeSpans(s string, esc func(string) string, cell bool) string {
	return escapeLines(s, esc, cell, CodeSpan)
}

// escapeLines escapes s line by line with esc and writes each code span
// through span, which gets its content after CommonMark's space stripping.
func escapeLines(s string, esc func(string) string, cell bool, span func(string) string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = escapeLineKeepingCodeSpans(line, esc, cell, span)
	}
	return strings.Join(lines, "\n")
}

func escapeLineKeepingCodeSpans(line string, esc func(string) string, cell bool, span func(string) string) string {
	var b strings.Builder
	text := 0 // start of the text not yet written
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		if backslashesBefore(line, text, i)%2 == 1 {
			// An escaped backtick is text; the rest of its run may open.
			i++
			continue
		}
		open := runEnd(line, i)
		closeAt, closeEnd := spanCloser(line, open, open-i)
		if closeAt < 0 {
			i = open
			continue
		}
		writeText(&b, line[text:i], esc, text == 0)
		content := line[open:closeAt]
		if stripsSpace(content) {
			content = content[1 : len(content)-1]
		}
		out := span(content)
		if cell {
			out = strings.ReplaceAll(out, "|", `\|`)
		}
		b.WriteString(out)
		text, i = closeEnd, closeEnd
	}
	if text == 0 {
		return esc(line)
	}
	writeText(&b, line[text:], esc, false)
	return b.String()
}

// writeText writes the escaped text of a line; atStart is set for the text
// at the start of the line.
func writeText(b *strings.Builder, text string, esc func(string) string, atStart bool) {
	switch {
	case atStart:
		b.WriteString(esc(text))
	case text != "":
		b.WriteString(escapeMid(text, esc))
	}
}

// escapeMid escapes text that follows a code span on its line, as esc
// escapes it in the middle of a line: esc's rules for the start of a line
// ('#', '-', "1." …) must not apply to it. It escapes a neutral letter in
// front of the text, which no escaper here changes and which starts no
// line-start rule, and drops it again; for an esc that changes the letter
// it falls back to esc of the text alone.
func escapeMid(text string, esc func(string) string) string {
	if out := esc("x" + text); strings.HasPrefix(out, "x") {
		return out[1:]
	}
	return esc(text)
}

// backslashesBefore counts the backslashes right before line[i], not
// looking before from.
func backslashesBefore(line string, from, i int) int {
	n := 0
	for j := i - 1; j >= from && line[j] == '\\'; j-- {
		n++
	}
	return n
}

// runEnd is the end of the backtick run that starts at line[i].
func runEnd(line string, i int) int {
	for i < len(line) && line[i] == '`' {
		i++
	}
	return i
}

// spanCloser finds the next run of exactly n backticks at or after from and
// returns its start and end, or -1, -1.
func spanCloser(line string, from, n int) (int, int) {
	for i := from; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		end := runEnd(line, i)
		if end-i == n {
			return i, end
		}
		i = end
	}
	return -1, -1
}
