package llmrun

import (
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Coverage accounts for every changed file (X-3): the files whose diff the
// model saw (Included, Clipped), the files left out for budget (Omitted),
// the files skipped for other reasons (Skipped) and the filtered files
// (Filtered, with the filter's reason).
type Coverage struct {
	Included []string      `json:"included"`
	Clipped  []string      `json:"clipped"`
	Omitted  OmittedFiles  `json:"omitted"`
	Skipped  []SkippedFile `json:"skipped"`
	Filtered []SkippedFile `json:"filtered"`

	// Partial and the three counts below are the X-18 summary of the lists
	// above; Finalize sets them and Tally is their single definition.
	// Partial is true when at least one reviewable changed file was not
	// fully reviewed. ReviewedFiles + NotReviewedFiles == TotalFiles.
	Partial          bool `json:"partial"`
	ReviewedFiles    int  `json:"reviewed_files"`
	TotalFiles       int  `json:"total_files"`
	NotReviewedFiles int  `json:"not_reviewed_files"`
}

// Tally is the X-18 summary of a Coverage. Every changed file is in exactly
// one of these places, and the mapping of the existing categories is:
//
//   - Included: reviewed.
//   - Clipped: NOT reviewed. The model saw only part of the file, so
//     nothing may be concluded about the rest of it.
//   - Omitted (added, modified, deleted): NOT reviewed. Deleted files count
//     too: X-3 reports them as left out to fit the context window, the
//     coverage section lists them next to the others, and on the compressed
//     path the model sees only their names. A banner that left them out
//     would disagree with the section it points to.
//   - Skipped for size (file_limit, size_limit), unreadable (fetch_failed,
//     unparseable_patch) or for any reason not named here: NOT reviewed. A
//     reviewable file was lost; an unknown reason counts as lost, never as
//     fine.
//   - Skipped as binary or as an empty diff (a pure rename, a mode change):
//     outside the count, like Filtered. There is no text change to review,
//     so nothing was lost; the coverage section still lists them.
//   - Filtered (ignore rules, generated files): outside the count. They
//     were excluded on purpose, and X-3 lists them with the rule.
//
// Providers truncate nothing: a file too large for a provider is skipped
// whole (size_limit), which is counted above.
type Tally struct {
	Partial                           bool
	Reviewed, NotReviewed, TotalFiles int
}

// Tally computes the X-18 summary from the file lists.
func (c *Coverage) Tally() Tally {
	t := Tally{
		Reviewed: len(c.Included),
		NotReviewed: len(c.Clipped) + len(c.Omitted.Added) + len(c.Omitted.Modified) +
			len(c.Omitted.Deleted),
	}
	for _, s := range c.Skipped {
		if skipLosesReviewableFile(s.Reason) {
			t.NotReviewed++
		}
	}
	t.TotalFiles = t.Reviewed + t.NotReviewed
	t.Partial = t.NotReviewed > 0
	return t
}

// skipLosesReviewableFile reports whether a skip reason means a file with
// reviewable changes was lost (see Tally). Unknown reasons count as lost.
func skipLosesReviewableFile(reason string) bool {
	switch reason {
	case provider.SkipBinary, diffpipe.SkipEmptyDiff:
		return false
	}
	return true
}

// Finalize sets Partial and the counts from the lists. BuildCoverage and
// TrimCoverage call it; nothing else changes the lists.
func (c *Coverage) Finalize() {
	t := c.Tally()
	c.Partial, c.ReviewedFiles, c.TotalFiles, c.NotReviewedFiles = t.Partial, t.Reviewed, t.TotalFiles, t.NotReviewed
}

// OmittedFiles are the files left out of the diff for budget, by change
// type (renamed files count as modified).
type OmittedFiles struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Deleted  []string `json:"deleted"`
}

// SkippedFile is a file left out for a reason other than budget.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// NonNil returns s, or an empty non-nil slice for nil (JSON arrays, never
// null).
func NonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// BuildCoverage combines the prepared diff's accounting with the
// provider's skips; filtered files get the filter's reason (as diag diff
// reports them).
func BuildCoverage(p *diffpipe.Prepared, f *filter.Filter) Coverage {
	c := Coverage{
		Included: NonNil(append([]string(nil), p.Included...)),
		Clipped:  NonNil(append([]string(nil), p.Clipped...)),
		Omitted: OmittedFiles{
			Added:    NonNil(append([]string(nil), p.Omitted.Added...)),
			Modified: NonNil(append([]string(nil), p.Omitted.Modified...)),
			Deleted:  NonNil(append([]string(nil), p.Omitted.Deleted...)),
		},
		Skipped:  []SkippedFile{},
		Filtered: []SkippedFile{},
	}
	for _, s := range p.Skipped {
		if s.Reason != provider.SkipFiltered {
			c.Skipped = append(c.Skipped, SkippedFile{Path: s.Path, Reason: s.Reason})
			continue
		}
		reason := provider.SkipFiltered
		if f != nil {
			if included, why := f.Explain(s.Path); !included && why != "" {
				reason = why
			}
		}
		c.Filtered = append(c.Filtered, SkippedFile{Path: s.Path, Reason: reason})
	}
	c.Finalize()
	return c
}

// TrimCoverage moves the files the guard cut out of the diff: a file whose
// content is entirely in the first kept lines stays where it was, a file
// that was cut becomes Clipped, a file that is gone moves to Omitted by its
// change type. A file whose header cannot be found is reported as Clipped
// (its content may be incomplete), never as complete.
func TrimCoverage(c *Coverage, diff string, kept int, types map[string]provider.ChangeType) {
	defer c.Finalize()
	lines := strings.Split(diff, "\n")
	order := append(slices.Clone(c.Included), c.Clipped...)
	wasClipped := map[string]bool{}
	for _, p := range c.Clipped {
		wasClipped[p] = true
	}
	starts := make([]int, len(order))
	cur, lost := 0, len(order)
	for i, p := range order {
		starts[i] = -1
		hdrs := patch.FileHeaderLines(p)
		for j := cur; j < len(lines); j++ {
			if slices.Contains(hdrs, lines[j]) {
				starts[i], cur = j, j+1
				break
			}
		}
		if starts[i] < 0 {
			lost = i
			break
		}
	}
	bodyEnd := len(lines)
	sections := []string{
		strings.TrimSuffix(patch.AddedFilesHeader, "\n"),
		strings.TrimSuffix(patch.ModifiedFilesHeader, "\n"),
		strings.TrimSuffix(patch.DeletedFilesHeader, "\n"),
	}
	if lost > 0 {
		for j := starts[lost-1] + 1; j < len(lines); j++ {
			if slices.Contains(sections, lines[j]) {
				bodyEnd = j
				break
			}
		}
	}

	c.Included, c.Clipped = []string{}, []string{}
	for i, p := range order {
		if i >= lost {
			c.Clipped = append(c.Clipped, p)
			continue
		}
		end := bodyEnd
		if i+1 < lost {
			end = starts[i+1]
		}
		// The file's content ends at its last non-blank line.
		for end > starts[i]+1 && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		switch {
		case kept >= end:
			if wasClipped[p] {
				c.Clipped = append(c.Clipped, p)
			} else {
				c.Included = append(c.Included, p)
			}
		case kept > starts[i]:
			c.Clipped = append(c.Clipped, p)
		default:
			switch types[p] {
			case provider.ChangeAdded:
				c.Omitted.Added = append(c.Omitted.Added, p)
			case provider.ChangeDeleted:
				c.Omitted.Deleted = append(c.Omitted.Deleted, p)
			default:
				c.Omitted.Modified = append(c.Omitted.Modified, p)
			}
		}
	}
}
