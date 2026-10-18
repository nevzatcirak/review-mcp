package repotools

import (
	"context"
	"io"
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
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
)

func realGit(t *testing.T) (gitPath, backend string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("git http-backend tests are skipped on Windows: the CGI backend of Git for Windows is not run under net/http/cgi in CI; the fake-reader tests cover the rest")
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

// TestToolsOnRealRepository: a real Session fetches the head of a pull
// request over git http-backend (a partial clone that leaves out blobs over
// 1 MiB), and the three tools read it offline: a file, a directory, a
// search; a binary file, a blob the clone lacks and a file over
// diff.max_file_bytes give their fixed results, never a fetch. The server
// sees no request while the tools run.
func TestToolsOnRealRepository(t *testing.T) {
	gitPath, backend := realGit(t)
	t.Setenv("HOME", t.TempDir())
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	files := map[string]string{
		"main.go":         "package main\n\nfunc main() { lookup() }\n",
		"store/lookup.go": "package store\n\n// lookup returns nil, nil when the key is not found.\nfunc lookup() (*int, error) { return nil, nil }\n",
		"store/data.bin":  "\x00\x01binary",
		"store/huge.txt":  "needle\n" + strings.Repeat("y", 1100<<10) + "\n",
		"store/mid.txt":   strings.Repeat("z", 5000) + "\n",
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
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Defaults()
	cfg.Context.Repo.Enabled = true
	cfg.Context.Repo.CacheDir = filepath.Join(tmp, "cache")
	cfg.Gitea.BaseURL = srv.URL
	cfg.Secrets.GiteaToken = config.NewSecret("FAKE-token-do-not-leak")
	ref := provider.PRRef{Kind: provider.KindGitea, Namespace: "owner", Repo: "repo", Number: 7}
	s := repoctx.Open(cfg, nil, ref, nil, head, nil, nil, nil, nil)
	if _, ok := Open(s, Settings{}, nil); ok {
		t.Fatal("a box before the fetch")
	}
	if reason := s.Ready(context.Background()); reason != "" {
		t.Fatalf("fetch: %s", reason)
	}
	b, ok := Open(s, Settings{MaxToolTokens: 40000, MaxFileBytes: 4096}, nil)
	if !ok {
		t.Fatal("no box after the fetch")
	}
	fetched := requests

	msgs := b.Round(context.Background(), 1, []llm.ToolCall{
		call("1", ReadFile, `{"path":"store/lookup.go","start_line":3,"end_line":4}`),
		call("2", ListDir, `{"path":"store"}`),
		call("3", Search, `{"text":"nil, nil"}`),
		call("4", ReadFile, `{"path":"store/data.bin"}`),
	}, 1<<20)
	if want := "lines 3-4 of 4\n```\n3  // lookup returns nil, nil when the key is not found.\n4  func lookup() (*int, error) { return nil, nil }\n```"; !strings.HasSuffix(msgs[0].Content, want) {
		t.Errorf("read_file: %q", msgs[0].Content)
	}
	if want := "list_dir store: 4 entries\n```\ndata.bin\nhuge.txt\nlookup.go\nmid.txt\n```"; !strings.HasSuffix(msgs[1].Content, want) {
		t.Errorf("list_dir: %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[2].Content, "search \"nil, nil\" in the repository: 2 matches\n```\n[store/lookup.go:3]\n2  \n3  // lookup returns nil, nil") {
		t.Errorf("search: %q", msgs[2].Content)
	}
	if msgs[3].Content != ResultBinary {
		t.Errorf("binary: %q", msgs[3].Content)
	}
	msgs = b.Round(context.Background(), 2, []llm.ToolCall{
		call("5", ReadFile, `{"path":"store/huge.txt"}`),
		call("6", ReadFile, `{"path":"store/mid.txt"}`),
		call("7", Search, `{"text":"needle"}`),
		call("8", ReadFile, `{"path":"../../etc/passwd"}`),
	}, 1<<20)
	if msgs[0].Content != ResultNotFetched || msgs[1].Content != ResultTooLarge || msgs[3].Content != ResultNotAllowed {
		t.Errorf("fixed results: %q", msgs)
	}
	if !strings.Contains(msgs[2].Content, "0 matches") || !strings.Contains(msgs[2].Content, "1 files were not searched: they were not fetched.") {
		t.Errorf("search of a missing blob: %q", msgs[2].Content)
	}
	if requests != fetched {
		t.Fatalf("the tools made %d requests to the git server", requests-fetched)
	}
}
