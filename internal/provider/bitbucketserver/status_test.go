package bitbucketserver_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	mergeAPI   = prAPI + "/merge"
	vetoSecret = "VETOTEXT-SECRET-7f3a"
)

func breviewer(name, display, status, lastReviewed string) map[string]any {
	m := map[string]any{"user": map[string]any{"id": len(name), "name": name, "displayName": display},
		"role": "REVIEWER", "status": status, "approved": status == "APPROVED"}
	if lastReviewed != "" {
		m["lastReviewedCommit"] = lastReviewed
	}
	return m
}

func (f *fakeBBS) statusFixture(extra map[string]any, merge any) {
	pr := prJSON()
	for k, v := range extra {
		pr[k] = v
	}
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	f.handleJSON("GET", prAPI, pr)
	f.handleJSON("GET", mergeBase, map[string]any{"id": "mergesha"})
	if merge != nil {
		f.handleJSON("GET", mergeAPI, merge)
	}
}

func bbsStatus(t *testing.T, f *fakeBBS) *provider.ReviewStatus {
	t.Helper()
	p := f.provider(t, nil)
	pr, err := p.GetPullRequest(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	return p.GetReviewStatus(context.Background(), ref(), pr, provider.ReviewStatusOptions{})
}

func veto(summary string) map[string]any {
	return map[string]any{"summaryMessage": summary, "detailedMessage": vetoSecret + " " + summary}
}

// TestReviewStatusReviewers: statuses map to states; every listed reviewer
// is requested; a reviewed commit that is not the head makes a decided
// state stale; a pending reviewer is never stale.
func TestReviewStatusReviewers(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.statusFixture(map[string]any{
		"reviewers": []any{
			breviewer("amy", "Amy A", "APPROVED", "headsha"),
			breviewer("ben", "Ben B", "APPROVED", "oldsha"),
			breviewer("cat", "", "NEEDS_WORK", "headsha"),
			breviewer("dan", "", "UNAPPROVED", "oldsha"),
			breviewer("eve", "", "SOMETHING_NEW", ""),
		},
		// A participant that is not a reviewer is not listed.
		"participants": []any{map[string]any{"user": map[string]any{"name": "pat"}, "role": "PARTICIPANT", "status": "APPROVED"}},
	}, map[string]any{"canMerge": true, "conflicted": false, "vetoes": []any{}})
	st := bbsStatus(t, f)

	want := []struct {
		login string
		state provider.ReviewState
		stale bool
	}{
		{"amy", provider.ReviewApproved, false},
		{"ben", provider.ReviewApproved, true},
		{"cat", provider.ReviewChangesRequested, false},
		{"dan", provider.ReviewPending, false},
		{"eve", provider.ReviewPending, false},
	}
	if len(st.Reviewers) != len(want) {
		t.Fatalf("reviewers = %+v", st.Reviewers)
	}
	for i, w := range want {
		r := st.Reviewers[i]
		if r.User.Name != w.login || r.State != w.state || r.Stale != w.stale || !r.Requested || !r.At.IsZero() {
			t.Errorf("reviewer %d = %+v, want %+v (requested, no time)", i, r, w)
		}
	}
	if st.Reviewers[0].DisplayName != "Amy A" {
		t.Errorf("display name = %q", st.Reviewers[0].DisplayName)
	}
	if st.Mergeable == nil || !*st.Mergeable || len(st.MergeBlockers) != 0 || st.RequiredApprovals != nil {
		t.Errorf("merge = %v %v required %v", st.Mergeable, st.MergeBlockers, st.RequiredApprovals)
	}
}

// TestReviewStatusKnownVetoes: known vetoes map to the fixed blockers, raw
// server text never appears, and a stated number is the required approvals.
func TestReviewStatusKnownVetoes(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.statusFixture(nil, map[string]any{
		"canMerge": false, "conflicted": true,
		"vetoes": []any{
			veto("Requires 2 approvals"),
			veto("Not all required builds are successful yet"),
			veto("Some reviewers marked this pull request as Needs work"),
			veto("Requires 2 approvals"), // duplicates collapse
		},
	})
	st := bbsStatus(t, f)
	want := []string{provider.BlockerConflict, provider.BlockerApprovals, provider.BlockerBuilds, provider.BlockerNeedsWork}
	if st.Mergeable == nil || *st.Mergeable || strings.Join(st.MergeBlockers, "|") != strings.Join(want, "|") {
		t.Errorf("mergeable %v blockers %v, want %v", st.Mergeable, st.MergeBlockers, want)
	}
	if st.RequiredApprovals == nil || *st.RequiredApprovals != 2 {
		t.Errorf("required approvals = %v, want 2", st.RequiredApprovals)
	}
	if strings.Contains(fmt.Sprintf("%+v", st), vetoSecret) {
		t.Error("veto text reached the status")
	}
}

// TestReviewStatusUnknownVetoIsOtherMergeCheck: [canary 3] an unknown veto is
// the fixed "other merge check", never its server text.
func TestReviewStatusUnknownVetoIsOtherMergeCheck(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.statusFixture(nil, map[string]any{
		"canMerge": false,
		"vetoes":   []any{veto("Custom plugin says no " + "SECRETPLUGIN")},
	})
	st := bbsStatus(t, f)
	if len(st.MergeBlockers) != 1 || st.MergeBlockers[0] != provider.BlockerOtherCheck {
		t.Errorf("blockers = %v, want [%s]", st.MergeBlockers, provider.BlockerOtherCheck)
	}
	if s := fmt.Sprintf("%+v", st); strings.Contains(s, vetoSecret) || strings.Contains(s, "SECRETPLUGIN") {
		t.Errorf("raw veto text in %s", s)
	}
}

// TestReviewStatusRequiredApprovalsNeverGuessed: a veto that states a
// remaining count, or no number, gives nil.
func TestReviewStatusRequiredApprovalsNeverGuessed(t *testing.T) {
	for _, summary := range []string{
		"Needs 1 more approval", "Not enough approvals", "2 additional approvals required",
		"Not all required reviewers have approved yet",
	} {
		f := newFake(t, "/bitbucket")
		f.statusFixture(nil, map[string]any{"canMerge": false, "vetoes": []any{veto(summary)}})
		st := bbsStatus(t, f)
		if st.RequiredApprovals != nil {
			t.Errorf("%q: required approvals = %d, want nil", summary, *st.RequiredApprovals)
		}
		if len(st.MergeBlockers) != 1 || st.MergeBlockers[0] != provider.BlockerApprovals {
			t.Errorf("%q: blockers = %v", summary, st.MergeBlockers)
		}
	}
	f := newFake(t, "/bitbucket")
	f.statusFixture(nil, map[string]any{"canMerge": false, "vetoes": []any{veto("At least 3 approvals required")}})
	if st := bbsStatus(t, f); st.RequiredApprovals == nil || *st.RequiredApprovals != 3 {
		t.Errorf("stated total: %v", st.RequiredApprovals)
	}
}

// TestReviewStatusMergeUnreadable: a failing merge call leaves Mergeable nil
// with the fixed note; the reviewers are still there.
func TestReviewStatusMergeUnreadable(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.statusFixture(map[string]any{"reviewers": []any{breviewer("amy", "", "APPROVED", "headsha")}}, nil)
	f.handle("GET", mergeAPI, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, vetoSecret, http.StatusForbidden) })
	st := bbsStatus(t, f)
	if st.Mergeable != nil || len(st.Notes) != 1 || st.Notes[0] != provider.NoteMergeUnreadable || len(st.Reviewers) != 1 {
		t.Errorf("status = %+v", st)
	}
	if strings.Contains(fmt.Sprintf("%+v", st), vetoSecret) {
		t.Error("server text in status")
	}
}

// TestReviewStatusMergedPRHasNoMergeCall: a merged PR is not asked for its
// merge status (the fake fails the test on an unexpected request).
func TestReviewStatusMergedPRHasNoMergeCall(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.statusFixture(map[string]any{"state": "MERGED"}, nil)
	st := bbsStatus(t, f)
	if st.Mergeable != nil || len(st.Notes) != 0 {
		t.Errorf("status = %+v", st)
	}
	pr, err := f.provider(t, nil).GetPullRequest(context.Background(), ref())
	if err != nil || !pr.Merged {
		t.Errorf("merged = %v, err %v", pr != nil && pr.Merged, err)
	}
}
