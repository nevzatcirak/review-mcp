package patch

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// maxExtraLines is upstream's MAX_EXTRA_LINES (pr_processing.py).
const maxExtraLines = 10

// ExtendFile is the per-file entry point of the static context extension
// for the fast path. It applies the upstream extend_patch preconditions that
// need the file, then calls Extend:
//   - no extension when the path ends with one of d.SkipExtendExtensions
//     (upstream should_skip_patch: a case-sensitive suffix match on the new
//     path);
//   - a deleted file is extended against an empty head, exactly as upstream
//     extends it (its head_file is "");
//   - otherwise base and head are fp.BaseContent and fp.HeadContent, and a
//     nil side (not fetched) disables the extension (Extend).
func ExtendFile(fp provider.FilePatch, hunks []Hunk, d config.Diff) []Hunk {
	for _, ext := range d.SkipExtendExtensions {
		if ext != "" && strings.HasSuffix(fp.Path, ext) {
			return cloneHunks(hunks)
		}
	}
	head := fp.HeadContent
	if fp.Type == provider.ChangeDeleted {
		empty := ""
		head = &empty
	}
	return Extend(hunks, fp.BaseContent, head, d.ExtraLinesBefore, d.ExtraLinesAfter)
}

// Extend widens every hunk by up to before/after lines of unchanged context,
// with upstream extend_patch semantics and dynamic context off (DQ-1 — a
// deliberate deviation: upstream enables dynamic context by default).
//
// The result is never an alias of hunks. The hunks come back unchanged when:
// before and after are both 0; base is nil or empty (upstream: no original
// file); or head is nil (not fetched, so the pre-context check cannot run —
// spec §3.2).
//
// Lines are split at "\n" only (architect decision D5, PR #4; see pystr.go):
// a line containing \f, \v, \x85, U+2028, ... is one line, as in the file.
//
// A non-nil empty head extends without the pre-context check, as upstream
// does for an empty head file. before and after are clamped to 0..10.
//
// Otherwise every hunk is rewritten the way process_patch_lines does it,
// including its quirks:
//   - every valid hunk's header is rewritten to "@@ -a,b +c,d @@ <section>"
//     (with the space even when the section is empty), also when the hunk
//     is not extended;
//   - a hunk whose first context line does not match the base file at the
//     header's start line is not extended at all (check_if_hunk_lines_
//     matches_to_file);
//   - the section text is dropped when it appears in the added pre-context;
//   - extensions are never merged: overlapping context is repeated in both
//     hunks;
//   - the after-context of a valid hunk is appended even when its
//     pre-context was rejected, and after any malformed pseudo-hunk that
//     follows it.
func Extend(hunks []Hunk, base, head *string, before, after int) []Hunk {
	before, after = clampExtra(before), clampExtra(after)
	if len(hunks) == 0 || (before == 0 && after == 0) || base == nil || *base == "" || head == nil {
		return cloneHunks(hunks)
	}
	// Base and head are split at "\n" only (architect decision D5, PR #4;
	// see pystr.go), so a base line number is the file's real line number.
	// Upstream splits with str.splitlines, which also breaks at \f, \v, ...
	// and so re-splits such patch lines too; a piece such as "a\f@@ -1 +1 @@"
	// then became a hunk header upstream. With "\n"-only splitting that line
	// is plain content and the patch is extended normally.
	orig, origKeep := splitLinesKeep(*base)
	var newLines []string
	if *head != "" {
		newLines = splitLines(*head)
	}
	x := extender{orig: orig, origKeep: origKeep, newLines: newLines, before: before, after: after}
	return x.run(hunks)
}

func clampExtra(v int) int {
	return max(0, min(v, maxExtraLines))
}

type extender struct {
	orig, origKeep []string
	newLines       []string
	before, after  int
}

// run mirrors process_patch_lines with allow_dynamic_context off.
func (x *extender) run(hunks []Hunk) []Hunk {
	out := make([]Hunk, 0, len(hunks))
	valid := true
	start1, size1 := -1, -1
	for k, h := range hunks {
		if h.malformed {
			// A malformed "@@" line passes through as an ordinary line.
			nh := cloneHunks([]Hunk{h})[0]
			nh.form = formExtended
			out = append(out, nh)
			continue
		}
		// Finish the previous valid hunk: its after-context goes after
		// everything that followed it, including malformed pseudo-hunks.
		if valid && start1 != -1 && x.after > 0 {
			x.appendAfter(&out[len(out)-1], start1, size1)
		}
		start1, size1 = h.OldStart, h.OldLen
		valid = x.startMatches(nextLine(hunks, k), h.OldStart)

		e1, ez1, e2, ez2 := h.OldStart, h.OldLen, h.NewStart, h.NewLen
		section := h.Section
		var delta []Line
		if valid {
			e1, ez1, e2, ez2 = x.limits(h)
			deltaOrig := pySlice(x.orig, e1-1, h.OldStart-1)
			lo, hi := pySliceBounds(len(x.orig), e1-1, h.OldStart-1)
			deltaKeep := x.origKeep[lo:hi]
			if len(x.newLines) > 0 {
				deltaNew := pySlice(x.newLines, e2-1, h.NewStart-1)
				if !slices.Equal(deltaOrig, deltaNew) {
					// "Mini match": keep the longest equal suffix.
					found := false
					for i := range deltaOrig {
						if slices.Equal(deltaOrig[i:], pyTail(deltaNew, i)) {
							deltaOrig, deltaKeep = deltaOrig[i:], deltaKeep[i:]
							e1, ez1, e2, ez2 = e1+i, ez1-i, e2+i, ez2-i
							found = true
							break
						}
					}
					if !found {
						e1, ez1, e2, ez2 = h.OldStart, h.OldLen, h.NewStart, h.NewLen
						deltaOrig, deltaKeep = nil, nil
					}
				}
			}
			// Parity decision (lead; architect may override on PR #4): DQ-1 and spec §3.2 say the "@@ … @@ <section>"
			// text is preserved, but process_patch_lines with dynamic context
			// off drops it when it appears in the added pre-context (it is
			// then in-band). Keep or drop? — chose upstream parity (drop),
			// since byte parity is the acceptance criterion; see the
			// section_in_context golden.
			if section != "" {
				for _, l := range deltaOrig {
					if strings.Contains(" "+l, section) {
						section = ""
						break
					}
				}
			}
			for _, l := range deltaKeep {
				delta = append(delta, Line{Op: ' ', Text: l})
			}
		}
		nh := Hunk{
			OldStart: e1, OldLen: ez1, NewStart: e2, NewLen: ez2,
			Section: section,
			header:  fmt.Sprintf(extendedHunkHeaderFormat, e1, ez1, e2, ez2, section),
			form:    formExtended,
		}
		nh.Lines = append(delta, h.Lines...)
		nh.nDelta = len(delta)
		out = append(out, nh)
	}
	if start1 != -1 && x.after > 0 && valid {
		x.appendAfter(&out[len(out)-1], start1, size1)
	}
	return out
}

// limits mirrors _calc_context_limits: the extended ranges, capped so the
// old side never reaches past the end of the base file.
func (x *extender) limits(h Hunk) (e1, ez1, e2, ez2 int) {
	e1 = max(1, h.OldStart-x.before)
	ez1 = h.OldLen + (h.OldStart - e1) + x.after
	e2 = max(1, h.NewStart-x.before)
	ez2 = h.NewLen + (h.NewStart - e2) + x.after
	if over := e1 - 1 + ez1 - len(x.orig); over > 0 {
		ez1 = max(ez1-over, h.OldLen)
		ez2 = max(ez2-over, h.NewLen)
	}
	return e1, ez1, e2, ez2
}

// appendAfter adds the after-context of the hunk with old range
// start1,size1. Slicing stops at the end of the base file.
func (x *extender) appendAfter(h *Hunk, start1, size1 int) {
	from := start1 + size1 - 1
	lo, hi := pySliceBounds(len(x.orig), from, from+x.after)
	for _, l := range x.origKeep[lo:hi] {
		h.Lines = append(h.Lines, Line{Op: ' ', Text: l})
	}
}

// startMatches mirrors check_if_hunk_lines_matches_to_file: when the line
// after the header is a context line, it must equal (ignoring surrounding
// whitespace) the base file's line at the header's old start. Any failure
// of the check itself (an empty next line, a start past EOF) makes the hunk
// invalid. The upstream retry with other encodings only changes logging:
// it, too, reports the hunk invalid.
func (x *extender) startMatches(next *string, start1 int) bool {
	if next == nil {
		return true
	}
	if *next == "" {
		return false
	}
	if (*next)[0] != ' ' {
		return true
	}
	idx, ok := pyIndex(len(x.orig), start1-1)
	if !ok {
		return false
	}
	return pyStrip(*next) == pyStrip(x.orig[idx])
}

// nextLine returns the line that follows hunk k's header in the serialized
// patch, or nil at the end of the patch.
func nextLine(hunks []Hunk, k int) *string {
	var s string
	switch {
	case len(hunks[k].Lines) > 0:
		s = lineText(hunks[k].Lines[0])
	case k+1 < len(hunks):
		s = headerText(hunks[k+1])
	default:
		return nil
	}
	return &s
}
