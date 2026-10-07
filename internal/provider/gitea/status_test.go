package gitea_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

const (
	protectionAPI = repoAPI + "/branch_protections"
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

// rulesJSON serves the protection rules as one page; page 2 is empty.
func rulesJSON(rules ...any) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") != "1" {
			_, _ = fmt.Fprint(w, "[]")
			return
		}
		_ = json.NewEncoder(w).Encode(rules)
	}
}

func rule(name string, n int) map[string]any {
	return map[string]any{"rule_name": name, "required_approvals": n}
}

func protectionJSON(n int) func(http.ResponseWriter, *http.Request) {
	return rulesJSON(rule("main", n))
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

// TestReviewStatusProtectionUnreadable: a 401, 403, 404 or 500 on the rules
// list gives nil required approvals with the "not readable" note and no
// failure; the rest is intact.
func TestReviewStatusProtectionUnreadable(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			f := newFake(t, "")
			f.statusFixture([]any{greview(60, guser(1, "alice", ""), "APPROVED", 10, nil)}, nil,
				func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "SERVERTEXT-"+reviewBodyMk, code) })
			st := reviewStatus(t, f, &statusMe)
			if st.RequiredApprovals != nil {
				t.Errorf("required approvals = %d, want nil", *st.RequiredApprovals)
			}
			if st.RequiredApprovalsNote != provider.NoteApprovalsUnreadable {
				t.Errorf("note = %q", st.RequiredApprovalsNote)
			}
			if len(st.Reviewers) != 1 || strings.Contains(fmt.Sprintf("%+v", st), reviewBodyMk) {
				t.Errorf("status = %+v", st)
			}
		})
	}
}

func protectionStatus(t *testing.T, h func(http.ResponseWriter, *http.Request)) *provider.ReviewStatus {
	t.Helper()
	f := newFake(t, "")
	f.statusFixture(nil, nil, h)
	return reviewStatus(t, f, &statusMe)
}

// TestReviewStatusProtectionRules: the rule is chosen from the list by name or
// pattern (the target is "main").
func TestReviewStatusProtectionRules(t *testing.T) {
	cases := []struct {
		name  string
		rules []any
		want  *int // nil: required approvals nil
		note  string
	}{
		{"exact rule", []any{rule("dev", 5), rule("main", 2)}, ptr(2), ""},
		{"exact rule beats an earlier pattern", []any{rule("m*", 5), rule("main", 2)}, ptr(2), ""},
		{"legacy branch_name", []any{map[string]any{"branch_name": "main", "required_approvals": 4}}, ptr(4), ""},
		{"pattern rule", []any{rule("release/*", 5), rule("m*n", 3)}, ptr(3), ""},
		{"first matching pattern", []any{rule("ma*", 1), rule("m*", 2)}, ptr(1), ""},
		{"exact rule with 0", []any{rule("main", 0)}, ptr(0), ""},
		{"no matching rule", []any{rule("release/*", 5), rule("dev", 2)}, ptr(0), provider.NoteNoProtectionRule},
		{"no rules at all", []any{}, ptr(0), provider.NoteNoProtectionRule},
		{"unevaluable pattern", []any{rule("[", 5), rule("dev", 2)}, nil, provider.NoteProtectionPatternUnevaluable},
		{"a match stands despite an unevaluable pattern", []any{rule("[", 5), rule("main", 2)}, ptr(2), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := protectionStatus(t, rulesJSON(c.rules...))
			switch {
			case c.want == nil && st.RequiredApprovals != nil:
				t.Errorf("required approvals = %d, want nil", *st.RequiredApprovals)
			case c.want != nil && (st.RequiredApprovals == nil || *st.RequiredApprovals != *c.want):
				t.Errorf("required approvals = %v, want %d", st.RequiredApprovals, *c.want)
			}
			if st.RequiredApprovalsNote != c.note {
				t.Errorf("note = %q, want %q", st.RequiredApprovalsNote, c.note)
			}
		})
	}
}

// TestReviewStatusProtectionDoubleStar: "**" is not evaluated, because
// path.Match would read it as "*" and wrongly say that no rule applies to
// release/a/b.
func TestReviewStatusProtectionDoubleStar(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", prAPI, openPR(map[string]any{"base": map[string]any{"ref": "release/a/b", "sha": "basesha"}}))
	f.handlePages(reviewsAPI, []any{})
	f.handle("GET", protectionAPI, rulesJSON(rule("release/**", 2), rule("dev", 1)))
	st := reviewStatus(t, f, &statusMe)
	if st.RequiredApprovals != nil || st.RequiredApprovalsNote != provider.NoteProtectionPatternUnevaluable {
		t.Errorf("approvals %v note %q", st.RequiredApprovals, st.RequiredApprovalsNote)
	}
}

func ptr(n int) *int { return &n }

// TestReviewStatusProtectionPaged: a rule on the second page is found.
func TestReviewStatusProtectionPaged(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", prAPI, openPR(nil))
	f.handlePages(reviewsAPI, []any{})
	f.handlePages(protectionAPI, []any{rule("dev", 1)}, []any{rule("main", 3)})
	st := reviewStatus(t, f, &statusMe)
	if st.RequiredApprovals == nil || *st.RequiredApprovals != 3 {
		t.Errorf("required approvals = %v, want 3", st.RequiredApprovals)
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
