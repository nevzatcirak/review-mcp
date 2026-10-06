package gitea

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/nevzatcirak/review-mcp/internal/gitdiff"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// apiFile is the swagger ChangedFile object.
type apiFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Changes          int    `json:"changes"`
}

// mapStatus maps a Gitea changed-file status to a change type. Unknown
// values are treated as modified.
func mapStatus(s string) provider.ChangeType {
	switch strings.ToLower(s) {
	case "added", "copied":
		return provider.ChangeAdded
	case "deleted", "removed":
		return provider.ChangeDeleted
	case "renamed":
		return provider.ChangeRenamed
	default: // changed, modified, unchanged and anything unknown
		return provider.ChangeModified
	}
}

func mapDiffType(t gitdiff.ChangeType) provider.ChangeType {
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

// joined is one file of the merged view: the parsed diff entry and/or the
// /files metadata.
type joined struct {
	path string
	df   *gitdiff.File
	meta *apiFile
}

// join merges the parsed diff with /files metadata by path. The result keeps
// API order; files present only in the diff follow in diff order.
func join(files []gitdiff.File, metas []apiFile) []joined {
	byPath := make(map[string]int, len(files))
	for i := range files {
		if _, ok := byPath[files[i].Path]; !ok {
			byPath[files[i].Path] = i
		}
	}
	used := make([]bool, len(files))
	out := make([]joined, 0, len(files))
	for i := range metas {
		m := &metas[i]
		if idx, ok := byPath[m.Filename]; ok && !used[idx] {
			used[idx] = true
			out = append(out, joined{path: m.Filename, df: &files[idx], meta: m})
			continue
		}
		out = append(out, joined{path: m.Filename, meta: m})
	}
	for i := range files {
		if !used[i] {
			out = append(out, joined{path: files[i].Path, df: &files[i]})
		}
	}
	return out
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
	return repo + "/raw/" + strings.Join(segs, "/") + "?ref=" + url.QueryEscape(sha)
}

// GetDiff implements provider.Provider.
func (p *Provider) GetDiff(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	if pr == nil || pr.BaseSHA == "" || pr.HeadSHA == "" {
		return nil, protocolErr("the pull request has no base revision; fetch it with GetPullRequest first")
	}

	prp, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}

	raw, _, err := p.client.Get(ctx, prp+".diff", p.maxDiff, capKeyDiff)
	if err != nil {
		return nil, err
	}
	parsed, err := gitdiff.Parse(bytes.NewReader(raw))
	if err != nil {
		p.logger.Debug("gitea diff could not be parsed", "error", err.Error())
		return nil, protocolErr("the diff could not be parsed")
	}
	metas, err := httpx.PagesUntilEmpty[apiFile](ctx, p.client, prp+"/files", pageLimit)
	if err != nil {
		return nil, err
	}

	out := &provider.Diff{BaseStrategy: pr.BaseStrategy}
	var tasks []fetchTask
	limit := p.maxFiles
	for _, j := range join(parsed, metas) {
		if opts.Include != nil && !opts.Include(j.path) {
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: j.path, Reason: provider.SkipFiltered})
			continue
		}
		if j.df == nil {
			p.logger.Debug("gitea join mismatch: file listed by the files endpoint is missing from the diff", "path", j.path)
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: j.path, Reason: provider.SkipFetchFailed})
			continue
		}
		if j.meta == nil {
			p.logger.Debug("gitea join mismatch: file in the diff is missing from the files endpoint", "path", j.path)
		} else if mapStatus(j.meta.Status) != mapDiffType(j.df.Type) {
			p.logger.Debug("gitea join mismatch: change type differs between diff and files endpoint", "path", j.path)
		}
		if j.df.Binary {
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: j.path, Reason: provider.SkipBinary})
			continue
		}
		fp := provider.FilePatch{
			Path:      j.df.Path,
			OldPath:   j.df.OldPath,
			Type:      mapDiffType(j.df.Type),
			Patch:     j.df.Patch,
			Additions: j.df.Additions,
			Deletions: j.df.Deletions,
		}
		idx := len(out.Files)
		overLimit := idx >= limit
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
			tasks = append(tasks, fetchTask{file: idx, base: true, path: basePath, sha: pr.BaseSHA})
		}
		switch {
		case fp.Type == provider.ChangeDeleted:
			fp.HeadStatus = provider.ContentNotApplicable
		case overLimit:
			fp.HeadStatus = provider.ContentNotFetchedFileCap
		default:
			tasks = append(tasks, fetchTask{file: idx, base: false, path: fp.Path, sha: pr.HeadSHA})
		}
		out.Files = append(out.Files, fp)
	}

	p.fetchAll(ctx, rp, out.Files, tasks)
	if err := ctx.Err(); err != nil {
		return nil, provider.TransportError(err)
	}
	return out, nil
}

type fetchTask struct {
	file int
	base bool
	path string
	sha  string
}

// fetchAll runs the tasks with bounded concurrency. Every task writes only
// its own side of its own file, so the result does not depend on scheduling.
func (p *Provider) fetchAll(ctx context.Context, repo string, files []provider.FilePatch, tasks []fetchTask) {
	sem := make(chan struct{}, fetchConcurrency)
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			content, status := p.fetchContent(ctx, repo, t)
			f := &files[t.file]
			if t.base {
				f.BaseContent, f.BaseStatus = content, status
			} else {
				f.HeadContent, f.HeadStatus = content, status
			}
		}()
	}
	wg.Wait()
}

func (p *Provider) fetchContent(ctx context.Context, repo string, t fetchTask) (*string, provider.ContentStatus) {
	if ctx.Err() != nil {
		return nil, provider.ContentFetchFailed
	}
	if t.sha == "" || !validRawPath(t.path) {
		p.logger.Debug("gitea content fetch skipped: unusable path or revision", "path", t.path)
		return nil, provider.ContentFetchFailed
	}
	data, _, err := p.client.Get(ctx, rawPath(repo, t.path, t.sha), p.maxFile, capKeyFile)
	if err != nil {
		var perr *provider.Error
		if errors.As(err, &perr) && perr.Class == provider.ClassTooLarge {
			return nil, provider.ContentNotFetchedSizeCap
		}
		p.logger.Debug("gitea content fetch failed", "path", t.path, "side", sideName(t.base), "error", errClass(err))
		return nil, provider.ContentFetchFailed
	}
	s := string(data)
	return &s, provider.ContentFull
}

func sideName(base bool) string {
	if base {
		return "base"
	}
	return "head"
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
