package gitctx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// TestReadViewShallowHistory (WP-15 §2 item 3): a checkout as Ensure leaves
// it (--depth=1, partial) behaves through the read view exactly as it does
// itself: a walk from the head commit stops at the shallow boundary, the
// parent missing there is no error, a comparison with that parent fails as
// before, and nothing is fetched or written into the checkout (its shallow
// file and objects stay as they were).
func TestReadViewShallowHistory(t *testing.T) {
	resetSchemes()
	s := newGitServerFiles(t, provider.KindGitea, map[string]string{
		"a.go":    "package a\n",
		"big.txt": strings.Repeat("x", 1<<20+1024) + "\n", // over fetchFilter
	})
	first := s.head
	writeFiles(t, s.src, map[string]string{"b.go": "package a\n"})
	gitOut(t, s.src, s.gitPath, "add", ".")
	gitOut(t, s.src, s.gitPath, "commit", "-q", "-m", "second")
	gitOut(t, s.src, s.gitPath, "push", "-q", s.bare, "HEAD:refs/pull/7/head")
	head := strings.TrimSpace(gitOut(t, s.src, s.gitPath, "rev-parse", "HEAD"))

	r, _ := realRunner(t, Options{})
	ctx := context.Background()
	co, err := r.Ensure(ctx, giteaRepo(s.base), PR{Number: 7, HeadSHA: head})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	shallowPath := filepath.Join(co.GitDir, "shallow")
	shallow, err := os.ReadFile(shallowPath) //nolint:gosec // G304: the test's own checkout
	if err != nil || !strings.Contains(string(shallow), head) {
		t.Fatalf("the checkout is not shallow at the head (%q, %v); the fixture does not test what it should", shallow, err)
	}
	if r.HasCommit(ctx, co, first) {
		t.Fatal("the checkout holds the parent; the fixture does not test what it should")
	}
	objects := objectFiles(t, co.GitDir)
	s.requests()

	g, err := r.openCheckout(co)
	if err != nil {
		t.Fatal(err)
	}
	if g.view == "" {
		t.Fatal("openCheckout set no read view")
	}
	// The same command against the checkout itself (no view): the reference.
	direct := &gitRun{path: g.path, gitDir: g.gitDir, dir: g.dir, home: g.home}
	for _, args := range [][]string{
		{"rev-list", "--objects", "--missing=print", head},
		{"rev-list", "--parents", head},
		{"ls-tree", "-r", "-z", head},
		{"diff", "--name-only", "-z", head + "^{tree}", head},
	} {
		viewed := g.run(ctx, gitCmd{args: args, offline: true})
		want := direct.run(ctx, gitCmd{args: args, offline: true})
		if want.err != nil {
			t.Fatalf("git %s on the checkout itself: %v (%s)", args[0], want.err, want.stderr)
		}
		if viewed.err != nil || string(viewed.stdout) != string(want.stdout) {
			t.Errorf("git %v through the view = %q, %v (%s); the checkout says %q", args, viewed.stdout, viewed.err, viewed.stderr, want.stdout)
		}
	}
	// The boundary: the head has no parent here, and the big blob is the
	// only missing object (not the parent commit as well).
	if res := g.run(ctx, gitCmd{args: []string{"rev-list", "--parents", head}, offline: true}); strings.TrimSpace(string(res.stdout)) != head {
		t.Errorf("rev-list --parents through the view = %q, want the head alone", res.stdout)
	}
	if res := g.run(ctx, gitCmd{args: []string{"rev-list", "--objects", "--missing=print", head}, offline: true}); strings.Count(string(res.stdout), "?") != 1 {
		t.Errorf("rev-list --missing=print through the view: %d missing objects, want 1 (the big blob)", strings.Count(string(res.stdout), "?"))
	}
	if got, err := r.ChangedPaths(ctx, co, head, head); err != nil || len(got) != 0 {
		t.Errorf("ChangedPaths(head, head) = %q, %v", got, err)
	}
	var ge *Error
	if _, err := r.ChangedPaths(ctx, co, first, head); !errors.As(err, &ge) {
		t.Errorf("ChangedPaths(parent, head) error = %v, want a gitctx error (the parent is past the boundary)", err)
	}
	if got, err := r.ListPaths(ctx, co, head); err != nil || !slices.Equal(got, []string{"a.go", "b.go", "big.txt"}) {
		t.Errorf("ListPaths = %q, %v", got, err)
	}

	if reqs := s.requests(); len(reqs) != 0 {
		t.Errorf("the reads sent %d requests to the git server", len(reqs))
	}
	if got, err := os.ReadFile(shallowPath); err != nil || string(got) != string(shallow) { //nolint:gosec // G304: the test's own checkout
		t.Errorf("the checkout's shallow file changed: %q, %v", got, err)
	}
	if got := objectFiles(t, co.GitDir); !slices.Equal(got, objects) {
		t.Errorf("the checkout's objects changed: %d files, was %d", len(got), len(objects))
	}
}

// TestReadViewFiles: the view is a real directory holding exactly HEAD, a
// configuration with no promisor, and empty objects and refs; anything else
// found at its place is replaced (a link as itself, never its target), and
// no temporary directory is left behind.
func TestReadViewFiles(t *testing.T) {
	root := t.TempDir()
	v, err := ensureView(root, "sha1")
	if err != nil || v != filepath.Join(root, ".view-sha1") || !viewOK(v, "sha1") {
		t.Fatalf("ensureView = %q, %v", v, err)
	}
	cfg, _ := os.ReadFile(filepath.Join(v, "config")) //nolint:gosec // G304: the test's own view
	for _, bad := range []string{"partialclone", "promisor", "remote", "hookspath"} {
		if strings.Contains(strings.ToLower(string(cfg)), bad) {
			t.Errorf("the view's config names %s:\n%s", bad, cfg)
		}
	}
	if v256, err := ensureView(root, "sha256"); err != nil || !viewOK(v256, "sha256") {
		t.Fatalf("ensureView(sha256) = %q, %v", v256, err)
	} else if b, _ := os.ReadFile(filepath.Join(v256, "config")); !strings.Contains(string(b), "objectformat = sha256") { //nolint:gosec // G304: the test's own view
		t.Errorf("the sha256 view's config:\n%s", b)
	}
	if _, err := ensureView(root, "md5"); ReasonOf(err) != ReasonCache {
		t.Errorf("ensureView(md5) = %v, want cache_unusable", err)
	}

	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, spoil := range map[string]func() error{
		"commondir": func() error { return os.WriteFile(filepath.Join(v, "commondir"), []byte(outside+"\n"), 0o600) },
		"promisor config": func() error {
			return os.WriteFile(filepath.Join(v, "config"), []byte(viewConfig["sha1"]+"[extensions]\n\tpartialClone = origin\n"), 0o600)
		},
		"alternates": func() error {
			if err := os.MkdirAll(filepath.Join(v, "objects", "info"), 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(v, "objects", "info", "alternates"), []byte(outside+"\n"), 0o600)
		},
		"a file": func() error {
			if err := os.RemoveAll(v); err != nil {
				return err
			}
			return os.WriteFile(v, []byte("x"), 0o600)
		},
		"a link": func() error {
			if err := os.RemoveAll(v); err != nil {
				return err
			}
			return os.Symlink(outside, v)
		},
	} {
		if err := spoil(); err != nil {
			if name == "a link" {
				t.Logf("%s: %v (no symbolic links here)", name, err)
				continue
			}
			t.Fatalf("%s: %v", name, err)
		}
		if got, err := ensureView(root, "sha1"); err != nil || got != v || !viewOK(v, "sha1") {
			t.Errorf("%s: ensureView = %q, %v; the view was not rebuilt", name, got, err)
		}
		checkViewFiles(t, name, v, "sha1")
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s: a file outside the cache was touched: %v", name, err)
		}
	}
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("a temporary directory is left behind: %s", e.Name())
		}
	}
}

// checkViewFiles reads the view's HEAD and config itself, not through
// viewOK, so a viewOK that stops checking them is caught.
func checkViewFiles(t *testing.T, name, v, format string) {
	t.Helper()
	for file, want := range map[string]string{"HEAD": viewHead, "config": viewConfig[format]} {
		if b, err := os.ReadFile(filepath.Join(v, file)); err != nil || string(b) != want { //nolint:gosec // G304: the test's own view
			t.Errorf("%s: the view's %s = %q, %v; want %q", name, file, b, err, want)
		}
	}
}

// TestReadViewSHA256: a checkout in the sha256 object format is read
// through the sha256 view: a present blob is read, a path that is not
// there is not_found.
func TestReadViewSHA256(t *testing.T) {
	gitPath, _ := realGit(t)
	isolateHome(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	gitOut(t, tmp, gitPath, "init", "-q", "--object-format=sha256", src)
	writeFiles(t, src, map[string]string{"a.go": "package a\n"})
	gitOut(t, src, gitPath, "add", "-A")
	gitOut(t, src, gitPath, "commit", "-q", "-m", "first")
	sha := strings.TrimSpace(gitOut(t, src, gitPath, "rev-parse", "HEAD"))
	if len(sha) != 64 {
		t.Fatalf("head %q is not a sha256 commit", sha)
	}
	r, _ := realRunner(t, Options{})
	root, err := r.openRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "host.example", "_", "ns", "repo")
	if err := os.MkdirAll(entry, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(entry, gitDirName)
	gitOut(t, tmp, gitPath, "init", "-q", "--bare", "--object-format=sha256", gitDir)
	gitOut(t, src, gitPath, "push", "-q", gitDir, "HEAD:refs/review-mcp/pr/1")
	co := Checkout{GitDir: gitDir, HeadSHA: sha, Ref: "refs/review-mcp/pr/1"}

	ctx := context.Background()
	if got, err := r.FileAt(ctx, co, sha, "a.go", 1<<20); err != nil || string(got) != "package a\n" {
		t.Errorf("FileAt(a.go) = %q, %v", got, err)
	}
	var pe *provider.Error
	if _, err := r.FileAt(ctx, co, sha, "nope.go", 1<<20); !errors.As(err, &pe) || pe.Class != provider.ClassNotFound {
		t.Errorf("FileAt(nope.go) = %v, want not_found", err)
	}
	if !viewOK(filepath.Join(root, ".view-sha256"), "sha256") {
		t.Error("the read did not go through the sha256 view")
	}
}

// TestReadViewLocalGit (WP-15 §2 item 8): a read through the view with the
// local git and no http-backend, so that it runs on Windows too (where the
// http-backend tests skip themselves): of a partial clone over file://, a
// present blob is read and a missing one is blob_missing, and no object is
// added to the checkout.
func TestReadViewLocalGit(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	if _, ok := parseGitVersion(gitOut(t, "", gitPath, "--version")); !ok {
		t.Skip("git " + MinGitVersion + " or later is required")
	}
	isolateHome(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	gitOut(t, tmp, gitPath, "init", "-q", src)
	writeFiles(t, src, map[string]string{"small.txt": "s\n", "big.txt": strings.Repeat("x", 3000) + "\n"})
	gitOut(t, src, gitPath, "add", "-A")
	gitOut(t, src, gitPath, "commit", "-q", "-m", "first")
	gitOut(t, src, gitPath, "config", "uploadpack.allowFilter", "true")
	sha := strings.TrimSpace(gitOut(t, src, gitPath, "rev-parse", "HEAD"))

	r := New(Options{CacheDir: filepath.Join(tmp, "cache"), GitPath: gitPath})
	root, err := r.openRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "host.example", "_", "ns", "repo")
	if err := os.MkdirAll(entry, 0o700); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(entry, gitDirName)
	srcURL := "file://" + filepath.ToSlash(src)
	if !strings.HasPrefix(srcURL, "file:///") {
		srcURL = "file:///" + filepath.ToSlash(src) // C:/... on Windows
	}
	gitOut(t, tmp, gitPath, "clone", "-q", "--bare", "--filter=blob:limit=1k", srcURL, gitDir)
	co := Checkout{GitDir: gitDir, HeadSHA: sha, Ref: "refs/review-mcp/pr/1"}
	objects := objectFiles(t, gitDir)

	ctx := context.Background()
	if got, err := r.FileAt(ctx, co, sha, "small.txt", 1<<20); err != nil || strings.TrimRight(string(got), "\r\n") != "s" {
		t.Errorf("FileAt(small.txt) = %q, %v", got, err)
	}
	if _, err := r.FileAt(ctx, co, sha, "big.txt", 1<<20); ReasonOf(err) != ReasonBlobMissing {
		t.Errorf("FileAt(big.txt) = %v (reason %q), want blob_missing", err, ReasonOf(err))
	}
	if !viewOK(filepath.Join(root, ".view-sha1"), "sha1") {
		t.Error("the reads did not leave a valid view")
	}
	if got := objectFiles(t, gitDir); !slices.Equal(got, objects) {
		t.Errorf("the checkout's objects changed: %d files, was %d", len(got), len(objects))
	}
}

// TestStaleViewTempDirs: ensureView removes a temporary view directory older
// than an hour, keeps a younger one (another process may be building it), and
// removes a link named like one as a link, never its target.
func TestStaleViewTempDirs(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(p, "objects"), 0o700); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := mk(".view-sha1.tmp111", 2*time.Hour)
	old256 := mk(".view-sha256.tmp222", 90*time.Minute)
	young := mk(".view-sha1.tmp333", 5*time.Minute)
	other := mk("a-repo-dir.tmp444", 2*time.Hour)

	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".view-sha1.tmp555")
	linked := os.Symlink(outside, link) == nil

	if _, err := ensureView(root, "sha1"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{old, old256} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: an old temporary view directory was kept (%v)", filepath.Base(p), err)
		}
	}
	for _, p := range []string{young, other} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s: removed although it is young or not a view directory: %v", filepath.Base(p), err)
		}
	}
	if linked {
		if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the link was kept (%v)", err)
		}
		if b, err := os.ReadFile(keep); err != nil || string(b) != "x" { //nolint:gosec // G304: a file the test made under t.TempDir()
			t.Errorf("the link's target was touched: %q, %v", b, err)
		}
	} else {
		t.Log("no symbolic links here")
	}
}
