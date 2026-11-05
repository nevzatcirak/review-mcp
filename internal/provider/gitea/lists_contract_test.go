package gitea_test

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
)

// ctListWeb is the web base of the list fixture; its API base is the
// fake's origin, so a built URL must use the web base.
const ctListWeb = "https://gitea.example.com"

// ServeLists implements contract.ListFixture: a fake GET /api/v1/version
// (1.21.11 unless a test sets another answer) and GET
// /api/v1/repos/issues/search (E4, Gitea 1.14+). The fixture serves the
// items in the spec's order (the host orders by creation, WP-19a DQ-5),
// pull_request.draft (Gitea 1.22+) and X-Total-Count.
func (ctFixture) ServeLists(t *testing.T, ls contract.ListSpec) (provider.Provider, *provider.Resolver) {
	t.Helper()
	p, res, _ := serveCtLists(t, ls)
	return p, res
}

func serveCtLists(t *testing.T, ls contract.ListSpec) (provider.Provider, *provider.Resolver, *ctIssueSearch) {
	t.Helper()
	h := &ctIssueSearch{t: t, ls: ls, versionBody: `{"version":"1.21.11"}`}
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = contract.StartListServer(t, ls, h)
	cfg.Gitea.WebURL = ctListWeb
	cfg.Secrets.GiteaToken = config.NewSecret(ls.Env.Token)
	f := gitea.NewFactory()
	p, err := f.New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, provider.NewResolver(cfg, nil, f), h
}

type ctIssueSearch struct {
	t  *testing.T
	ls contract.ListSpec
	// clamp, when positive, is the host's MaxResponseItems (Gitea before
	// 1.26 caps limit at it).
	clamp int
	// noDraft serves pull_request without draft (Gitea before 1.22).
	noDraft bool
	// versionStatus, when set, is the status of GET /api/v1/version;
	// versionBody is its body.
	versionStatus int
	versionBody   string

	mu    sync.Mutex
	paths []string
}

// requests returns the paths of the requests served so far, in order.
func (h *ctIssueSearch) requests() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.paths...)
}

var ctFilters = map[string]provider.ListRole{"review_requested": provider.ListReviewer,
	"assigned": provider.ListAssignee, "created": provider.ListAuthor}

func (h *ctIssueSearch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	h.mu.Lock()
	h.paths = append(h.paths, r.Method+" "+path)
	h.mu.Unlock()
	if r.Method == http.MethodGet && path == "/api/v1/version" {
		if h.versionStatus != 0 {
			contract.WriteError(w, h.versionStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(h.versionBody))
		return
	}
	if r.Header.Get("Authorization") != "token "+h.ls.Env.Token || r.Method != http.MethodGet || path != "/api/v1/repos/issues/search" {
		h.t.Errorf("request %s %s: not a GET of the issue search with the token", r.Method, path)
		contract.WriteError(w, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	// Canary (c): pull requests only, and one filter of the signed-in user.
	if q.Get("type") != "pulls" || q.Get("state") != "open" || q.Get("page") != "1" {
		h.t.Errorf("the issue search has type=%q state=%q page=%q, want pulls, open and 1", q.Get("type"), q.Get("state"), q.Get("page"))
	}
	var role provider.ListRole
	n := 0
	for k := range q {
		if r, ok := ctFilters[k]; ok {
			n++
			role = r
			if q.Get(k) != "true" {
				h.t.Errorf("the issue search has %s=%q, want true", k, q.Get(k))
			}
		} else if k != "type" && k != "state" && k != "page" && k != "limit" {
			h.t.Errorf("the issue search has the parameter %q", k)
		}
	}
	if n != 1 {
		h.t.Errorf("the issue search has %d role filters, want one", n)
	}
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit < 1 || limit > provider.MaxListLimit {
		h.t.Errorf("the issue search has limit=%q", q.Get("limit"))
	}
	if h.clamp > 0 && limit > h.clamp {
		limit = h.clamp
	}
	items, _ := h.ls.ListPage(role, limit)
	out := make([]any, 0, len(items))
	for _, it := range items {
		host := ctListWeb
		if it.ForeignURL {
			host = "https://" + contract.ForeignHost
		}
		pr := map[string]any{"merged": false, "merged_at": nil}
		if !h.noDraft {
			pr["draft"] = it.Draft
		}
		out = append(out, map[string]any{
			"id": 3000 + it.Number, "number": it.Number, "title": it.Title, "state": "open",
			"updated_at": it.Updated, "created_at": it.Updated.Add(-48 * 3600e9),
			"user":         map[string]any{"id": it.Author.ID, "login": it.Author.Login},
			"html_url":     host + "/" + it.Namespace + "/" + it.Repo + "/pulls/" + strconv.FormatInt(it.Number, 10),
			"repository":   map[string]any{"id": 77, "name": it.Repo, "owner": it.Namespace, "full_name": it.Namespace + "/" + it.Repo},
			"pull_request": pr,
		})
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(h.ls.Items[role])))
	ctJSON(w, http.StatusOK, out)
}

// TestListClampedLimit: a host that caps limit at its MaxResponseItems
// answers fewer items than asked; X-Total-Count still gives HasMore.
// Before Gitea 1.22 there is no draft flag: Draft is nil.
func TestListClampedLimit(t *testing.T) {
	var items []contract.ListItem
	for i := range 60 {
		items = append(items, contract.ListItem{Namespace: "octo", Repo: "demo", Number: int64(i + 1), Title: "T",
			Author: contract.User{ID: 1, Login: "alice"}})
	}
	ls := contract.ListSpec{TokenUser: contract.User{ID: 900, Login: "review-bot"},
		Items: map[provider.ListRole][]contract.ListItem{provider.ListAuthor: items}, Env: contract.Env{Token: "tok-gitea-list"}} //nolint:gosec // synthetic test value
	p, _, h := serveCtLists(t, ls)
	h.clamp, h.noDraft = 50, true
	l, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListAuthor, Limit: 100})
	if err != nil || len(l.Items) != 50 || !l.HasMore {
		t.Fatalf("ListMyPullRequests = %d items, %v, %v; want 50 and HasMore", len(l.Items), l.HasMore, err)
	}
	if l.Items[0].Draft != nil {
		t.Error("Draft is set although the host does not report it")
	}
	if !strings.HasPrefix(l.Items[0].Ref.URL, ctListWeb+"/octo/demo/pulls/") {
		t.Errorf("URL %q, want the web base", l.Items[0].Ref.URL)
	}
}
