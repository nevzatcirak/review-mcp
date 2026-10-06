package yamlrepair

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Upstream works on Python strings. The helpers below reproduce the Python
// string semantics the repair chain depends on. Go strings are treated as
// UTF-8; every search target used here is ASCII, so byte offsets and Python
// code-point offsets select the same text.

// pyIsSpace mirrors Python's str.isspace() (and the regex class \s) for one
// code point. Python also treats the information separators \x1c..\x1f as
// whitespace; Go does not.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip mirrors Python's str.strip() without arguments.
func pyStrip(s string) string {
	return strings.TrimFunc(s, pyIsSpace)
}

// pyRStrip mirrors Python's str.rstrip() without arguments.
func pyRStrip(s string) string {
	return strings.TrimRightFunc(s, pyIsSpace)
}

// lastLine returns the text after the last "\n" (Python's
// s.split("\n")[-1]).
func lastLine(s string) string {
	return s[strings.LastIndexByte(s, '\n')+1:]
}

// lastRuneStart returns the byte offset of the last code point of s, which
// is where Python's index -1 points. It returns 0 for an empty string.
func lastRuneStart(s string) int {
	if s == "" {
		return 0
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return len(s) - size
}

// hasSuffixFoldASCII reports whether s ends with suffix, comparing ASCII
// letters case-insensitively (Python's s[-n:].lower() == suffix for an
// ASCII, lower-case suffix).
func hasSuffixFoldASCII(s, suffix string) bool {
	if len(s) < len(suffix) {
		return false
	}
	return equalFoldASCII(s[len(s)-len(suffix):], suffix)
}

// hasPrefixFoldASCII reports whether s starts with prefix, comparing ASCII
// letters case-insensitively.
func hasPrefixFoldASCII(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return equalFoldASCII(s[:len(prefix)], prefix)
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// skipSpaceTab returns the first offset at or after i that is not a space
// or a tab (the regex [ \t]*).
func skipSpaceTab(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// newlineAt reports whether s has "\n" or "\r\n" at offset i (the regex
// \r?\n) and returns the offset after it.
func newlineAt(s string, i int) (int, bool) {
	if i < len(s) && s[i] == '\r' {
		i++
	}
	if i < len(s) && s[i] == '\n' {
		return i + 1, true
	}
	return 0, false
}

// yamlLabelEnd matches the optional fence label (?i:yaml|yml) at offset i
// and returns the offsets where a match could end, longest alternative
// first; the last element is i itself (the label is optional).
func yamlLabelEnd(s string, i int) []int {
	var ends []int
	if hasPrefixFoldASCII(s[i:], "yaml") {
		ends = append(ends, i+4)
	}
	if hasPrefixFoldASCII(s[i:], "yml") {
		ends = append(ends, i+3)
	}
	return append(ends, i)
}
