package improve

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// OverviewMarker is the last line of the overview comment (v2 spec §3, Y-11):
// a CommonMark link reference definition, which renders as nothing. The
// comment is found by it and its author (llmrun.FindMarked) and edited in
// place on later runs (X-12).
const OverviewMarker = "[//]: # (review-mcp:improve:v1)"

// Suggestion fingerprint markers (X-13 pattern, their own prefix): the last
// line of every inline comment, "[//]: # (review-mcp:suggestion:<fp>)" with
// the 12 hex digits of SuggestionFingerprint.
const (
	suggestionMarkerPrefix = "[//]: # (review-mcp:suggestion:"
	suggestionMarkerSuffix = ")"
	fingerprintHexLen      = 12
)

// publishFailedMessage is shown for a publish error that is not a
// classified provider error.
const publishFailedMessage = "the suggestions could not be posted as a PR comment"

// Notes of publishing. They are fixed sentences with counts only.
const (
	// NoteOverviewReplaced: the overview found on the PR could not be
	// edited, so a new one was posted.
	NoteOverviewReplaced = "The previous overview could not be updated; a new one was posted."
	// NoteOverviewLookupFailed: the PR's comments or the token's user could
	// not be read, so an earlier overview could not be found.
	NoteOverviewLookupFailed = "The existing overview could not be looked up; a new one was posted."
	// NoteOverviewNotUpdated: the overview could not be edited after the
	// inline comments were posted, so it does not link to them.
	NoteOverviewNotUpdated = "The overview could not be updated after the inline comments were posted; " +
		"it links to the changed lines instead."
	// NoteInlineNotChecked: the comments already on the PR could not be
	// read, so no inline comment was posted (it could repeat one).
	NoteInlineNotChecked = "The comments already on the PR could not be read, so no inline suggestion was posted " +
		"(it could repeat one); the suggestions are listed in the overview only."
)

// errInlineNotChecked is the Anchor.Error of a suggestion that was not
// posted because the PR's comments could not be read.
const errInlineNotChecked = "the comments already on the PR could not be read"

func noteOlderOverviews(n int) string {
	return llmrun.CountPhrase(n, "older overview comment by the same user was",
		"older overview comments by the same user were") + " left unchanged."
}

func noteUnanchorable(n int) string {
	return llmrun.CountPhrase(n, "suggestion could not be placed on changed lines of one hunk and is",
		"suggestions could not be placed on changed lines of one hunk and are") + " listed in the overview only."
}

func noteAlreadyPosted(n int) string {
	return llmrun.CountPhrase(n, "suggestion was already posted on this PR and was not repeated.",
		"suggestions were already posted on this PR and were not repeated.")
}

func noteInlineFailed(n int) string {
	return llmrun.CountPhrase(n, "suggestion could not be posted as an inline comment and is",
		"suggestions could not be posted as inline comments and are") + " listed in the overview only."
}

// OverviewRenderer renders the overview comment for a provider with caps
// (without the marker, which the pipeline adds). link returns the URL of a
// line of a file at the PR's head, or "". It is implemented by
// render.Overview.
type OverviewRenderer func(res *Result, caps provider.Capabilities, link func(path string, line int) string) string

// InlineRenderer renders the body of one suggestion's inline comment (without
// the fingerprint marker, which the pipeline adds). It is implemented by
// render.Inline.
type InlineRenderer func(s *Suggestion, caps provider.Capabilities) string

// SuggestionFingerprint identifies a suggestion across parts and runs (X-13,
// llmrun.Fingerprint over its file, summary and existing code).
func SuggestionFingerprint(s *Suggestion) string {
	return llmrun.Fingerprint(s.File, s.Summary, s.ExistingCode)
}

// SuggestionMarker is the marker line of fingerprint fp.
func SuggestionMarker(fp string) string {
	return suggestionMarkerPrefix + fp + suggestionMarkerSuffix
}

// ParseSuggestionMarker returns the fingerprint of a comment body whose last
// line, ignoring trailing whitespace, is a suggestion marker.
func ParseSuggestionMarker(body string) (string, bool) {
	body = strings.TrimRight(body, " \t\r\n")
	last := strings.TrimSpace(body[strings.LastIndexByte(body, '\n')+1:])
	fp, ok := strings.CutPrefix(last, suggestionMarkerPrefix)
	if !ok {
		return "", false
	}
	fp, ok = strings.CutSuffix(fp, suggestionMarkerSuffix)
	if !ok || len(fp) != fingerprintHexLen {
		return "", false
	}
	for i := 0; i < len(fp); i++ {
		if (fp[i] < '0' || fp[i] > '9') && (fp[i] < 'a' || fp[i] > 'f') {
			return "", false
		}
	}
	return fp, true
}

// postedSuggestions collects the fingerprints of the suggestion comments by
// the token's own user on the PR (as pr_review's fingerprintsOf does for
// findings): every comment of every inline thread is examined, since Gitea
// groups the comments of one line into one thread. A marker in anyone else's
// comment does not count.
func postedSuggestions(threads []provider.Thread, me provider.User) map[string]bool {
	out := map[string]bool{}
	for i := range threads {
		t := &threads[i]
		if t.Kind != provider.ThreadInline {
			continue
		}
		for j := range t.Comments {
			c := &t.Comments[j]
			if !provider.IsUser(me, c.AuthorID, c.AuthorLogin) {
				continue
			}
			if fp, ok := ParseSuggestionMarker(c.Body); ok {
				out[fp] = true
			}
		}
	}
	return out
}

// inlineItem is a suggestion with an inline comment to post.
type inlineItem struct {
	idx  int // index in Result.Suggestions
	item provider.InlineComment
}

// publish runs the publish step when publishing was requested and records
// the outcome in pl.Result.Publish. It never fails the run: a failed post is
// an outcome, and the suggestions are returned either way, as pr_review does.
//
// Order (as pr_review's, spec P7 §3.3, §4.2): the inline comments are
// planned first (only verified suggestions whose range is on head-side lines
// inside one hunk; those already on the PR are skipped), so that the overview
// carries their notes; then the overview of an earlier run is looked up.
//
//   - None found: the overview is posted before any inline comment, so that
//     a failed inline post never leaves the PR without it; when inline
//     comments were attempted the overview is then edited once, so that each
//     suggestion links to its inline comment.
//   - One found: the inline comments are posted, then the found overview is
//     edited once, with the links.
func (pl *Plan) publish(ctx context.Context, deps Deps, args Args) {
	if !args.Publish {
		return
	}
	res, log := pl.Result, pl.log
	res.Publish = &PublishResult{}
	var render func(provider.Capabilities) string
	if deps.RenderOverview != nil {
		render = func(caps provider.Capabilities) string {
			return llmrun.WithMarker(deps.RenderOverview(res, caps, pl.link), OverviewMarker)
		}
	}

	threads, me, lookupErr := pl.readComments(ctx)
	inlineOn := deps.RenderInline != nil
	var items []inlineItem
	var sum *InlineSummary
	notesBefore := len(res.Notes)
	if inlineOn {
		items, sum = pl.planInline(deps.RenderInline, postedSuggestions(threads, me), lookupErr != nil)
		if lookupErr != nil && sum.Failed > 0 {
			res.Notes = append(res.Notes, NoteInlineNotChecked)
		}
		if sum.Unanchorable > 0 {
			res.Notes = append(res.Notes, noteUnanchorable(sum.Unanchorable))
		}
		if sum.SkippedDuplicate > 0 {
			res.Notes = append(res.Notes, noteAlreadyPosted(sum.SkippedDuplicate))
		}
	}

	var found *llmrun.MarkedComment
	if lookupErr != nil {
		res.Notes = append(res.Notes, NoteOverviewLookupFailed)
		log.Debug("improve: comments not read", "error", fixedError(lookupErr))
	} else if found = llmrun.FindMarked(threads, me, OverviewMarker); found != nil && found.Older > 0 {
		res.Notes = append(res.Notes, noteOlderOverviews(found.Older))
	}

	if found != nil {
		pl.editOverview(ctx, found, items, sum, render)
		return
	}
	pl.postOverview(ctx, render)
	if !inlineOn {
		return
	}
	if !res.Publish.Published {
		// No inline comments without the overview: it is the only place that
		// lists the unanchorable suggestions and explains the rest.
		res.Notes = res.Notes[:notesBefore]
		log.Debug("improve: inline comments skipped, the overview was not posted", "anchorable", len(items))
		return
	}
	res.Publish.Inline = sum
	if len(items) > 0 {
		pl.postInline(ctx, items, sum)
		pl.updateOverview(ctx, render)
	}
	logInline(log, sum)
}

func logInline(log *slog.Logger, sum *InlineSummary) {
	log.Debug("improve: inline suggestions", "posted", sum.Posted, "failed", sum.Failed,
		"unanchorable", sum.Unanchorable, "skipped_duplicate", sum.SkippedDuplicate)
}

// link is the file-line URL of the PR's head, for the overview's line links.
func (pl *Plan) link(path string, line int) string {
	return pl.p.FileLineURL(pl.ref, pl.pr, path, line)
}

// readComments reads the PR's threads and the token's own user: one read for
// the overview lookup and the duplicate check. An error means neither can be
// trusted.
func (pl *Plan) readComments(ctx context.Context) ([]provider.Thread, provider.User, error) {
	threads, err := pl.p.ListThreads(ctx, pl.ref)
	if err != nil {
		return nil, provider.User{}, err
	}
	me, err := pl.p.CurrentUser(ctx)
	if err != nil {
		return nil, provider.User{}, err
	}
	return threads, me, nil
}

// planInline resolves the inline comment of every verified suggestion and
// records the outcome of the ones that are not posted in Suggestion.Anchor:
// a range that is not on head-side lines of one hunk is unanchorable, one
// whose fingerprint is in posted is skipped as a duplicate. When unchecked is
// set the PR's comments could not be read, so nothing is posted and every
// anchorable suggestion is counted failed. A suggestion that is not verified
// is not considered and keeps no anchor.
func (pl *Plan) planInline(render InlineRenderer, posted map[string]bool, unchecked bool) ([]inlineItem, *InlineSummary) {
	sum := &InlineSummary{}
	caps := pl.p.Capabilities()
	var out []inlineItem
	for i := range pl.Result.Suggestions {
		s := &pl.Result.Suggestions[i]
		if !s.Verified || s.StartLine == nil || s.EndLine == nil {
			continue
		}
		a, ok := anchorFor(pl.files[s.File], *s.StartLine, *s.EndLine)
		if !ok {
			sum.Unanchorable++
			s.Anchor = &Anchor{Status: AnchorUnanchorable}
			continue
		}
		fp := SuggestionFingerprint(s)
		switch {
		case unchecked:
			sum.Failed++
			s.Anchor = &Anchor{Status: AnchorFailed, Line: a.Line, Error: errInlineNotChecked}
			continue
		case posted[fp]:
			sum.SkippedDuplicate++
			s.Anchor = &Anchor{Status: AnchorSkippedDuplicate, Line: a.Line}
			continue
		}
		body := strings.TrimRight(render(s, caps), " \t\r\n") + "\n\n" + SuggestionMarker(fp)
		a.Body = provider.SanitizeBody(caps, body)
		out = append(out, inlineItem{idx: i, item: a})
	}
	return out, sum
}

// anchorFor places the head-side range start to end of fp on the provider's
// own hunks (FilePatch.Patch as GetDiff returned it, never the extended
// context of the prompt: a server accepts inline comments only on lines of
// its own diff). It succeeds only when every line of the range is a new-side
// line (added or context) of one single hunk, so that a suggestion block
// replaces exactly those lines. The comment sits on the first line of the
// range. It fails for a nil, deleted or binary file, a range that is not
// positive or whose end is before its start, an unparsable patch, and a
// range that leaves a hunk or reaches into the gap between two hunks.
// Malformed pseudo-hunks are never anchors. The returned comment has no
// body.
func anchorFor(fp *provider.FilePatch, start, end int) (provider.InlineComment, bool) {
	if fp == nil || fp.Binary || fp.Type == provider.ChangeDeleted || start < 1 || end < start {
		return provider.InlineComment{}, false
	}
	hunks, err := patch.ParseHunks(fp.Patch)
	if err != nil {
		return provider.InlineComment{}, false
	}
	for _, h := range hunks {
		if h.Malformed() {
			continue
		}
		covered, typ := 0, provider.LineType("")
		n := h.NewStart
		for _, l := range h.Lines {
			if l.Op != '+' && l.Op != ' ' {
				continue
			}
			if n >= start && n <= end {
				covered++
				if n == start {
					typ = provider.LineContext
					if l.Op == '+' {
						typ = provider.LineAdded
					}
				}
			}
			n++
		}
		if covered != end-start+1 {
			continue
		}
		c := provider.InlineComment{Path: fp.Path, Line: start, LineType: typ}
		if fp.Type == provider.ChangeRenamed {
			c.OldPath = fp.OldPath
		}
		return c, true
	}
	return provider.InlineComment{}, false
}

// editOverview publishes over the overview found on the PR: the inline
// comments first (sum is nil when inline suggestions are off), then one edit
// of the found comment with the links and notes. Any edit error (not_owner, a
// vanished comment, a conflict, any other provider error) posts a new
// overview instead, with NoteOverviewReplaced; publishing never fails the run.
func (pl *Plan) editOverview(ctx context.Context, found *llmrun.MarkedComment, items []inlineItem, sum *InlineSummary,
	render func(provider.Capabilities) string) {
	res, log := pl.Result, pl.log
	pub := res.Publish
	if sum != nil {
		pub.Inline = sum
		if len(items) > 0 {
			pl.postInline(ctx, items, sum)
		}
		logInline(log, sum)
	}
	err := errNoRenderer
	if render != nil {
		caps := pl.p.Capabilities()
		err = pl.p.EditComment(ctx, pl.ref, found.ID, provider.SanitizeBody(caps, render(caps)))
	}
	if err == nil {
		pub.Published, pub.Updated, pub.CommentID, pub.URL = true, true, found.ID, found.URL
		log.Debug("improve: overview updated in place", "older_overviews", found.Older)
		return
	}
	log.Debug("improve: overview update failed, posting a new one", "error", fixedError(err))
	res.Notes = append(res.Notes, NoteOverviewReplaced)
	pl.postOverview(ctx, render)
	if !pub.Published {
		// Neither edited nor posted: "a new one was posted" is not true, and
		// the old overview stays as it was.
		res.Notes = res.Notes[:len(res.Notes)-1]
	}
}

// errNoRenderer stands for a missing Deps.RenderOverview.
var errNoRenderer = errors.New("improve: no overview renderer")

// postOverview posts a new overview comment and records the outcome.
func (pl *Plan) postOverview(ctx context.Context, render func(provider.Capabilities) string) {
	var r llmrun.PublishResult
	llmrun.PostResult(ctx, pl.log, pl.ref, pl.p, &r, publishFailedMessage, render)
	pub := pl.Result.Publish
	pub.Published, pub.CommentID, pub.URL, pub.Error = r.Published, r.CommentID, r.URL, r.Error
}

// postInline posts the inline comments in one call and records each item's
// outcome in its suggestion's Anchor and in sum. An item the server refused
// for its position (InlineReasonUnanchorable) counts as unanchorable, the
// others as failed.
func (pl *Plan) postInline(ctx context.Context, items []inlineItem, sum *InlineSummary) {
	res := pl.Result
	in := make([]provider.InlineComment, len(items))
	for i := range items {
		in[i] = items[i].item
	}
	results, err := pl.p.PostInlineComments(ctx, pl.ref, pl.pr, in)
	if err != nil {
		pl.log.Debug("improve: inline comments not posted", "error", fixedError(err))
	}
	failed, refused := 0, 0
	for i, it := range items {
		s := &res.Suggestions[it.idx]
		line := it.item.Line
		switch {
		case err != nil:
			failed++
			sum.Failed++
			s.Anchor = &Anchor{Status: AnchorFailed, Line: line, Error: provider.ItemError(err)}
		case i >= len(results) || !results[i].Posted:
			r := provider.InlineResult{Reason: provider.InlineReasonFailed}
			if i < len(results) {
				r = results[i]
			}
			if r.Reason == provider.InlineReasonUnanchorable {
				refused++
				sum.Unanchorable++
				s.Anchor = &Anchor{Status: AnchorUnanchorable, Error: r.Error}
				continue
			}
			failed++
			sum.Failed++
			s.Anchor = &Anchor{Status: AnchorFailed, Line: line, Error: r.Error}
		default:
			sum.Posted++
			s.Anchor = &Anchor{Status: AnchorPosted, Line: line, CommentID: results[i].ID, URL: results[i].URL}
		}
	}
	if refused > 0 {
		res.Notes = append(res.Notes, noteUnanchorable(refused))
	}
	if failed > 0 {
		res.Notes = append(res.Notes, noteInlineFailed(failed))
	}
}

// updateOverview re-renders the overview, now with the inline links and
// notes, and edits the posted comment in place. A failure adds
// NoteOverviewNotUpdated; the posted overview stays as it is.
func (pl *Plan) updateOverview(ctx context.Context, render func(provider.Capabilities) string) {
	res := pl.Result
	if render == nil || res.Publish.CommentID == "" {
		res.Notes = append(res.Notes, NoteOverviewNotUpdated)
		pl.log.Debug("improve: overview not updated, its comment id is unknown")
		return
	}
	caps := pl.p.Capabilities()
	if err := pl.p.EditComment(ctx, pl.ref, res.Publish.CommentID, provider.SanitizeBody(caps, render(caps))); err != nil {
		res.Notes = append(res.Notes, NoteOverviewNotUpdated)
		pl.log.Debug("improve: overview update failed", "error", fixedError(err))
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
