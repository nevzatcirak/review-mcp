package llmrun

import (
	"errors"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// The parts of a tool run in parts (X-19), shared by pr_review and
// pr_describe: the coverage over the parts and the fixed failure class of a
// part's note.

// PartsCoverage is the coverage of a run in parts (X-19 honesty): the parts'
// files in part order, then the files no part covered. covs[i] holds part
// i's own files after the request-size guard (Included, Clipped,
// DeletedListed, and in Omitted only the files the guard cut). failed marks
// the parts whose model call failed (nil: none); their files are Skipped
// with reason SkipModelCallFailed. Skipped holds the provider's and the
// renderer's skips first, then the files too large for a part of their own
// (too_large), then the files of failed parts. ModelCalls is len(covs) and
// FailedParts the failed count; RepoContext is left off for the caller.
func PartsCoverage(ch *diffpipe.Chunks, covs []Coverage, failed []bool, f *filter.Filter) Coverage {
	c := BuildCoverage(&diffpipe.Prepared{Skipped: ch.Skipped}, f)
	var lost []SkippedFile
	for i, pc := range covs {
		if failed != nil && failed[i] {
			for _, l := range [][]string{pc.Included, pc.Clipped, pc.DeletedListed} {
				for _, p := range l {
					lost = append(lost, SkippedFile{Path: p, Reason: SkipModelCallFailed})
				}
			}
		} else {
			c.Included = append(c.Included, pc.Included...)
			c.Clipped = append(c.Clipped, pc.Clipped...)
			c.DeletedListed = append(c.DeletedListed, pc.DeletedListed...)
		}
		c.Omitted.Added = append(c.Omitted.Added, pc.Omitted.Added...)
		c.Omitted.Modified = append(c.Omitted.Modified, pc.Omitted.Modified...)
		c.Omitted.Deleted = append(c.Omitted.Deleted, pc.Omitted.Deleted...)
	}
	c.Omitted.Added = append(c.Omitted.Added, ch.Omitted.Added...)
	c.Omitted.Modified = append(c.Omitted.Modified, ch.Omitted.Modified...)
	c.Omitted.Deleted = append(c.Omitted.Deleted, ch.Omitted.Deleted...)
	for _, p := range ch.TooLarge {
		c.Skipped = append(c.Skipped, SkippedFile{Path: p, Reason: diffpipe.SkipTooLarge})
	}
	c.Skipped = append(c.Skipped, lost...)
	c.ModelCalls = len(covs)
	for _, fl := range failed {
		if fl {
			c.FailedParts++
		}
	}
	c.Finalize()
	return c
}

// FailureClass is the fixed class of a part's error for its note: the class
// of a classified pipeline or LLM error, else "unclassified". It never
// carries error text (X-6).
func FailureClass(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return string(pe.Class)
	}
	if c, ok := llm.ClassOf(err); ok {
		return string(c)
	}
	var prov *provider.Error
	if errors.As(err, &prov) {
		return string(prov.Class)
	}
	return "unclassified"
}

// LeavesFilesOut reports whether a prepared diff left files for a further
// part: the budget omitted them. A clipped file is not given to a further
// part (diffpipe.PrepareChunks).
func LeavesFilesOut(p *diffpipe.Prepared) bool {
	return len(p.Omitted.Added)+len(p.Omitted.Modified)+len(p.Omitted.Deleted) > 0
}
