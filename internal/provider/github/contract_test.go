package github_test

import (
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
	"github.com/nevzatcirak/review-mcp/internal/provider/github"
)

// TestContract runs the provider contract suite against a fake GitHub
// Enterprise Server API that serves each contract.Spec. The read path is
// WP-2j, the comments WP-2k, the inline comments WP-2l and the review
// status and description edits WP-2m: every case runs.
func TestContract(t *testing.T) {
	contract.Run(t, ctFixture{})
}

const (
	// ctAPI is the API prefix the provider derives for a GHES web base.
	ctAPI  = "/api/v3"
	ctRepo = ctAPI + "/repos/octo/demo"
	ctPull = ctRepo + "/pulls/7"
	// ctRepoByID is the path form GitHub uses in its Link headers; the fake
	// pages only through it, so a next page can only be found by reading
	// the Link header.
	ctRepoByID = ctAPI + "/repositories/4242"
	// ctTargetTip is the target branch's head, which the pull request
	// records as base.sha. The merge base (Spec.BaseSHA) differs from it.
	ctTargetTip = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	// ctPageSize is the fake's page size: smaller than per_page, so every
	// list of the sample pull request has several pages.
	ctPageSize = 2
)

type ctFixture struct{}

func (ctFixture) Kind() provider.Kind { return provider.KindGitHub }

func (ctFixture) Traits() contract.Traits {
	return contract.Traits{
		BaseStrategies: github.BaseStrategies(),
		InlineRanges:   true,
	}
}

func (ctFixture) Serve(t *testing.T, pr contract.Spec) (provider.Provider, provider.PRRef) {
	t.Helper()
	p, ref, _ := serveCt(t, pr)
	return p, ref
}

// serveCt serves pr through a new fake and also returns the fake.
func serveCt(t *testing.T, pr contract.Spec) (provider.Provider, provider.PRRef, *ctGitHub) {
	t.Helper()
	g := newCtGitHub(t, pr)
	cfg := config.Defaults()
	// The web base is the fake's origin; the API base is derived from it
	// as for GitHub Enterprise Server ({base}/api/v3).
	cfg.GitHub.BaseURL = contract.StartServer(t, pr, g)
	cfg.Secrets.GitHubToken = config.NewSecret(pr.Env.Token)
	cfg.Diff.MaxFilesFullContent = pr.Env.MaxFiles
	cfg.Diff.MaxFileBytes = pr.Env.MaxFileBytes
	p, err := github.NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, provider.PRRef{Kind: provider.KindGitHub, Namespace: "octo", Repo: "demo", Number: 7}, g
}

// TestContractInlineRequests: on the contract fake, inline comments that
// all fit the diff cost exactly one write (the review); when one does not,
// GitHub refuses the review (422) and each item is then posted alone: the
// review plus one write per item. Only the refused item is unanchorable.
func TestContractInlineRequests(t *testing.T) {
	const path = "src/app.go"
	spec := contract.Spec{
		Title: "T", Author: contract.User{ID: 101, Login: "alice"}, SourceBranch: "f", TargetBranch: "main",
		HeadSHA: "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1", BaseSHA: "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2",
		TokenUser: contract.User{ID: 900, Login: "review-bot"},
		Files: []contract.File{{
			Path: path, Type: provider.ChangeModified,
			Base:  "a\nb\nc\n",
			Head:  "a\nB\nc\n",
			Hunks: "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n",
		}},
		Env: contract.Env{Token: "tok-FAKE-wp2l", MaxFiles: 10, MaxFileBytes: 4096}, //nolint:gosec // synthetic test value
	}
	pr := &provider.PullRequest{HeadSHA: spec.HeadSHA}
	good := []provider.InlineComment{
		{Path: path, Line: 2, LineType: provider.LineAdded, Body: "One line."},
		{Path: path, Line: 1, EndLine: 3, LineType: provider.LineContext, Body: "Three lines."},
	}
	outside := provider.InlineComment{Path: path, Line: 9, LineType: provider.LineContext, Body: "Outside."}
	const reviews, comments = "POST " + ctPull + "/reviews", "POST " + ctPull + "/comments"

	p, ref, g := serveCt(t, spec)
	res, err := p.PostInlineComments(t.Context(), ref, pr, good)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if !r.Posted || r.ID == "" || r.Reason != provider.InlineReasonPosted {
			t.Errorf("item %d: %+v, want posted with an id", i, r)
		}
	}
	if want := []string{reviews}; !slices.Equal(g.writesSoFar(), want) {
		t.Errorf("writes %q, want %q", g.writesSoFar(), want)
	}

	p, ref, g = serveCt(t, spec)
	items := []provider.InlineComment{good[0], outside, good[1]}
	if res, err = p.PostInlineComments(t.Context(), ref, pr, items); err != nil {
		t.Fatal(err)
	}
	reasons := []provider.InlineReason{res[0].Reason, res[1].Reason, res[2].Reason}
	if want := []provider.InlineReason{provider.InlineReasonPosted, provider.InlineReasonUnanchorable, provider.InlineReasonPosted}; !slices.Equal(reasons, want) {
		t.Errorf("reasons %q, want %q", reasons, want)
	}
	if want := []string{reviews, comments, comments, comments}; !slices.Equal(g.writesSoFar(), want) {
		t.Errorf("writes %q, want %q", g.writesSoFar(), want)
	}
}

// ctGitHub is a fake GitHub REST API v3 that serves one contract.Spec. It
// checks the headers of every request and answers any request it does not
// know with an error that fails the test.
type ctGitHub struct {
	t  *testing.T
	pr contract.Spec
	mu sync.Mutex

	// issue are the issue comments (general threads); inline are the review
	// comments, with the id of the root they reply to (0 for a root).
	issue  []contract.Comment
	inline []ctInline
	nextID int64
	// ownBodies are the bodies of the review comments of a review that the
	// Spec's own review owns, by review id: the marker of review-mcp's own
	// review lives in a comment, as the inline batch has no body of its own.
	ownBodies map[int64][]string
	// writes records every request other than a GET, as "METHOD path".
	writes []string
}

type ctInline struct {
	contract.Comment
	path    string
	line    int
	replyTo int64
	// review is the id of the review the comment was created in; 0 for a
	// comment of the Spec or one posted alone.
	review int64
}

// ctFirstID is the first id of a comment created by a request.
const ctFirstID = 1000

func newCtGitHub(t *testing.T, pr contract.Spec) *ctGitHub {
	g := &ctGitHub{t: t, pr: pr, nextID: ctFirstID}
	for _, th := range pr.Threads {
		if th.Kind == provider.ThreadGeneral {
			g.issue = append(g.issue, th.Comments...)
			continue
		}
		for i, c := range th.Comments {
			in := ctInline{Comment: c, path: th.Path, line: th.Line}
			if i > 0 {
				in.replyTo = th.Comments[0].ID
			}
			g.inline = append(g.inline, in)
		}
	}
	return g
}

func ctUser(u contract.User) map[string]any {
	return map[string]any{"id": u.ID, "login": u.Login}
}

func ctJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-RateLimit-Remaining", "4999")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// requestedJSON is requested_reviewers: the reviewers of the Spec that have
// no verdict yet.
func (g *ctGitHub) requestedJSON() []any {
	out := []any{}
	for _, r := range g.pr.Reviewers {
		if r.State == provider.ReviewPending {
			out = append(out, ctUser(r.User))
		}
	}
	return out
}

// ctOldCommit is the commit a stale review was given on.
const ctOldCommit = "d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4"

// reviewsJSON renders the Spec's reviewers as GitHub lists reviews: a
// dismissed verdict is DISMISSED, a stale one carries an older commit_id,
// and the own review is a COMMENTED review without a body whose marker is in
// one of its comments. A reviewer that is only requested has no review.
func (g *ctGitHub) reviewsJSON() []any {
	out := []any{}
	base := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, r := range g.pr.Reviewers {
		m := map[string]any{"id": 5000 + i, "user": ctUser(r.User), "body": "", "commit_id": g.pr.HeadSHA,
			"submitted_at": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)}
		switch {
		case r.Own:
			m["state"] = "COMMENTED"
			if g.ownBodies == nil {
				g.ownBodies = map[int64][]string{}
			}
			g.ownBodies[int64(5000+i)] = []string{contract.OwnMarker}
		case r.Dismissed:
			m["state"] = "DISMISSED"
		case r.State == provider.ReviewApproved:
			m["state"] = "APPROVED"
		case r.State == provider.ReviewChangesRequested:
			m["state"] = "CHANGES_REQUESTED"
		default:
			continue
		}
		if r.Stale {
			m["commit_id"] = ctOldCommit
		}
		out = append(out, m)
	}
	return out
}

func (g *ctGitHub) prJSON() map[string]any {
	return map[string]any{
		"number": 7, "title": g.pr.Title, "body": g.pr.Description, "state": "open", "draft": g.pr.Draft,
		"requested_reviewers": g.requestedJSON(), "requested_teams": []any{}, "commits": 1,
		"merged": false, "merged_at": nil, "mergeable": true, "mergeable_state": "clean",
		"html_url":      "https://github.example.com/octo/demo/pull/7",
		"changed_files": len(g.pr.Files),
		"user":          ctUser(g.pr.Author),
		"head":          map[string]any{"ref": g.pr.SourceBranch, "sha": g.pr.HeadSHA},
		"base":          map[string]any{"ref": g.pr.TargetBranch, "sha": ctTargetTip},
	}
}

// ctStatus is GitHub's status word of a change type.
var ctStatus = map[provider.ChangeType]string{
	provider.ChangeAdded: "added", provider.ChangeDeleted: "removed",
	provider.ChangeRenamed: "renamed", provider.ChangeModified: "modified",
}

// filesJSON renders the files as GitHub lists them: the patch is the hunks
// without the last newline; a binary file has no patch and no line
// changes; an oversized file has its line counts but no patch (GitHub omits
// the patch of a diff too large to show).
func (g *ctGitHub) filesJSON() []any {
	var out []any
	for _, f := range g.pr.Files {
		add, del := 0, 0
		for _, l := range strings.Split(f.Hunks, "\n") {
			switch {
			case strings.HasPrefix(l, "+"):
				add++
			case strings.HasPrefix(l, "-"):
				del++
			}
		}
		m := map[string]any{"sha": "1111111111111111111111111111111111111111", "filename": f.Path,
			"status": ctStatus[f.Type], "additions": add, "deletions": del, "changes": add + del}
		if f.OldPath != "" {
			m["previous_filename"] = f.OldPath
		}
		if !f.Binary && !f.TooLarge {
			m["patch"] = strings.TrimSuffix(f.Hunks, "\n")
		}
		out = append(out, m)
	}
	return out
}

// ctPage serves items in pages of ctPageSize. The first page is asked for
// under the repository's name; every further page only through the
// opaque cursor of a Link header, under the repository's id. A page number
// in a request means the client guessed a page: the test fails.
func (g *ctGitHub) ctPage(w http.ResponseWriter, r *http.Request, items []any, byIDPath string) {
	q := r.URL.Query()
	if q.Get("page") != "" {
		g.t.Errorf("request %s asks for a page number: pages must come from the Link header", r.URL.EscapedPath())
		contract.WriteError(w, http.StatusBadRequest)
		return
	}
	start := 0
	if c := q.Get("cursor"); c != "" {
		raw, err := base64.RawURLEncoding.DecodeString(c)
		n, aerr := strconv.Atoi(strings.TrimPrefix(string(raw), "offset:"))
		if err != nil || aerr != nil || n < 0 || n > len(items) {
			contract.WriteError(w, http.StatusBadRequest)
			return
		}
		start = n
	}
	end := min(start+ctPageSize, len(items))
	if end < len(items) {
		next := "http://" + r.Host + byIDPath + "?per_page=100&cursor=" +
			base64.RawURLEncoding.EncodeToString([]byte("offset:"+strconv.Itoa(end)))
		w.Header().Set("Link", `<`+next+`>; rel="next", <http://`+r.Host+byIDPath+`?per_page=100>; rel="first"`)
	}
	page := items[start:end]
	if page == nil {
		page = []any{}
	}
	ctJSON(w, http.StatusOK, page)
}

func (g *ctGitHub) checkHeaders(r *http.Request, accept string) bool {
	ok := true
	if r.Header.Get("Authorization") != "Bearer "+g.pr.Env.Token {
		g.t.Errorf("request %s %s: unexpected Authorization header shape", r.Method, r.URL.EscapedPath())
		ok = false
	}
	if got := r.Header.Get("Accept"); got != accept {
		g.t.Errorf("request %s %s: Accept %q, want %q", r.Method, r.URL.EscapedPath(), got, accept)
		ok = false
	}
	if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
		g.t.Errorf("request %s %s: X-GitHub-Api-Version %q", r.Method, r.URL.EscapedPath(), got)
		ok = false
	}
	if !strings.HasPrefix(r.Header.Get("User-Agent"), "review-mcp/") {
		g.t.Errorf("request %s %s: User-Agent %q", r.Method, r.URL.EscapedPath(), r.Header.Get("User-Agent"))
		ok = false
	}
	return ok
}

func (g *ctGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	path := r.URL.EscapedPath()
	accept := "application/vnd.github+json"
	if strings.HasPrefix(path, ctRepo+"/contents/") {
		accept = "application/vnd.github.raw"
	}
	if !g.checkHeaders(r, accept) {
		contract.WriteError(w, http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		g.writes = append(g.writes, r.Method+" "+path)
	}
	if g.serveInline(w, r, path) || g.serveComments(w, r, path) {
		return
	}
	if r.Method == http.MethodPatch && path == ctPull {
		g.patchPull(w, r)
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
	case path == ctPull:
		ctJSON(w, http.StatusOK, g.prJSON())
	case path == ctRepo+"/rules/branches/"+g.pr.TargetBranch:
		ctJSON(w, http.StatusOK, []any{
			map[string]any{"type": "deletion"},
			map[string]any{"type": "pull_request", "parameters": map[string]any{"required_approving_review_count": 2}},
		})
	case path == ctRepo+"/branches/"+g.pr.TargetBranch+"/protection":
		contract.WriteError(w, http.StatusForbidden) // classic protection needs admin
	case path == ctRepo+"/compare/"+ctTargetTip+"..."+g.pr.HeadSHA:
		ctJSON(w, http.StatusOK, map[string]any{"status": "ahead", "merge_base_commit": map[string]any{"sha": g.pr.BaseSHA}})
	case path == ctPull+"/files" || path == ctRepoByID+"/pulls/7/files":
		g.ctPage(w, r, g.filesJSON(), ctRepoByID+"/pulls/7/files")
	case path == ctPull+"/commits" || path == ctRepoByID+"/pulls/7/commits":
		g.ctPage(w, r, nil, ctRepoByID+"/pulls/7/commits")
	case strings.HasPrefix(path, ctRepo+"/contents/"):
		g.serveContents(w, r, strings.TrimPrefix(path, ctRepo+"/contents/"))
	default:
		g.t.Errorf("unexpected request %s %s", r.Method, path)
		contract.WriteError(w, http.StatusNotFound)
	}
}

// patchPull applies PATCH /pulls/7. Like GitHub it changes only the fields
// that are sent; a body with any other field than title and body fails the
// test, since review-mcp must send nothing else.
func (g *ctGitHub) patchPull(w http.ResponseWriter, r *http.Request) {
	var in map[string]*string
	if json.NewDecoder(r.Body).Decode(&in) != nil || len(in) == 0 {
		contract.WriteError(w, http.StatusUnprocessableEntity)
		return
	}
	for k, v := range in {
		switch {
		case k == "title" && v != nil:
			g.pr.Title = *v
		case k == "body" && v != nil:
			g.pr.Description = *v
		default:
			g.t.Errorf("PATCH /pulls/7 carries the field %q", k)
			contract.WriteError(w, http.StatusUnprocessableEntity)
			return
		}
	}
	ctJSON(w, http.StatusOK, g.prJSON())
}

// serveContents answers a raw contents request at the merge base or the
// head; content the Spec does not have is a 404.
func (g *ctGitHub) serveContents(w http.ResponseWriter, r *http.Request, escaped string) {
	p, err := url.PathUnescape(escaped)
	if err != nil {
		contract.WriteError(w, http.StatusBadRequest)
		return
	}
	sha := r.URL.Query().Get("ref")
	for _, f := range g.pr.Files {
		switch {
		case f.Type != provider.ChangeAdded && sha == g.pr.BaseSHA && p == f.BasePath():
			_, _ = io.WriteString(w, f.Base)
			return
		case f.Type != provider.ChangeDeleted && sha == g.pr.HeadSHA && p == f.Path:
			_, _ = io.WriteString(w, f.Head)
			return
		}
	}
	contract.WriteError(w, http.StatusNotFound)
}

const (
	ctIssueURL = "https://github.example.com/api/v3/repos/octo/demo/issues/7"
	ctPullURL  = "https://github.example.com/api/v3/repos/octo/demo/pulls/7"
)

func (g *ctGitHub) issueJSON(c contract.Comment) map[string]any {
	return map[string]any{"id": c.ID, "user": ctUser(c.Author), "body": c.Body,
		"created_at": c.Created.Format(time.RFC3339), "updated_at": c.Created.Format(time.RFC3339),
		"html_url":  "https://github.example.com/octo/demo/pull/7#issuecomment-" + strconv.FormatInt(c.ID, 10),
		"issue_url": ctIssueURL}
}

func (g *ctGitHub) inlineJSON(c ctInline) map[string]any {
	m := map[string]any{"id": c.ID, "user": ctUser(c.Author), "body": c.Body, "path": c.path,
		"line": c.line, "original_line": c.line, "side": "RIGHT",
		"created_at": c.Created.Format(time.RFC3339), "updated_at": c.Created.Format(time.RFC3339),
		"html_url":         "https://github.example.com/octo/demo/pull/7#discussion_r" + strconv.FormatInt(c.ID, 10),
		"pull_request_url": ctPullURL}
	if c.replyTo != 0 {
		m["in_reply_to_id"] = c.replyTo
	}
	return m
}

// ctBody decodes the {"body": ...} request body of a comment write.
func ctBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Body string `json:"body"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		contract.WriteError(w, http.StatusUnprocessableEntity)
		return "", false
	}
	return in.Body, true
}

// serveComments serves the comment endpoints (WP-2k) and reports whether it
// answered the request. The reviews list is empty: the sample pull request
// has no review with a body.
func (g *ctGitHub) serveComments(w http.ResponseWriter, r *http.Request, path string) bool {
	get, post, patch := r.Method == http.MethodGet, r.Method == http.MethodPost, r.Method == http.MethodPatch
	switch {
	case get && (path == ctRepo+"/issues/7/comments" || path == ctRepoByID+"/issues/7/comments"):
		var out []any
		for _, c := range g.issue {
			out = append(out, g.issueJSON(c))
		}
		g.ctPage(w, r, out, ctRepoByID+"/issues/7/comments")
	case post && path == ctRepo+"/issues/7/comments":
		body, ok := ctBody(w, r)
		if !ok {
			return true
		}
		c := contract.Comment{ID: g.nextID, Author: g.pr.TokenUser, Body: body, Created: time.Now().UTC()}
		g.nextID++
		g.issue = append(g.issue, c)
		ctJSON(w, http.StatusCreated, g.issueJSON(c))
	case (get || patch) && strings.HasPrefix(path, ctRepo+"/issues/comments/"):
		id := strings.TrimPrefix(path, ctRepo+"/issues/comments/")
		for i := range g.issue {
			c := &g.issue[i]
			if strconv.FormatInt(c.ID, 10) != id {
				continue
			}
			if patch {
				body, ok := ctBody(w, r)
				if !ok {
					return true
				}
				c.Body = body
			}
			ctJSON(w, http.StatusOK, g.issueJSON(*c))
			return true
		}
		contract.WriteError(w, http.StatusNotFound)
	case get && (path == ctPull+"/comments" || path == ctRepoByID+"/pulls/7/comments"):
		var out []any
		for _, c := range g.inline {
			out = append(out, g.inlineJSON(c))
		}
		g.ctPage(w, r, out, ctRepoByID+"/pulls/7/comments")
	case (get || patch) && strings.HasPrefix(path, ctRepo+"/pulls/comments/"):
		id := strings.TrimPrefix(path, ctRepo+"/pulls/comments/")
		for i := range g.inline {
			c := &g.inline[i]
			if strconv.FormatInt(c.ID, 10) != id {
				continue
			}
			if patch {
				body, ok := ctBody(w, r)
				if !ok {
					return true
				}
				c.Body = body
			}
			ctJSON(w, http.StatusOK, g.inlineJSON(*c))
			return true
		}
		contract.WriteError(w, http.StatusNotFound)
	case post && strings.HasPrefix(path, ctPull+"/comments/") && strings.HasSuffix(path, "/replies"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, ctPull+"/comments/"), "/replies")
		for _, root := range g.inline {
			if strconv.FormatInt(root.ID, 10) != id {
				continue
			}
			if root.replyTo != 0 { // GitHub: replies to a reply are refused
				contract.WriteError(w, http.StatusUnprocessableEntity)
				return true
			}
			body, ok := ctBody(w, r)
			if !ok {
				return true
			}
			c := ctInline{Comment: contract.Comment{ID: g.nextID, Author: g.pr.TokenUser, Body: body, Created: time.Now().UTC()},
				path: root.path, line: root.line, replyTo: root.ID}
			g.nextID++
			g.inline = append(g.inline, c)
			ctJSON(w, http.StatusCreated, g.inlineJSON(c))
			return true
		}
		contract.WriteError(w, http.StatusNotFound)
	case get && (path == ctPull+"/reviews" || path == ctRepoByID+"/pulls/7/reviews"):
		g.ctPage(w, r, g.reviewsJSON(), ctRepoByID+"/pulls/7/reviews")
	case get && strings.HasPrefix(path, ctPull+"/reviews/"):
		contract.WriteError(w, http.StatusNotFound)
	default:
		return false
	}
	return true
}

// writesSoFar returns a copy of writes.
func (g *ctGitHub) writesSoFar() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.writes)
}

// ctPosition is a comment position as GitHub's review endpoints take it.
type ctPosition struct {
	CommitID  string `json:"commit_id"`
	Path      string `json:"path"`
	Body      string `json:"body"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line"`
	StartSide string `json:"start_side"`
	// Position is the deprecated diff-relative position; review-mcp must
	// not send it.
	Position *int `json:"position"`
}

// anchorable reports whether GitHub would accept c on the head commit: the
// file is in the diff, both sides are RIGHT and every line from start_line
// (or line) to line is a new-side line of one hunk.
func (g *ctGitHub) anchorable(c ctPosition) bool {
	if c.Side != "RIGHT" || c.Position != nil || c.Line <= 0 || strings.TrimSpace(c.Body) == "" {
		return false
	}
	start := c.Line
	if c.StartLine != 0 {
		if c.StartSide != "RIGHT" || c.StartLine >= c.Line {
			return false
		}
		start = c.StartLine
	}
	for i := range g.pr.Files {
		f := &g.pr.Files[i]
		if f.Path != c.Path || f.Type == provider.ChangeDeleted || f.Binary {
			continue
		}
		hunk := ctHunks(f.Hunks)
		h, ok := hunk[start]
		for n := start; ok && n <= c.Line; n++ {
			if hn, in := hunk[n]; !in || hn != h {
				return false
			}
		}
		return ok
	}
	return false
}

// ctHunks maps every new-side line of hunks to the number of its hunk.
func ctHunks(hunks string) map[int]int {
	out := map[int]int{}
	n, h := 0, 0
	for _, l := range strings.Split(hunks, "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			h++
			_, rest, _ := strings.Cut(l, " +")
			end := strings.IndexAny(rest, ", ")
			if end < 0 {
				end = len(rest)
			}
			n, _ = strconv.Atoi(rest[:end])
		case strings.HasPrefix(l, "+"), strings.HasPrefix(l, " "):
			out[n] = h
			n++
		}
	}
	return out
}

// create stores c as a review comment of the token's user.
func (g *ctGitHub) create(c ctPosition, review int64) ctInline {
	in := ctInline{Comment: contract.Comment{ID: g.nextID, Author: g.pr.TokenUser, Body: c.Body, Created: time.Now().UTC()},
		path: c.Path, line: c.Line, review: review}
	g.nextID++
	g.inline = append(g.inline, in)
	return in
}

// serveInline serves the inline writes (WP-2l) and reports whether it
// answered the request. Like GitHub, POST /reviews creates the review and
// all its comments, or, when any comment cannot be placed on the diff,
// nothing (422); POST /comments places one comment.
func (g *ctGitHub) serveInline(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case r.Method == http.MethodPost && path == ctPull+"/reviews":
		var in struct {
			CommitID string       `json:"commit_id"`
			Event    string       `json:"event"`
			Body     *string      `json:"body"`
			Comments []ctPosition `json:"comments"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.CommitID != g.pr.HeadSHA || in.Event != "COMMENT" ||
			in.Body != nil || len(in.Comments) == 0 {
			g.t.Errorf("unexpected review request: commit %q, event %q, body set %v, %d comments",
				in.CommitID, in.Event, in.Body != nil, len(in.Comments))
			contract.WriteError(w, http.StatusUnprocessableEntity)
			return true
		}
		for _, c := range in.Comments {
			if c.CommitID != "" {
				g.t.Errorf("a review comment carries its own commit_id")
			}
			if !g.anchorable(c) {
				contract.WriteError(w, http.StatusUnprocessableEntity)
				return true
			}
		}
		rid := g.nextID
		g.nextID++
		for _, c := range in.Comments {
			g.create(c, rid)
		}
		ctJSON(w, http.StatusOK, map[string]any{"id": rid, "state": "COMMENTED", "user": ctUser(g.pr.TokenUser),
			"html_url": "https://github.example.com/octo/demo/pull/7#pullrequestreview-" + strconv.FormatInt(rid, 10)})
	case r.Method == http.MethodGet && strings.HasPrefix(path, ctPull+"/reviews/") && strings.HasSuffix(path, "/comments"),
		r.Method == http.MethodGet && strings.HasPrefix(path, ctRepoByID+"/pulls/7/reviews/") && strings.HasSuffix(path, "/comments"):
		id := strings.TrimSuffix(path, "/comments")
		id = id[strings.LastIndexByte(id, '/')+1:]
		var out []any
		for _, c := range g.inline {
			if c.review != 0 && strconv.FormatInt(c.review, 10) == id {
				out = append(out, g.inlineJSON(c))
			}
		}
		if rid, err := strconv.ParseInt(id, 10, 64); err == nil {
			for _, b := range g.ownBodies[rid] {
				out = append(out, g.inlineJSON(ctInline{Comment: contract.Comment{ID: 9000 + rid, Author: g.pr.TokenUser, Body: b}, path: "x", line: 1}))
			}
		}
		g.ctPage(w, r, out, ctRepoByID+"/pulls/7/reviews/"+id+"/comments")
	case r.Method == http.MethodPost && path == ctPull+"/comments":
		var c ctPosition
		if json.NewDecoder(r.Body).Decode(&c) != nil || c.CommitID != g.pr.HeadSHA {
			g.t.Errorf("unexpected comment request: commit %q", c.CommitID)
			contract.WriteError(w, http.StatusUnprocessableEntity)
			return true
		}
		if !g.anchorable(c) {
			contract.WriteError(w, http.StatusUnprocessableEntity)
			return true
		}
		ctJSON(w, http.StatusCreated, g.inlineJSON(g.create(c, 0)))
	default:
		return false
	}
	return true
}
