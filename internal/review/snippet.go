package review

import (
	"fmt"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// MaxSnippetLines caps a finding's snippet (DQ-12).
const MaxSnippetLines = 30

// Snippet notes (DQ-12).
const (
	// SnippetNoteUnverified: the range could not be resolved; the finding
	// is kept without a snippet.
	SnippetNoteUnverified = "lines could not be verified against the diff"
)

// snippetNoteCut is the note of a snippet cut at MaxSnippetLines.
var snippetNoteCut = fmt.Sprintf("snippet shortened to the first %d lines of the range", MaxSnippetLines)

// snippet returns the lines start to end (1-based, inclusive) of fp's new
// side (DQ-12): from the head content when it was fetched completely,
// otherwise from a walk over the patch's hunks that must resolve every line
// of the range. An unresolvable range returns no text and
// SnippetNoteUnverified. A range longer than MaxSnippetLines is cut, with a
// note. An end of 0 means start; line endings are dropped.
func snippet(fp *provider.FilePatch, start, end int) (text, note string) {
	if end == 0 {
		end = start
	}
	if fp == nil || start <= 0 || end < start {
		return "", SnippetNoteUnverified
	}
	var lines []string
	var ok bool
	if fp.HeadStatus == provider.ContentFull && fp.HeadContent != nil {
		lines, ok = llmrun.HeadLines(*fp.HeadContent, start, end)
	} else {
		lines, ok = llmrun.PatchLines(fp.Patch, start, end)
	}
	if !ok {
		return "", SnippetNoteUnverified
	}
	if len(lines) > MaxSnippetLines {
		lines, note = lines[:MaxSnippetLines], snippetNoteCut
	}
	return strings.Join(lines, "\n"), note
}
