package tools

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Fixed sentences of pr_review_decision (X-47, v2.7 spec V-14 §2.3 and
// §2.4). They never echo an argument; the names in the parametrised ones
// are fixed words (a decision, a provider's display name, short commit
// ids).
const (
	DecisionInvalidMessage     = "decision must be approve, request_changes or clear"
	DecisionHeadInvalidMessage = "head_sha must be 40 or 64 hexadecimal characters"
	DecisionBodyUTF8Message    = "body must be valid UTF-8"
	DecisionNotOpenMessage     = "the pull request is not open"
	DecisionOwnPRMessage       = "the token's user is the author of this pull request; a decision on your own pull request is not made"
	// NoteDecisionStatusUnreadable: step 8 could not read the review status,
	// so the decision was sent without the idempotency check.
	NoteDecisionStatusUnreadable = "the current review status could not be read; the decision was sent anyway"
	// NoteDecisionBodyNotPosted: the decision is recorded, but the comment
	// that carries the body failed.
	NoteDecisionBodyNotPosted = "the decision was recorded, but the body could not be posted as a comment"
	// decisionNewerCommit stands for a head that is unknown or not a commit
	// id (X-6: a host string is never repeated).
	decisionNewerCommit = "a newer commit"
)

// decisionNotAllowedMessage is the refusal of step 2.
func decisionNotAllowedMessage(d provider.Decision) string {
	return "decision.allowed does not include " + string(d)
}

// decisionUnsupportedMessage is the refusal of step 3.
func decisionUnsupportedMessage(k provider.Kind, d provider.Decision) string {
	return providerDisplayName(k) + " does not support " + string(d) + " through its API"
}

// decisionBodyRequiredMessage is the refusal of step 4.
func decisionBodyRequiredMessage(k provider.Kind) string {
	return "body is required for request_changes on " + providerDisplayName(k)
}

// decisionHeadMovedMessage is the refusal of step 7, and of a conflict in
// step 9: now is the head the pull request has (short), or "" when it is
// unknown or not a commit id.
func decisionHeadMovedMessage(now, reviewed string) string {
	return "the pull request head is now " + decisionShortSHA(now) + ", not " + decisionShortSHA(reviewed) +
		"; review the new commits before deciding"
}

// decisionShortSHA is the first seven characters of a commit id, or
// decisionNewerCommit for anything that is not one.
func decisionShortSHA(sha string) string {
	if !provider.ValidHeadSHA(sha) {
		return decisionNewerCommit
	}
	return sha[:7]
}

// providerDisplayName is the display name of a provider kind.
func providerDisplayName(k provider.Kind) string {
	switch k {
	case provider.KindGitea:
		return "Gitea"
	case provider.KindBitbucketServer:
		return "Bitbucket Server"
	case provider.KindGitHub:
		return "GitHub"
	case provider.KindGitLab:
		return "GitLab"
	}
	return string(k)
}

// PRReviewDecisionArgs are the arguments of pr_review_decision.
type PRReviewDecisionArgs struct {
	PRURL    string
	Decision string
	HeadSHA  string
	Body     string
}

// normalize returns a with the head in lower case and a blank body (empty
// after strings.TrimSpace) as no body (spec §2.3).
func (a PRReviewDecisionArgs) normalize() PRReviewDecisionArgs {
	a.HeadSHA = strings.ToLower(a.HeadSHA)
	a.Body = provider.DecisionBody(a.Body)
	return a
}

// Validate is step 1: the arguments, checked before any network call, on
// normalized arguments. The body follows pr_comment_create's rules (at
// most MaxCreateBodyChars, no review-mcp marker line), except that it may
// be absent and that invalid UTF-8 is refused instead of replaced.
func (a PRReviewDecisionArgs) Validate() error {
	switch provider.Decision(a.Decision) {
	case provider.DecisionApprove, provider.DecisionRequestChanges, provider.DecisionClear:
	default:
		return &ArgumentError{DecisionInvalidMessage}
	}
	if !provider.ValidHeadSHA(a.HeadSHA) {
		return &ArgumentError{DecisionHeadInvalidMessage}
	}
	switch {
	case a.Body == "":
	case !utf8.ValidString(a.Body):
		return &ArgumentError{DecisionBodyUTF8Message}
	case utf8.RuneCountInString(a.Body) > MaxCreateBodyChars:
		return &ArgumentError{CreateBodyTooLongMessage}
	case review.ContainsMarkerLine(a.Body):
		return &ArgumentError{CreateBodyMarkerMessage}
	}
	return nil
}

// PRReviewDecisionResult is the structured result of pr_review_decision.
type PRReviewDecisionResult struct {
	// PR is the pull request as pr_info names it (pr.kind and pr.url; spec
	// §2.7 commit 5, WP-18b DQ-8).
	PR        PRInfo `json:"pr"`
	Decision  string `json:"decision" jsonschema:"the decision: approve, request_changes or clear"`
	HeadSHA   string `json:"head_sha" jsonschema:"the commit the decision is for, in lower case"`
	State     string `json:"state" jsonschema:"the host's own word for the new state (APPROVED, NEEDS_WORK, UNAPPROVED on Bitbucket Server; APPROVED, CHANGES_REQUESTED on GitHub; APPROVED, REQUEST_CHANGES on Gitea; approved, unapproved on GitLab); empty when unchanged"`
	Unchanged bool   `json:"unchanged" jsonschema:"true when the user's own state already was the requested one at this head, so nothing was written"`
	// BodyPosted is provider.BodyPostedReview, BodyPostedComment,
	// BodyNotPosted or "".
	BodyPosted string   `json:"body_posted" jsonschema:"review (the body is the review's), comment (posted after the decision), not_posted (the decision succeeded, the comment failed), or empty without a body or when unchanged"`
	Notes      []string `json:"notes" jsonschema:"fixed notes"`
}

// PRReviewDecision records the token's user's own decision on the pull
// request at a.PRURL, in the order of spec §2.4. Every step before the
// last sends no write request: the arguments (1), decision.allowed (2),
// the provider's support (3), a required body (4), an open pull request
// (5), not the user's own (6), the reviewed head (7), and the user's
// current state (8: an unchanged state writes nothing). Then the decision
// (9); a conflict maps to the head-moved refusal of step 7.
func PRReviewDecision(ctx context.Context, cfg *config.Config, resolver PRResolver, a PRReviewDecisionArgs) (PRReviewDecisionResult, error) {
	var zero PRReviewDecisionResult
	a = a.normalize()
	// 1. The arguments.
	if err := a.Validate(); err != nil {
		return zero, err
	}
	d := provider.Decision(a.Decision)
	// 2. decision.allowed.
	if cfg == nil || !cfg.Decision.Allows(a.Decision) {
		return zero, &CommentError{decisionNotAllowedMessage(d)}
	}
	// 3. The provider's support.
	ref, p, err := resolver.Resolve(a.PRURL)
	if err != nil {
		return zero, err
	}
	w, isWriter := p.(provider.DecisionWriter)
	caps := p.Capabilities()
	if !isWriter || !caps.Decisions.Allows(d) {
		return zero, &CommentError{decisionUnsupportedMessage(ref.Kind, d)}
	}
	// 4. A required body.
	if d == provider.DecisionRequestChanges && a.Body == "" && caps.Decisions.RequestChangesNeedsBody {
		return zero, &ArgumentError{decisionBodyRequiredMessage(ref.Kind)}
	}
	// 5. An open pull request.
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return zero, err
	}
	if pr == nil {
		return zero, &provider.Error{Class: provider.ClassProtocol}
	}
	if prInfoState(pr) != "open" {
		return zero, &CommentError{DecisionNotOpenMessage}
	}
	// 6. Not the user's own pull request.
	me, err := p.CurrentUser(ctx)
	if err != nil {
		return zero, err
	}
	if provider.IsUser(me, pr.AuthorID, pr.Author) {
		return zero, &CommentError{DecisionOwnPRMessage}
	}
	// 7. The reviewed head.
	if strings.ToLower(pr.HeadSHA) != a.HeadSHA {
		return zero, &CommentError{decisionHeadMovedMessage(pr.HeadSHA, a.HeadSHA)}
	}
	res := PRReviewDecisionResult{
		PR:       PRInfo{Kind: string(ref.Kind), URL: logging.RedactURL(ref.URL)},
		Decision: a.Decision, HeadSHA: a.HeadSHA, Notes: []string{},
	}
	// 8. The user's current state.
	st := p.GetReviewStatus(ctx, ref, pr, provider.ReviewStatusOptions{Me: &me, IsOwn: review.IsMarkedBody})
	if st == nil || st.Reviewers == nil {
		res.Notes = append(res.Notes, NoteDecisionStatusUnreadable)
	} else if decisionHolds(st.Reviewers, me, d, caps.Decisions) {
		res.Unchanged = true
		return res, nil
	}
	// 9. The decision.
	body := ""
	if a.Body != "" {
		body = provider.SanitizeBody(caps, a.Body)
	}
	out, err := w.SetReviewDecision(ctx, ref, pr, provider.DecisionRequest{Decision: d, HeadSHA: a.HeadSHA, Body: body})
	if err != nil {
		var moved *provider.HeadMovedError
		switch {
		case errors.As(err, &moved):
			return zero, &CommentError{decisionHeadMovedMessage(moved.Head, a.HeadSHA)}
		case errors.Is(err, provider.ErrConflict):
			return zero, &CommentError{decisionHeadMovedMessage("", a.HeadSHA)}
		}
		return zero, err
	}
	if out == nil {
		return zero, &provider.Error{Class: provider.ClassProtocol}
	}
	res.State, res.BodyPosted = out.State, out.BodyPosted
	if out.BodyPosted == provider.BodyNotPosted {
		res.Notes = append(res.Notes, NoteDecisionBodyNotPosted)
	}
	return res, nil
}

// decisionHolds reports whether the user's own entry among reviewers
// already has the state d asks for at this head (step 8): approved and
// not stale for approve, changes_requested and not stale for
// request_changes, and for clear no approved entry at all (a stale
// approval is still an approval to clear) and, where clear also resets a
// change request (caps.ClearResetsChangeRequest), no changes_requested
// entry either (spec §2.7).
func decisionHolds(reviewers []provider.Reviewer, me provider.User, d provider.Decision, caps provider.DecisionCaps) bool {
	toClear := false
	for _, r := range reviewers {
		if !provider.IsUser(me, r.User.ID, r.User.Name) {
			continue
		}
		switch {
		case d == provider.DecisionApprove && r.State == provider.ReviewApproved && !r.Stale,
			d == provider.DecisionRequestChanges && r.State == provider.ReviewChangesRequested && !r.Stale:
			return true
		case r.State == provider.ReviewApproved,
			r.State == provider.ReviewChangesRequested && caps.ClearResetsChangeRequest:
			toClear = true
		}
	}
	return d == provider.DecisionClear && !toClear
}

// RenderPRReviewDecisionText renders the text content of
// pr_review_decision: one line, then the notes, one per line.
func RenderPRReviewDecisionText(r PRReviewDecisionResult) string {
	at := decisionShortSHA(r.HeadSHA)
	var line string
	switch d := provider.Decision(r.Decision); {
	case r.Unchanged && d == provider.DecisionApprove:
		line = "Already approved at " + at + "; nothing was changed."
	case r.Unchanged && d == provider.DecisionRequestChanges:
		line = "Changes already requested at " + at + "; nothing was changed."
	case r.Unchanged:
		line = "Nothing to clear at " + at + "; nothing was changed."
	case d == provider.DecisionApprove:
		line = "Approved at " + at + " on " + providerDisplayName(provider.Kind(r.PR.Kind)) + "."
	case d == provider.DecisionRequestChanges:
		line = "Changes requested at " + at + " on " + providerDisplayName(provider.Kind(r.PR.Kind)) + "."
	default:
		line = "Decision cleared at " + at + " on " + providerDisplayName(provider.Kind(r.PR.Kind)) + "."
	}
	var b strings.Builder
	b.WriteString(line + "\n")
	for _, n := range r.Notes {
		b.WriteString(n + "\n")
	}
	return b.String()
}
