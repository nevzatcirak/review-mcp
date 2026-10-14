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

// Anchor is the place of an inline comment: one new-side line of a hunk,
// or a run of them.
type Anchor struct {
	// Path is the file's new path.
	Path string
	// OldPath is the old path of a renamed file, and "" otherwise.
	OldPath string
	// Line is the absolute new-side line number.
	Line int
	// EndLine is the last line of the range the comment covers, from Line
	// on, when that is more than one line; 0 otherwise. Every line from
	// Line to EndLine is a new-side line of Line's hunk.
	EndLine int
	// LineType is LineAdded for a "+" line and LineContext for an unchanged
	// line of the hunk.
	LineType provider.LineType
	// OldLine is the old-side line number of Line when it is a context
	// line, and 0 for an added line (provider.InlineComment.OldLine).
	OldLine int
}

// Comment returns the inline comment with body at the anchor.
func (a Anchor) Comment(body string) provider.InlineComment {
	return provider.InlineComment{Path: a.Path, OldPath: a.OldPath, Line: a.Line, EndLine: a.EndLine,
		LineType: a.LineType, OldLine: a.OldLine, Body: body}
}

// Resolve returns the anchor of the new-side line range start to end of
// file: the first line of the range that is a new-side line (added or
// context) of one of the file's hunks. An end before start means start.
// EndLine extends the anchor over the following lines of the range for as
// long as they are new-side lines of the same hunk: a provider that can
// anchor a comment on a range (InlineComment.EndLine) covers them, the
// others ignore it.
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
	line, last, typ, old := 0, 0, provider.LineType(""), 0
	for _, h := range hunks {
		if h.Malformed() {
			continue
		}
		// The new-side lines of a hunk are numbered without gaps, so its
		// lines inside the range run from first to run.
		n, first, run := h.NewStart, 0, 0
		for _, l := range h.Lines {
			if l.Op != '+' && l.Op != ' ' {
				continue
			}
			if n >= start && n <= end {
				if first == 0 {
					first = n
				}
				run = n
				if line == 0 || n < line {
					line = n
					typ, old = lineTypeAt(h, n, l.Op)
				}
			}
			n++
		}
		if first != 0 && first == line {
			last = run
		}
	}
	if line == 0 {
		return Anchor{}, false
	}
	a := Anchor{Path: file.Path, Line: line, LineType: typ, OldLine: old}
	if last > line {
		a.EndLine = last
	}
	if file.Type == provider.ChangeRenamed {
		a.OldPath = file.OldPath
	}
	return a, true
}

// ResolveExact returns the anchor of the head-side range start to end of
// file, for a caller that replaces exactly those lines (a suggestion block):
// it succeeds only when every line of the range is a new-side line (added or
// context) of one single hunk. The anchor is at start and, for a range of
// several lines, covers it to end (EndLine). It reports false, with a zero
// Anchor, when the file is nil, deleted or binary, when the range is not
// positive or ends before it starts, when the patch cannot be parsed, and
// when the range leaves a hunk or reaches into the gap between two hunks.
// Malformed pseudo-hunks are never anchors.
func ResolveExact(file *provider.FilePatch, start, end int) (Anchor, bool) {
	if file == nil || file.Binary || file.Type == provider.ChangeDeleted || start < 1 || end < start {
		return Anchor{}, false
	}
	hunks, err := patch.ParseHunks(file.Patch)
	if err != nil {
		return Anchor{}, false
	}
	for _, h := range hunks {
		if h.Malformed() {
			continue
		}
		covered, typ, old := 0, provider.LineType(""), 0
		n := h.NewStart
		for _, l := range h.Lines {
			if l.Op != '+' && l.Op != ' ' {
				continue
			}
			if n >= start && n <= end {
				covered++
				if n == start {
					typ, old = lineTypeAt(h, n, l.Op)
				}
			}
			n++
		}
		if covered != end-start+1 {
			continue
		}
		a := Anchor{Path: file.Path, Line: start, LineType: typ, OldLine: old}
		if end > start {
			a.EndLine = end
		}
		if file.Type == provider.ChangeRenamed {
			a.OldPath = file.OldPath
		}
		return a, true
	}
	return Anchor{}, false
}

// lineTypeAt returns the line type and the old-side line number of the
// new-side line n of h, whose line has the operation op ('+' or ' '): the one
// place where both resolvers decide them.
func lineTypeAt(h patch.Hunk, n int, op byte) (provider.LineType, int) {
	if op == '+' {
		return provider.LineAdded, 0
	}
	return provider.LineContext, h.OldLineOf(n)
}
