package gitctx

import (
	"bytes"
	"strings"
)

// Enclosing definition of a line (V-18 spec §1.1, X-50).
//
// The impact trace attributes a use to the definition around it: the method
// that holds the call, not the class above the method. It does so by name
// and indentation only, with the patterns of definitionName; nothing is
// parsed by a compiler (R-1).

// maxEnclosingLookBack is the number of lines above a use that are looked at.
const maxEnclosingLookBack = 400

// Definition is the definition that encloses a line.
type Definition struct {
	// Name is the defined name.
	Name string
	// Line is the 1-based line of the definition.
	Line int
	// Text is the definition line, trimmed and cut at 400 bytes. It is
	// untrusted repository text.
	Text string
}

// blockWords open a block with a parenthesised head that the bare-method
// pattern of definitionName reads as a definition ("foreach (var x in xs) {").
// They are skipped by the scan only, compared with their case, so that a
// method named Lock or Using is still a definition and ExtractSymbols is
// unchanged.
var blockWords = setOf("foreach", "lock", "synchronized", "using", "fixed", "checked", "unchecked")

// EnclosingDefinition returns the definition that encloses line (1-based) of
// content, a file of language group lang (filter.Language): the nearest line
// at or above it, at most 400 lines up, for which definitionName gives a
// name (with the rules of a hunk header, section true) and whose indentation
// is smaller than the use's. The use line itself counts when it is a
// definition: a use on a class line is the class, a one-line function is
// the function. Blank lines are skipped. Python is indented by rule; brace
// languages are read with the same indentation heuristic.
//
// ok is false when no such line exists within reach (the use is at file
// level), when line is outside content, or when the use line is blank.
// A use inside a string or a comment is attributed like any other: the
// trace matches names. The function is pure.
func EnclosingDefinition(content []byte, lang string, line int) (Definition, bool) {
	return enclosingIn(splitLines(content), lang, line)
}

// splitLines splits content into lines without their "\n" (a "\r" stays and
// is trimmed where a line is read).
func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	parts := bytes.Split(content, []byte{'\n'})
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = string(p)
	}
	return out
}

// enclosingIn is EnclosingDefinition on a file already split into lines.
func enclosingIn(lines []string, lang string, line int) (Definition, bool) {
	if line < 1 || line > len(lines) {
		return Definition{}, false
	}
	use := strings.TrimRight(lines[line-1], "\r")
	if strings.TrimSpace(use) == "" {
		return Definition{}, false
	}
	if d, ok := definitionAt(lines, lang, line); ok {
		return d, true
	}
	limit := indentWidth(use)
	for i := line - 1; i >= 1 && i >= line-maxEnclosingLookBack; i-- {
		text := strings.TrimRight(lines[i-1], "\r")
		if strings.TrimSpace(text) == "" {
			continue
		}
		w := indentWidth(text)
		if w >= limit {
			continue
		}
		if d, ok := definitionAt(lines, lang, i); ok {
			return d, true
		}
	}
	return Definition{}, false
}

// definitionAt returns the definition on line i (1-based), if it is one.
func definitionAt(lines []string, lang string, i int) (Definition, bool) {
	text := strings.TrimRight(lines[i-1], "\r")
	name, ok := definitionName(text, lang, true)
	if !ok || blockWords[name] {
		return Definition{}, false
	}
	return Definition{Name: name, Line: i, Text: cutLine(strings.TrimSpace(text))}, true
}

// indentWidth is the width of the leading blanks of s, a tab counting as
// four columns.
func indentWidth(s string) int {
	w := 0
	for _, r := range s {
		switch r {
		case ' ':
			w++
		case '\t':
			w += 4
		default:
			return w
		}
	}
	return w
}
