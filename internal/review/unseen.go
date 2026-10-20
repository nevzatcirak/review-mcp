package review

import (
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/related"
)

// markUnseen marks the paths in the text of the run's findings that the
// review never saw (v2.5 spec V-13b §6 item 1, WP-13f): an inline code span
// of a finding's title or body that looks like a repository path
// (llmrun.PathSpans) and names no file the prompts showed the model. Such
// a finding gets UnverifiedPaths and one last line naming them
// (llmrun.UnseenLine); nothing else in its text changes. A finding without
// such a path is left as it is, so a run whose text has none is unchanged.
//
// Seen are the files of pl.seen (the primary diff, the placed
// repository-context hits and the head commit's files the repository tools
// showed the model), the guidelines file and the files of the file tree as
// sent, and the files of the placed related sections, each under its
// label. It runs on the run's own findings only, before the carried ones
// are appended: those come from an earlier review and no model saw them in
// this run.
func (pl *Plan) markUnseen(rev *Review) {
	seen := &pl.seen
	seen.Add(pl.proj.SeenFiles()...)
	related.AddSeen(seen, pl.relatedShown)
	marked := 0
	for i := range rev.KeyIssuesToReview {
		ki := &rev.KeyIssuesToReview[i]
		paths := seen.Unseen(ki.IssueHeader, ki.IssueContent)
		if len(paths) == 0 {
			continue
		}
		ki.UnverifiedPaths = paths
		ki.IssueContent = llmrun.WithUnseen(ki.IssueContent, paths)
		marked++
	}
	pl.log.Debug("review: unseen paths", "findings", len(rev.KeyIssuesToReview), "marked", marked)
}
