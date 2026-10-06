package yamlrepair

import (
	"strings"
	"unicode/utf8"
)

// input is what every tactic starts from. Tactics never modify it: each one
// builds its candidates from a fresh copy.
type input struct {
	// text is the preprocessed answer (upstream's response_text).
	text string
	// original is the raw answer with only the control characters removed
	// (upstream's response_text_original after sanitizing).
	original string
	keys     Keys
}

// tactic is one repair step. apply returns the candidate texts to parse, in
// order; the driver returns the first one that parses to a non-empty
// mapping. A tactic that does not apply returns nothing.
type tactic struct {
	name  string
	apply func(in input) []string
}

// Tactic names, as reported in Trace.Tactic. The numbers are upstream's
// fallback positions in try_fix_yaml (porting map §D).
const (
	tacticBlockScalarKeys = "block_scalar_keys" // 1
	tacticExplicitIndent  = "explicit_indent"   // 2 (upstream "1.5")
	tacticFencedSnippet   = "fenced_snippet"    // 4 (upstream "second")
	tacticStripBraces     = "strip_braces"      // 5 (upstream "third")
	tacticKeyWindow       = "key_window"        // 6 (upstream "forth")
	tacticStripPlus       = "strip_plus"        // 7 (upstream "fifth")
	tacticDiffMarkers     = "diff_markers"      // 8 (upstream "5.5")
	tacticTabsToSpaces    = "tabs_to_spaces"    // 9 (upstream "sixth")
	tacticRootPipe        = "root_pipe"         // 11 (upstream "eighth")
	tacticReencode        = "reencode"          // 12 (upstream "ninth")
)

// chain is the repair chain (DQ-8): upstream tactics 1, 2, 4, 5, 6, 7, 8,
// 9, 11 and 12 in upstream order. Later tactics assume the earlier ones
// failed, so the order must not change. Tactic 3 (re-indenting "}" lines
// into block scalars) and tactic 10 (re-indenting the code sections of the
// improve and describe tools) target answer shapes the review tool does
// not ask for and are not ported.
var chain = []tactic{
	{tacticBlockScalarKeys, blockScalarKeys},
	{tacticExplicitIndent, explicitIndent},
	{tacticFencedSnippet, fencedSnippet},
	{tacticStripBraces, stripBraces},
	{tacticKeyWindow, keyWindow},
	{tacticStripPlus, stripPlus},
	{tacticDiffMarkers, diffMarkers},
	{tacticTabsToSpaces, tabsToSpaces},
	{tacticRootPipe, rootPipe},
	{tacticReencode, reencode},
}

// blockScalarKeys (upstream tactic 1) turns "<key>:" into
// "<key>: |" plus a line break and eight spaces on every line that contains
// a known key and no "|", so the value becomes a block scalar. This repairs
// unquoted values that contain ": " or quotes. Keys are tried in order on
// the line as modified so far.
func blockScalarKeys(in input) []string {
	lines := strings.Split(in.text, "\n")
	for i, line := range lines {
		for _, name := range in.keys.Names {
			if name == "" {
				continue
			}
			key := name + ":"
			if strings.Contains(line, key) && !strings.Contains(line, "|") {
				line = strings.ReplaceAll(line, key, key+" |\n        ")
			}
		}
		lines[i] = line
	}
	return []string{strings.Join(lines, "\n")}
}

// explicitIndentText replaces every "|" at the end of a line with "|2", an
// explicit indentation indicator, so a block scalar whose content later
// dedents below its first line still parses.
func explicitIndentText(text string) string {
	return strings.ReplaceAll(text, "|\n", "|2\n")
}

// explicitIndent is upstream tactic 2 ("1.5").
func explicitIndent(in input) []string {
	return []string{explicitIndentText(in.text)}
}

// fencedSnippet (upstream tactic 4) extracts the body of a fenced block
// (```, ```yaml or ```yml) whose closing fence ends the text or is followed
// by a double quote. It searches the tactic-2 text first and, only when
// that has no such block, the original answer, whose fences preprocessing
// has not removed.
func fencedSnippet(in input) []string {
	// DESIGN-QUESTION: upstream searches the output of tactic 3, which is
	// not ported (DQ-8); which text should the search use? — chose the
	// tactic-2 text ("|" -> "|2") because tactic 3 starts from it and
	// leaves it unchanged unless a line indented by exactly two spaces
	// contains "}", so this matches upstream on every other input.
	if body, ok := findSnippet(explicitIndentText(in.text)); ok {
		return []string{body}
	}
	if body, ok := findSnippet(in.original); ok {
		return []string{body}
	}
	return nil
}

// findSnippet mirrors upstream's re.search with
// ```[ \t]*(?:(?i:yaml|yml)[ \t]*)?\r?\n([\s\S]*?)```(?=\s*$|"): the
// leftmost opening fence with a shortest body whose closing fence is
// followed only by whitespace or by a double quote.
func findSnippet(s string) (string, bool) {
	for start := 0; ; start++ {
		off := strings.Index(s[start:], fence)
		if off < 0 {
			return "", false
		}
		start += off
		breakAt, ok := fenceOpening(s, start)
		if !ok {
			continue
		}
		bodyStart, _ := newlineAt(s, breakAt)
		for end := bodyStart; ; end++ {
			off := strings.Index(s[end:], fence)
			if off < 0 {
				break
			}
			end += off
			if closingFenceEnds(s[end+len(fence):]) {
				return s[bodyStart:end], true
			}
		}
	}
}

// closingFenceEnds is the snippet pattern's lookahead (?=\s*$|") on the
// text after a closing fence. Python's $ also matches before a final "\n",
// which \s* already covers.
func closingFenceEnds(rest string) bool {
	return strings.HasPrefix(rest, `"`) || strings.TrimFunc(rest, pyIsSpace) == ""
}

// stripBraces (upstream tactic 5) removes one leading "{" and one trailing
// "}" and then trailing ":" and newlines, for an answer wrapped like JSON.
func stripBraces(in input) []string {
	s := pyStrip(in.text)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	return []string{strings.TrimRight(s, ":\n")}
}

// keyWindow (upstream tactic 6) parses the window from the first
// "<First>:" to the first blank line after the last "<Last>:" (or the end
// of the text), without a trailing ```yaml/```yml line and surrounding
// backticks. This rescues YAML embedded in prose.
//
// Upstream's index arithmetic is kept: when "<First>:" does not occur, the
// window starts at the text's last character (Python's index -1), and when
// "<Last>:" does not occur, it ends at the end of the text.
func keyWindow(in input) []string {
	first, last := in.keys.First, in.keys.Last
	if first == "" || last == "" {
		return nil
	}
	s := in.text
	start := strings.Index(s, "\n"+first+":")
	if start < 0 {
		start = strings.Index(s, first+":")
	}
	if start < 0 {
		start = lastRuneStart(s)
	}
	end := len(s)
	if lastAt := strings.LastIndex(s, last+":"); lastAt >= 0 {
		if off := strings.Index(s[lastAt:], "\n\n"); off >= 0 {
			end = lastAt + off
		}
	}
	window := ""
	if start < end {
		window = pyStrip(s[start:end])
	}
	for _, f := range []string{"\n```yaml", "\n```yml"} {
		if hasSuffixFoldASCII(window, f) {
			window = window[:len(window)-len(f)]
			break
		}
	}
	window = pyStrip(strings.Trim(window, "`"))
	if window == "" {
		return nil
	}
	return []string{window}
}

// stripPlus (upstream tactic 7) replaces a line-leading "+" with a space:
// models echo diff markers into code blocks.
func stripPlus(in input) []string {
	lines := strings.Split(in.text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "+") {
			lines[i] = " " + line[1:]
		}
	}
	return []string{strings.Join(lines, "\n")}
}

// diffMarkers (upstream tactic 8, "5.5") applies the "+" fix of tactic 7
// and also normalizes line-leading "-" markers. A "- " line whose next
// character is not a space, tab, "+" or "-" is kept as a YAML list item;
// on any other "-" line the leading run of "+" and "-" is removed and a
// space is put in front when the rest does not start with a space or tab,
// so a removed diff line inside a block scalar no longer ends it. It
// applies only when it changed a line.
func diffMarkers(in input) []string {
	lines := strings.Split(in.text, "\n")
	modified := false
	for i, line := range lines {
		if strings.HasPrefix(line, "+") {
			lines[i] = " " + line[1:]
			modified = true
		}
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "-") {
			continue
		}
		rest := line[1:]
		if strings.HasPrefix(line, "- ") && len(rest) > 1 && !strings.ContainsRune(" \t+-", rune(rest[1])) {
			continue // a real list item
		}
		cleaned := strings.TrimLeft(rest, "+-")
		if cleaned != "" && cleaned[0] != ' ' && cleaned[0] != '\t' {
			cleaned = " " + cleaned
		}
		if cleaned != line {
			lines[i] = cleaned
			modified = true
		}
	}
	if !modified {
		return nil
	}
	return []string{strings.Join(lines, "\n")}
}

// tabsToSpaces (upstream tactic 9) replaces every tab with four spaces when
// the text contains a tab.
func tabsToSpaces(in input) []string {
	if !strings.Contains(in.text, "\t") {
		return nil
	}
	return []string{strings.ReplaceAll(in.text, "\t", "    ")}
}

// rootPipe (upstream tactic 11) removes leading "|" and newline characters,
// a stray block-scalar pipe before the root mapping.
func rootPipe(in input) []string {
	return []string{strings.TrimLeft(in.text, "|\n")}
}

// reencode (upstream tactic 12) repairs mojibake: text that was UTF-8 but
// was decoded as Latin-1 somewhere is encoded back to Latin-1 bytes and
// decoded as UTF-8. It does not apply when a character is outside Latin-1
// or the bytes are not valid UTF-8.
//
// Upstream then tries the same with UTF-16. Python's UTF-16 encoder writes
// a byte-order mark (0xFF 0xFE) first, and 0xFF never occurs in valid
// UTF-8, so that attempt can never succeed and is not ported.
func reencode(in input) []string {
	b := make([]byte, 0, len(in.text))
	for _, r := range in.text {
		if r > 0xFF {
			return nil // includes utf8.RuneError for invalid input bytes
		}
		b = append(b, byte(r)) //nolint:gosec // r <= 0xFF is checked above
	}
	if !utf8.Valid(b) {
		return nil
	}
	return []string{string(b)}
}
