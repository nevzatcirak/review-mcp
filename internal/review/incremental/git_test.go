package incremental

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// ---- a real git repository in a repository-context cache ----

// gitRepo is a source repository and a cache entry the way repository
// context leaves one after earlier fetches (the layout of internal/gitctx:
// <cache>/CACHEDIR.TAG, <cache>/.home, <cache>/<host>/<base>/<ns>/<repo>/
// with last-used and the bare repository git/).
type gitRepo struct {
	t       *testing.T
	gitPath string
	src     string
	runner  *gitctx.Runner
	co      gitctx.Checkout
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; the real-repository tests need it")
	}
	if !strings.HasPrefix(gitctx.GitStatus(gitPath), "git ") {
		t.Skip("git " + gitctx.MinGitVersion + " or later is required")
	}
	tmp := t.TempDir()
	g := &gitRepo{t: t, gitPath: gitPath, src: filepath.Join(tmp, "src")}
	if err := os.MkdirAll(g.src, 0o700); err != nil {
		t.Fatal(err)
	}
	g.git(g.src, "init", "-q")

	cache := filepath.Join(tmp, "cache")
	entry := filepath.Join(cache, "host.example", "_", "owner", "repo")
	for _, d := range []string{filepath.Join(cache, ".home"), entry} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tag := "Signature: 8a477f597d28d172789f06886806bc55\n# This file is a cache directory tag created by review-mcp (repository context).\n"
	if err := os.WriteFile(filepath.Join(cache, "CACHEDIR.TAG"), []byte(tag), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "last-used"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(entry, "git")
	g.git(tmp, "init", "-q", "--bare", gitDir)
	g.runner = gitctx.New(gitctx.Options{CacheDir: cache, GitPath: gitPath, FetchTimeout: 30 * time.Second})
	g.co = gitctx.Checkout{GitDir: gitDir, Ref: "refs/review-mcp/pr/7"}
	return g
}

// git runs a real git command for the fixture, with a clean environment.
func (g *gitRepo) git(dir string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command(g.gitPath, args...) //nolint:gosec // G204: the system git with fixed test arguments
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + g.t.TempDir(), "LANG=C", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	}
	if v := os.Getenv("SYSTEMROOT"); v != "" {
		cmd.Env = append(cmd.Env, "SYSTEMROOT="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// commit writes files, deletes remove (slash-separated names) and commits;
// it returns the new commit.
func (g *gitRepo) commit(files map[string]string, remove ...string) string {
	g.t.Helper()
	for name, content := range files {
		p := filepath.Join(g.src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			g.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			g.t.Fatal(err)
		}
	}
	for _, name := range remove {
		if err := os.Remove(filepath.Join(g.src, filepath.FromSlash(name))); err != nil {
			g.t.Fatal(err)
		}
	}
	g.git(g.src, "add", "-A")
	g.git(g.src, "commit", "-q", "--allow-empty", "-m", "step")
	return strings.TrimSpace(g.git(g.src, "rev-parse", "HEAD"))
}

// fetch copies the commit rev and its history into the cache under ref, as
// a fetch of the pull request head does.
func (g *gitRepo) fetch(rev, ref string) {
	g.t.Helper()
	g.git(g.src, "push", "-q", "-f", g.co.GitDir, rev+":"+ref)
}

// cacheReader is the provider of the API strategy over the same
// repository: GetFileAt and ListDir answer from the cache, so both
// strategies see one fixture.
type cacheReader struct {
	g     *gitRepo
	reads atomic.Int32
}

func (c *cacheReader) GetFileAt(ctx context.Context, _ provider.PRRef, path, rev string) ([]byte, error) {
	c.reads.Add(1)
	return c.g.runner.FileAt(ctx, c.g.co, rev, path, 1<<20)
}

func (c *cacheReader) ListDir(ctx context.Context, _ provider.PRRef, dir, rev string) (*provider.DirListing, error) {
	return c.g.runner.ListDir(ctx, c.g.co, rev, dir)
}

// refusingReader fails the test on any use: the cache must answer alone.
type refusingReader struct{ t *testing.T }

func (r refusingReader) GetFileAt(context.Context, provider.PRRef, string, string) ([]byte, error) {
	r.t.Error("GetFileAt was called although the cache holds both commits")
	return nil, provider.ErrUpstream
}

func (r refusingReader) ListDir(context.Context, provider.PRRef, string, string) (*provider.DirListing, error) {
	r.t.Error("ListDir was called although the cache holds both commits")
	return nil, provider.ErrUpstream
}

// realHistory is the pull request of the real-repository cases. The
// returned files are the diff's listed files at the head; want is the
// outcome since the last reviewed commit.
func realHistory(g *gitRepo) (last, head string, files []provider.FilePatch, want map[string]State) {
	g.commit(map[string]string{
		"keep.go": "package keep\n", "edit.go": "package edit\n", "revert.go": "package revert\n",
		"old name.go": "package renamed\n", "dir with space/ünï.go": "package unicode\n", "ünï stays.go": "package stays\n",
		"deleted-before.go": "package d\n", "deleted-since.go": "package d\n", "orig.go": "package orig\n",
	})
	// The pull request's first push, which the last review saw.
	last = g.commit(map[string]string{
		"keep.go": "package keep // pr\n", "edit.go": "package edit // pr\n", "revert.go": "package revert // pr\n",
		"ünï stays.go": "package stays // pr\n", "moved.go": "package orig\n",
	}, "deleted-before.go", "orig.go")
	g.commit(map[string]string{
		"edit.go": "package edit // edited since\n", "revert.go": "package revert // for a while\n",
		"new name.go": "package renamed\n", "dir with space/ünï.go": "package unicode // edited\n",
	}, "old name.go", "deleted-since.go")
	head = g.commit(map[string]string{"revert.go": "package revert // pr\n"})
	files = []provider.FilePatch{
		modified("keep.go"), modified("edit.go"), modified("revert.go"), modified("ünï stays.go"),
		modified("dir with space/ünï.go"), renamed("new name.go", "old name.go"), renamed("moved.go", "orig.go"),
		deleted("deleted-before.go"), deleted("deleted-since.go"),
	}
	want = map[string]State{
		"keep.go":               Unchanged,
		"edit.go":               Changed,
		"revert.go":             Unchanged, // the same blob again
		"ünï stays.go":          Unchanged,
		"dir with space/ünï.go": Changed,
		"new name.go":           Changed, // renamed since the last review
		"moved.go":              Unchanged,
		"deleted-before.go":     Unchanged,
		"deleted-since.go":      Changed,
	}
	return last, head, files, want
}

// TestGitStrategyRealRepository: with both commits in the cache, one offline
// git diff decides, and the provider is never asked. The API strategy over
// the same repository reaches the same outcome.
func TestGitStrategyRealRepository(t *testing.T) {
	g := newGitRepo(t)
	last, head, files, want := realHistory(g)
	g.fetch(last, "refs/review-mcp/pr/7")
	g.fetch(head, "refs/review-mcp/pr/7") // the later fetch moves the ref; the old commit stays
	g.co.HeadSHA = head
	ctx := context.Background()

	res, err := Detect(ctx, Input{Ref: testRef, Files: files, Head: head, Last: last, Provider: refusingReader{t}, Local: g.runner, Checkout: g.co})
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != StrategyGit || res.Full {
		t.Fatalf("result = %+v, want the git strategy", res)
	}
	wantStates(t, res, want)

	api := &cacheReader{g: g}
	res, err = Detect(ctx, Input{Ref: testRef, Files: files, Head: head, Last: last, Provider: api})
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != StrategyAPI {
		t.Fatalf("result = %+v, want the API strategy", res)
	}
	wantStates(t, res, want)
	if api.reads.Load() == 0 {
		t.Error("the API strategy read nothing")
	}
}

// TestGitStrategyForcePush: the head was force-pushed (rebased onto another
// base) and the last reviewed commit is no ancestor of it, but the cache
// still holds it: the content decides.
func TestGitStrategyForcePush(t *testing.T) {
	g := newGitRepo(t)
	base := g.commit(map[string]string{"same.go": "v0\n", "rebased.go": "v0\n", "other.go": "v0\n"})
	last := g.commit(map[string]string{"same.go": "v1\n", "rebased.go": "v1\n"})
	g.git(g.src, "checkout", "-q", "-b", "rewritten", base)
	g.commit(map[string]string{"other.go": "upstream change\n"})
	head := g.commit(map[string]string{"same.go": "v1\n", "rebased.go": "v2\n"})
	g.fetch(last, "refs/review-mcp/pr/7")
	g.fetch(head, "refs/review-mcp/pr/7")
	g.co.HeadSHA = head

	files := []provider.FilePatch{modified("same.go"), modified("rebased.go")}
	res, err := Detect(context.Background(), Input{Ref: testRef, Files: files, Head: head, Last: last, Provider: refusingReader{t}, Local: g.runner, Checkout: g.co})
	if err != nil || res.Strategy != StrategyGit {
		t.Fatalf("result = %+v, %v, want the git strategy", res, err)
	}
	wantStates(t, res, map[string]State{"same.go": Unchanged, "rebased.go": Changed})
}

// TestGitStrategyLacksCommit: a cache that does not hold the last reviewed
// commit (an earlier fetch never brought it in) is not used; the provider
// compares instead.
func TestGitStrategyLacksCommit(t *testing.T) {
	g := newGitRepo(t)
	g.commit(map[string]string{"a.go": "v0\n", "b.go": "v0\n"})
	last := g.commit(map[string]string{"a.go": "v1\n"})
	g.git(g.src, "checkout", "-q", "-b", "rewritten", "HEAD~1")
	head := g.commit(map[string]string{"a.go": "v1\n", "b.go": "v2\n"})
	g.fetch(head, "refs/review-mcp/pr/7")
	g.co.HeadSHA = head
	ctx := context.Background()
	if g.runner.HasCommit(ctx, g.co, last) {
		t.Fatal("the cache holds the last commit; the fixture does not test what it should")
	}

	r := &fakeReader{files: map[string]map[string]string{
		last: {"a.go": "v1\n", "b.go": "v0\n"},
		head: {"a.go": "v1\n", "b.go": "v2\n"},
	}}
	files := []provider.FilePatch{modified("a.go"), modified("b.go")}
	res, err := Detect(ctx, Input{Ref: testRef, Files: files, Head: head, Last: last, Provider: r, Local: g.runner, Checkout: g.co})
	if err != nil || res.Strategy != StrategyAPI {
		t.Fatalf("result = %+v, %v, want the API strategy", res, err)
	}
	wantStates(t, res, map[string]State{"a.go": Unchanged, "b.go": Changed})
	if r.lists != 1 {
		t.Errorf("%d probes of the last commit through the provider, want 1", r.lists)
	}
}
