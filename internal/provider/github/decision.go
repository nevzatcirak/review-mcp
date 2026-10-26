package github

import (
	"context"
	"net/http"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// decisionCaps: a review approves or requests changes, with the body as
// the review's own (required for a change request). GitHub cannot remove
// the user's own approval (dismissal is a maintainer action on someone
// else's review), and commit_id may name an older commit, so the head is
// checked by the caller only.
var decisionCaps = provider.DecisionCaps{Approve: true, RequestChanges: true, BodyInReview: true, RequestChangesNeedsBody: true}

// reviewEvent maps a decision to the review event that submits it and the
// state the submitted review has.
var reviewEvent = map[provider.Decision]struct{ event, state string }{
	provider.DecisionApprove:        {"APPROVE", "APPROVED"},
	provider.DecisionRequestChanges: {"REQUEST_CHANGES", "CHANGES_REQUESTED"},
}

// SetReviewDecision implements provider.DecisionWriter with POST
// /repos/{o}/{r}/pulls/{n}/reviews and the body {"commit_id": HeadSHA,
// "event": APPROVE or REQUEST_CHANGES, "body": Body}, body left out when
// blank (provider.DecisionBody). The event is never empty: a review
// without one stays PENDING. The answer's state must be APPROVED or
// CHANGES_REQUESTED; any other state (PENDING: the review was not
// submitted) is a protocol error. A 4xx refusal is checked for a moved
// head with a re-read (provider.DecisionRefused).
func (p *Provider) SetReviewDecision(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, req provider.DecisionRequest) (*provider.DecisionResult, error) {
	if err := provider.ValidateDecision(decisionCaps, req); err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, protocolErr("the pull request is unknown")
	}
	pp, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	ev := reviewEvent[req.Decision]
	in := struct {
		CommitID string `json:"commit_id"`
		Event    string `json:"event"`
		Body     string `json:"body,omitempty"`
	}{CommitID: req.HeadSHA, Event: ev.event, Body: provider.DecisionBody(req.Body)}
	var out struct {
		State string `json:"state"`
	}
	if err := p.sendJSON(ctx, http.MethodPost, pp+"/reviews", in, &out); err != nil {
		return nil, provider.DecisionRefused(ctx, err, req.HeadSHA, func(ctx context.Context) (*provider.PullRequest, error) {
			return p.GetPullRequest(ctx, ref)
		})
	}
	if out.State != ev.state {
		return nil, protocolErr("the review was not submitted with the requested state")
	}
	posted := provider.PostDecisionBody(ctx, p, ref, decisionCaps, req.Body)
	p.logger.Debug("github review decision submitted", "state", out.State, "body_posted", posted)
	return &provider.DecisionResult{State: out.State, BodyPosted: posted}, nil
}
