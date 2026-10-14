package gitlab

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/nevzatcirak/review-mcp/internal/filter/data"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// Diff notes. They are fixed sentences (X-6).
const (
	// NoteDiffLimit: the merge request's changes_count ends in "+", so
	// GitLab stopped listing changed files at its diff limit.
	NoteDiffLimit = "GitLab did not list every changed file (its diff limit was reached); the files it left out are not reviewed"
	// NoteDiffPageCap: GitLab kept listing changed files past diffsPageCap
	// pages of perPage files.
	NoteDiffPageCap = "review-mcp reads at most 4000 changed files of a merge request; the files GitLab listed after them are not reviewed"
)

// apiDiff is one entry of GET /merge_requests/{iid}/diffs.
type apiDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	AMode       string `json:"a_mode"`
	BMode       string `json:"b_mode"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	TooLarge    bool   `json:"too_large"`
	Collapsed   bool   `json:"collapsed"`
}

// changeType maps the flags of a diff entry to a change type: new_file is
// an addition, deleted_file a deletion, renamed_file a rename (from
// old_path), anything else a modification.
func (d *apiDiff) changeType() provider.ChangeType {
	switch {
	case d.NewFile:
		return provider.ChangeAdded
	case d.DeletedFile:
		return provider.ChangeDeleted
	case d.RenamedFile && d.OldPath != "" && d.OldPath != d.NewPath:
		return provider.ChangeRenamed
	}
	return provider.ChangeModified
}

// filePath is the entry's path: the new path, or the old one for a
// deletion.
func (d *apiDiff) filePath() string {
	if d.DeletedFile && d.OldPath != "" {
		return d.OldPath
	}
	if d.NewPath == "" {
		return d.OldPath
	}
	return d.NewPath
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

// withoutDiff decides what an entry with an empty diff is. ok is true when
// the file is still listed, with an empty patch (diffpipe labels it
// empty_diff): a rename whose modes are equal (a rename without edits), or
// a file with nothing to show that is neither cut by GitLab nor binary by
// the extension rule (a mode change, an empty added file). Otherwise reason
// is size_limit when GitLab cut the diff (too_large or collapsed) and
// binary when the extension rule says so.
func withoutDiff(d *apiDiff, t provider.ChangeType) (reason string, ok bool) {
	switch {
	case t == provider.ChangeRenamed && d.AMode == d.BMode:
		return "", true
	case d.TooLarge || d.Collapsed:
		return provider.SkipSizeLimit, false
	case binaryByExtension(d.filePath()):
		return provider.SkipBinary, false
	}
	return "", true
}

// hunkOnly returns GitLab's diff as provider.FilePatch.Patch requires it:
// it starts at the first "@@" line and ends with a newline. Bytes are
// otherwise kept as they are. ok is false when there is no hunk header.
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

// countChanges counts the added and removed lines of a hunk-only patch.
func countChanges(patch string) (add, del int) {
	for _, l := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
		case strings.HasPrefix(l, "+"):
			add++
		case strings.HasPrefix(l, "-"):
			del++
		}
	}
	return add, del
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

// escapePath escapes every segment of a repository path for a web URL.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// rawPath is the raw content request of a file: the file path is one
// URL-encoded segment, its "/" sent as "%2F".
func rawPath(project, p, sha string) string {
	return project + "/repository/files/" + url.PathEscape(p) + "/raw?ref=" + url.QueryEscape(sha)
}

// GetDiff implements provider.Provider. The files come from GET
// /merge_requests/{iid}/diffs (Link-paged). Contents for extended context
// come from GET /repository/files/{path}/raw?ref={sha}, for the first
// diff.max_files_full_content listed files, each capped at
// diff.max_file_bytes. When the merge request's changes_count ends in "+"
// (pr.ChangedFilesAtLeast), or the listing reaches diffsPageCap pages, a
// fixed note says that changed files were left out.
func (p *Provider) GetDiff(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	if pr == nil || pr.BaseSHA == "" || pr.HeadSHA == "" {
		return nil, protocolErr("the pull request has no base revision; fetch it with GetPullRequest first")
	}
	mp, err := mrPath(ref)
	if err != nil {
		return nil, err
	}
	pp, err := projectPath(ref)
	if err != nil {
		return nil, err
	}
	diffs, truncated, err := httpx.PagesByLinkCapped(ctx, p.client, p.fetchPage,
		mp+"/diffs?per_page="+strconv.Itoa(perPage), httpx.Cap{Pages: diffsPageCap},
		func(pg *[]apiDiff) []apiDiff { return *pg })
	if err != nil {
		return nil, p.versionCheck(ctx, mp, err)
	}

	out := &provider.Diff{BaseStrategy: pr.BaseStrategy}
	if pr.ChangedFilesAtLeast {
		out.Notes = append(out.Notes, NoteDiffLimit)
		p.logger.Debug("gitlab diff limit reached", "listed", len(diffs), "changes_count_at_least", pr.ChangedFiles)
	}
	if truncated {
		out.Notes = append(out.Notes, NoteDiffPageCap)
		p.logger.Debug("gitlab diff list cut at the page cap", "pages", diffsPageCap)
	}
	var tasks []fetchTask
	seen := make(map[string]bool, len(diffs))
	for i := range diffs {
		d := &diffs[i]
		fpath := d.filePath()
		if fpath == "" || seen[fpath] {
			p.logger.Debug("gitlab diff entry ignored: empty or repeated path")
			continue
		}
		seen[fpath] = true
		if opts.Include != nil && !opts.Include(fpath) {
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: fpath, Reason: provider.SkipFiltered})
			continue
		}
		fp := provider.FilePatch{Path: fpath, Type: d.changeType()}
		if fp.Type == provider.ChangeRenamed {
			fp.OldPath = d.OldPath
		}
		if d.Diff == "" {
			if reason, listed := withoutDiff(d, fp.Type); !listed {
				out.Skipped = append(out.Skipped, provider.SkippedFile{Path: fpath, Reason: reason})
				continue
			}
		} else {
			patch, ok := hunkOnly(d.Diff)
			if !ok {
				reason := provider.SkipFetchFailed
				if strings.HasPrefix(d.Diff, "Binary files ") {
					reason = provider.SkipBinary
				}
				p.logger.Debug("gitlab diff has no hunk header", "path", fpath, "reason", reason)
				out.Skipped = append(out.Skipped, provider.SkippedFile{Path: fpath, Reason: reason})
				continue
			}
			fp.Patch = patch
			fp.Additions, fp.Deletions = countChanges(patch)
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

	p.fetchAll(ctx, pp, out.Files, tasks)
	if err := ctx.Err(); err != nil {
		return nil, provider.TransportError(err)
	}
	return out, nil
}

// versionCheck turns a not_found answer of an endpoint of the merge
// request (err) into unsupported_version when the merge request itself can
// be read (GitLab older than 16.0 lacks the endpoint). Any other error, or
// a merge request that cannot be read either, is returned as err.
func (p *Provider) versionCheck(ctx context.Context, mr string, err error) error {
	if !errors.Is(err, provider.ErrNotFound) {
		return err
	}
	var probe struct {
		IID int64 `json:"iid"`
	}
	if perr := p.getJSON(ctx, mr, &probe); perr != nil {
		return err
	}
	p.logger.Debug("gitlab endpoint missing although the merge request exists")
	return &provider.Error{Class: provider.ClassUnsupportedVersion, Hint: HintMinVersion}
}

type fetchTask struct {
	file int
	base bool
	path string
	sha  string
}

// fetchAll runs the tasks with bounded concurrency. Every task writes only
// its own side of its own file, so the result does not depend on scheduling.
func (p *Provider) fetchAll(ctx context.Context, project string, files []provider.FilePatch, tasks []fetchTask) {
	sem := make(chan struct{}, fetchConcurrency)
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			content, status := p.fetchContent(ctx, project, t)
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

func (p *Provider) fetchContent(ctx context.Context, project string, t fetchTask) (*string, provider.ContentStatus) {
	if ctx.Err() != nil {
		return nil, provider.ContentFetchFailed
	}
	data, err := p.getRaw(ctx, project, t.path, t.sha)
	if err != nil {
		var perr *provider.Error
		if errors.As(err, &perr) && perr.Class == provider.ClassTooLarge {
			return nil, provider.ContentNotFetchedSizeCap
		}
		p.logger.Debug("gitlab content fetch failed", "path", t.path, "side", sideName(t.base), "error", errClass(err))
		return nil, provider.ContentFetchFailed
	}
	s := string(data)
	return &s, provider.ContentFull
}

// getRaw reads the raw content of path at sha, capped at
// diff.max_file_bytes. It is the one content request of GetDiff and
// GetFileAt. An unusable path or revision is a protocol error, with no
// request.
func (p *Provider) getRaw(ctx context.Context, project, path, sha string) ([]byte, error) {
	if !provider.ValidRevision(sha) || !validContentPath(path) {
		return nil, protocolErr("unusable path or revision")
	}
	resp, err := p.do(ctx, request{path: rawPath(project, path, sha), maxBytes: p.maxFile, capKey: capKeyFile})
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func sideName(base bool) string {
	if base {
		return "base"
	}
	return "head"
}
