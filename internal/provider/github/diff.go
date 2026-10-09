package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/nevzatcirak/review-mcp/internal/filter/data"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// NoteFilesNotListedFormat is the Diff note for changed files GitHub did not
// list (it lists at most 3000 files of a pull request). %d is the number of
// files not listed.
const NoteFilesNotListedFormat = "GitHub lists at most 3000 files of a pull request: %d more changed files were not listed, so they are not reviewed (file_limit)."

// apiFile is one entry of GET /pulls/{n}/files.
type apiFile struct {
	Filename         string  `json:"filename"`
	PreviousFilename string  `json:"previous_filename"`
	Status           string  `json:"status"`
	Additions        int     `json:"additions"`
	Deletions        int     `json:"deletions"`
	Changes          int     `json:"changes"`
	Patch            *string `json:"patch"`
}

// mapStatus maps a GitHub file status to a change type: copied is an
// addition, changed (a mode change) and unchanged are modifications, and
// unknown values are treated as modified.
func mapStatus(s string) provider.ChangeType {
	switch s {
	case "added", "copied":
		return provider.ChangeAdded
	case "removed":
		return provider.ChangeDeleted
	case "renamed":
		return provider.ChangeRenamed
	default: // modified, changed, unchanged and anything unknown
		return provider.ChangeModified
	}
}

var badExtensions = func() map[string]bool {
	m := make(map[string]bool, len(data.BadExtensions))
	for _, e := range data.BadExtensions {
		m[strings.ToLower(e)] = true
	}
	return m
}()

// binaryByExtension applies the extension rule of the file filter
// (data.BadExtensions): a file whose extension is on it is binary or
// otherwise never reviewed.
func binaryByExtension(p string) bool {
	base := path.Base(p)
	i := strings.LastIndexByte(base, '.')
	return i >= 0 && badExtensions[strings.ToLower(base[i+1:])]
}

// withoutPatch decides what a file without a patch is. GitHub omits the
// patch of a binary file and of a diff too large to show. ok is true when
// the file is still listed (with an empty patch): a pure rename or a mode
// change, which have no line changes to show. Otherwise reason is binary
// when the extension rule says so or when the file has no line changes at
// all (GitHub's mark of a binary file), and size_limit else.
func withoutPatch(f *apiFile, t provider.ChangeType) (reason string, ok bool) {
	lineChanges := f.Additions+f.Deletions+f.Changes > 0
	switch {
	case binaryByExtension(f.Filename):
		return provider.SkipBinary, false
	case !lineChanges && (t == provider.ChangeRenamed || f.Status == "changed"):
		return "", true
	case !lineChanges:
		return provider.SkipBinary, false
	}
	return provider.SkipSizeLimit, false
}

// hunkOnly returns GitHub's patch as provider.FilePatch.Patch requires it:
// it starts at the first "@@" line and ends with a newline (GitHub drops the
// last one). Bytes are otherwise kept as they are. ok is false when there is
// no hunk header at all.
func hunkOnly(p string) (string, bool) {
	if !strings.HasPrefix(p, "@@") {
		i := strings.Index(p, "\n@@")
		if i < 0 {
			return "", false
		}
		p = p[i+1:]
	}
	if !strings.HasSuffix(p, "\n") {
		p += "\n"
	}
	return p, true
}

// validContentPath reports whether every segment of p is usable in a
// contents request: no empty, "." or ".." segment.
func validContentPath(p string) bool {
	if p == "" {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || isDots(s) {
			return false
		}
	}
	return true
}

func contentsPath(repo, p, sha string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return repo + "/contents/" + strings.Join(segs, "/") + "?ref=" + url.QueryEscape(sha)
}

// GetDiff implements provider.Provider. The files come from GET
// /pulls/{n}/files (Link-paged). Contents for extended context come from
// GET /contents/{path}?ref={sha} with the raw media type, for the first
// diff.max_files_full_content listed files, each capped at
// diff.max_file_bytes.
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
	files, err := httpx.PagesByLink[apiFile](ctx, p.client, p.fetchPage, prp+"/files?per_page="+strconv.Itoa(perPage), filesPageCap)
	if err != nil {
		return nil, err
	}

	out := &provider.Diff{BaseStrategy: pr.BaseStrategy}
	if missing := pr.ChangedFiles - len(files); missing > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf(NoteFilesNotListedFormat, missing))
		p.logger.Debug("github file list is cut short", "listed", len(files), "changed_files", pr.ChangedFiles,
			"listing_limit", maxListedFiles)
	}
	var tasks []fetchTask
	seen := make(map[string]bool, len(files))
	for i := range files {
		f := &files[i]
		if f.Filename == "" || seen[f.Filename] {
			p.logger.Debug("github file list entry ignored: empty or repeated path")
			continue
		}
		seen[f.Filename] = true
		if opts.Include != nil && !opts.Include(f.Filename) {
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: f.Filename, Reason: provider.SkipFiltered})
			continue
		}
		fp := provider.FilePatch{
			Path:      f.Filename,
			Type:      mapStatus(f.Status),
			Additions: f.Additions,
			Deletions: f.Deletions,
		}
		if fp.Type == provider.ChangeRenamed {
			fp.OldPath = f.PreviousFilename
			if fp.OldPath == "" {
				fp.Type = provider.ChangeModified
			}
		}
		if f.Patch == nil || *f.Patch == "" {
			reason, listed := withoutPatch(f, fp.Type)
			if !listed {
				out.Skipped = append(out.Skipped, provider.SkippedFile{Path: f.Filename, Reason: reason})
				continue
			}
		} else {
			patch, ok := hunkOnly(*f.Patch)
			if !ok {
				p.logger.Debug("github patch has no hunk header", "path", f.Filename)
				out.Skipped = append(out.Skipped, provider.SkippedFile{Path: f.Filename, Reason: provider.SkipFetchFailed})
				continue
			}
			fp.Patch = patch
		}

		idx := len(out.Files)
		overLimit := idx >= p.maxFiles
		basePath := fp.Path
		if fp.OldPath != "" {
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
	if t.sha == "" || !validContentPath(t.path) {
		p.logger.Debug("github content fetch skipped: unusable path or revision", "path", t.path)
		return nil, provider.ContentFetchFailed
	}
	resp, err := p.do(ctx, request{method: http.MethodGet, path: contentsPath(repo, t.path, t.sha), accept: mediaRaw,
		maxBytes: p.maxFile, capKey: capKeyFile})
	if err != nil {
		var perr *provider.Error
		if errors.As(err, &perr) && perr.Class == provider.ClassTooLarge {
			return nil, provider.ContentNotFetchedSizeCap
		}
		p.logger.Debug("github content fetch failed", "path", t.path, "side", sideName(t.base), "error", errClass(err))
		return nil, provider.ContentFetchFailed
	}
	s := string(resp.Data)
	return &s, provider.ContentFull
}

func sideName(base bool) string {
	if base {
		return "base"
	}
	return "head"
}
