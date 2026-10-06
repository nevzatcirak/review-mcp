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
//
// A patch line containing \f, \v, ... is one line here (D5); upstream
// re-splits it, so a piece such as "a\f@@ -1 +1 @@" was a header there.
func OmitDeletionHunks(hunks []Hunk) []Hunk {
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
		// Lines are split at "\n" only (architect decision D5, PR #4), so
		// a patch line is one line and only its op can mark an addition.
		for _, l := range h.Lines {
			if l.Op == '+' {
				hasAdd = true
			}
		}
	}
	if inside && hasAdd {
		out = append(out, group...)
	}
	return out
}
