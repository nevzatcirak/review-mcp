// Package incremental finds the files of a pull request whose content
// changed since the last review review-mcp published on it (X-36, V-1;
// spec v2.1 §3, WP-7c). It is detection only: it reads, compares and
// reports, and it decides nothing about the review itself.
//
// Every listed file of the current diff is compared between the commit the
// last review recorded and the current head, by content, so a rebase or a
// force-push needs no special case: a file whose content is the same at both
// commits is unchanged, whatever happened to the history in between.
//
// Two strategies, chosen in this order:
//
//   - git:diff, when repository context (X-22) is enabled and its cache
//     holds both commits: one offline "git diff --name-only" of the two
//     commits in the cache (gitctx.Runner.ChangedPaths) and the file lists
//     of both commits (gitctx.Runner.ListPaths). A file is changed when its
//     path or its old path is listed by the diff, and unchanged only when
//     git also knows its path: in both commits, or for a deleted file in
//     neither.
//   - api:contents, otherwise: the file at the last reviewed commit is read
//     through the provider and compared byte for byte with its content at
//     the head.
//
// Doubt always means "changed": a read error, a file too large to read, a
// file present on one side only. A comparison that cannot be made at all
// (the last reviewed commit is gone or unreadable, or the pull request has
// too many files for the API strategy) is a full review with one fixed
// note. Nothing is built or run (R-1): the local strategy runs git plumbing
// offline, with no external diff program and no textconv filter.
package incremental

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Strategies reported in Result.Strategy. A full review has none ("").
const (
	StrategyGit = "git:diff"
	StrategyAPI = "api:contents"
)

// DefaultMaxFiles is the default of review.incremental_max_files (owner
// default 2): the most listed files the API strategy compares.
const DefaultMaxFiles = 300

// Notes of a full review (spec §3 item 4, verbatim).
const (
	// NoteCommitGone: the last reviewed commit no longer exists (a
	// force-push followed by garbage collection, for example).
	NoteCommitGone = "The commit of the last review is no longer available, so the whole pull request was reviewed."
	// NoteNotCompared: the last reviewed commit could not be read for
	// another reason, or the comparison could not be made at all.
	NoteNotCompared = "The last review could not be compared with the current head, so the whole pull request was reviewed."
)

// NoteTooManyFiles is the note of a pull request with more than max listed
// files, which the API strategy does not compare.
func NoteTooManyFiles(max int) string {
	return "The pull request has more than " + strconv.Itoa(max) +
		" files, so it was not compared with the last review and was reviewed in full."
}

// State is the outcome for one file.
type State string

// File states.
const (
	Changed   State = "changed"
	Unchanged State = "unchanged"
)

// File is the outcome for one listed file of the diff.
type File struct {
	// Path and OldPath are the file's FilePatch.Path and FilePatch.OldPath.
	Path, OldPath string
	State         State
}

// Result is what Detect found.
type Result struct {
	// Full is true when no comparison was made or none could be used: the
	// whole pull request is to be reviewed, Files is nil, Strategy is "" and
	// Notes holds the reason as one fixed sentence.
	Full bool
	// Strategy is StrategyGit or StrategyAPI; "" for a full review.
	Strategy string
	// Files has one entry per file of Input.Files, in the same order.
	Files []File
	// Notes are fixed sentences; exactly one for a full review, none
	// otherwise.
	Notes []string
}

// Counts returns the number of changed and unchanged files.
func (r Result) Counts() (changed, unchanged int) {
	for _, f := range r.Files {
		if f.State == Unchanged {
			unchanged++
		} else {
			changed++
		}
	}
	return changed, unchanged
}

// Reader is what the API strategy needs from the provider.
// provider.Provider implements it.
type Reader interface {
	GetFileAt(ctx context.Context, ref provider.PRRef, path, rev string) ([]byte, error)
	ListDir(ctx context.Context, ref provider.PRRef, dir, rev string) (*provider.DirListing, error)
}

var _ Reader = provider.Provider(nil)

// Local is what the git strategy needs from repository context.
// *gitctx.Runner implements it.
type Local interface {
	HasCommit(ctx context.Context, c gitctx.Checkout, rev string) bool
	ChangedPaths(ctx context.Context, c gitctx.Checkout, from, to string) ([]string, error)
	ListPaths(ctx context.Context, c gitctx.Checkout, rev string) ([]string, error)
}

var _ Local = (*gitctx.Runner)(nil)

// Input is one detection.
type Input struct {
	Ref provider.PRRef
	// Files are the listed files of the current diff (provider.Diff.Files).
	// Only they are classified: a file the provider skipped or filtered
	// (Diff.Skipped) is not listed, and neither is one the host did not
	// list at all (Diff.Incomplete).
	Files []provider.FilePatch
	// Head is the current head commit (PullRequest.HeadSHA, the revision
	// the diff's head contents were read at); Last is the head commit the
	// last review recorded (WP-7b). Both are full SHAs.
	Head, Last string
	// Provider reads the files of the API strategy.
	Provider Reader
	// Local and Checkout are the repository context of this run: nil Local
	// when it is off or the pull request head was not fetched by this run
	// (X-32: the cache answers only then).
	Local    Local
	Checkout gitctx.Checkout
	// MaxFiles is review.incremental_max_files; 0 or less means
	// DefaultMaxFiles.
	MaxFiles int
	// Concurrency bounds the parallel reads of the API strategy; 0 or less
	// means provider.ContentFetchConcurrency, the bound of the providers'
	// own content fetches.
	Concurrency int
}

var shaRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Detect classifies every file of in.Files as changed or unchanged since
// in.Last, or reports a full review with a fixed note.
//
// The git strategy is used when in.Local holds both commits. If the
// comparison fails there (a tree the shallow or partial cache lacks, output
// past its cap, any git failure), the API strategy runs instead (WP-7c
// DQ-3, below). The API strategy first checks that the last
// reviewed commit is readable (ListDir of its root): not_found is a full
// review with NoteCommitGone, any other error one with NoteNotCompared. A
// pull request with more than MaxFiles listed files is a full review with
// NoteTooManyFiles before any request.
//
// DESIGN-QUESTION (WP-7c DQ-4): the limit is checked before the probe of the
// last commit, so a pull request above it costs no request at all (the
// review is full either way); it counts the listed files, the ones that
// would be read; and it applies to the API strategy only, as owner default 2
// says ("API change detection reads at most 300 files"): the git strategy is
// one local command whatever the size. Recommendation: keep.
//
// DESIGN-QUESTION (WP-7c DQ-9): the error is non-nil only when ctx ends
// (the review is being given up, so no note is due); nothing is then
// reported. Recommendation: keep.
func Detect(ctx context.Context, in Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	head, last := strings.ToLower(in.Head), strings.ToLower(in.Last)
	if !shaRE.MatchString(head) || !shaRE.MatchString(last) {
		// DESIGN-QUESTION (WP-7c DQ-8): WP-7b reads only well-formed SHAs, so
		// this is a caller's mistake; it is reported like an unreadable
		// commit rather than as an error.
		return full(NoteNotCompared), nil
	}

	if in.Local != nil && in.Local.HasCommit(ctx, in.Checkout, head) && in.Local.HasCommit(ctx, in.Checkout, last) {
		if res, err := byGit(ctx, in, head, last); err == nil {
			return res, nil
		}
		// DESIGN-QUESTION (WP-7c DQ-3): the spec does not say what follows a
		// failed comparison in the cache (a tree the shallow or partial
		// cache lacks, output past the cap, a git failure). It is not a
		// skip and not yet a full review: the provider compares instead,
		// as every other cache read falls back to the API (X-32).
		// Recommendation: keep.
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	maxFiles := in.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	if len(in.Files) > maxFiles {
		return full(NoteTooManyFiles(maxFiles)), nil
	}
	if in.Provider == nil {
		return full(NoteNotCompared), nil
	}
	if _, err := in.Provider.ListDir(ctx, in.Ref, "", last); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return Result{}, cerr
		}
		if errors.Is(err, provider.ErrNotFound) {
			return full(NoteCommitGone), nil
		}
		return full(NoteNotCompared), nil
	}
	return byContents(ctx, in, head, last)
}

func full(note string) Result {
	return Result{Full: true, Notes: []string{note}}
}

// byGit runs the git strategy: the paths that changed between the two
// commits and the file lists of both. Any error ends it (the caller then
// compares through the provider, or returns when ctx ended).
func byGit(ctx context.Context, in Input, head, last string) (Result, error) {
	changed, err := in.Local.ChangedPaths(ctx, in.Checkout, last, head)
	if err != nil {
		return Result{}, err
	}
	atHead, err := in.Local.ListPaths(ctx, in.Checkout, head)
	if err != nil {
		return Result{}, err
	}
	atLast, err := in.Local.ListPaths(ctx, in.Checkout, last)
	if err != nil {
		return Result{}, err
	}
	return byPaths(in.Files, changed, atHead, atLast), nil
}

func toSet(paths []string) map[string]bool {
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		m[p] = true
	}
	return m
}

// byPaths classifies the files by the paths git listed between the two
// commits: a file is changed when its path or its old path is among them.
// Otherwise it is unchanged only when git knows its path: a deleted file's
// path is in neither commit's file list, any other file's path is in both.
// Anything else is changed, so a provider path git never matched (another
// byte form of the same name, a path present on one side only) is never
// taken for unchanged (DQ-7, settled by the architect: the presence check).
//
// DESIGN-QUESTION (WP-7c DQ-6): the two strategies can differ in two rare
// cases, both towards "changed" in the git strategy: a change of the file
// mode alone (git lists the path; the content is the same) and a renamed
// file whose old path was touched again since the last review. The spec
// defines both rules as they are. Recommendation: keep.
//
// The old path of a rename is only matched against the diff, as before.
func byPaths(files []provider.FilePatch, changedPaths, atHead, atLast []string) Result {
	changed, head, last := toSet(changedPaths), toSet(atHead), toSet(atLast)
	out := Result{Strategy: StrategyGit, Files: make([]File, len(files))}
	for i, f := range files {
		st := Changed
		if !changed[f.Path] && (f.OldPath == "" || !changed[f.OldPath]) {
			if f.Type == provider.ChangeDeleted {
				if !head[f.Path] && !last[f.Path] {
					st = Unchanged
				}
			} else if head[f.Path] && last[f.Path] {
				st = Unchanged
			}
		}
		out.Files[i] = File{Path: f.Path, OldPath: f.OldPath, State: st}
	}
	return out
}

// byContents compares every file through the provider, at most
// in.Concurrency files at a time. Every goroutine has finished when it
// returns; once ctx ends no further file is started and the context's error
// is returned.
func byContents(ctx context.Context, in Input, head, last string) (Result, error) {
	limit := in.Concurrency
	if limit <= 0 {
		limit = provider.ContentFetchConcurrency
	}
	states := make([]State, len(in.Files))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
launch:
	for i := range in.Files {
		select {
		case <-ctx.Done():
			break launch
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			// A slot freed by a read that gave up: select picks at random.
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			states[i] = compare(ctx, in.Provider, in.Ref, &in.Files[i], head, last)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	out := Result{Strategy: StrategyAPI, Files: make([]File, len(in.Files))}
	for i, f := range in.Files {
		out.Files[i] = File{Path: f.Path, OldPath: f.OldPath, State: states[i]}
	}
	return out, nil
}

// compare decides one file by content. The file is read at the last
// reviewed commit under its current path (for a deletion, the path it had):
//
//   - not_found there: a deleted file is unchanged (absent on both sides);
//     any other file is changed (present on one side only, which is also a
//     rename since the last review);
//   - too_large or any other error: changed;
//   - otherwise its head content (the diff's, when fetched in full, else
//     read at head) must be byte for byte the same, and any error reading it
//     is changed. A deleted file present at the last commit is changed.
func compare(ctx context.Context, r Reader, ref provider.PRRef, f *provider.FilePatch, head, last string) State {
	if f.Path == "" {
		return Changed
	}
	before, err := r.GetFileAt(ctx, ref, f.Path, last)
	absent := errors.Is(err, provider.ErrNotFound)
	if err != nil && !absent {
		return Changed
	}
	if f.Type == provider.ChangeDeleted {
		if absent {
			return Unchanged
		}
		return Changed
	}
	if absent {
		return Changed
	}
	if f.HeadStatus == provider.ContentFull && f.HeadContent != nil {
		if string(before) == *f.HeadContent {
			return Unchanged
		}
		return Changed
	}
	now, err := r.GetFileAt(ctx, ref, f.Path, head)
	if err != nil || string(before) != string(now) {
		return Changed
	}
	return Unchanged
}
