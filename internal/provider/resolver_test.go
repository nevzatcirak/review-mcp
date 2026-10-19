package provider

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

type fakeFactory struct {
	kind     Kind
	gotRem   []string
	gotRepo  []string
	gotQuery []url.Values
	newErr   error
}

func (f *fakeFactory) Kind() Kind { return f.kind }

// ParsePRPath accepts /{ns}/{repo}/pulls/{n}[/more...] and unescapes segments.
func (f *fakeFactory) ParsePRPath(rem string) (string, string, int64, error) {
	f.gotRem = append(f.gotRem, rem)
	parts := strings.Split(strings.TrimPrefix(rem, "/"), "/")
	if len(parts) < 4 || parts[2] != "pulls" {
		return "", "", 0, errors.New("bad path")
	}
	n, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return "", "", 0, err
	}
	ns, _ := urlUnescape(parts[0])
	repo, _ := urlUnescape(parts[1])
	return ns, repo, n, nil
}

// ParseRepoPath accepts /{ns}/{repo} and /{ns}/{repo}/tree/{branch...}.
func (f *fakeFactory) ParseRepoPath(rem string, q url.Values) (string, string, string, error) {
	f.gotRepo = append(f.gotRepo, rem)
	f.gotQuery = append(f.gotQuery, q)
	parts := strings.Split(strings.TrimPrefix(rem, "/"), "/")
	switch {
	case len(parts) == 2:
		return parts[0], parts[1], "", nil
	case len(parts) > 3 && parts[2] == "tree":
		return parts[0], parts[1], strings.Join(parts[3:], "/"), nil
	}
	return "", "", "", errors.New("bad path")
}

func (f *fakeFactory) New(*config.Config, *slog.Logger) (Provider, error) {
	if f.newErr != nil {
		return nil, f.newErr
	}
	return fakeProvider{f.kind}, nil
}

type fakeProvider struct{ kind Kind }

func (p fakeProvider) Kind() Kind { return p.kind }

func (fakeProvider) Capabilities() Capabilities { return Capabilities{} }
func (fakeProvider) GetPullRequest(_ ctxT, _ PRRef) (*PullRequest, error) {
	return nil, nil
}
func (fakeProvider) GetCommitMessages(_ ctxT, _ PRRef) ([]string, error) { return nil, nil }
func (fakeProvider) GetDiff(_ ctxT, _ PRRef, _ *PullRequest, _ DiffOptions) (*Diff, error) {
	return nil, nil
}
func (fakeProvider) PostComment(_ ctxT, _ PRRef, _ string) (*Comment, error) { return nil, nil }
func (fakeProvider) ListThreads(_ ctxT, _ PRRef) ([]Thread, error)           { return nil, nil }
func (fakeProvider) ReplyToComment(_ ctxT, _ PRRef, _, _ string) (*ReplyResult, error) {
	return nil, nil
}
func (fakeProvider) CurrentUser(ctxT) (User, error)                 { return User{}, nil }
func (fakeProvider) EditComment(_ ctxT, _ PRRef, _, _ string) error { return nil }
func (fakeProvider) PostInlineComments(_ ctxT, _ PRRef, _ *PullRequest, _ []InlineComment) ([]InlineResult, error) {
	return nil, nil
}
func (fakeProvider) GetReviewStatus(_ ctxT, _ PRRef, _ *PullRequest, _ ReviewStatusOptions) *ReviewStatus {
	return nil
}
func (fakeProvider) UpdatePullRequest(_ ctxT, _ PRRef, _ UpdatePR) error  { return nil }
func (fakeProvider) GetChecks(_ ctxT, _ PRRef, _ string) (*Checks, error) { return nil, nil }
func (fakeProvider) GetFileAt(_ ctxT, _ PRRef, _, _ string) ([]byte, error) {
	return nil, nil
}
func (fakeProvider) ListDir(_ ctxT, _ PRRef, _, _ string) (*DirListing, error) {
	return nil, nil
}
func (fakeProvider) FileLineURL(PRRef, *PullRequest, string, int) string { return "" }

func cfgFor(gitea, web, bbs string) *config.Config {
	c := config.Defaults()
	c.Gitea.BaseURL, c.Gitea.WebURL, c.BitbucketServer.BaseURL = gitea, web, bbs
	return c
}

func newRes(c *config.Config) (*Resolver, *fakeFactory, *fakeFactory) {
	g := &fakeFactory{kind: KindGitea}
	b := &fakeFactory{kind: KindBitbucketServer}
	return NewResolver(c, nil, g, b), g, b
}

func TestResolveHappyPaths(t *testing.T) {
	r, _, _ := newRes(cfgFor("https://your-gitea.example", "https://web.your-gitea.example/gitea", "https://bitbucket.example.com/bb"))
	cases := []struct {
		url  string
		kind Kind
		ns   string
		repo string
		n    int64
	}{
		{"https://your-gitea.example/o/r/pulls/7", KindGitea, "o", "r", 7},
		{"HTTPS://YOUR-GITEA.EXAMPLE:443/o/r/pulls/7/files?x=1#frag", KindGitea, "o", "r", 7},
		{"https://web.your-gitea.example/gitea/o/r/pulls/8", KindGitea, "o", "r", 8},
		{"https://bitbucket.example.com/bb/P/R/pulls/9", KindBitbucketServer, "P", "R", 9},
	}
	for _, tc := range cases {
		ref, p, err := r.Resolve(tc.url)
		if err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if ref.Kind != tc.kind || ref.Namespace != tc.ns || ref.Repo != tc.repo || ref.Number != tc.n || ref.URL != tc.url {
			t.Errorf("%s: got %+v", tc.url, ref)
		}
		if p == nil || p.Kind() != tc.kind {
			t.Errorf("%s: wrong provider", tc.url)
		}
	}
}

func TestResolveLongestPrefixWins(t *testing.T) {
	// Both providers on one host; the longer base path must win.
	r, g, b := newRes(cfgFor("https://h.example.com", "", "https://h.example.com/bb"))
	ref, _, err := r.Resolve("https://h.example.com/bb/P/R/pulls/1")
	if err != nil || ref.Kind != KindBitbucketServer {
		t.Fatalf("got %+v %v", ref, err)
	}
	if len(g.gotRem) != 0 || len(b.gotRem) != 1 || b.gotRem[0] != "/P/R/pulls/1" {
		t.Fatalf("remainders: gitea=%v bbs=%v", g.gotRem, b.gotRem)
	}
	ref, _, err = r.Resolve("https://h.example.com/o/r/pulls/1")
	if err != nil || ref.Kind != KindGitea {
		t.Fatalf("got %+v %v", ref, err)
	}
}

// [canary] Foreign, look-alike, wrong-port, wrong-scheme and sibling-prefix
// URLs are rejected and a fake server registers zero requests.
func TestResolveRejectsForeignURLsWithoutNetwork(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	r, g, b := newRes(cfgFor("https://your-gitea.example", "", "https://bitbucket.example.com/bb"))
	bad := []string{
		"https://evil.example.org/o/r/pulls/1",
		"https://your-gitea.example.evil.com/o/r/pulls/1",
		"https://evil.your-gitea.example/o/r/pulls/1",
		"https://your-gitea.example:8443/o/r/pulls/1",
		"http://your-gitea.example/o/r/pulls/1",
		"http://your-gitea.example:443/o/r/pulls/1", // scheme differs, port equal
		"https://bitbucket.example.com/bbx/P/R/pulls/1",
		"https://bitbucket.example.com/other/P/R/pulls/1",
		"ftp://your-gitea.example/o/r/pulls/1",
		"your-gitea.example/o/r/pulls/1",
		srv.URL + "/o/r/pulls/1", // a real listening server that is not configured
		"",
	}
	for _, u := range bad {
		_, p, err := r.Resolve(u)
		if !errors.Is(err, ErrURLNotConfigured) || p != nil {
			t.Errorf("%q: got err=%v provider=%v, want url_not_configured", u, err, p)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("fake server saw %d requests", hits.Load())
	}
	if len(g.gotRem)+len(b.gotRem) != 0 {
		t.Fatal("ParsePRPath ran for a rejected URL")
	}
}

func TestResolveHintListsRedactedBases(t *testing.T) {
	r, _, _ := newRes(cfgFor("https://your-gitea.example", "", "https://bitbucket.example.com/bb?token=SECRETQ"))
	_, _, err := r.Resolve("https://evil.example.org/a/b/pulls/1")
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatal(err)
	}
	if !strings.Contains(pe.Hint, "https://your-gitea.example") || !strings.Contains(pe.Hint, "https://bitbucket.example.com/bb") {
		t.Errorf("hint %q does not list the base URLs", pe.Hint)
	}
	if strings.Contains(err.Error(), "SECRETQ") {
		t.Errorf("hint leaks query value: %q", err)
	}
}

func TestResolveMalformed(t *testing.T) {
	r, _, _ := newRes(cfgFor("https://your-gitea.example", "", ""))
	for _, u := range []string{
		"https://your-gitea.example/o/r/issues/1",
		"https://your-gitea.example/o/r/pulls/abc",
		"https://your-gitea.example/",
		"https://your-gitea.example",
		"https://your-gitea.example/o/r/pulls/1/../../x",
		"https://your-gitea.example/o/r/pulls/0",
		"https://your-gitea.example/o/%zz/pulls/1",
	} {
		_, _, err := r.Resolve(u)
		if !errors.Is(err, ErrURLMalformed) {
			t.Errorf("%q: got %v, want url_malformed", u, err)
		}
	}
}

func TestResolveUserinfoRejectedNotEchoed(t *testing.T) {
	r, _, _ := newRes(cfgFor("https://your-gitea.example", "", ""))
	_, _, err := r.Resolve("https://alice:hunter2-PW@your-gitea.example/o/r/pulls/1")
	if !errors.Is(err, ErrURLMalformed) {
		t.Fatalf("got %v", err)
	}
	for _, bad := range []string{"hunter2-PW", "alice"} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("error echoes userinfo: %q", err)
		}
	}
}

// An encoded slash inside a segment must stay one segment: the factory gets
// the ESCAPED remainder, so %2F cannot fabricate extra segments.
func TestResolveEncodedSlashStaysOneSegment(t *testing.T) {
	r, g, _ := newRes(cfgFor("https://your-gitea.example", "", ""))
	ref, _, err := r.Resolve("https://your-gitea.example/own%2Fer/r/pulls/3")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Namespace != "own/er" || ref.Repo != "r" || ref.Number != 3 {
		t.Fatalf("got %+v", ref)
	}
	if g.gotRem[0] != "/own%2Fer/r/pulls/3" {
		t.Fatalf("remainder %q is not the escaped form", g.gotRem[0])
	}
	// %2F in the base-path position must not match a two-segment base.
	r2, _, _ := newRes(cfgFor("https://h.example.com/a/b", "", ""))
	if _, _, err := r2.Resolve("https://h.example.com/a%2Fb/o/r/pulls/1"); !errors.Is(err, ErrURLNotConfigured) {
		t.Fatalf("got %v, want url_not_configured", err)
	}
}

func TestResolveDisabledAndMissingFactory(t *testing.T) {
	// Gitea disabled: its factory is ignored and its host is not a candidate.
	r := NewResolver(cfgFor("", "", "https://bitbucket.example.com"), nil, &fakeFactory{kind: KindGitea}, &fakeFactory{kind: KindBitbucketServer})
	if _, _, err := r.Resolve("https://your-gitea.example/o/r/pulls/1"); !errors.Is(err, ErrURLNotConfigured) {
		t.Fatalf("got %v", err)
	}
	// Enabled but unregistered: url_not_configured, no panic.
	r = NewResolver(cfgFor("https://your-gitea.example", "", ""), nil)
	_, _, err := r.Resolve("https://your-gitea.example/o/r/pulls/1")
	if !errors.Is(err, ErrURLNotConfigured) || !strings.Contains(err.Error(), "gitea") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveFactoryErrorPropagates(t *testing.T) {
	want := &Error{Class: ClassUnsupportedVersion}
	r := NewResolver(cfgFor("https://your-gitea.example", "", ""), nil, &fakeFactory{kind: KindGitea, newErr: want})
	if _, _, err := r.Resolve("https://your-gitea.example/o/r/pulls/1"); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("got %v", err)
	}
}

// closingProvider counts CloseIdleConnections calls.
type closingProvider struct {
	fakeProvider
	closes *atomic.Int64
}

func (p closingProvider) CloseIdleConnections() { p.closes.Add(1) }

type closingFactory struct {
	*fakeFactory
	closes *atomic.Int64
}

func (f closingFactory) New(*config.Config, *slog.Logger) (Provider, error) {
	return closingProvider{fakeProvider{f.kind}, f.closes}, nil
}

func TestResolverCloseIdleConnectionsClosesBuiltProviders(t *testing.T) {
	var nilResolver *Resolver
	nilResolver.CloseIdleConnections()

	closes := &atomic.Int64{}
	r := NewResolver(cfgFor("https://your-gitea.example", "", "https://bitbucket.example.com"), nil,
		closingFactory{&fakeFactory{kind: KindGitea}, closes}, &fakeFactory{kind: KindBitbucketServer})
	r.CloseIdleConnections() // unused: nothing to close
	for range 2 {
		if _, _, err := r.Resolve("https://your-gitea.example/octo/demo/pulls/7"); err != nil {
			t.Fatal(err)
		}
	}
	// A provider without CloseIdleConnections is skipped.
	if _, _, err := r.Resolve("https://bitbucket.example.com/octo/demo/pulls/7"); err != nil {
		t.Fatal(err)
	}
	r.CloseIdleConnections()
	if got := closes.Load(); got != 2 {
		t.Fatalf("CloseIdleConnections reached %d providers, want 2", got)
	}
	r.CloseIdleConnections() // a second call closes nothing twice
	if got := closes.Load(); got != 2 {
		t.Fatalf("second CloseIdleConnections: %d closes, want still 2", got)
	}
}

// TestResolveItem: a pull request URL is a pull request item (ParsePRPath
// first, the repository parser never asked); any other URL under a
// configured base is tried as a repository URL, with the URL's query; an
// URL that is neither is url_malformed, and a URL of no configured
// provider is url_not_configured before any parser runs. Nothing is sent
// anywhere.
func TestResolveItem(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	r, g, b := newRes(cfgFor("https://your-gitea.example", "", "https://bitbucket.example.com/bb"))

	kind, ref, repo, p, err := r.ResolveItem("https://your-gitea.example/o/r/pulls/7")
	if err != nil || kind != ItemPullRequest || ref.Number != 7 || ref.Namespace != "o" || repo != (RepoRef{}) || p == nil {
		t.Fatalf("pull request: %v %+v %+v %v", kind, ref, repo, err)
	}
	if len(g.gotRepo) != 0 {
		t.Errorf("the repository parser ran for a pull request URL: %q", g.gotRepo)
	}

	const branchURL = "https://bitbucket.example.com/bb/P/R/tree/feature/x?at=refs%2Fheads%2Fy#frag"
	kind, ref, repo, p, err = r.ResolveItem(branchURL)
	if err != nil || kind != ItemRepository || ref != (PRRef{}) || p == nil || p.Kind() != KindBitbucketServer {
		t.Fatalf("repository: %v %+v %v", kind, ref, err)
	}
	if want := (RepoRef{Kind: KindBitbucketServer, Namespace: "P", Repo: "R", Branch: "feature/x", URL: branchURL}); repo != want {
		t.Errorf("repository %+v, want %+v", repo, want)
	}
	if len(b.gotQuery) != 1 || b.gotQuery[0].Get("at") != "refs/heads/y" {
		t.Errorf("the query reached the parser as %v", b.gotQuery)
	}
	kind, _, repo, _, err = r.ResolveItem("https://your-gitea.example/o/r")
	if err != nil || kind != ItemRepository || repo.Branch != "" || repo.Repo != "r" {
		t.Errorf("default branch: %v %+v %v", kind, repo, err)
	}

	for _, u := range []string{
		"https://your-gitea.example/o/r/issues/1",
		"https://your-gitea.example/o",
		"https://your-gitea.example/o/r/tree/a..b",   // the factory's branch is checked again
		"https://your-gitea.example/o/r/tree/../x",   // a dot segment
		"https://your-gitea.example/o/%2E%2E/tree/x", // an escaped dot segment
	} {
		if kind, _, _, p, err := r.ResolveItem(u); !errors.Is(err, ErrURLMalformed) || p != nil || kind != "" {
			t.Errorf("%q: %v %v, want url_malformed", u, kind, err)
		}
	}
	before := len(g.gotRepo) + len(b.gotRepo)
	for _, u := range []string{srv.URL + "/o/r", "https://evil.example.org/o/r", "http://your-gitea.example/o/r",
		"https://bitbucket.example.com/other/P/R"} {
		if _, _, _, p, err := r.ResolveItem(u); !errors.Is(err, ErrURLNotConfigured) || p != nil {
			t.Errorf("%q: %v, want url_not_configured", u, err)
		}
	}
	if _, _, _, _, err := r.ResolveItem("https://u:SECRET-PW@your-gitea.example/o/r"); !errors.Is(err, ErrURLMalformed) ||
		strings.Contains(err.Error(), "SECRET-PW") {
		t.Errorf("credentials: %v", err)
	}
	if len(g.gotRepo)+len(b.gotRepo) != before {
		t.Error("a parser ran for a URL of no configured provider")
	}
	if hits.Load() != 0 {
		t.Fatalf("the server saw %d requests", hits.Load())
	}
}

// TestResolveItemNeverLogsThePath: the resolution log names the provider
// kind and nothing of the URL's path or branch.
func TestResolveItemNeverLogsThePath(t *testing.T) {
	var logs strings.Builder
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := NewResolver(cfgFor("https://your-gitea.example", "", ""), log, &fakeFactory{kind: KindGitea})
	if _, _, _, _, err := r.ResolveItem("https://your-gitea.example/OWNER-MARK/REPO-MARK/tree/BRANCH-MARK"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "kind=gitea") {
		t.Errorf("the log lacks the kind:\n%s", logs.String())
	}
	for _, leak := range []string{"OWNER-MARK", "REPO-MARK", "BRANCH-MARK"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("the log carries %q", leak)
		}
	}
}

// TestValidBranchName is the branch-name table (spec §1.1 item 2). [canary]
func TestValidBranchName(t *testing.T) {
	long := strings.Repeat("a", MaxBranchBytes)
	for _, ok := range []string{"main", "feature/x", "release/2.5.0", "a_b-c.d", "x/y/z", "v1.0", "a-", "a.b/c_d", long, "HEADS", "a/HEAD"} {
		if !ValidBranchName(ok) {
			t.Errorf("ValidBranchName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{
		"", long + "a", "HEAD",
		"a..b", "..", "a/../b", // no ".."
		"a//b",     // no "//"
		"/a", "a/", // no leading or trailing "/"
		".a", "a.", // no leading or trailing "."
		"a/.b", "a/-b", // no segment starting with "." or "-"
		"-a",                 // nor the first one
		"a.lock", "a.lock/b", // no ".lock" ending, on any segment
		"a b", "a~1", "a^", "a:b", // outside the set
		"a@{1}", "a\\b", "a*", "a?b", // ref expressions and globs
		"ä", "a\x00b", "a\nb",
	} {
		if ValidBranchName(bad) {
			t.Errorf("ValidBranchName(%q) = true, want false", bad)
		}
	}
}
