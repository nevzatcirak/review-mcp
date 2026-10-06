package patch

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// The upstream algorithms this package mirrors work on Python strings. The
// helpers below reproduce the exact Python string semantics they rely on, so
// that the rendered bytes match the upstream output (spec §0.1, DQ-10).
//
// Go strings are treated as UTF-8. Invalid bytes never count as line breaks
// or whitespace.

// isPyLineBreak reports whether r is a line boundary for Python's
// str.splitlines: \n, \r, \v, \f, \x1c, \x1d, \x1e, \x85, U+2028 and U+2029.
func isPyLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// pySplitLines mirrors Python's str.splitlines(): the text is split at every
// Python line boundary ("\r\n" counts as one) and the boundaries are dropped.
// An empty string yields no lines; a trailing boundary does not produce a
// trailing empty line.
func pySplitLines(s string) []string {
	lines, _ := pySplitLinesKeep(s)
	return lines
}

// pySplitLinesKeep is pySplitLines that also returns every line with its
// original boundary attached (str.splitlines(keepends=True)).
func pySplitLinesKeep(s string) (lines, withEnds []string) {
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if !isPyLineBreak(r) {
			i += size
			continue
		}
		next := i + size
		if r == '\r' && next < len(s) && s[next] == '\n' {
			next++
		}
		lines = append(lines, s[start:i])
		withEnds = append(withEnds, s[start:next])
		i, start = next, next
	}
	if start < len(s) {
		lines = append(lines, s[start:])
		withEnds = append(withEnds, s[start:])
	}
	return lines, withEnds
}

// pyIsSpace mirrors Python's str.isspace() for one code point. Python also
// treats the information separators \x1c..\x1f as whitespace; Go does not.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip mirrors Python's str.strip() without arguments.
func pyStrip(s string) string {
	return strings.TrimFunc(s, pyIsSpace)
}

// stripCRLF mirrors Python's str.strip("\r\n").
func stripCRLF(s string) string {
	return strings.Trim(s, "\r\n")
}

// rstripCRLF mirrors Python's str.rstrip("\r\n").
func rstripCRLF(s string) string {
	return strings.TrimRight(s, "\r\n")
}

// pySliceBounds converts the Python slice s[a:b] of a sequence of length n
// into Go bounds, including negative indices and clamping.
func pySliceBounds(n, a, b int) (lo, hi int) {
	norm := func(i int) int {
		if i < 0 {
			i += n
			if i < 0 {
				i = 0
			}
		}
		if i > n {
			i = n
		}
		return i
	}
	lo, hi = norm(a), norm(b)
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

// pySlice returns the Python slice s[a:b].
func pySlice(s []string, a, b int) []string {
	lo, hi := pySliceBounds(len(s), a, b)
	return s[lo:hi]
}

// pyTail returns the Python slice s[i:] for i >= 0.
func pyTail(s []string, i int) []string {
	if i >= len(s) {
		return nil
	}
	return s[i:]
}

// pyIndex resolves the Python index s[i]; ok is false where Python raises
// IndexError.
func pyIndex(n, i int) (int, bool) {
	if i < 0 {
		i += n
	}
	return i, i >= 0 && i < n
}
