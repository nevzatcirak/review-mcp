package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// listAnswer is the canned answer of one role of a fake lister.
type listAnswer struct {
	list *provider.PRList
	err  error
}

// listFake is a provider with canned pull request lists. The embedded nil
// interface makes any other method panic.
type listFake struct {
	provider.Provider
	kind    provider.Kind
	caps    provider.ListCaps
	answers map[provider.ListRole]listAnswer

	mu    sync.Mutex
	asked []provider.ListQuery
}

func (f *listFake) Kind() provider.Kind { return f.kind }

func (f *listFake) Capabilities() provider.Capabilities { return provider.Capabilities{Lists: f.caps} }

func (f *listFake) ListMyPullRequests(_ context.Context, q provider.ListQuery) (*provider.PRList, error) {
	f.mu.Lock()
	f.asked = append(f.asked, q)
	f.mu.Unlock()
	a, ok := f.answers[q.Role]
	if !ok {
		return nil, &provider.Error{Class: provider.ClassUnsupported}
	}
	return a.list, a.err
}

func (f *listFake) roles() []provider.ListRole {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []provider.ListRole
	for _, q := range f.asked {
		out = append(out, q.Role)
	}
	return out
}

// noLister is a provider without PRLister.
type noLister struct {
	provider.Provider
	kind provider.Kind
}

func (f noLister) Kind() provider.Kind { return f.kind }

func (f noLister) Capabilities() provider.Capabilities { return provider.Capabilities{} }

// fakeSource is a ListSource over fixed providers; noToken kinds are
// reported and not handed out.
type fakeSource struct {
	kinds     []provider.Kind
	providers map[provider.Kind]provider.Provider
	noToken   []provider.Kind
}

func (s fakeSource) EnabledKinds() []provider.Kind { return s.kinds }

func (s fakeSource) ListProviders(kinds []provider.Kind) ([]provider.Provider, []provider.Kind) {
	var ps []provider.Provider
	var nt []provider.Kind
	for _, k := range s.kinds {
		if !slices.Contains(kinds, k) {
			continue
		}
		if slices.Contains(s.noToken, k) {
			nt = append(nt, k)
		} else if p, ok := s.providers[k]; ok {
			ps = append(ps, p)
		}
	}
	return ps, nt
}

func newSource(noToken []provider.Kind, ps ...provider.Provider) fakeSource {
	s := fakeSource{providers: map[provider.Kind]provider.Provider{}, noToken: noToken}
	for _, p := range ps {
		s.providers[p.Kind()] = p
	}
	for _, k := range allKinds {
		if _, ok := s.providers[k]; ok || slices.Contains(noToken, k) {
			s.kinds = append(s.kinds, k)
		}
	}
	return s
}

var (
	allRoles = provider.ListCaps{Reviewer: true, Assignee: true, Author: true}
	listT0   = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
)

func listedPR(kind provider.Kind, repo string, n int64, title string, updated time.Time) provider.ListedPR {
	return provider.ListedPR{
		Ref: provider.PRRef{Kind: kind, Namespace: "octo", Repo: repo, Number: n,
			URL: fmt.Sprintf("https://%s.example.com/octo/%s/pulls/%d", kind, repo, n)},
		Title: title, Author: "alice", UpdatedAt: updated,
	}
}

func pageOf(items ...provider.ListedPR) listAnswer {
	return listAnswer{list: &provider.PRList{Items: items}}
}

func runList(t *testing.T, src ListSource, a PRListArgs) PRListResult {
	t.Helper()
	res, err := PRList(context.Background(), src, a, nil)
	if err != nil {
		t.Fatalf("PRList: %v", err)
	}
	return res
}

func statusOf(res PRListResult, kind provider.Kind) ListProviderStatus {
	for _, s := range res.Providers {
		if s.Kind == string(kind) {
			return s
		}
	}
	return ListProviderStatus{}
}

// TestPRListMergesByURLWithBothRoles: [canary h] with include_assigned the
// reviewer and the assignee query run, and a pull request in both lists is
// one item holding both roles.
func TestPRListMergesByURLWithBothRoles(t *testing.T) {
	both := listedPR(provider.KindGitea, "demo", 7, "both", listT0)
	onlyAssigned := listedPR(provider.KindGitea, "demo", 8, "assigned", listT0.Add(-time.Hour))
	g := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListReviewer: pageOf(both),
		provider.ListAssignee: pageOf(both, onlyAssigned),
	}}
	res := runList(t, newSource(nil, g), PRListArgs{Role: ListRoleReviewRequests, IncludeAssigned: true})

	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2 (merged by URL): %+v", len(res.Items), res.Items)
	}
	if got := res.Items[0].Roles; !slices.Equal(got, []string{"reviewer", "assignee"}) {
		t.Errorf("roles of the merged item = %v", got)
	}
	if got := res.Items[1].Roles; !slices.Equal(got, []string{"assignee"}) {
		t.Errorf("roles of the assigned item = %v", got)
	}
	if st := statusOf(res, provider.KindGitea); st.Status != ListStatusOK || st.Count != 2 {
		t.Errorf("status = %+v, want ok with count 2 (the merged count)", st)
	}
	if !slices.Equal(g.roles(), []provider.ListRole{provider.ListReviewer, provider.ListAssignee}) {
		t.Errorf("queries = %v", g.roles())
	}

	// Without include_assigned only the reviewer query runs.
	g2 := &listFake{kind: provider.KindGitea, caps: allRoles, answers: g.answers}
	res = runList(t, newSource(nil, g2), PRListArgs{Role: ListRoleReviewRequests})
	if !slices.Equal(g2.roles(), []provider.ListRole{provider.ListReviewer}) || len(res.Items) != 1 ||
		!slices.Equal(res.Items[0].Roles, []string{"reviewer"}) {
		t.Errorf("queries %v, items %+v", g2.roles(), res.Items)
	}
}

// TestPRListFailedProviderIsReported: [canary i] a failed provider is in
// providers with its fixed reason and has a note; the others' items stay.
func TestPRListFailedProviderIsReported(t *testing.T) {
	ok := &listFake{kind: provider.KindGitLab, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListAuthor: pageOf(listedPR(provider.KindGitLab, "web", 3, "t", listT0))}}
	bad := &listFake{kind: provider.KindGitHub, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListAuthor: {err: &provider.Error{Class: provider.ClassRateLimited, Status: 403}}}}
	res := runList(t, newSource(nil, bad, ok), PRListArgs{Role: ListRoleAuthored})

	if len(res.Items) != 1 || len(res.Providers) != 2 {
		t.Fatalf("items %d, providers %d", len(res.Items), len(res.Providers))
	}
	st := statusOf(res, provider.KindGitHub)
	if st.Status != ListStatusFailed || st.Reason == nil || *st.Reason != ListReasonRateLimited || st.Count != 0 {
		t.Errorf("GitHub status = %+v", st)
	}
	want := fmt.Sprintf(NoteListProviderFailedFormat, "GitHub", "rate_limited")
	if !slices.Contains(res.Notes, want) {
		t.Errorf("notes %q lack %q", res.Notes, want)
	}
	// An authored list has no GitHub review-request note, and nothing is
	// "ok" about GitHub.
	if slices.Contains(res.Notes, NoteGitHubReviewRequests) {
		t.Error("the GitHub review request note belongs to review request lists")
	}
	md := RenderPRListMarkdown(res)
	if !strings.Contains(md, "`github`: failed (rate_limited)") {
		t.Errorf("markdown lacks the failed provider:\n%s", md)
	}
}

// TestPRListEverythingFailedIsNotEmpty: with every provider failed the
// markdown never says that nothing exists.
func TestPRListEverythingFailedIsNotEmpty(t *testing.T) {
	bad := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListReviewer: {err: &provider.Error{Class: provider.ClassTransport, Hint: "timeout"}}}}
	res := runList(t, newSource(nil, bad), PRListArgs{Role: ListRoleReviewRequests})
	md := RenderPRListMarkdown(res)
	if strings.Contains(md, "No open pull requests ask for your review") || !strings.Contains(md, "No pull requests were listed.") ||
		!strings.Contains(md, "failed (timeout)") {
		t.Errorf("markdown:\n%s", md)
	}
}

// TestPRListHasMoreGivesNote: [canary j] a provider's has_more, and a cut,
// each add NoteListMore once.
func TestPRListHasMoreGivesNote(t *testing.T) {
	more := &provider.PRList{Items: []provider.ListedPR{listedPR(provider.KindGitea, "demo", 1, "t", listT0)}, HasMore: true}
	g := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{provider.ListAuthor: {list: more}}}
	res := runList(t, newSource(nil, g), PRListArgs{Role: ListRoleAuthored})
	if n := countOf(res.Notes, NoteListMore); n != 1 {
		t.Errorf("NoteListMore written %d times, notes %q", n, res.Notes)
	}
	if st := statusOf(res, provider.KindGitea); !st.HasMore {
		t.Errorf("status = %+v, want has_more", st)
	}

	// No has_more, no cut: no note.
	g = &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListAuthor: pageOf(listedPR(provider.KindGitea, "demo", 1, "t", listT0))}}
	if res = runList(t, newSource(nil, g), PRListArgs{Role: ListRoleAuthored}); slices.Contains(res.Notes, NoteListMore) {
		t.Errorf("unexpected note: %q", res.Notes)
	}
}

func countOf(ss []string, s string) int {
	n := 0
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return n
}

// TestPRListCutsAfterSorting: the merged list is sorted by update time,
// then kind, then URL, and cut at limit; the cut says so and marks the
// provider whose item was cut.
func TestPRListCutsAfterSorting(t *testing.T) {
	gitea := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListAuthor: pageOf(listedPR(provider.KindGitea, "a", 1, "old", listT0.Add(-2*time.Hour)),
			listedPR(provider.KindGitea, "b", 2, "same time", listT0))}}
	gitlab := &listFake{kind: provider.KindGitLab, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListAuthor: pageOf(listedPR(provider.KindGitLab, "c", 3, "same time", listT0),
			listedPR(provider.KindGitLab, "d", 4, "newest", listT0.Add(time.Hour)))}}
	two := 2
	res := runList(t, newSource(nil, gitea, gitlab), PRListArgs{Role: ListRoleAuthored, Limit: &two})
	var titles []string
	for _, it := range res.Items {
		titles = append(titles, it.Provider+":"+it.Title)
	}
	if want := []string{"gitlab:newest", "gitea:same time"}; !slices.Equal(titles, want) {
		t.Errorf("items = %q, want %q", titles, want)
	}
	if !slices.Contains(res.Notes, NoteListMore) {
		t.Errorf("notes = %q", res.Notes)
	}
	if st := statusOf(res, provider.KindGitea); st.Count != 1 || !st.HasMore {
		t.Errorf("Gitea lost an item to the cut: %+v", st)
	}
	if st := statusOf(res, provider.KindGitLab); st.Count != 1 || !st.HasMore {
		t.Errorf("GitLab lost an item to the cut: %+v", st)
	}
	for _, q := range gitea.asked {
		if q.Limit != 2 {
			t.Errorf("provider asked for %d items, want the limit 2", q.Limit)
		}
	}
}

// TestPRListPartialFailure: with include_assigned, one failing query keeps
// the items of the other one, marks the provider failed and names the role.
func TestPRListPartialFailure(t *testing.T) {
	item := listedPR(provider.KindGitHub, "demo", 7, "t", listT0)
	g := &listFake{kind: provider.KindGitHub, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListReviewer: pageOf(item),
		provider.ListAssignee: {err: &provider.Error{Class: provider.ClassUpstream, Status: 502}}}}
	res := runList(t, newSource(nil, g), PRListArgs{Role: ListRoleReviewRequests, IncludeAssigned: true})
	st := statusOf(res, provider.KindGitHub)
	if len(res.Items) != 1 || st.Status != ListStatusFailed || st.Reason == nil || *st.Reason != ListReasonUnavailable || st.Count != 1 {
		t.Fatalf("items %d, status %+v", len(res.Items), st)
	}
	want := fmt.Sprintf(NoteListRoleMissingFormat, "GitHub", "assigned pull requests", "unavailable")
	if !slices.Contains(res.Notes, want) {
		t.Errorf("notes %q lack %q", res.Notes, want)
	}
	// The reviewer query failing and the assignee one working is the mirror.
	g = &listFake{kind: provider.KindGitHub, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListReviewer: {err: &provider.Error{Class: provider.ClassAuth, Status: 401}},
		provider.ListAssignee: pageOf(item)}}
	res = runList(t, newSource(nil, g), PRListArgs{Role: ListRoleReviewRequests, IncludeAssigned: true})
	want = fmt.Sprintf(NoteListRoleMissingFormat, "GitHub", "review requests", "auth")
	if len(res.Items) != 1 || !slices.Contains(res.Notes, want) {
		t.Errorf("items %d, notes %q", len(res.Items), res.Notes)
	}
}

// TestPRListBitbucketHasNoAssignees: the assignee query is not sent, the
// status stays ok and the note is written; MyStatus passes through.
func TestPRListBitbucketHasNoAssignees(t *testing.T) {
	it := listedPR(provider.KindBitbucketServer, "demo", 7, "t", listT0)
	it.MyStatus = provider.MyStatusNeedsWork
	b := &listFake{kind: provider.KindBitbucketServer, caps: provider.ListCaps{Reviewer: true, Author: true},
		answers: map[provider.ListRole]listAnswer{provider.ListReviewer: pageOf(it)}}
	res := runList(t, newSource(nil, b), PRListArgs{Role: ListRoleReviewRequests, IncludeAssigned: true})
	if !slices.Equal(b.roles(), []provider.ListRole{provider.ListReviewer}) {
		t.Errorf("queries = %v", b.roles())
	}
	if st := statusOf(res, provider.KindBitbucketServer); st.Status != ListStatusOK || st.Reason != nil {
		t.Errorf("status = %+v", st)
	}
	if !slices.Contains(res.Notes, NoteListNoAssignees) {
		t.Errorf("notes = %q", res.Notes)
	}
	if res.Items[0].MyStatus == nil || *res.Items[0].MyStatus != "needs_work" {
		t.Errorf("my_status = %v", res.Items[0].MyStatus)
	}
	// Without include_assigned there is no note.
	b = &listFake{kind: provider.KindBitbucketServer, caps: b.caps, answers: b.answers}
	if res = runList(t, newSource(nil, b), PRListArgs{Role: ListRoleReviewRequests}); slices.Contains(res.Notes, NoteListNoAssignees) {
		t.Errorf("unexpected note: %q", res.Notes)
	}
}

// TestPRListDroppedNote: items a provider dropped are counted in a note
// that names the provider (DQ-1 of the architect review).
func TestPRListDroppedNote(t *testing.T) {
	g := &listFake{kind: provider.KindGitHub, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListAuthor: {list: &provider.PRList{Items: []provider.ListedPR{listedPR(provider.KindGitHub, "demo", 1, "t", listT0)}, Dropped: 3}}}}
	res := runList(t, newSource(nil, g), PRListArgs{Role: ListRoleAuthored})
	want := fmt.Sprintf(NoteListDroppedFormat, 3, "GitHub")
	if want != "3 pull requests from GitHub were left out because their repository could not be matched to the configured host." ||
		!slices.Contains(res.Notes, want) {
		t.Errorf("notes %q, want %q", res.Notes, want)
	}
}

// TestPRListGitHubReviewRequestNote: the fixed note is written when GitHub
// is in a review request list.
func TestPRListGitHubReviewRequestNote(t *testing.T) {
	g := &listFake{kind: provider.KindGitHub, caps: allRoles, answers: map[provider.ListRole]listAnswer{provider.ListReviewer: pageOf()}}
	res := runList(t, newSource(nil, g), PRListArgs{Role: ListRoleReviewRequests})
	if !slices.Contains(res.Notes, NoteGitHubReviewRequests) {
		t.Errorf("notes = %q", res.Notes)
	}
}

// TestPRListNoTokenSendsNothing: a kind without a token is no_token, and
// its provider is never asked.
func TestPRListNoTokenAndUnsupported(t *testing.T) {
	ok := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{provider.ListAuthor: pageOf()}}
	plain := noLister{kind: provider.KindGitLab}
	src := newSource([]provider.Kind{provider.KindGitHub}, ok, plain)
	res := runList(t, src, PRListArgs{Role: ListRoleAuthored})
	if st := statusOf(res, provider.KindGitHub); st.Status != ListStatusNoToken || st.Reason != nil || st.Count != 0 {
		t.Errorf("GitHub = %+v", st)
	}
	if st := statusOf(res, provider.KindGitLab); st.Status != ListStatusUnsupported || st.Reason != nil {
		t.Errorf("GitLab = %+v", st)
	}
	md := RenderPRListMarkdown(res)
	if !strings.Contains(md, "`github`: no token in this request") || strings.Contains(md, "You have no open pull requests.") {
		t.Errorf("markdown:\n%s", md)
	}
}

// TestPRListUnsupportedErrorOfTheMainQuery: a provider answering
// unsupported is the status unsupported, without a reason or a note.
func TestPRListUnsupportedErrorOfTheMainQuery(t *testing.T) {
	g := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListAuthor: {err: &provider.Error{Class: provider.ClassUnsupported}}}}
	res := runList(t, newSource(nil, g), PRListArgs{Role: ListRoleAuthored})
	if st := statusOf(res, provider.KindGitea); st.Status != ListStatusUnsupported || st.Reason != nil || len(res.Notes) != 0 {
		t.Errorf("status %+v, notes %q", st, res.Notes)
	}
}

// TestListReasonMapping: every provider error class maps to one fixed
// reason.
func TestListReasonMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&provider.Error{Class: provider.ClassAuth, Status: 401}, ListReasonAuth},
		{&provider.Error{Class: provider.ClassAuth}, ListReasonAuth},
		{&provider.Error{Class: provider.ClassAuth, Status: 403}, ListReasonForbidden},
		{&provider.Error{Class: provider.ClassRateLimited, Status: 429}, ListReasonRateLimited},
		{&provider.Error{Class: provider.ClassTransport, Hint: "timeout"}, ListReasonTimeout},
		{&provider.Error{Class: provider.ClassTransport, Hint: "connection failed"}, ListReasonUnavailable},
		{&provider.Error{Class: provider.ClassUpstream, Status: 503}, ListReasonUnavailable},
		{&provider.Error{Class: provider.ClassUnsupportedVersion}, ListReasonUnsupportedVersion},
		{&provider.Error{Class: provider.ClassProtocol}, ListReasonBadResponse},
		{&provider.Error{Class: provider.ClassNotFound, Status: 404}, ListReasonBadResponse},
		{&provider.Error{Class: provider.ClassTooLarge}, ListReasonBadResponse},
		{context.DeadlineExceeded, ListReasonTimeout},
		{errors.New("https://host.example/x?token=SECRET failed"), ListReasonBadResponse},
	} {
		if got := listReason(tc.err); got != tc.want {
			t.Errorf("listReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
		if !slices.Contains(ListReasons, listReason(tc.err)) {
			t.Errorf("%q is not a declared reason", listReason(tc.err))
		}
	}
}

// TestPRListArguments: the arguments are validated before any request.
func TestPRListArguments(t *testing.T) {
	g := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{provider.ListAuthor: pageOf()}}
	src := newSource(nil, g)
	n := func(v int) *int { return &v }
	for _, tc := range []struct {
		name string
		a    PRListArgs
		src  ListSource
		want string
	}{
		{"limit 0", PRListArgs{Role: ListRoleAuthored, Limit: n(0)}, src, ListLimitMessage},
		{"limit 101", PRListArgs{Role: ListRoleAuthored, Limit: n(101)}, src, ListLimitMessage},
		{"limit negative", PRListArgs{Role: ListRoleAuthored, Limit: n(-1)}, src, ListLimitMessage},
		{"unknown provider", PRListArgs{Role: ListRoleAuthored, Provider: "svn <b>"}, src, ListProviderUnknownMessage},
		{"provider not enabled", PRListArgs{Role: ListRoleAuthored, Provider: "github"}, src,
			"the github provider is not configured; configured providers: gitea"},
		{"nothing enabled", PRListArgs{Role: ListRoleAuthored}, newSource(nil), ListNoProviderMessage},
		{"no source", PRListArgs{Role: ListRoleAuthored}, nil, ListNoProviderMessage},
		{"assigned on authored", PRListArgs{Role: ListRoleAuthored, IncludeAssigned: true}, src, listIncludeAssignedMessage},
	} {
		_, err := PRList(context.Background(), tc.src, tc.a, nil)
		var ae *ArgumentError
		if !errors.As(err, &ae) || ae.Error() != tc.want {
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		}
	}
	if len(g.asked) != 0 {
		t.Errorf("a refused call sent %d queries", len(g.asked))
	}
	// 1 and 100 are accepted; a named provider restricts the call.
	for _, v := range []int{1, 100} {
		if _, err := PRList(context.Background(), src, PRListArgs{Role: ListRoleAuthored, Provider: "gitea", Limit: n(v)}, nil); err != nil {
			t.Errorf("limit %d: %v", v, err)
		}
	}
	if g.asked[0].Limit != 1 || g.asked[1].Limit != 100 {
		t.Errorf("asked %+v", g.asked)
	}
}

// TestPRListMarkdownEmpty: the empty sentence is scoped to the providers
// listed by the lines below it.
func TestPRListMarkdownEmpty(t *testing.T) {
	g := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{
		provider.ListAuthor: pageOf(), provider.ListReviewer: pageOf()}}
	md := RenderPRListMarkdown(runList(t, newSource(nil, g), PRListArgs{Role: ListRoleAuthored}))
	if !strings.Contains(md, "You have no open pull requests.") || !strings.Contains(md, "- `gitea`: ok, 0 listed") {
		t.Errorf("markdown:\n%s", md)
	}
	md = RenderPRListMarkdown(runList(t, newSource(nil, g), PRListArgs{Role: ListRoleReviewRequests}))
	if !strings.Contains(md, "No open pull requests ask for your review.") {
		t.Errorf("markdown:\n%s", md)
	}
}

// TestPRListMarkdownUntrustedText: [canary m] a title and an author full of
// markup render inside code spans and break nothing: no HTML outside a code
// span, no link made of the title, one line per item.
func TestPRListMarkdownUntrustedText(t *testing.T) {
	const title = "fix `x` <b>bold</b> [x](http://evil.example) | cell"
	it := listedPR(provider.KindGitea, "demo", 7, title, listT0)
	it.Author = "eve <i>|`"
	branch := "feat/<x>"
	it.TargetBranch = &branch
	g := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{provider.ListAuthor: pageOf(it)}}
	md := RenderPRListMarkdown(runList(t, newSource(nil, g), PRListArgs{Role: ListRoleAuthored}))

	if !strings.Contains(md, mdutil.CodeSpan(title)) || !strings.Contains(md, mdutil.CodeSpan(it.Author)) ||
		!strings.Contains(md, mdutil.CodeSpan(branch)) {
		t.Errorf("title, author and branch are not code spans:\n%s", md)
	}
	assertNoHTML(t, md)
	// Outside the code spans the evil link does not exist.
	rest := md
	for _, span := range []string{mdutil.CodeSpan(title), mdutil.CodeSpan(it.Author), mdutil.CodeSpan(branch)} {
		rest = strings.Replace(rest, span, "", 1)
	}
	if strings.Contains(rest, "evil.example") || strings.Contains(rest, "<b>") {
		t.Errorf("untrusted text outside a code span:\n%s", rest)
	}
	// The item is one line, and the only link is to the built URL.
	var itemLines int
	for _, l := range strings.Split(md, "\n") {
		if strings.Contains(l, "demo!7") {
			itemLines++
			if !strings.Contains(l, "[demo!7](https://gitea.example.com/octo/demo/pulls/7)") {
				t.Errorf("item line = %q", l)
			}
		}
	}
	if itemLines != 1 {
		t.Errorf("%d item lines:\n%s", itemLines, md)
	}
}

// TestPRListLogsCountsOnly: the engine's debug lines carry kinds and counts,
// never a title or an author.
func TestPRListLogsCountsOnly(t *testing.T) {
	it := listedPR(provider.KindGitea, "demo", 7, "TITLE-MARKER-9d2f", listT0)
	it.Author = "AUTHOR-MARKER-9d2f"
	g := &listFake{kind: provider.KindGitea, caps: allRoles, answers: map[provider.ListRole]listAnswer{provider.ListAuthor: pageOf(it)}}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := PRList(context.Background(), newSource(nil, g), PRListArgs{Role: ListRoleAuthored}, log); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "pull request list") || !strings.Contains(out, "count=1") {
		t.Errorf("no counts logged:\n%s", out)
	}
	for _, s := range []string{"TITLE-MARKER", "AUTHOR-MARKER", "demo", "example.com"} {
		if strings.Contains(out, s) {
			t.Errorf("log contains %q:\n%s", s, out)
		}
	}
}
