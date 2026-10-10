package github

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	stHead = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	stOld  = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
	// stMarker is the marker the tests' IsOwn looks for.
	stMarker = "<!-- own-marker-wp2m -->"
)

func stReview(id int, login string, uid int, state, commit, at, body string) map[string]any {
	return map[string]any{"id": id, "user": user(login, uid), "state": state, "commit_id": commit,
		"submitted_at": at, "body": body}
}

// stPR is the payload GetReviewStatus reads again.
func stPR(reviewers, teams []any, commits int) map[string]any {
	return map[string]any{"requested_reviewers": reviewers, "requested_teams": teams, "commits": commits}
}

func stOpts() provider.ReviewStatusOptions {
	return provider.ReviewStatusOptions{
		Me:    &provider.User{ID: "900", Name: "review-bot"},
		IsOwn: func(b string) bool { return strings.Contains(b, stMarker) },
	}
}

func stOpen(state string) *provider.PullRequest {
	t := true
	return &provider.PullRequest{State: "open", HeadSHA: stHead, TargetBranch: "main", Mergeable: &t, MergeableState: state}
}

// stStatusFake serves a payload and reviews; the rules list is empty and the
// classic protection is a 404 unless the test registers them.
func stStatusFake(t *testing.T, payload map[string]any, reviews []any) *fake {
	t.Helper()
	f := newFake(t, "/api/v3")
	f.json("/repos/octo/demo/pulls/7", payload)
	f.json("/repos/octo/demo/pulls/7/reviews", reviews)
	f.json("/repos/octo/demo/rules/branches/main", []any{})
	f.handle(http.MethodGet, "/repos/octo/demo/branches/main/protection", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, 404) })
	return f
}

func stByLogin(rs []provider.Reviewer) map[string]provider.Reviewer {
	m := map[string]provider.Reviewer{}
	for _, r := range rs {
		m[r.User.Name] = r
	}
	return m
}

// TestReviewFolding: per user the latest APPROVED or CHANGES_REQUESTED
// decides, DISMISSED never counts, COMMENTED counts only without a decisive
// review, PENDING is not reported, a review on another commit is stale, and
// requested reviewers without a verdict are pending.
func TestReviewFolding(t *testing.T) {
	f := stStatusFake(t,
		stPR([]any{user("heidi", 8), user("ivan", 9)}, []any{map[string]any{"slug": "core", "name": "Core team"}}, 3),
		[]any{
			// alice: approved, then changes requested later: the later decides.
			stReview(1, "alice", 1, "APPROVED", stHead, "2026-01-01T01:00:00Z", ""),
			stReview(2, "alice", 1, "CHANGES_REQUESTED", stHead, "2026-01-01T02:00:00Z", ""),
			// bob: approved on an older commit, then only comments: the
			// approval stands (stale).
			stReview(3, "bob", 2, "APPROVED", stOld, "2026-01-01T03:00:00Z", ""),
			stReview(4, "bob", 2, "COMMENTED", stHead, "2026-01-01T04:00:00Z", ""),
			// carol: comments only.
			stReview(5, "carol", 3, "COMMENTED", stHead, "2026-01-01T05:00:00Z", ""),
			// dan: only a dismissed review.
			stReview(6, "dan", 4, "DISMISSED", stHead, "2026-01-01T06:00:00Z", ""),
			// erin: changes requested, then a later dismissed review. The
			// dismissed one is not a verdict: changes requested stands.
			stReview(7, "erin", 5, "CHANGES_REQUESTED", stHead, "2026-01-01T07:00:00Z", ""),
			stReview(8, "erin", 5, "DISMISSED", stHead, "2026-01-01T08:00:00Z", ""),
			// frank: a pending draft is not reported.
			stReview(9, "frank", 6, "PENDING", stHead, "", ""),
			// heidi was asked again after commenting: still requested.
			stReview(10, "heidi", 8, "COMMENTED", stHead, "2026-01-01T09:00:00Z", ""),
		})
	p, _ := f.provider(f.config(""), time.Now())
	st := p.GetReviewStatus(t.Context(), testRef(), stOpen("clean"), stOpts())
	if st.Reviewers == nil {
		t.Fatalf("no reviewers: %+v", st)
	}
	got := stByLogin(st.Reviewers)
	for name, want := range map[string]struct {
		state     provider.ReviewState
		stale     bool
		requested bool
	}{
		"alice": {provider.ReviewChangesRequested, false, false},
		"bob":   {provider.ReviewApproved, true, false}, // the stale approval beats the later comment
		"carol": {provider.ReviewCommented, false, false},
		"erin":  {provider.ReviewChangesRequested, false, false},
		"heidi": {provider.ReviewCommented, false, true},
		"ivan":  {provider.ReviewPending, false, false}, // see below
	} {
		r, ok := got[name]
		if !ok {
			t.Errorf("%s is not listed", name)
			continue
		}
		if want.state == provider.ReviewPending {
			want.requested = true
		}
		if r.State != want.state || r.Stale != want.stale || r.Requested != want.requested {
			t.Errorf("%s: state %q stale %v requested %v, want %q %v %v", name, r.State, r.Stale, r.Requested, want.state, want.stale, want.requested)
		}
	}
	for _, name := range []string{"dan", "frank"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s is listed, want left out", name)
		}
	}
	team, ok := got["@octo/core"]
	if !ok || team.State != provider.ReviewPending || !team.Requested || team.DisplayName != "Core team" || !team.At.IsZero() {
		t.Errorf("team reviewer = %+v (listed %v)", team, ok)
	}
	if len(st.Reviewers) != 7 {
		t.Errorf("%d reviewers, want 7: %+v", len(st.Reviewers), st.Reviewers)
	}
	if a := got["alice"]; !a.At.Equal(time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("alice At = %v, want the time of the deciding review", a.At)
	}
}

// TestReviewFoldingStale: a decisive review is stale when its commit_id is
// not the head; a missing commit_id or head is not stale.
func TestReviewFoldingStale(t *testing.T) {
	f := stStatusFake(t, stPR(nil, nil, 1), []any{
		stReview(1, "alice", 1, "APPROVED", stOld, "2026-01-01T01:00:00Z", ""),
		stReview(2, "bob", 2, "APPROVED", strings.ToUpper(stHead), "2026-01-01T02:00:00Z", ""),
		stReview(3, "carol", 3, "APPROVED", "", "2026-01-01T03:00:00Z", ""),
	})
	p, _ := f.provider(f.config(""), time.Now())
	got := stByLogin(p.GetReviewStatus(t.Context(), testRef(), stOpen("clean"), stOpts()).Reviewers)
	if !got["alice"].Stale || got["bob"].Stale || got["carol"].Stale {
		t.Errorf("stale: alice %v bob %v carol %v, want true false false", got["alice"].Stale, got["bob"].Stale, got["carol"].Stale)
	}
}

// TestReviewFoldingOwn: a COMMENTED review of the token's user that carries a
// marker in its body or in one of its comments is review-mcp's own and not
// listed; an unmarked one, a verdict and a review of another user are. A
// review whose comments cannot be read is left out with a note.
func TestReviewFoldingOwn(t *testing.T) {
	f := stStatusFake(t, stPR(nil, nil, 1), []any{
		stReview(10, "review-bot", 900, "COMMENTED", stHead, "2026-01-01T01:00:00Z", "Summary "+stMarker),
		stReview(11, "review-bot", 900, "COMMENTED", stHead, "2026-01-01T02:00:00Z", ""),      // marker in a comment
		stReview(12, "review-bot", 900, "COMMENTED", stHead, "2026-01-01T03:00:00Z", ""),      // no marker anywhere
		stReview(13, "other-bot", 901, "COMMENTED", stHead, "2026-01-01T04:00:00Z", stMarker), // not the token's user
	})
	f.json("/repos/octo/demo/pulls/7/reviews/11/comments", []any{map[string]any{"id": 1, "body": "Finding " + stMarker}})
	f.json("/repos/octo/demo/pulls/7/reviews/12/comments", []any{map[string]any{"id": 2, "body": "A human-like remark"}})
	p, _ := f.provider(f.config(""), time.Now())
	st := p.GetReviewStatus(t.Context(), testRef(), stOpen("clean"), stOpts())
	got := stByLogin(st.Reviewers)
	if r, ok := got["review-bot"]; !ok || r.State != provider.ReviewCommented || !r.At.Equal(time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("review-bot = %+v (listed %v): only the unmarked review counts", r, ok)
	}
	if _, ok := got["other-bot"]; !ok {
		t.Error("another user's marked review must still be listed")
	}
	if len(st.Notes) != 0 {
		t.Errorf("notes = %q", st.Notes)
	}

	// Without the token's user or without IsOwn nothing is excluded.
	st = p.GetReviewStatus(t.Context(), testRef(), stOpen("clean"), provider.ReviewStatusOptions{})
	if _, ok := stByLogin(st.Reviewers)["review-bot"]; !ok {
		t.Error("nothing may be excluded without Me and IsOwn")
	}

	// The comments of a review cannot be read: left out, with the note.
	f2 := stStatusFake(t, stPR(nil, nil, 1), []any{stReview(20, "review-bot", 900, "COMMENTED", stHead, "2026-01-01T01:00:00Z", "")})
	f2.handle(http.MethodGet, "/repos/octo/demo/pulls/7/reviews/20/comments", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, 500) })
	p2, _ := f2.provider(f2.config(""), time.Now())
	st = p2.GetReviewStatus(t.Context(), testRef(), stOpen("clean"), stOpts())
	if len(st.Reviewers) != 0 || !slices.Contains(st.Notes, provider.NoteActivityUnclassified) {
		t.Errorf("status = %+v, want no reviewer and %q", st, provider.NoteActivityUnclassified)
	}
}

// TestReviewsArePagedByLink: reviews come from every page of the Link header.
func TestReviewsArePagedByLink(t *testing.T) {
	f := stStatusFake(t, stPR(nil, nil, 1), nil)
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<http://`+r.Host+`/api/v3/repositories/42/pulls/7/reviews?cursor=2>; rel="next"`)
		writeJSON(w, []any{stReview(1, "alice", 1, "APPROVED", stHead, "2026-01-01T01:00:00Z", "")})
	})
	f.handle(http.MethodGet, "/repositories/42/pulls/7/reviews", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []any{stReview(2, "bob", 2, "CHANGES_REQUESTED", stHead, "2026-01-01T02:00:00Z", "")})
	})
	p, _ := f.provider(f.config(""), time.Now())
	got := stByLogin(p.GetReviewStatus(t.Context(), testRef(), stOpen("clean"), stOpts()).Reviewers)
	if got["alice"].State != provider.ReviewApproved || got["bob"].State != provider.ReviewChangesRequested {
		t.Errorf("reviewers = %+v", got)
	}
}

// TestBlockersFromMergeableState is the mapping table of mergeable_state to
// the fixed blocker texts. The state itself is never shown.
func TestBlockersFromMergeableState(t *testing.T) {
	for state, want := range map[string][]string{
		"clean":     {},
		"has_hooks": {},
		"unknown":   {},
		"":          {},
		"dirty":     {provider.BlockerConflict},
		"blocked":   {provider.BlockerRequirements},
		"behind":    {provider.BlockerBehind},
		"unstable":  {provider.BlockerBuilds},
		"draft":     {provider.BlockerDraft},
		"DIRTY":     {provider.BlockerConflict},
		"something": {provider.BlockerOtherCheck},
	} {
		f := stStatusFake(t, stPR(nil, nil, 1), []any{})
		p, _ := f.provider(f.config(""), time.Now())
		st := p.GetReviewStatus(t.Context(), testRef(), stOpen(state), stOpts())
		if !slices.Equal(st.MergeBlockers, want) || st.MergeBlockers == nil {
			t.Errorf("mergeable_state %q: blockers %q, want %q", state, st.MergeBlockers, want)
		}
	}
}

// TestMergeableOnlyForOpenPullRequests: the verdict is the PR's own, nil when
// GitHub has not computed it, and absent for a closed or merged PR (which
// has no blockers either).
func TestMergeableOnlyForOpenPullRequests(t *testing.T) {
	f := stStatusFake(t, stPR(nil, nil, 1), []any{})
	p, _ := f.provider(f.config(""), time.Now())
	yes, no := true, false

	pr := stOpen("dirty")
	pr.Mergeable = &no
	if st := p.GetReviewStatus(t.Context(), testRef(), pr, stOpts()); st.Mergeable == nil || *st.Mergeable {
		t.Errorf("Mergeable = %v, want false", st.Mergeable)
	}
	pr = stOpen("unknown")
	pr.Mergeable = nil
	if st := p.GetReviewStatus(t.Context(), testRef(), pr, stOpts()); st.Mergeable != nil || len(st.MergeBlockers) != 0 {
		t.Errorf("not computed yet: Mergeable %v blockers %q, want nil and none", st.Mergeable, st.MergeBlockers)
	}
	for name, mod := range map[string]func(*provider.PullRequest){
		"closed": func(pr *provider.PullRequest) { pr.State = "closed" },
		"merged": func(pr *provider.PullRequest) { pr.State, pr.Merged = "closed", true },
	} {
		pr = stOpen("dirty")
		pr.Mergeable = &yes
		mod(pr)
		if st := p.GetReviewStatus(t.Context(), testRef(), pr, stOpts()); st.Mergeable != nil || len(st.MergeBlockers) != 0 {
			t.Errorf("%s: Mergeable %v blockers %q, want nil and none", name, st.Mergeable, st.MergeBlockers)
		}
	}
}

func stHandleApprovals(f *fake, path string, status int, body any) {
	f.handle(http.MethodGet, path, func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			fakeError(w, status)
			return
		}
		writeJSON(w, body)
	})
}

func stRule(count int) map[string]any {
	return map[string]any{"type": "pull_request", "parameters": map[string]any{"required_approving_review_count": count}}
}

// TestRequiredApprovals: the rulesets first (the largest pull_request rule),
// then the classic protection, and nil with the fixed note whenever neither
// gave a count.
func TestRequiredApprovals(t *testing.T) {
	const rules, prot = "/repos/octo/demo/rules/branches/main", "/repos/octo/demo/branches/main/protection"
	for _, tc := range []struct {
		name  string
		rules func(f *fake)
		prot  func(f *fake)
		want  *int
		note  string
	}{
		{"max over rulesets", func(f *fake) {
			stHandleApprovals(f, rules, 200, []any{map[string]any{"type": "deletion"}, stRule(1), stRule(3), stRule(2)})
		}, nil, ptr(3), ""},
		{"a rule that asks for none", func(f *fake) { stHandleApprovals(f, rules, 200, []any{stRule(0)}) }, nil, ptr(0), ""},
		{"rules without pull_request, classic protection", func(f *fake) {
			stHandleApprovals(f, rules, 200, []any{map[string]any{"type": "deletion"}})
		}, func(f *fake) {
			stHandleApprovals(f, prot, 200, map[string]any{"required_pull_request_reviews": map[string]any{"required_approving_review_count": 2}})
		}, ptr(2), ""},
		{"no rules, classic protection", func(f *fake) { stHandleApprovals(f, rules, 200, []any{}) }, func(f *fake) {
			stHandleApprovals(f, prot, 200, map[string]any{"required_pull_request_reviews": map[string]any{"required_approving_review_count": 1}})
		}, ptr(1), ""},
		{"rules unavailable (older GHES), classic protection", func(f *fake) { stHandleApprovals(f, rules, 404, nil) }, func(f *fake) {
			stHandleApprovals(f, prot, 200, map[string]any{"required_pull_request_reviews": map[string]any{"required_approving_review_count": 4}})
		}, ptr(4), ""},
		{"classic protection without a review requirement", func(f *fake) { stHandleApprovals(f, rules, 200, []any{}) }, func(f *fake) {
			stHandleApprovals(f, prot, 200, map[string]any{"required_status_checks": map[string]any{}})
		}, ptr(0), ""},
		{"classic protection needs admin", func(f *fake) { stHandleApprovals(f, rules, 200, []any{}) }, func(f *fake) {
			stHandleApprovals(f, prot, 403, nil)
		}, nil, provider.NoteApprovalsUnreadable},
		{"not protected or not visible", func(f *fake) { stHandleApprovals(f, rules, 200, []any{}) }, func(f *fake) {
			stHandleApprovals(f, prot, 404, nil)
		}, nil, provider.NoteApprovalsUnreadable},
		{"both unreadable", func(f *fake) { stHandleApprovals(f, rules, 500, nil) }, func(f *fake) {
			stHandleApprovals(f, prot, 500, nil)
		}, nil, provider.NoteApprovalsUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := stStatusFake(t, stPR(nil, nil, 1), []any{})
			tc.rules(f)
			if tc.prot != nil {
				tc.prot(f)
			} else {
				f.handle(http.MethodGet, prot, func(http.ResponseWriter, *http.Request) {
					t.Error("the classic protection is read although the rulesets gave a count")
				})
			}
			p, _ := f.provider(f.config(""), time.Now())
			st := p.GetReviewStatus(t.Context(), testRef(), stOpen("clean"), stOpts())
			switch {
			case tc.want == nil && st.RequiredApprovals != nil:
				t.Errorf("RequiredApprovals = %d, want nil", *st.RequiredApprovals)
			case tc.want != nil && (st.RequiredApprovals == nil || *st.RequiredApprovals != *tc.want):
				t.Errorf("RequiredApprovals = %v, want %d", st.RequiredApprovals, *tc.want)
			}
			if st.RequiredApprovalsNote != tc.note {
				t.Errorf("note = %q, want %q", st.RequiredApprovalsNote, tc.note)
			}
		})
	}
}

func ptr(n int) *int { return &n }

// TestRequiredApprovalsBranchIsEscaped: a branch name with "/" is one path
// segment, so it cannot reach another endpoint.
func TestRequiredApprovalsBranchIsEscaped(t *testing.T) {
	f := stStatusFake(t, stPR(nil, nil, 1), []any{})
	stHandleApprovals(f, "/repos/octo/demo/rules/branches/release%2F1.0", 200, []any{stRule(2)})
	p, _ := f.provider(f.config(""), time.Now())
	pr := stOpen("clean")
	pr.TargetBranch = "release/1.0"
	if st := p.GetReviewStatus(t.Context(), testRef(), pr, stOpts()); st.RequiredApprovals == nil || *st.RequiredApprovals != 2 {
		t.Errorf("RequiredApprovals = %v, want 2", st.RequiredApprovals)
	}
}

// TestCommitsPastGitHubsLimitGetANote: a pull request with more commits than
// GitHub lists carries the fixed note through the notes of the status (the
// channel of pr_info); 250 commits are all listed and get none.
func TestCommitsPastGitHubsLimitGetANote(t *testing.T) {
	for commits, want := range map[int]bool{1: false, 250: false, 251: true, 1200: true} {
		f := stStatusFake(t, stPR(nil, nil, commits), []any{})
		p, _ := f.provider(f.config(""), time.Now())
		st := p.GetReviewStatus(t.Context(), testRef(), stOpen("clean"), stOpts())
		if got := slices.Contains(st.Notes, provider.NoteCommitsTruncated); got != want {
			t.Errorf("%d commits: note %v, want %v (notes %q)", commits, got, want, st.Notes)
		}
	}
}

// TestReviewsUnreadable: a failure of the reviews gives no reviewer list and
// the fixed note, and the merge status of the pull request stays.
func TestReviewsUnreadable(t *testing.T) {
	f := newFake(t, "/api/v3")
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/7", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, 500) })
	stHandleApprovals(f, "/repos/octo/demo/rules/branches/main", 500, nil)
	stHandleApprovals(f, "/repos/octo/demo/branches/main/protection", 500, nil)
	p, _ := f.provider(f.config(""), time.Now())
	st := p.GetReviewStatus(t.Context(), testRef(), stOpen("dirty"), stOpts())
	if st.Reviewers != nil || !slices.Contains(st.Notes, provider.NoteReviewsUnreadable) ||
		!slices.Equal(st.MergeBlockers, []string{provider.BlockerConflict}) || st.RequiredApprovalsNote != provider.NoteApprovalsUnreadable {
		t.Errorf("status = %+v", st)
	}
	for _, n := range st.Notes {
		if strings.Contains(n, testSentinel) || strings.Contains(n, testToken) {
			t.Errorf("note %q carries response text", n)
		}
	}
	if st := p.GetReviewStatus(t.Context(), testRef(), nil, stOpts()); st == nil || st.Reviewers != nil {
		t.Errorf("nil pull request: %+v", st)
	}
}

// ---- UpdatePullRequest ----

func TestUpdatePullRequestSendsOnlyTheNamedFields(t *testing.T) {
	var bodies []string
	f := newFake(t, "/api/v3")
	f.handle(http.MethodPatch, "/repos/octo/demo/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		writeJSON(w, prJSON("b", "h"))
	})
	p, _ := f.provider(f.config(""), time.Now())
	title, desc, empty := "New title", "New body\r\n```\r\n", ""
	for _, tc := range []struct {
		up   provider.UpdatePR
		want map[string]string
	}{
		{provider.UpdatePR{Title: &title}, map[string]string{"title": title}},
		{provider.UpdatePR{Description: &desc}, map[string]string{"body": desc}},
		{provider.UpdatePR{Title: &title, Description: &empty}, map[string]string{"title": title, "body": ""}},
		// A Version is another provider's lock; GitHub ignores it.
		{provider.UpdatePR{Title: &title, Version: "5"}, map[string]string{"title": title}},
	} {
		bodies = nil
		if err := p.UpdatePullRequest(t.Context(), testRef(), tc.up); err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		if len(bodies) != 1 || json.Unmarshal([]byte(bodies[0]), &got) != nil || len(got) != len(tc.want) {
			t.Fatalf("bodies = %q, want one with the fields %v only", bodies, tc.want)
		}
		for k, v := range tc.want {
			if got[k] != v {
				t.Errorf("field %s = %q, want %q", k, got[k], v)
			}
		}
	}
	if !p.Capabilities().DescriptionEdit {
		t.Error("DescriptionEdit is off")
	}
}

func TestUpdatePullRequestValidatesFirstAndMapsErrors(t *testing.T) {
	f := newFake(t, "/api/v3")
	f.handle(http.MethodPatch, "/repos/octo/demo/pulls/7", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, 403) })
	p, _ := f.provider(f.config(""), time.Now())
	blank, ok := " ", "T"
	for _, up := range []provider.UpdatePR{{}, {Title: &blank}, {Title: &ok, Version: "v1"}} {
		if err := p.UpdatePullRequest(t.Context(), testRef(), up); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%+v: err = %v, want protocol", up, err)
		}
	}
	if got := f.requests(); len(got) != 0 {
		t.Errorf("requests = %q for invalid input, want none", got)
	}
	err := p.UpdatePullRequest(t.Context(), testRef(), provider.UpdatePR{Title: &ok})
	if !errors.Is(err, provider.ErrAuth) || strings.Contains(err.Error(), testSentinel) || strings.Contains(err.Error(), testToken) {
		t.Errorf("err = %v, want a clean auth error", err)
	}
}
