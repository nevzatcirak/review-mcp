package contract

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// ListItem is one open pull request of a list a fixture serves (X-48).
type ListItem struct {
	Namespace, Repo string
	Number          int64
	Title           string
	Author          User
	TargetBranch    string
	Draft           bool
	Updated         time.Time
	// MyStatus is the token user's reviewer status on a host whose list
	// carries it (Traits.ListsMyStatus); a fixture serves it in the host's
	// own words next to another reviewer's different status.
	MyStatus string
	// ForeignURL makes the fixture serve the item's web URL (html_url,
	// web_url, links.self) on ForeignHost: the provider must still build
	// the URL from its configured base.
	ForeignURL bool
}

// ForeignHost is the host of a ListItem's response URL with ForeignURL.
const ForeignHost = "elsewhere.example.com"

// ListSpec is what a list fixture serves: per role, the pull requests the
// host lists for the token's user, in the host's own order.
type ListSpec struct {
	TokenUser User
	Items     map[provider.ListRole][]ListItem
	Env       Env
}

// ListFixture is implemented by the Fixture of a provider that implements
// provider.PRLister. ServeLists serves ls through the host's list
// endpoints (through StartListServer) and returns the provider and a
// resolver built from the same configuration and the provider's factory.
// A fixture also checks, for every list request, that it carries exactly
// the parameters its host documents for the role (failing t otherwise).
type ListFixture interface {
	ServeLists(t *testing.T, ls ListSpec) (provider.Provider, *provider.Resolver)
}

// StartListServer is StartServer for a ListSpec.
func StartListServer(t *testing.T, ls ListSpec, h http.Handler) string {
	t.Helper()
	return StartServer(t, Spec{Env: ls.Env}, h)
}

// Pages a fixture serves: at most limit items of the role, and the host's
// "more" signal when there are more. ListPage returns them.
func (ls ListSpec) ListPage(role provider.ListRole, limit int) (items []ListItem, more bool) {
	all := ls.Items[role]
	if limit > 0 && len(all) > limit {
		return all[:limit], true
	}
	return all, false
}

// listTitle is a title with a newline, a control character and 300 runes.
var listTitle = "Rename\nthe\x07 helper " + strings.Repeat("é", 300)

// listTitleWant is listTitle as CleanListText(_, 200) gives it.
var listTitleWant = "Rename the helper " + strings.Repeat("é", provider.MaxListTitleRunes-len([]rune("Rename the helper ")))

// listSpec returns the ListSpec of the lists case for role: two valid
// items (the newer one served second, so that the provider must sort, the
// older one a draft with listTitle and a response URL on ForeignHost), and
// one whose namespace does not validate.
func listSpec(role provider.ListRole) ListSpec {
	items := []ListItem{
		{Namespace: "octo", Repo: "demo", Number: 7, Title: listTitle, Author: alice, TargetBranch: "main",
			Draft: true, Updated: at(1), MyStatus: provider.MyStatusNeedsWork, ForeignURL: true},
		{Namespace: "..", Repo: "demo", Number: 8, Title: "Dropped", Author: bob, TargetBranch: "main", Updated: at(3)},
		{Namespace: "team", Repo: "service", Number: 42, Title: "Add the\tretry", Author: User{ID: 110, Login: "bob\u202e-x", DisplayName: "Bob"},
			TargetBranch: "release/1.2", Updated: at(5), MyStatus: provider.MyStatusApproved},
	}
	return ListSpec{TokenUser: tokenUser, Items: map[provider.ListRole][]ListItem{role: items},
		Env: Env{Token: testToken}}
}

var allListRoles = []provider.ListRole{provider.ListReviewer, provider.ListAssignee, provider.ListAuthor}

// lists: a provider reports Capabilities().Lists allowing a role exactly
// when it implements provider.PRLister. For each role it allows, the list
// holds the valid items newest first, with cleaned and cut titles and
// names, the draft flag, the target branch where the host carries it, the
// token user's own status where the host carries it, and a URL built from
// the configured base that the resolver maps back to the same PRRef, even
// when the response's URL points elsewhere; an item whose repository path
// does not validate is dropped and counted; the limit is the page size and
// the host's "more" signal sets HasMore. Every request is a GET. A role the
// provider does not allow, and a limit out of range, send no request.
func (s *suite) lists(t *testing.T) {
	spec := samplePR()
	p, _, _ := s.serve(t, spec)
	caps := p.Capabilities().Lists
	_, isLister := p.(provider.PRLister)
	if caps.Any() != isLister {
		t.Fatalf("Capabilities().Lists %+v allows a role: %v, but the provider implements provider.PRLister: %v",
			caps, caps.Any(), isLister)
	}
	if !isLister {
		return
	}
	lf, ok := s.f.(ListFixture)
	if !ok {
		t.Fatal("the provider implements provider.PRLister, but the fixture does not implement contract.ListFixture")
	}
	serve := func(t *testing.T, ls ListSpec) (provider.PRLister, *provider.Resolver, *requestLog) {
		t.Helper()
		log := &requestLog{}
		ls.Env.log = log
		lp, res := lf.ServeLists(t, ls)
		if lp == nil || res == nil {
			t.Fatal("ServeLists returned a nil provider or resolver")
		}
		if !log.isStarted() {
			t.Fatal("the fixture did not serve through contract.StartListServer")
		}
		return lp.(provider.PRLister), res, log
	}
	for _, role := range allListRoles {
		t.Run(string(role), func(t *testing.T) {
			if !caps.Allows(role) {
				lp, _, log := serve(t, listSpec(role))
				l, err := lp.ListMyPullRequests(t.Context(), provider.ListQuery{Role: role, Limit: 30})
				if !errors.Is(err, provider.ErrUnsupported) || l != nil {
					t.Errorf("ListMyPullRequests(%s) = %+v, %v; want nil and an unsupported error", role, l, err)
				}
				checkClean(t, string(role), err)
				if n, _ := log.count(); n != 0 {
					t.Errorf("%d requests were sent for an unsupported role, want none", n)
				}
				return
			}
			s.listRole(t, role, serve)
		})
	}
	t.Run("limit_out_of_range", func(t *testing.T) {
		role := allListRoles[0]
		for _, r := range allListRoles {
			if caps.Allows(r) {
				role = r
				break
			}
		}
		lp, _, log := serve(t, listSpec(role))
		for _, n := range []int{0, -1, provider.MaxListLimit + 1} {
			if l, err := lp.ListMyPullRequests(t.Context(), provider.ListQuery{Role: role, Limit: n}); !errors.Is(err, provider.ErrProtocol) || l != nil {
				t.Errorf("limit %d: %+v, %v; want nil and a protocol error", n, l, err)
			}
		}
		if n, _ := log.count(); n != 0 {
			t.Errorf("%d requests were sent for an invalid limit, want none", n)
		}
	})
}

func (s *suite) listRole(t *testing.T, role provider.ListRole,
	serve func(*testing.T, ListSpec) (provider.PRLister, *provider.Resolver, *requestLog)) {
	ls := listSpec(role)
	lp, res, log := serve(t, ls)
	l, err := lp.ListMyPullRequests(t.Context(), provider.ListQuery{Role: role, Limit: 30})
	// GET only, checked first: a host may refuse any other method.
	if all, writes := log.count(); writes != 0 || all == 0 {
		t.Errorf("%d requests, %d of them not GET; want only GET requests", all, writes)
	}
	if err != nil {
		t.Fatalf("ListMyPullRequests(%s): %v", role, err)
	}
	src := ls.Items[role]
	want := []ListItem{src[2], src[0]} // newest first; src[1] is dropped
	if len(l.Items) != len(want) {
		t.Fatalf("%d items, want %d: %+v", len(l.Items), len(want), l.Items)
	}
	if l.HasMore {
		t.Error("HasMore is set for a list that fits the limit")
	}
	if l.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1 (the item whose namespace does not validate)", l.Dropped)
	}
	wantTitles := []string{"Add the retry", listTitleWant}
	wantAuthors := []string{"bob-x", alice.Login}
	for i, w := range want {
		got := l.Items[i]
		if got.Ref.Kind != s.f.Kind() || got.Ref.Namespace != w.Namespace || got.Ref.Repo != w.Repo || got.Ref.Number != w.Number {
			t.Errorf("item %d: Ref %+v, want %s/%s #%d", i, got.Ref, w.Namespace, w.Repo, w.Number)
		}
		if strings.Contains(got.Ref.URL, ForeignHost) {
			t.Errorf("item %d: URL %q was copied from the response", i, got.Ref.URL)
		}
		rref, _, rerr := res.Resolve(got.Ref.URL)
		if rerr != nil || rref.Kind != got.Ref.Kind || rref.Namespace != w.Namespace || rref.Repo != w.Repo || rref.Number != w.Number {
			t.Errorf("item %d: Resolve(%q) = %+v, %v; want the same pull request", i, got.Ref.URL, rref, rerr)
		}
		if got.Title != wantTitles[i] {
			t.Errorf("item %d: Title %q, want %q", i, got.Title, wantTitles[i])
		}
		if got.Author != wantAuthors[i] {
			t.Errorf("item %d: Author %q, want %q", i, got.Author, wantAuthors[i])
		}
		if !got.UpdatedAt.Equal(w.Updated) {
			t.Errorf("item %d: UpdatedAt %v, want %v", i, got.UpdatedAt, w.Updated)
		}
		if got.Draft == nil || *got.Draft != w.Draft {
			t.Errorf("item %d: Draft %v, want %v", i, got.Draft, w.Draft)
		}
		switch {
		case s.tr.ListsNoTargetBranch && got.TargetBranch != nil:
			t.Errorf("item %d: TargetBranch %q, want nil: the host's list does not carry it", i, *got.TargetBranch)
		case !s.tr.ListsNoTargetBranch && (got.TargetBranch == nil || *got.TargetBranch != w.TargetBranch):
			t.Errorf("item %d: TargetBranch %v, want %q", i, got.TargetBranch, w.TargetBranch)
		}
		wantStatus := ""
		if s.tr.ListsMyStatus && role == provider.ListReviewer {
			wantStatus = w.MyStatus
		}
		if got.MyStatus != wantStatus {
			t.Errorf("item %d: MyStatus %q, want %q", i, got.MyStatus, wantStatus)
		}
	}

	t.Run("limit_and_more", func(t *testing.T) {
		lp, _, _ := serve(t, ls)
		l, err := lp.ListMyPullRequests(t.Context(), provider.ListQuery{Role: role, Limit: 1})
		if err != nil {
			t.Fatalf("ListMyPullRequests: %v", err)
		}
		if !l.HasMore {
			t.Error("HasMore is not set although the host has more")
		}
		if len(l.Items) > 1 {
			t.Errorf("%d items for limit 1", len(l.Items))
		}
	})
	t.Run("empty", func(t *testing.T) {
		empty := ls
		empty.Items = map[provider.ListRole][]ListItem{}
		lp, _, _ := serve(t, empty)
		l, err := lp.ListMyPullRequests(t.Context(), provider.ListQuery{Role: role, Limit: 30})
		if err != nil || l == nil || l.Items == nil || len(l.Items) != 0 || l.HasMore {
			t.Errorf("ListMyPullRequests on an empty host = %+v, %v; want an empty list", l, err)
		}
	})
	t.Run("failure", func(t *testing.T) {
		for _, c := range []struct {
			status int
			want   *provider.Error
		}{{401, provider.ErrAuth}, {500, provider.ErrUpstream}} {
			f := ls
			f.Env.Failure = Failure{Status: c.status}
			lp, _, _ := serve(t, f)
			l, err := lp.ListMyPullRequests(t.Context(), provider.ListQuery{Role: role, Limit: 30})
			if !errors.Is(err, c.want) || l != nil {
				t.Errorf("HTTP %d: %+v, %v; want nil and %s", c.status, l, err, c.want.Class)
			}
			checkClean(t, "list", err)
		}
	})
}
