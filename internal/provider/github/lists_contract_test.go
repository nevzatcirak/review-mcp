package github_test

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
	"github.com/nevzatcirak/review-mcp/internal/provider/github"
)

// ServeLists implements contract.ListFixture: a fake GET /search/issues
// (E3) below the GHES-style API base {origin}/api/v3. The fixture checks
// the query (canary b) and records the assumption that repository_url is
// {api base}/repos/{owner}/{repo} (the docs give the field's schema, not
// an example).
func (f ctFixture) ServeLists(t *testing.T, ls contract.ListSpec) (provider.Provider, *provider.Resolver) {
	t.Helper()
	p, res, _ := serveLists(t, ls, f.selfLogin)
	return p, res
}

func serveLists(t *testing.T, ls contract.ListSpec, selfLogin string) (provider.Provider, *provider.Resolver, *ctSearch) {
	t.Helper()
	h := &ctSearch{t: t, ls: ls, refuseUser: selfLogin != ""}
	cfg := config.Defaults()
	cfg.GitHub.BaseURL = contract.StartListServer(t, ls, h)
	cfg.GitHub.SelfLogin = selfLogin
	cfg.Secrets.GitHubToken = config.NewSecret(ls.Env.Token)
	fac := github.NewFactory()
	p, err := fac.New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, provider.NewResolver(cfg, nil, fac), h
}

type ctSearch struct {
	t          *testing.T
	ls         contract.ListSpec
	refuseUser bool

	mu sync.Mutex
	// searches counts the search requests; status, when set, answers them.
	searches int
	status   int
	header   http.Header
	// extra items are added to every answer, as the host would serve them.
	extra []map[string]any
	total int
}

var ctQualifiers = map[string]provider.ListRole{"review-requested": provider.ListReviewer,
	"assignee": provider.ListAssignee, "author": provider.ListAuthor}

func (h *ctSearch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := r.URL.EscapedPath()
	if r.Header.Get("Authorization") != "Bearer "+h.ls.Env.Token {
		h.t.Errorf("request %s %s: unexpected Authorization header", r.Method, path)
		contract.WriteError(w, http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && path == "/api/v3/user" && !h.refuseUser:
		ctJSON(w, http.StatusOK, ctUser(h.ls.TokenUser))
		return
	case r.Method == http.MethodGet && path == "/api/v3/search/issues":
	default:
		h.t.Errorf("unexpected request %s %s", r.Method, path)
		contract.WriteError(w, http.StatusNotFound)
		return
	}
	h.searches++
	if h.status != 0 {
		for k, v := range h.header {
			w.Header()[k] = v
		}
		contract.WriteError(w, h.status)
		return
	}
	q := r.URL.Query()
	if q.Get("sort") != "updated" || q.Get("order") != "desc" {
		h.t.Errorf("the search has sort=%q order=%q, want updated and desc", q.Get("sort"), q.Get("order"))
	}
	terms := strings.Fields(q.Get("q"))
	// Canary (b): the query asks for open pull requests of the token user.
	issues := !slices.Contains(terms, "is:pr")
	for _, want := range []string{"is:pr", "is:open", "archived:false"} {
		if !slices.Contains(terms, want) {
			h.t.Errorf("the search query %q lacks %s", q.Get("q"), want)
		}
	}
	var role provider.ListRole
	n := 0
	for _, term := range terms {
		k, v, ok := strings.Cut(term, ":")
		if r, known := ctQualifiers[k]; ok && known {
			n++
			role = r
			if v != h.ls.TokenUser.Login {
				h.t.Errorf("the search query has %s:%s, want the token user's login %s", k, v, h.ls.TokenUser.Login)
			}
		}
	}
	if n != 1 || len(terms) != 4 {
		h.t.Errorf("the search query %q, want is:pr is:open archived:false and one role qualifier", q.Get("q"))
	}
	limit, err := strconv.Atoi(q.Get("per_page"))
	if err != nil || limit < 1 || limit > provider.MaxListLimit {
		h.t.Errorf("the search has per_page=%q", q.Get("per_page"))
	}
	items, more := h.ls.ListPage(role, limit)
	out := make([]any, 0, len(items)+1)
	for _, it := range items {
		out = append(out, ctSearchItem(r, it))
	}
	for _, e := range h.extra {
		out = append(out, e)
	}
	if issues {
		// Without is:pr the host answers issues as well.
		out = append(out, map[string]any{"number": 99, "title": "An issue", "state": "open",
			"updated_at": "2026-01-02T03:04:00Z", "user": map[string]any{"login": "carol", "id": 103},
			"repository_url": "http://" + r.Host + "/api/v3/repos/octo/demo",
			"html_url":       "http://" + r.Host + "/octo/demo/issues/99"})
	}
	total := len(out)
	if more {
		total += 5
	}
	if h.total != 0 {
		total = h.total
	}
	ctJSON(w, http.StatusOK, map[string]any{"total_count": total, "incomplete_results": false, "items": out})
}

func ctSearchItem(r *http.Request, it contract.ListItem) map[string]any {
	host := r.Host
	if it.ForeignURL {
		host = contract.ForeignHost
	}
	n := strconv.FormatInt(it.Number, 10)
	return map[string]any{
		"number": it.Number, "title": it.Title, "state": "open", "draft": it.Draft,
		"updated_at": it.Updated, "created_at": it.Updated.Add(-48 * 3600e9),
		"user":           ctUser(it.Author),
		"repository_url": "http://" + r.Host + "/api/v3/repos/" + url.PathEscape(it.Namespace) + "/" + url.PathEscape(it.Repo),
		"html_url":       "http://" + host + "/" + it.Namespace + "/" + it.Repo + "/pull/" + n,
		"pull_request":   map[string]any{"html_url": "http://" + host + "/" + it.Namespace + "/" + it.Repo + "/pull/" + n},
	}
}

func oneItem() contract.ListSpec {
	return contract.ListSpec{TokenUser: contract.User{ID: 900, Login: "review-bot"},
		Items: map[provider.ListRole][]contract.ListItem{provider.ListReviewer: {
			{Namespace: "octo", Repo: "demo", Number: 7, Title: "T", Author: contract.User{ID: 1, Login: "alice"}},
		}}, Env: contract.Env{Token: "tok-github-list"}} //nolint:gosec // synthetic test value
}

// TestListSelfLogin: with github.self_login the login is searched for
// without GET /user (the fake refuses it).
func TestListSelfLogin(t *testing.T) {
	ls := oneItem()
	p, _, _ := serveLists(t, ls, "review-bot")
	l, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListReviewer, Limit: 30})
	if err != nil || len(l.Items) != 1 {
		t.Fatalf("ListMyPullRequests = %+v, %v", l, err)
	}
}

// TestListLoginRefused: a login that would change the query is refused
// before the search.
func TestListLoginRefused(t *testing.T) {
	for _, login := range []string{"a b", "x\"y", "a:b", "-a", strings.Repeat("a", 40), ""} {
		ls := oneItem()
		ls.TokenUser.Login = login
		p, _, h := serveLists(t, ls, login)
		if login == "" {
			p, _, h = serveLists(t, ls, "")
		}
		if _, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListReviewer, Limit: 30}); err == nil {
			t.Errorf("login %q: no error", login)
		}
		if h.searches != 0 {
			t.Errorf("login %q: %d searches", login, h.searches)
		}
	}
}

// TestListForeignRepository: an item whose repository_url is not below the
// configured API base is dropped and counted, as is one with a path of
// another shape; incomplete_results and a total above the page set
// HasMore.
func TestListForeignRepository(t *testing.T) {
	ls := oneItem()
	p, _, h := serveLists(t, ls, "")
	bad := func(repoURL string) map[string]any {
		return map[string]any{"number": 9, "title": "x", "updated_at": "2026-01-02T03:04:00Z", "pull_request": map[string]any{},
			"user": map[string]any{"login": "eve"}, "repository_url": repoURL}
	}
	h.extra = []map[string]any{
		bad("https://api.github.com/repos/octo/demo"),
		bad("https://" + contract.ForeignHost + "/api/v3/repos/octo/demo"),
		{"number": 10, "title": "x", "updated_at": "2026-01-02T03:04:00Z", "pull_request": map[string]any{},
			"user": nil, "repository_url": "PLACEHOLDER"},
	}
	l, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListReviewer, Limit: 30})
	if err != nil || len(l.Items) != 1 || l.Dropped != 3 || l.HasMore {
		t.Fatalf("ListMyPullRequests = %+v, %v; want one item, three dropped", l, err)
	}
	h.extra, h.total = nil, 50
	l, err = p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListReviewer, Limit: 30})
	if err != nil || !l.HasMore {
		t.Fatalf("total_count above the page: %+v, %v; want HasMore", l, err)
	}
}

// TestListSearchRateLimit: a search rate limit is rate_limited after one
// request, without the wait and retry of the other requests.
func TestListSearchRateLimit(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		p, _, h := serveLists(t, oneItem(), "")
		h.status = status
		h.header = http.Header{"X-Ratelimit-Remaining": {"0"}, "Retry-After": {"1"}}
		_, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListReviewer, Limit: 30})
		if !errors.Is(err, provider.ErrRateLimited) {
			t.Errorf("HTTP %d: error %v, want rate_limited", status, err)
		}
		if h.searches != 1 {
			t.Errorf("HTTP %d: %d searches, want exactly one", status, h.searches)
		}
	}
}
