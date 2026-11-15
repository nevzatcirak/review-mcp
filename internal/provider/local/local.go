// Package local is the read-only source of a local review (V-20, X-52;
// spec docs/plan/v2.10-V20-spec.md §1.2 and §1.3): work on the user's own
// machine that has not been pushed, presented to the review pipeline as a
// provider of kind "local".
//
// A Source is one repository and one base. It implements the read methods
// of provider.Provider from git (internal/gitctx's Local, which reads the
// repository and never writes to it, runs nothing from it and never uses
// the network); every write method returns ClassUnsupported before any git
// call. No URL resolves to a Source: the provider Resolver has no factory
// for it, so a tool that takes a pull request URL can never reach the
// user's disk.
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/gitdiff"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	// MaxDescriptionSubjects is the most commit subjects the description
	// lists (newest first).
	MaxDescriptionSubjects = 20
	// MaxCommits is the most commit messages GetCommitMessages returns, the
	// newest ones, as GitHub caps a pull request's commits at 250.
	MaxCommits = 250
	// UncommittedTitle is the title when no commit follows the merge base.
	UncommittedTitle = "Uncommitted changes"
	// binarySniff is how much of a file is searched for a NUL byte, as git
	// does, to tell a binary untracked file.
	binarySniff = 8000
)

// Reasons of an Error.
const (
	// ReasonNoBase: no base was given and none of the default candidates
	// exists.
	ReasonNoBase = "no_base"
	// ReasonBaseInvalid: base is neither a ref name nor a hexadecimal
	// commit.
	ReasonBaseInvalid = "base_invalid"
	// ReasonBaseNotFound: base names no commit in the repository.
	ReasonBaseNotFound = "base_not_found"
	// ReasonNoMergeBase: the base and HEAD have no common commit.
	ReasonNoMergeBase = "no_merge_base"
)

var sentences = map[string]string{
	ReasonNoBase: "no base was found: tried the current branch's upstream, origin/HEAD, origin/main, origin/master, " +
		"main and master; pass base (a branch, a tag or a commit)",
	ReasonBaseInvalid:  "base must be a ref name (such as main or origin/main) or a hexadecimal commit",
	ReasonBaseNotFound: "base names no commit in the repository",
	ReasonNoMergeBase: "the base and HEAD have no common commit (unrelated histories, or a shallow clone that lacks it), " +
		"so there is nothing to compare against",
}

// Error is a refusal of a local review's base. It carries a fixed reason.
type Error struct {
	Reason string
}

func (e *Error) Error() string { return "local review: " + sentences[e.Reason] }

// Is matches an *Error of the same reason.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Reason == e.Reason
}

// ErrLocalNoBase is returned when no base was given and none of the
// default candidates (§0 item 5) exists. Its message names what was tried
// and how to pass base.
var ErrLocalNoBase = &Error{Reason: ReasonNoBase}

// defaultCandidates follow the current branch's upstream.
var defaultCandidates = []string{
	"refs/remotes/origin/HEAD",
	"refs/remotes/origin/main",
	"refs/remotes/origin/master",
	"refs/heads/main",
	"refs/heads/master",
}

// Request is what a local review reviews.
type Request struct {
	// RepoPath is the absolute path of the work tree or a directory in it.
	RepoPath string
	// Base is a ref name or a hexadecimal commit; "" tries the defaults.
	Base string
	// IncludeUncommitted adds the uncommitted changes to tracked files,
	// staged and unstaged: the head side is the work tree.
	IncludeUncommitted bool
	// IncludeUntracked adds the untracked files the repository's ignore
	// rules do not exclude, as added files.
	IncludeUntracked bool
}

// DefaultRequest is the request of a local review of repoPath with the
// owner's defaults (§0 item 2): uncommitted changes to tracked files
// included, untracked files not, and the default base. The tool and the CLI
// start from it.
func DefaultRequest(repoPath string) Request {
	return Request{RepoPath: repoPath, IncludeUncommitted: true}
}

// Options are the settings of a Source.
type Options struct {
	// GitPath is the git binary; "" is "git" in PATH.
	GitPath string
	// AllowedRoots is local.allowed_roots.
	AllowedRoots []string
	// TempDir is the parent of the temporary directory; "" is the system's.
	TempDir string
	// MaxFiles, MaxFileBytes and MaxDiffBytes are diff.max_files_full_content,
	// diff.max_file_bytes and diff.max_diff_bytes, as for the providers.
	MaxFiles, MaxFileBytes, MaxDiffBytes int
	// Logger receives debug lines with counts only; nil discards.
	Logger *slog.Logger
}

// OptionsFrom takes the diff limits from cfg.
func OptionsFrom(cfg *config.Config) Options {
	return Options{
		MaxFiles:     cfg.Diff.MaxFilesFullContent,
		MaxFileBytes: cfg.Diff.MaxFileBytes,
		MaxDiffBytes: cfg.Diff.MaxDiffBytes,
	}
}

// Info describes what a Source reviews (the result's local object, §2.1).
type Info struct {
	// RepoPath is the work tree's absolute path.
	RepoPath string
	// BaseRef is the base as given, or the default candidate that was found
	// (short form: origin/main, main, or the upstream's short name).
	BaseRef string
	// BaseSHA is the commit BaseRef named; MergeBase the merge base of it
	// and HeadSHA, the commit HEAD named.
	BaseSHA, MergeBase, HeadSHA string
	// BaseRemote is true when the base is a remote-tracking branch: the
	// local copy, which may be behind the remote.
	BaseRemote bool
	// Uncommitted is IncludeUncommitted; UntrackedFiles the number of
	// untracked files included (0 without IncludeUntracked).
	Uncommitted    bool
	UntrackedFiles int
	// HeldBack is the number of untracked files left out because their
	// base names suggest secrets (gitctx.LikelySecret): the result's
	// local.untracked_held_back.
	HeldBack int
	// Branch is the current branch's short name, "" for a detached HEAD.
	Branch string
}

// Source is the read-only provider of one local review.
type Source struct {
	repo      *gitctx.Local
	opts      Options
	req       Request
	info      Info
	commits   []gitctx.LocalCommit // newest first, after the merge base
	untracked []string
	heldBack  int
	log       *slog.Logger
}

var _ provider.Provider = (*Source)(nil)

// Open opens the repository at req.RepoPath, resolves the base and the
// merge base, and reads the commits after it (and the untracked files when
// asked). Close releases it.
func Open(ctx context.Context, opts Options, req Request) (*Source, error) {
	repo, err := gitctx.OpenLocal(ctx, gitctx.LocalOptions{GitPath: opts.GitPath, AllowedRoots: opts.AllowedRoots,
		TempDir: opts.TempDir}, req.RepoPath)
	if err != nil {
		return nil, err
	}
	s := &Source{repo: repo, opts: opts, req: req, log: opts.Logger}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if err := s.init(ctx); err != nil {
		_ = repo.Close()
		return nil, err
	}
	return s, nil
}

func (s *Source) init(ctx context.Context) error {
	head := s.repo.HeadSHA()
	baseRef, baseFull, baseSHA, err := s.resolveBase(ctx)
	if err != nil {
		return err
	}
	mb, ok, err := s.repo.MergeBase(ctx, baseSHA, head)
	if err != nil {
		return err
	}
	if !ok {
		return &Error{Reason: ReasonNoMergeBase}
	}
	commits, _, err := s.repo.Commits(ctx, mb, head, MaxCommits)
	if err != nil {
		return err
	}
	s.commits = commits
	if s.req.IncludeUntracked {
		if s.untracked, s.heldBack, err = s.repo.Untracked(ctx); err != nil {
			return err
		}
	}
	s.info = Info{
		RepoPath:       s.repo.Top(),
		BaseRef:        baseRef,
		BaseSHA:        baseSHA,
		MergeBase:      mb,
		HeadSHA:        head,
		BaseRemote:     strings.HasPrefix(baseFull, "refs/remotes/"),
		Uncommitted:    s.req.IncludeUncommitted,
		UntrackedFiles: len(s.untracked),
		HeldBack:       s.heldBack,
		Branch:         strings.TrimPrefix(s.repo.Branch(), "refs/heads/"),
	}
	s.log.Debug("local review opened", "commits", len(commits), "uncommitted", s.req.IncludeUncommitted,
		"untracked", len(s.untracked), "held_back", s.heldBack, "base_remote", s.info.BaseRemote)
	return nil
}

// shortRef is the name a ref is shown with: without refs/heads/ or
// refs/remotes/.
func shortRef(ref string) string {
	if s, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return s
	}
	if s, ok := strings.CutPrefix(ref, "refs/remotes/"); ok {
		return s
	}
	return ref
}

// resolveBase returns the base's display name, its full ref name ("" for
// an object name) and its commit: req.Base when given (a ref name or a
// hexadecimal commit), otherwise the first of the current branch's
// upstream, origin/HEAD, origin/main, origin/master, main and master that
// names a commit. No fetch happens (§0 item 5).
func (s *Source) resolveBase(ctx context.Context) (name, full, sha string, err error) {
	if s.req.Base != "" {
		if !gitctx.ValidLocalRev(s.req.Base) || s.req.Base == "HEAD" {
			return "", "", "", &Error{Reason: ReasonBaseInvalid}
		}
		sha, ok, err := s.repo.ResolveCommit(ctx, s.req.Base)
		if err != nil {
			return "", "", "", err
		}
		if !ok {
			return "", "", "", &Error{Reason: ReasonBaseNotFound}
		}
		full, err := s.repo.FullRefName(ctx, s.req.Base)
		if err != nil {
			return "", "", "", err
		}
		return s.req.Base, full, sha, nil
	}
	refs := slices.Clone(defaultCandidates)
	up, err := s.repo.Upstream(ctx)
	if err != nil {
		return "", "", "", err
	}
	if up != "" {
		refs = append([]string{up}, refs...)
	}
	for _, ref := range refs {
		sha, ok, err := s.repo.ResolveCommit(ctx, ref)
		if err != nil {
			return "", "", "", err
		}
		if ok {
			return shortRef(ref), ref, sha, nil
		}
	}
	return "", "", "", ErrLocalNoBase
}

// Info describes what the Source reviews.
func (s *Source) Info() Info { return s.info }

// Ref is the reference of the local review: kind local and the work
// tree's base name as the repository; no URL and no number.
func (s *Source) Ref() provider.PRRef {
	return provider.PRRef{Kind: provider.KindLocal, Repo: filepath.Base(s.info.RepoPath)}
}

// Close removes the temporary directory; the repository is not touched.
func (s *Source) Close() error { return s.repo.Close() }

// Calls is the number of git processes started for the repository.
func (s *Source) Calls() int64 { return s.repo.Calls() }

// StaleBaseNote is the note of a review whose base is a remote-tracking
// branch (§0 item 5), or "".
func (s *Source) StaleBaseNote() string {
	if !s.info.BaseRemote {
		return ""
	}
	return "the base " + s.info.BaseRef + " is the local copy of a remote-tracking branch and may be behind the remote; " +
		"nothing was fetched"
}

// HeldBackNote is the note of the untracked files held back as likely
// secrets (gitctx.NoteUntrackedHeldBack), or "" when there are none.
func (s *Source) HeldBackNote() string {
	if s.heldBack == 0 {
		return ""
	}
	return fmt.Sprintf(gitctx.NoteUntrackedHeldBack, s.heldBack)
}

// EmptyNote is the note of a review with nothing to review (§1.3): it
// names the base and what was included.
func (s *Source) EmptyNote() string {
	parts := []string{"the commits after the merge base"}
	if s.info.Uncommitted {
		parts = append(parts, "uncommitted changes to tracked files")
	}
	if s.req.IncludeUntracked {
		parts = append(parts, "untracked files")
	}
	what := parts[0]
	switch len(parts) {
	case 2:
		what += " and " + parts[1]
	case 3:
		what += ", " + parts[1] + " and " + parts[2]
	}
	return "nothing to review against " + s.info.BaseRef + " (" + short(s.info.BaseSHA) + "): " + what + " are empty"
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// ---- provider.Provider: reads ----

// Kind implements provider.Provider.
func (s *Source) Kind() provider.Kind { return provider.KindLocal }

// Capabilities implements provider.Provider: everything off. A local
// review only reads; it has no markup to publish to, no threads, no
// issues, no decisions and no lists.
func (s *Source) Capabilities() provider.Capabilities { return provider.Capabilities{} }

// GetPullRequest implements provider.Provider. The title is the newest
// commit's subject after the merge base, or UncommittedTitle when there is
// none; the description lists at most MaxDescriptionSubjects subjects,
// newest first. The head is HEAD's commit and the base the merge base. No
// author, no URL.
func (s *Source) GetPullRequest(_ context.Context, _ provider.PRRef) (*provider.PullRequest, error) {
	title := UncommittedTitle
	if len(s.commits) > 0 {
		title = s.commits[0].Subject()
	}
	var lines []string
	for i, c := range s.commits {
		if i == MaxDescriptionSubjects {
			break
		}
		lines = append(lines, "- "+c.Subject())
	}
	return &provider.PullRequest{
		Title:        title,
		Description:  strings.Join(lines, "\n"),
		SourceBranch: s.info.Branch,
		TargetBranch: s.info.BaseRef,
		HeadSHA:      s.info.HeadSHA,
		BaseSHA:      s.info.MergeBase,
		BaseStrategy: provider.BaseLocalMergeBase,
		State:        "local",
		CommitCount:  len(s.commits),
	}, nil
}

// GetCommitMessages implements provider.Provider: the messages of the
// commits after the merge base, oldest first, at most MaxCommits (the
// newest).
func (s *Source) GetCommitMessages(_ context.Context, _ provider.PRRef) ([]string, error) {
	out := make([]string, len(s.commits))
	for i, c := range s.commits {
		out[len(s.commits)-1-i] = c.Message
	}
	return out, nil
}

// GetDiff implements provider.Provider: the merge base against HEAD, or
// against the work tree with IncludeUncommitted, plus the untracked files
// as added files with IncludeUntracked. The file, size and binary limits
// and the content statuses are the providers' (see the gitea provider): a
// binary file is skipped, a file past diff.max_files_full_content keeps its
// patch without contents, a side larger than diff.max_file_bytes is
// not_fetched_size_limit, and a side that cannot be read is fetch_failed,
// never an error for the whole diff. With nothing to review, Notes holds
// EmptyNote.
func (s *Source) GetDiff(ctx context.Context, _ provider.PRRef, _ *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	raw, err := s.repo.Diff(ctx, s.info.MergeBase, s.info.HeadSHA, s.info.Uncommitted, s.opts.MaxDiffBytes)
	if err != nil {
		return nil, err
	}
	parsed, err := gitdiff.Parse(bytes.NewReader(raw))
	if err != nil {
		s.log.Debug("local diff could not be parsed")
		return nil, &provider.Error{Class: provider.ClassProtocol, Hint: "the local diff could not be parsed"}
	}
	out := &provider.Diff{BaseStrategy: provider.BaseLocalMergeBase}
	for i := range parsed {
		df := &parsed[i]
		if opts.Include != nil && !opts.Include(df.Path) {
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: df.Path, Reason: provider.SkipFiltered})
			continue
		}
		if df.Binary {
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: df.Path, Reason: provider.SkipBinary})
			continue
		}
		fp := provider.FilePatch{Path: df.Path, OldPath: df.OldPath, Type: changeType(df.Type), Patch: df.Patch,
			Additions: df.Additions, Deletions: df.Deletions}
		s.fillContents(ctx, &fp, len(out.Files) >= s.opts.MaxFiles)
		out.Files = append(out.Files, fp)
	}
	size := len(raw)
	for _, path := range s.untracked {
		if opts.Include != nil && !opts.Include(path) {
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: path, Reason: provider.SkipFiltered})
			continue
		}
		fp, skip := s.untrackedFile(path, len(out.Files) >= s.opts.MaxFiles)
		if skip != "" {
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: path, Reason: skip})
			continue
		}
		if size += len(fp.Patch); size > s.opts.MaxDiffBytes {
			return nil, &gitctx.LocalError{Reason: gitctx.LocalTooLarge}
		}
		out.Files = append(out.Files, fp)
	}
	if len(out.Files) == 0 && len(out.Skipped) == 0 {
		out.Notes = []string{s.EmptyNote()}
	}
	s.log.Debug("local diff read", "files", len(out.Files), "skipped", len(out.Skipped), "bytes", size)
	return out, nil
}

func changeType(t gitdiff.ChangeType) provider.ChangeType {
	switch t {
	case gitdiff.Added:
		return provider.ChangeAdded
	case gitdiff.Deleted:
		return provider.ChangeDeleted
	case gitdiff.Renamed:
		return provider.ChangeRenamed
	default:
		return provider.ChangeModified
	}
}

// fillContents reads both sides of fp: the base at the merge base, the
// head at HEAD or, with uncommitted changes, from the work tree.
func (s *Source) fillContents(ctx context.Context, fp *provider.FilePatch, overLimit bool) {
	basePath := fp.Path
	if fp.Type == provider.ChangeRenamed && fp.OldPath != "" {
		basePath = fp.OldPath
	}
	switch {
	case fp.Type == provider.ChangeAdded:
		fp.BaseStatus = provider.ContentNotApplicable
	case overLimit:
		fp.BaseStatus = provider.ContentNotFetchedFileCap
	default:
		fp.BaseContent, fp.BaseStatus = s.content(s.repo.FileAt(ctx, s.info.MergeBase, basePath, s.opts.MaxFileBytes))
	}
	switch {
	case fp.Type == provider.ChangeDeleted:
		fp.HeadStatus = provider.ContentNotApplicable
	case overLimit:
		fp.HeadStatus = provider.ContentNotFetchedFileCap
	case s.info.Uncommitted:
		b, err := s.repo.WorkTreeFile(fp.Path, s.opts.MaxFileBytes)
		if err == nil {
			b = normalizeEOL(b)
		}
		fp.HeadContent, fp.HeadStatus = s.content(b, err)
	default:
		fp.HeadContent, fp.HeadStatus = s.content(s.repo.FileAt(ctx, s.info.HeadSHA, fp.Path, s.opts.MaxFileBytes))
	}
}

// content maps a read to a content and its status.
func (s *Source) content(b []byte, err error) (*string, provider.ContentStatus) {
	if err != nil {
		if errors.Is(err, provider.ErrTooLarge) {
			return nil, provider.ContentNotFetchedSizeCap
		}
		s.log.Debug("local content not read", "reason", reasonOf(err))
		return nil, provider.ContentFetchFailed
	}
	str := string(b)
	return &str, provider.ContentFull
}

// reasonOf is the fixed class or reason of a read error, for the log.
func reasonOf(err error) string {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return string(pe.Class)
	}
	var le *gitctx.LocalError
	if errors.As(err, &le) {
		return le.Reason
	}
	return "unknown"
}

// untrackedFile reads an untracked file as an added file. skip is a
// SkippedFile reason when the file is not listed: binary, larger than
// diff.max_file_bytes, or unreadable.
func (s *Source) untrackedFile(path string, overLimit bool) (provider.FilePatch, string) {
	b, err := s.repo.WorkTreeFile(path, s.opts.MaxFileBytes)
	switch {
	case errors.Is(err, provider.ErrTooLarge):
		return provider.FilePatch{}, provider.SkipSizeLimit
	case err != nil:
		return provider.FilePatch{}, provider.SkipFetchFailed
	case bytes.IndexByte(b[:min(len(b), binarySniff)], 0) >= 0:
		return provider.FilePatch{}, provider.SkipBinary
	}
	b = normalizeEOL(b)
	patch, n := addedPatch(string(b))
	fp := provider.FilePatch{Path: path, Type: provider.ChangeAdded, Patch: patch, Additions: n,
		BaseStatus: provider.ContentNotApplicable}
	if overLimit {
		fp.HeadStatus = provider.ContentNotFetchedFileCap
	} else {
		str := string(b)
		fp.HeadContent, fp.HeadStatus = &str, provider.ContentFull
	}
	return fp, ""
}

// normalizeEOL turns CRLF line endings into LF in a text file whose lines
// all end in CRLF, as git's core.autocrlf=input does when it reads a work
// tree file for the diff (Local.Diff); any other content is left as it is.
func normalizeEOL(b []byte) []byte {
	if bytes.IndexByte(b[:min(len(b), binarySniff)], 0) >= 0 || !bytes.Contains(b, []byte("\r\n")) {
		return b
	}
	if bytes.Count(b, []byte("\n")) != bytes.Count(b, []byte("\r\n")) {
		return b // mixed endings: git's text=auto leaves those alone too
	}
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// addedPatch is the hunk-only patch of a new file with content, in git's
// format, and its number of lines.
func addedPatch(content string) (string, int) {
	if content == "" {
		return "", 0
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var b strings.Builder
	b.WriteString("@@ -0,0 +1")
	if len(lines) != 1 {
		b.WriteString("," + strconv.Itoa(len(lines)))
	}
	b.WriteString(" @@\n")
	for _, l := range lines {
		b.WriteString("+" + l)
		if !strings.HasSuffix(l, "\n") {
			b.WriteString("\n\\ No newline at end of file\n")
		}
	}
	return b.String(), len(lines)
}

// GetFileAt implements provider.Provider: git cat-file of rev:path through
// the read view, capped at diff.max_file_bytes. With uncommitted changes,
// the head side (rev is HEAD's commit) is the work-tree file, read with no
// symbolic link followed out of the work tree.
func (s *Source) GetFileAt(ctx context.Context, _ provider.PRRef, path, rev string) ([]byte, error) {
	if !provider.ValidRevision(rev) {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	if s.info.Uncommitted && strings.EqualFold(rev, s.info.HeadSHA) {
		b, err := s.repo.WorkTreeFile(path, s.opts.MaxFileBytes)
		if err != nil {
			return nil, err
		}
		return normalizeEOL(b), nil
	}
	return s.repo.FileAt(ctx, rev, path, s.opts.MaxFileBytes)
}

// ListDir implements provider.Provider: git ls-tree at rev through the
// read view.
func (s *Source) ListDir(ctx context.Context, _ provider.PRRef, dir, rev string) (*provider.DirListing, error) {
	return s.repo.ListDir(ctx, rev, dir)
}

// FileLineURL implements provider.Provider: a local file has no URL; the
// markdown shows path:line.
func (s *Source) FileLineURL(provider.PRRef, *provider.PullRequest, string, int) string { return "" }

// ListThreads implements provider.Provider: local work has no discussion.
func (s *Source) ListThreads(context.Context, provider.PRRef) ([]provider.Thread, error) {
	return nil, nil
}

// GetReviewStatus implements provider.Provider: local work has no
// reviewers, required approvals or merge status; everything is unknown.
func (s *Source) GetReviewStatus(context.Context, provider.PRRef, *provider.PullRequest, provider.ReviewStatusOptions) *provider.ReviewStatus {
	return &provider.ReviewStatus{}
}

// errUnsupported is the error of every method a local review does not
// have. Nothing is run for it.
func errUnsupported() error {
	return &provider.Error{Class: provider.ClassUnsupported, Hint: "a local review only reads the repository"}
}

// CurrentUser implements provider.Provider: there is no token user.
func (s *Source) CurrentUser(context.Context) (provider.User, error) {
	return provider.User{}, errUnsupported()
}

// GetChecks implements provider.Provider: local work has no CI checks.
func (s *Source) GetChecks(context.Context, provider.PRRef, string) (*provider.Checks, error) {
	return nil, errUnsupported()
}

// ---- provider.Provider: writes (all unsupported, no git call) ----

// PostComment implements provider.Provider: unsupported.
func (s *Source) PostComment(context.Context, provider.PRRef, string) (*provider.Comment, error) {
	return nil, errUnsupported()
}

// ReplyToComment implements provider.Provider: unsupported.
func (s *Source) ReplyToComment(context.Context, provider.PRRef, string, string) (*provider.ReplyResult, error) {
	return nil, errUnsupported()
}

// EditComment implements provider.Provider: unsupported.
func (s *Source) EditComment(context.Context, provider.PRRef, string, string) error {
	return errUnsupported()
}

// PostInlineComments implements provider.Provider: unsupported.
func (s *Source) PostInlineComments(context.Context, provider.PRRef, *provider.PullRequest, []provider.InlineComment) ([]provider.InlineResult, error) {
	return nil, errUnsupported()
}

// UpdatePullRequest implements provider.Provider: unsupported.
func (s *Source) UpdatePullRequest(context.Context, provider.PRRef, provider.UpdatePR) error {
	return errUnsupported()
}
