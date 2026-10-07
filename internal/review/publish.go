package review

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review/anchor"
)

// publishFailedMessage is shown for a publish error that is not a
// classified provider error.
const publishFailedMessage = "the review could not be posted as a PR comment"

// NoteOverviewNotUpdated: the overview could not be edited after the
// inline comments were posted, so it does not link to them (step 13).
const NoteOverviewNotUpdated = "The overview could not be updated after the inline comments were posted; " +
	"it links to the changed lines instead."

// noteUnanchorable: findings with no line in the PR's diff (spec P7 §3.3).
func noteUnanchorable(n int) string {
	return countPhrase(n, "finding could not be placed on a changed line and is",
		"findings could not be placed on a changed line and are") + " listed in the overview only."
}

// noteDuplicates: findings whose inline comment is already on the PR.
func noteDuplicates(n int) string {
	return countPhrase(n, "finding was already posted on this PR and was not repeated.",
		"findings were already posted on this PR and were not repeated.")
}

// noteInlineFailed: anchorable findings whose inline comment failed.
func noteInlineFailed(n int) string {
	return countPhrase(n, "finding could not be posted as an inline comment and is",
		"findings could not be posted as inline comments and are") + " listed in the overview only."
}

// inlineFinding is an anchorable finding and its inline comment.
type inlineFinding struct {
	issue int // index in Review.KeyIssuesToReview
	item  provider.InlineComment
}

// inlineEnabled reports whether a publish posts inline comments: there is a
// renderer and Args.InlineFindings is not false.
func inlineEnabled(deps Deps, args Args) bool {
	return deps.RenderInline != nil && (args.InlineFindings == nil || *args.InlineFindings)
}

// publish runs step 13 when publishing was requested and records the
// outcome in pl.Result. It never fails the review.
//
// Order (spec P7 §3.3, §4.2): the anchors are resolved first, so that the
// overview carries the inline notes; then the overview of an earlier run is
// looked up (persistent overview, on unless Args.PersistentOverview is
// false).
//
//   - None found: the overview is posted before any inline comment, so that
//     a failed inline post never leaves the PR without it; when inline
//     comments were attempted, the overview is then edited once so that
//     each finding links to its inline comment (WP-PR-7c).
//   - One found: the inline comments are posted, then the found overview is
//     edited once, with the links (editOverview).
//
// DESIGN-QUESTION: the spec's order for a found overview is "edit it, post
// the inline comments, edit it again with the links"; may the two edits be
// folded into one? — chose to fold them, because the PR already has an
// overview while the inline comments are posted (the reason the first
// write must be the overview does not apply), one edit is one request and
// one ownership check fewer, and the single edit already carries every
// inline link and note. When that edit fails, the replacement overview is
// posted after the inline comments and also carries the links.
func publish(ctx context.Context, deps Deps, args Args, pl *Plan) {
	if !args.Publish {
		return
	}
	res, log := pl.Result, pl.log
	res.Publish = &PublishResult{}
	var render func(provider.Capabilities) string
	if deps.RenderProvider != nil {
		render = func(caps provider.Capabilities) string {
			return withOverviewMarker(deps.RenderProvider(res, caps))
		}
	}

	inlineOn := inlineEnabled(deps, args)
	var items []inlineFinding
	var sum *InlineSummary
	notesBefore := len(res.Notes)
	if inlineOn {
		items, sum = planInline(res, pl.d, pl.postedFingerprints, deps.RenderInline, pl.p.Capabilities())
		if sum.Unanchorable > 0 {
			res.Notes = append(res.Notes, noteUnanchorable(sum.Unanchorable))
		}
		if sum.SkippedDuplicate > 0 {
			res.Notes = append(res.Notes, noteDuplicates(sum.SkippedDuplicate))
		}
	}

	if args.PersistentOverview == nil || *args.PersistentOverview {
		found, err := pl.findOverview(ctx)
		switch {
		case err != nil:
			res.Notes = append(res.Notes, NoteOverviewLookupFailed)
			log.Debug("review: overview lookup failed", "error", fixedError(err))
		case found != nil:
			if found.older > 0 {
				res.Notes = append(res.Notes, noteOlderOverviews(found.older))
			}
			editOverview(ctx, log, pl, found, items, sum, render)
			return
		}
	}

	postOverview(ctx, log, pl.ref, pl.p, res.Publish, render)
	if !inlineOn {
		return
	}
	if !res.Publish.Published {
		// No inline comments without the overview: it is the only place
		// that lists the unanchorable findings and explains the rest.
		res.Notes = res.Notes[:notesBefore]
		log.Debug("review: inline comments skipped, the overview was not posted", "anchorable", len(items))
		return
	}
	res.Publish.Inline = sum
	if len(items) > 0 {
		postInline(ctx, log, pl, items, sum)
		if sum.Failed > 0 {
			res.Notes = append(res.Notes, noteInlineFailed(sum.Failed))
		}
		updateOverview(ctx, log, pl.ref, pl.p, res, render)
	}
	logInline(log, sum)
}

func logInline(log *slog.Logger, sum *InlineSummary) {
	log.Debug("review: inline findings", "posted", sum.Posted, "failed", sum.Failed,
		"unanchorable", sum.Unanchorable, "skipped_duplicate", sum.SkippedDuplicate)
}

// editOverview publishes over the overview found on the PR: the inline
// comments first (sum is nil when inline findings are off), then one edit
// of the found comment with the links and notes. Any edit error (not_owner,
// a vanished comment, a conflict, any other provider error) posts a new
// overview instead, with NoteOverviewReplaced; the review never fails.
func editOverview(ctx context.Context, log *slog.Logger, pl *Plan, found *foundOverview,
	items []inlineFinding, sum *InlineSummary, render func(provider.Capabilities) string) {
	res := pl.Result
	pub := res.Publish
	if sum != nil {
		pub.Inline = sum
		if len(items) > 0 {
			postInline(ctx, log, pl, items, sum)
			if sum.Failed > 0 {
				res.Notes = append(res.Notes, noteInlineFailed(sum.Failed))
			}
		}
		logInline(log, sum)
	}
	err := errNoRenderer
	if render != nil {
		err = pl.p.EditComment(ctx, pl.ref, found.id, render(pl.p.Capabilities()))
	}
	if err == nil {
		pub.Published, pub.Updated, pub.CommentID, pub.URL = true, true, found.id, found.url
		log.Debug("review: overview updated in place", "older_overviews", found.older)
		return
	}
	log.Debug("review: overview update failed, posting a new one", "error", fixedError(err))
	res.Notes = append(res.Notes, NoteOverviewReplaced)
	postOverview(ctx, log, pl.ref, pl.p, pub, render)
	if !pub.Published {
		// Neither edited nor posted: the note's "a new one was posted" is
		// not true, and the old overview stays as it was.
		res.Notes = res.Notes[:len(res.Notes)-1]
	}
}

// errNoRenderer stands for a missing Deps.RenderProvider in editOverview.
var errNoRenderer = errors.New("review: no provider renderer")

// planInline resolves each finding's anchor on the provider's unextended
// hunks (d.Files[i].Patch; spec P7 §3.1) and renders the inline comment of
// every anchorable finding whose fingerprint is not in posted. The file is
// matched exactly, as for the finding's link. The summary counts the
// unanchorable and duplicate findings; Posted and Failed are left 0.
func planInline(res *Result, d *provider.Diff, posted map[string]bool, render InlineRenderer, caps provider.Capabilities) ([]inlineFinding, *InlineSummary) {
	sum := &InlineSummary{}
	if res.Review == nil {
		return nil, sum
	}
	files := map[string]*provider.FilePatch{}
	if d != nil {
		for i := range d.Files {
			files[d.Files[i].Path] = &d.Files[i]
		}
	}
	var out []inlineFinding
	for n := range res.Review.KeyIssuesToReview {
		ki := &res.Review.KeyIssuesToReview[n]
		a, ok := anchor.Resolve(files[ki.RelevantFile], ki.StartLine, ki.EndLine)
		if !ok {
			sum.Unanchorable++
			ki.InlineStatus = InlineUnanchorable
			continue
		}
		fp := Fingerprint(a.Path, ki.IssueHeader, ki.IssueContent)
		if posted[fp] {
			sum.SkippedDuplicate++
			ki.InlineStatus = InlineSkippedDuplicate
			continue
		}
		body := strings.TrimRight(render(ki, caps), " \t\r\n") + "\n\n" + FingerprintMarker(fp)
		out = append(out, inlineFinding{issue: n, item: a.Comment(body)})
	}
	return out, sum
}

// postOverview posts a new overview comment and records the outcome in out.
func postOverview(ctx context.Context, log *slog.Logger, ref provider.PRRef, p provider.Provider,
	out *PublishResult, render func(provider.Capabilities) string) {
	var r llmrun.PublishResult
	llmrun.PostResult(ctx, log, ref, p, &r, publishFailedMessage, render)
	out.Published, out.CommentID, out.URL, out.Error = r.Published, r.CommentID, r.URL, r.Error
}

// postInline posts the inline comments in one call and counts each item as
// posted or failed; a posted finding gets its InlineURL.
func postInline(ctx context.Context, log *slog.Logger, pl *Plan, items []inlineFinding, sum *InlineSummary) {
	in := make([]provider.InlineComment, len(items))
	for i := range items {
		in[i] = items[i].item
	}
	results, err := pl.p.PostInlineComments(ctx, pl.ref, pl.pr, in)
	if err != nil {
		log.Debug("review: inline comments not posted", "error", fixedError(err))
	}
	for i, it := range items {
		ki := &pl.Result.Review.KeyIssuesToReview[it.issue]
		if err != nil || i >= len(results) || !results[i].Posted {
			sum.Failed++
			ki.InlineStatus = InlineFailed
			continue
		}
		sum.Posted++
		ki.InlineStatus = InlinePosted
		ki.InlineURL = results[i].URL
	}
}

// updateOverview re-renders the overview, now with the inline links and
// notes, and edits the posted comment in place. A failure adds
// NoteOverviewNotUpdated to the result; the posted overview stays as it is.
func updateOverview(ctx context.Context, log *slog.Logger, ref provider.PRRef, p provider.Provider,
	res *Result, render func(provider.Capabilities) string) {
	if render == nil || res.Publish.CommentID == "" {
		res.Notes = append(res.Notes, NoteOverviewNotUpdated)
		log.Debug("review: overview not updated, its comment id is unknown")
		return
	}
	if err := p.EditComment(ctx, ref, res.Publish.CommentID, render(p.Capabilities())); err != nil {
		res.Notes = append(res.Notes, NoteOverviewNotUpdated)
		log.Debug("review: overview update failed", "error", fixedError(err))
	}
}

// fixedError is the loggable text of a provider call's error: the fixed
// sentence of a classified provider error, or a generic one (X-8).
func fixedError(err error) string {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return pe.Error()
	}
	return "unclassified error"
}
