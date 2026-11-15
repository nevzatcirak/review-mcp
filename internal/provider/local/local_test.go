package local_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
	"github.com/nevzatcirak/review-mcp/internal/provider/local"
)

// ---- a synthetic repository ----

type repo struct {
	t        *testing.T
	git, dir string
	// base is the commit on main; feature1 and feature2 the two commits of
	// the branch feat after it.
	base, feature1, feature2 string
}

func gitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; the local source tests need it")
	}
	return p
}

func (r *repo) run(args ...string) string {
	r.t.Helper()
	cmd := exec.Command(r.git, args...) //nolint:gosec // G204: the system git with fixed test arguments
	cmd.Dir = r.dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + r.t.TempDir(), "LANG=C", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	}
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "TMP="+os.TempDir(), "TEMP="+os.TempDir())
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repo) write(files map[string]string) {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
}

// newRepo is a work tree on the branch feat with two commits after main,
// and a remote "origin" whose main is the base commit.
func newRepo(t *testing.T) *repo {
	t.Helper()
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	// The global excludes lookup must not read the real user's
	// configuration.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("USERPROFILE", home)
	r := &repo{t: t, git: gitBin(t), dir: dir}
	r.run("init", "-q", "-b", "main")
	r.run("config", "core.autocrlf", "false")
	r.write(map[string]string{
		"README.md":   "# demo\n",
		"src/app.go":  "package app\n\nfunc Run() int { return 1 }\n",
		"src/util.go": "package app\n\nfunc helper() {}\n",
		".gitignore":  "*.log\n",
	})
	r.run("add", "-A")
	r.run("commit", "-q", "-m", "base")
	r.base = r.run("rev-parse", "HEAD")
	// A remote-tracking branch without a remote to fetch from: the refs
	// exist locally, as after a clone.
	r.run("update-ref", "refs/remotes/origin/main", r.base)
	r.run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	r.run("checkout", "-q", "-b", "feat")
	r.write(map[string]string{"src/app.go": "package app\n\nfunc Run() int { return 2 }\n"})
	r.run("commit", "-q", "-am", "change the result\n\nBody of the first commit.")
	r.feature1 = r.run("rev-parse", "HEAD")
	r.write(map[string]string{"src/new.go": "package app\n\nfunc New() {}\n"})
	r.run("add", "src/new.go")
	r.run("commit", "-q", "-m", "add New")
	r.feature2 = r.run("rev-parse", "HEAD")
	return r
}

func testOptions(r *repo) local.Options {
	o := local.OptionsFrom(config.Defaults())
	o.GitPath = r.git
	return o
}

func open(t *testing.T, r *repo, req local.Request) *local.Source {
	t.Helper()
	if req.RepoPath == "" {
		req.RepoPath = r.dir
	}
	s, err := local.Open(t.Context(), testOptions(r), req)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func paths(d *provider.Diff) []string {
	var out []string
	for _, f := range d.Files {
		out = append(out, f.Path)
	}
	return out
}

// ---- the contract's local run (canary g) ----

type fixture struct{}

func (fixture) ServeLocal(t *testing.T) (provider.Provider, provider.PRRef, func() int64) {
	r := newRepo(t)
	s := open(t, r, local.DefaultRequest(r.dir))
	return s, s.Ref(), s.Calls
}

// TestContractLocal is canary (g): every write method returns unsupported
// without a git call (contract.RunLocal).
// Mutation: implement PostComment as a no-op success.
func TestContractLocal(t *testing.T) {
	contract.RunLocal(t, fixture{})
}

// ---- base and merge base (§1.2) ----

func TestBaseOrder(t *testing.T) {
	r := newRepo(t)
	// No upstream: origin/HEAD, a remote-tracking ref, with the stale note.
	s := open(t, r, local.DefaultRequest(r.dir))
	in := s.Info()
	if in.BaseRef != "origin/HEAD" || in.BaseSHA != r.base || in.MergeBase != r.base || in.HeadSHA != r.feature2 || !in.BaseRemote {
		t.Errorf("Info without upstream = %+v", in)
	}
	if n := s.StaleBaseNote(); !strings.Contains(n, "origin/HEAD") || !strings.Contains(n, "may be behind the remote") {
		t.Errorf("StaleBaseNote = %q", n)
	}
	if !samePath(in.RepoPath, r.dir) || in.Branch != "feat" {
		t.Errorf("RepoPath %q Branch %q", in.RepoPath, in.Branch)
	}
	// The upstream comes first (Q1).
	r.run("update-ref", "refs/remotes/origin/feat", r.feature1)
	// A placeholder remote, never contacted: the upstream needs its refspec.
	r.run("config", "remote.origin.url", "https://git.example.com/demo.git")
	r.run("config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	r.run("config", "branch.feat.remote", "origin")
	r.run("config", "branch.feat.merge", "refs/heads/feat")
	s = open(t, r, local.DefaultRequest(r.dir))
	if in := s.Info(); in.BaseRef != "origin/feat" || in.MergeBase != r.feature1 || !in.BaseRemote {
		t.Errorf("Info with an upstream = %+v", in)
	}
	r.run("config", "--unset", "branch.feat.remote")
	r.run("config", "--unset", "branch.feat.merge")
	// Then origin/main, main and master.
	r.run("symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	s = open(t, r, local.DefaultRequest(r.dir))
	if in := s.Info(); in.BaseRef != "origin/main" {
		t.Errorf("BaseRef without origin/HEAD = %q", in.BaseRef)
	}
	r.run("update-ref", "-d", "refs/remotes/origin/main")
	s = open(t, r, local.DefaultRequest(r.dir))
	if in := s.Info(); in.BaseRef != "main" || in.BaseRemote || s.StaleBaseNote() != "" {
		t.Errorf("Info with main only = %+v, note %q", in, s.StaleBaseNote())
	}
	r.run("branch", "-q", "-m", "main", "master")
	s = open(t, r, local.DefaultRequest(r.dir))
	if in := s.Info(); in.BaseRef != "master" {
		t.Errorf("BaseRef with master only = %q", in.BaseRef)
	}
	r.run("branch", "-q", "-m", "master", "trunk")
	_, err := local.Open(t.Context(), testOptions(r), local.DefaultRequest(r.dir))
	if !errors.Is(err, local.ErrLocalNoBase) {
		t.Fatalf("Open with no base = %v, want ErrLocalNoBase", err)
	}
	for _, want := range []string{"upstream", "origin/HEAD", "origin/main", "origin/master", "main and master", "pass base"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the no-base message %q does not name %q", err, want)
		}
	}
	// A given base: a branch, a commit, an invalid and an unknown one.
	req := local.DefaultRequest(r.dir)
	req.Base = "trunk"
	if in := open(t, r, req).Info(); in.BaseRef != "trunk" || in.MergeBase != r.base || in.BaseRemote {
		t.Errorf("Info with base trunk = %+v", in)
	}
	req.Base = r.feature1[:12]
	if in := open(t, r, req).Info(); in.MergeBase != r.feature1 {
		t.Errorf("Info with a commit base = %+v", in)
	}
	for base, reason := range map[string]string{"HEAD~1": local.ReasonBaseInvalid, "--all": local.ReasonBaseInvalid,
		"HEAD": local.ReasonBaseInvalid, "nope": local.ReasonBaseNotFound} {
		req.Base = base
		if _, err := local.Open(t.Context(), testOptions(r), req); !errors.Is(err, &local.Error{Reason: reason}) {
			t.Errorf("Open with base %q = %v, want %s", base, err, reason)
		}
	}
}

func TestDetachedHead(t *testing.T) {
	r := newRepo(t)
	r.run("checkout", "-q", "--detach", r.feature1)
	s := open(t, r, local.DefaultRequest(r.dir))
	if in := s.Info(); in.Branch != "" || in.HeadSHA != r.feature1 || in.BaseRef != "origin/HEAD" {
		t.Errorf("Info on a detached HEAD = %+v", in)
	}
}

func TestNoMergeBase(t *testing.T) {
	r := newRepo(t)
	r.run("checkout", "-q", "--orphan", "lonely")
	r.run("commit", "-q", "-m", "unrelated")
	_, err := local.Open(t.Context(), testOptions(r), local.DefaultRequest(r.dir))
	if !errors.Is(err, &local.Error{Reason: local.ReasonNoMergeBase}) || !strings.Contains(err.Error(), "no common commit") {
		t.Errorf("Open with unrelated histories = %v", err)
	}
}

func TestOpenRefusals(t *testing.T) {
	r := newRepo(t)
	outside := t.TempDir()
	o := testOptions(r)
	o.AllowedRoots = []string{outside}
	if _, err := local.Open(t.Context(), o, local.DefaultRequest(r.dir)); !errors.Is(err, &gitctx.LocalError{Reason: gitctx.LocalOutsideRoots}) {
		t.Errorf("Open outside the roots = %v", err)
	}
	if _, err := local.Open(t.Context(), testOptions(r), local.DefaultRequest("relative/path")); !errors.Is(err, &gitctx.LocalError{Reason: gitctx.LocalNotAbsolute}) {
		t.Errorf("Open with a relative path = %v", err)
	}
}

// ---- the source (§1.3) ----

func TestPullRequest(t *testing.T) {
	r := newRepo(t)
	s := open(t, r, local.DefaultRequest(r.dir))
	pr, err := s.GetPullRequest(t.Context(), s.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if pr.Title != "add New" || pr.Description != "- add New\n- change the result" || pr.HeadSHA != r.feature2 ||
		pr.BaseSHA != r.base || pr.CommitCount != 2 || pr.SourceBranch != "feat" || pr.TargetBranch != "origin/HEAD" {
		t.Errorf("GetPullRequest = %+v", pr)
	}
	msgs, err := s.GetCommitMessages(t.Context(), s.Ref())
	if err != nil || len(msgs) != 2 || msgs[0] != "change the result\n\nBody of the first commit." || msgs[1] != "add New" {
		t.Errorf("GetCommitMessages = %q %v", msgs, err)
	}
	if ref := s.Ref(); ref.Repo != filepath.Base(r.dir) || ref.URL != "" {
		t.Errorf("Ref = %+v", ref)
	}
}

func TestDescriptionCap(t *testing.T) {
	r := newRepo(t)
	for i := 0; i < 25; i++ {
		r.run("commit", "-q", "--allow-empty", "-m", "commit "+string(rune('a'+i)))
	}
	pr, err := open(t, r, local.DefaultRequest(r.dir)).GetPullRequest(t.Context(), provider.PRRef{})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(pr.Description, "\n")
	if len(lines) != local.MaxDescriptionSubjects || lines[0] != "- commit y" || pr.Title != "commit y" {
		t.Errorf("description of %d lines, first %q, title %q", len(lines), lines[0], pr.Title)
	}
}

func TestUncommittedOnlyTitle(t *testing.T) {
	r := newRepo(t)
	r.run("checkout", "-q", "main")
	r.write(map[string]string{"README.md": "# demo\nedited\n"})
	req := local.DefaultRequest(r.dir)
	req.Base = "main"
	s := open(t, r, req)
	pr, err := s.GetPullRequest(t.Context(), s.Ref())
	if err != nil || pr.Title != local.UncommittedTitle || pr.Description != "" {
		t.Errorf("GetPullRequest with only uncommitted changes = %+v %v", pr, err)
	}
	d, err := s.GetDiff(t.Context(), s.Ref(), pr, provider.DiffOptions{})
	if err != nil || !slices.Equal(paths(d), []string{"README.md"}) {
		t.Errorf("GetDiff = %v %v", paths(d), err)
	}
}

// TestUncommitted is Q2: staged and unstaged edits are in the diff by
// default, and absent with IncludeUncommitted false.
func TestUncommitted(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"src/util.go": "package app\n\nfunc helper() { staged() }\n"})
	r.run("add", "src/util.go")
	r.write(map[string]string{"README.md": "# demo\nunstaged\n"})

	s := open(t, r, local.DefaultRequest(r.dir))
	if !s.Info().Uncommitted {
		t.Error("Info.Uncommitted is false by default")
	}
	d, err := s.GetDiff(t.Context(), s.Ref(), nil, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(d); !slices.Equal(got, []string{"README.md", "src/app.go", "src/new.go", "src/util.go"}) {
		t.Errorf("files with uncommitted changes = %v", got)
	}
	for _, f := range d.Files {
		if f.Path == "README.md" && (f.HeadContent == nil || !strings.Contains(*f.HeadContent, "unstaged") || f.BaseContent == nil) {
			t.Errorf("README.md contents: %+v", f)
		}
	}
	if b, err := s.GetFileAt(t.Context(), s.Ref(), "README.md", r.feature2); err != nil || !strings.Contains(string(b), "unstaged") {
		t.Errorf("GetFileAt(head) with uncommitted changes = %q %v; want the work tree", b, err)
	}
	if b, err := s.GetFileAt(t.Context(), s.Ref(), "README.md", r.base); err != nil || strings.Contains(string(b), "unstaged") {
		t.Errorf("GetFileAt(base) = %q %v", b, err)
	}

	req := local.DefaultRequest(r.dir)
	req.IncludeUncommitted = false
	s = open(t, r, req)
	d, err = s.GetDiff(t.Context(), s.Ref(), nil, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(d); !slices.Equal(got, []string{"src/app.go", "src/new.go"}) {
		t.Errorf("files without uncommitted changes = %v", got)
	}
	if b, err := s.GetFileAt(t.Context(), s.Ref(), "README.md", r.feature2); err != nil || strings.Contains(string(b), "unstaged") {
		t.Errorf("GetFileAt(head) without uncommitted changes = %q %v; want HEAD's blob", b, err)
	}
}

// TestUntracked is canary (e) and Q3: by default an untracked file that is
// not ignored is left out; with IncludeUntracked it is an added file; an
// ignored file never appears.
// Mutation: DefaultRequest sets IncludeUntracked.
func TestUntracked(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{".env": "TOKEN=placeholder-not-a-secret\n", "debug.log": "ignored\n", "notes/todo.md": "one\r\ntwo\r\n"})

	s := open(t, r, local.DefaultRequest(r.dir))
	d, err := s.GetDiff(t.Context(), s.Ref(), nil, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range d.Files {
		if f.Path == ".env" || f.Path == "notes/todo.md" || f.Path == "debug.log" {
			t.Errorf("the untracked file %s is in the default diff", f.Path)
		}
	}
	if s.Info().UntrackedFiles != 0 {
		t.Errorf("UntrackedFiles = %d by default", s.Info().UntrackedFiles)
	}

	req := local.DefaultRequest(r.dir)
	req.IncludeUntracked = true
	s = open(t, r, req)
	d, err = s.GetDiff(t.Context(), s.Ref(), nil, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := paths(d)
	if !slices.Contains(got, "notes/todo.md") || slices.Contains(got, "debug.log") || slices.Contains(got, ".env") {
		t.Errorf("files with untracked files = %v", got)
	}
	// .env is not ignored, but its name suggests a secret: held back and
	// counted (DQ-6).
	if in := s.Info(); in.UntrackedFiles != 1 || in.HeldBack != 1 {
		t.Errorf("UntrackedFiles = %d, HeldBack = %d; want 1 and 1", in.UntrackedFiles, in.HeldBack)
	}
	if n := s.HeldBackNote(); n != "1 untracked files were left out because their names suggest secrets." {
		t.Errorf("HeldBackNote = %q", n)
	}
	for _, f := range d.Files {
		if f.Path != "notes/todo.md" {
			continue
		}
		if f.Type != provider.ChangeAdded || f.Patch != "@@ -0,0 +1,2 @@\n+one\n+two\n" || f.Additions != 2 ||
			f.HeadContent == nil || *f.HeadContent != "one\ntwo\n" || f.BaseStatus != provider.ContentNotApplicable {
			t.Errorf("the untracked file: %+v", f)
		}
	}
}

func TestEmpty(t *testing.T) {
	r := newRepo(t)
	req := local.DefaultRequest(r.dir)
	req.Base = "feat"
	s := open(t, r, req)
	d, err := s.GetDiff(t.Context(), s.Ref(), nil, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 0 || len(d.Skipped) != 0 || len(d.Notes) != 1 {
		t.Fatalf("empty diff = %+v", d)
	}
	n := d.Notes[0]
	if !strings.Contains(n, "nothing to review against feat ("+r.feature2[:12]+")") ||
		!strings.Contains(n, "the commits after the merge base and uncommitted changes to tracked files") {
		t.Errorf("empty note %q", n)
	}
}

func TestLimitsAndStatuses(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"big.txt": strings.Repeat("x", 3000) + "\n", "img.bin": "a\x00b\n", "a1.txt": "1\n", "a2.txt": "2\n"})
	r.run("add", "-A")
	r.run("commit", "-q", "-m", "more files")
	r.write(map[string]string{"untracked.bin": "\x00\x01", "untracked-big.txt": strings.Repeat("y", 3000)})
	o := testOptions(r)
	o.MaxFileBytes = 2000
	o.MaxFiles = 3
	req := local.DefaultRequest(r.dir)
	req.IncludeUntracked = true
	s, err := local.Open(t.Context(), o, req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	d, err := s.GetDiff(t.Context(), s.Ref(), nil, provider.DiffOptions{Include: func(p string) bool { return p != "a2.txt" }})
	if err != nil {
		t.Fatal(err)
	}
	skipped := map[string]string{}
	for _, sk := range d.Skipped {
		skipped[sk.Path] = sk.Reason
	}
	want := map[string]string{"img.bin": provider.SkipBinary, "a2.txt": provider.SkipFiltered,
		"untracked.bin": provider.SkipBinary, "untracked-big.txt": provider.SkipSizeLimit}
	for p, reason := range want {
		if skipped[p] != reason {
			t.Errorf("%s skipped as %q, want %q (all: %v)", p, skipped[p], reason, skipped)
		}
	}
	byPath := map[string]provider.FilePatch{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if f := byPath["big.txt"]; f.HeadStatus != provider.ContentNotFetchedSizeCap || f.Patch == "" {
		t.Errorf("big.txt: %v %q", f.HeadStatus, f.HeadStatus)
	}
	capped := 0
	for i, f := range d.Files {
		if i >= o.MaxFiles && f.Type != provider.ChangeAdded && f.HeadStatus != provider.ContentNotFetchedFileCap {
			t.Errorf("%s past the file cap has head status %q", f.Path, f.HeadStatus)
		}
		if f.HeadStatus == provider.ContentNotFetchedFileCap {
			capped++
		}
	}
	if capped == 0 {
		t.Error("no file was past diff.max_files_full_content")
	}
	if _, err := s.GetFileAt(t.Context(), s.Ref(), "big.txt", s.Info().HeadSHA); !errors.Is(err, provider.ErrTooLarge) {
		t.Errorf("GetFileAt over the cap = %v", err)
	}
	if ls, err := s.ListDir(t.Context(), s.Ref(), "src", s.Info().HeadSHA); err != nil || len(ls.Entries) != 3 {
		t.Errorf("ListDir = %+v %v", ls, err)
	}
}

// TestDeletedAndRenamed: a file deleted in the work tree and a renamed one
// keep their providers' statuses.
func TestDeletedAndRenamed(t *testing.T) {
	r := newRepo(t)
	r.run("mv", "src/util.go", "src/helpers.go")
	if err := os.Remove(filepath.Join(r.dir, "README.md")); err != nil {
		t.Fatal(err)
	}
	s := open(t, r, local.DefaultRequest(r.dir))
	d, err := s.GetDiff(t.Context(), s.Ref(), nil, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range d.Files {
		switch f.Path {
		case "README.md":
			if f.Type != provider.ChangeDeleted || f.HeadStatus != provider.ContentNotApplicable || f.BaseContent == nil {
				t.Errorf("deleted: %+v", f)
			}
		case "src/helpers.go":
			if f.Type != provider.ChangeRenamed || f.OldPath != "src/util.go" || f.BaseContent == nil || f.HeadContent == nil {
				t.Errorf("renamed: %+v", f)
			}
		}
	}
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
