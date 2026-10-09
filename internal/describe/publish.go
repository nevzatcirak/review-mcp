package describe

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Values of Args.PublishMode.
const (
	PublishModeComment     = "comment"
	PublishModeDescription = "description"
)

// ProviderRenderer renders the provider-profile markdown of a result for a
// provider with the given capabilities. It is implemented by
// describe/render.Provider; the markers are added by the pipeline.
type ProviderRenderer func(res *Result, caps provider.Capabilities) string

// publish runs the publish step when publishing was requested and records
// the outcome in pl.Result.Publish. It never fails the run: a refusal (no
// description edit, a damaged region, a concurrent change) and a failed
// write are outcomes with a fixed sentence, and the description is returned
// either way, as pr_review does.
func (pl *Plan) publish(ctx context.Context, deps Deps, args Args) {
	if !args.Publish {
		return
	}
	mode := args.PublishMode
	if mode == "" {
		mode = PublishModeComment
	}
	res := pl.Result
	res.Publish = &PublishResult{Mode: mode}
	var render func(provider.Capabilities) string
	if deps.RenderProvider != nil {
		render = func(caps provider.Capabilities) string { return deps.RenderProvider(res, caps) }
	}
	if mode == PublishModeDescription {
		pl.publishDescription(ctx, args, render)
		return
	}
	pl.publishComment(ctx, render)
}

// ---- publish_mode=comment ----

// publishComment posts the description comment, or edits in place the one an
// earlier run left (X-12: its last line is CommentMarker and its author is
// the token's user; the newest of several is edited, the others are noted
// and never deleted). Any edit error posts a new comment instead, with
// NoteCommentReplaced.
func (pl *Plan) publishComment(ctx context.Context, render func(provider.Capabilities) string) {
	res, log, p := pl.Result, pl.log, pl.p
	pub := res.Publish
	var withMarker func(provider.Capabilities) string
	if render != nil {
		withMarker = func(caps provider.Capabilities) string { return llmrun.WithMarker(render(caps), CommentMarker) }
	}

	replaced := false
	found, err := pl.findComment(ctx)
	switch {
	case err != nil:
		res.Notes = append(res.Notes, NoteCommentLookupFailed)
		log.Debug("describe: comment lookup failed", "error", fixedError(err))
	case found != nil:
		if found.Older > 0 {
			res.Notes = append(res.Notes, noteOlderComments(found.Older))
		}
		err := errNoRenderer
		if withMarker != nil {
			caps := p.Capabilities()
			err = p.EditComment(ctx, pl.ref, found.ID, provider.SanitizeBody(caps, withMarker(caps)))
		}
		if err == nil {
			pub.Published, pub.Updated, pub.CommentID, pub.URL = true, true, found.ID, found.URL
			log.Debug("describe: comment updated in place", "older_comments", found.Older)
			return
		}
		log.Debug("describe: comment update failed, posting a new one", "error", fixedError(err))
		res.Notes = append(res.Notes, NoteCommentReplaced)
		replaced = true
	}

	var r llmrun.PublishResult
	llmrun.PostResult(ctx, log, pl.ref, p, &r, publishFailedMessage, withMarker)
	pub.Published, pub.CommentID, pub.URL, pub.Error = r.Published, r.CommentID, r.URL, r.Error
	if replaced && !pub.Published {
		// Neither edited nor posted: "a new one was posted" is not true, and
		// the old comment stays as it was.
		res.Notes = res.Notes[:len(res.Notes)-1]
	}
}

// errNoRenderer stands for a missing Deps.RenderProvider.
var errNoRenderer = errors.New("describe: no provider renderer")

// findComment looks up the description comment of an earlier run
// (llmrun.FindMarked, shared with pr_review's overview). It returns nil when
// there is none and an error when the threads or the user could not be read:
// the caller then posts a new comment.
func (pl *Plan) findComment(ctx context.Context) (*llmrun.MarkedComment, error) {
	threads, err := pl.p.ListThreads(ctx, pl.ref)
	if err != nil {
		return nil, err
	}
	me, err := pl.p.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	return llmrun.FindMarked(threads, me, CommentMarker), nil
}

// noteOlderComments: several comments of ours were found; the newest was
// updated and the others were left as they are.
func noteOlderComments(n int) string {
	return llmrun.CountPhrase(n, "older description comment by the same user was",
		"older description comments by the same user were") + " left unchanged."
}

// ---- publish_mode=description ----

// publishDescription writes the managed region into the PR description
// (Y-5) and, with args.UpdateTitle, replaces the title with the generated
// one.
//
// The text outside the region is never changed (ApplyRegion). A concurrent
// edit is never overwritten: the PR is re-read right before the write, and
//
//   - when the description differs from the text the region was computed on,
//     the region is computed again on the fresh text, once;
//   - the write carries the fresh read's version (Bitbucket Server), so a
//     change after the re-read is a conflict; the conflict is handled like a
//     changed text, which is why it consumes the same single retry;
//   - when the text changes or conflicts a second time, nothing is written
//     and the outcome is MsgChangedWhileUpdating.
//
// After a successful write by a provider that reports a PR Version (a full
// replace), the reviewers are compared with those read just before the write
// (checkReviewers).
//
// DESIGN-QUESTION: Bitbucket Server's "409 is retried once" and Gitea's
// "recompute once" are one rule here: two computations at most, each
// followed by a re-read, and a repeat refuses. Chose it so the two
// providers cannot diverge and a write is never preceded by an unchecked
// read; the cost is one extra GET after a 409.
func (pl *Plan) publishDescription(ctx context.Context, args Args, render func(provider.Capabilities) string) {
	res, log, p := pl.Result, pl.log, pl.p
	pub := res.Publish
	caps := p.Capabilities()
	if !caps.DescriptionEdit {
		pub.Error = MsgNoDescriptionEdit
		log.Debug("describe: provider cannot edit the description")
		return
	}
	if len(res.Files) == 0 && (res.Description == nil || strings.TrimSpace(*res.Description) == "") {
		// An empty region would replace an earlier good one: write nothing,
		// and send no request.
		pub.Error = MsgNothingDescribed
		log.Debug("describe: nothing described, description not changed")
		return
	}
	if render == nil {
		pub.Error = publishFailedMessage
		log.Debug("publish skipped, no provider renderer")
		return
	}
	body := provider.SanitizeBody(caps, render(caps))

	var title *string
	if args.UpdateTitle {
		if t := stripDraftPrefix(generatedTitle(res)); t != "" {
			title = &t
		} else {
			res.Notes = append(res.Notes, NoteTitleNotGenerated)
		}
	}

	base := pl.pr
	for attempt := 0; attempt < 2; attempt++ {
		next, replaced, err := ApplyRegion(base.Description, body)
		if err != nil {
			pub.Error = MsgDamagedRegion
			log.Debug("describe: description region damaged")
			return
		}
		// Re-read right before the write.
		fresh, err := p.GetPullRequest(ctx, pl.ref)
		if err != nil {
			pub.Error = errorSentence(err)
			log.Debug("describe: description re-read failed", "error", pub.Error)
			return
		}
		if fresh.Description != base.Description {
			log.Debug("describe: description changed since it was read", "attempt", attempt+1)
			base = fresh
			continue
		}
		up := provider.UpdatePR{Version: fresh.Version}
		if next != fresh.Description {
			up.Description = &next
		}
		if title != nil {
			if t := keepDraftPrefix(fresh, *title); t != fresh.Title {
				up.Title = &t
			}
		}
		if up.Description == nil && up.Title == nil {
			// Idempotent run: the region already says this.
			pub.Published, pub.Updated, pub.URL = true, replaced, fresh.WebURL
			log.Debug("describe: description already up to date")
			return
		}
		// A provider that reports a Version replaces the whole PR on a
		// write and can lose reviewer state in doing so: take the reviewers
		// now, to compare them after the write.
		var before *provider.ReviewStatus
		if fresh.Version != "" {
			before = p.GetReviewStatus(ctx, pl.ref, fresh, provider.ReviewStatusOptions{})
		}
		err = p.UpdatePullRequest(ctx, pl.ref, up)
		switch {
		case err == nil:
			pub.Published, pub.Updated, pub.URL = true, replaced, fresh.WebURL
			if before != nil {
				pl.checkReviewers(ctx, fresh, before)
			}
			pub.TitleUpdated = up.Title != nil
			log.Debug("describe: description written", "replaced", replaced, "title", up.Title != nil)
			return
		case errors.Is(err, provider.ErrConflict):
			// Someone changed the PR after the re-read: read it again.
			log.Debug("describe: update conflict", "attempt", attempt+1)
			again, rerr := p.GetPullRequest(ctx, pl.ref)
			if rerr != nil {
				pub.Error = errorSentence(rerr)
				return
			}
			base = again
		default:
			pub.Error = errorSentence(err)
			log.Debug("describe: update failed", "error", pub.Error)
			return
		}
	}
	pub.Error = MsgChangedWhileUpdating
	log.Debug("describe: description changed twice, nothing written")
}

// checkReviewers re-reads the reviewers after a successful write of a
// full-replace provider and compares them with the read taken before it. A
// dropped reviewer or a changed state cannot be undone, so it is reported
// (NoteReviewersChanged); a comparison that cannot be made is reported too
// (NoteReviewersUnchecked). The publish still counts as published.
func (pl *Plan) checkReviewers(ctx context.Context, pr *provider.PullRequest, before *provider.ReviewStatus) {
	res := pl.Result
	after := pl.p.GetReviewStatus(ctx, pl.ref, pr, provider.ReviewStatusOptions{})
	if !reviewersRead(before) || !reviewersRead(after) {
		res.Notes = append(res.Notes, NoteReviewersUnchecked)
		pl.log.Debug("describe: reviewers could not be compared")
		return
	}
	if reviewersChanged(before.Reviewers, after.Reviewers) {
		res.Notes = append(res.Notes, NoteReviewersChanged)
		pl.log.Debug("describe: reviewers changed during the update", "before", len(before.Reviewers), "after", len(after.Reviewers))
	}
}

// reviewersRead reports whether the reviewers of st were read.
func reviewersRead(st *provider.ReviewStatus) bool {
	return st != nil && !slices.Contains(st.Notes, provider.NoteReviewsUnreadable)
}

// reviewersChanged reports whether a reviewer of before is missing from
// after or has another state. A reviewer that appears only in after is not a
// loss and is ignored.
func reviewersChanged(before, after []provider.Reviewer) bool {
	now := make(map[string]provider.ReviewState, len(after))
	for _, r := range after {
		now[r.User.ID+"\x00"+r.User.Name] = r.State
	}
	for _, r := range before {
		if st, ok := now[r.User.ID+"\x00"+r.User.Name]; !ok || st != r.State {
			return true
		}
	}
	return false
}

// draftPrefixes are the title prefixes that make a pull request a draft on a
// host that derives the draft state from the title: Gitea's default
// WORK_IN_PROGRESS_PREFIXES, matched case-insensitively at the start of the
// title. The server does not report its setting, so a non-default list is
// not known here.
var draftPrefixes = []string{"WIP:", "[WIP]"}

// draftPrefixLen returns the length of the draft prefix at the start of
// title, with the whitespace around it, or 0 when there is none.
func draftPrefixLen(title string) int {
	rest := strings.TrimLeft(title, " \t")
	lead := len(title) - len(rest)
	for _, p := range draftPrefixes {
		if len(rest) >= len(p) && strings.EqualFold(rest[:len(p)], p) {
			after := title[lead+len(p):]
			return len(title) - len(strings.TrimLeft(after, " \t"))
		}
	}
	return 0
}

// stripDraftPrefix removes draft prefixes from the start of a generated
// title, so that it cannot turn a pull request into a draft.
func stripDraftPrefix(title string) string {
	for {
		n := draftPrefixLen(title)
		if n == 0 {
			return title
		}
		title = title[n:]
	}
}

// keepDraftPrefix puts the draft prefix of the current title, exactly as it
// is written, in front of the generated title when the pull request is a
// draft, so that replacing the title never changes the draft state. Where
// the draft state is a flag (Bitbucket Server echoes it on write), a title
// without a prefix is returned as it is.
func keepDraftPrefix(cur *provider.PullRequest, generated string) string {
	if cur.Draft == nil || !*cur.Draft {
		return generated
	}
	n := draftPrefixLen(cur.Title)
	if n == 0 {
		return generated
	}
	prefix := cur.Title[:n]
	if prefix[len(prefix)-1] != ' ' && prefix[len(prefix)-1] != '\t' {
		prefix += " "
	}
	return prefix + generated
}

// generatedTitle returns the generated title as one line, or "" when there
// is none.
func generatedTitle(res *Result) string {
	if res.Title == nil {
		return ""
	}
	return strings.Join(strings.Fields(*res.Title), " ")
}

// errorSentence is the fixed sentence of a provider call's error: the
// sentence of a classified provider error (an auth failure reads "check the
// token and its scopes"), or a generic one.
func errorSentence(err error) string {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return pe.Error()
	}
	return publishFailedMessage
}

// fixedError is the loggable text of a provider call's error (X-8).
func fixedError(err error) string {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return pe.Error()
	}
	return "unclassified error"
}
