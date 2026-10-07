package provider

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

type fakeFactory struct {
	kind   Kind
	gotRem []string
	newErr error
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
