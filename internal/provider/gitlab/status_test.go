package gitlab

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

// ctStatusSpec is a small merge request with the four kinds of reviewer.
func ctStatusSpec() contract.Spec {
	spec := ctSpec(ctModified)
	spec.Reviewers = []contract.Reviewer{
		{User: contract.User{ID: 201, Login: "dave", DisplayName: "Dave"}, State: provider.ReviewApproved},
		{User: contract.User{ID: 202, Login: "erin"}, State: provider.ReviewChangesRequested},
		{User: contract.User{ID: 203, Login: "frank"}, State: provider.ReviewApproved, Stale: true}, // approved_by only
		{User: contract.User{ID: 204, Login: "heidi"}, State: provider.ReviewPending},
		{User: contract.User{ID: 205, Login: "ivan"}, State: provider.ReviewCommented},
	}
	return spec
}

// ctStatusOf reads the merge request and its review status.
func ctStatusOf(t *testing.T, f ctFixture, spec contract.Spec) (*provider.ReviewStatus, *ctGitLab) {
	t.Helper()
	p, ref, g, _ := f.serve(t, spec)
	pr, err := p.GetPullRequest(t.Context(), ref)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	return p.GetReviewStatus(t.Context(), ref, pr, provider.ReviewStatusOptions{}), g
}

type ctReviewerWant struct {
	state     provider.ReviewState
	requested bool
}

func ctReviewersOf(st *provider.ReviewStatus) map[string]ctReviewerWant {
	got := map[string]ctReviewerWant{}
	for _, r := range st.Reviewers {
		got[r.User.Name] = ctReviewerWant{r.State, r.Requested}
	}
	return got
}

// TestReviewerStates: every state of a requested reviewer, and the user of
// approved_by who is not a reviewer (approved, not requested).
func TestReviewerStates(t *testing.T) {
	st, _ := ctStatusOf(t, ctFixture{}, ctStatusSpec())
	want := map[string]ctReviewerWant{
		"dave":  {provider.ReviewApproved, true},
		"erin":  {provider.ReviewChangesRequested, true},
		"frank": {provider.ReviewApproved, false},
		"heidi": {provider.ReviewPending, true},
		"ivan":  {provider.ReviewCommented, true},
	}
	got := ctReviewersOf(st)
	if len(st.Reviewers) != len(want) {
		t.Errorf("%d reviewers, want %d: %+v", len(st.Reviewers), len(want), st.Reviewers)
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %+v, want %+v", name, got[name], w)
		}
	}
	for _, r := range st.Reviewers {
		if r.Stale || !r.At.IsZero() {
			t.Errorf("%s: stale %v at %v, want neither", r.User.Name, r.Stale, r.At)
		}
	}
	if st.Reviewers[0].DisplayName != "Dave" || st.Reviewers[0].User.ID != "201" {
		t.Errorf("first reviewer = %+v", st.Reviewers[0])
	}
	for in, want := range map[string]provider.ReviewState{
		"approved": provider.ReviewApproved, "requested_changes": provider.ReviewChangesRequested,
		"reviewed": provider.ReviewCommented, "unreviewed": provider.ReviewPending, "review_started": provider.ReviewPending,
		"unapproved": provider.ReviewPending, "": provider.ReviewPending, "brand_new": provider.ReviewPending,
	} {
		if got := reviewerState(in); got != want {
			t.Errorf("reviewerState(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRequestedReviewerApproved: a requested reviewer in approved_by is
// approved whatever /reviewers says, except one who requested changes; one
// not in approved_by keeps the /reviewers state. [canary: ignore
// approved_by for requested reviewers]
func TestRequestedReviewerApproved(t *testing.T) {
	for _, tc := range []struct {
		state string
		in    bool
		want  provider.ReviewState
	}{
		{"unreviewed", true, provider.ReviewApproved},
		{"reviewed", true, provider.ReviewApproved},
		{"review_started", true, provider.ReviewApproved},
		{"approved", true, provider.ReviewApproved},
		{"requested_changes", true, provider.ReviewChangesRequested},
		{"unreviewed", false, provider.ReviewPending},
		{"reviewed", false, provider.ReviewCommented},
		{"approved", false, provider.ReviewApproved},
		{"requested_changes", false, provider.ReviewChangesRequested},
	} {
		if got := requestedState(tc.state, tc.in); got != tc.want {
			t.Errorf("requestedState(%q, in approved_by %v) = %q, want %q", tc.state, tc.in, got, tc.want)
		}
	}
}

// TestRequestedReviewerApprovedServed: the same rule through /reviewers and
// /approvals: approvers whose /reviewers state is unreviewed, reviewed or
// requested_changes, and a reviewer not in approved_by.
func TestRequestedReviewerApprovedServed(t *testing.T) {
	spec := ctSpec(ctModified)
	spec.Reviewers = []contract.Reviewer{
		{User: contract.User{ID: 201, Login: "dave"}, State: provider.ReviewApproved},
		{User: contract.User{ID: 202, Login: "erin"}, State: provider.ReviewApproved},
		{User: contract.User{ID: 203, Login: "frank"}, State: provider.ReviewApproved},
		{User: contract.User{ID: 204, Login: "heidi"}, State: provider.ReviewCommented},
	}
	f := ctFixture{reviewerStates: map[string]string{"dave": "unreviewed", "erin": "reviewed", "frank": "requested_changes"}}
	st, _ := ctStatusOf(t, f, spec)
	want := map[string]ctReviewerWant{
		"dave":  {provider.ReviewApproved, true},
		"erin":  {provider.ReviewApproved, true},
		"frank": {provider.ReviewChangesRequested, true},
		"heidi": {provider.ReviewCommented, true},
	}
	got := ctReviewersOf(st)
	if len(st.Reviewers) != len(want) {
		t.Errorf("%d reviewers, want %d: %+v", len(st.Reviewers), len(want), st.Reviewers)
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %+v, want %+v", name, got[name], w)
		}
	}
}

// TestReviewersNotFound: an instance that answers 404 for /reviewers falls
// back to the merge request's reviewers[], pending unless approved_by
// lists them, plus approved_by;
// any other failure leaves the reviewers unread, with the fixed note, and
// the required approvals are still read.
func TestReviewersNotFound(t *testing.T) {
	st, g := ctStatusOf(t, ctFixture{reviewersStatus: 404}, ctStatusSpec())
	want := map[string]ctReviewerWant{
		"dave":  {provider.ReviewApproved, true}, // in approved_by
		"erin":  {provider.ReviewPending, true},
		"heidi": {provider.ReviewPending, true},
		"ivan":  {provider.ReviewPending, true},
		"frank": {provider.ReviewApproved, false},
	}
	got := ctReviewersOf(st)
	if len(got) != len(want) || st.Reviewers == nil {
		t.Errorf("reviewers = %+v", st.Reviewers)
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %+v, want %+v", name, got[name], w)
		}
	}
	// Two reads for GetPullRequest (the first has empty diff_refs), one for
	// the fallback.
	if g.mrReads != 3 {
		t.Errorf("%d merge request reads, want 3", g.mrReads)
	}

	for _, status := range []int{401, 403, 500} {
		st, _ := ctStatusOf(t, ctFixture{reviewersStatus: status}, ctStatusSpec())
		if st.Reviewers != nil || !slices.Contains(st.Notes, provider.NoteReviewsUnreadable) {
			t.Errorf("reviewers status %d: %+v, want no reviewers and the note", status, st)
		}
		if st.RequiredApprovals == nil || *st.RequiredApprovals != 2 {
			t.Errorf("reviewers status %d: required approvals %v, want 2", status, st.RequiredApprovals)
		}
	}
}

// TestRequiredApprovals: approvals_required present, 0, absent and
// unreadable.
func TestRequiredApprovals(t *testing.T) {
	zero, three := 0, 3
	for _, tc := range []struct {
		name string
		fx   ctFixture
		want *int
		note string
	}{
		{"default", ctFixture{}, ptr(2), ""},
		{"three", ctFixture{required: &three}, &three, ""},
		{"zero", ctFixture{required: &zero}, &zero, provider.NoteNoApprovalRule},
		{"absent", ctFixture{omitRequired: true}, nil, provider.NoteApprovalsUnreadable},
		{"unreadable_403", ctFixture{approvalsStatus: 403}, nil, provider.NoteApprovalsUnreadable},
		{"unreadable_500", ctFixture{approvalsStatus: 500}, nil, provider.NoteApprovalsUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := ctStatusOf(t, tc.fx, ctStatusSpec())
			switch {
			case (tc.want == nil) != (st.RequiredApprovals == nil), tc.want != nil && *tc.want != *st.RequiredApprovals:
				t.Errorf("required approvals = %v, want %v", st.RequiredApprovals, tc.want)
			case st.RequiredApprovalsNote != tc.note:
				t.Errorf("note = %q, want %q", st.RequiredApprovalsNote, tc.note)
			}
			if st.Reviewers == nil {
				t.Errorf("the reviewers are not listed although they were read")
			}
		})
	}
}

func ptr(n int) *int { return &n }

// TestMergeStatusTable: every value of detailed_merge_status (spec 2C §4).
// [canary: map discussions_not_resolved to BlockerOtherCheck]
func TestMergeStatusTable(t *testing.T) {
	yes, no := true, false
	other := provider.BlockerOtherCheck
	for _, tc := range []struct {
		status  string
		mergeOK *bool
		blocker string
	}{
		{"mergeable", &yes, ""},
		{"unchecked", nil, ""}, {"checking", nil, ""}, {"approvals_syncing", nil, ""}, {"preparing", nil, ""},
		{"conflict", &no, provider.BlockerConflict}, {"broken_status", &no, provider.BlockerConflict},
		{"ci_must_pass", &no, provider.BlockerBuilds}, {"ci_still_running", &no, provider.BlockerBuilds},
		{"external_status_checks", &no, provider.BlockerBuilds}, {"security_policy_pipeline_check", &no, provider.BlockerBuilds},
		{"discussions_not_resolved", &no, provider.BlockerDiscussions},
		{"draft_status", &no, provider.BlockerDraft},
		{"need_rebase", &no, provider.BlockerBehind},
		{"not_approved", &no, provider.BlockerApprovals},
		{"requested_changes", &no, provider.BlockerNeedsWork},
		{"not_open", &no, other}, {"blocked_status", &no, other}, {"merge_request_blocked", &no, other},
		{"jira_association_missing", &no, other}, {"title_regex", &no, other}, {"merge_time", &no, other},
		{"locked_paths", &no, other}, {"a_status_from_the_future", &no, other},
	} {
		t.Run(tc.status, func(t *testing.T) {
			st, _ := ctStatusOf(t, ctFixture{mergeStatus: tc.status}, ctStatusSpec())
			if (tc.mergeOK == nil) != (st.Mergeable == nil) || (tc.mergeOK != nil && *tc.mergeOK != *st.Mergeable) {
				t.Errorf("mergeable = %v, want %v", st.Mergeable, tc.mergeOK)
			}
			var want []string
			if tc.blocker != "" {
				want = []string{tc.blocker}
			}
			if !slices.Equal(st.MergeBlockers, want) {
				t.Errorf("blockers = %q, want %q", st.MergeBlockers, want)
			}
			if slices.Contains(st.Notes, provider.NoteMergeUnreadable) {
				t.Errorf("unexpected note %q", provider.NoteMergeUnreadable)
			}
		})
	}
	if provider.BlockerDiscussions != "unresolved discussions must be resolved" ||
		provider.NoteNoApprovalRule != "no approval rule applies to this merge request" {
		t.Errorf("fixed texts changed: %q, %q", provider.BlockerDiscussions, provider.NoteNoApprovalRule)
	}
}

// TestMergeStatusNotOpen: a merged or closed merge request has no merge
// state; an open one whose status was not read has the fixed note.
func TestMergeStatusNotOpen(t *testing.T) {
	for _, state := range []string{"merged", "closed", "locked"} {
		st, _ := ctStatusOf(t, ctFixture{state: state, mergeStatus: "not_open"}, ctStatusSpec())
		if st.Mergeable != nil || len(st.MergeBlockers) != 0 || len(st.Notes) != 0 {
			t.Errorf("state %s: %+v, want no merge state", state, st)
		}
	}
	p, ref, _, _ := ctFixture{}.serve(t, ctStatusSpec())
	st := p.GetReviewStatus(t.Context(), ref, &provider.PullRequest{State: "open"}, provider.ReviewStatusOptions{})
	if st.Mergeable != nil || !slices.Contains(st.Notes, provider.NoteMergeUnreadable) {
		t.Errorf("open without a status: %+v, want unknown and the note", st)
	}
}

// TestCommitStatusStates: every commit status value (spec 2C §4 item 3).
// [canary: map a failed job with allow_failure false to neutral]
func TestCommitStatusStates(t *testing.T) {
	for _, tc := range []struct {
		status string
		allow  bool
		want   provider.CheckState
	}{
		{"success", false, provider.CheckSuccess}, {"success", true, provider.CheckSuccess},
		{"failed", false, provider.CheckFailure}, {"failed", true, provider.CheckNeutral},
		{"canceled", false, provider.CheckError}, {"canceled", true, provider.CheckError},
		{"skipped", false, provider.CheckSkipped},
		{"manual", false, provider.CheckPending}, {"manual", true, provider.CheckNeutral},
		{"created", false, provider.CheckPending}, {"waiting_for_resource", false, provider.CheckPending},
		{"preparing", false, provider.CheckPending}, {"pending", false, provider.CheckPending},
		{"running", false, provider.CheckPending}, {"scheduled", false, provider.CheckPending},
		{"brand_new", false, provider.CheckNeutral}, {"", false, provider.CheckNeutral},
	} {
		if got := mapCheckState(tc.status, tc.allow); got != tc.want {
			t.Errorf("mapCheckState(%q, allow_failure %v) = %q, want %q", tc.status, tc.allow, got, tc.want)
		}
	}

	// End to end, over several pages: names and URLs through NewCheck.
	raw := func(name, status string, allow bool, target string) map[string]any {
		return map[string]any{"name": name, "status": status, "allow_failure": allow, "target_url": target}
	}
	spec := ctSpec(ctModified)
	f := ctFixture{statusPage: 2, statuses: []map[string]any{
		raw("build", "success", false, "https://ci.example.com/b/1"),
		raw("unit", "failed", false, "https://ci.example.com/u/1"),
		raw("flaky", "failed", true, "javascript:alert(1)"),
		raw("deploy", "manual", false, ""),
		raw("e2e", "manual", true, ""),
	}}
	p, ref, _, _ := f.serve(t, spec)
	got, err := p.GetChecks(t.Context(), ref, spec.HeadSHA)
	if err != nil {
		t.Fatalf("GetChecks: %v", err)
	}
	var names, states []string
	for _, c := range got.Items {
		names = append(names, c.Name)
		states = append(states, string(c.State))
	}
	if !slices.Equal(names, []string{"build", "unit", "flaky", "deploy", "e2e"}) ||
		!slices.Equal(states, []string{"success", "failure", "neutral", "pending", "neutral"}) {
		t.Errorf("checks %q states %q", names, states)
	}
	if got.Items[2].URL != "" || got.Items[0].URL != "https://ci.example.com/b/1" {
		t.Errorf("URLs = %q, %q", got.Items[0].URL, got.Items[2].URL)
	}
	if got.Truncated || got.Strategy != provider.ChecksGitLabCommitStatuses {
		t.Errorf("truncated %v strategy %q", got.Truncated, got.Strategy)
	}
}

// TestUpdatePutBody: the PUT carries the title and/or the description and
// nothing else (the fake fails the test on any other field).
func TestUpdatePutBody(t *testing.T) {
	spec := ctSpec(ctModified)
	p, ref, g, _ := ctFixture{}.serve(t, spec)
	if !p.Capabilities().DescriptionEdit {
		t.Fatal("DescriptionEdit is false")
	}
	title, desc := "New title", "New /description\n/label ~x"
	for _, up := range []provider.UpdatePR{{Title: &title}, {Description: &desc}, {Title: &title, Description: &desc}} {
		if err := p.UpdatePullRequest(t.Context(), ref, up); err != nil {
			t.Fatalf("UpdatePullRequest: %v", err)
		}
	}
	if want := []string{"title", "description", "description,title"}; !slices.Equal(g.putFields(), want) {
		t.Errorf("PUT fields = %q, want %q", g.putFields(), want)
	}
	if g.pr.Title != title || g.pr.Description != desc {
		t.Errorf("stored title %q description %q", g.pr.Title, g.pr.Description)
	}
	// Invalid input sends nothing.
	n := len(g.puts)
	empty := ""
	if err := p.UpdatePullRequest(t.Context(), ref, provider.UpdatePR{Title: &empty}); !errors.Is(err, provider.ErrProtocol) || len(g.puts) != n {
		t.Errorf("empty title: %v (%d PUTs)", err, len(g.puts)-n)
	}
}

// TestWriteURLsUseWebURL: PostComment, ReplyToComment and
// PostInlineComments all build the note link from the merge request's
// web_url (as ListThreads does), also when it differs from gitlab.base_url
// by host and path. Without a web_url the web base is the fallback.
// [canary: build the PostComment URL from the configured base]
func TestWriteURLsUseWebURL(t *testing.T) {
	const web = "https://gitlab.example.com/proxy/group/sub/project/-/merge_requests/7"
	spec := ctSpec(ctShifted)
	spec.Threads = []contract.Thread{ctThread(provider.ThreadGeneral, 0, 80, contract.User{ID: 102, Login: "bob"})}
	p, ref, g, waits := ctFixture{webURL: web}.serve(t, spec)

	posted, err := p.PostComment(t.Context(), ref, "A comment.")
	if err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if len(*waits) != 0 || g.mrReads != 1 {
		t.Errorf("PostComment waited %v and read the merge request %d times, want one plain read and no wait", *waits, g.mrReads)
	}
	replied, err := p.ReplyToComment(t.Context(), ref, "80", "A reply.")
	if err != nil {
		t.Fatalf("ReplyToComment: %v", err)
	}
	pr, err := p.GetPullRequest(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	items := ctItems(t, p, ref, pr, ctAt{"src/shift.go", 4})
	inline, err := p.PostInlineComments(t.Context(), ref, pr, items)
	if err != nil || len(inline) != 1 || !inline[0].Posted {
		t.Fatalf("PostInlineComments = %+v, %v", inline, err)
	}
	ts, err := p.ListThreads(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, th := range ts {
		for _, c := range th.Comments {
			listed[c.ID] = c.URL
		}
	}
	for name, got := range map[string]struct{ id, url string }{
		"PostComment": {posted.ID, posted.URL}, "ReplyToComment": {replied.Comment.ID, replied.Comment.URL},
		"PostInlineComments": {inline[0].ID, inline[0].URL},
	} {
		if want := web + "#note_" + got.id; got.url != want || listed[got.id] != want {
			t.Errorf("%s URL = %q, ListThreads %q; want %q", name, got.url, listed[got.id], want)
		}
	}
	if strings.Contains(posted.URL, "127.0.0.1") {
		t.Errorf("the URL %q is built from the configured base", posted.URL)
	}

	// No web_url: the web base is the fallback, for all three.
	p, ref, _, _ = ctFixture{emptyWebURL: true}.serve(t, spec)
	c, err := p.PostComment(t.Context(), ref, "A comment.")
	if err != nil {
		t.Fatal(err)
	}
	if want := p.webURL + "/group/sub/project/-/merge_requests/7#note_" + c.ID; c.URL != want {
		t.Errorf("fallback URL = %q, want %q", c.URL, want)
	}
}

// TestPostCommentWhenDiffNotReady: a note needs no diff, so a merge
// request whose diff_refs are empty (the fake's first read) can be
// commented on at once, without the wait of GetPullRequest.
func TestPostCommentWhenDiffNotReady(t *testing.T) {
	p, ref, g, waits := ctFixture{}.serve(t, ctSpec(ctModified))
	if _, err := p.PostComment(t.Context(), ref, "Early."); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if len(*waits) != 0 || !slices.Equal(g.writes, []string{"Early."}) {
		t.Errorf("waits %v writes %q", *waits, g.writes)
	}
}
