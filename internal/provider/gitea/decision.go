package gitea

import (
	"context"
	"net/http"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// decisionCaps: a review approves or requests changes, with the body as
// the review's own. Gitea has no removal of the user's own approval
// (dismissal is a maintainer action on someone else's review), and the
// commit_id it is given is not checked, so the head is checked by the
// caller only.
//
// DESIGN-QUESTION (WP-18a DQ-2): RequestChangesNeedsBody. Gitea's
// preparePullReviewType (routers/api/v1/repo/pull_review.go) refuses
// REQUEST_CHANGES with a blank body with 422 in every version since the
// review API came in 1.12 ("review event REQUEST_CHANGES need body" in
// 1.12, "requires a body" in 1.20 and main). So the flag matches the
// host and saves a request. Recommendation: keep it.
var decisionCaps = provider.DecisionCaps{Approve: true, RequestChanges: true, BodyInReview: true, RequestChangesNeedsBody: true}

// reviewEvent maps a decision to Gitea's review event, which is also the
// state of the submitted review.
var reviewEvent = map[provider.Decision]string{
	provider.DecisionApprove:        "APPROVED",
	provider.DecisionRequestChanges: "REQUEST_CHANGES",
}

// SetReviewDecision implements provider.DecisionWriter with POST
// /api/v1/repos/{o}/{r}/pulls/{n}/reviews and the body {"commit_id":
// HeadSHA, "event": APPROVED or REQUEST_CHANGES, "body": Body}, body left
// out when empty. The event is never empty: Gitea keeps a review without
// one as a PENDING draft. The answer's state must be the event; any other
// state is a protocol error. A 4xx refusal is checked for a moved head
// with a re-read (provider.DecisionRefused).
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
	event := reviewEvent[req.Decision]
	in := struct {
		CommitID string `json:"commit_id"`
		Event    string `json:"event"`
		Body     string `json:"body,omitempty"`
	}{CommitID: req.HeadSHA, Event: event, Body: req.Body}
	var out apiPostedReview
	if err := p.client.SendJSON(ctx, http.MethodPost, pp+"/reviews", in, &out); err != nil {
		return nil, provider.DecisionRefused(ctx, err, req.HeadSHA, func(ctx context.Context) (*provider.PullRequest, error) {
			return p.GetPullRequest(ctx, ref)
		})
	}
	if out.State != event {
		return nil, protocolErr("the review was not submitted with the requested state")
	}
	posted := provider.PostDecisionBody(ctx, p, ref, decisionCaps, req.Body)
	p.logger.Debug("gitea review decision submitted", "state", out.State, "body_posted", posted)
	return &provider.DecisionResult{State: out.State, BodyPosted: posted}, nil
}
