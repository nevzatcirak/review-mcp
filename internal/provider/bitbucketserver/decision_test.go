package bitbucketserver_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

// decisionSpec is a small pull request for the decision tests.
func decisionSpec() contract.Spec {
	return contract.Spec{
		Title: "T", Author: contract.User{ID: 101, Login: "alice"}, SourceBranch: "f", TargetBranch: "main",
		HeadSHA: "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1", BaseSHA: "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2",
		TokenUser: contract.User{ID: 900, Login: "review-bot"},
		Env:       contract.Env{Token: "tok-FAKE-wp18a", MaxFiles: 10, MaxFileBytes: 4096}, //nolint:gosec // synthetic test value
	}
}

// TestDecisionSlug: the participant is named by the slug of the entry
// whose name is the token user's name exactly and whose id is the token
// user's id; without such an entry the decision is a protocol error with a
// fixed hint, and no participant update is sent.
func TestDecisionSlug(t *testing.T) {
	spec := decisionSpec()
	for _, c := range []struct {
		name  string
		users []any
	}{
		{"no_entry", []any{}},
		{"name_in_another_case", []any{map[string]any{"id": 900, "name": "Review-Bot", "slug": "review_bot-slug"}}},
		{"name_contains_it", []any{map[string]any{"id": 900, "name": "review-bot-two", "slug": "review_bot-slug"}}},
		{"another_id", []any{map[string]any{"id": 901, "name": "review-bot", "slug": "review_bot-slug"}}},
		{"no_slug", []any{map[string]any{"id": 900, "name": "review-bot"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var fake *ctBBS
			p, ref := serveCtBBS(t, spec, func(b *ctBBS) { b.users, fake = c.users, b })
			pr, err := p.GetPullRequest(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			res, err := p.(provider.DecisionWriter).SetReviewDecision(t.Context(), ref, pr,
				provider.DecisionRequest{Decision: provider.DecisionApprove, HeadSHA: spec.HeadSHA})
			if !errors.Is(err, provider.ErrProtocol) || res != nil || !strings.HasSuffix(err.Error(), ": the token's user slug could not be read") {
				t.Errorf("SetReviewDecision = %+v, %v; want a protocol error about the slug", res, err)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if fake.puts != 0 {
				t.Errorf("%d participant updates were sent, want none", fake.puts)
			}
		})
	}
}

// TestDecisionAnswerStatus: a 2xx answer whose status field names another
// status is a protocol error.
func TestDecisionAnswerStatus(t *testing.T) {
	spec := decisionSpec()
	p, ref := serveCtBBS(t, spec, func(b *ctBBS) { b.answerStatus = "UNAPPROVED" })
	pr, err := p.GetPullRequest(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.(provider.DecisionWriter).SetReviewDecision(t.Context(), ref, pr,
		provider.DecisionRequest{Decision: provider.DecisionApprove, HeadSHA: spec.HeadSHA, Body: "Looks good."})
	if !errors.Is(err, provider.ErrProtocol) || res != nil {
		t.Errorf("SetReviewDecision = %+v, %v; want a protocol error", res, err)
	}
}
