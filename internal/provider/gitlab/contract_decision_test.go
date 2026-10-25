package gitlab

// The decision requests of the contract fake (V-14 spec §1.6).

import (
	"encoding/json"
	"net/http"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

// serveDecision answers POST .../approve and .../unapprove, modelled on
// GitLab's approvals API: approve takes an optional sha and answers 409
// when it is not the head (a request without one is accepted), unapprove
// takes nothing; both answer the approval state. What GitLab answers to an
// unapprove when the user has not approved is a live-verification item
// (design note §7); the fake answers it as a success, so that the
// contract's clear runs on a pull request without the user's approval
// (the tool does not send one there: its step 8 finds nothing to clear).
// Env.Decision
// overrides the answer. It reports whether it answered the request.
func (g *ctGitLab) serveDecision(w http.ResponseWriter, r *http.Request, path string) bool {
	approve, unapprove := path == ctMR+"/approve", path == ctMR+"/unapprove"
	if r.Method != http.MethodPost || (!approve && !unapprove) {
		return false
	}
	if d := g.pr.Env.Decision; d.Status != 0 {
		if d.MovedHead != "" {
			g.pr.HeadSHA = d.MovedHead
		}
		contract.WriteError(w, d.Status)
		return true
	}
	if approve {
		var in struct {
			SHA *string `json:"sha"`
		}
		if r.ContentLength != 0 && json.NewDecoder(r.Body).Decode(&in) != nil {
			contract.WriteError(w, http.StatusBadRequest)
			return true
		}
		if in.SHA != nil && *in.SHA != g.pr.HeadSHA {
			contract.WriteError(w, http.StatusConflict)
			return true
		}
		g.approved = true
	} else {
		g.approved = false
	}
	approvedBy := []any{}
	if g.approved {
		approvedBy = append(approvedBy, map[string]any{"user": ctUser(g.pr.TokenUser)})
	}
	ctJSON(w, http.StatusCreated, map[string]any{"iid": 7, "approved": g.approved, "approved_by": approvedBy})
	return true
}

// DecisionRequest implements contract.DecisionExpecter: POST .../approve
// with the sha, POST .../unapprove without a body.
func (ctFixture) DecisionRequest(spec contract.Spec, d provider.Decision, _ string) contract.WriteRequest {
	if d == provider.DecisionClear {
		return contract.WriteRequest{Method: http.MethodPost, Path: ctMR + "/unapprove"}
	}
	return contract.WriteRequest{Method: http.MethodPost, Path: ctMR + "/approve", Body: map[string]any{"sha": spec.HeadSHA}}
}

// DecisionState implements contract.DecisionExpecter.
func (ctFixture) DecisionState(d provider.Decision) string {
	if d == provider.DecisionClear {
		return "unapproved"
	}
	return "approved"
}
