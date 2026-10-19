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

// fakeFetcher records the branch fetches, in order, into trace; errs fails
// a repository (by name) with an error.
type fakeFetcher struct {
	trace *[]string
	repos []gitctx.Repo
	errs  map[string]error
}

func (f *fakeFetcher) EnsureBranch(_ context.Context, repo gitctx.Repo, branch string) (gitctx.Checkout, error) {
	*f.trace = append(*f.trace, "fetch "+repo.Name+"@"+branch)
	f.repos = append(f.repos, repo)
	if err := f.errs[repo.Name]; err != nil {
		return gitctx.Checkout{}, err
	}
	return gitctx.Checkout{GitDir: "/cache/git", HeadSHA: pinnedCommit, Ref: "refs/review-mcp/branch/x"}, nil
}

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
// the pull request items, fetched after them, sequentially, each with the
// configured provider's base URL and token; the primary's own repository
// (any branch) and a repeat of the same repository and branch are ignored;
// a fetch failure is the item's fixed reason; the logs carry no URL path,
// branch, commit or name.
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
	items := Read(context.Background(), r, "https://git.example/gitea/o/r/7", primary, urls, nil, false,
		&Repositories{Enabled: true, Config: repoConfig(), Fetcher: f}, log)

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
	if it := items[0]; !it.Fetched || it.Checkout.HeadSHA != pinnedCommit || it.Repo.Branch != "feature/BRANCH-MARK" ||
		it.Read || len(ReadItems(items)) != 1 {
		t.Errorf("fetched item %+v", it)
	}
	if !items[5].Fetched || items[5].Repo.Branch != "" {
		t.Errorf("default-branch item %+v", items[5])
	}
	// Resolution in argument order, then the pull request's reads, and the
	// fetches only after every item was resolved, in argument order.
	wantTrace := []string{"resolve repo web-ui", "resolve pr", "resolve repo R", "resolve repo web-ui", "resolve repo web-ui",
		"resolve repo gone", "resolve repo locked", "resolve repo slow",
		"fetch web-ui@feature/BRANCH-MARK", "fetch web-ui@", "fetch gone@x", "fetch locked@", "fetch slow@"}
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
	for _, leak := range []string{"BRANCH-MARK", "web-ui", "team", pinnedCommit, "gitlab.example", "/repo/"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the logs carry %q", leak)
		}
	}
	for _, want := range []string{"index=0 item=repository kind=gitlab status=fetched reason=\"\" identifiers=0 matches=0 matches_shown=0",
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
	}, nil, false, &Repositories{Enabled: false, Config: repoConfig(), Fetcher: f}, nil)
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
	// keeps a test backend that cannot fetch branches out.
	cfg := repoConfig()
	if rs := RepositoriesFor(cfg, f); rs.Enabled || rs.Fetcher != nil {
		t.Errorf("off: %+v", rs)
	}
	cfg.Context.Repo.Enabled = true
	if rs := RepositoriesFor(cfg, f); !rs.Enabled || rs.Fetcher != f {
		t.Errorf("on with a fetcher: %+v", rs)
	}
	if rs := RepositoriesFor(cfg, struct{}{}); rs.Fetcher != nil {
		t.Errorf("a backend without branches: %+v", rs)
	}
	if rs := RepositoriesFor(cfg, nil); rs.Fetcher == nil {
		t.Error("no backend: the real runner is not selected")
	}
	// With a fetcher missing, the item fails as unsupported, still without
	// a git command.
	items = Read(context.Background(), r, "", provider.PRRef{}, []string{"https://gitlab.example/repo/team/web-ui"}, nil, false,
		&Repositories{Enabled: true, Config: repoConfig()}, nil)
	if it := items[0]; it.Status != StatusFailed || it.Reason != gitctx.ReasonUnsupported {
		t.Errorf("no fetcher: %+v", it)
	}
}

// TestReadRepositoriesOffAndPRInfo: with related.max_tokens 0 nothing is
// resolved or fetched; without Repositories (pr_info) or with a resolver
// that cannot resolve repository URLs, every URL is a pull request URL, as
// in V-13a.
func TestReadRepositoriesOffAndPRInfo(t *testing.T) {
	var trace []string
	r, _, _ := newItemResolver(&trace)
	f := &fakeFetcher{trace: &trace}
	repos := &Repositories{Enabled: true, Config: repoConfig(), Fetcher: f}
	items := Read(context.Background(), r, "", provider.PRRef{}, []string{"https://gitlab.example/repo/team/web-ui"}, nil, true, repos, nil)
	if it := items[0]; it.Status != StatusNotPlaced || it.Reason != ReasonOff || len(trace) != 0 {
		t.Errorf("off: %+v, trace %q", it, trace)
	}
	items = ReadPullRequests(context.Background(), r, "", provider.PRRef{}, []string{"https://gitlab.example/repo/team/web-ui"}, nil)
	if it := items[0]; it.Status != StatusFailed || it.Reason != ReasonURLNotConfigured || it.IsRepository() || len(trace) != 0 {
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
// fetched, the commit; a pull request item carries none of them. A fetched
// item takes the run's not_placed reasons (Mark), is never did_not_fit
// (Place has nothing of it to place in WP-13a), and is otherwise not_placed
// without a reason. Failed and ignored repository items get their notes.
func TestCoverageRepositoryItems(t *testing.T) {
	var trace []string
	r, _, _ := newItemResolver(&trace)
	f := &fakeFetcher{trace: &trace, errs: map[string]error{"gone": &gitctx.Error{Reason: gitctx.ReasonBranchNotFound}}}
	read := func() []*Item {
		return Read(context.Background(), r, "", provider.PRRef{Kind: provider.KindGitea, Namespace: "o", Repo: "r"}, []string{
			"https://gitlab.example/gitlab/g/p/3", "https://gitlab.example/repo/team/web-ui",
			"https://gitlab.example/repo/team/api/release/2.5", "https://gitlab.example/repo/team/gone/x",
			"https://git.example/repo/o/r",
		}, nil, false, &Repositories{Enabled: true, Config: repoConfig(), Fetcher: f}, nil)
	}
	items := read()
	raw, err := json.Marshal(Coverage(items, nil))
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"kind", "branch", "commit", "identifiers", "matches", "matches_shown", "source"} {
		if _, ok := got[0][k]; ok {
			t.Errorf("the pull request item carries %q: %v", k, got[0])
		}
	}
	wantRepo := []map[string]any{
		{"kind": "repository", "branch": "", "commit": pinnedCommit, "status": "not_placed", "identifiers": 0.0, "matches": 0.0, "matches_shown": 0.0},
		{"kind": "repository", "branch": "release/2.5", "commit": pinnedCommit, "status": "not_placed"},
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
			t.Errorf("fetched entry %d has a reason: %v", i, got[i])
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
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("notes %q lack %q", notes, n)
		}
	}
	if len(notes) != 2 {
		t.Errorf("notes %q", notes)
	}

	// The run's reasons: review_in_parts reaches the fetched items; a block
	// that does not fit marks only the pull request item.
	items = read()
	Mark(items, StatusNotPlaced, ReasonReviewInParts)
	for _, i := range []int{0, 1, 2} {
		if items[i].Status != StatusNotPlaced || items[i].Reason != ReasonReviewInParts {
			t.Errorf("in parts, item %d: %q %q", i, items[i].Status, items[i].Reason)
		}
	}
	if items[3].Reason != ReasonBranchNotFound {
		t.Errorf("a failed item was marked: %+v", items[3])
	}
	items = read()
	if _, err := Place(items, 1000, 0, repoConfig(), nil, false, nil); err != nil {
		t.Fatal(err)
	}
	if items[0].Reason != ReasonDidNotFit || items[1].Status != "" || items[2].Status != "" {
		t.Errorf("did not fit: %+v %+v", items[0], items[1])
	}
}
