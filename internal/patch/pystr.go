package patch

import (
	"strings"
	"unicode"
)

// The upstream algorithms this package mirrors work on Python strings. The
// helpers below reproduce the Python string semantics the rendered bytes
// still depend on (strip, slicing and indexing; spec §0.1, DQ-10).
//
// Line splitting is the exception. Upstream splits with str.splitlines, which
// also breaks lines at \f, \v, \x1c-\x1e, \x85, U+2028, U+2029 and a lone \r,
// so every later line number drifts from the file's real line numbers. Per
// architect decision D5 (PR #4), an intentional deviation from upstream,
// lines are split at "\n" only (splitLines): numbered line numbers must equal
// real "\n"-based file line numbers, which the DQ-12 snippets and links (P4)
// and the v2 anchoring depend on. CRLF handling follows the lead decision on
// the D5 implementation: a "\r" directly before the "\n" belongs to the line
// ending and is dropped wherever upstream drops "\r\n", so CRLF files render
// exactly as before; a lone "\r" is ordinary content, as in git.
//
// Go strings are treated as UTF-8. Invalid bytes never count as whitespace.

// splitLines splits s into lines at "\n" only and drops the line endings
// ("\n", or "\r\n" as one ending). An empty string yields no lines; a
// trailing ending does not produce a trailing empty line. Every other
// character, including \f, \v, \x85, U+2028 and a lone "\r", is content.
func splitLines(s string) []string {
	lines, _ := splitLinesKeep(s)
	return lines
}

// splitLinesKeep is splitLines that also returns every line with its
// original ending attached.
func splitLinesKeep(s string) (lines, withEnds []string) {
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			lines = append(lines, s)
			withEnds = append(withEnds, s)
			break
		}
		lines = append(lines, strings.TrimSuffix(s[:i], "\r"))
		withEnds = append(withEnds, s[:i+1])
		s = s[i+1:]
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
