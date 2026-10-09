package improve

import (
	"context"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// readDiscussion reads the PR's threads and renders the discussion block
// of the suggestion prompt (X-13) within maxTokens (0 or less: no block,
// and nothing is read). Any read error returns an empty discussion and
// NoteDiscussionUnreadable: the discussion is context, never a reason to
// fail the run. Without the token's own user our comments cannot be told
// from other people's, so a failed CurrentUser is treated as a failed read.
//
// The comments left out are the token's own user's comments whose last
// line is a marker of any review-mcp tool (llmrun.HasToolMarkerLastLine):
// pr_review's overview and findings, pr_describe's comment, and later
// pr_improve's own. pr_review leaves out only its own two markers.
//
// DESIGN-QUESTION: are pr_review's findings, posted by the same token, part
// of pr_improve's discussion, so that a point the review already raised is
// not suggested again? — chose no: the block's header says the threads are
// "written by people" (X-13), the bot's own comments are left out of it
// for pr_review too, and P3 of the acceptance is about a point "a reviewer
// already raised"; human replies to the bot's comments stay in, as in
// pr_review.
func (pl *Plan) readDiscussion(ctx context.Context, maxTokens int, factor float64) (llmrun.Discussion, []string) {
	if maxTokens <= 0 {
		return llmrun.Discussion{}, nil
	}
	threads, err := pl.p.ListThreads(ctx, pl.ref)
	var me provider.User
	if err == nil {
		me, err = pl.p.CurrentUser(ctx)
	}
	if err != nil {
		pl.log.Debug("improve: discussion not read", "class", llmrun.FailureClass(err))
		return llmrun.Discussion{}, []string{NoteDiscussionUnreadable}
	}
	own := func(c *provider.CommentItem) bool {
		return provider.IsUser(me, c.AuthorID, c.AuthorLogin) && llmrun.HasToolMarkerLastLine(c.Body)
	}
	d := llmrun.RenderDiscussion(DiscussionHeader, llmrun.HumanThreads(threads, own), maxTokens, factor)
	pl.log.Debug("improve: discussion read", "threads", len(threads), "shown", d.Included, "omitted", d.Omitted)
	return d, nil
}
