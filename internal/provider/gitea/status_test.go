package gitea_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

const (
	protectionAPI = repoAPI + "/branch_protections/main"
	reviewBodyMk  = "REVIEWBODY-MARKER-5d1e"
)

// statusMe is the token's user in these tests.
var statusMe = provider.User{ID: "42", Name: "review-bot"}

func guser(id int, login, full string) map[string]any {
	return map[string]any{"id": id, "login": login, "full_name": full}
}

func greview(id int, user map[string]any, state string, sec int, extra map[string]any) map[string]any {
	m := map[string]any{"id": id, "user": user, "state": state, "body": reviewBodyMk,
		"submitted_at": ts(sec), "updated_at": ts(sec)}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func openPR(extra map[string]any) map[string]any {
	m := prJSON("mergesha")
	m["mergeable"] = true
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func ownMark(string) string { return review.FingerprintMarker("0123456789ab") }

// statusFixture registers a PR with a mix of reviews.
func (f *fakeGitea) statusFixture(reviews []any, requested []any, protection func(http.ResponseWriter, *http.Request)) {
	f.handleJSON("GET", prAPI, openPR(map[string]any{"requested_reviewers": requested}))
	f.handlePages(reviewsAPI, reviews)
	if protection != nil {
		f.handle("GET", protectionAPI, protection)
	}
}

func reviewStatus(t *testing.T, f *fakeGitea, me *provider.User) *provider.ReviewStatus {
	t.Helper()
	p := f.provider(t, nil)
	pr, err := p.GetPullRequest(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	return p.GetReviewStatus(context.Background(), ref(), pr, provider.ReviewStatusOptions{Me: me, IsOwn: review.IsMarkedBody})
}

func byLogin(st *provider.ReviewStatus) map[string]provider.Reviewer {
	out := map[string]provider.Reviewer{}
	for _, r := range st.Reviewers {
		out[r.User.Name] = r
	}
	return out
}

func protectionJSON(n int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"rule_name":"main","required_approvals":%d}`, n)
	}
}

// TestReviewStatusStates: approved, changes requested, stale approval, a
// requested-but-silent reviewer, a comment-only reviewer, and the counts of
// required approvals.
func TestReviewStatusStates(t *testing.T) {
	f := newFake(t, "")
	alice, bob, carol, dave, gina := guser(1, "alice", "Alice A"), guser(2, "bob", ""), guser(3, "carol", "Carol C"), guser(4, "dave", ""), guser(7, "gina", "")
	f.statusFixture(
		[]any{
			greview(10, alice, "APPROVED", 10, map[string]any{"stale": true, "official": true}),
			greview(11, bob, "APPROVED", 5, nil),
			greview(12, bob, "REQUEST_CHANGES", 20, nil), // the latest decisive review wins
			greview(13, bob, "COMMENT", 30, nil),         // a later comment does not undo it
			greview(14, gina, "COMMENT", 40, nil),
			greview(15, dave, "PENDING", 50, nil), // other users' drafts are not reported
		},
		[]any{carol, dave}, protectionJSON(2))
	st := reviewStatus(t, f, &statusMe)

	got := byLogin(st)
	if len(st.Reviewers) != 5 {
		t.Fatalf("reviewers = %+v, want alice, bob, gina, carol, dave", st.Reviewers)
	}
	check := func(login string, state provider.ReviewState, stale, requested bool) {
		t.Helper()
		r, ok := got[login]
		if !ok || r.State != state || r.Stale != stale || r.Requested != requested {
			t.Errorf("%s = %+v, want state %s stale %v requested %v", login, r, state, stale, requested)
		}
	}
	check("alice", provider.ReviewApproved, true, false)
	check("bob", provider.ReviewChangesRequested, false, false)
	check("gina", provider.ReviewCommented, false, false)
	check("carol", provider.ReviewPending, false, true)
	check("dave", provider.ReviewPending, false, true)
	if got["alice"].DisplayName != "Alice A" || got["alice"].At.IsZero() || !got["carol"].At.IsZero() {
		t.Errorf("display name or time wrong: %+v %+v", got["alice"], got["carol"])
	}
	// Reviewers with a verdict come first (oldest first), the silent ones last.
	var order []string
	for _, r := range st.Reviewers {
		order = append(order, r.User.Name)
	}
	if strings.Join(order, ",") != "alice,bob,gina,carol,dave" {
		t.Errorf("order = %v", order)
	}
	if st.RequiredApprovals == nil || *st.RequiredApprovals != 2 {
		t.Errorf("required approvals = %v, want 2", st.RequiredApprovals)
	}
	if st.Mergeable == nil || !*st.Mergeable || len(st.MergeBlockers) != 0 || len(st.Notes) != 0 {
		t.Errorf("merge = %v %v notes %v", st.Mergeable, st.MergeBlockers, st.Notes)
	}
	if strings.Contains(fmt.Sprintf("%+v", st), reviewBodyMk) {
		t.Error("a review body reached the status")
	}
}

// TestReviewStatusDismissedReviewDoesNotCount: [canary 2] a dismissed
// approval is no verdict. Its author is not listed unless requested; a
// dismissed approval followed by a comment is a comment.
func TestReviewStatusDismissedReviewDoesNotCount(t *testing.T) {
	f := newFake(t, "")
	erin, frank, hal := guser(5, "erin", ""), guser(6, "frank", ""), guser(8, "hal", "")
	f.statusFixture(
		[]any{
			greview(20, erin, "APPROVED", 10, map[string]any{"dismissed": true}),
			greview(21, frank, "APPROVED", 10, map[string]any{"dismissed": true}),
			greview(22, frank, "COMMENT", 20, nil),
			greview(23, hal, "REQUEST_CHANGES", 10, map[string]any{"dismissed": true}),
		},
		[]any{hal}, protectionJSON(1))
	st := reviewStatus(t, f, &statusMe)

	got := byLogin(st)
	if _, ok := got["erin"]; ok {
		t.Errorf("erin (only a dismissed approval) is listed: %+v", got["erin"])
	}
	if r := got["frank"]; r.State != provider.ReviewCommented {
		t.Errorf("frank = %+v, want commented", r)
	}
	if r := got["hal"]; r.State != provider.ReviewPending || !r.Requested {
		t.Errorf("hal = %+v, want pending and requested (his changes request was dismissed)", r)
	}
	for _, r := range st.Reviewers {
		if r.State == provider.ReviewApproved || r.State == provider.ReviewChangesRequested {
			t.Errorf("a dismissed review counts: %+v", r)
		}
	}
}

// TestReviewStatusExcludesReviewMCPsOwnReview: [canary 1] a COMMENT review of
// the token's user whose comments carry the fingerprint marker (the inline
// batch, empty body) is review-mcp's activity, not a reviewer. A plain human
// review from the same account is a reviewer, and so is the same review when
// the token's user is unknown (nothing can be excluded then).
func TestReviewStatusExcludesReviewMCPsOwnReview(t *testing.T) {
	f := newFake(t, "")
	bot := guser(42, "review-bot", "")
	other := guser(9, "ivy", "")
	f.statusFixture(
		[]any{
			greview(30, bot, "COMMENT", 10, map[string]any{"body": ""}),
			greview(31, other, "APPROVED", 20, nil),
		},
		nil, protectionJSON(1))
	f.handlePages(reviewCommentsAPI(30), []any{
		map[string]any{"id": 300, "body": "finding text\n\n" + ownMark(""), "path": "a.go", "position": 1, "user": bot},
		map[string]any{"id": 301, "body": "second finding\n\n" + ownMark(""), "path": "a.go", "position": 2, "user": bot},
	})
	st := reviewStatus(t, f, &statusMe)
	if got := byLogin(st); len(got) != 1 || got["ivy"].State != provider.ReviewApproved {
		t.Fatalf("reviewers = %+v, want only ivy approved (review-mcp's own review excluded)", st.Reviewers)
	}

	// Without the token's user nothing is excluded.
	f2 := newFake(t, "")
	f2.statusFixture([]any{greview(30, bot, "COMMENT", 10, map[string]any{"body": ""})}, nil, protectionJSON(1))
	st = reviewStatus(t, f2, nil)
	if got := byLogin(st); got["review-bot"].State != provider.ReviewCommented {
		t.Errorf("without Me: %+v", st.Reviewers)
	}
}

// TestReviewStatusSameAccountHumanReviewIsAReviewer: the token's user's own
// plain reviews count: an approval (never review-mcp's, which only comments)
// and a COMMENT review without markers; a marked review next to them is
// still excluded.
func TestReviewStatusSameAccountHumanReviewIsAReviewer(t *testing.T) {
	f := newFake(t, "")
	bot := guser(42, "review-bot", "Bot")
	f.statusFixture(
		[]any{
			greview(40, bot, "COMMENT", 10, map[string]any{"body": ""}), // marked batch
			greview(41, bot, "APPROVED", 20, nil),                       // a human approval by the same account
		},
		nil, protectionJSON(1))
	f.handlePages(reviewCommentsAPI(40), []any{
		map[string]any{"id": 400, "body": "finding\n\n" + ownMark(""), "user": bot},
	})
	st := reviewStatus(t, f, &statusMe)
	if r := byLogin(st)["review-bot"]; len(st.Reviewers) != 1 || r.State != provider.ReviewApproved {
		t.Fatalf("reviewers = %+v, want review-bot approved", st.Reviewers)
	}

	// A COMMENT review of the same account without a marker, in the body or
	// in its comments, is a reviewer who commented.
	f2 := newFake(t, "")
	f2.statusFixture([]any{greview(50, bot, "COMMENT", 10, nil)}, nil, protectionJSON(1))
	f2.handlePages(reviewCommentsAPI(50), []any{
		map[string]any{"id": 500, "body": "plain human remark", "user": bot},
	})
	st = reviewStatus(t, f2, &statusMe)
	if r := byLogin(st)["review-bot"]; r.State != provider.ReviewCommented {
		t.Errorf("plain comment review = %+v", st.Reviewers)
	}
}

// TestReviewStatusProtectionUnreadable: a 403 and a 404 on the protection
// read give nil required approvals and no failure; the rest is intact.
func TestReviewStatusProtectionUnreadable(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			f := newFake(t, "")
			f.statusFixture([]any{greview(60, guser(1, "alice", ""), "APPROVED", 10, nil)}, nil,
				func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "SERVERTEXT-"+reviewBodyMk, code) })
			st := reviewStatus(t, f, &statusMe)
			if st.RequiredApprovals != nil {
				t.Errorf("required approvals = %d, want nil", *st.RequiredApprovals)
			}
			if len(st.Reviewers) != 1 || strings.Contains(fmt.Sprintf("%+v", st), reviewBodyMk) {
				t.Errorf("status = %+v", st)
			}
		})
	}
}

// TestReviewStatusReviewsUnreadable: the reviews failing leaves the reviewers
// nil with the fixed note, and the protection is still read.
func TestReviewStatusReviewsUnreadable(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", prAPI, openPR(nil))
	f.handle("GET", reviewsAPI, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) })
	f.handle("GET", protectionAPI, protectionJSON(3))
	st := reviewStatus(t, f, &statusMe)
	if st.Reviewers != nil || len(st.Notes) != 1 || st.Notes[0] != provider.NoteReviewsUnreadable {
		t.Errorf("reviewers %v notes %v", st.Reviewers, st.Notes)
	}
	if st.RequiredApprovals == nil || *st.RequiredApprovals != 3 {
		t.Errorf("required approvals = %v", st.RequiredApprovals)
	}
}

// TestReviewStatusMergeableOnlyForOpenPRs: a merged PR has no merge verdict,
// and a PR without the field has none either; Gitea gives no blockers.
func TestReviewStatusMergeableOnlyForOpenPRs(t *testing.T) {
	f := newFake(t, "")
	f.statusFixture(nil, nil, protectionJSON(0))
	st := reviewStatus(t, f, &statusMe)
	if st.Mergeable == nil || !*st.Mergeable || len(st.MergeBlockers) != 0 {
		t.Errorf("open: %v %v", st.Mergeable, st.MergeBlockers)
	}
	if st.RequiredApprovals == nil || *st.RequiredApprovals != 0 {
		t.Errorf("a protection with 0 approvals gives 0, got %v", st.RequiredApprovals)
	}

	f = newFake(t, "")
	f.handleJSON("GET", prAPI, openPR(map[string]any{"state": "closed", "merged": true, "mergeable": false}))
	f.handlePages(reviewsAPI, []any{})
	f.handle("GET", protectionAPI, protectionJSON(1))
	st = reviewStatus(t, f, &statusMe)
	if st.Mergeable != nil {
		t.Errorf("merged PR: mergeable = %v, want nil", *st.Mergeable)
	}
}

// TestPullRequestCarriesDraftAndMerged: the PR fields pr_info reads.
func TestPullRequestCarriesDraftAndMerged(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", prAPI, openPR(map[string]any{"draft": true, "state": "closed", "merged": true}))
	pr, err := f.provider(t, nil).GetPullRequest(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	if pr.Draft == nil || !*pr.Draft || !pr.Merged {
		t.Errorf("draft %v merged %v", pr.Draft, pr.Merged)
	}
}
