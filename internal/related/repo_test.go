package related

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// pinnedCommit is the commit the fake fetcher pins every branch at.
const pinnedCommit = "fedcba9876543210fedcba9876543210fedcba98"

// itemResolver is fakeResolver that also resolves repository URLs:
// "https://<host>/repo/<ns>/<name>[/<branch...>]" is the repository
// <ns>/<name> of host's provider, on the default branch without a branch.
// Every resolution and read is appended to trace.
type itemResolver struct {
	*fakeResolver
	trace *[]string
}

func (r itemResolver) ResolveItem(raw string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
	parts := strings.Split(strings.TrimPrefix(raw, "https://"), "/")
	if len(parts) >= 4 && parts[1] == "repo" {
		p := r.providers[parts[0]]
		if p == nil {
			return "", provider.PRRef{}, provider.RepoRef{}, nil, &provider.Error{Class: provider.ClassURLNotConfigured}
		}
		*r.trace = append(*r.trace, "resolve repo "+parts[3])
		return provider.ItemRepository, provider.PRRef{}, provider.RepoRef{Kind: p.kind, Namespace: parts[2], Repo: parts[3],
			Branch: strings.Join(parts[4:], "/"), URL: raw}, p, nil
	}
	*r.trace = append(*r.trace, "resolve pr")
	ref, p, err := r.Resolve(raw)
	return provider.ItemPullRequest, ref, provider.RepoRef{}, p, err
}

// fakeFetcher records the branch fetches and searches, in order, into
// trace; errs fails a repository's fetch (by name) with an error, and
// searchErrs its search. A search returns the hits of hits for the
// repository (by name), with found as Found when set.
type fakeFetcher struct {
	trace      *[]string
	repos      []gitctx.Repo
	errs       map[string]error
	searchErrs map[string]error
	hits       map[string][]gitctx.Hit
	found      map[string]int
	queries    []gitctx.TermQuery
}

func (f *fakeFetcher) EnsureBranch(_ context.Context, repo gitctx.Repo, branch string) (gitctx.Checkout, error) {
	*f.trace = append(*f.trace, "fetch "+repo.Name+"@"+branch)
	f.repos = append(f.repos, repo)
	if err := f.errs[repo.Name]; err != nil {
		return gitctx.Checkout{}, err
	}
	return gitctx.Checkout{GitDir: "/cache/" + repo.Name + "/git", HeadSHA: pinnedCommit, Ref: "refs/review-mcp/branch/x"}, nil
}

func (f *fakeFetcher) SearchTerms(_ context.Context, c gitctx.Checkout, q gitctx.TermQuery) (gitctx.TermResult, error) {
	name := strings.TrimSuffix(strings.TrimPrefix(c.GitDir, "/cache/"), "/git")
	*f.trace = append(*f.trace, "search "+name)
	f.queries = append(f.queries, q)
	if err := f.searchErrs[name]; err != nil {
		return gitctx.TermResult{}, err
	}
	hits := f.hits[name]
	found := len(hits)
	if n, ok := f.found[name]; ok {
		found = n
	}
	return gitctx.TermResult{Hits: hits, Found: found, Searched: len(q.Terms)}, nil
}

// enabledRepos is Repositories with f as fetcher and searcher.
func enabledRepos(f *fakeFetcher) *Repositories {
	return &Repositories{Enabled: true, Config: repoConfig(), Fetcher: f, Searcher: f}
}

// surface is a fixed changed contract surface of the tests.
var surface = []Identifier{{Text: "userName", Kind: KindGone}, {Text: "/users", Kind: KindRoute},
	{Text: "amountDue", Kind: KindChanged}, {Text: "FetchUser", Kind: KindSymbol}}

// repoConfig enables Gitea and GitLab with tokens, as RepoFor needs.
func repoConfig() *config.Config {
	cfg := config.Defaults()
	cfg.Gitea.BaseURL, cfg.Secrets.GiteaToken = "https://git.example", config.NewSecret("gitea-token")
	cfg.GitLab.BaseURL, cfg.Secrets.GitLabToken = "https://gitlab.example", config.NewSecret("gitlab-token")
	return cfg
}

func newItemResolver(trace *[]string) (itemResolver, *fakeProvider, *fakeProvider) {
	gitea := &fakeProvider{kind: provider.KindGitea}
	gitlab := &fakeProvider{kind: provider.KindGitLab}
	return itemResolver{fakeResolver: &fakeResolver{providers: map[string]*fakeProvider{
		"git.example": gitea, "gitlab.example": gitlab,
	}}, trace: trace}, gitea, gitlab
}

// TestReadRepositories: repository items are resolved in argument order with
// the pull request items, and not fetched by Read; Search fetches them after
// that, sequentially in argument order, each with the configured provider's
// base URL and token, and searches each one fetched; the primary's own
// repository (any branch) and a repeat of the same repository and branch
// are ignored; a fetch failure is the item's fixed reason; the logs carry no
// URL path, branch, commit, identifier or name.
func TestReadRepositories(t *testing.T) {
	var trace []string
	r, _, gitlab := newItemResolver(&trace)
	f := &fakeFetcher{trace: &trace, errs: map[string]error{
		"gone":   &gitctx.Error{Reason: gitctx.ReasonBranchNotFound},
		"locked": &gitctx.Error{Reason: gitctx.ReasonAuth},
		"slow":   &gitctx.Error{Reason: gitctx.ReasonTimeout},
	}}
	primary := provider.PRRef{Kind: provider.KindGitea, Namespace: "o", Repo: "r", Number: 7}
	urls := []string{
		"https://gitlab.example/repo/team/web-ui/feature/BRANCH-MARK", // fetched
		"https://gitlab.example/gitlab/g/p/3",                         // a pull request, read first
		"https://git.example/repo/O/R/other",                          // the primary's repository, another branch
		"https://gitlab.example/repo/team/web-ui/feature/BRANCH-MARK", // a duplicate URL
		"https://gitlab.example/repo/TEAM/web-ui/feature/BRANCH-MARK", // the same repository and branch
		"https://gitlab.example/repo/team/web-ui",                     // the same repository, default branch: fetched
		"https://gitlab.example/repo/team/gone/x",                     // branch_not_found
		"https://gitlab.example/repo/team/locked",                     // auth, reported as unauthorized
		"https://git.example/repo/team/slow",                          // timeout
		"https://unknown.example/repo/team/x",                         // url_not_configured
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	items := Read(context.Background(), r, "https://git.example/gitea/o/r/7", primary, urls, nil, false, enabledRepos(f), log)
	if slices.ContainsFunc(trace, func(s string) bool { return strings.HasPrefix(s, "fetch") }) {
		t.Fatalf("Read fetched: %q", trace)
	}
	if got := len(Pending(items)); got != 5 {
		t.Errorf("%d pending repository items, want 5", got)
	}
	Search(context.Background(), items, surface, 0, 20, log)

	want := []struct{ status, reason string }{
		{"", ""}, {"", ""}, {StatusIgnored, ReasonPrimaryRepository}, {StatusIgnored, ReasonDuplicate},
		{StatusIgnored, ReasonDuplicate}, {"", ""}, {StatusFailed, ReasonBranchNotFound},
		{StatusFailed, ReasonUnauthorized}, {StatusFailed, gitctx.ReasonTimeout}, {StatusFailed, ReasonURLNotConfigured},
	}
	for i, w := range want {
		if items[i].Status != w.status || items[i].Reason != w.reason {
			t.Errorf("item %d: %q %q, want %q %q", i, items[i].Status, items[i].Reason, w.status, w.reason)
		}
	}
	for _, i := range []int{0, 2, 3, 4, 5, 6, 7, 8} {
		if !items[i].IsRepository() {
			t.Errorf("item %d is not a repository item", i)
		}
	}
	if items[1].IsRepository() || !items[1].Read || items[9].IsRepository() {
		t.Errorf("pull request item %+v, unresolved item %+v", items[1], items[9])
	}
	if it := items[0]; !it.Fetched || !it.Searched || it.Checkout.HeadSHA != pinnedCommit || it.Repo.Branch != "feature/BRANCH-MARK" ||
		it.Read || len(ReadItems(items)) != 1 || it.Identifiers != 4 {
		t.Errorf("fetched item %+v", it)
	}
	if !items[5].Fetched || !items[5].Searched || items[5].Repo.Branch != "" {
		t.Errorf("default-branch item %+v", items[5])
	}
	if items[6].Fetched || items[6].Searched {
		t.Errorf("a failed fetch is searched: %+v", items[6])
	}
	// Resolution in argument order, then the pull request's reads; the
	// fetches only in Search, in argument order, each followed by its search.
	wantTrace := []string{"resolve repo web-ui", "resolve pr", "resolve repo R", "resolve repo web-ui", "resolve repo web-ui",
		"resolve repo gone", "resolve repo locked", "resolve repo slow",
		"fetch web-ui@feature/BRANCH-MARK", "search web-ui", "fetch web-ui@", "search web-ui", "fetch gone@x", "fetch locked@",
		"fetch slow@"}
	if !slices.Equal(trace, wantTrace) {
		t.Errorf("trace %q\nwant  %q", trace, wantTrace)
	}
	if !slices.Equal(gitlab.reads, []string{"pr 3", "diff 3"}) {
		t.Errorf("gitlab reads %q", gitlab.reads)
	}
	if got := f.repos[0]; got.Kind != provider.KindGitLab || got.BaseURL != "https://gitlab.example" || got.Namespace != "team" ||
		got.Name != "web-ui" || got.Token.Reveal() != "gitlab-token" || got.Identity == nil {
		t.Errorf("fetched repository %+v", got)
	}
	if got := f.repos[4]; got.Kind != provider.KindGitea || got.BaseURL != "https://git.example" {
		t.Errorf("gitea repository %+v", got)
	}
	q := f.queries[0]
	if len(q.Terms) != 4 || !q.Terms[0].Word || q.Terms[1].Word || q.Terms[1].Text != "/users" || q.Filter != nil ||
		q.MaxPerTerm != 5 || q.MaxTotal != 30 || q.ContextLines != 2 || q.Paths != nil {
		t.Errorf("query %+v", q)
	}
	for _, leak := range []string{"BRANCH-MARK", "web-ui", "team", pinnedCommit, "gitlab.example", "/repo/", "userName", "/users"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the logs carry %q", leak)
		}
	}
	for _, want := range []string{"index=0 item=repository kind=gitlab status=readable reason=\"\" identifiers=0 matches=0 matches_shown=0",
		"index=0 item=repository kind=gitlab status=searched reason=\"\" identifiers=4 matches=0 matches_shown=0",
		"index=6 item=repository kind=gitlab status=failed reason=branch_not_found",
		"index=2 item=repository kind=gitea status=ignored reason=primary_repository"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the logs lack %q:\n%s", want, logs.String())
		}
	}
}

// TestReadRepositoriesContextOff: without context.repo.enabled (always so in
// serve mode) every repository item is failed with repo_context_off and no
// git command runs; pull request items are read as before.
func TestReadRepositoriesContextOff(t *testing.T) {
	var trace []string
	r, _, gitlab := newItemResolver(&trace)
	f := &fakeFetcher{trace: &trace}
	items := Read(context.Background(), r, "https://git.example/gitea/o/r/7", provider.PRRef{}, []string{
		"https://gitlab.example/repo/team/web-ui", "https://gitlab.example/gitlab/g/p/3", "https://git.example/repo/team/api/main",
	}, nil, false, &Repositories{Enabled: false, Config: repoConfig(), Fetcher: f, Searcher: f}, nil)
	Search(context.Background(), items, surface, 0, 20, nil)
	for _, i := range []int{0, 2} {
		if it := items[i]; it.Status != StatusFailed || it.Reason != ReasonRepoContextOff || !it.IsRepository() || it.Fetched {
			t.Errorf("item %d: %+v", i, it)
		}
	}
	if !items[1].Read || len(gitlab.reads) != 2 {
		t.Errorf("the pull request item %+v, reads %q", items[1], gitlab.reads)
	}
	if len(f.repos) != 0 || slices.ContainsFunc(trace, func(s string) bool { return strings.HasPrefix(s, "fetch") }) {
		t.Errorf("a git fetch ran: %q", trace)
	}
	// RepositoriesFor builds no fetcher without context.repo.enabled, and
	// keeps a test backend that cannot fetch branches out. The search
	// filter is the operator's: the ignore globs of the merged configuration
	// do not reach it.
	cfg, operator := repoConfig(), repoConfig()
	cfg.Ignore.Glob = []string{"web/**"}
	if rs := RepositoriesFor(cfg, operator, f); rs.Enabled || rs.Fetcher != nil || rs.Searcher != nil {
		t.Errorf("off: %+v", rs)
	}
	cfg.Context.Repo.Enabled = true
	rs := RepositoriesFor(cfg, operator, f)
	if !rs.Enabled || rs.Fetcher != f || rs.Searcher != f || rs.Filter == nil || !rs.Filter.Include("web/app.ts") {
		t.Errorf("on with a fetcher: %+v", rs)
	}
	if rs := RepositoriesFor(cfg, nil, f); rs.Filter == nil || rs.Filter.Include("web/app.ts") {
		t.Errorf("without an operator configuration the run's filter is used: %+v", rs)
	}
	if rs := RepositoriesFor(cfg, operator, struct{}{}); rs.Fetcher != nil || rs.Searcher != nil {
		t.Errorf("a backend without branches: %+v", rs)
	}
	if rs := RepositoriesFor(cfg, operator, nil); rs.Fetcher == nil || rs.Searcher == nil {
		t.Error("no backend: the real runner is not selected")
	}
	// With a fetcher or a searcher missing, the item fails as unsupported,
	// still without a git command.
	for _, repos := range []*Repositories{{Enabled: true, Config: repoConfig()}, {Enabled: true, Config: repoConfig(), Fetcher: f}} {
		items = Read(context.Background(), r, "", provider.PRRef{}, []string{"https://gitlab.example/repo/team/web-ui"}, nil, false,
			repos, nil)
		if it := items[0]; it.Status != StatusFailed || it.Reason != gitctx.ReasonUnsupported || len(Pending(items)) != 0 {
			t.Errorf("no fetcher or searcher: %+v", it)
		}
	}
	if len(f.repos) != 0 {
		t.Errorf("a git fetch ran: %q", trace)
	}
}

// TestReadRepositoriesOffAndPRInfo: with related.max_tokens 0 nothing is
// resolved or fetched; pr_info (ReadPullRequests) resolves a repository URL
// without reading it (no status, nothing fetched, V-13b §4 item 2); with a
// resolver that cannot resolve repository URLs, every URL is a pull request
// URL, as in V-13a.
func TestReadRepositoriesOffAndPRInfo(t *testing.T) {
	var trace []string
	r, _, _ := newItemResolver(&trace)
	f := &fakeFetcher{trace: &trace}
	repos := enabledRepos(f)
	items := Read(context.Background(), r, "", provider.PRRef{}, []string{"https://gitlab.example/repo/team/web-ui"}, nil, true, repos, nil)
	if it := items[0]; it.Status != StatusNotPlaced || it.Reason != ReasonOff || len(trace) != 0 {
		t.Errorf("off: %+v, trace %q", it, trace)
	}
	items = ReadPullRequests(context.Background(), r, "", provider.PRRef{}, []string{"https://gitlab.example/repo/team/web-ui"}, nil)
	if it := items[0]; it.Status != "" || !it.IsRepository() || it.Repo.Repo != "web-ui" || it.Fetched || len(Pending(items)) != 0 ||
		len(f.repos) != 0 || slices.Contains(trace, "fetch") {
		t.Errorf("pr_info: %+v, trace %q", it, trace)
	}
	items = Read(context.Background(), r.fakeResolver, "", provider.PRRef{}, []string{"https://gitlab.example/repo/team/web-ui"}, nil,
		false, repos, nil)
	if it := items[0]; it.IsRepository() || it.Status != StatusFailed || len(f.repos) != 0 {
		t.Errorf("a resolver without ResolveItem: %+v", it)
	}
}

// TestCoverageRepositoryItems: a repository item's entry carries kind,
// branch ("" for the default branch), the counts (0 included) and, when
// fetched, the commit; a pull request item carries none of them. An item is
// fetched only by Search, so one the run marks first (review_in_parts,
// nothing_to_review) has no commit; a block that does not fit marks the
// searched repository items too. Failed and ignored repository items get
// their notes.
func TestCoverageRepositoryItems(t *testing.T) {
	var trace []string
	r, _, _ := newItemResolver(&trace)
	f := &fakeFetcher{trace: &trace, errs: map[string]error{"gone": &gitctx.Error{Reason: gitctx.ReasonBranchNotFound}},
		hits: map[string][]gitctx.Hit{"web-ui": {{Symbol: "userName", Path: "src/a.ts", Line: 3, Snippet: "x.userName"}}}}
	read := func() []*Item {
		return Read(context.Background(), r, "", provider.PRRef{Kind: provider.KindGitea, Namespace: "o", Repo: "r"}, []string{
			"https://gitlab.example/gitlab/g/p/3", "https://gitlab.example/repo/team/web-ui",
			"https://gitlab.example/repo/team/api/release/2.5", "https://gitlab.example/repo/team/gone/x",
			"https://git.example/repo/o/r",
		}, nil, false, enabledRepos(f), nil)
	}
	items := read()
	Search(context.Background(), items, surface, 0, 20, nil)
	if _, err := Place(items, 100000, 0, repoConfig(), nil, true, func(string, bool) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	got := coverageMaps(t, items, nil)
	for _, k := range []string{"kind", "branch", "commit", "identifiers", "matches", "matches_shown", "source"} {
		if _, ok := got[0][k]; ok {
			t.Errorf("the pull request item carries %q: %v", k, got[0])
		}
	}
	wantRepo := []map[string]any{
		{"kind": "repository", "branch": "", "commit": pinnedCommit, "status": "full", "identifiers": 4.0, "matches": 1.0, "matches_shown": 1.0},
		{"kind": "repository", "branch": "release/2.5", "commit": pinnedCommit, "status": "no_use_found", "identifiers": 4.0, "matches": 0.0},
		{"kind": "repository", "branch": "x", "status": "failed", "reason": "branch_not_found", "identifiers": 0.0},
		{"kind": "repository", "branch": "", "status": "ignored", "reason": "primary_repository"},
	}
	for i, w := range wantRepo {
		e := got[i+1]
		for k, v := range w {
			if e[k] != v {
				t.Errorf("entry %d: %s = %v, want %v (%v)", i+1, k, e[k], v, e)
			}
		}
		if _, ok := e["source"]; ok {
			t.Errorf("entry %d carries a source", i+1)
		}
	}
	for _, i := range []int{1, 2} {
		if _, ok := got[i]["reason"]; ok {
			t.Errorf("searched entry %d has a reason: %v", i, got[i])
		}
	}
	for _, i := range []int{3, 4} {
		if _, ok := got[i]["commit"]; ok {
			t.Errorf("entry %d has a commit without a fetch: %v", i, got[i])
		}
	}
	notes := Notes(items, nil)
	for _, n := range []string{
		"Related repository https://gitlab.example/repo/team/gone/x could not be read (branch_not_found) and was not used.",
		"Related repository https://git.example/repo/o/r was ignored: it is the reviewed pull request's own repository or a duplicate.",
		"No use of the changed identifiers was found in team/api at " + pinnedCommit[:12] + ". Text search does not see dynamic " +
			"or generated uses, so this is not a statement that nothing is affected.",
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("notes %q lack %q", notes, n)
		}
	}
	if len(notes) != 3 {
		t.Errorf("notes %q", notes)
	}

	// The run's reasons reach the readable repository items, which are then
	// never fetched.
	trace = nil
	items = read()
	Mark(items, StatusNotPlaced, ReasonReviewInParts)
	for _, i := range []int{0, 1, 2, 3} {
		if items[i].Status != StatusNotPlaced || items[i].Reason != ReasonReviewInParts || items[i].Fetched {
			t.Errorf("in parts, item %d: %+v", i, items[i])
		}
	}
	Search(context.Background(), items, surface, 0, 20, nil)
	if slices.ContainsFunc(trace, func(s string) bool { return strings.HasPrefix(s, "fetch") }) {
		t.Errorf("a marked item was fetched: %q", trace)
	}
	if got := coverageMaps(t, items, nil); got[1]["commit"] != nil || got[1]["reason"] != ReasonReviewInParts {
		t.Errorf("in parts: %v", got[1])
	}

	// A block that does not fit marks the pull request and the searched
	// repositories; one that cannot be tried (the primary diff is trimmed)
	// leaves the repositories unsearched and marks them too.
	items = read()
	Search(context.Background(), items, surface, 0, 20, nil)
	if _, err := Place(items, 1000, 0, repoConfig(), nil, true, func(string, bool) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1, 2} {
		if items[i].Status != StatusNotPlaced || items[i].Reason != ReasonDidNotFit {
			t.Errorf("did not fit, item %d: %+v", i, items[i])
		}
	}
	if items[1].MatchesShown != 0 || items[1].Matches != 1 {
		t.Errorf("did not fit: counts %+v", items[1])
	}
	items = read()
	trace = nil
	SearchFiles(context.Background(), items, []provider.FilePatch{fp("a.proto", "@@ -1,1 +1,0 @@", "-  string gone_field = 1;")},
		nil, 20, false, nil)
	if _, err := Place(items, 1000, 0, repoConfig(), nil, false, nil); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1, 2} {
		if items[i].Status != StatusNotPlaced || items[i].Reason != ReasonDidNotFit || items[i].Fetched {
			t.Errorf("no room, item %d: %+v", i, items[i])
		}
	}
	if len(trace) != 0 {
		t.Errorf("git ran without room for the block: %q", trace)
	}
}

// coverageMaps is Coverage as decoded JSON objects.
func coverageMaps(t *testing.T, items []*Item, placed []Section) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(Coverage(items, placed))
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}
