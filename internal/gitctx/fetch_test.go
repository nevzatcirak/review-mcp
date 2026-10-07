package gitctx

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func basicOf(user, token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
}

func wantReason(t *testing.T, err error, want string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *Error with reason %s", err, want)
	}
	if e.Reason != want {
		t.Fatalf("reason = %s, want %s", e.Reason, want)
	}
	if e.Error() != "repository context: "+want {
		t.Fatalf("error text = %q", e.Error())
	}
}

// schemesOf reduces Authorization headers to their scheme words.
func schemesOf(auths []string) []string {
	var out []string
	for _, a := range auths {
		w, _, _ := strings.Cut(a, " ")
		if len(out) == 0 || out[len(out)-1] != w {
			out = append(out, w)
		}
	}
	return out
}

// TestEnsureGitea fetches refs/pull/7/head from the Gitea layout
// ({base}/{owner}/{repo}.git) with "Authorization: token".
func TestEnsureGitea(t *testing.T) {
	resetSchemes()
	s := newGitServer(t, provider.KindGitea)
	s.set(acceptExactly("token "+testToken), "")
	r, _ := realRunner(t, Options{})

	co, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: strings.ToUpper(s.head)})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if co.HeadSHA != s.head || co.Ref != "refs/review-mcp/pr/7" {
		t.Errorf("checkout = %+v, want head %s", co, s.head)
	}
	gitPath, _ := realGit(t)
	if got := gitOut(t, "", gitPath, "--git-dir="+co.GitDir, "cat-file", "-p", s.head+":main.go"); !strings.Contains(got, "func Helper()") {
		t.Errorf("main.go at head = %q", got)
	}
	if got := schemesOf(s.requests()); len(got) != 1 || got[0] != "token" {
		t.Errorf("auth schemes = %v, want [token]", got)
	}

	// A second use fetches again into the same repository.
	if _, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: s.head}); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
}

// TestEnsureBitbucketBearer fetches refs/pull-requests/7/from from the
// Bitbucket layout ({base}/scm/{project}/{repo}.git, base with a context
// path) with "Authorization: Bearer"; the identity is not needed.
func TestEnsureBitbucketBearer(t *testing.T) {
	resetSchemes()
	s := newGitServer(t, provider.KindBitbucketServer)
	s.set(acceptExactly("Bearer "+testToken), "")
	r, _ := realRunner(t, Options{})
	calls := 0
	repo := bbsRepo(s.base)
	repo.Identity = identityOf(testUser, &calls)

	co, err := r.Ensure(context.Background(), repo, PR{Number: 7, HeadSHA: s.head})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if co.HeadSHA != s.head {
		t.Errorf("head = %s, want %s", co.HeadSHA, s.head)
	}
	if calls != 0 {
		t.Errorf("identity called %d times, want 0", calls)
	}
	if got := schemesOf(s.requests()); len(got) != 1 || got[0] != "Bearer" {
		t.Errorf("auth schemes = %v, want [Bearer]", got)
	}
}

// TestBasicFallbackAndSchemeCache: on HTTP 401 the fetch is retried once
// with HTTP Basic (user from the identity, password the token), sent as a
// header; the scheme that worked is tried first from then on (§3.0).
func TestBasicFallbackAndSchemeCache(t *testing.T) {
	for _, kind := range []provider.Kind{provider.KindBitbucketServer, provider.KindGitea} {
		t.Run(string(kind), func(t *testing.T) {
			resetSchemes()
			s := newGitServer(t, kind)
			s.set(acceptExactly(basicOf(testUser, testToken)), "")
			r, _ := realRunner(t, Options{})
			calls := 0
			repo := giteaRepo(s.base)
			first := "token"
			if kind == provider.KindBitbucketServer {
				repo, first = bbsRepo(s.base), "Bearer"
			}
			repo.Identity = identityOf(testUser, &calls)

			if _, err := r.Ensure(context.Background(), repo, PR{Number: 7, HeadSHA: s.head}); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if got := schemesOf(s.requests()); len(got) != 2 || got[0] != first || got[1] != "Basic" {
				t.Errorf("auth schemes = %v, want [%s Basic]", got, first)
			}
			if calls != 1 {
				t.Errorf("identity called %d times, want 1", calls)
			}

			// The working scheme is cached for the process: Basic first now.
			if _, err := r.Ensure(context.Background(), repo, PR{Number: 7, HeadSHA: s.head}); err != nil {
				t.Fatalf("second Ensure: %v", err)
			}
			if got := schemesOf(s.requests()); len(got) != 1 || got[0] != "Basic" {
				t.Errorf("second run auth schemes = %v, want [Basic]", got)
			}
		})
	}
}

// TestAuthRefused: both schemes refused is auth after exactly two attempts;
// an identity that cannot be read is auth too.
func TestAuthRefused(t *testing.T) {
	resetSchemes()
	s := newGitServer(t, provider.KindBitbucketServer)
	s.set(func(string) bool { return false }, "")
	r, _ := realRunner(t, Options{})
	calls := 0
	repo := bbsRepo(s.base)
	repo.Identity = identityOf(testUser, &calls)
	_, err := r.Ensure(context.Background(), repo, PR{Number: 7, HeadSHA: s.head})
	wantReason(t, err, ReasonAuth)
	if got := schemesOf(s.requests()); len(got) != 2 {
		t.Errorf("auth schemes = %v, want two attempts", got)
	}

	repo.Identity = func(context.Context) (string, error) { return "", errors.New("identity: " + testToken) }
	_, err = r.Ensure(context.Background(), repo, PR{Number: 7, HeadSHA: s.head})
	wantReason(t, err, ReasonAuth)
	if strings.Contains(err.Error(), testToken) {
		t.Error("error text carries the identity error")
	}
}

// TestTokenNeverOnDisk [canary]: after fetches with every header scheme,
// no file anywhere in the cache (including the bare repository's config)
// holds the token, the token in base64, or the Basic credentials.
func TestTokenNeverOnDisk(t *testing.T) {
	resetSchemes()
	g := newGitServer(t, provider.KindGitea)
	g.set(acceptExactly("token "+testToken), "")
	b := newGitServer(t, provider.KindBitbucketServer)
	b.set(acceptExactly(basicOf(testUser, testToken)), "")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	r, _ := realRunner(t, Options{CacheDir: cacheDir})

	if _, err := r.Ensure(context.Background(), giteaRepo(g.base), PR{Number: 7, HeadSHA: g.head}); err != nil {
		t.Fatalf("gitea Ensure: %v", err)
	}
	calls := 0
	repo := bbsRepo(b.base)
	repo.Identity = identityOf(testUser, &calls)
	if _, err := r.Ensure(context.Background(), repo, PR{Number: 7, HeadSHA: b.head}); err != nil {
		t.Fatalf("bitbucket Ensure: %v", err)
	}

	secrets := [][]byte{
		[]byte(testToken),
		[]byte(base64.StdEncoding.EncodeToString([]byte(testToken))),
		[]byte(strings.TrimPrefix(basicOf(testUser, testToken), "Basic ")),
	}
	files, configs := 0, 0
	err := filepath.WalkDir(cacheDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		files++
		if d.Name() == "config" {
			configs++
		}
		data, err := os.ReadFile(p) //nolint:gosec // G304: walking the test's own cache directory
		if err != nil {
			return err
		}
		for _, s := range secrets {
			if bytes.Contains(data, s) {
				t.Errorf("the token is on disk in %s", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if configs != 2 || files < 10 {
		t.Fatalf("walked %d files and %d repository configs; the cache was not populated", files, configs)
	}
}

// TestSHAMismatchSkipsContext [canary]: a fetched head that differs from the
// API's head SHA (a push in between) is sha_mismatch, with no checkout.
func TestSHAMismatchSkipsContext(t *testing.T) {
	resetSchemes()
	s := newGitServer(t, provider.KindGitea)
	r, _ := realRunner(t, Options{})
	other := strings.Repeat("ab", 20)
	co, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: other})
	wantReason(t, err, ReasonSHAMismatch)
	if co != (Checkout{}) {
		t.Errorf("checkout = %+v, want none", co)
	}
}

// TestRedirectNotFollowed: a redirect is not followed (the token would go
// along) and is reported as redirect.
func TestRedirectNotFollowed(t *testing.T) {
	resetSchemes()
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	s := newGitServer(t, provider.KindGitea)
	s.mu.Lock()
	s.redirectTo = other.URL
	s.mu.Unlock()
	s.set(nil, "redirect")
	r, _ := realRunner(t, Options{})
	_, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: s.head})
	wantReason(t, err, ReasonRedirect)
	if elsewhere.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

// TestInsteadOfGuardRealGit [canary]: with the real git, a
// url.<other>.insteadOf=<pinned base> (here an extra configuration entry;
// in the field a Windows system configuration) would send the fetch, and
// its Authorization header, to another server. The ls-remote --get-url
// guard stops it first: redirect, and no request reaches either server.
func TestInsteadOfGuardRealGit(t *testing.T) {
	resetSchemes()
	var elsewhere atomic.Int32
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		if strings.Contains(r.Header.Get("Authorization"), testToken) {
			leaked.Store(true)
		}
	}))
	defer other.Close()
	s := newGitServer(t, provider.KindGitea)
	s.set(acceptExactly("token "+testToken), "")
	setExtraConfig(t, [][2]string{{"url." + other.URL + "/.insteadOf", s.base + "/"}})
	r, _ := realRunner(t, Options{})
	_, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: s.head})
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("%d requests reached the other server (token sent: %v)", n, leaked.Load())
	}
	if got := s.requests(); len(got) != 0 {
		t.Errorf("%d requests reached the pinned server", len(got))
	}
	wantReason(t, err, ReasonRedirect)
}

// TestFetchTimeout: a server that does not answer is timeout after
// fetch_timeout, not a hang.
func TestFetchTimeout(t *testing.T) {
	resetSchemes()
	s := newGitServer(t, provider.KindGitea)
	s.set(nil, "hang")
	r, _ := realRunner(t, Options{FetchTimeout: time.Second})
	start := time.Now()
	_, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: s.head})
	wantReason(t, err, ReasonTimeout)
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("Ensure took %v", d)
	}
}

// TestTooLarge: a repository above max_repo_mb (measured after the fetch)
// is too_large and is removed from the cache.
func TestTooLarge(t *testing.T) {
	resetSchemes()
	s := newGitServer(t, provider.KindGitea)
	r, cacheDir := realRunner(t, Options{MaxRepoBytes: 1})
	_, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: s.head})
	wantReason(t, err, ReasonTooLarge)
	entries, _ := os.ReadDir(cacheDir)
	for _, e := range scan(cacheDir) {
		t.Errorf("entry %s left in the cache", e.Repo)
	}
	if len(entries) == 0 {
		t.Error("the cache directory was not created")
	}
}

// TestNotFound: a missing PR ref or repository is not_found.
func TestNotFound(t *testing.T) {
	resetSchemes()
	s := newGitServer(t, provider.KindGitea)
	r, _ := realRunner(t, Options{})
	_, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 99, HeadSHA: s.head})
	wantReason(t, err, ReasonNotFound)
	repo := giteaRepo(s.base)
	repo.Name = "missing"
	_, err = r.Ensure(context.Background(), repo, PR{Number: 7, HeadSHA: s.head})
	wantReason(t, err, ReasonNotFound)
}
