package bitbucketserver

import (
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// contextLines is the number of unchanged lines around each hunk.
const contextLines = 3

// splitLines splits s after every "\n", keeping the line endings, so a CRLF
// line keeps its "\r\n". An empty string yields no lines.
//
// difflib.SplitLines is deliberately not used: it appends "\n" to the last
// element, which for text that already ends in "\n" invents an extra empty
// line and would add a spurious "+" or "-" line to every patch.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// withTrailingNewline appends "\n" to a non-empty string that lacks one.
func withTrailingNewline(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}

// makePatch builds the hunk-only unified diff (3 context lines) between the
// two file contents. ok is false when there is nothing to report: both sides
// are empty, or they are identical after trailing-newline normalization.
//
// Upstream parity: a missing final newline is added to a non-empty side
// before diffing, so a "\ No newline at end of file" line is never emitted.
func makePatch(base, head string) (patch string, ok bool, err error) {
	if base == "" && head == "" {
		return "", false, nil
	}
	base, head = withTrailingNewline(base), withTrailingNewline(head)
	if base == head {
		return "", false, nil
	}
	var sb strings.Builder
	// No FromFile/ToFile: go-difflib then writes no ---/+++ header lines,
	// so the output already starts at the first "@@".
	err = difflib.WriteUnifiedDiff(&sb, difflib.UnifiedDiff{
		A:       splitLines(base),
		B:       splitLines(head),
		Context: contextLines,
	})
	if err != nil {
		return "", false, err
	}
	patch = sb.String()
	if !strings.HasPrefix(patch, "@@") {
		return "", false, nil
	}
	return patch, true, nil
}

// countChanges counts the added and removed lines of a hunk-only patch.
// Because a hunk-only patch has no ---/+++ header lines, every line that
// starts with "+" or "-" is content, including removed lines such as
// "-- comment" (patch line "--- comment").
func countChanges(patch string) (additions, deletions int) {
	for _, line := range splitLines(patch) {
		switch line[0] {
		case '+':
			additions++
		case '-':
			deletions++
		}
	}
	return additions, deletions
}
