package bitbucketserver_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

const ctDashboard = "/rest/api/1.0/dashboard/pull-requests"

// ServeLists implements contract.ListFixture: a fake dashboard
// (E1: GET /rest/api/1.0/dashboard/pull-requests, Bitbucket Server 4.10+).
//
// Assumptions the fixture records (WP-19a DQ-2, DQ-3): order=NEWEST is
// the order by last update (the fixture serves the items in the spec's
// order, which is not sorted, so the provider must sort); every response
// carries X-AUSERNAME and X-AUSERID; draft is in the payload (8.17+).
func (ctFixture) ServeLists(t *testing.T, ls contract.ListSpec) (provider.Provider, *provider.Resolver) {
	t.Helper()
	h := &ctDash{t: t, ls: ls}
	cfg := config.Defaults()
	cfg.BitbucketServer.BaseURL = contract.StartListServer(t, ls, h) + ctPrefix
	cfg.Secrets.BitbucketServerToken = config.NewSecret(ls.Env.Token)
	f := bitbucketserver.NewFactory()
	p, err := f.New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, provider.NewResolver(cfg, nil, f)
}

type ctDash struct {
	t  *testing.T
	ls contract.ListSpec
	// requests counts the dashboard requests.
	requests int
}

var ctDashRoles = map[string]provider.ListRole{"REVIEWER": provider.ListReviewer, "AUTHOR": provider.ListAuthor}

var ctHostStatus = map[string]string{provider.MyStatusApproved: "APPROVED",
	provider.MyStatusNeedsWork: "NEEDS_WORK", provider.MyStatusUnapproved: "UNAPPROVED"}

func (d *ctDash) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, ok := strings.CutPrefix(r.URL.EscapedPath(), ctPrefix)
	if r.Header.Get("Authorization") != "Bearer "+d.ls.Env.Token || !ok || r.Method != http.MethodGet || path != ctDashboard {
		d.t.Errorf("request %s %s: not a GET of the dashboard with the token", r.Method, r.URL.EscapedPath())
		contract.WriteError(w, http.StatusNotFound)
		return
	}
	d.requests++
	q := r.URL.Query()
	role, known := ctDashRoles[q.Get("role")]
	// Canary (a): the request names the role and the open state.
	if !known {
		d.t.Errorf("the dashboard request has role=%q, want REVIEWER or AUTHOR", q.Get("role"))
	}
	if q.Get("state") != "OPEN" {
		d.t.Errorf("the dashboard request has state=%q, want OPEN", q.Get("state"))
	}
	if q.Get("order") != "NEWEST" || q.Get("start") != "0" {
		d.t.Errorf("the dashboard request has order=%q start=%q, want NEWEST and 0", q.Get("order"), q.Get("start"))
	}
	if q.Has("user") || q.Has("participantStatus") {
		d.t.Error("the dashboard request names a user or a participant status")
	}
	for k := range q {
		if k != "state" && k != "role" && k != "order" && k != "start" && k != "limit" {
			d.t.Errorf("the dashboard request has the undocumented parameter %q", k)
		}
	}
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit < 1 || limit > provider.MaxListLimit {
		d.t.Errorf("the dashboard request has limit=%q", q.Get("limit"))
	}
	items, more := d.ls.ListPage(role, limit)
	values := make([]any, 0, len(items))
	for _, it := range items {
		values = append(values, ctDashPR(r, it, d.ls.TokenUser))
	}
	w.Header().Set("X-AUSERNAME", d.ls.TokenUser.Login)
	w.Header().Set("X-AUSERID", strconv.FormatInt(d.ls.TokenUser.ID, 10))
	ctJSON(w, http.StatusOK, map[string]any{"start": 0, "size": len(values), "limit": limit,
		"isLastPage": !more, "values": values, "nextPageStart": len(values)})
}

// ctDashPR is a dashboard pull request: the target repository's project
// key and slug, the author's user name, and the reviewers: another
// reviewer first (with a status unlike the token user's), then the token
// user with MyStatus.
func ctDashPR(r *http.Request, it contract.ListItem, me contract.User) map[string]any {
	host := r.Host
	if it.ForeignURL {
		host = contract.ForeignHost
	}
	self := "http://" + host + ctPrefix + "/projects/" + url.PathEscape(it.Namespace) + "/repos/" +
		url.PathEscape(it.Repo) + "/pull-requests/" + strconv.FormatInt(it.Number, 10)
	other := "UNAPPROVED"
	if it.MyStatus == provider.MyStatusUnapproved {
		other = "APPROVED"
	}
	reviewers := []any{map[string]any{"user": map[string]any{"id": 4242, "name": "carol"}, "status": other}}
	if it.MyStatus != "" {
		reviewers = append(reviewers, map[string]any{"user": map[string]any{"id": me.ID, "name": me.Login},
			"status": ctHostStatus[it.MyStatus]})
	}
	return map[string]any{
		"id": it.Number, "title": it.Title, "draft": it.Draft, "state": "OPEN",
		"updatedDate": it.Updated.UnixMilli(), "createdDate": it.Updated.Add(-48 * 3600e9).UnixMilli(),
		"toRef": map[string]any{"id": "refs/heads/" + it.TargetBranch, "displayId": it.TargetBranch,
			"repository": map[string]any{"slug": it.Repo, "project": map[string]any{"key": it.Namespace}}},
		"author":    map[string]any{"user": map[string]any{"id": it.Author.ID, "name": it.Author.Login, "displayName": it.Author.DisplayName}},
		"reviewers": reviewers,
		"links":     map[string]any{"self": []any{map[string]any{"href": self}}},
	}
}

// TestListPersonalProject: a pull request of a personal repository
// (project key "~JDOE") gets the /users/{user}/... URL, which resolves to
// the same reference.
func TestListPersonalProject(t *testing.T) {
	ls := contract.ListSpec{TokenUser: contract.User{ID: 900, Login: "review-bot"},
		Items: map[provider.ListRole][]contract.ListItem{provider.ListAuthor: {
			{Namespace: "~JDOE", Repo: "notes", Number: 3, Title: "Mine", Author: contract.User{ID: 900, Login: "review-bot"}, TargetBranch: "main"},
		}}, Env: contract.Env{Token: "tok-bbs-list"}} //nolint:gosec // synthetic test value
	p, res := ctFixture{}.ServeLists(t, ls)
	l, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListAuthor, Limit: 30})
	if err != nil || len(l.Items) != 1 {
		t.Fatalf("ListMyPullRequests = %+v, %v", l, err)
	}
	u := l.Items[0].Ref.URL
	if !strings.HasSuffix(u, ctPrefix+"/users/JDOE/repos/notes/pull-requests/3") {
		t.Errorf("URL %q, want the /users/ form", u)
	}
	if ref, _, err := res.Resolve(u); err != nil || ref.Namespace != "~JDOE" || ref.Repo != "notes" || ref.Number != 3 {
		t.Errorf("Resolve(%q) = %+v, %v", u, ref, err)
	}
	if l.Items[0].MyStatus != "" {
		t.Errorf("MyStatus %q on an author list", l.Items[0].MyStatus)
	}
}

// TestListWithoutIdentityHeaders: without X-AUSERID and X-AUSERNAME the
// token user's entry cannot be told, so MyStatus stays "" and no other
// reviewer's status is shown.
func TestListWithoutIdentityHeaders(t *testing.T) {
	ls := contract.ListSpec{TokenUser: contract.User{},
		Items: map[provider.ListRole][]contract.ListItem{provider.ListReviewer: {
			{Namespace: "PROJ", Repo: "demo", Number: 7, Title: "T", Author: contract.User{ID: 1, Login: "alice"},
				TargetBranch: "main", MyStatus: provider.MyStatusApproved},
		}}, Env: contract.Env{Token: "tok-bbs-list"}} //nolint:gosec // synthetic test value
	p, _ := ctFixture{}.ServeLists(t, ls)
	l, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListReviewer, Limit: 30})
	if err != nil || len(l.Items) != 1 {
		t.Fatalf("ListMyPullRequests = %+v, %v", l, err)
	}
	if l.Items[0].MyStatus != "" {
		t.Errorf("MyStatus %q without the identity headers, want none", l.Items[0].MyStatus)
	}
}
