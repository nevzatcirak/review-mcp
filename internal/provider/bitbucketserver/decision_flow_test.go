package bitbucketserver_test

import (
	"strconv"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// flowResolver hands pr_review_decision one served provider.
type flowResolver struct {
	p   provider.Provider
	ref provider.PRRef
}

func (r flowResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	ref := r.ref
	ref.URL = u
	return ref, r.p, nil
}

// flowDecide runs pr_review_decision through the contract fake.
func flowDecide(t *testing.T, r flowResolver, d, head string) tools.PRReviewDecisionResult {
	t.Helper()
	cfg := config.Defaults()
	cfg.Decision.Enabled = true
	res, err := tools.PRReviewDecision(t.Context(), cfg, r, tools.PRReviewDecisionArgs{
		PRURL: "https://bitbucket.example.com/projects/PROJ/repos/demo/pull-requests/7", Decision: d, HeadSHA: head})
	if err != nil {
		t.Fatalf("%s: %v", d, err)
	}
	return res
}

// TestDecisionParticipantFlow (v2.7 spec V-14 §2.7 commit 3): the token's
// user is not a requested reviewer, so their decision makes them a
// participant. The review status lists them, not requested, so step 8
// sees their state: a second approval is unchanged, and clear after their
// approval writes UNAPPROVED instead of answering unchanged.
func TestDecisionParticipantFlow(t *testing.T) {
	spec := decisionSpec()
	var fake *ctBBS
	p, ref := serveCtBBS(t, spec, func(b *ctBBS) { fake = b })
	r := flowResolver{p: p, ref: ref}

	if res := flowDecide(t, r, "approve", spec.HeadSHA); res.Unchanged || res.State != "APPROVED" {
		t.Fatalf("approve: %+v", res)
	}
	pr, err := p.GetPullRequest(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	me := provider.User{ID: strconv.FormatInt(spec.TokenUser.ID, 10), Name: spec.TokenUser.Login}
	st := p.GetReviewStatus(t.Context(), ref, pr, provider.ReviewStatusOptions{Me: &me, IsOwn: review.IsMarkedBody})
	if len(st.Reviewers) != 1 || st.Reviewers[0].User != me || st.Reviewers[0].State != provider.ReviewApproved ||
		st.Reviewers[0].Requested || st.Reviewers[0].Stale {
		t.Fatalf("the participant's approval is not listed as theirs, not requested: %+v", st.Reviewers)
	}
	if res := flowDecide(t, r, "approve", spec.HeadSHA); !res.Unchanged {
		t.Errorf("a second approval wrote: %+v", res)
	}
	if res := flowDecide(t, r, "clear", spec.HeadSHA); res.Unchanged || res.State != "UNAPPROVED" {
		t.Errorf("clear after the participant's approval: %+v, want UNAPPROVED written", res)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.puts != 2 || fake.status != "UNAPPROVED" {
		t.Errorf("%d participant updates, status %q; want 2 and UNAPPROVED", fake.puts, fake.status)
	}
}
