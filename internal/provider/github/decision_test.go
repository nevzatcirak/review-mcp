package github_test

import (
	"errors"
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

// TestDecisionCaps pins the decision capabilities of V-14 spec §1.4.
func TestDecisionCaps(t *testing.T) {
	p, _, _ := serveCt(t, decisionSpec())
	want := provider.DecisionCaps{Approve: true, RequestChanges: true, BodyInReview: true, RequestChangesNeedsBody: true}
	if got := p.Capabilities().Decisions; got != want {
		t.Errorf("Decisions = %+v, want %+v", got, want)
	}
}

// TestDecisionUnsubmittedReview: an answer whose state is not the one the
// event submits (PENDING: the review was not submitted) is a protocol
// error.
func TestDecisionUnsubmittedReview(t *testing.T) {
	for _, state := range []string{"PENDING", "COMMENTED", "CHANGES_REQUESTED", "approved"} {
		t.Run(state, func(t *testing.T) {
			spec := decisionSpec()
			p, ref, g := serveCt(t, spec)
			g.mu.Lock()
			g.answerState = state
			g.mu.Unlock()
			pr, err := p.GetPullRequest(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			res, err := p.(provider.DecisionWriter).SetReviewDecision(t.Context(), ref, pr,
				provider.DecisionRequest{Decision: provider.DecisionApprove, HeadSHA: spec.HeadSHA})
			if !errors.Is(err, provider.ErrProtocol) || res != nil {
				t.Errorf("answer %q: SetReviewDecision = %+v, %v; want a protocol error", state, res, err)
			}
		})
	}
}
