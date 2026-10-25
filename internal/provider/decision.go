package provider

import (
	"context"
	"errors"
	"net/http"
)

// Decision is a reviewer's decision on a pull request (X-47).
type Decision string

// Decisions.
const (
	DecisionApprove        Decision = "approve"
	DecisionRequestChanges Decision = "request_changes"
	DecisionClear          Decision = "clear"
)

// DecisionRequest is the request of DecisionWriter.SetReviewDecision.
type DecisionRequest struct {
	Decision Decision
	// HeadSHA is the commit the decision is for. The caller has already
	// compared it with PullRequest.HeadSHA; a provider with an atomic check
	// sends it as well (DecisionCaps.AtomicHead).
	HeadSHA string
	// Body is optional, already validated and passed through
	// SanitizeBody(caps, body) by the caller, exactly as pr_comment_create
	// does (GitLab quick actions). Where the provider has a review body
	// (DecisionCaps.BodyInReview) it is the review's body; otherwise the
	// provider posts it with PostComment after the decision succeeded.
	Body string
}

// Values of DecisionResult.BodyPosted.
const (
	// BodyPostedReview: the body is the review's own body.
	BodyPostedReview = "review"
	// BodyPostedComment: the body was posted as a general comment after the
	// decision.
	BodyPostedComment = "comment"
	// BodyNotPosted: the decision succeeded, but the comment that carries
	// the body failed. It is not an error.
	BodyNotPosted = "not_posted"
)

// DecisionResult reports what the provider did.
type DecisionResult struct {
	// State is the host's own word for the new state, from a fixed set per
	// provider: APPROVED, NEEDS_WORK, UNAPPROVED (Bitbucket Server);
	// APPROVED, CHANGES_REQUESTED (GitHub); APPROVED, REQUEST_CHANGES
	// (Gitea); approved, unapproved (GitLab). Never a body or a name.
	State string
	// BodyPosted is BodyPostedReview, BodyPostedComment, BodyNotPosted or
	// "" (no body).
	BodyPosted string
}

// DecisionWriter is implemented by providers whose Capabilities().Decisions
// allows at least one decision. The contract suite checks both directions.
type DecisionWriter interface {
	// SetReviewDecision records the token's user's decision on pr, the
	// pull request ref names as GetPullRequest read it. It validates req
	// with ValidateDecision before any request is sent. The decision
	// request comes first; the body, when it is not part of it, is posted
	// after it succeeded (PostDecisionBody). A refusal of the decision
	// request whose re-read shows a moved head is a *HeadMovedError
	// (DecisionRefused).
	SetReviewDecision(ctx context.Context, ref PRRef, pr *PullRequest, req DecisionRequest) (*DecisionResult, error)
}

// DecisionCaps says which decisions the host's API supports for the
// token's own user.
type DecisionCaps struct {
	Approve, RequestChanges, Clear bool
	// AtomicHead: the host refuses the decision itself when HeadSHA is not
	// the latest commit (Bitbucket Server lastReviewedCommit, GitLab sha).
	AtomicHead bool
	// BodyInReview: the body is part of the decision request (GitHub,
	// Gitea); otherwise it is posted as a general comment afterwards.
	BodyInReview bool
	// RequestChangesNeedsBody: request_changes is refused without a body.
	RequestChangesNeedsBody bool
}

// Allows reports whether c allows d; it is false for an unknown value.
func (c DecisionCaps) Allows(d Decision) bool {
	switch d {
	case DecisionApprove:
		return c.Approve
	case DecisionRequestChanges:
		return c.RequestChanges
	case DecisionClear:
		return c.Clear
	}
	return false
}

// Any reports whether c allows at least one decision.
func (c DecisionCaps) Any() bool { return c.Approve || c.RequestChanges || c.Clear }

// ValidateDecision checks req against caps. Providers call it before any
// request is sent. A decision caps does not allow, a HeadSHA that is not
// 40 or 64 lowercase hex characters, and request_changes without a body
// when caps.RequestChangesNeedsBody give a protocol error with a fixed
// hint; the body is never part of it.
func ValidateDecision(caps DecisionCaps, req DecisionRequest) error {
	switch {
	case !caps.Allows(req.Decision):
		return &Error{Class: ClassProtocol, Hint: "the decision is not supported"}
	case !ValidHeadSHA(req.HeadSHA):
		return &Error{Class: ClassProtocol, Hint: "invalid head sha"}
	case req.Decision == DecisionRequestChanges && caps.RequestChangesNeedsBody && req.Body == "":
		return &Error{Class: ClassProtocol, Hint: "a body is required for request_changes"}
	}
	return nil
}

// ValidHeadSHA reports whether s is a full commit id: 40 (SHA-1) or 64
// (SHA-256) lowercase hex characters.
func ValidHeadSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if (s[i] < '0' || s[i] > '9') && (s[i] < 'a' || s[i] > 'f') {
			return false
		}
	}
	return true
}

// HintHeadMoved is the hint of the conflict a refused decision gives when
// the pull request's head moved.
const HintHeadMoved = "head moved"

// HeadMovedError is the conflict of SetReviewDecision when the pull
// request's head is no longer the request's HeadSHA. Head is the head the
// re-read saw, or "" when the provider knows only that it moved (GitLab's
// 409). errors.Is(err, ErrConflict) holds for it, and errors.As finds its
// *Error.
//
// DESIGN-QUESTION (WP-18a DQ-4): the spec's tool maps this conflict to a
// message with "the head that the re-read saw", which an *Error cannot
// carry (Hint is a fixed phrase). This type carries it without a new
// error class. Recommendation: keep.
type HeadMovedError struct {
	Head string
	// Status is the HTTP status of the refused decision request.
	Status int
}

func (e *HeadMovedError) err() *Error {
	return &Error{Class: ClassConflict, Status: e.Status, Hint: HintHeadMoved}
}

// Error returns the conflict's fixed sentence (X-6).
func (e *HeadMovedError) Error() string { return e.err().Error() }

// Unwrap returns the conflict as an *Error.
func (e *HeadMovedError) Unwrap() error { return e.err() }

// DecisionRefused is SetReviewDecision's error for err, the failure of the
// decision request. On a 4xx answer other than 401, 403 and 429 it reads
// the pull request again with reread: when the head is no longer head, the
// error is a *HeadMovedError with the new head; otherwise, and when the
// re-read fails, it is err. Response bodies are never read (X-6).
func DecisionRefused(ctx context.Context, err error, head string, reread func(context.Context) (*PullRequest, error)) error {
	var pe *Error
	if !errors.As(err, &pe) || pe.Status < 400 || pe.Status >= 500 ||
		pe.Status == http.StatusUnauthorized || pe.Status == http.StatusForbidden || pe.Status == http.StatusTooManyRequests {
		return err
	}
	pr, rerr := reread(ctx)
	if rerr != nil || pr == nil || pr.HeadSHA == head {
		return err
	}
	return &HeadMovedError{Head: pr.HeadSHA, Status: pe.Status}
}

// PostDecisionBody finishes a decision that succeeded: it returns
// DecisionResult.BodyPosted for body. Without a body it is "". With
// BodyInReview the body was part of the decision request (BodyPostedReview).
// Otherwise the body is posted with p.PostComment: BodyPostedComment, or
// BodyNotPosted when the comment failed, which is not an error.
func PostDecisionBody(ctx context.Context, p Provider, ref PRRef, caps DecisionCaps, body string) string {
	switch {
	case body == "":
		return ""
	case caps.BodyInReview:
		return BodyPostedReview
	}
	if _, err := p.PostComment(ctx, ref, body); err != nil {
		return BodyNotPosted
	}
	return BodyPostedComment
}
