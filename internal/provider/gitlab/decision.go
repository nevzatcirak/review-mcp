package gitlab

import (
	"context"
	"errors"
	"net/http"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// decisionCaps: GitLab approves a merge request at a sha it checks, and
// removes the user's own approval; the body is a note after it.
//
// DESIGN-QUESTION (WP-18a DQ-3): request changes. The REST documentation
// of the latest GitLab (Merge request approvals API, Merge requests API)
// documents no request for it: the approvals API has approve, unapprove
// and reset_approvals, and the reviewers list (GET .../reviewers) is
// read-only; "requested_changes" appears only as a detailed_merge_status
// value. Recommendation: leave it unsupported (no version check).
var decisionCaps = provider.DecisionCaps{Approve: true, Clear: true, AtomicHead: true}

// HintApprover is the hint of an auth error of an approval or its removal.
const HintApprover = "the user may not be an eligible approver, or the project requires re-authentication to approve"

// SetReviewDecision implements provider.DecisionWriter. Approve is POST
// /projects/{id}/merge_requests/{iid}/approve with {"sha": HeadSHA}; GitLab
// answers 409 when the sha is not the head, which is a conflict (head
// moved) without a re-read. Clear is POST .../unapprove without a body (it
// takes no sha; the caller's head check still applies). A 401 or 403 keeps
// class auth with HintApprover (an approver rule, or approval_password,
// which is not supported). Any other 4xx refusal is checked for a moved
// head with a re-read (provider.DecisionRefused). The state is approved or
// unapproved; the body, when there is one, is posted as a note after the
// decision (provider.PostDecisionBody).
func (p *Provider) SetReviewDecision(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, req provider.DecisionRequest) (*provider.DecisionResult, error) {
	if err := provider.ValidateDecision(decisionCaps, req); err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, protocolErr("the pull request is unknown")
	}
	mr, err := mrPath(ref)
	if err != nil {
		return nil, err
	}
	state := "approved"
	if req.Decision == provider.DecisionClear {
		state = "unapproved"
		_, err = p.do(ctx, request{method: http.MethodPost, path: mr + "/unapprove"})
	} else {
		err = p.sendJSON(ctx, http.MethodPost, mr+"/approve", map[string]string{"sha": req.HeadSHA}, nil)
	}
	if err != nil {
		var pe *provider.Error
		switch {
		case errors.As(err, &pe) && pe.Class == provider.ClassAuth:
			return nil, &provider.Error{Class: provider.ClassAuth, Status: pe.Status, Hint: HintApprover}
		case errors.As(err, &pe) && pe.Status == http.StatusConflict && req.Decision == provider.DecisionApprove:
			return nil, &provider.HeadMovedError{Status: pe.Status}
		}
		return nil, provider.DecisionRefused(ctx, err, req.HeadSHA, func(ctx context.Context) (*provider.PullRequest, error) {
			return p.GetPullRequest(ctx, ref)
		})
	}
	posted := provider.PostDecisionBody(ctx, p, ref, decisionCaps, req.Body)
	p.logger.Debug("gitlab review decision set", "state", state, "body_posted", posted)
	return &provider.DecisionResult{State: state, BodyPosted: posted}, nil
}
