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
// nested group.
func TestContract(t *testing.T) {
	contract.Run(t, ctFixture{})
}

// TestReviewDuplicates runs the pr_review duplicate scenario (X-29)
// through this provider and the contract fake.
func TestReviewDuplicates(t *testing.T) {
	contract.RunReviewDuplicates(t, ctFixture{})
}

// TestReviewFindings runs the merged-publish scenario (X-33) through this
// provider and the contract fake.
func TestReviewFindings(t *testing.T) {
	contract.RunReviewFindings(t, ctFixture{})
}

// TestReviewCI runs the CI scenario (X-31) of pr_review through this
// provider and the contract fake: the commit statuses of the head commit.
func TestReviewCI(t *testing.T) {
	contract.RunReviewCI(t, ctFixture{})
}

// TestReviewProject runs the project-context scenario (X-32) of pr_review
// and pr_improve through this provider and the contract fake: the
// guidelines and the file tree are read at the base revision.
func TestReviewProject(t *testing.T) {
	contract.RunReviewProject(t, ctFixture{})
}

// TestReviewIncremental runs pr_review's incremental scenario (X-36)
// through this provider and the contract fake.
func TestReviewIncremental(t *testing.T) {
	contract.RunReviewIncremental(t, ctFixture{})
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
	// modeOnly are paths whose diff entry changes the file mode (b_mode
	// 100755) and nothing else.
	modeOnly map[string]bool
	// limitFirst answers the first merge request read with 429 and
	// Retry-After: 3.
	limitFirst bool
	// individual serves the general threads that are not resolved as
	// individual notes (not resolvable), and systemNote adds a discussion
	// of one system note ("added 1 commit"). Neither is part of the
	// contract Specs, which need every general thread resolvable.
	individual, systemNote bool
	// strayLine makes an inline post on that new-side line create a general
	// note (GitLab could not place it), written by strayAuthor when set and
	// by the token user otherwise; strayDeleteStatus answers the delete of
	// a note with that error status.
	strayLine         int
	strayAuthor       *contract.User
	strayDeleteStatus int
	// webURL is the merge request's web_url when set (the default is
	// https://gitlab.example.com/group/sub/project/-/merge_requests/7), and
	// emptyWebURL serves none.
	webURL      string
	emptyWebURL bool
	// mergeStatus is the merge request's detailed_merge_status ("mergeable"
	// when empty) and state its state ("opened" when empty).
	mergeStatus, state string
	// required is approvals_required of the approvals read (2 when nil);
	// omitRequired leaves the field out; approvalsStatus and
	// reviewersStatus answer those reads with that error status, and
	// noReviewersRoute answers 404 on /reviewers only.
	required        *int
	omitRequired    bool
	approvalsStatus int
	reviewersStatus int
	// reviewerStates overrides the /reviewers state served for a login.
	reviewerStates map[string]string
	// statuses are served instead of the Spec's checks when set (raw
	// commit statuses); statusPage is their page size (100 when 0).
	statuses   []map[string]any
	statusPage int
	// objectErrors sends a refused position's errors as an object
	// ({"message": {"line_code": [...]}}) instead of a Ruby hash string.
	objectErrors bool
}

func (ctFixture) Kind() provider.Kind { return provider.KindGitLab }

func (ctFixture) Traits() contract.Traits {
	return contract.Traits{
		BaseStrategies:  BaseStrategies(),
		CheckStrategies: CheckStrategies(),
		NoStaleReviews:  true,
		// Issues and merge requests are numbered apart.
		SeparateIssueNumbers: true,
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

	// discs are the discussions, built from the Spec on the first request
	// and changed by the writes; nextID numbers the notes written.
	discs  []*ctDisc
	nextID int64
	// writes are the bodies of the notes written, in order.
	writes []string
	// positions are the positions of the inline posts, in order; deleted
	// the ids of the notes deleted.
	positions []ctPositionIn
	deleted   []int64
	// puts are the bodies of the PUTs of the merge request, as sent.
	puts []map[string]json.RawMessage
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

// mrBody is the merge request as the title, description and state are now.
func (g *ctGitLab) mrBody() map[string]any {
	count := strconv.Itoa(len(g.pr.Files))
	if g.fx.diffLimit || g.listLimited() {
		count = "1000+"
	}
	m := map[string]any{
		"id": 99007, "iid": 7, "project_id": 4242, "title": g.pr.Title, "description": g.pr.Description,
		"state": "opened", "draft": g.pr.Draft, "detailed_merge_status": "mergeable", "reviewers": g.reviewerUsers(),
		"web_url":       "https://gitlab.example.com/group/sub/project/-/merge_requests/7",
		"changes_count": count, "source_branch": g.pr.SourceBranch, "target_branch": g.pr.TargetBranch,
		"sha": g.pr.HeadSHA, "author": ctUser(g.pr.Author), "source_project_id": 4242, "target_project_id": 4242,
		"diff_refs": map[string]any{"base_sha": g.pr.BaseSHA, "head_sha": g.pr.HeadSHA, "start_sha": ctStartSHA},
	}
	if g.fx.webURL != "" {
		m["web_url"] = g.fx.webURL
	}
	if g.fx.emptyWebURL {
		m["web_url"] = ""
	}
	if g.fx.mergeStatus != "" {
		m["detailed_merge_status"] = g.fx.mergeStatus
	}
	if g.fx.state != "" {
		m["state"] = g.fx.state
	}
	return m
}

// mrJSON is mrBody for a read: the first read has empty diff_refs.
func (g *ctGitLab) mrJSON() map[string]any {
	m := g.mrBody()
	g.mrReads++
	if g.mrReads == 1 {
		m["diff_refs"] = nil
	}
	return m
}

// listLimited reports that the spec makes the host stop listing files
// (Env.ListLimit): GitLab then serves only the first diffs and a
// changes_count with a "+".
func (g *ctGitLab) listLimited() bool {
	return g.pr.Env.ListLimit > 0 && g.pr.Env.ListLimit < len(g.pr.Files)
}

// diffsJSON renders the files as GitLab lists them: the diff is the hunks
// without a header; a binary file has an empty diff (the extension rule
// marks it), and an oversized file is too_large with an empty diff.
func (g *ctGitLab) diffsJSON() []any {
	var out []any
	files := g.pr.Files
	if g.listLimited() {
		files = files[:g.pr.Env.ListLimit]
	}
	for _, f := range files {
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
	for i := len(g.pr.Commits) - 1; i >= 0; i-- {
		msg := g.pr.Commits[i]
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
	// The raw file request takes any media type; every other request JSON.
	wantAccept := "application/json"
	if p := r.URL.EscapedPath(); strings.HasPrefix(p, ctProject+"/repository/files/") && strings.HasSuffix(p, "/raw") {
		wantAccept = "*/*"
	}
	if got := r.Header.Get("Accept"); got != wantAccept {
		g.t.Errorf("request %s %s: Accept %q, want %q", r.Method, r.URL.EscapedPath(), got, wantAccept)
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
	if g.serveInline(w, r, path) || g.serveComments(w, r, path) || g.serveStatus(w, r, path) {
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
	case strings.HasPrefix(path, ctAPI+"/projects/group%2Fsub%2F") && strings.Count(path, "/") == 7 &&
		strings.Split(path, "/")[6] == "issues":
		g.serveIssue(w, strings.TrimPrefix(strings.Split(path, "/")[5], "group%2Fsub%2F"), strings.Split(path, "/")[7])
	default:
		g.t.Errorf("unexpected request %s %s", r.Method, path)
		contract.WriteError(w, http.StatusNotFound)
	}
}

// serveIssue answers GET /projects/group%2Fsub%2F{repo}/issues/{iid} like
// GitLab: label names as strings, state "opened" or "closed", and no answer
// for a merge request's number (issues and merge requests are numbered
// apart).
func (g *ctGitLab) serveIssue(w http.ResponseWriter, repo, number string) {
	is, status := g.pr.IssueAnswer("project", repo, number)
	if is == nil {
		contract.WriteError(w, status)
		return
	}
	if is.PullRequest {
		contract.WriteError(w, http.StatusNotFound)
		return
	}
	state := "opened"
	if is.Closed {
		state = "closed"
	}
	out := map[string]any{
		"id": 5000 + is.Number, "iid": is.Number, "title": is.Title, "state": state, "labels": append([]string{}, is.Labels...),
		"web_url": "https://gitlab.example.com/gitlab/group/sub/" + repo + "/-/issues/" + number, "description": nil,
	}
	if is.Body != "" {
		out["description"] = is.Body
	}
	ctJSON(w, http.StatusOK, out)
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
			if !slices.Equal(d.Notes, want) || pr.ChangedFilesAtLeast != limit || d.Incomplete != limit {
				t.Errorf("notes %q (at least %v, incomplete %v), want %q", d.Notes, pr.ChangedFilesAtLeast, d.Incomplete, want)
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
	spec := ctSpec(ctModified)
	spec.Commits = want
	p, ref, _, _ := ctFixture{}.serve(t, spec)
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
