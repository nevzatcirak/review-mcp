package related

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitlab"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
)

// Related repositories declared in the repository config (v2.5 spec V-13b
// §3.2).

// TestReadDeclared: every declaration is a repository item with source
// repo_config: one that resolves is fetched and searched like an argument's
// repository, on the declared branch and below the declared paths; one on
// a host no provider is configured for is failed with url_not_configured,
// one naming a pull request, or a branch other than its URL's, with
// url_malformed, and none of these is read or fetched; coverage.related
// carries the source on each.
func TestReadDeclared(t *testing.T) {
	var trace []string
	r, _, gitlab := newItemResolver(&trace)
	f := &fakeFetcher{trace: &trace}
	decls := []repocfg.Declared{
		{URL: "https://gitlab.example/repo/grp/web-ui", Branch: "main", Paths: []string{"src/api/"}},
		{URL: "https://unknown.example/repo/grp/web-ui"},
		{URL: "https://gitlab.example/x/grp/svc/3"},
		{URL: "https://gitlab.example/repo/grp/mobile/release/2", Branch: "main"},
		{URL: "https://gitlab.example/repo/grp/desk/release/2", Branch: "release/2"},
		{URL: "not a url"},
	}
	items := ReadDeclared(context.Background(), r, "https://git.example/octo/demo/pulls/7",
		provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7}, decls, false, enabledRepos(f), nil)
	if len(items) != len(decls) {
		t.Fatalf("%d items", len(items))
	}
	for i, it := range items {
		if !it.IsRepository() || it.Source != SourceRepoConfig {
			t.Errorf("item %d: kind %q source %q", i, it.Kind, it.Source)
		}
	}
	for i, want := range []struct{ status, reason string }{{"", ""}, {"failed", "url_not_configured"}, {"failed", "url_malformed"},
		{"failed", "url_malformed"}, {"", ""}, {"failed", "url_malformed"}} {
		if it := items[i]; it.Status != want.status || it.Reason != want.reason {
			t.Errorf("item %d: %q/%q, want %q/%q", i, it.Status, it.Reason, want.status, want.reason)
		}
	}
	if len(gitlab.reads) != 0 || len(f.repos) != 0 {
		t.Fatalf("read before Search: provider %q, fetches %d", gitlab.reads, len(f.repos))
	}
	Search(context.Background(), items, surface, 0, 20, nil)
	if !slices.Equal(trace, []string{"resolve repo web-ui", "resolve pr", "resolve repo mobile", "resolve repo desk",
		"fetch web-ui@main", "search web-ui", "fetch desk@release/2", "search desk"}) {
		t.Fatalf("trace %q", trace)
	}
	if q := f.queries[0]; !slices.Equal(q.Paths, []string{"src/api/"}) || f.queries[1].Paths != nil {
		t.Fatalf("paths %+v %+v", f.queries[0].Paths, f.queries[1].Paths)
	}
	if len(gitlab.reads) != 0 {
		t.Fatalf("a pull request was read: %q", gitlab.reads)
	}
	for _, it := range Pending(items) {
		t.Errorf("still pending: %+v", it)
	}
	cov, err := json.Marshal(Coverage(items, nil))
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(cov, &entries); err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		if e["source"] != "repo_config" || e["kind"] != "repository" {
			t.Errorf("entry %d %v", i, e)
		}
	}
	if entries[0]["branch"] != "main" || entries[1]["branch"] != "" || entries[3]["branch"] != "main" {
		t.Errorf("branches %v %v %v", entries[0]["branch"], entries[1]["branch"], entries[3]["branch"])
	}
}

// TestReadDeclaredUnconfiguredHost: a declaration on a host no provider is
// configured for goes through the run's real resolver (pinned hosts) and is
// failed with url_not_configured without any request, to that host or to
// the configured one, and without any git command: a repository config can
// never add a host. ResolveDeclared (pr_info) does the same.
func TestReadDeclaredUnconfiguredHost(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cfg := config.Defaults()
	cfg.GitLab.BaseURL, cfg.Secrets.GitLabToken = srv.URL, config.NewSecret("gitlab-token")
	res := provider.NewResolver(cfg, nil, gitlab.NewFactory())
	var trace []string
	f := &fakeFetcher{trace: &trace}
	decls := []repocfg.Declared{{URL: "https://git.example/team/web-ui"}, {URL: "https://your-gitea.example/team/web-ui/src/branch/main"}}
	repos := &Repositories{Enabled: true, Config: cfg, Fetcher: f, Searcher: f}
	items := ReadDeclared(context.Background(), res, srv.URL+"/grp/demo/-/merge_requests/7",
		provider.PRRef{Kind: provider.KindGitLab, Namespace: "grp", Repo: "demo", Number: 7}, decls, false, repos, nil)
	Search(context.Background(), items, surface, 0, 20, nil)
	resolved := ResolveDeclared(res, provider.PRRef{Kind: provider.KindGitLab, Namespace: "grp", Repo: "demo", Number: 7}, decls, nil)
	for _, set := range [][]*Item{items, resolved} {
		for i, it := range set {
			if it.Status != StatusFailed || it.Reason != ReasonURLNotConfigured || it.Source != SourceRepoConfig || !it.IsRepository() {
				t.Errorf("item %d %+v", i, it)
			}
		}
	}
	if n := requests.Load(); n != 0 || len(trace) != 0 {
		t.Fatalf("%d requests, git %q", n, trace)
	}
	if n := NoteRepositoryFailed("https://git.example/team/web-ui", "url_not_configured"); !slices.Contains(Notes(items, nil), n) {
		t.Errorf("notes %q lack %q", Notes(items, nil), n)
	}
}

// TestResolveDeclared: pr_info's resolution of the declarations reads and
// fetches nothing; a resolvable one keeps no status; the primary's own
// repository and a duplicate are ignored.
func TestResolveDeclared(t *testing.T) {
	var trace []string
	r, gitea, gitlab := newItemResolver(&trace)
	items := ResolveDeclared(r, provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7},
		[]repocfg.Declared{
			{URL: "https://gitlab.example/repo/grp/web-ui", Branch: "main"},
			{URL: "https://gitlab.example/repo/grp/web-ui/main"},
			{URL: "https://git.example/repo/octo/demo"},
		}, nil)
	for i, want := range []struct{ status, reason string }{{"", ""}, {"ignored", "duplicate"}, {"ignored", "primary_repository"}} {
		if it := items[i]; it.Status != want.status || it.Reason != want.reason || it.Source != SourceRepoConfig {
			t.Errorf("item %d %+v", i, it)
		}
	}
	if items[0].Repo.Branch != "main" || items[0].Name() != "grp/web-ui" {
		t.Errorf("item 0 %+v", items[0].Repo)
	}
	if len(gitea.reads)+len(gitlab.reads) != 0 {
		t.Fatalf("reads %q %q", gitea.reads, gitlab.reads)
	}
}
