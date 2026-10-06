// Package anchor places a finding's line range on the diff the code host
// shows (spec P7 §3.1). It is pure: no I/O.
//
// Anchors are resolved against the provider's own hunks, FilePatch.Patch as
// GetDiff returned it, never against the extended context the review prompt
// adds (patch.Extend): a server accepts inline comments only on lines of
// its own diff. The pipeline never rewrites FilePatch.Patch; the context
// extension works on copies of the parsed hunks.
package anchor

import (
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Anchor is the place of an inline comment: one new-side line of a hunk.
type Anchor struct {
	// Path is the file's new path.
	Path string
	// OldPath is the old path of a renamed file, and "" otherwise.
	OldPath string
	// Line is the absolute new-side line number.
	Line int
	// LineType is LineAdded for a "+" line and LineContext for an unchanged
	// line of the hunk.
	LineType provider.LineType
}

// Comment returns the inline comment with body at the anchor.
func (a Anchor) Comment(body string) provider.InlineComment {
	return provider.InlineComment{Path: a.Path, OldPath: a.OldPath, Line: a.Line, LineType: a.LineType, Body: body}
}

// Resolve returns the anchor of the new-side line range start to end of
// file: the first line of the range that is a new-side line (added or
// context) of one of the file's hunks. An end before start means start.
//
// It reports false, with a zero Anchor, when the file is nil, deleted or
// binary, when start is not positive, when the patch cannot be parsed, or
// when no line of the range is visible on the new side of a hunk.
// Malformed pseudo-hunks and "\ No newline at end of file" lines are never
// anchors.
func Resolve(file *provider.FilePatch, start, end int) (Anchor, bool) {
	if file == nil || file.Binary || file.Type == provider.ChangeDeleted || start <= 0 {
		return Anchor{}, false
	}
	end = max(end, start)
	hunks, err := patch.ParseHunks(file.Patch)
	if err != nil {
		return Anchor{}, false
	}
	line, typ := 0, provider.LineType("")
	for _, h := range hunks {
		if h.Malformed() {
			continue
		}
		n := h.NewStart
		for _, l := range h.Lines {
			if l.Op != '+' && l.Op != ' ' {
				continue
			}
			if n >= start && n <= end && (line == 0 || n < line) {
				line, typ = n, provider.LineContext
				if l.Op == '+' {
					typ = provider.LineAdded
				}
			}
			n++
		}
	}
	if line == 0 {
		return Anchor{}, false
	}
	a := Anchor{Path: file.Path, Line: line, LineType: typ}
	if file.Type == provider.ChangeRenamed {
		a.OldPath = file.OldPath
	}
	return a, true
}
