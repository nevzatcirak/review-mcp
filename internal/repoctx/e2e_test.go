package repoctx

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: serves git http-backend to the test only
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const snippetSentinel = "SNIPPET-SENTINEL-4c9e1b"

func realGit(t *testing.T) (gitPath, backend string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("git http-backend tests are skipped on Windows: the CGI backend of Git for Windows is not run under net/http/cgi in CI; the fake-backend tests cover the rest")
	}
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; the http-backend tests need it")
	}
	out, err := exec.Command(p, "--exec-path").Output() //nolint:gosec // G204: the system git
	if err != nil {
		t.Skip("git --exec-path failed")
	}
	be := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if fi, err := os.Stat(be); err != nil || fi.IsDir() {
		t.Skip("git-http-backend was not found under git --exec-path")
	}
	return p, be
}

func gitIn(t *testing.T, dir, gitPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitPath, args...) //nolint:gosec // G204: the system git with fixed test arguments
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "LANG=C", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestEndToEndRealGit: a real Session on the system git over git
// http-backend (Gitea layout): the head is fetched once, the symbol the
// diff defines is found in another file, the pull request's own file is not
// among the hits, and the rendered block carries the snippet. The log, at
// debug level, holds counts only: never the snippet, a symbol or a path.
func TestEndToEndRealGit(t *testing.T) {
	gitPath, backend := realGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	files := map[string]string{
		"main.go":     "package main\n\nfunc WidgetFactory() int { return 1 }\n",
		"pkg/use.go":  "package pkg\n\nfunc caller() int {\n\t// " + snippetSentinel + "\n\treturn WidgetFactory()\n}\n",
		"pkg/more.go": "package pkg\n\nvar _ = WidgetFactory\n",
	}
	for name, content := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, src, gitPath, "init", "-q")
	gitIn(t, src, gitPath, "add", ".")
	gitIn(t, src, gitPath, "commit", "-q", "-m", "first")
	root := filepath.Join(tmp, "srv")
	bare := filepath.Join(root, "owner", "repo.git")
	gitIn(t, tmp, gitPath, "init", "-q", "--bare", bare)
	gitIn(t, tmp, gitPath, "--git-dir="+bare, "config", "uploadpack.allowFilter", "true")
	gitIn(t, src, gitPath, "push", "-q", bare, "HEAD:refs/pull/7/head", "HEAD:refs/heads/main")
	head := strings.TrimSpace(gitIn(t, src, gitPath, "rev-parse", "HEAD"))

	h := &cgi.Handler{Path: backend, Root: "", Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}, Stderr: io.Discard}
	srv := httptest.NewServer(http.HandlerFunc(h.ServeHTTP))
	t.Cleanup(srv.Close)

	cfg := config.Defaults()
	cfg.Context.Repo.Enabled = true
	cfg.Context.Repo.CacheDir = filepath.Join(tmp, "cache")
	cfg.Gitea.BaseURL = srv.URL
	cfg.Secrets.GiteaToken = config.NewSecret("FAKE-token-do-not-leak")

	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ref := provider.PRRef{Kind: provider.KindGitea, Namespace: "owner", Repo: "repo", Number: 7}
	changed := []provider.FilePatch{{Path: "main.go", Type: provider.ChangeModified,
		Patch: "@@ -1,3 +1,3 @@\n package main\n \n-func WidgetFactory() int { return 0 }\n+func WidgetFactory() int { return 1 }\n"}}
	s := Open(cfg, nil, ref, nil, head, changed, nil, nil, log)

	found := s.Find(context.Background(), changed)
	if found.Skipped != "" || found.Symbols != 1 || len(found.Hits) < 2 {
		t.Fatalf("found = %+v", found)
	}
	for _, hit := range found.Hits {
		if hit.Path == "main.go" {
			t.Errorf("the pull request's own file is among the hits: %+v", hit)
		}
	}
	b := Render(found.Hits, 2000, factor)
	if b.Entries != len(found.Hits) || !strings.Contains(b.Text, "uses WidgetFactory") || !strings.Contains(b.Text, snippetSentinel) {
		t.Errorf("block = %q", b.Text)
	}
	// A second search of the same session does not fetch again.
	again := s.Find(context.Background(), changed)
	if again.Skipped != "" || len(again.Hits) != len(found.Hits) {
		t.Errorf("second find = %+v", again)
	}
	for _, leak := range []string{snippetSentinel, "WidgetFactory", "pkg/use.go", "FAKE-token-do-not-leak"} {
		if strings.Contains(logs.String(), leak) {
			t.Errorf("%q reached the debug log:\n%s", leak, logs.String())
		}
	}

	// A wrong head SHA is a fixed reason, not an error.
	s2 := Open(cfg, nil, ref, nil, strings.Repeat("1", 40), changed, nil, nil, log)
	if f := s2.Find(context.Background(), changed); f.Skipped == "" || len(f.Hits) != 0 {
		t.Errorf("wrong head: %+v", f)
	}
	rc, notes := Summarize([]Outcome{{Status: llmrun.RepoSkipped, Reason: s2.Ready(context.Background())}})
	if rc.Status != llmrun.RepoSkipped || len(notes) != 1 || !strings.HasPrefix(notes[0], "repository context skipped: ") {
		t.Errorf("summary = %+v %v", rc, notes)
	}
}
