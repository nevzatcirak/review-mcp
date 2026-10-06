package patch

import (
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// HandleDeletions mirrors upstream handle_patch_deletions for the compressed
// path (spec §3.3). It is applied to the unextended hunks.
//
//   - A deleted file with no head content reports deleted = true and no
//     hunks: the caller lists only its name under DeletedFilesHeader.
//   - Otherwise deletion-only hunks are dropped (OmitDeletionHunks). As
//     upstream does, the original hunks are kept unchanged when nothing
//     would remain, that is when every hunk is deletion-only.
func HandleDeletions(t provider.ChangeType, head *string, hunks []Hunk) (kept []Hunk, deleted bool) {
	if t == provider.ChangeDeleted && (head == nil || *head == "") {
		return nil, true
	}
	omitted := OmitDeletionHunks(hunks)
	if text := patchText(omitted); text != "" && text != patchText(hunks) {
		return omitted, false
	}
	return cloneHunks(hunks), false
}

// OmitDeletionHunks mirrors upstream omit_deletion_hunks: it keeps the
// hunks that contain at least one '+' line. The result is in upstream's
// joined form: line endings are dropped from the serialized patch.
//
// Upstream quirks reproduced: a malformed "@@" line is dropped and the lines
// after it count as part of the preceding valid hunk (or of the first valid
// hunk, when no valid hunk precedes it); with no valid hunk at all the
// result is empty.
func OmitDeletionHunks(hunks []Hunk) []Hunk {
	if !pyLineSafe(hunks) {
		// Parity decision (lead; architect may override on PR #4): same ambiguity as in Extend (a Python line
		// inside a patch line that starts with "@@") — chose to omit
		// nothing: the result serializes like the input, so
		// HandleDeletions keeps the patch.
		return cloneHunks(hunks)
	}
	var out, group []Hunk
	hasAdd, inside := false, false
	for _, h := range hunks {
		c := cloneHunks([]Hunk{h})[0]
		c.form = formCompact
		if !h.malformed {
			if inside {
				if hasAdd {
					out = append(out, group...)
				}
				group, hasAdd = nil, false
			}
			inside = true
		}
		group = append(group, c)
		// The first Python line of the header is the "@@" line itself; any
		// further Python lines of it count like content lines.
		pieces := headerPieces(h)[1:]
		for _, l := range h.Lines {
			pieces = append(pieces, linePieces(l)...)
		}
		for _, p := range pieces {
			if p != "" && p[0] == '+' {
				hasAdd = true
			}
		}
	}
	if inside && hasAdd {
		out = append(out, group...)
	}
	return out
}
