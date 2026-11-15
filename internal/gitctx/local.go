package gitctx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Safe reads of a repository on the user's own machine (V-20, X-52; spec
// docs/plan/v2.10-V20-spec.md §1.1).
//
// R-1 binds harder here: the repository belongs to the user. review-mcp
// reads it with git and nothing else. It never builds, runs or loads its
// code; it never runs a hook, a filter, a textconv or external diff driver,
// or an fsmonitor; it never writes to the repository, its index, its config
// or its refs; and it never uses the network.
//
// Every git process on a local repository is built in one place, run with
// gitCmd.local set:
//
//   - the arguments start with localSafetyArgs: core.hooksPath is an empty
//     directory under this repository's temporary directory, core.fsmonitor
//     is off, diff.external is empty, protocol.allow is never, and
//     --no-optional-locks;
//   - the environment is localChildEnv: PATH (and SYSTEMROOT on Windows),
//     HOME pointing at an empty directory, GIT_CONFIG_NOSYSTEM=1 on every
//     system (no TLS setting is ever needed), GIT_TERMINAL_PROMPT=0,
//     GIT_NO_LAZY_FETCH=1, GIT_OPTIONAL_LOCKS=0 and LC_ALL=C. Nothing else
//     of the parent's environment is passed on; in particular a GIT_DIR or
//     GIT_WORK_TREE set by a git hook that runs review-mcp never reaches git;
//   - the subcommand must be one of localSubcommands, checked before git
//     is started; anything else is refused without a process.
//
// There are two ways a command reaches the repository (E1, E3 of the spec):
//
//   - in the repository (cwd = the work tree): rev-parse, symbolic-ref,
//     for-each-ref, merge-base, log, ls-files and a diff of two commits.
//     These read the repository's own configuration, which the arguments
//     above override where it could run something. None of them reads a
//     work-tree file's content, so no filter can run;
//   - through a read view (view.go) of this repository: --git-dir is a bare
//     repository written by review-mcp in the temporary directory, with no
//     configuration but its format, and GIT_OBJECT_DIRECTORY is the
//     repository's own object directory. Blob and tree reads (cat-file,
//     ls-tree) and the diff against the work tree run here. The view has no
//     promisor, so a blob a partial clone lacks is reported missing and is
//     never fetched; it defines no filter, textconv or diff driver, so the
//     work tree's .gitattributes cannot name one that runs; and the diff
//     against the work tree reads a private copy of the index, because git
//     diff refreshes and rewrites the index it reads, --no-optional-locks or
//     not (verified on git 2.43: E1 of the WP-24a report).
//
// The read view is reached only after git has accepted the work tree in the
// repository (rev-parse in OpenLocal), so git's safe.directory check (E2)
// applies to it: the view alone would not check the owner.

// localSubcommands is the fixed allow-list of the git subcommands run on a
// local repository (spec §1.1, plus ls-tree for ListDir, §1.3).
// TestLocalSubcommandAllowList checks every call site against it.
var localSubcommands = map[string]bool{
	"rev-parse":    true,
	"merge-base":   true,
	"diff":         true,
	"ls-files":     true,
	"show":         true,
	"cat-file":     true,
	"log":          true,
	"for-each-ref": true,
	"symbolic-ref": true,
	"grep":         true,
	"ls-tree":      true,
}

// LocalSubcommands returns the allow-list, sorted (for documentation and
// the tests of other packages).
func LocalSubcommands() []string {
	out := make([]string, 0, len(localSubcommands))
	for s := range localSubcommands {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// localEnv is the per-repository part of the local safety settings.
type localEnv struct {
	// hooks is an empty directory, the value of core.hooksPath.
	hooks string
	// home is an empty directory, HOME and XDG_CONFIG_HOME (and USERPROFILE
	// on Windows).
	home string
	// ceiling is GIT_CEILING_DIRECTORIES, or "": the parent of the allowed
	// root the repository path is under, so that git never looks for a
	// repository above that root.
	ceiling string
	// calls counts the git processes started for this repository.
	calls *atomic.Int64
}

// testLocalHook, when set by a test, is called with the full argument list
// of every local git command before it is checked and run.
var testLocalHook func(args []string)

// localSafetyArgs are the global options of every local git command. They
// are not secret, so they are arguments, where a test can see them.
func localSafetyArgs(le *localEnv) []string {
	a := []string{
		"-c", "core.hooksPath=" + le.hooks,
		"-c", "core.fsmonitor=false",
		"-c", "core.useBuiltinFSMonitor=false", // older Git for Windows
		"-c", "diff.external=",
		"-c", "protocol.allow=never",
		"-c", "credential.helper=",
		"-c", "core.askPass=",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		// git log and git show would run gpg for every signed commit.
		"-c", "log.showSignature=false",
		"-c", "submodule.recurse=false",
		"-c", "color.ui=false",
		"--no-optional-locks",
	}
	return a
}

// localChildEnv is the environment of a local git process: an allowlist,
// never the parent's environment.
func localChildEnv(le *localEnv, goos string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if k == "PATH" || (goos == "windows" && (strings.EqualFold(k, "PATH") || strings.EqualFold(k, "SYSTEMROOT"))) {
			env = append(env, kv)
		}
	}
	env = append(env, "HOME="+le.home, "XDG_CONFIG_HOME="+le.home)
	if goos == "windows" {
		env = append(env, "USERPROFILE="+le.home)
	}
	env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
		"LANG=C",
	)
	if le.ceiling != "" {
		env = append(env, "GIT_CEILING_DIRECTORIES="+le.ceiling)
	}
	return env
}

// subcommandOf returns the git subcommand of a local argument list: the
// first argument after the global options this package puts in front of
// it ("-c k=v", --git-dir=, --work-tree=, --shallow-file <file>,
// --no-optional-locks).
func subcommandOf(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "--shallow-file":
			i++
		case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") || a == "--no-optional-locks":
		default:
			return a
		}
	}
	return ""
}

// errLocalRefused is the result of a local command whose subcommand is not
// on the allow-list; git is not started.
var errLocalRefused = errors.New("git subcommand not allowed on a local repository")

// ---- errors ----

// Reasons of a LocalError.
const (
	// LocalNotAbsolute: repo_path is not an absolute path.
	LocalNotAbsolute = "not_absolute"
	// LocalNotFound: repo_path does not exist or is not a directory.
	LocalNotFound = "not_found"
	// LocalOutsideRoots: repo_path is not under local.allowed_roots.
	LocalOutsideRoots = "outside_allowed_roots"
	// LocalNotRepository: repo_path is not in a git work tree.
	LocalNotRepository = "not_a_repository"
	// LocalBare: the repository is bare (no work tree).
	LocalBare = "bare_repository"
	// LocalDubiousOwnership: git refuses the repository because another OS
	// user owns it (safe.directory, E2).
	LocalDubiousOwnership = "dubious_ownership"
	// LocalUnbornHead: HEAD names a branch with no commit yet.
	LocalUnbornHead = "unborn_head"
	// LocalGitUnavailable: git is missing or older than MinGitVersion.
	LocalGitUnavailable = "git_unavailable"
	// LocalObjectsMissing: git could not read an object of the repository
	// (a partial clone lacks it, or the repository is damaged); nothing is
	// fetched.
	LocalObjectsMissing = "objects_missing"
	// LocalTimeout: a git command took longer than its limit.
	LocalTimeout = "timeout"
	// LocalTooLarge: the diff is larger than diff.max_diff_bytes.
	LocalTooLarge = "too_large"
	// LocalTempDir: the temporary directory cannot be created or used.
	LocalTempDir = "temp_unusable"
	// LocalGitFailed: any other git failure.
	LocalGitFailed = "git_failed"
)

var localSentences = map[string]string{
	LocalNotAbsolute:   "repo_path must be an absolute path",
	LocalNotFound:      "repo_path does not exist or is not a directory",
	LocalOutsideRoots:  "repo_path is not under a directory of local.allowed_roots",
	LocalNotRepository: "repo_path is not inside a git work tree",
	LocalBare:          "the repository is bare: a local review needs a work tree",
	LocalDubiousOwnership: "git refuses the repository because it is owned by another OS user (safe.directory); " +
		"review-mcp neither adds nor reads safe.directory entries: run it as the repository's owner",
	LocalUnbornHead:     "the current branch has no commit yet",
	LocalGitUnavailable: "git " + MinGitVersion + " or later is required for a local review",
	LocalObjectsMissing: "git could not read an object of the repository (a partial clone lacks it, or the repository is damaged); nothing is fetched",
	LocalTimeout:        "a git command on the repository took too long",
	LocalTooLarge:       "the local diff is larger than diff.max_diff_bytes",
	LocalTempDir:        "the temporary directory for the local review cannot be used",
	LocalGitFailed:      "git failed on the repository",
}

// LocalError is a failure of a local repository read. It carries a fixed
// reason only: git's stderr, paths and file contents are never part of it.
type LocalError struct {
	Reason string
}

func (e *LocalError) Error() string {
	if s, ok := localSentences[e.Reason]; ok {
		return "local review: " + s
	}
	return "local review: " + localSentences[LocalGitFailed]
}

// Is matches a *LocalError of the same reason.
func (e *LocalError) Is(target error) bool {
	t, ok := target.(*LocalError)
	return ok && t.Reason == e.Reason
}

func localFail(reason string) error { return &LocalError{Reason: reason} }

// localFailure classifies a failed local command. stderr is read here and
// dropped.
func localFailure(r result) error {
	switch {
	case errors.Is(r.err, context.DeadlineExceeded):
		return localFail(LocalTimeout)
	case errors.Is(r.err, context.Canceled):
		return context.Canceled
	case errors.Is(r.err, errLocalRefused):
		return localFail(LocalGitFailed)
	}
	s := strings.ToLower(r.stderr)
	switch {
	case strings.Contains(s, "dubious ownership"):
		return localFail(LocalDubiousOwnership)
	case strings.Contains(s, "not a git repository"):
		return localFail(LocalNotRepository)
	case strings.Contains(s, "unable to read"), strings.Contains(s, "bad object"),
		strings.Contains(s, "missing blob"), strings.Contains(s, "could not fetch"),
		strings.Contains(s, "lazy fetch"), strings.Contains(s, "promisor"):
		return localFail(LocalObjectsMissing)
	}
	return localFail(LocalGitFailed)
}

// ---- the repository ----

// localTimeout bounds each local git command.
var localTimeout = 60 * time.Second

// LocalOptions configure OpenLocal.
type LocalOptions struct {
	// GitPath is the git binary; "" is "git" in PATH.
	GitPath string
	// AllowedRoots is local.allowed_roots: when not empty, the repository
	// path must be one of these directories or below one, after symbolic
	// links are resolved on both sides.
	AllowedRoots []string
	// TempDir is the parent of the repository's temporary directory; ""
	// is os.TempDir().
	TempDir string
}

// Local is a git work tree on this machine, opened by OpenLocal. Close
// removes its temporary directory. It is safe for concurrent use.
type Local struct {
	gitPath string
	// top is the work tree's top-level directory, gitDir its git directory
	// (per worktree) and commonDir the directory of the shared objects and
	// refs, all absolute and in the system's form.
	top, gitDir, commonDir string
	format                 string
	headSHA                string
	branch                 string
	tmp                    string
	view                   string
	env                    *localEnv
	calls                  atomic.Int64
	seq                    atomic.Int64
	closeOnce              sync.Once
}

// Top is the absolute path of the work tree's top-level directory.
func (l *Local) Top() string { return l.top }

// HeadSHA is the commit HEAD named when the repository was opened.
func (l *Local) HeadSHA() string { return l.headSHA }

// Branch is the full name of the current branch (refs/heads/...), or ""
// for a detached HEAD.
func (l *Local) Branch() string { return l.branch }

// Calls is the number of git processes started for this repository.
func (l *Local) Calls() int64 { return l.calls.Load() }

// Close removes the temporary directory (the hooks directory, the empty
// home, the read view and the index copies). The repository is not
// touched.
func (l *Local) Close() error {
	var err error
	l.closeOnce.Do(func() { err = os.RemoveAll(l.tmp) })
	return err
}

// OpenLocal checks repoPath and opens the work tree it is in (spec §1.2):
//
//   - repoPath must be absolute; symbolic links are resolved, and the
//     result must be a directory under one of AllowedRoots when that is
//     set. All of this happens before any git process is started;
//   - git must accept it as a work tree (rev-parse in the directory): a bare
//     repository, a git directory itself and a repository git refuses for
//     its owner (safe.directory) are refused;
//   - a subdirectory means the work tree's top level;
//   - HEAD must name a commit (an unborn branch is refused).
func OpenLocal(ctx context.Context, opts LocalOptions, repoPath string) (*Local, error) {
	real, ceiling, err := checkLocalPath(repoPath, opts.AllowedRoots)
	if err != nil {
		return nil, err
	}
	probe := gitProbe(opts.GitPath)
	if !probe.ok() {
		return nil, localFail(LocalGitUnavailable)
	}
	tmp, err := os.MkdirTemp(opts.TempDir, "review-mcp-local-")
	if err != nil {
		return nil, localFail(LocalTempDir)
	}
	l := &Local{gitPath: probe.path, tmp: tmp}
	l.env = &localEnv{hooks: filepath.Join(tmp, "hooks"), home: filepath.Join(tmp, "home"), ceiling: ceiling, calls: &l.calls}
	for _, d := range []string{l.env.hooks, l.env.home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			_ = l.Close()
			return nil, localFail(LocalTempDir)
		}
	}
	if err := l.open(ctx, real, opts.AllowedRoots); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// checkLocalPath validates repoPath without running git: it returns the
// path with symbolic links resolved and the GIT_CEILING_DIRECTORIES value
// for the allowed root it is under ("" without roots).
func checkLocalPath(repoPath string, roots []string) (real, ceiling string, err error) {
	if repoPath == "" || !filepath.IsAbs(repoPath) {
		return "", "", localFail(LocalNotAbsolute)
	}
	real, err = filepath.EvalSymlinks(filepath.Clean(repoPath))
	if err != nil {
		return "", "", localFail(LocalNotFound)
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", "", localFail(LocalNotFound)
	}
	if len(roots) == 0 {
		return real, "", nil
	}
	root, ok := allowedRootOf(real, roots)
	if !ok {
		return "", "", localFail(LocalOutsideRoots)
	}
	if parent := filepath.Dir(root); parent != root {
		ceiling = parent
	}
	return real, ceiling, nil
}

// allowedRootOf returns the allowed root (symbolic links resolved) that
// real is equal to or below; roots that do not exist are skipped.
func allowedRootOf(real string, roots []string) (string, bool) {
	for _, r := range roots {
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		rr, err := filepath.EvalSymlinks(filepath.Clean(r))
		if err != nil {
			continue
		}
		if samePath(rr, real) || withinPath(rr, real) {
			return rr, true
		}
	}
	return "", false
}

// samePath and withinPath compare paths as the file system does: without
// case on Windows and macOS (their default file systems), exactly
// elsewhere.
func samePath(a, b string) bool {
	if caseInsensitiveFS() {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func withinPath(root, p string) bool {
	if caseInsensitiveFS() {
		return within(strings.ToLower(root), strings.ToLower(p))
	}
	return within(root, p)
}

func caseInsensitiveFS() bool { return runtime.GOOS == "windows" || runtime.GOOS == "darwin" }

// open runs the rev-parse checks in dir and fills l.
func (l *Local) open(ctx context.Context, dir string, roots []string) error {
	out, err := l.inRepo(ctx, dir, gitCmd{args: []string{"rev-parse", "--is-bare-repository", "--is-inside-work-tree"}})
	if err != nil {
		return err
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return localFail(LocalGitFailed)
	}
	if f[0] == "true" {
		return localFail(LocalBare)
	}
	if f[1] != "true" {
		return localFail(LocalNotRepository)
	}
	out, err = l.inRepo(ctx, dir, gitCmd{args: []string{"rev-parse", "--path-format=absolute",
		"--show-toplevel", "--git-dir", "--git-common-dir", "--show-object-format"}})
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 4 {
		return localFail(LocalGitFailed)
	}
	l.top = filepath.Clean(filepath.FromSlash(strings.TrimRight(lines[0], "\r")))
	l.gitDir = filepath.Clean(filepath.FromSlash(strings.TrimRight(lines[1], "\r")))
	l.commonDir = filepath.Clean(filepath.FromSlash(strings.TrimRight(lines[2], "\r")))
	l.format = strings.TrimSpace(lines[3])
	if _, ok := viewConfig[l.format]; !ok || !filepath.IsAbs(l.top) || !filepath.IsAbs(l.commonDir) {
		return localFail(LocalGitFailed)
	}
	// The top level must still be under an allowed root; GIT_CEILING_DIRECTORIES
	// already keeps git from looking above it.
	if len(roots) > 0 {
		real, err := filepath.EvalSymlinks(l.top)
		if err != nil {
			return localFail(LocalNotRepository)
		}
		if _, ok := allowedRootOf(real, roots); !ok {
			return localFail(LocalOutsideRoots)
		}
	}
	sha, ok, err := l.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return err
	}
	if !ok {
		return localFail(LocalUnbornHead)
	}
	l.headSHA = sha
	if out, err := l.inRepo(ctx, l.top, gitCmd{args: []string{"symbolic-ref", "-q", "HEAD"}}); err == nil {
		l.branch = strings.TrimSpace(string(out))
	} else if !isExit(err, 1) {
		return err
	}
	v := filepath.Join(l.tmp, "view")
	if err := os.Mkdir(v, 0o700); err != nil {
		return localFail(LocalTempDir)
	}
	if err := writeView(v, l.format); err != nil {
		return localFail(LocalTempDir)
	}
	l.view = v
	return nil
}

// exitError is the exit code of a failed local command, kept for the
// callers that read "not found" from it (rev-parse --verify --quiet,
// symbolic-ref -q, merge-base).
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func isExit(err error, code int) bool {
	var e *exitError
	return errors.As(err, &e) && e.code == code
}

// inRepo runs a local command in dir (the repository itself). It returns
// stdout, or an error that carries git's exit code next to the fixed
// reason.
func (l *Local) inRepo(ctx context.Context, dir string, c gitCmd) ([]byte, error) {
	c.dir = dir
	c.local = l.env
	res := l.runLocal(ctx, c)
	if res.err != nil {
		return res.stdout, l.wrapExit(res)
	}
	if res.truncated {
		return res.stdout, localFail(LocalTooLarge)
	}
	return res.stdout, nil
}

func (l *Local) wrapExit(res result) error {
	err := localFailure(res)
	var ee interface{ ExitCode() int }
	if errors.As(res.err, &ee) {
		return &exitError{code: ee.ExitCode(), err: err}
	}
	return err
}

// runLocal runs c with a timeout.
func (l *Local) runLocal(ctx context.Context, c gitCmd) result {
	cctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	return run(cctx, l.gitPath, c)
}

// viewRun is the runner of the reads through the read view: tree and blob
// reads of the repository's objects (tree.go), with the local settings.
func (l *Local) viewRun() *gitRun {
	return &gitRun{path: l.gitPath, gitDir: l.commonDir, dir: l.tmp, home: l.env.home, view: l.view, local: l.env}
}

// hexRE is an abbreviated or full object name.
var hexRE = regexp.MustCompile(`^[0-9a-fA-F]{4,64}$`)

// ResolveCommit returns the commit rev names in the repository: a ref name
// (as git resolves it: HEAD, a branch, a remote-tracking branch, a tag) or
// a hexadecimal object name. ok is false when rev names no commit. rev
// must be a valid ref name or hexadecimal (ValidLocalRev).
func (l *Local) ResolveCommit(ctx context.Context, rev string) (sha string, ok bool, err error) {
	if !ValidLocalRev(rev) {
		return "", false, nil
	}
	// rev cannot start with "-" (ValidLocalRev), and with ^{commit} it is one
	// revision argument; rev-parse of git 2.31 does not know --end-of-options
	// outside --verify's own parsing.
	out, err := l.inRepo(ctx, l.top, gitCmd{args: []string{"rev-parse", "--verify", "--quiet", rev + "^{commit}"}})
	if isExit(err, 1) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	sha = strings.ToLower(strings.TrimSpace(string(out)))
	if !shaRE.MatchString(sha) {
		return "", false, localFail(LocalGitFailed)
	}
	return sha, true, nil
}

// ValidLocalRev reports whether rev can be passed to git as a revision of a
// local repository: "HEAD", a hexadecimal object name of 4 to 64 digits,
// or a valid ref name (git check-ref-format rules, one or more segments)
// that cannot be read as an option or a revision expression.
func ValidLocalRev(rev string) bool {
	if rev == "HEAD" || hexRE.MatchString(rev) {
		return true
	}
	if rev == "" || len(rev) > 255 || strings.HasPrefix(rev, "-") {
		return false
	}
	for i := 0; i < len(rev); i++ {
		c := rev[i]
		if c < 0x21 || c == 0x7f || strings.IndexByte("~^:?*[\\", c) >= 0 {
			return false
		}
	}
	if strings.Contains(rev, "..") || strings.Contains(rev, "@{") || strings.Contains(rev, "//") ||
		strings.HasPrefix(rev, "/") || strings.HasSuffix(rev, "/") || strings.HasSuffix(rev, ".") || rev == "@" {
		return false
	}
	for _, seg := range strings.Split(rev, "/") {
		if strings.HasPrefix(seg, ".") || strings.HasSuffix(seg, ".lock") {
			return false
		}
	}
	return true
}

// FullRefName returns the full name of the ref rev names as git resolves it
// (refs/heads/main for main, refs/remotes/origin/main for origin/main,
// refs/tags/v1 for v1), or "" when rev is an object name or names nothing.
func (l *Local) FullRefName(ctx context.Context, rev string) (string, error) {
	if !ValidLocalRev(rev) {
		return "", nil
	}
	out, err := l.inRepo(ctx, l.top, gitCmd{args: []string{"rev-parse", "--verify", "--quiet", "--symbolic-full-name", rev}})
	if isExit(err, 1) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Upstream returns the full name of the current branch's upstream
// (refs/remotes/origin/main, or refs/heads/x for a local upstream), or ""
// when HEAD is detached or the branch has none.
func (l *Local) Upstream(ctx context.Context) (string, error) {
	if l.branch == "" {
		return "", nil
	}
	out, err := l.inRepo(ctx, l.top, gitCmd{args: []string{"for-each-ref", "--format=%(upstream)", "--end-of-options", l.branch}})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase returns the best common ancestor of the commits a and b; ok is
// false when there is none (unrelated histories, or a shallow clone that
// lacks it).
func (l *Local) MergeBase(ctx context.Context, a, b string) (sha string, ok bool, err error) {
	if !shaRE.MatchString(a) || !shaRE.MatchString(b) {
		return "", false, localFail(LocalGitFailed)
	}
	out, err := l.inRepo(ctx, l.top, gitCmd{args: []string{"merge-base", a, b}})
	if isExit(err, 1) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	sha = strings.ToLower(strings.TrimSpace(string(out)))
	if !shaRE.MatchString(sha) {
		return "", false, localFail(LocalGitFailed)
	}
	return sha, true, nil
}

// LocalCommit is one commit of a range: its full message.
type LocalCommit struct {
	// Message is the commit message as stored, with a trailing newline
	// trimmed.
	Message string
}

// Subject is the message's first line.
func (c LocalCommit) Subject() string {
	s, _, _ := strings.Cut(c.Message, "\n")
	return strings.TrimRight(s, "\r")
}

// Commits returns the commits reachable from to and not from from, newest
// first, at most max of them; more reports that there were more.
func (l *Local) Commits(ctx context.Context, from, to string, max int) (commits []LocalCommit, more bool, err error) {
	if !shaRE.MatchString(from) || !shaRE.MatchString(to) || max <= 0 {
		return nil, false, localFail(LocalGitFailed)
	}
	out, err := l.inRepo(ctx, l.top, gitCmd{args: []string{"log", "--format=%x00%B", "--no-color", "--no-show-signature",
		"--no-notes", "--max-count=" + strconv.Itoa(max+1), "--end-of-options", from + ".." + to}})
	if err != nil {
		return nil, false, err
	}
	for _, rec := range strings.Split(string(out), "\x00")[1:] {
		commits = append(commits, LocalCommit{Message: strings.TrimRight(rec, "\n")})
	}
	if len(commits) > max {
		return commits[:max], true, nil
	}
	return commits, false, nil
}

// Diff returns the output of git diff from the commit from to the commit
// to, or to the work tree (tracked files: staged and unstaged changes)
// when workTree is set, at most maxBytes bytes (more is too_large).
//
// Both run with --no-ext-diff --no-textconv --no-color -M, three lines of
// context, the a/ and b/ prefixes, and submodules shown as their commits
// only. The diff of two commits runs in the repository. The diff against
// the work tree runs through the read view with the repository as its work
// tree and a private copy of the index: the view's configuration defines no
// filter, so no clean filter or filter process can run while git reads the
// work tree; core.autocrlf=input lets a work tree checked out with CRLF line
// endings compare equal to blobs stored with LF (git leaves alone a file
// whose blob already has CRLF), and core.fileMode=false keeps a file system
// without executable bits from marking files as changed. On Windows
// core.symlinks=false is set for the same reason.
func (l *Local) Diff(ctx context.Context, from, to string, workTree bool, maxBytes int) ([]byte, error) {
	if !shaRE.MatchString(from) || (!workTree && !shaRE.MatchString(to)) || maxBytes <= 0 {
		return nil, localFail(LocalGitFailed)
	}
	flags := []string{"--no-ext-diff", "--no-textconv", "--no-color", "-M", "-U3",
		"--src-prefix=a/", "--dst-prefix=b/", "--no-relative"}
	if !workTree {
		args := append([]string{"-c", "diff.suppressBlankEmpty=false", "-c", "diff.noprefix=false", "diff"}, flags...)
		args = append(args, "--ignore-submodules=none", "--end-of-options", from, to, "--")
		out, err := l.inRepo(ctx, l.top, gitCmd{args: args, maxStdout: maxBytes})
		return out, err
	}
	copyDir, idx, err := l.copyIndex()
	if copyDir != "" {
		defer l.removeTemp(copyDir)
	}
	if err != nil {
		return nil, err
	}
	args := []string{"--git-dir=" + l.view, "--work-tree=" + l.top,
		"-c", "core.autocrlf=input", "-c", "core.fileMode=false"}
	if runtime.GOOS == "windows" {
		args = append(args, "-c", "core.symlinks=false")
	}
	args = append(args, "diff")
	args = append(args, flags...)
	args = append(args, "--ignore-submodules=dirty", "--end-of-options", from, "--")
	c := gitCmd{args: args, dir: l.top, local: l.env, maxStdout: maxBytes,
		objectDir: filepath.Join(l.commonDir, "objects"), indexFile: idx}
	res := l.runLocal(ctx, c)
	if res.err != nil {
		return nil, localFailure(res)
	}
	if res.truncated {
		return nil, localFail(LocalTooLarge)
	}
	return res.stdout, nil
}

// copyIndex copies the work tree's index, and any shared index files of a
// split index next to it, into a fresh directory under the temporary
// directory. It returns that directory (to be removed with removeTemp, also
// after a failure) and the copy's path. A missing index is an empty one.
func (l *Local) copyIndex() (dir, index string, err error) {
	dir = filepath.Join(l.tmp, "index-"+strconv.FormatInt(l.seq.Add(1), 10))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", "", localFail(LocalTempDir)
	}
	dst := filepath.Join(dir, "index")
	if err := copyRegular(filepath.Join(l.gitDir, "index"), dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return dir, "", localFail(LocalGitFailed)
	}
	// A split index names its shared part by hash; git looks for it next to
	// the index file it reads.
	ents, err := os.ReadDir(l.gitDir)
	if err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "sharedindex.") && e.Type().IsRegular() {
				if err := copyRegular(filepath.Join(l.gitDir, e.Name()), filepath.Join(dir, e.Name())); err != nil {
					return dir, "", localFail(LocalGitFailed)
				}
			}
		}
	}
	return dir, dst, nil
}

// removeTemp removes dir, a directory this Local created in its temporary
// directory; it refuses anything else, so that no path of the repository
// can ever be removed.
func (l *Local) removeTemp(dir string) {
	if within(l.tmp, dir) {
		_ = os.RemoveAll(dir)
	}
}

// copyRegular copies the regular file src (not followed when it is a
// link) to the new file dst.
func copyRegular(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	in, err := os.Open(src) //nolint:gosec // G304: the index of the repository the user named, read only
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a new file in review-mcp's own temporary directory
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Untracked returns the untracked files of the work tree that the
// repository's ignore rules (.gitignore files, .git/info/exclude and
// core.excludesFile of the repository's configuration) do not exclude, as
// slash-separated paths relative to the top level, in git's order.
func (l *Local) Untracked(ctx context.Context) ([]string, error) {
	out, err := l.inRepo(ctx, l.top, gitCmd{args: []string{"ls-files", "-z", "--others", "--exclude-standard", "--full-name"},
		maxStdout: maxListOutput})
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	return paths, nil
}

// FileAt returns the content of the file path at commit rev, at most
// maxBytes bytes (larger is a provider too_large error). A path that is not
// a file at rev is a provider not_found error. It reads through the read
// view: cat-file prints the stored blob, with no filter or textconv.
func (l *Local) FileAt(ctx context.Context, rev, path string, maxBytes int) ([]byte, error) {
	rev = strings.ToLower(rev)
	if !shaRE.MatchString(rev) || !usablePath(path) || path == "" {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	g := l.viewRun()
	dir, name := "", path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		dir, name = path[:i], path[i+1:]
	}
	entries, err := g.listTree(ctx, rev, dir)
	if err != nil {
		return nil, localReadErr(err)
	}
	for _, e := range entries {
		if e.name == name && e.typ == "blob" {
			b, err := g.readBlob(ctx, e.oid, maxBytes)
			return b, localReadErr(err)
		}
	}
	return nil, &provider.Error{Class: provider.ClassNotFound}
}

// ListDir returns the direct entries of directory dir ("" is the root) at
// commit rev, through the read view (ls-tree), capped like the providers'.
func (l *Local) ListDir(ctx context.Context, rev, dir string) (*provider.DirListing, error) {
	rev = strings.ToLower(rev)
	if !shaRE.MatchString(rev) || !usablePath(dir) {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	entries, err := l.viewRun().listTree(ctx, rev, dir)
	if err != nil {
		return nil, localReadErr(err)
	}
	return provider.FinishDirListing(dirEntries(entries), false), nil
}

// localReadErr keeps provider errors (not_found, too_large) and turns this
// package's *Error into a *LocalError.
func localReadErr(err error) error {
	if err == nil {
		return nil
	}
	var pe *provider.Error
	if errors.As(err, &pe) {
		return err
	}
	var le *LocalError
	if errors.As(err, &le) {
		return err
	}
	switch ReasonOf(err) {
	case ReasonBlobMissing:
		return localFail(LocalObjectsMissing)
	case ReasonTimeout:
		return localFail(LocalTimeout)
	}
	return localFail(LocalGitFailed)
}

// WorkTreeFile reads the file path (slash-separated, relative to the top
// level) from the work tree, at most maxBytes bytes (larger is a provider
// too_large error). The read is confined to the work tree (os.Root): no
// symbolic link is followed out of it. A path that is a symbolic link
// yields its target, as git stores it; a missing path, a directory or
// anything else is not_found.
func (l *Local) WorkTreeFile(path string, maxBytes int) ([]byte, error) {
	if !usablePath(path) || path == "" || !filepath.IsLocal(filepath.FromSlash(path)) {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	root, err := os.OpenRoot(l.top)
	if err != nil {
		return nil, localFail(LocalNotFound)
	}
	defer func() { _ = root.Close() }()
	name := filepath.FromSlash(path)
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := root.Readlink(name)
		if err != nil {
			return nil, &provider.Error{Class: provider.ClassNotFound}
		}
		return []byte(filepath.ToSlash(target)), nil
	case !fi.Mode().IsRegular():
		return nil, &provider.Error{Class: provider.ClassNotFound}
	case fi.Size() > int64(maxBytes):
		return nil, &provider.Error{Class: provider.ClassTooLarge}
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return nil, localFail(LocalGitFailed)
	}
	if len(b) > maxBytes {
		return nil, &provider.Error{Class: provider.ClassTooLarge}
	}
	return b, nil
}
