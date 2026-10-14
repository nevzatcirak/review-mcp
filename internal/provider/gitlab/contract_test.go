package gitlab

// The contract test lives in the package itself (not gitlab_test) so that
// the fixture can replace the provider's sleep: the fake serves every merge
// request with empty diff_refs on its first read, and the one 2 s wait
// before the second read must not slow the suite down.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

// TestContract runs the provider contract suite against a fake GitLab API
// v4 under a context path, serving each contract.Spec from a project in a
// nested group. The read path is WP-6a; the cases of the later work
// packages are declared pending.
func TestContract(t *testing.T) {
	contract.Run(t, ctFixture{})
}

// TestReviewProject runs the project-context scenario (X-32) of pr_review
// and pr_improve through this provider and the contract fake: the
// guidelines and the file tree are read at the base revision.
func TestReviewProject(t *testing.T) {
	contract.RunReviewProject(t, ctFixture{})
}

const (
	// ctContext is the context path of the fake's web base; the API base
	// is {origin}{ctContext}/api/v4.
	ctContext = "/gitlab"
	ctAPI     = ctContext + "/api/v4"
	// ctProject addresses the project by its URL-encoded full path, as
	// review-mcp must; ctProjectByID is the numeric form GitLab uses in its
	// Link headers. The fake pages only through ctProjectByID, so a next
	// page can only be found by reading the Link header.
	ctProject     = ctAPI + "/projects/group%2Fsub%2Fproject"
	ctProjectByID = ctAPI + "/projects/4242"
	ctMR          = ctProject + "/merge_requests/7"
	ctMRByID      = ctProjectByID + "/merge_requests/7"
	// ctStartSHA is the target branch's head when GitLab computed the diff
	// (diff_refs.start_sha); the merge base (Spec.BaseSHA) differs from it.
	ctStartSHA = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	// ctPageSize is the fake's page size for diffs and commits: smaller
	// than per_page, so that every list of the sample merge request has
	// several pages. The tree honours per_page (keyset pagination).
	ctPageSize = 2
)

// ctFixture serves contract Specs. The fields are knobs of the
// GitLab-specific cases below; the contract suite uses the zero value.
type ctFixture struct {
	// diffLimit serves changes_count with a "+": GitLab stopped listing
	// the changed files at its diff limit.
	diffLimit bool
	// commits are the merge request's commit messages, oldest first; the
	// fake lists them newest first, as GitLab does.
	commits []string
	// modeOnly are paths whose diff entry changes the file mode (b_mode
	// 100755) and nothing else.
	modeOnly map[string]bool
	// limitFirst answers the first merge request read with 429 and
	// Retry-After: 3.
	limitFirst bool
}

func (ctFixture) Kind() provider.Kind { return provider.KindGitLab }

func (ctFixture) Traits() contract.Traits {
	return contract.Traits{
		BaseStrategies:  BaseStrategies(),
		CheckStrategies: CheckStrategies(),
		Pending: map[string]string{
			"capabilities":              "WP-6b",
			"threads":                   "WP-6b",
			"reply_in_thread":           "WP-6b",
			"general_reply":             "WP-6b",
			"edit_ownership":            "WP-6b",
			"inline_anchoring":          "WP-6c",
			"review_status":             "WP-6d",
			"update_pull_request":       "WP-6d",
			"checks":                    "WP-6d",
			"errors/ListThreads":        "WP-6b",
			"errors/PostComment":        "WP-6b",
			"errors/ReplyToComment":     "WP-6b",
			"errors/EditComment":        "WP-6b",
			"errors/PostInlineComments": "WP-6c",
			"errors/UpdatePullRequest":  "WP-6d",
			"errors/GetReviewStatus":    "WP-6d",
			"errors/GetChecks":          "WP-6d",
		},
	}
}

func (f ctFixture) Serve(t *testing.T, pr contract.Spec) (provider.Provider, provider.PRRef) {
	t.Helper()
	p, ref, _, _ := f.serve(t, pr)
	return p, ref
}

// serve serves pr through a new fake and also returns the fake and the
// waits the provider took.
func (f ctFixture) serve(t *testing.T, pr contract.Spec) (*Provider, provider.PRRef, *ctGitLab, *[]time.Duration) {
	t.Helper()
	g := &ctGitLab{t: t, pr: pr, fx: f}
	cfg := config.Defaults()
	cfg.GitLab.BaseURL = contract.StartServer(t, pr, g) + ctContext
	cfg.Secrets.GitLabToken = config.NewSecret(pr.Env.Token)
	cfg.Diff.MaxFilesFullContent = pr.Env.MaxFiles
	cfg.Diff.MaxFileBytes = pr.Env.MaxFileBytes
	pp, err := NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := pp.(*Provider)
	var mu sync.Mutex
	waits := &[]time.Duration{}
	p.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		*waits = append(*waits, d)
		return nil
	}
	return p, provider.PRRef{Kind: provider.KindGitLab, Namespace: "group/sub", Repo: "project", Number: 7}, g, waits
}

// ctGitLab is a fake GitLab REST API v4 that serves one contract.Spec. It
// checks the headers of every request and answers any request it does not
// know with an error that fails the test.
type ctGitLab struct {
	t  *testing.T
	pr contract.Spec
	fx ctFixture
	mu sync.Mutex

	// mrReads counts the reads of the merge request: the first is served
	// with empty diff_refs (GitLab fills them asynchronously).
	mrReads int
	limited bool
}

func ctUser(u contract.User) map[string]any {
	return map[string]any{"id": u.ID, "username": u.Login, "name": u.DisplayName, "state": "active"}
}

func ctJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("RateLimit-Limit", "2000")
	w.Header().Set("RateLimit-Remaining", "1999")
	w.Header().Set("RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10))
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (g *ctGitLab) mrJSON() map[string]any {
	count := strconv.Itoa(len(g.pr.Files))
	if g.fx.diffLimit {
		count = "1000+"
	}
	m := map[string]any{
		"id": 99007, "iid": 7, "project_id": 4242, "title": g.pr.Title, "description": g.pr.Description,
		"state": "opened", "draft": g.pr.Draft, "detailed_merge_status": "mergeable",
		"web_url":       "https://gitlab.example.com/group/sub/project/-/merge_requests/7",
		"changes_count": count, "source_branch": g.pr.SourceBranch, "target_branch": g.pr.TargetBranch,
		"sha": g.pr.HeadSHA, "author": ctUser(g.pr.Author), "source_project_id": 4242, "target_project_id": 4242,
		"diff_refs": map[string]any{"base_sha": g.pr.BaseSHA, "head_sha": g.pr.HeadSHA, "start_sha": ctStartSHA},
	}
	g.mrReads++
	if g.mrReads == 1 {
		m["diff_refs"] = nil
	}
	return m
}

// diffsJSON renders the files as GitLab lists them: the diff is the hunks
// without a header; a binary file has an empty diff (the extension rule
// marks it), and an oversized file is too_large with an empty diff.
func (g *ctGitLab) diffsJSON() []any {
	var out []any
	for _, f := range g.pr.Files {
		m := map[string]any{"old_path": f.BasePath(), "new_path": f.Path, "a_mode": "100644", "b_mode": "100644",
			"new_file": f.Type == provider.ChangeAdded, "renamed_file": f.Type == provider.ChangeRenamed,
			"deleted_file": f.Type == provider.ChangeDeleted, "too_large": f.TooLarge, "collapsed": false,
			"generated_file": false, "diff": f.Hunks}
		switch f.Type {
		case provider.ChangeAdded:
			m["a_mode"] = "0"
		case provider.ChangeDeleted:
			m["b_mode"] = "0"
		}
		if g.fx.modeOnly[f.Path] {
			m["b_mode"] = "100755"
		}
		if f.Binary || f.TooLarge {
			m["diff"] = ""
		}
		out = append(out, m)
	}
	return out
}

// commitsJSON lists the commits newest first.
func (g *ctGitLab) commitsJSON() []any {
	out := []any{}
	for i := len(g.fx.commits) - 1; i >= 0; i-- {
		msg := g.fx.commits[i]
		out = append(out, map[string]any{"id": strings.Repeat(strconv.Itoa(i%10), 40), "title": strings.SplitN(msg, "\n", 2)[0],
			"message": msg})
	}
	return out
}

// ctPage serves items in pages of size. The first page is asked for under
// the project's full path; every further page only through the opaque
// cursor of a Link header, under the project's id. A page number in a
// request means the client guessed a page: the test fails.
func (g *ctGitLab) ctPage(w http.ResponseWriter, r *http.Request, items []any, byIDPath string, size int, keep string) {
	q := r.URL.Query()
	if q.Get("page") != "" {
		g.t.Errorf("request %s asks for a page number: pages must come from the Link header", r.URL.EscapedPath())
		contract.WriteError(w, http.StatusBadRequest)
		return
	}
	start := 0
	if c := q.Get("page_token"); c != "" {
		raw, err := base64.RawURLEncoding.DecodeString(c)
		n, aerr := strconv.Atoi(strings.TrimPrefix(string(raw), "offset:"))
		if err != nil || aerr != nil || n < 0 || n > len(items) {
			contract.WriteError(w, http.StatusBadRequest)
			return
		}
		start = n
	}
	end := min(start+size, len(items))
	if end < len(items) {
		next := "http://" + r.Host + byIDPath + "?" + keep + "page_token=" +
			base64.RawURLEncoding.EncodeToString([]byte("offset:"+strconv.Itoa(end)))
		w.Header().Set("Link", `<`+next+`>; rel="next", <http://`+r.Host+byIDPath+`?`+keep+`per_page=100>; rel="first"`)
	}
	page := items[start:end]
	if page == nil {
		page = []any{}
	}
	ctJSON(w, http.StatusOK, page)
}

func (g *ctGitLab) checkHeaders(r *http.Request) bool {
	ok := true
	if r.Header.Get("PRIVATE-TOKEN") != g.pr.Env.Token || r.Header.Get("Authorization") != "" {
		g.t.Errorf("request %s %s: unexpected token header shape", r.Method, r.URL.EscapedPath())
		ok = false
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		g.t.Errorf("request %s %s: Accept %q", r.Method, r.URL.EscapedPath(), got)
		ok = false
	}
	if !strings.HasPrefix(r.Header.Get("User-Agent"), "review-mcp/") {
		g.t.Errorf("request %s %s: User-Agent %q", r.Method, r.URL.EscapedPath(), r.Header.Get("User-Agent"))
		ok = false
	}
	return ok
}

func (g *ctGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	path := r.URL.EscapedPath()
	if !g.checkHeaders(r) {
		contract.WriteError(w, http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		g.t.Errorf("unexpected request %s %s", r.Method, path)
		contract.WriteError(w, http.StatusMethodNotAllowed)
		return
	}
	switch {
	case path == ctAPI+"/user":
		ctJSON(w, http.StatusOK, ctUser(g.pr.TokenUser))
	case path == ctMR:
		if g.fx.limitFirst && !g.limited {
			g.limited = true
			w.Header().Set("Retry-After", "3")
			w.Header().Set("RateLimit-Remaining", "0")
			contract.WriteError(w, http.StatusTooManyRequests)
			return
		}
		ctJSON(w, http.StatusOK, g.mrJSON())
	case path == ctMR+"/diffs" || path == ctMRByID+"/diffs":
		g.ctPage(w, r, g.diffsJSON(), ctMRByID+"/diffs", ctPageSize, "per_page=100&")
	case path == ctMR+"/commits" || path == ctMRByID+"/commits":
		g.ctPage(w, r, g.commitsJSON(), ctMRByID+"/commits", ctPageSize, "per_page=100&")
	case strings.HasPrefix(path, ctProject+"/repository/files/") && strings.HasSuffix(path, "/raw"):
		g.serveRaw(w, r, strings.TrimSuffix(strings.TrimPrefix(path, ctProject+"/repository/files/"), "/raw"))
	case path == ctProject+"/repository/tree" || path == ctProjectByID+"/repository/tree":
		g.serveTree(w, r)
	default:
		g.t.Errorf("unexpected request %s %s", r.Method, path)
		contract.WriteError(w, http.StatusNotFound)
	}
}

// serveRaw answers a raw file request at the merge base or the head. The
// file path is one escaped segment ("src%2Fapp.go"); content the Spec does
// not have is a 404.
func (g *ctGitLab) serveRaw(w http.ResponseWriter, r *http.Request, escaped string) {
	if !strings.Contains(escaped, "%2F") && strings.Contains(escaped, "/") {
		g.t.Errorf("raw file path %q is not one escaped segment", escaped)
	}
	p, err := url.PathUnescape(escaped)
	if err != nil {
		contract.WriteError(w, http.StatusBadRequest)
		return
	}
	content, ok := g.pr.FileAt(r.URL.Query().Get("ref"), p)
	if !ok {
		contract.WriteError(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, content) //nolint:gosec // test fake; the content is a fixed Spec fixture, not request input
}

// serveTree answers a repository tree request with keyset pagination: a
// directory lists its entries (directories as tree, files as blob); a path
// that is a file is an empty tree; anything else is a 404.
func (g *ctGitLab) serveTree(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("pagination") != "keyset" {
		g.t.Errorf("tree request without keyset pagination: %s", r.URL.RequestURI())
	}
	sha, dir := q.Get("ref"), q.Get("path")
	size, err := strconv.Atoi(q.Get("per_page"))
	if err != nil || size < 1 || size > 100 {
		size = 20
	}
	keep := "pagination=keyset&per_page=" + strconv.Itoa(size) + "&ref=" + url.QueryEscape(sha) + "&"
	if dir != "" {
		keep += "path=" + url.QueryEscape(dir) + "&"
	}
	if _, isFile := g.pr.FileAt(sha, dir); isFile {
		g.ctPage(w, r, nil, ctProjectByID+"/repository/tree", size, keep)
		return
	}
	entries, ok := g.pr.DirAt(sha, dir)
	if !ok {
		contract.WriteError(w, http.StatusNotFound)
		return
	}
	// GitLab lists directories first; the provider sorts.
	var items []any
	for _, dirs := range []bool{true, false} {
		for _, e := range entries {
			if e.Dir != dirs {
				continue
			}
			typ := "blob"
			if e.Dir {
				typ = "tree"
			}
			items = append(items, map[string]any{"id": strings.Repeat("e", 40), "name": e.Name, "type": typ,
				"path": strings.TrimPrefix(dir+"/"+e.Name, "/"), "mode": "100644"})
		}
	}
	g.ctPage(w, r, items, ctProjectByID+"/repository/tree", size, keep)
}

// ctSpec is a small merge request for the GitLab-specific cases.
func ctSpec(files ...contract.File) contract.Spec {
	return contract.Spec{
		Title: "T", Description: "D", Author: contract.User{ID: 101, Login: "alice"}, SourceBranch: "f", TargetBranch: "main",
		HeadSHA: "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1", BaseSHA: "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2",
		TokenUser: contract.User{ID: 900, Login: "review-bot"},
		Files:     files,
		Env:       contract.Env{Token: "tok-FAKE-wp6a", MaxFiles: 10, MaxFileBytes: 4096}, //nolint:gosec // synthetic test value
	}
}

var ctModified = contract.File{Path: "src/app.go", Type: provider.ChangeModified, Base: "a\nb\n", Head: "a\nB\n",
	Hunks: "@@ -1,2 +1,2 @@\n a\n-b\n+B\n"}

// TestContractDiffRefsFilledLater: the fake serves the merge request with
// empty diff_refs on its first read; the provider waits once, 2 s, reads it
// again and reports the base from the filled diff_refs.
func TestContractDiffRefsFilledLater(t *testing.T) {
	spec := ctSpec(ctModified)
	p, ref, g, waits := ctFixture{}.serve(t, spec)
	pr, err := p.GetPullRequest(t.Context(), ref)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if pr.BaseSHA != spec.BaseSHA || pr.StartSHA != ctStartSHA || pr.HeadSHA != spec.HeadSHA ||
		pr.BaseStrategy != provider.BaseGitLabDiffRefs {
		t.Errorf("base %q start %q head %q strategy %q", pr.BaseSHA, pr.StartSHA, pr.HeadSHA, pr.BaseStrategy)
	}
	if !slices.Equal(*waits, []time.Duration{2 * time.Second}) || g.mrReads != 2 {
		t.Errorf("waits %v, %d reads; want one 2s wait and two reads", *waits, g.mrReads)
	}
}

// TestContractDiffLimit: a changes_count of "1000+" means GitLab did not
// list every changed file: the diff carries the fixed note. A plain count
// carries none. [canary]
func TestContractDiffLimit(t *testing.T) {
	for _, limit := range []bool{false, true} {
		t.Run("limit_"+strconv.FormatBool(limit), func(t *testing.T) {
			p, ref, _, _ := ctFixture{diffLimit: limit}.serve(t, ctSpec(ctModified))
			pr, err := p.GetPullRequest(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			d, err := p.GetDiff(t.Context(), ref, pr, provider.DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			if limit {
				want = []string{NoteDiffLimit}
			}
			if !slices.Equal(d.Notes, want) || pr.ChangedFilesAtLeast != limit {
				t.Errorf("notes %q (at least %v), want %q", d.Notes, pr.ChangedFilesAtLeast, want)
			}
			if len(d.Files) != 1 {
				t.Errorf("%d files, want the one listed", len(d.Files))
			}
		})
	}
}

// TestContractCommitOrder: GitLab lists commits newest first, over several
// pages; GetCommitMessages returns them oldest first. [canary]
func TestContractCommitOrder(t *testing.T) {
	want := []string{"first commit", "second commit\n\nwith a body", "third commit", "fourth commit", "fifth commit"}
	p, ref, _, _ := ctFixture{commits: want}.serve(t, ctSpec(ctModified))
	got, err := p.GetCommitMessages(t.Context(), ref)
	if err != nil {
		t.Fatalf("GetCommitMessages: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("commits = %q, want oldest first %q", got, want)
	}
}

// TestContractRenameAndModeShapes: a rename without edits (empty diff,
// equal modes) and a mode-only change are listed with no hunks; a rename
// with edits keeps its hunks and its old path.
func TestContractRenameAndModeShapes(t *testing.T) {
	spec := ctSpec(
		contract.File{Path: "pkg/new.go", OldPath: "pkg/old.go", Type: provider.ChangeRenamed, Base: "x\n", Head: "x\n"},
		contract.File{Path: "bin/run.sh", Type: provider.ChangeModified, Base: "echo\n", Head: "echo\n"},
		contract.File{Path: "pkg/edit.go", OldPath: "pkg/orig.go", Type: provider.ChangeRenamed, Base: "a\n", Head: "b\n",
			Hunks: "@@ -1 +1 @@\n-a\n+b\n"},
	)
	p, ref, _, _ := ctFixture{modeOnly: map[string]bool{"bin/run.sh": true}}.serve(t, spec)
	pr, err := p.GetPullRequest(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.GetDiff(t.Context(), ref, pr, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 3 || len(d.Skipped) != 0 {
		t.Fatalf("files %+v skipped %+v, want three listed", d.Files, d.Skipped)
	}
	for i, want := range []struct {
		path, old, patch string
		typ              provider.ChangeType
	}{
		{"pkg/new.go", "pkg/old.go", "", provider.ChangeRenamed},
		{"bin/run.sh", "", "", provider.ChangeModified},
		{"pkg/edit.go", "pkg/orig.go", "@@ -1 +1 @@\n-a\n+b\n", provider.ChangeRenamed},
	} {
		f := d.Files[i]
		if f.Path != want.path || f.OldPath != want.old || f.Patch != want.patch || f.Type != want.typ {
			t.Errorf("file %d = %s (old %q, %s) patch %q; want %+v", i, f.Path, f.OldPath, f.Type, f.Patch, want)
		}
		if f.BaseStatus != provider.ContentFull || f.HeadStatus != provider.ContentFull {
			t.Errorf("%s: statuses %s/%s", f.Path, f.BaseStatus, f.HeadStatus)
		}
	}
}

// TestContractRateLimitWaitsOnce: a 429 with Retry-After on the merge
// request read is waited for once and the read repeated.
func TestContractRateLimitWaitsOnce(t *testing.T) {
	p, ref, _, waits := ctFixture{limitFirst: true}.serve(t, ctSpec(ctModified))
	if _, err := p.GetPullRequest(t.Context(), ref); err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	// 3 s for the rate limit, then 2 s for the empty diff_refs of the
	// first successful read.
	if !slices.Equal(*waits, []time.Duration{3 * time.Second, 2 * time.Second}) {
		t.Errorf("waits = %v", *waits)
	}
}
