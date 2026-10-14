package gitctx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// ---- fixtures ----

// historyStep changes the source tree before the next commit: files are
// written, remove are deleted (slash-separated names).
type historyStep struct {
	files  map[string]string
	remove []string
}

// localHistory builds a cache entry whose bare repository holds one commit
// per step, the way an earlier fetch and a later one leave it. clone selects
// how the entry gets the history: "" pushes every commit, "partial" is a
// partial clone without blobs above 1 KiB (a promisor remote a lazy fetch
// would reach), "shallow" a clone of the last commit only. It returns the
// SHAs in step order.
func localHistory(t *testing.T, clone string, steps ...historyStep) (*Runner, Checkout, []string) {
	t.Helper()
	gitPath, _ := realGit(t)
	isolateHome(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	gitOut(t, src, gitPath, "init", "-q")
	var shas []string
	for _, s := range steps {
		writeFiles(t, src, s.files)
		for _, name := range s.remove {
			if err := os.Remove(filepath.Join(src, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
		}
		gitOut(t, src, gitPath, "add", "-A")
		gitOut(t, src, gitPath, "commit", "-q", "--allow-empty", "-m", "step")
		shas = append(shas, strings.TrimSpace(gitOut(t, src, gitPath, "rev-parse", "HEAD")))
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
	if err := touch(filepath.Join(entry, markerName)); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(entry, gitDirName)
	srcURL := "file://" + filepath.ToSlash(src)
	switch clone {
	case "":
		gitOut(t, tmp, gitPath, "init", "-q", "--bare", gitDir)
		gitOut(t, src, gitPath, "push", "-q", gitDir, "HEAD:refs/review-mcp/pr/1")
	case "partial":
		gitOut(t, src, gitPath, "config", "uploadpack.allowFilter", "true")
		gitOut(t, tmp, gitPath, "clone", "-q", "--bare", "--filter=blob:limit=1k", srcURL, gitDir)
	case "shallow":
		gitOut(t, tmp, gitPath, "clone", "-q", "--bare", "--depth=1", srcURL, gitDir)
	default:
		t.Fatalf("unknown clone %q", clone)
	}
	return r, Checkout{GitDir: gitDir, HeadSHA: shas[len(shas)-1], Ref: "refs/review-mcp/pr/1"}, shas
}

// changedHistory is the history of TestRunnerChangedPaths: an earlier review at
// commit 0, then edits, a rename, a deletion and a revert.
func changedHistory() []historyStep {
	return []historyStep{
		{files: map[string]string{
			"keep.go":                "package keep\n",
			"edit.go":                "package edit\n",
			"revert.go":              "package revert\n",
			"old name.go":            "package renamed\n",
			"dir with space/ünï.go":  "package unicode\n",
			"odd\nname.go":           "package odd\n",
			"gone.go":                "package gone\n",
			"mode.sh":                "echo hi\n",
			"nested/deeper/inner.go": "package inner\n",
		}},
		{
			files: map[string]string{
				"edit.go":               "package edit\n\nfunc New() {}\n",
				"revert.go":             "package revert // changed for a while\n",
				"new name.go":           "package renamed\n",
				"dir with space/ünï.go": "package unicode // edited\n",
				"odd\nname.go":          "package odd // edited\n",
			},
			remove: []string{"old name.go", "gone.go"},
		},
		{files: map[string]string{"revert.go": "package revert\n"}},
	}
}

// ---- real git ----

// TestRunnerChangedPaths: against a real repository, the paths whose content
// changed between two commits come back as git stores them; a file edited
// and then reverted to its earlier content is the same blob and is not
// listed; a rename is its old and its new path (--no-renames).
func TestRunnerChangedPaths(t *testing.T) {
	r, co, shas := localHistory(t, "", changedHistory()...)
	ctx := context.Background()
	got, err := r.ChangedPaths(ctx, co, shas[0], strings.ToUpper(shas[2]))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"dir with space/ünï.go", "edit.go", "gone.go", "new name.go", "odd\nname.go", "old name.go"}
	if !slices.Equal(got, want) {
		t.Errorf("ChangedPaths = %q, want %q", got, want)
	}

	if got, err := r.ChangedPaths(ctx, co, shas[1], shas[2]); err != nil || !slices.Equal(got, []string{"revert.go"}) {
		t.Errorf("ChangedPaths(last step) = %q, %v, want only revert.go", got, err)
	}
	if got, err := r.ChangedPaths(ctx, co, shas[2], shas[2]); err != nil || len(got) != 0 {
		t.Errorf("ChangedPaths(same commit) = %q, %v, want none", got, err)
	}
}

// TestRunnerChangedPathsUnknownCommit: a commit the cache does not hold is an
// error, never an empty list.
func TestRunnerChangedPathsUnknownCommit(t *testing.T) {
	r, co, shas := localHistory(t, "", changedHistory()...)
	_, err := r.ChangedPaths(context.Background(), co, strings.Repeat("ab", 20), shas[2])
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("ChangedPaths(unknown commit) error = %v, want a gitctx error", err)
	}
}

// TestRunnerChangedPathsPartialClone: the comparison reads tree objects only, so
// a blob the partial clone lacks does not matter and is never fetched.
func TestRunnerChangedPathsPartialClone(t *testing.T) {
	steps := []historyStep{
		{files: map[string]string{"big.txt": strings.Repeat("a", 3000) + "\n", "small.txt": "one\n"}},
		{files: map[string]string{"big.txt": strings.Repeat("b", 3000) + "\n"}},
	}
	r, co, shas := localHistory(t, "partial", steps...)
	ctx := context.Background()
	if _, err := r.FileAt(ctx, co, shas[1], "big.txt", 1<<20); err == nil {
		t.Fatal("the partial clone holds the big blob; the fixture does not test what it should")
	}
	got, err := r.ChangedPaths(ctx, co, shas[0], shas[1])
	if err != nil || !slices.Equal(got, []string{"big.txt"}) {
		t.Errorf("ChangedPaths(partial clone) = %q, %v, want big.txt", got, err)
	}
	if _, err := r.FileAt(ctx, co, shas[1], "big.txt", 1<<20); err == nil {
		t.Error("the comparison fetched the missing blob")
	}
}

// TestRunnerChangedPathsShallow: a commit a shallow cache lacks is an error (the
// caller compares through the provider); nothing is fetched.
func TestRunnerChangedPathsShallow(t *testing.T) {
	r, co, shas := localHistory(t, "shallow", changedHistory()...)
	ctx := context.Background()
	if r.HasCommit(ctx, co, shas[0]) {
		t.Fatal("the shallow clone holds the first commit; the fixture does not test what it should")
	}
	_, err := r.ChangedPaths(ctx, co, shas[0], shas[2])
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("ChangedPaths(shallow) error = %v, want a gitctx error", err)
	}
	if r.HasCommit(ctx, co, shas[0]) {
		t.Error("the comparison fetched the missing commit")
	}
}

// TestRunnerListPaths: every file of a commit, nested ones included, as git
// stores the path; an unknown commit is an error, never an empty list.
func TestRunnerListPaths(t *testing.T) {
	r, co, shas := localHistory(t, "", changedHistory()...)
	ctx := context.Background()
	got, err := r.ListPaths(ctx, co, strings.ToUpper(shas[0]))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"dir with space/ünï.go", "edit.go", "gone.go", "keep.go", "mode.sh", "nested/deeper/inner.go", "odd\nname.go", "old name.go", "revert.go"}
	if !slices.Equal(got, want) {
		t.Errorf("ListPaths(first) = %q, want %q", got, want)
	}
	got, err = r.ListPaths(ctx, co, shas[2])
	if err != nil || !slices.Contains(got, "new name.go") || slices.Contains(got, "old name.go") || slices.Contains(got, "gone.go") || len(got) != 8 {
		t.Errorf("ListPaths(last) = %q, %v", got, err)
	}
	_, err = r.ListPaths(ctx, co, strings.Repeat("ab", 20))
	var ge *Error
	if !errors.As(err, &ge) {
		t.Errorf("ListPaths(unknown commit) error = %v, want a gitctx error", err)
	}
}

// ---- argv, isolation and failures (fake git) ----

const fakeSHA2 = "89abcdef0123456789abcdef0123456789abcdef"

// TestRunnerChangedPathsArgv [canary]: the exact command line (R-1): no external
// diff program and no textconv filter can run, renames are two paths, the
// revisions cannot be read as options or paths; the command runs offline
// from the allowlisted environment with the cache's empty home and no hook.
func TestRunnerChangedPathsArgv(t *testing.T) {
	t.Setenv("GIT_EXTERNAL_DIFF", "/bin/false")
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA, DiffOut: "a.go\x00b.go\x00"}, Options{})
	co := fakeCheckout(t, r)
	got, err := r.ChangedPaths(context.Background(), co, strings.ToUpper(fakeSHA), fakeSHA2)
	if err != nil || !slices.Equal(got, []string{"a.go", "b.go"}) {
		t.Fatalf("ChangedPaths = %q, %v", got, err)
	}
	diffs := callsOf(f.calls(t), "diff")
	if len(diffs) != 1 {
		t.Fatalf("%d git diff calls, want 1", len(diffs))
	}
	c := diffs[0]
	want := append(safetyArgsFor(nil, runtime.GOOS, true), "--git-dir="+co.GitDir,
		"diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--end-of-options", fakeSHA, fakeSHA2, "--")
	if !slices.Equal(c.Args, want) {
		t.Errorf("git diff argv =\n  %q\nwant\n  %q", c.Args, want)
	}
	for _, bad := range []string{"--textconv", "--ext-diff", "--find-renames", "-M"} {
		if slices.Contains(c.Args, bad) {
			t.Errorf("git diff argv carries %s", bad)
		}
	}
	args := " " + strings.Join(c.Args, " ") + " "
	if !strings.Contains(args, " -c core.hooksPath="+os.DevNull+" ") || !strings.Contains(args, " -c protocol.allow=never ") ||
		strings.Contains(args, "protocol.https.allow") {
		t.Errorf("git diff is not offline and hook-free: %s", args)
	}
	if v, ok := envValue(c.Env, "GIT_NO_LAZY_FETCH"); !ok || v != "1" {
		t.Errorf("GIT_NO_LAZY_FETCH = %q (set %v), want 1", v, ok)
	}
	root, _ := r.CacheDir()
	if h, _ := envValue(c.Env, "HOME"); h != homeDir(root) {
		t.Errorf("HOME = %q, want the cache's empty home", h)
	}
	for _, kv := range c.Env {
		name, _, _ := strings.Cut(kv, "=")
		if name != "GIT_NO_LAZY_FETCH" && !allowedEnv.MatchString(name) {
			t.Errorf("git diff got %s, which is not on the allowlist", name)
		}
	}
}

// TestRunnerListPathsArgv: the exact command line of ListPaths: tree objects
// only, the commit after --end-of-options and nothing after it (ls-tree
// would read a "--" there as a path and list nothing); offline, with the
// cache's empty home.
func TestRunnerListPathsArgv(t *testing.T) {
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA, LsTreeOut: "a b/ü.go\x00c.go\x00"}, Options{})
	co := fakeCheckout(t, r)
	got, err := r.ListPaths(context.Background(), co, strings.ToUpper(fakeSHA2))
	if err != nil || !slices.Equal(got, []string{"a b/ü.go", "c.go"}) {
		t.Fatalf("ListPaths = %q, %v", got, err)
	}
	calls := callsOf(f.calls(t), "ls-tree")
	if len(calls) != 1 {
		t.Fatalf("%d git ls-tree calls, want 1", len(calls))
	}
	c := calls[0]
	want := append(safetyArgsFor(nil, runtime.GOOS, true), "--git-dir="+co.GitDir,
		"ls-tree", "-r", "-z", "--name-only", "--end-of-options", fakeSHA2)
	if !slices.Equal(c.Args, want) {
		t.Errorf("git ls-tree argv =\n  %q\nwant\n  %q", c.Args, want)
	}
	if v, ok := envValue(c.Env, "GIT_NO_LAZY_FETCH"); !ok || v != "1" {
		t.Errorf("GIT_NO_LAZY_FETCH = %q (set %v), want 1", v, ok)
	}
	root, _ := r.CacheDir()
	if h, _ := envValue(c.Env, "HOME"); h != homeDir(root) {
		t.Errorf("HOME = %q, want the cache's empty home", h)
	}
	for _, kv := range c.Env {
		name, _, _ := strings.Cut(kv, "=")
		if name != "GIT_NO_LAZY_FETCH" && !allowedEnv.MatchString(name) {
			t.Errorf("git ls-tree got %s, which is not on the allowlist", name)
		}
	}
}

// TestRunnerListPathsFailures: an unusable revision is refused before git
// runs; a failure and a list cut by the cap are errors, never a short list;
// a canceled context is reported as such.
func TestRunnerListPathsFailures(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
		co := fakeCheckout(t, r)
		for _, rev := range []string{"--output=x", "HEAD", fakeSHA[:12], "", fakeSHA + ":dir"} {
			_, err := r.ListPaths(context.Background(), co, rev)
			wantReason(t, err, ReasonUnsupported)
		}
		if n := len(callsOf(f.calls(t), "ls-tree")); n != 0 {
			t.Errorf("%d git ls-tree calls for refused input", n)
		}
	})
	t.Run("git fails", func(t *testing.T) {
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, LsTreeExit: 128, LsTreeOut: "partial.go\x00"}, Options{})
		got, err := r.ListPaths(context.Background(), fakeCheckout(t, r), fakeSHA)
		var ge *Error
		if !errors.As(err, &ge) || got != nil {
			t.Errorf("ListPaths = %q, %v, want a gitctx error and no paths", got, err)
		}
	})
	t.Run("output cap", func(t *testing.T) {
		saved := maxChangedOutput
		maxChangedOutput = 64
		t.Cleanup(func() { maxChangedOutput = saved })
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, LsTreeOut: strings.Repeat("some/long/path.go\x00", 20)}, Options{})
		got, err := r.ListPaths(context.Background(), fakeCheckout(t, r), fakeSHA)
		if got != nil {
			t.Errorf("a cut list came back: %q", got)
		}
		wantReason(t, err, ReasonTooLarge)
	})
	t.Run("canceled", func(t *testing.T) {
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := r.ListPaths(ctx, fakeCheckout(t, r), fakeSHA); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

// TestRunnerChangedPathsOutput: the NUL-separated output is read as it is: a
// path may hold spaces, newlines, tabs and quotes; a duplicate is listed
// once.
func TestRunnerChangedPathsOutput(t *testing.T) {
	out := "dir with space/a.go\x00line\nbreak.go\x00tab\there.go\x00\"quoted\".go\x00dir with space/a.go\x00"
	r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, DiffOut: out}, Options{})
	got, err := r.ChangedPaths(context.Background(), fakeCheckout(t, r), fakeSHA, fakeSHA2)
	want := []string{"dir with space/a.go", "line\nbreak.go", "tab\there.go", "\"quoted\".go"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("ChangedPaths = %q, %v, want %q", got, err, want)
	}
	// Bytes that are not UTF-8 (the fake's JSON configuration cannot carry
	// them, so the reader is checked alone).
	if got := nameList([]byte("bad\xffbyte.go\x00\x00ok.go\x00")); !slices.Equal(got, []string{"bad\xffbyte.go", "ok.go"}) {
		t.Errorf("nameList = %q", got)
	}
	if got := nameList(nil); got != nil {
		t.Errorf("nameList(nothing) = %q, want nil", got)
	}
}

// TestRunnerChangedPathsFailures: an unusable revision or checkout is refused
// before git runs; a git failure is a fixed reason without git's text; a
// list cut by the output cap is an error, never a short list; a canceled
// context is reported as such.
func TestRunnerChangedPathsFailures(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
		co := fakeCheckout(t, r)
		for _, revs := range [][2]string{
			{"--output=x", fakeSHA2}, {fakeSHA, "-p"}, {"HEAD", fakeSHA2}, {fakeSHA[:12], fakeSHA2}, {"", fakeSHA2},
			{fakeSHA + "\n", fakeSHA2}, {fakeSHA, fakeSHA2 + ":x"},
		} {
			_, err := r.ChangedPaths(context.Background(), co, revs[0], revs[1])
			wantReason(t, err, ReasonUnsupported)
		}
		outside := Checkout{GitDir: filepath.Join(t.TempDir(), gitDirName), HeadSHA: fakeSHA}
		_, err := r.ChangedPaths(context.Background(), outside, fakeSHA, fakeSHA2)
		wantReason(t, err, ReasonCache)
		if n := len(callsOf(f.calls(t), "diff")); n != 0 {
			t.Errorf("%d git diff calls for refused input", n)
		}
	})
	t.Run("git fails", func(t *testing.T) {
		const stderrText = "fatal: bad object /home/someone/hidden-path"
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, DiffExit: 128, DiffStderr: stderrText, DiffOut: "partial.go\x00"}, Options{})
		got, err := r.ChangedPaths(context.Background(), fakeCheckout(t, r), fakeSHA, fakeSHA2)
		var ge *Error
		if !errors.As(err, &ge) || got != nil {
			t.Fatalf("ChangedPaths = %q, %v, want a gitctx error and no paths", got, err)
		}
		if strings.Contains(err.Error(), "hidden-path") {
			t.Errorf("git's stderr reached the error: %v", err)
		}
	})
	t.Run("output cap", func(t *testing.T) {
		saved := maxChangedOutput
		maxChangedOutput = 64
		t.Cleanup(func() { maxChangedOutput = saved })
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, DiffOut: strings.Repeat("some/long/path.go\x00", 20)}, Options{})
		got, err := r.ChangedPaths(context.Background(), fakeCheckout(t, r), fakeSHA, fakeSHA2)
		if got != nil {
			t.Errorf("a cut list came back: %q", got)
		}
		wantReason(t, err, ReasonTooLarge)
	})
	t.Run("canceled", func(t *testing.T) {
		r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
		co := fakeCheckout(t, r)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := r.ChangedPaths(ctx, co, fakeSHA, fakeSHA2); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if n := len(callsOf(f.calls(t), "diff")); n != 0 {
			t.Errorf("%d git diff calls after cancellation", n)
		}
	})
}
