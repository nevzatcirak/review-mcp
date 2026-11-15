package gitctx

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// ---- helpers ----

// localGit returns the system git for the local repository tests, or
// skips. Unlike realGit it runs on Windows too: no http-backend is needed.
func localGit(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; the local repository tests need it")
	}
	if _, ok := parseGitVersion(gitOut(t, "", p, "--version")); !ok {
		t.Skip("git " + MinGitVersion + " or later is required for the local repository tests")
	}
	return p
}

// localRepo is a synthetic work tree for the tests: two commits on main
// ("base", then "feature work"), the second one on the branch feat.
type localRepo struct {
	git, dir      string
	base, feature string
}

func newLocalRepo(t *testing.T) *localRepo {
	t.Helper()
	g := localGit(t)
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	r := &localRepo{git: g, dir: dir}
	r.run(t, "init", "-q", "-b", "main")
	r.run(t, "config", "core.autocrlf", "false")
	writeFiles(t, dir, map[string]string{
		"README.md":      "# demo\n",
		"src/app.go":     "package app\n\nfunc Run() int { return 1 }\n",
		"src/util.go":    "package app\n\nfunc helper() {}\n",
		"docs/guide.txt": "line one\nline two\n",
		".gitignore":     "*.log\n",
	})
	r.run(t, "add", "-A")
	r.run(t, "commit", "-q", "-m", "base")
	r.base = r.rev(t, "HEAD")
	r.run(t, "checkout", "-q", "-b", "feat")
	writeFiles(t, dir, map[string]string{"src/app.go": "package app\n\nfunc Run() int { return 2 }\n"})
	r.run(t, "commit", "-q", "-am", "feature work\n\nWith a body line.")
	r.feature = r.rev(t, "HEAD")
	return r
}

func (r *localRepo) run(t *testing.T, args ...string) string {
	t.Helper()
	return gitOut(t, r.dir, r.git, args...)
}

func (r *localRepo) rev(t *testing.T, rev string) string {
	t.Helper()
	return strings.TrimSpace(r.run(t, "rev-parse", rev))
}

func (r *localRepo) open(t *testing.T, opts LocalOptions) *Local {
	t.Helper()
	opts.GitPath = r.git
	l, err := OpenLocal(t.Context(), opts, r.dir)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// recordSubcommands records the subcommand of every local git command for
// the rest of the test.
func recordSubcommands(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	testLocalHook = func(args []string) {
		mu.Lock()
		got = append(got, subcommandOf(args))
		mu.Unlock()
	}
	t.Cleanup(func() { testLocalHook = nil })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// ---- opening ----

func TestOpenLocal(t *testing.T) {
	r := newLocalRepo(t)
	l := r.open(t, LocalOptions{})
	if !samePath(l.Top(), r.dir) {
		t.Errorf("Top %q, want %q", l.Top(), r.dir)
	}
	if l.HeadSHA() != r.feature || l.Branch() != "refs/heads/feat" {
		t.Errorf("HeadSHA %q Branch %q, want %q refs/heads/feat", l.HeadSHA(), l.Branch(), r.feature)
	}
	// A subdirectory means the top level.
	sub, err := OpenLocal(t.Context(), LocalOptions{GitPath: r.git}, filepath.Join(r.dir, "src"))
	if err != nil {
		t.Fatalf("OpenLocal(subdirectory): %v", err)
	}
	defer func() { _ = sub.Close() }()
	if !samePath(sub.Top(), r.dir) {
		t.Errorf("Top of a subdirectory %q, want %q", sub.Top(), r.dir)
	}
	// Close removes the temporary directory and nothing else.
	tmp := sub.tmp
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the temporary directory is still there after Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "HEAD")); err != nil {
		t.Errorf("the repository was touched by Close: %v", err)
	}
}

func TestOpenLocalRefusals(t *testing.T) {
	r := newLocalRepo(t)
	g := r.git
	file := filepath.Join(r.dir, "README.md")
	bare := filepath.Join(t.TempDir(), "bare.git")
	gitOut(t, "", g, "init", "-q", "--bare", bare)
	unborn := t.TempDir()
	gitOut(t, unborn, g, "init", "-q", "-b", "main")
	plain := t.TempDir()
	for _, tc := range []struct {
		name, path, reason string
	}{
		{"relative", "src", LocalNotAbsolute},
		{"empty", "", LocalNotAbsolute},
		{"missing", filepath.Join(r.dir, "nope"), LocalNotFound},
		{"file", file, LocalNotFound},
		{"bare", bare, LocalBare},
		{"git directory", filepath.Join(r.dir, ".git"), LocalNotRepository},
		{"unborn", unborn, LocalUnbornHead},
		{"not a repository", plain, LocalNotRepository},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := OpenLocal(t.Context(), LocalOptions{GitPath: g}, tc.path)
			if err == nil {
				_ = l.Close()
				t.Fatalf("OpenLocal(%s) accepted it", tc.name)
			}
			if !errors.Is(err, &LocalError{Reason: tc.reason}) {
				t.Errorf("OpenLocal(%s) = %v, want reason %s", tc.name, err, tc.reason)
			}
			if strings.Contains(err.Error(), r.dir) || strings.Contains(err.Error(), "fatal") {
				t.Errorf("the error %q names a path or carries git's text", err)
			}
		})
	}
}

// TestOpenLocalNotARepositoryAboveRoot: with allowed roots, git does not
// look for a repository above the root (GIT_CEILING_DIRECTORIES), so a
// plain directory under the root inside a larger work tree is not taken
// for that work tree.
func TestOpenLocalNotARepositoryAboveRoot(t *testing.T) {
	r := newLocalRepo(t)
	root := filepath.Join(r.dir, "area")
	if err := os.MkdirAll(filepath.Join(root, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := OpenLocal(t.Context(), LocalOptions{GitPath: r.git, AllowedRoots: []string{root}}, filepath.Join(root, "inner"))
	if !errors.Is(err, &LocalError{Reason: LocalNotRepository}) {
		t.Errorf("OpenLocal under a root inside a larger work tree = %v, want not_a_repository", err)
	}
	// Without roots, the same directory means the larger work tree.
	l, err := OpenLocal(t.Context(), LocalOptions{GitPath: r.git}, filepath.Join(root, "inner"))
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
}

// TestOpenLocalAllowedRoots is canary (f): a path outside local.allowed_roots,
// directly or through a symbolic link that points outside, is refused
// before any git process is started; a path inside, also through a link
// that points inside, is accepted.
// Mutation: check the path against the roots before resolving symbolic
// links; the link that points outside is then accepted.
func TestOpenLocalAllowedRoots(t *testing.T) {
	r := newLocalRepo(t)
	allowed := t.TempDir()
	if real, err := filepath.EvalSymlinks(allowed); err == nil {
		allowed = real
	}
	inside := filepath.Join(allowed, "repo")
	gitOut(t, "", r.git, "clone", "-q", "--no-local", r.dir, inside)
	linkOut := filepath.Join(allowed, "link-out")
	linkIn := filepath.Join(t.TempDir(), "link-in")
	if err := os.Symlink(r.dir, linkOut); err != nil {
		t.Skip("symbolic links cannot be created here: " + err.Error())
	}
	if err := os.Symlink(inside, linkIn); err != nil {
		t.Fatal(err)
	}
	calls := recordSubcommands(t)
	for _, p := range []string{r.dir, linkOut, filepath.Join(linkOut, "src")} {
		l, err := OpenLocal(t.Context(), LocalOptions{GitPath: r.git, AllowedRoots: []string{allowed}}, p)
		if err == nil {
			_ = l.Close()
			t.Errorf("OpenLocal(%s) outside the roots was accepted", filepath.Base(p))
			continue
		}
		if !errors.Is(err, &LocalError{Reason: LocalOutsideRoots}) {
			t.Errorf("OpenLocal(%s) = %v, want outside_allowed_roots", filepath.Base(p), err)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("git ran before the refusal: %v", got)
	}
	for _, p := range []string{inside, linkIn} {
		l, err := OpenLocal(t.Context(), LocalOptions{GitPath: r.git, AllowedRoots: []string{allowed}}, p)
		if err != nil {
			t.Errorf("OpenLocal(%s) inside the roots: %v", filepath.Base(p), err)
			continue
		}
		_ = l.Close()
	}
}

func TestOpenLocalLinkedWorktree(t *testing.T) {
	r := newLocalRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	r.run(t, "worktree", "add", "-q", "-b", "other", wt, r.base)
	writeFiles(t, wt, map[string]string{"README.md": "# demo\nchanged in the worktree\n"})
	l, err := OpenLocal(t.Context(), LocalOptions{GitPath: r.git}, wt)
	if err != nil {
		t.Fatalf("OpenLocal(worktree): %v", err)
	}
	defer func() { _ = l.Close() }()
	if l.HeadSHA() != r.base || l.Branch() != "refs/heads/other" {
		t.Errorf("worktree HEAD %q %q", l.HeadSHA(), l.Branch())
	}
	out, err := l.Diff(t.Context(), r.base, "", true, 1<<20)
	if err != nil {
		t.Fatalf("Diff(worktree): %v", err)
	}
	if !strings.Contains(string(out), "+changed in the worktree") || strings.Contains(string(out), "return 2") {
		t.Errorf("the worktree diff is not the worktree's own:\n%s", out)
	}
}

// ---- reads ----

func TestLocalReads(t *testing.T) {
	r := newLocalRepo(t)
	l := r.open(t, LocalOptions{})
	ctx := t.Context()

	if sha, ok, err := l.ResolveCommit(ctx, "main"); err != nil || !ok || sha != r.base {
		t.Errorf("ResolveCommit(main) = %q %v %v", sha, ok, err)
	}
	if sha, ok, err := l.ResolveCommit(ctx, r.base[:10]); err != nil || !ok || sha != r.base {
		t.Errorf("ResolveCommit(short) = %q %v %v", sha, ok, err)
	}
	for _, bad := range []string{"nope", "--output=x", "HEAD~1", "main..feat", "a b"} {
		if _, ok, err := l.ResolveCommit(ctx, bad); ok || err != nil {
			t.Errorf("ResolveCommit(%q) = %v, %v; want not found", bad, ok, err)
		}
	}
	if mb, ok, err := l.MergeBase(ctx, r.base, r.feature); err != nil || !ok || mb != r.base {
		t.Errorf("MergeBase = %q %v %v", mb, ok, err)
	}
	cs, more, err := l.Commits(ctx, r.base, r.feature, 20)
	if err != nil || more || len(cs) != 1 || cs[0].Subject() != "feature work" || !strings.Contains(cs[0].Message, "With a body line.") {
		t.Errorf("Commits = %+v %v %v", cs, more, err)
	}
	for rev, want := range map[string]string{"main": "refs/heads/main", "HEAD": "refs/heads/feat", r.base: "", "nope": ""} {
		if got, err := l.FullRefName(ctx, rev); err != nil || got != want {
			t.Errorf("FullRefName(%q) = %q %v, want %q", rev, got, err, want)
		}
	}
	if up, err := l.Upstream(ctx); err != nil || up != "" {
		t.Errorf("Upstream without one = %q %v", up, err)
	}
	r.run(t, "branch", "-q", "--set-upstream-to=main")
	if up, err := l.Upstream(ctx); err != nil || up != "refs/heads/main" {
		t.Errorf("Upstream = %q %v", up, err)
	}

	b, err := l.FileAt(ctx, r.base, "src/app.go", 1<<20)
	if err != nil || !strings.Contains(string(b), "return 1") {
		t.Errorf("FileAt(base) = %q %v", b, err)
	}
	if _, err := l.FileAt(ctx, r.base, "src/app.go", 5); !errors.Is(err, provider.ErrTooLarge) {
		t.Errorf("FileAt over the cap = %v, want too_large", err)
	}
	if _, err := l.FileAt(ctx, r.base, "src", 1<<20); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("FileAt(directory) = %v, want not_found", err)
	}
	ls, err := l.ListDir(ctx, r.feature, "src")
	if err != nil || len(ls.Entries) != 2 || ls.Entries[0].Name != "app.go" {
		t.Errorf("ListDir(src) = %+v %v", ls, err)
	}
	if _, err := l.ListDir(ctx, r.feature, "nope"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("ListDir(missing) = %v", err)
	}

	writeFiles(t, r.dir, map[string]string{"src/app.go": "package app\n\nfunc Run() int { return 3 }\n"})
	if b, err := l.WorkTreeFile("src/app.go", 1<<20); err != nil || !strings.Contains(string(b), "return 3") {
		t.Errorf("WorkTreeFile = %q %v", b, err)
	}
	for _, bad := range []string{"../x", "/etc/passwd", "src", "nope", ""} {
		if _, err := l.WorkTreeFile(bad, 1<<20); !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("WorkTreeFile(%q) = %v, want not_found", bad, err)
		}
	}
}

// TestLocalWorkTreeFileLinks: a symbolic link in the work tree yields its
// target as text, and a link whose target lies outside the work tree is
// never followed.
func TestLocalWorkTreeFileLinks(t *testing.T) {
	r := newLocalRepo(t)
	l := r.open(t, LocalOptions{})
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(r.dir, "link.txt")); err != nil {
		t.Skip("symbolic links cannot be created here: " + err.Error())
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(r.dir, "dirlink")); err != nil {
		t.Fatal(err)
	}
	b, err := l.WorkTreeFile("link.txt", 1<<20)
	if err != nil || strings.Contains(string(b), "outside content") {
		t.Errorf("WorkTreeFile(link) = %q %v; want the link target, not the content", b, err)
	}
	if b, err := l.WorkTreeFile("dirlink/secret.txt", 1<<20); err == nil {
		t.Errorf("WorkTreeFile through a directory link out of the work tree = %q", b)
	}
}

func TestLocalDiff(t *testing.T) {
	r := newLocalRepo(t)
	l := r.open(t, LocalOptions{})
	ctx := t.Context()
	out, err := l.Diff(ctx, r.base, r.feature, false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "diff --git a/src/app.go b/src/app.go") || !strings.Contains(string(out), "+func Run() int { return 2 }") {
		t.Errorf("commit diff:\n%s", out)
	}
	// Staged and unstaged edits, a staged new file and a deletion.
	writeFiles(t, r.dir, map[string]string{"src/util.go": "package app\n\nfunc helper() { _ = 1 }\n", "src/new.go": "package app\n"})
	r.run(t, "add", "src/new.go")
	writeFiles(t, r.dir, map[string]string{"README.md": "# demo\nstaged\n"})
	r.run(t, "add", "README.md")
	if err := os.Remove(filepath.Join(r.dir, "docs", "guide.txt")); err != nil {
		t.Fatal(err)
	}
	out, err = l.Diff(ctx, r.base, "", true, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+func Run() int { return 2 }", "+func helper() { _ = 1 }", "new file mode", "b/src/new.go",
		"+staged", "deleted file mode"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the work-tree diff lacks %q:\n%s", want, out)
		}
	}
	if _, err := l.Diff(ctx, r.base, "", true, 10); !errors.Is(err, &LocalError{Reason: LocalTooLarge}) {
		t.Errorf("Diff over the cap = %v, want too_large", err)
	}
}

// TestLocalDiffLineEndings: a work tree checked out with CRLF (Windows,
// core.autocrlf=true) is not a change against blobs stored with LF, and a
// file whose blob has CRLF stays as it is (Q11).
func TestLocalDiffLineEndings(t *testing.T) {
	r := newLocalRepo(t)
	writeFiles(t, r.dir, map[string]string{"crlf.txt": "one\r\ntwo\r\n"})
	r.run(t, "add", "crlf.txt")
	r.run(t, "commit", "-q", "-m", "a CRLF file")
	head := r.rev(t, "HEAD")
	// Check out with CRLF, as Git for Windows does, with a time unlike the
	// index's so that git reads the files.
	old := time.Now().Add(-48 * time.Hour)
	for name, content := range map[string]string{
		"src/app.go": "package app\r\n\r\nfunc Run() int { return 2 }\r\n",
		"crlf.txt":   "one\r\ntwo\r\n",
	} {
		p := filepath.Join(r.dir, filepath.FromSlash(name))
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	l := r.open(t, LocalOptions{})
	out, err := l.Diff(t.Context(), head, "", true, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(out)) != 0 {
		t.Errorf("line endings alone show as changes:\n%s", out)
	}
}

func TestLocalUntracked(t *testing.T) {
	r := newLocalRepo(t)
	writeFiles(t, r.dir, map[string]string{"notes/todo.md": "x\n", "debug.log": "ignored\n", "excluded.txt": "x\n"})
	writeFiles(t, r.dir, map[string]string{".git/info/exclude": "excluded.txt\n"})
	l := r.open(t, LocalOptions{})
	got, err := l.Untracked(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"notes/todo.md"}) {
		t.Errorf("Untracked = %q, want only notes/todo.md", got)
	}
}

func TestLocalSubmoduleListedNotEntered(t *testing.T) {
	r := newLocalRepo(t)
	sub := newLocalRepo(t)
	r.run(t, "update-index", "--add", "--cacheinfo", "160000,"+sub.base+",lib")
	r.run(t, "commit", "-q", "-m", "add a submodule pointer")
	head := r.rev(t, "HEAD")
	// The submodule's directory holds a repository whose own configuration
	// would run something if git entered it.
	marker := filepath.Join(t.TempDir(), "sub-marker")
	gitOut(t, "", r.git, "clone", "-q", "--no-local", sub.dir, filepath.Join(r.dir, "lib"))
	gitOut(t, filepath.Join(r.dir, "lib"), r.git, "config", "core.fsmonitor", trapScript(t, marker, "fsmonitor"))
	writeFiles(t, filepath.Join(r.dir, "lib"), map[string]string{"README.md": "dirty submodule\n"})
	l := r.open(t, LocalOptions{})
	out, err := l.Diff(t.Context(), r.base, "", true, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "+Subproject commit "+sub.feature) {
		t.Errorf("the submodule pointer is not listed:\n%s", out)
	}
	if _, err := l.Diff(t.Context(), r.base, head, false, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("git entered the submodule and ran its fsmonitor")
	}
}

func TestLocalNoMergeBase(t *testing.T) {
	r := newLocalRepo(t)
	r.run(t, "checkout", "-q", "--orphan", "lonely")
	r.run(t, "commit", "-q", "-m", "unrelated")
	lonely := r.rev(t, "HEAD")
	l := r.open(t, LocalOptions{})
	if mb, ok, err := l.MergeBase(t.Context(), r.base, lonely); ok || err != nil {
		t.Errorf("MergeBase of unrelated histories = %q %v %v", mb, ok, err)
	}
}

// ---- R-1: the traps (canaries a to d) ----

// trapScript writes a shell script that creates marker when it runs, then
// does what kind needs to look harmless (print its input), and returns its
// path in the slash form git's shell accepts on every system.
func trapScript(t *testing.T, marker, kind string) string {
	t.Helper()
	m := filepath.ToSlash(marker)
	body := "#!/bin/sh\necho " + kind + " >> \"" + m + "\"\n"
	switch kind {
	case "textconv":
		body += "cat \"$1\"\n"
	case "clean", "smudge":
		body += "cat\n"
	}
	p := filepath.Join(t.TempDir(), kind+"-trap")
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil { //nolint:gosec // G306: the trap must be executable
		t.Fatal(err)
	}
	return filepath.ToSlash(p)
}

// trapRepo is a work tree that tries everything R-1 forbids: an fsmonitor,
// a textconv and a filter driver named by .gitattributes, and hooks in
// .git/hooks and in a core.hooksPath directory. Each writes to marker.
func trapRepo(t *testing.T) (*localRepo, string) {
	t.Helper()
	r := newLocalRepo(t)
	marker := filepath.Join(t.TempDir(), "marker")
	writeFiles(t, r.dir, map[string]string{".gitattributes": "*.go diff=trap filter=trap\n*.txt diff=trap\n"})
	r.run(t, "add", ".gitattributes")
	r.run(t, "commit", "-q", "-m", "attributes")
	r.run(t, "config", "core.fsmonitor", trapScript(t, marker, "fsmonitor"))
	r.run(t, "config", "diff.trap.textconv", trapScript(t, marker, "textconv"))
	r.run(t, "config", "filter.trap.clean", trapScript(t, marker, "clean"))
	r.run(t, "config", "filter.trap.smudge", trapScript(t, marker, "smudge"))
	r.run(t, "config", "filter.trap.required", "true")
	r.run(t, "config", "diff.external", trapScript(t, marker, "external"))
	hooks := filepath.Join(t.TempDir(), "hooks")
	for _, dir := range []string{filepath.Join(r.dir, ".git", "hooks"), hooks} {
		for _, h := range []string{"pre-commit", "post-checkout", "post-index-change", "reference-transaction", "post-merge", "pre-auto-gc"} {
			body := "#!/bin/sh\necho " + h + " >> \"" + filepath.ToSlash(marker) + "\"\n"
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, h), []byte(body), 0o700); err != nil { //nolint:gosec // G306: the trap must be executable
				t.Fatal(err)
			}
		}
	}
	r.run(t, "config", "core.hooksPath", filepath.ToSlash(hooks))
	if _, err := os.Stat(marker); err == nil {
		// The setup itself ran a trap (a commit runs hooks): start clean.
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
	}
	// Uncommitted changes, stat-dirty against the index so that git reads
	// the work tree, a file whose content is unchanged but whose time is
	// not (git refreshes the index for it), and an untracked file.
	old := time.Now().Add(-72 * time.Hour)
	writeFiles(t, r.dir, map[string]string{"src/app.go": "package app\n\nfunc Run() int { return 4 }\n", "src/extra.txt": "new\n"})
	for _, p := range []string{"src/app.go", "src/util.go", "docs/guide.txt"} {
		if err := os.Chtimes(filepath.Join(r.dir, filepath.FromSlash(p)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	return r, marker
}

// exerciseLocal runs every read of Local once, as a local review would.
func exerciseLocal(t *testing.T, l *Local, r *localRepo) {
	t.Helper()
	ctx := t.Context()
	head := l.HeadSHA()
	if _, _, err := l.ResolveCommit(ctx, "main"); err != nil {
		t.Errorf("ResolveCommit: %v", err)
	}
	if _, err := l.Upstream(ctx); err != nil {
		t.Errorf("Upstream: %v", err)
	}
	if _, _, err := l.MergeBase(ctx, r.base, head); err != nil {
		t.Errorf("MergeBase: %v", err)
	}
	if _, _, err := l.Commits(ctx, r.base, head, 20); err != nil {
		t.Errorf("Commits: %v", err)
	}
	if out, err := l.Diff(ctx, r.base, head, false, 1<<20); err != nil || !strings.Contains(string(out), "return 2") {
		t.Errorf("Diff(commits) = %v\n%s", err, out)
	}
	if out, err := l.Diff(ctx, r.base, "", true, 1<<20); err != nil || !strings.Contains(string(out), "return 4") {
		t.Errorf("Diff(work tree) = %v\n%s", err, out)
	}
	if _, err := l.Untracked(ctx); err != nil {
		t.Errorf("Untracked: %v", err)
	}
	if _, err := l.FileAt(ctx, head, "src/app.go", 1<<20); err != nil {
		t.Errorf("FileAt: %v", err)
	}
	if _, err := l.ListDir(ctx, head, "src"); err != nil {
		t.Errorf("ListDir: %v", err)
	}
	if _, err := l.WorkTreeFile("src/app.go", 1<<20); err != nil {
		t.Errorf("WorkTreeFile: %v", err)
	}
}

func readMarker(marker string) string {
	b, err := os.ReadFile(marker) //nolint:gosec // G304: the test's own marker file
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// TestLocalTrapsNeverRun covers canaries (a), (b) and (c): after every read
// a local review makes, no fsmonitor, textconv, filter, external diff or
// hook has run, and only allow-listed subcommands were started.
//
//   - (a) Mutation: drop "-c core.fsmonitor=false" (TestLocalTrapFSMonitor).
//   - (b) Mutation: drop --no-textconv from the diff of two commits
//     (TestLocalTrapTextconv).
//   - (c) Mutation: run checkout from a helper of this package; the
//     allow-list test fails (TestLocalSubcommandAllowList) and the
//     subcommand is refused here.
func TestLocalTrapsNeverRun(t *testing.T) {
	r, marker := trapRepo(t)
	calls := recordSubcommands(t)
	l := r.open(t, LocalOptions{})
	exerciseLocal(t, l, r)
	if m := readMarker(marker); m != "" {
		t.Errorf("a trap ran during the local reads: %q", m)
	}
	for _, s := range calls() {
		if !localSubcommands[s] {
			t.Errorf("the subcommand %q was started on the local repository", s)
		}
	}
	if l.Calls() == 0 {
		t.Error("no git process was counted")
	}
}

// TestLocalTrapFSMonitor is canary (a): the repository's core.fsmonitor
// would run on every read of the index (ls-files); -c core.fsmonitor=false
// stops it. The test also proves the trap works: with the setting
// dropped, the marker appears.
func TestLocalTrapFSMonitor(t *testing.T) {
	r, marker := trapRepo(t)
	l := r.open(t, LocalOptions{})
	if _, err := l.Untracked(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m := readMarker(marker); strings.Contains(m, "fsmonitor") {
		t.Fatalf("the fsmonitor ran: %q", m)
	}
}

// TestLocalTrapTextconv is canary (b): the diff of two commits runs in the
// repository, whose .gitattributes names a diff driver with a textconv;
// --no-textconv stops it.
func TestLocalTrapTextconv(t *testing.T) {
	r, marker := trapRepo(t)
	l := r.open(t, LocalOptions{})
	head := r.rev(t, "HEAD")
	if _, err := l.Diff(t.Context(), r.base, head, false, 1<<20); err != nil {
		t.Fatal(err)
	}
	if m := readMarker(marker); strings.Contains(m, "textconv") {
		t.Fatalf("the textconv ran: %q", m)
	}
}

// TestLocalTrapsArmed proves the traps of trapRepo work: git run plainly in
// the repository sets every one of them off. Without this, a trap that
// never fires would make the canaries above pass for nothing.
func TestLocalTrapsArmed(t *testing.T) {
	r, marker := trapRepo(t)
	if runtime.GOOS == "windows" {
		// Git for Windows runs these through its own shell, which the
		// runner has; the commands are the same.
		t.Log("Windows: the traps run through Git for Windows' sh")
	}
	head := r.rev(t, "HEAD")
	r.run(t, "ls-files", "-o")
	r.run(t, "diff", "--no-ext-diff", "--textconv", r.base, head)
	r.run(t, "diff", r.base) // the work tree: the clean filter and the external diff
	m := readMarker(marker)
	for _, want := range []string{"fsmonitor", "textconv", "clean", "external"} {
		if !strings.Contains(m, want) {
			t.Errorf("the %s trap did not fire when git ran plainly (marker %q)", want, m)
		}
	}
}

// TestLocalIndexUnchanged is canary (d): after a review with uncommitted
// changes the index file's modification time and content are unchanged,
// although git diff refreshes the index it reads (a stat-dirty file with
// unchanged content makes it write one), --no-optional-locks or not.
// Mutation: let the diff read the repository's own index instead of the
// private copy (copyIndex returns <git dir>/index).
func TestLocalIndexUnchanged(t *testing.T) {
	r, _ := trapRepo(t)
	idx := filepath.Join(r.dir, ".git", "index")
	before, err := os.ReadFile(idx) //nolint:gosec // G304: the test repository's index
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	// Make the index older than any write the review could make.
	old := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(idx, old, old); err != nil {
		t.Fatal(err)
	}
	l := r.open(t, LocalOptions{})
	exerciseLocal(t, l, r)
	after, err := os.ReadFile(idx) //nolint:gosec // G304: the test repository's index
	if err != nil {
		t.Fatal(err)
	}
	fi2, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the index content changed")
	}
	if !fi2.ModTime().Equal(old) || fi2.Size() != fi.Size() {
		t.Errorf("the index was rewritten: mtime %v, want %v", fi2.ModTime(), old)
	}
	if _, err := os.Stat(idx + ".lock"); err == nil {
		t.Error("an index.lock was left behind")
	}
}

// TestLocalRefusesUnlistedSubcommand: a command outside the allow-list is
// refused before git starts.
func TestLocalRefusesUnlistedSubcommand(t *testing.T) {
	r := newLocalRepo(t)
	l := r.open(t, LocalOptions{})
	n := l.Calls()
	for _, sub := range []string{"checkout", "add", "stash", "update-index", "fetch", "gc", "config", "status"} {
		_, err := l.inRepo(t.Context(), l.top, gitCmd{args: []string{sub}})
		if !errors.Is(err, &LocalError{Reason: LocalGitFailed}) {
			t.Errorf("%s: %v", sub, err)
		}
	}
	if l.Calls() != n {
		t.Errorf("git was started for a refused subcommand (%d calls)", l.Calls()-n)
	}
}

// ---- the subcommand allow-list (spec §1.1) ----

// gitSubcommands are the names of git's commands (git help -a): a string
// literal with one of these names in the package's code is a subcommand
// the package can run.
var gitSubcommands = []string{
	"add", "am", "annotate", "apply", "archive", "bisect", "blame", "branch", "bugreport", "bundle",
	"cat-file", "check-attr", "check-ignore", "check-mailmap", "check-ref-format", "checkout", "checkout-index",
	"cherry", "cherry-pick", "clean", "clone", "column", "commit", "commit-graph", "commit-tree", "config",
	"count-objects", "credential", "describe", "diff", "diff-files", "diff-index", "diff-tree", "difftool",
	"fast-export", "fast-import", "fetch", "fetch-pack", "filter-branch", "for-each-ref", "for-each-repo",
	"format-patch", "fsck", "fsmonitor--daemon", "gc", "grep", "hash-object", "help", "hook", "index-pack", "init",
	"interpret-trailers", "log", "ls-files", "ls-remote", "ls-tree", "maintenance", "merge", "merge-base",
	"merge-file", "merge-index", "merge-tree", "mergetool", "mktag", "mktree", "multi-pack-index", "mv",
	"name-rev", "notes", "pack-objects", "pack-refs", "prune", "pull", "push", "range-diff", "read-tree",
	"rebase", "reflog", "remote", "repack", "replace", "rerere", "reset", "restore", "rev-list", "rev-parse",
	"revert", "rm", "send-pack", "shortlog", "show", "show-branch", "show-ref", "sparse-checkout", "stash",
	"status", "stripspace", "submodule", "switch", "symbolic-ref", "tag", "unpack-objects", "update-index",
	"update-ref", "update-server-info", "upload-pack", "var", "verify-commit", "verify-pack", "verify-tag",
	"whatchanged", "worktree", "write-tree",
}

// cacheSubcommands are the subcommands only the cache of repository
// context runs (on review-mcp's own bare repositories, never on a local
// one), with the files that may name them.
var cacheSubcommands = map[string][]string{
	"init":      {"ensure.go"},
	"config":    {"ensure.go"},
	"fetch":     {"ensure.go"},
	"ls-remote": {"ensure.go"},
	"rev-list":  {"grep.go"},
}

// TestLocalSubcommandAllowList lists every git subcommand this package's
// code can start and fails on one that is neither on the local allow-list
// nor a cache subcommand in its own file. A subcommand is the first
// argument that is not an option ("-c" takes the next one with it) in a
// []string literal or among the literals appended to an argument list,
// when it is the name of a git command. With the runtime check in run (a
// local command outside the allow-list is refused before git starts), a
// write command cannot reach a local repository.
func TestLocalSubcommandAllowList(t *testing.T) {
	names := map[string]bool{}
	for _, n := range gitSubcommands {
		names[n] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			var elems []ast.Expr
			switch x := n.(type) {
			case *ast.CompositeLit:
				if at, ok := x.Type.(*ast.ArrayType); ok {
					if id, ok := at.Elt.(*ast.Ident); ok && id.Name == "string" {
						elems = x.Elts
					}
				}
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "append" && len(x.Args) > 1 {
					elems = x.Args[1:]
				}
			}
			lit, s := firstSubcommand(elems)
			if lit == nil || !names[s] {
				return true
			}
			found[s] = true
			pos := fset.Position(lit.Pos())
			if localSubcommands[s] {
				return true
			}
			if allowed, ok := cacheSubcommands[s]; ok && slices.Contains(allowed, f) {
				return true
			}
			t.Errorf("%s:%d: the package can run the git subcommand %q, which is not on the allow-list", pos.Filename, pos.Line, s)
			return true
		})
	}
	var listed []string
	for s := range found {
		listed = append(listed, s)
	}
	slices.Sort(listed)
	t.Logf("subcommands the package can run: %v", listed)
	for _, must := range []string{"rev-parse", "diff", "ls-files", "log", "merge-base", "cat-file", "ls-tree", "for-each-ref", "symbolic-ref"} {
		if !found[must] {
			t.Errorf("the scan did not find %q: it no longer sees the call sites", must)
		}
	}
	want := LocalSubcommands()
	if len(want) != 11 || slices.Contains(want, "checkout") {
		t.Errorf("the allow-list changed: %v", want)
	}
}

// firstSubcommand returns the first element of an argument list that is
// not an option, when it is a string literal.
func firstSubcommand(elems []ast.Expr) (*ast.BasicLit, string) {
	for i := 0; i < len(elems); i++ {
		var lit *ast.BasicLit
		switch e := elems[i].(type) {
		case *ast.BasicLit:
			lit = e
		case *ast.BinaryExpr: // "--git-dir=" + dir
			if l, ok := e.X.(*ast.BasicLit); ok {
				if s, err := strconv.Unquote(l.Value); err == nil && strings.HasPrefix(s, "-") {
					continue
				}
			}
			return nil, ""
		default:
			return nil, ""
		}
		if lit.Kind != token.STRING {
			return nil, ""
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return nil, ""
		}
		switch {
		case s == "-c" || s == "--shallow-file":
			i++
		case strings.HasPrefix(s, "-"):
		default:
			return lit, s
		}
	}
	return nil, ""
}

// TestLocalSafetyArgs pins the options every local command starts with
// and the environment it gets (spec §1.1).
func TestLocalSafetyArgs(t *testing.T) {
	le := &localEnv{hooks: "/tmp/x/hooks", home: "/tmp/x/home"}
	a := strings.Join(localSafetyArgs(le), " ")
	for _, want := range []string{"-c core.hooksPath=/tmp/x/hooks", "-c core.fsmonitor=false", "-c diff.external=",
		"-c protocol.allow=never", "--no-optional-locks", "-c log.showSignature=false"} {
		if !strings.Contains(a, want) {
			t.Errorf("the local safety arguments lack %q: %s", want, a)
		}
	}
	if strings.Contains(a, "protocol.https.allow") {
		t.Errorf("a local command allows a protocol: %s", a)
	}
	t.Setenv("GIT_DIR", "/elsewhere")
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com")
	env := localChildEnv(le, runtime.GOOS)
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "HOME=/tmp/x/home"} {
		if !slices.Contains(env, want) {
			t.Errorf("the local environment lacks %s", want)
		}
	}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == "GIT_DIR" || k == "GIT_WORK_TREE" || strings.EqualFold(k, "HTTPS_PROXY") {
			t.Errorf("the local environment passes %s on", k)
		}
	}
	if got := subcommandOf([]string{"--git-dir=/v", "--work-tree=/w", "-c", "a=b", "--shallow-file", "/s", "diff", "x"}); got != "diff" {
		t.Errorf("subcommandOf = %q", got)
	}
}

func TestValidLocalRev(t *testing.T) {
	for _, ok := range []string{"HEAD", "main", "origin/main", "feature/x-1", "v1.2.3", "abcd", "0123456789abcdef0123456789abcdef01234567", "refs/remotes/origin/HEAD"} {
		if !ValidLocalRev(ok) {
			t.Errorf("ValidLocalRev(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "-x", "--output=f", "a..b", "HEAD~1", "HEAD^", "a:b", "x@{1}", "@", "a b", "a\nb", "/x", "x/", "x.lock", ".x", "a//b", "x."} {
		if ValidLocalRev(bad) {
			t.Errorf("ValidLocalRev(%q) = true", bad)
		}
	}
}
