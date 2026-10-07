package gitctx

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeRunner is a Runner on a fake git with a fresh cache directory.
func fakeRunner(t *testing.T, beh fakeBehavior, opts Options) (*Runner, *fakeGit) {
	t.Helper()
	f := newFakeGit(t, beh)
	if opts.CacheDir == "" {
		opts.CacheDir = filepath.Join(t.TempDir(), "cache")
	}
	opts.GitPath = f.path
	return New(opts), f
}

func callsOf(calls []fakeCall, sub string) []fakeCall {
	var out []fakeCall
	for _, c := range calls {
		if c.subcommand() == sub {
			out = append(out, c)
		}
	}
	return out
}

// envValue returns the value of name in env and whether it is present.
func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// TestTokenNeverInArgv [canary]: the token reaches git only through the
// environment of the fetch (GIT_CONFIG_*), never through an argument, and
// no other git command sees it at all.
func TestTokenNeverInArgv(t *testing.T) {
	resetSchemes()
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
	if _, err := r.Ensure(context.Background(), giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	calls := f.calls(t)
	fetches := callsOf(calls, "fetch")
	if len(fetches) != 1 {
		t.Fatalf("%d fetches, want 1 (calls: %v)", len(fetches), calls)
	}
	for _, c := range calls {
		for i, a := range c.Args {
			if strings.Contains(a, testToken) || strings.Contains(strings.ToLower(a), "extraheader") {
				t.Errorf("git %s: argument %d carries the credential", c.subcommand(), i)
			}
		}
		if c.subcommand() != "fetch" && strings.Contains(strings.Join(c.Env, "\n"), testToken) {
			t.Errorf("git %s: the token is in its environment", c.subcommand())
		}
	}
	env := fetches[0].Env
	for name, want := range map[string]string{
		"GIT_CONFIG_COUNT":   "2",
		"GIT_CONFIG_KEY_0":   "http.extraHeader",
		"GIT_CONFIG_VALUE_0": "",
		"GIT_CONFIG_KEY_1":   "http.extraHeader",
		"GIT_CONFIG_VALUE_1": "Authorization: token " + testToken,
	} {
		if got, ok := envValue(env, name); !ok || got != want {
			t.Errorf("fetch env %s = %q (set %v), want %q", name, got, ok, want)
		}
	}
}

// TestSafetySettings: every command carries the fixed -c settings; the
// fetch has the pinned URL, the PR ref and the size flags, and allows http
// only for an http base URL.
func TestSafetySettings(t *testing.T) {
	for _, base := range []string{"https://bitbucket.example.com/bb", "http://bitbucket.example.com:7990"} {
		t.Run(base, func(t *testing.T) {
			resetSchemes()
			r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
			repo := bbsRepo(base)
			repo.CACert = "/etc/ssl/bitbucket-ca.pem"
			if _, err := r.Ensure(context.Background(), repo, PR{Number: 7, HeadSHA: fakeSHA}); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			calls := f.calls(t)
			for _, c := range calls {
				args := " " + strings.Join(c.Args, " ") + " "
				for _, want := range []string{
					" -c credential.helper= ", " -c core.hooksPath=" + os.DevNull + " ",
					" -c protocol.allow=never ", " -c protocol.https.allow=always ",
					" -c http.followRedirects=false ",
				} {
					if !strings.Contains(args, want) {
						t.Errorf("git %s lacks %q: %s", c.subcommand(), strings.TrimSpace(want), args)
					}
				}
				if c.subcommand() != "fetch" && strings.Contains(args, "protocol.http.allow") {
					t.Errorf("git %s allows http", c.subcommand())
				}
				for _, name := range []string{"GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "SSH_ASKPASS", "LANG"} {
					want := map[string]string{"GIT_TERMINAL_PROMPT": "0", "LANG": "C"}[name]
					if got, ok := envValue(c.Env, name); !ok || got != want {
						t.Errorf("git %s: %s = %q (set %v), want %q", c.subcommand(), name, got, ok, want)
					}
				}
			}
			fetch := " " + strings.Join(callsOf(calls, "fetch")[0].Args, " ") + " "
			for _, want := range []string{
				" --depth=1 ", " --filter=blob:limit=1m ", " --no-tags ",
				" origin +refs/pull-requests/7/from:refs/review-mcp/pr/7 ",
				" -c http.sslCAInfo=/etc/ssl/bitbucket-ca.pem ",
			} {
				if !strings.Contains(fetch, want) {
					t.Errorf("fetch lacks %q: %s", strings.TrimSpace(want), fetch)
				}
			}
			if http := strings.Contains(fetch, " -c protocol.http.allow=always "); http != strings.HasPrefix(base, "http:") {
				t.Errorf("protocol.http.allow in fetch = %v for %s", http, base)
			}
			var url string
			for _, c := range callsOf(calls, "config") {
				if c.Args[len(c.Args)-2] == "remote.origin.url" {
					url = c.Args[len(c.Args)-1]
				}
			}
			if want := base + "/scm/PROJ/repo.git"; url != want {
				t.Errorf("remote.origin.url = %q, want %q", url, want)
			}
		})
	}
}

// allowedEnv is the allowlist of the child environment.
var allowedEnv = regexp.MustCompile(`^(?i:PATH|HOME|USERPROFILE|SYSTEMROOT|LANG|GIT_TERMINAL_PROMPT|GIT_ASKPASS|SSH_ASKPASS|GIT_CONFIG_COUNT|GIT_CONFIG_KEY_\d+|GIT_CONFIG_VALUE_\d+)$`)

// TestParentEnvironmentNotPassed [canary]: a variable of the parent never
// reaches git; every variable git sees is on the allowlist.
func TestParentEnvironmentNotPassed(t *testing.T) {
	resetSchemes()
	const sentinel = "sentinel-value-ZQ7X"
	t.Setenv("REVIEW_MCP_SENTINEL_CANARY", sentinel)
	t.Setenv("GIT_DIR", "/tmp/elsewhere-"+sentinel)
	t.Setenv("GIT_CONFIG_PARAMETERS", "'http.extraheader'='"+sentinel+"'")
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
	if _, err := r.Ensure(context.Background(), giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	calls := f.calls(t)
	if len(calls) < 3 {
		t.Fatalf("only %d git calls recorded", len(calls))
	}
	for _, c := range calls {
		for _, kv := range c.Env {
			if strings.HasPrefix(kv, "=") { // Windows per-drive directories
				continue
			}
			name, _, _ := strings.Cut(kv, "=")
			if strings.Contains(kv, sentinel) || !allowedEnv.MatchString(name) {
				t.Errorf("git %s got the parent's %s", c.subcommand(), name)
			}
		}
	}
}

// TestStderrNeverInError: git's stderr (which can echo URLs and headers) is
// classified and dropped; the error and the note are fixed text.
func TestStderrNeverInError(t *testing.T) {
	resetSchemes()
	const leak = "SECRET-STDERR-ZQ7X"
	cases := []struct {
		stderr, reason string
	}{
		{"fatal: unable to access 'https://x:" + leak + "@your-gitea.example/': SSL certificate problem\n", ReasonGitFailed},
		{"fatal: Authentication failed for 'https://your-gitea.example/" + leak + "'\n", ReasonAuth},
		{"fatal: couldn't find remote ref refs/pull/7/head " + leak + "\n", ReasonNotFound},
		{"fatal: unable to access '" + leak + "': The requested URL returned error: 302\n", ReasonRedirect},
	}
	for _, tc := range cases {
		r, _ := fakeRunner(t, fakeBehavior{FetchStderr: tc.stderr, FetchExit: 128}, Options{})
		_, err := r.Ensure(context.Background(), giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA})
		wantReason(t, err, tc.reason)
		if strings.Contains(err.Error(), leak) || strings.Contains(Note(ReasonOf(err)), leak) {
			t.Errorf("stderr leaked: %q", err.Error())
		}
	}
}

// TestGitVersionCheck: a missing or old git is git_unavailable (a note,
// never a failure); the version is checked once per process and binary.
func TestGitVersionCheck(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
		status  string
	}{
		{"git version 2.30.9", false, "unavailable: git 2.31 or later is required"},
		{"git version 1.99", false, "unavailable: git 2.31 or later is required"},
		{"not git at all", false, "unavailable: git 2.31 or later is required"},
		{"git version 2.31.0", true, "git 2.31.0"},
		{"git version 2.39.3 (Apple Git-146)", true, "git 2.39.3"},
		{"git version 2.45.1.windows.1", true, "git 2.45.1"},
		{"git version 3.0", true, "git 3.0"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			resetSchemes()
			r, f := fakeRunner(t, fakeBehavior{Version: tc.version, SHA: fakeSHA}, Options{})
			for i := 0; i < 2; i++ {
				_, err := r.Ensure(context.Background(), giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA})
				if tc.ok && err != nil {
					t.Fatalf("Ensure: %v", err)
				}
				if !tc.ok {
					wantReason(t, err, ReasonGitUnavailable)
				}
			}
			if got := GitStatus(f.path); got != tc.status {
				t.Errorf("GitStatus = %q, want %q", got, tc.status)
			}
			if n := len(callsOf(f.calls(t), "--version")); n != 1 {
				t.Errorf("git --version ran %d times, want once", n)
			}
			if !tc.ok && len(f.calls(t)) != 1 {
				t.Errorf("an unusable git was used: %d calls", len(f.calls(t)))
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "no-such-git")
	_, err := New(Options{GitPath: missing, CacheDir: t.TempDir()}).Ensure(context.Background(),
		giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA})
	wantReason(t, err, ReasonGitUnavailable)
	if got := GitStatus(missing); got != "unavailable: git 2.31 or later is required" {
		t.Errorf("GitStatus(missing) = %q", got)
	}
}

// TestBusyAndStaleLock: a fresh lock held by another process makes Ensure
// wait and give up with busy at the fetch timeout; a lock older than ten
// minutes is stale and taken over.
func TestBusyAndStaleLock(t *testing.T) {
	resetSchemes()
	r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{FetchTimeout: 300 * time.Millisecond})
	root, err := r.openRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "your-gitea.example", "owner", "repo")
	if err := os.MkdirAll(entry, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(entry, lockName)
	if err := os.WriteFile(lock, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = r.Ensure(context.Background(), giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA})
	wantReason(t, err, ReasonBusy)

	old := time.Now().Add(-lockStaleAfter - time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	r.opts.FetchTimeout = 30 * time.Second // only the busy case needs a short one
	if _, err := r.Ensure(context.Background(), giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA}); err != nil {
		t.Fatalf("Ensure with a stale lock: %v", err)
	}
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Error("the lock was not released")
	}
}

// TestEnsureSweepsIdleRepositories: every use first deletes repositories
// idle longer than idle_days (simulated by back-dating the marker, L3).
func TestEnsureSweepsIdleRepositories(t *testing.T) {
	resetSchemes()
	r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{IdleDays: 7})
	root, err := r.openRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	idle := makeEntry(t, root, "your-gitea.example/owner/old", 2048, time.Now().Add(-8*24*time.Hour))
	fresh := makeEntry(t, root, "your-gitea.example/owner/fresh", 2048, time.Now().Add(-6*24*time.Hour))
	if _, err := r.Ensure(context.Background(), giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Lstat(idle); !os.IsNotExist(err) {
		t.Error("the idle repository was not swept")
	}
	if _, err := os.Lstat(fresh); err != nil {
		t.Error("a repository within idle_days was swept")
	}
}

// TestUnsupportedInput: an unusable base URL, name, number or SHA is
// refused before git runs.
func TestUnsupportedInput(t *testing.T) {
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
	bad := []struct {
		name string
		repo Repo
		pr   PR
	}{
		{"credentials in base", giteaRepo("https://user:pw@your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA}},
		{"query in base", giteaRepo("https://your-gitea.example?x=1"), PR{Number: 7, HeadSHA: fakeSHA}},
		{"file scheme", giteaRepo("file:///srv/git"), PR{Number: 7, HeadSHA: fakeSHA}},
		{"ssh scheme", giteaRepo("ssh://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA}},
		{"no number", giteaRepo("https://your-gitea.example"), PR{HeadSHA: fakeSHA}},
		{"short sha", giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: "abc123"}},
		{"option-like sha", giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: "--upload-pack=x"}},
		{"unknown kind", Repo{Kind: "github", BaseURL: "https://your-gitea.example", Namespace: "o", Name: "r"}, PR{Number: 7, HeadSHA: fakeSHA}},
	}
	for _, tc := range bad {
		_, err := r.Ensure(context.Background(), tc.repo, tc.pr)
		var e *Error
		if !asError(err, &e) || e.Reason != ReasonUnsupported {
			t.Errorf("%s: err = %v, want unsupported", tc.name, err)
		}
	}
	if n := len(f.calls(t)); n != 0 {
		t.Errorf("git ran %d times for refused input", n)
	}
}
