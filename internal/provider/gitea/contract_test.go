package gitea_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
)

// TestContract runs the provider contract suite against a fake Gitea that
// serves each contract.Spec.
func TestContract(t *testing.T) {
	contract.Run(t, ctFixture{})
}

const (
	ctWeb     = "https://your-gitea.example"
	ctRepo    = "/api/v1/repos/octo/demo"
	ctPull    = ctRepo + "/pulls/7"
	ctWebPR   = ctWeb + "/octo/demo/pulls/7"
	ctFirstID = 1000 // ids of comments and reviews created by requests
)

// ctVerdicts is when the reviewers of a Spec gave their verdicts.
var ctVerdicts = time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)

type ctFixture struct{}

func (ctFixture) Kind() provider.Kind { return provider.KindGitea }

func (ctFixture) Traits() contract.Traits {
	return contract.Traits{
		BaseStrategies: []string{provider.BaseGiteaMergeBase, provider.BaseGiteaBaseSHA},
		// TODO(WP-2b): replace with Capabilities().ThreadResolution. Gitea
		// stores a resolver on review (inline) comments only.
		ResolvableThreads: []provider.ThreadKind{provider.ThreadInline},
	}
}

func (ctFixture) Serve(t *testing.T, pr contract.Spec) (provider.Provider, provider.PRRef) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = contract.StartServer(t, pr, newCtGitea(t, pr))
	cfg.Gitea.WebURL = ctWeb
	cfg.Secrets.GiteaToken = config.NewSecret(pr.Env.Token)
	cfg.Diff.MaxFilesFullContent = pr.Env.MaxFiles
	cfg.Diff.MaxFileBytes = pr.Env.MaxFileBytes
	p, err := gitea.NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7}
}

// ctReview is one review of the fake, with its comments.
type ctReview struct {
	id               int64
	user             contract.User
	state, body      string
	stale, dismissed bool
	at               time.Time
	comments         []ctReviewComment
}

type ctReviewComment struct {
	id         int64
	user       contract.User
	body, path string
	line       int
	at         time.Time
	resolver   *contract.User
}

// ctGitea is a fake Gitea API v1 that serves one contract.Spec: issue
// comments for general threads, one COMMENT review per inline comment (as
// Gitea records them), and a review per reviewer verdict. It answers any
// request it does not know with an error and fails the test.
type ctGitea struct {
	t  *testing.T
	pr contract.Spec

	mu      sync.Mutex
	issue   []contract.Comment
	reviews []*ctReview
	nextID  int64
}

func newCtGitea(t *testing.T, pr contract.Spec) *ctGitea {
	g := &ctGitea{t: t, pr: pr, nextID: ctFirstID}
	rid := int64(500)
	for _, th := range pr.Threads {
		if th.Kind == provider.ThreadGeneral {
			g.issue = append(g.issue, th.Comments...)
			continue
		}
		for i, c := range th.Comments {
			rc := ctReviewComment{id: c.ID, user: c.Author, body: c.Body, path: th.Path, line: th.Line, at: c.Created}
			if i == 0 && th.Resolved {
				rc.resolver = &pr.Author
			}
			rid++
			g.reviews = append(g.reviews, &ctReview{id: rid, user: c.Author, state: "COMMENT", at: c.Created,
				comments: []ctReviewComment{rc}})
		}
	}
	for i, r := range pr.Reviewers {
		rv := &ctReview{user: r.User, stale: r.Stale, dismissed: r.Dismissed, at: ctVerdicts.Add(time.Duration(i) * time.Hour)}
		switch {
		case r.Own:
			rv.state, rv.body = "COMMENT", "Automated review. "+contract.OwnMarker
		case r.State == provider.ReviewApproved:
			rv.state = "APPROVED"
		case r.State == provider.ReviewChangesRequested:
			rv.state = "REQUEST_CHANGES"
		default:
			continue // pending: only requested, see prJSON
		}
		rid++
		rv.id = rid
		g.reviews = append(g.reviews, rv)
	}
	return g
}

func ctUser(u contract.User) map[string]any {
	return map[string]any{"id": u.ID, "login": u.Login, "full_name": u.DisplayName}
}

func ctTime(t time.Time) string { return t.Format(time.RFC3339) }

func (g *ctGitea) prJSON() map[string]any {
	var requested []any
	for _, r := range g.pr.Reviewers {
		if r.State == provider.ReviewPending {
			requested = append(requested, ctUser(r.User))
		}
	}
	return map[string]any{
		"title": g.pr.Title, "body": g.pr.Description, "state": "open", "html_url": ctWebPR,
		"merge_base": g.pr.BaseSHA, "mergeable": true, "user": ctUser(g.pr.Author),
		"requested_reviewers": requested,
		"head":                map[string]any{"ref": g.pr.SourceBranch, "sha": g.pr.HeadSHA},
		"base":                map[string]any{"ref": g.pr.TargetBranch, "sha": g.pr.BaseSHA},
	}
}

// diff renders the files as git's unified diff of the PR.
func (g *ctGitea) diff() string {
	var b strings.Builder
	for _, f := range g.pr.Files {
		from, to := "a/"+f.BasePath(), "b/"+f.Path
		fmt.Fprintf(&b, "diff --git %s %s\n", from, to)
		switch f.Type {
		case provider.ChangeAdded:
			b.WriteString("new file mode 100644\nindex 0000000..1111111\n")
			from = "/dev/null"
		case provider.ChangeDeleted:
			b.WriteString("deleted file mode 100644\nindex 1111111..0000000\n")
			to = "/dev/null"
		case provider.ChangeRenamed:
			fmt.Fprintf(&b, "similarity index 80%%\nrename from %s\nrename to %s\nindex 1111111..2222222 100644\n", f.OldPath, f.Path)
		default:
			b.WriteString("index 1111111..2222222 100644\n")
		}
		if f.Binary {
			fmt.Fprintf(&b, "Binary files %s and %s differ\n", from, to)
			continue
		}
		fmt.Fprintf(&b, "--- %s\n+++ %s\n%s", from, to, f.Hunks)
	}
	return b.String()
}

func (g *ctGitea) filesJSON() []any {
	status := map[provider.ChangeType]string{provider.ChangeAdded: "added", provider.ChangeDeleted: "deleted",
		provider.ChangeRenamed: "renamed", provider.ChangeModified: "changed"}
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
		out = append(out, map[string]any{"filename": f.Path, "previous_filename": f.OldPath,
			"status": status[f.Type], "additions": add, "deletions": del, "changes": add + del})
	}
	return out
}

func (g *ctGitea) issueJSON(c contract.Comment) map[string]any {
	id := strconv.FormatInt(c.ID, 10)
	return map[string]any{
		"id": c.ID, "user": ctUser(c.Author), "body": c.Body, "type": "comment",
		"created_at": ctTime(c.Created), "updated_at": ctTime(c.Created),
		"html_url":         ctWebPR + "#issuecomment-" + id,
		"issue_url":        ctWeb + ctRepo + "/issues/7",
		"pull_request_url": ctWeb + ctPull,
	}
}

func (r *ctReview) json() map[string]any {
	return map[string]any{
		"id": r.id, "user": ctUser(r.user), "state": r.state, "body": r.body, "stale": r.stale,
		"dismissed": r.dismissed, "official": false, "submitted_at": ctTime(r.at), "updated_at": ctTime(r.at),
		"html_url": ctWebPR + "#pullrequestreview-" + strconv.FormatInt(r.id, 10),
	}
}

func (c *ctReviewComment) json() map[string]any {
	m := map[string]any{
		"id": c.id, "user": ctUser(c.user), "body": c.body, "path": c.path,
		"position": c.line, "original_position": c.line,
		"created_at": ctTime(c.at), "updated_at": ctTime(c.at),
		"html_url": ctWebPR + "/files#issuecomment-" + strconv.FormatInt(c.id, 10), "resolver": nil,
	}
	if c.resolver != nil {
		m["resolver"] = ctUser(*c.resolver)
	}
	return m
}

func ctJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ctPaged serves a list on page 1 (or without page) and [] after it.
func ctPaged[T any](w http.ResponseWriter, r *http.Request, items []T) {
	if p := r.URL.Query().Get("page"); p != "" && p != "1" {
		items = nil
	}
	if items == nil {
		items = []T{}
	}
	ctJSON(w, http.StatusOK, items)
}

func (g *ctGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+g.pr.Env.Token {
		g.t.Errorf("request %s %s: unexpected Authorization header shape", r.Method, r.URL.EscapedPath())
		contract.WriteError(w, http.StatusUnauthorized)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	path := r.URL.EscapedPath()
	get := r.Method == http.MethodGet
	switch {
	case get && path == "/api/v1/user":
		ctJSON(w, http.StatusOK, ctUser(g.pr.TokenUser))
	case get && path == ctPull:
		ctJSON(w, http.StatusOK, g.prJSON())
	case get && path == ctPull+".diff":
		_, _ = io.WriteString(w, g.diff())
	case get && path == ctPull+"/files":
		ctPaged(w, r, g.filesJSON())
	case get && path == ctPull+"/commits", get && path == ctRepo+"/branch_protections":
		ctPaged[any](w, r, nil)
	case get && strings.HasPrefix(path, ctRepo+"/raw/"):
		g.serveRaw(w, r, strings.TrimPrefix(path, ctRepo+"/raw/"))
	case path == ctRepo+"/issues/7/comments":
		g.serveIssueComments(w, r)
	case strings.HasPrefix(path, ctRepo+"/issues/comments/"):
		g.serveIssueComment(w, r, strings.TrimPrefix(path, ctRepo+"/issues/comments/"))
	case path == ctPull+"/reviews":
		g.serveReviews(w, r)
	case get && strings.HasPrefix(path, ctPull+"/reviews/") && strings.HasSuffix(path, "/comments"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, ctPull+"/reviews/"), "/comments")
		for _, rv := range g.reviews {
			if strconv.FormatInt(rv.id, 10) == id {
				var out []any
				for i := range rv.comments {
					out = append(out, rv.comments[i].json())
				}
				ctPaged(w, r, out)
				return
			}
		}
		contract.WriteError(w, http.StatusNotFound)
	default:
		g.t.Errorf("unexpected request %s %s", r.Method, path)
		contract.WriteError(w, http.StatusNotFound)
	}
}

// serveRaw answers a raw content request; content the Spec does not have
// is a plain 404.
func (g *ctGitea) serveRaw(w http.ResponseWriter, r *http.Request, escaped string) {
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

func (g *ctGitea) serveIssueComments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var out []any
		for _, c := range g.issue {
			out = append(out, g.issueJSON(c))
		}
		ctPaged(w, r, out)
	case http.MethodPost:
		var in struct {
			Body string `json:"body"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			contract.WriteError(w, http.StatusUnprocessableEntity)
			return
		}
		c := contract.Comment{ID: g.nextID, Author: g.pr.TokenUser, Body: in.Body, Created: time.Now().UTC()}
		g.nextID++
		g.issue = append(g.issue, c)
		ctJSON(w, http.StatusCreated, g.issueJSON(c))
	default:
		contract.WriteError(w, http.StatusMethodNotAllowed)
	}
}

// serveIssueComment answers GET and PATCH of one comment. Like Gitea, it
// answers 204 without a body for the id of a review comment.
func (g *ctGitea) serveIssueComment(w http.ResponseWriter, r *http.Request, id string) {
	for i := range g.issue {
		c := &g.issue[i]
		if strconv.FormatInt(c.ID, 10) != id {
			continue
		}
		switch r.Method {
		case http.MethodGet:
			ctJSON(w, http.StatusOK, g.issueJSON(*c))
		case http.MethodPatch:
			var in struct {
				Body string `json:"body"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				contract.WriteError(w, http.StatusUnprocessableEntity)
				return
			}
			c.Body = in.Body
			ctJSON(w, http.StatusOK, g.issueJSON(*c))
		default:
			contract.WriteError(w, http.StatusMethodNotAllowed)
		}
		return
	}
	for _, rv := range g.reviews {
		for _, c := range rv.comments {
			if strconv.FormatInt(c.id, 10) == id && r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	contract.WriteError(w, http.StatusNotFound)
}

// serveReviews lists the reviews, or creates one. Like Gitea, a review with
// a comment outside the diff is refused with 500 and not created.
func (g *ctGitea) serveReviews(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		var out []any
		for _, rv := range g.reviews {
			out = append(out, rv.json())
		}
		ctPaged(w, r, out)
		return
	}
	if r.Method != http.MethodPost {
		contract.WriteError(w, http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Event    string `json:"event"`
		CommitID string `json:"commit_id"`
		Comments []struct {
			Path        string `json:"path"`
			Body        string `json:"body"`
			NewPosition int    `json:"new_position"`
		} `json:"comments"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Event != "COMMENT" || in.CommitID != g.pr.HeadSHA {
		g.t.Errorf("unexpected review request (event %q, commit %q)", in.Event, in.CommitID)
		contract.WriteError(w, http.StatusUnprocessableEntity)
		return
	}
	rv := &ctReview{id: g.nextID, user: g.pr.TokenUser, state: "COMMENT", at: time.Now().UTC()}
	g.nextID++
	for _, c := range in.Comments {
		if !ctAnchorable(g.pr.Files, c.Path, c.NewPosition) {
			contract.WriteError(w, http.StatusInternalServerError)
			return
		}
		rv.comments = append(rv.comments, ctReviewComment{id: g.nextID, user: g.pr.TokenUser, body: c.Body,
			path: c.Path, line: c.NewPosition, at: rv.at})
		g.nextID++
	}
	g.reviews = append(g.reviews, rv)
	ctJSON(w, http.StatusOK, rv.json())
}

func ctAnchorable(files []contract.File, path string, line int) bool {
	for i := range files {
		if f := &files[i]; f.Path == path && !f.Binary {
			_, ok := f.NewLines()[line]
			return ok
		}
	}
	return false
}
