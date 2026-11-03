package gitlab

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

// ServeLists implements contract.ListFixture: a fake GET
// /api/v4/merge_requests (E2, GitLab 16.0+). The fixture records the
// assumption that reviewer_id needs scope=all (the scope filter is ANDed
// with the reviewer filter, issuable_finder.rb at v16.0.0-ee), and serves
// X-Next-Page empty on the last page and no X-Total.
func (ctFixture) ServeLists(t *testing.T, ls contract.ListSpec) (provider.Provider, *provider.Resolver) {
	t.Helper()
	h := &ctMRList{t: t, ls: ls}
	cfg := config.Defaults()
	cfg.GitLab.BaseURL = contract.StartListServer(t, ls, h) + ctContext
	cfg.Secrets.GitLabToken = config.NewSecret(ls.Env.Token)
	f := NewFactory()
	p, err := f.New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, provider.NewResolver(cfg, nil, f)
}

type ctMRList struct {
	t  *testing.T
	ls contract.ListSpec
}

func (h *ctMRList) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	if r.Header.Get("PRIVATE-TOKEN") != h.ls.Env.Token {
		h.t.Errorf("request %s %s: unexpected token header", r.Method, path)
		contract.WriteError(w, http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodGet && path == ctAPI+"/user":
		ctJSON(w, http.StatusOK, ctUser(h.ls.TokenUser))
		return
	case r.Method == http.MethodGet && path == ctAPI+"/merge_requests":
	default:
		h.t.Errorf("unexpected request %s %s", r.Method, path)
		contract.WriteError(w, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	for k, want := range map[string]string{"state": "opened", "order_by": "updated_at", "sort": "desc", "page": "1"} {
		if q.Get(k) != want {
			h.t.Errorf("the merge request list has %s=%q, want %q", k, q.Get(k), want)
		}
	}
	var role provider.ListRole
	me := strconv.FormatInt(h.ls.TokenUser.ID, 10)
	// Canary (d): the reviewer query is scope=all with the token user's id.
	switch scope := q.Get("scope"); {
	case scope == "all" && q.Get("reviewer_id") == me:
		role = provider.ListReviewer
	case scope == "assigned_to_me" && !q.Has("reviewer_id"):
		role = provider.ListAssignee
	case scope == "created_by_me" && !q.Has("reviewer_id"):
		role = provider.ListAuthor
	default:
		h.t.Errorf("the merge request list has scope=%q reviewer_id=%q, want scope=all&reviewer_id=%s, assigned_to_me or created_by_me",
			scope, q.Get("reviewer_id"), me)
	}
	for k := range q {
		switch k {
		case "state", "order_by", "sort", "page", "per_page", "scope", "reviewer_id":
		default:
			h.t.Errorf("the merge request list has the parameter %q", k)
		}
	}
	limit, err := strconv.Atoi(q.Get("per_page"))
	if err != nil || limit < 1 || limit > provider.MaxListLimit {
		h.t.Errorf("the merge request list has per_page=%q", q.Get("per_page"))
	}
	items, more := h.ls.ListPage(role, limit)
	out := make([]any, 0, len(items))
	for _, it := range items {
		host := r.Host
		if it.ForeignURL {
			host = contract.ForeignHost
		}
		full := it.Namespace + "/" + it.Repo
		out = append(out, map[string]any{
			"id": 5000 + it.Number, "iid": it.Number, "project_id": 4242, "title": it.Title, "state": "opened",
			"draft": it.Draft, "work_in_progress": it.Draft, "updated_at": it.Updated, "created_at": it.Updated.Add(-48 * 3600e9),
			"target_branch": it.TargetBranch, "source_branch": "feature/x",
			"references": map[string]any{"short": "!" + strconv.FormatInt(it.Number, 10), "full": full + "!" + strconv.FormatInt(it.Number, 10)},
			"author":     ctUser(it.Author),
			"web_url":    "http://" + host + ctContext + "/" + full + "/-/merge_requests/" + strconv.FormatInt(it.Number, 10),
		})
	}
	next := ""
	if more {
		next = "2"
	}
	w.Header().Set("X-Next-Page", next)
	w.Header().Set("X-Page", "1")
	w.Header().Set("X-Per-Page", strconv.Itoa(limit))
	ctJSON(w, http.StatusOK, out)
}

// TestListNestedGroup: a merge request of a project in a nested group is
// listed with the whole namespace, and its URL resolves (acceptance Q9).
func TestListNestedGroup(t *testing.T) {
	ls := contract.ListSpec{TokenUser: contract.User{ID: 900, Login: "review-bot"},
		Items: map[provider.ListRole][]contract.ListItem{provider.ListAuthor: {
			{Namespace: "grp/sub/team", Repo: "svc", Number: 12, Title: "Nested", Author: contract.User{ID: 900, Login: "review-bot"}, TargetBranch: "main"},
			{Namespace: "", Repo: "lonely", Number: 13, Title: "No namespace", Author: contract.User{ID: 900, Login: "review-bot"}, TargetBranch: "main"},
		}}, Env: contract.Env{Token: "tok-gitlab-list"}} //nolint:gosec // synthetic test value
	p, res := ctFixture{}.ServeLists(t, ls)
	l, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListAuthor, Limit: 30})
	if err != nil || len(l.Items) != 1 || l.Dropped != 1 {
		t.Fatalf("ListMyPullRequests = %+v, %v; want one item and one dropped", l, err)
	}
	u := l.Items[0].Ref.URL
	if !strings.HasSuffix(u, ctContext+"/grp/sub/team/svc/-/merge_requests/12") {
		t.Errorf("URL %q", u)
	}
	if ref, _, err := res.Resolve(u); err != nil || ref.Namespace != "grp/sub/team" || ref.Repo != "svc" || ref.Number != 12 {
		t.Errorf("Resolve(%q) = %+v, %v", u, ref, err)
	}
}

// TestListReferenceMismatch: an item whose references.full names another
// number than its iid is dropped.
func TestListReferenceMismatch(t *testing.T) {
	p := &Provider{webURL: "https://gitlab.example.com"}
	for _, full := range []string{"g/p!8", "g/p", "p!7", "g/p!7x", "g/../p!7", "g//p!7"} {
		if _, ok := p.listedRef(full, 7); ok {
			t.Errorf("listedRef(%q, 7) accepted it", full)
		}
	}
	ref, ok := p.listedRef("g/p!7", 7)
	if !ok || ref.URL != "https://gitlab.example.com/g/p/-/merge_requests/7" {
		t.Errorf("listedRef(g/p!7) = %+v, %v", ref, ok)
	}
	if _, err := url.Parse(ref.URL); err != nil {
		t.Error(err)
	}
}
