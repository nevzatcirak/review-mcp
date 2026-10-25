package gitlab

import (
	"errors"
	"net/http"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

func decisionSpec() contract.Spec {
	return contract.Spec{
		Title: "T", Author: contract.User{ID: 101, Login: "alice"}, SourceBranch: "f", TargetBranch: "main",
		HeadSHA: "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1", BaseSHA: "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2",
		TokenUser: contract.User{ID: 900, Login: "review-bot"},
		Env:       contract.Env{Token: "tok-FAKE-wp18a", MaxFiles: 10, MaxFileBytes: 4096}, //nolint:gosec // synthetic test value
	}
}

// TestDecisionCaps pins the decision capabilities of V-14 spec §1.6.
func TestDecisionCaps(t *testing.T) {
	want := provider.DecisionCaps{Approve: true, Clear: true, AtomicHead: true}
	if got := (*Provider)(nil).Capabilities().Decisions; got != want {
		t.Errorf("Decisions = %+v, want %+v", got, want)
	}
}

// TestDecisionRefusals: a 409 on approve is a conflict without a re-read
// of the merge request, its head unknown; a 401 or 403 on approve or
// unapprove keeps class auth with HintApprover; a 409 on unapprove follows
// the shared rule (re-read; an unmoved head keeps the status error).
func TestDecisionRefusals(t *testing.T) {
	for _, c := range []struct {
		name    string
		d       provider.Decision
		status  int
		want    *provider.Error
		hint    string
		rereads int
	}{
		{"approve_409", provider.DecisionApprove, http.StatusConflict, provider.ErrConflict, provider.HintHeadMoved, 0},
		{"approve_401", provider.DecisionApprove, http.StatusUnauthorized, provider.ErrAuth, HintApprover, 0},
		{"approve_403", provider.DecisionApprove, http.StatusForbidden, provider.ErrAuth, HintApprover, 0},
		{"unapprove_403", provider.DecisionClear, http.StatusForbidden, provider.ErrAuth, HintApprover, 0},
		{"unapprove_409", provider.DecisionClear, http.StatusConflict, provider.ErrProtocol, "", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			spec := decisionSpec()
			spec.Env.Decision.Status = c.status
			p, ref, g, _ := ctFixture{}.serve(t, spec)
			pr, err := p.GetPullRequest(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			g.mu.Lock()
			reads := g.mrReads
			g.mu.Unlock()
			res, err := p.SetReviewDecision(t.Context(), ref, pr, provider.DecisionRequest{Decision: c.d, HeadSHA: spec.HeadSHA, Body: "x"})
			var pe *provider.Error
			if !errors.Is(err, c.want) || res != nil || !errors.As(err, &pe) || pe.Hint != c.hint || pe.Status != c.status {
				t.Errorf("SetReviewDecision = %+v, %v; want %s (HTTP %d) with hint %q", res, err, c.want.Class, c.status, c.hint)
			}
			var hm *provider.HeadMovedError
			if errors.As(err, &hm) && hm.Head != "" {
				t.Errorf("HeadMovedError.Head = %q, want unknown (no re-read)", hm.Head)
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			if got := g.mrReads - reads; got != c.rereads {
				t.Errorf("%d re-reads of the merge request, want %d", got, c.rereads)
			}
		})
	}
}
