package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const corpusDir = "corpus"

// evalset runs the command with the given environment and returns the exit
// code, stdout and stderr.
func evalset(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb, func(k string) string { return env[k] })
	return code, out.String(), errb.String()
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func readPRs(t *testing.T, dir string) prsFile {
	t.Helper()
	var f prsFile
	b, err := os.ReadFile(filepath.Join(dir, "prs.json")) //nolint:gosec // G304: a test directory
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output() //nolint:gosec // G204: test arguments
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestRepoName(t *testing.T) {
	re := regexp.MustCompile(`^evalset-[0-9a-f]{8}$`)
	seen := map[string]string{}
	for _, id := range []string{"java-cascade-overload", "python-security-injection", "go-clean-docs"} {
		n := repoName("evalset-", id)
		if !re.MatchString(n) {
			t.Errorf("repoName(%q) = %q, want evalset- and 8 hex digits", id, n)
		}
		for _, word := range strings.Split(id, "-") {
			if strings.Contains(strings.TrimPrefix(n, "evalset-"), word) {
				t.Errorf("repoName(%q) = %q contains %q", id, n, word)
			}
		}
		if n != repoName("evalset-", id) {
			t.Errorf("repoName(%q) is not stable", id)
		}
		if other, ok := seen[n]; ok {
			t.Errorf("%s and %s share %s", id, other, n)
		}
		seen[n] = id
	}
	// The first 8 hex digits of SHA-256("go-clean-docs").
	if got := repoName("evalset-", "go-clean-docs"); got != "evalset-916152a3" {
		t.Errorf("repoName = %s", got)
	}
}

// TestSetupReproducible is canary (c4): two setups of the same case give the
// same head commit id. The second setup starts in a later second than the
// first, so a commit date taken from the clock would differ.
func TestSetupReproducible(t *testing.T) {
	needGit(t)
	const id = "go-cascade-wrapped-error"
	setup := func() prEntry {
		clones, out := t.TempDir(), t.TempDir()
		code, stdout, stderr := evalset(t, nil, "setup", "-local", clones, "-corpus", corpusDir, "-cases", id,
			"-cache-dir", t.TempDir(), "-out", out)
		if code != 0 {
			t.Fatalf("setup exit %d: %s%s", code, stdout, stderr)
		}
		f := readPRs(t, out)
		if len(f.Cases) != 1 {
			t.Fatalf("prs.json has %d cases", len(f.Cases))
		}
		return f.Cases[0]
	}
	first := setup()
	start := time.Now().Unix()
	for time.Now().Unix() == start {
		time.Sleep(50 * time.Millisecond)
	}
	second := setup()
	if first.Head != second.Head || first.Base != second.Base {
		t.Errorf("two setups of %s gave head %s and %s, base %s and %s; want the same commits",
			id, first.Head, second.Head, first.Base, second.Base)
	}
}

// TestSetupLocalContent checks what a clone holds: the base on main, the
// head on change (checked out), neutral commit messages, the fixed
// identity, and nothing of the case metadata.
func TestSetupLocalContent(t *testing.T) {
	needGit(t)
	const id = "kotlin-clean-rename" // has deleted.txt
	clones, out := t.TempDir(), t.TempDir()
	code, _, stderr := evalset(t, nil, "setup", "-local", clones, "-corpus", corpusDir, "-cases", id,
		"-cache-dir", t.TempDir(), "-out", out)
	if code != 0 {
		t.Fatalf("setup exit %d: %s", code, stderr)
	}
	f := readPRs(t, out)
	e := f.Cases[0]
	if f.Mode != "local" || e.ID != id || e.Repo != repoName("evalset-", id) || e.Path != filepath.Join(clones, e.Repo) {
		t.Fatalf("prs.json entry = %+v (mode %s)", e, f.Mode)
	}
	dir := e.Path
	if got := gitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "change" {
		t.Errorf("checked out %s, want change", got)
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%s", "main"); got != "base" {
		t.Errorf("base message %q", got)
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%s", "change"); got != "Rename StatusEmails to CustomerNotifier" {
		t.Errorf("head message %q, want the case title", got)
	}
	// 2026-01-01T01:00:00Z as Unix seconds (the ISO form differs between git versions).
	if got := gitOut(t, dir, "log", "-1", "--format=%an <%ae> %at %ct", "change"); got != "evalset <evalset@example.com> 1767229200 1767229200" {
		t.Errorf("head identity and dates %q", got)
	}
	if got := gitOut(t, dir, "rev-list", "--count", "change"); got != "2" {
		t.Errorf("change has %s commits, want 2", got)
	}
	files := gitOut(t, dir, "ls-tree", "-r", "--name-only", "change")
	for _, bad := range []string{"case.toml", "deleted.txt", "base/", "head/"} {
		if strings.Contains(files, bad) {
			t.Errorf("the head tree holds %s:\n%s", bad, files)
		}
	}
	all := gitOut(t, dir, "log", "--format=%B", "--all")
	if strings.Contains(all, id) || strings.Contains(files, id) {
		t.Errorf("the case id appears in the commits")
	}
	// The deletion of deleted.txt is in the head commit.
	if gitOut(t, dir, "diff", "--no-renames", "--name-status", "--diff-filter=D", "main", "change") == "" {
		t.Errorf("the head commit deletes nothing; want the paths of deleted.txt deleted")
	}
	if e.HeadTree != gitOut(t, dir, "rev-parse", "change^{tree}") || e.BaseTree != gitOut(t, dir, "rev-parse", "main^{tree}") {
		t.Errorf("prs.json trees do not match the clone")
	}
}

func TestSetupGitea(t *testing.T) {
	const token = "gitea-token-7c1f9e5a3b" //nolint:gosec // G101: a synthetic test value
	f := newFakeGitea(t, "evalbot", token)
	env := map[string]string{envGiteaToken: token}
	ids := "go-cascade-interface,go-clean-interface-twin"
	out := t.TempDir()
	code, stdout, stderr := evalset(t, env, "setup", "-gitea-url", f.srv.URL, "-owner", "evalbot", "-corpus", corpusDir,
		"-cases", ids, "-cache-dir", t.TempDir(), "-out", out)
	if code != 0 {
		t.Fatalf("setup exit %d: %s%s", code, stdout, stderr)
	}
	if want := "evalset setup: 2 cases; repositories 2 created, 0 reused; pull requests 2 opened, 0 reused; prs.json written\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if strings.Contains(stdout+stderr, token) {
		t.Errorf("the token appears in the output")
	}
	if f.authOK == 0 {
		t.Fatalf("the fake Gitea saw no authenticated request")
	}
	prs := readPRs(t, out)
	if prs.Mode != "gitea" || len(prs.Cases) != 2 {
		t.Fatalf("prs.json = %+v", prs)
	}
	for _, e := range prs.Cases {
		bare := f.bare("evalbot", e.Repo)
		if got := gitOut(t, bare, "rev-parse", "refs/heads/main"); got != e.Base {
			t.Errorf("%s: main is %s, want %s", e.ID, got, e.Base)
		}
		if got := gitOut(t, bare, "rev-parse", "refs/heads/change"); got != e.Head {
			t.Errorf("%s: change is %s, want %s", e.ID, got, e.Head)
		}
		if !strings.HasPrefix(e.URL, f.srv.URL+"/evalbot/"+e.Repo+"/pulls/") {
			t.Errorf("%s: URL %s", e.ID, e.URL)
		}
		if strings.Contains(e.URL, e.ID) || strings.Contains(e.URL, "cascade") || strings.Contains(e.URL, "clean") {
			t.Errorf("%s: the pull request URL names the case: %s", e.ID, e.URL)
		}
		p := f.pulls["evalbot/"+e.Repo][0]
		if p["title"] == "" || strings.Contains(p["title"].(string), e.ID) {
			t.Errorf("%s: pull request title %q", e.ID, p["title"])
		}
	}
	// The twin has the same base as its cascade case: same base commit.
	if prs.Cases[0].Base != prs.Cases[1].Base {
		t.Errorf("a twin's base commit differs from its cascade case's")
	}
	b, _ := os.ReadFile(filepath.Join(out, "prs.json")) //nolint:gosec // G304: a test directory
	if strings.Contains(string(b), token) {
		t.Errorf("prs.json holds the token")
	}

	// A second setup reuses the repositories and the pull requests.
	out2 := t.TempDir()
	code, stdout, stderr = evalset(t, env, "setup", "-gitea-url", f.srv.URL, "-owner", "evalbot", "-corpus", corpusDir,
		"-cases", ids, "-cache-dir", t.TempDir(), "-out", out2)
	if code != 0 || stdout != "evalset setup: 2 cases; repositories 0 created, 2 reused; pull requests 0 opened, 2 reused; prs.json written\n" {
		t.Fatalf("second setup exit %d: %s%s", code, stdout, stderr)
	}
	if got := readPRs(t, out2); got.Cases[0].URL != prs.Cases[0].URL || got.Cases[1].Head != prs.Cases[1].Head {
		t.Errorf("the second setup gave other pull requests or commits")
	}

	// A repository whose main is not the case's base is refused, by case.
	other := prs.Cases[0]
	tmp := t.TempDir()
	gitOut(t, tmp, "init", "-q")
	gitOut(t, tmp, "-c", "user.name=x", "-c", "user.email=x@example.com", "commit", "-q", "--allow-empty", "-m", "x")
	gitOut(t, tmp, "push", "-q", "--force", f.bare("evalbot", other.Repo), "HEAD:refs/heads/main")
	code, _, stderr = evalset(t, env, "setup", "-gitea-url", f.srv.URL, "-owner", "evalbot", "-corpus", corpusDir,
		"-cases", ids, "-cache-dir", t.TempDir(), "-out", t.TempDir())
	if code != 1 || !strings.Contains(stderr, "case "+other.ID+": the repository "+other.Repo+" exists and its main is not the base commit of the case") {
		t.Errorf("tampered main: exit %d, stderr %q", code, stderr)
	}
}

func TestSetupRefusals(t *testing.T) {
	cache := t.TempDir()
	full := t.TempDir()
	if err := os.WriteFile(filepath.Join(full, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{envGiteaToken: "t"}
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"no token", nil, []string{"-gitea-url", "http://gitea.example.com", "-owner", "o", "-out", t.TempDir()},
			"EVALSET_GITEA_TOKEN is not set; the token is read from the environment only"},
		{"no owner", env, []string{"-gitea-url", "http://gitea.example.com", "-out", t.TempDir()}, "-gitea-url and -owner are required (or -local)"},
		{"credentials in the URL", env, []string{"-gitea-url", "http://u:p@gitea.example.com", "-owner", "o", "-out", t.TempDir()},
			"-gitea-url must not carry credentials, a query or a fragment"},
		{"token flag", env, []string{"-token", "t", "-out", t.TempDir()}, "flag provided but not defined: -token"},
		{"local and gitea", env, []string{"-local", t.TempDir(), "-owner", "o", "-out", t.TempDir()}, "-local cannot be combined"},
		{"out not empty", env, []string{"-local", t.TempDir(), "-out", full}, "-out is not empty"},
		{"out inside the cache", env, []string{"-local", t.TempDir(), "-out", filepath.Join(cache, "x")}, "-out is inside the repository-context cache"},
		{"unknown case", env, []string{"-local", t.TempDir(), "-cases", "no-such-case", "-out", t.TempDir()}, "-cases names no-such-case"},
		{"bad prefix", env, []string{"-local", t.TempDir(), "-prefix", "a/b", "-out", t.TempDir()}, "-prefix may hold"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"setup", "-corpus", corpusDir, "-cache-dir", cache}, tc.args...)
			code, _, stderr := evalset(t, tc.env, args...)
			if code == 0 || !strings.Contains(stderr, tc.want) {
				t.Errorf("exit %d, stderr %q; want a refusal containing %q", code, stderr, tc.want)
			}
		})
	}
}
