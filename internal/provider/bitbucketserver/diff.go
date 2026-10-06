package bitbucketserver

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// binarySniffBytes is how much of each side is scanned for a NUL byte.
const binarySniffBytes = 8000

type apiChange struct {
	Path struct {
		ToString string `json:"toString"`
	} `json:"path"`
	SrcPath *struct {
		ToString string `json:"toString"`
	} `json:"srcPath"`
	Type string `json:"type"`
}

// classify maps a change to the provider-neutral type, the new path and the
// old path (set only for a real rename).
func (c *apiChange) classify() (t provider.ChangeType, path, oldPath string) {
	path = c.Path.ToString
	src := ""
	if c.SrcPath != nil {
		src = c.SrcPath.ToString
	}
	switch strings.ToUpper(c.Type) {
	case "ADD", "COPY":
		return provider.ChangeAdded, path, ""
	case "DELETE":
		return provider.ChangeDeleted, path, ""
	case "MOVE", "RENAME":
		if src == "" || src == path {
			return provider.ChangeModified, path, ""
		}
		return provider.ChangeRenamed, path, src
	default: // MODIFY and anything unknown
		return provider.ChangeModified, path, ""
	}
}

// validRawPath reports whether every segment of path is usable in a raw
// content request: no empty, "." or ".." segment.
func validRawPath(path string) bool {
	if path == "" {
		return false
	}
	for _, s := range strings.Split(path, "/") {
		if s == "" || isDots(s) {
			return false
		}
	}
	return true
}

func rawPath(repo, path, sha string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return repo + "/raw/" + strings.Join(segs, "/") + "?at=" + url.QueryEscape(sha)
}

// work is one processed (not filtered, not over the file limit) change.
type work struct {
	typ     provider.ChangeType
	path    string
	oldPath string
}

// outcome is the result of processing one file: a FilePatch, a skip, or
// neither (the file is dropped because nothing changed).
type outcome struct {
	file *provider.FilePatch
	skip *provider.SkippedFile
}

// entry is one position in API order: either an immediate skip or the index
// of a processed file.
type entry struct {
	skip *provider.SkippedFile
	task int
}

// GetDiff implements provider.Provider.
func (p *Provider) GetDiff(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	pp, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	rp, err := repoPath(apiV1, ref)
	if err != nil {
		return nil, err
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}

	strategy := ""
	if pr != nil {
		p.mu.Lock()
		strategy = p.strategies[strategyKey(ref, pr.BaseSHA)]
		p.mu.Unlock()
	}
	if pr == nil || strategy == "" {
		// The PR was not obtained from this provider (or is stale): refetch
		// so BaseSHA and BaseStrategy are consistent with each other.
		var perr error
		if pr, strategy, perr = p.fetchPR(ctx, ref); perr != nil {
			return nil, perr
		}
	}

	changes, err := httpx.PagesStartLimit[apiChange](ctx, p.client, pp+"/changes", pageLimit)
	if err != nil {
		return nil, err
	}

	var (
		entries []entry
		tasks   []work
	)
	for i := range changes {
		typ, path, oldPath := changes[i].classify()
		switch {
		case opts.Include != nil && !opts.Include(path):
			entries = append(entries, entry{skip: &provider.SkippedFile{Path: path, Reason: provider.SkipFiltered}})
		case len(tasks) >= p.maxFiles:
			entries = append(entries, entry{skip: &provider.SkippedFile{Path: path, Reason: provider.SkipFileLimit}})
		default:
			entries = append(entries, entry{task: len(tasks)})
			tasks = append(tasks, work{typ: typ, path: path, oldPath: oldPath})
		}
	}

	results := p.processAll(ctx, rp, pr, tasks)
	if err := ctx.Err(); err != nil {
		return nil, provider.TransportError(err)
	}

	out := &provider.Diff{BaseStrategy: strategy}
	for _, e := range entries {
		if e.skip != nil {
			out.Skipped = append(out.Skipped, *e.skip)
			continue
		}
		r := results[e.task]
		switch {
		case r.skip != nil:
			out.Skipped = append(out.Skipped, *r.skip)
		case r.file != nil:
			out.Files = append(out.Files, *r.file)
		}
	}
	return out, nil
}

// processAll runs the tasks with bounded concurrency. Every task writes only
// its own slot of the result slice, so the result does not depend on
// scheduling.
func (p *Provider) processAll(ctx context.Context, repo string, pr *provider.PullRequest, tasks []work) []outcome {
	results := make([]outcome, len(tasks))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range fetchConcurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = p.processFile(ctx, repo, pr, tasks[i])
			}
		}()
	}
	for i := range tasks {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

// processFile fetches both sides of one file (except a side that does not
// exist), then builds the patch.
func (p *Provider) processFile(ctx context.Context, repo string, pr *provider.PullRequest, w work) outcome {
	skip := func(reason string) outcome {
		return outcome{skip: &provider.SkippedFile{Path: w.path, Reason: reason}}
	}
	if ctx.Err() != nil {
		return skip(provider.SkipFetchFailed)
	}
	fp := provider.FilePatch{Path: w.path, OldPath: w.oldPath, Type: w.typ}
	basePath := w.path
	if w.oldPath != "" {
		basePath = w.oldPath
	}

	var base, head string
	if w.typ == provider.ChangeAdded {
		fp.BaseStatus = provider.ContentNotApplicable
	} else {
		s, reason := p.fetchSide(ctx, repo, basePath, pr.BaseSHA, "base")
		if reason != "" {
			return skip(reason)
		}
		base = s
		fp.BaseContent, fp.BaseStatus = &s, provider.ContentFull
	}
	if w.typ == provider.ChangeDeleted {
		fp.HeadStatus = provider.ContentNotApplicable
	} else {
		s, reason := p.fetchSide(ctx, repo, w.path, pr.HeadSHA, "head")
		if reason != "" {
			return skip(reason)
		}
		head = s
		fp.HeadContent, fp.HeadStatus = &s, provider.ContentFull
	}

	if hasNUL(base) || hasNUL(head) {
		return skip(provider.SkipBinary)
	}
	patch, ok, err := makePatch(base, head)
	if err != nil {
		p.logger.Debug("bitbucket server patch generation failed", "path", w.path)
		return skip(provider.SkipFetchFailed)
	}
	if !ok {
		return outcome{} // nothing changed: dropped silently, not a skip
	}
	fp.Patch = patch
	fp.Additions, fp.Deletions = countChanges(patch)
	return outcome{file: &fp}
}

func hasNUL(s string) bool {
	if len(s) > binarySniffBytes {
		s = s[:binarySniffBytes]
	}
	return strings.IndexByte(s, 0) >= 0
}

// fetchSide fetches one side of a file. reason is "" on success, otherwise
// the Skipped reason: size_limit for an oversized side, fetch_failed for any
// other failure (including an unusable path or revision, with no request).
func (p *Provider) fetchSide(ctx context.Context, repo, path, sha, side string) (content, reason string) {
	if sha == "" || !validRawPath(path) {
		p.logger.Debug("bitbucket server content fetch skipped: unusable path or revision", "path", path, "side", side)
		return "", provider.SkipFetchFailed
	}
	data, _, err := p.client.Get(ctx, rawPath(repo, path, sha), p.maxFile, capKeyFile)
	if err != nil {
		var perr *provider.Error
		if errors.As(err, &perr) && perr.Class == provider.ClassTooLarge {
			return "", provider.SkipSizeLimit
		}
		p.logger.Debug("bitbucket server content fetch failed", "path", path, "side", side, "error", errClass(err))
		return "", provider.SkipFetchFailed
	}
	return string(data), ""
}

// errClass returns only the class (and status) of a provider error.
func errClass(err error) string {
	var perr *provider.Error
	if errors.As(err, &perr) {
		s := string(perr.Class)
		if perr.Status != 0 {
			s += " " + strconv.Itoa(perr.Status)
		}
		return s
	}
	return "unknown"
}
