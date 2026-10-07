package gitctx

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: serves git http-backend to the test only; Go is far past 1.6.3
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	testToken = "FAKE-git-token-ZQ7X-do-not-leak" //nolint:gosec // synthetic test value
	testUser  = "review-bot"
)

// fakeGitBin is the fake git, built once per test run from
// testdata/fakegit (a small program: not race-instrumented, quick to start).
// It is built in TestMain, before any test changes the environment.
var (
	fakeGitBin string
	fakeGitErr error
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "gitctx-fakegit-")
	if err != nil {
		fakeGitErr = err
	} else {
		fakeGitBin, fakeGitErr = buildFakeGit(tmp)
	}
	code := m.Run()
	if tmp != "" {
		_ = os.RemoveAll(tmp)
	}
	os.Exit(code)
}

func buildFakeGit(dir string) (string, error) {
	bin := filepath.Join(dir, "fakegit"+exeSuffix())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", bin, "./testdata/fakegit") //nolint:gosec // G204: fixed arguments; the output is under a temporary directory
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", errors.New("go build ./testdata/fakegit: " + err.Error() + "\n" + string(out))
	}
	return bin, nil
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// fakeBehavior configures the fake git (testdata/fakegit).
type fakeBehavior struct {
	// Version is the "git --version" output; "" means a current git.
	Version string `json:"version"`
	// FetchStderr and FetchExit are the fetch's stderr and exit code.
	FetchStderr string `json:"fetch_stderr"`
	FetchExit   int    `json:"fetch_exit"`
	// SHA is what rev-parse prints.
	SHA string `json:"sha"`
}

// fakeCall is one recorded invocation of the fake git.
type fakeCall struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

// subcommand returns the first argument after the global options.
func (c fakeCall) subcommand() string {
	for i := 0; i < len(c.Args); i++ {
		a := c.Args[i]
		switch {
		case a == "-c":
			i++
		case strings.HasPrefix(a, "--git-dir="):
		default:
			return a
		}
	}
	return ""
}

// fakeGit is the fake git of one test: a link to the built binary under a
// path of its own (so its version check is its own), configured and
// recording through the test's HOME.
type fakeGit struct {
	home, path string
}

// newFakeGit points HOME at a fresh directory and installs the fake git.
func newFakeGit(t *testing.T, beh fakeBehavior) *fakeGit {
	t.Helper()
	if fakeGitErr != nil {
		t.Fatalf("building the fake git: %v", fakeGitErr)
	}
	bin := fakeGitBin
	isolateHome(t)
	dst := filepath.Join(t.TempDir(), "git"+exeSuffix())
	if err := os.Link(bin, dst); err != nil {
		copyFile(t, bin, dst)
	}
	f := &fakeGit{home: os.Getenv("HOME"), path: dst}
	f.set(t, beh)
	return f
}

func (f *fakeGit) set(t *testing.T, beh fakeBehavior) {
	t.Helper()
	b, _ := json.Marshal(beh)
	if err := os.WriteFile(filepath.Join(f.home, "fake.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeGit) calls(t *testing.T) []fakeCall {
	t.Helper()
	fh, err := os.Open(filepath.Join(f.home, "calls.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fh.Close() }()
	var out []fakeCall
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var c fakeCall
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src) //nolint:gosec // G304: the fake git binary built by this test run
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700) //nolint:gosec // G302: the fake git must be executable
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// isolateHome points HOME (and USERPROFILE) at an empty directory so that
// the developer's own git configuration cannot change a test.
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
}

func resetSchemes() { workingScheme.Clear() }

const fakeSHA = "0123456789abcdef0123456789abcdef01234567"

func giteaRepo(base string) Repo {
	return Repo{Kind: provider.KindGitea, BaseURL: base, Namespace: "owner", Name: "repo", Token: config.NewSecret(testToken)}
}

func bbsRepo(base string) Repo {
	return Repo{Kind: provider.KindBitbucketServer, BaseURL: base, Namespace: "PROJ", Name: "repo", Token: config.NewSecret(testToken)}
}

// ---- real git and git http-backend ----

// realGit returns the system git for the http-backend tests, or skips.
func realGit(t *testing.T) (gitPath, backend string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("git http-backend tests are skipped on Windows: the CGI backend of Git for Windows is not run under net/http/cgi in CI; the fake-git and cache tests cover the Windows-specific paths")
	}
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; the http-backend tests need it")
	}
	if _, ok := parseGitVersion(gitOut(t, "", p, "--version")); !ok {
		t.Skip("git " + MinGitVersion + " or later is required for the http-backend tests")
	}
	be := filepath.Join(strings.TrimSpace(gitOut(t, "", p, "--exec-path")), "git-http-backend")
	if fi, err := os.Stat(be); err != nil || fi.IsDir() {
		t.Skip("git-http-backend was not found under git --exec-path")
	}
	return p, be
}

// gitOut runs a real git command for test setup, with a clean environment.
func gitOut(t *testing.T, dir, gitPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitPath, args...) //nolint:gosec // G204: the system git with fixed test arguments
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "LANG=C", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// gitServer serves bare repositories through git http-backend. It records
// the Authorization header of every request and answers 401 to any that
// accept rejects.
type gitServer struct {
	srv    *httptest.Server
	base   string // the provider base URL
	head   string // head SHA of PR 7
	root   string // GIT_PROJECT_ROOT
	mu     sync.Mutex
	auths  []string
	accept func(auth string) bool
	mode   string // "", "redirect", "hang"
	hang   chan struct{}
	// redirectTo is the target of mode "redirect".
	redirectTo string
}

// newGitServer serves owner/repo (Gitea layout, refs/pull/7/head) or
// PROJ/repo (Bitbucket layout under the context path /bb, with
// refs/pull-requests/7/from).
func newGitServer(t *testing.T, kind provider.Kind) *gitServer {
	t.Helper()
	gitPath, backend := realGit(t)
	isolateHome(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	gitOut(t, src, gitPath, "init", "-q")
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc Helper() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOut(t, src, gitPath, "add", ".")
	gitOut(t, src, gitPath, "commit", "-q", "-m", "first")

	ns, prRef, prefix := "owner", "refs/pull/7/head", ""
	if kind == provider.KindBitbucketServer {
		ns, prRef, prefix = "PROJ", "refs/pull-requests/7/from", "/bb"
	}
	root := filepath.Join(tmp, "srv")
	bare := filepath.Join(root, ns, "repo.git")
	gitOut(t, tmp, gitPath, "init", "-q", "--bare", bare)
	gitOut(t, tmp, gitPath, "--git-dir="+bare, "config", "uploadpack.allowFilter", "true")
	gitOut(t, src, gitPath, "push", "-q", bare, "HEAD:"+prRef, "HEAD:refs/heads/main")

	s := &gitServer{root: root, hang: make(chan struct{})}
	s.head = strings.TrimSpace(gitOut(t, src, gitPath, "rev-parse", "HEAD"))
	cgiRoot := prefix
	if kind == provider.KindBitbucketServer {
		cgiRoot = prefix + "/scm"
	}
	h := &cgi.Handler{
		Path:   backend,
		Root:   cgiRoot,
		Env:    []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
		Stderr: io.Discard,
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		s.mu.Lock()
		s.auths = append(s.auths, auth)
		accept, mode, target := s.accept, s.mode, s.redirectTo
		s.mu.Unlock()
		switch mode {
		case "redirect":
			http.Redirect(w, r, target+r.URL.Path, http.StatusFound) //nolint:gosec // G710: the redirect under test, to a test server
			return
		case "hang":
			select {
			case <-s.hang:
			case <-r.Context().Done():
			}
			return
		}
		if accept != nil && !accept(auth) {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		close(s.hang)
		s.srv.Close()
	})
	s.base = s.srv.URL + prefix
	return s
}

func (s *gitServer) set(accept func(string) bool, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accept, s.mode = accept, mode
}

// requests returns and clears the recorded Authorization headers.
func (s *gitServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.auths
	s.auths = nil
	return out
}

func acceptExactly(v string) func(string) bool {
	return func(auth string) bool { return auth == v }
}

// realRunner is a Runner on the system git with a fresh cache directory.
func realRunner(t *testing.T, opts Options) (*Runner, string) {
	t.Helper()
	gitPath, _ := realGit(t)
	if opts.CacheDir == "" {
		opts.CacheDir = filepath.Join(t.TempDir(), "cache")
	}
	if opts.FetchTimeout == 0 {
		opts.FetchTimeout = 30 * time.Second
	}
	opts.GitPath = gitPath
	return New(opts), opts.CacheDir
}

func identityOf(name string, calls *int) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		*calls++
		return name, nil
	}
}
