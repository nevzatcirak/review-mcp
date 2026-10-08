package bitbucketserver_test

import (
	"encoding/json"
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
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
)

// TestContract runs the provider contract suite against a fake Bitbucket
// Server that serves each contract.Spec.
func TestContract(t *testing.T) {
	contract.Run(t, ctFixture{})
}

const (
	ctPrefix    = "/bitbucket" // context path
	ctRepo      = "/rest/api/1.0/projects/PROJ/repos/demo"
	ctPull      = ctRepo + "/pull-requests/7"
	ctLatest    = "/rest/api/latest/projects/PROJ/repos/demo/pull-requests/7"
	ctProps     = "/rest/api/1.0/application-properties"
	ctTargetSHA = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3" // head of the target branch
	ctOlderSHA  = "d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4" // an older PR commit, for stale verdicts
	ctFirstID   = 1000                                       // ids of comments created by requests
)

type ctFixture struct{}

func (ctFixture) Kind() provider.Kind { return provider.KindBitbucketServer }

func (ctFixture) Traits() contract.Traits {
	return contract.Traits{
		BaseStrategies:       bitbucketserver.BaseStrategies(),
		OmitsNoNewlineMarker: true,
	}
}

func (ctFixture) Serve(t *testing.T, pr contract.Spec) (provider.Provider, provider.PRRef) {
	t.Helper()
	cfg := config.Defaults()
	cfg.BitbucketServer.BaseURL = contract.StartServer(t, pr, newCtBBS(t, pr)) + ctPrefix
	cfg.Secrets.BitbucketServerToken = config.NewSecret(pr.Env.Token)
	cfg.Diff.MaxFilesFullContent = pr.Env.MaxFiles
	cfg.Diff.MaxFileBytes = pr.Env.MaxFileBytes
	p, err := bitbucketserver.NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, provider.PRRef{Kind: provider.KindBitbucketServer, Namespace: "PROJ", Repo: "demo", Number: 7}
}

// ctComment is one comment of the fake. Replies hang off their root.
type ctComment struct {
	c        contract.Comment
	version  int
	resolved bool
	anchor   map[string]any // inline roots only
	replies  []*ctComment
}

// ctBBS is a fake Bitbucket Server REST API that serves one contract.Spec:
// one COMMENTED activity per thread with the replies nested in the root,
// and the reviewers of the PR payload. It answers any request it does not
// know with an error and fails the test.
type ctBBS struct {
	t  *testing.T
	pr contract.Spec

	mu     sync.Mutex
	roots  []*ctComment
	byID   map[int64]*ctComment
	nextID int64
}

func newCtBBS(t *testing.T, pr contract.Spec) *ctBBS {
	b := &ctBBS{t: t, pr: pr, byID: map[int64]*ctComment{}, nextID: ctFirstID}
	for _, th := range pr.Threads {
		root := &ctComment{c: th.Comments[0], resolved: th.Resolved}
		if th.Kind == provider.ThreadInline {
			root.anchor = ctAnchor(pr.Files, th.Path, th.Line)
		}
		b.add(root, nil)
		for _, c := range th.Comments[1:] {
			b.add(&ctComment{c: c}, root)
		}
	}
	return b
}

func (b *ctBBS) add(c, parent *ctComment) {
	b.byID[c.c.ID] = c
	if parent == nil {
		b.roots = append(b.roots, c)
		return
	}
	parent.replies = append(parent.replies, c)
}

// ctAnchor is the anchor of an inline thread on the new side of the diff.
func ctAnchor(files []contract.File, path string, line int) map[string]any {
	a := map[string]any{"path": path, "line": line, "lineType": "CONTEXT", "fileType": "TO",
		"diffType": "EFFECTIVE", "orphaned": false}
	for i := range files {
		if f := &files[i]; f.Path == path {
			if f.NewLines()[line] == provider.LineAdded {
				a["lineType"] = "ADDED"
			}
			if f.OldPath != "" {
				a["srcPath"] = f.OldPath
			}
		}
	}
	return a
}

func ctUser(u contract.User) map[string]any {
	return map[string]any{"id": u.ID, "name": u.Login, "displayName": u.DisplayName}
}

func ctMillis(t time.Time) int64 { return t.UnixMilli() }

func (c *ctComment) json() map[string]any {
	state := "OPEN"
	if c.resolved {
		state = "RESOLVED"
	}
	replies := []any{}
	for _, r := range c.replies {
		replies = append(replies, r.json())
	}
	m := map[string]any{
		"id": c.c.ID, "version": c.version, "text": c.c.Body, "author": ctUser(c.c.Author),
		"createdDate": ctMillis(c.c.Created), "updatedDate": ctMillis(c.c.Created),
		"state": state, "threadResolved": c.resolved, "comments": replies,
	}
	if c.anchor != nil {
		m["anchor"] = c.anchor
	}
	return m
}

func (b *ctBBS) prJSON() map[string]any {
	reviewers := []any{}
	for _, r := range b.pr.Reviewers {
		// Bitbucket Server cannot dismiss a verdict, and review-mcp casts
		// no Bitbucket review: neither has a counterpart here.
		if r.Dismissed || r.Own {
			continue
		}
		status, last := "UNAPPROVED", ""
		switch r.State {
		case provider.ReviewApproved:
			status, last = "APPROVED", b.pr.HeadSHA
		case provider.ReviewChangesRequested:
			status, last = "NEEDS_WORK", b.pr.HeadSHA
		}
		if r.Stale {
			last = ctOlderSHA
		}
		reviewers = append(reviewers, map[string]any{"user": ctUser(r.User), "status": status,
			"approved": status == "APPROVED", "lastReviewedCommit": last})
	}
	return map[string]any{
		"title": b.pr.Title, "description": b.pr.Description, "state": "OPEN", "draft": false,
		"author":    map[string]any{"user": ctUser(b.pr.Author)},
		"reviewers": reviewers,
		"fromRef":   map[string]any{"displayId": b.pr.SourceBranch, "latestCommit": b.pr.HeadSHA},
		"toRef":     map[string]any{"displayId": b.pr.TargetBranch, "latestCommit": ctTargetSHA},
		"links": map[string]any{"self": []any{map[string]any{
			"href": "https://bitbucket.example.com/projects/PROJ/repos/demo/pull-requests/7"}}},
	}
}

func (b *ctBBS) changesJSON() []any {
	types := map[provider.ChangeType]string{provider.ChangeAdded: "ADD", provider.ChangeDeleted: "DELETE",
		provider.ChangeRenamed: "MOVE", provider.ChangeModified: "MODIFY"}
	var out []any
	for _, f := range b.pr.Files {
		c := map[string]any{"type": types[f.Type], "path": map[string]any{"toString": f.Path}}
		if f.OldPath != "" {
			c["srcPath"] = map[string]any{"toString": f.OldPath}
		}
		out = append(out, c)
	}
	return out
}

func (b *ctBBS) activitiesJSON() []any {
	var out []any
	for i, root := range b.roots {
		a := map[string]any{"id": i + 1, "action": "COMMENTED", "commentAction": "ADDED",
			"createdDate": ctMillis(root.c.Created), "comment": root.json()}
		if root.anchor != nil {
			a["commentAnchor"] = root.anchor
		}
		out = append(out, a)
	}
	return out
}

func ctJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ctPaged serves a whole list as the only page of a start/limit resource.
func ctPaged(w http.ResponseWriter, r *http.Request, values []any) {
	if values == nil {
		values = []any{}
	}
	if s := r.URL.Query().Get("start"); s != "" && s != "0" {
		values = []any{}
	}
	ctJSON(w, http.StatusOK, map[string]any{"values": values, "size": len(values), "start": 0, "isLastPage": true})
}

func (b *ctBBS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, ok := strings.CutPrefix(r.URL.EscapedPath(), ctPrefix)
	if r.Header.Get("Authorization") != "Bearer "+b.pr.Env.Token || !ok {
		b.t.Errorf("request %s %s: unexpected Authorization header shape or context path", r.Method, r.URL.EscapedPath())
		contract.WriteError(w, http.StatusUnauthorized)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	get := r.Method == http.MethodGet
	switch {
	case get && path == ctProps:
		w.Header().Set("X-AUSERNAME", b.pr.TokenUser.Login)
		w.Header().Set("X-AUSERID", strconv.FormatInt(b.pr.TokenUser.ID, 10))
		ctJSON(w, http.StatusOK, map[string]any{"version": "8.9.0"})
	case get && path == ctPull:
		ctJSON(w, http.StatusOK, b.prJSON())
	case get && path == ctLatest+"/merge-base":
		ctJSON(w, http.StatusOK, map[string]any{"id": b.pr.BaseSHA})
	case get && path == ctPull+"/changes":
		ctPaged(w, r, b.changesJSON())
	case get && path == ctPull+"/activities":
		ctPaged(w, r, b.activitiesJSON())
	case get && path == ctPull+"/commits":
		ctPaged(w, r, nil)
	case get && path == ctPull+"/merge":
		ctJSON(w, http.StatusOK, map[string]any{"canMerge": true, "conflicted": false, "vetoes": []any{}})
	case get && strings.HasPrefix(path, ctRepo+"/raw/"):
		b.serveRaw(w, r, strings.TrimPrefix(path, ctRepo+"/raw/"))
	case r.Method == http.MethodPost && path == ctPull+"/comments":
		b.postComment(w, r)
	case strings.HasPrefix(path, ctPull+"/comments/"):
		b.serveComment(w, r, strings.TrimPrefix(path, ctPull+"/comments/"))
	default:
		b.t.Errorf("unexpected request %s %s", r.Method, path)
		contract.WriteError(w, http.StatusNotFound)
	}
}

// serveRaw answers a raw content request; content the Spec does not have
// is a plain 404.
func (b *ctBBS) serveRaw(w http.ResponseWriter, r *http.Request, escaped string) {
	p, err := url.PathUnescape(escaped)
	if err != nil {
		contract.WriteError(w, http.StatusBadRequest)
		return
	}
	sha := r.URL.Query().Get("at")
	for _, f := range b.pr.Files {
		switch {
		case f.Type != provider.ChangeAdded && sha == b.pr.BaseSHA && p == f.BasePath():
			_, _ = io.WriteString(w, f.Base)
			return
		case f.Type != provider.ChangeDeleted && sha == b.pr.HeadSHA && p == f.Path:
			_, _ = io.WriteString(w, f.Head)
			return
		}
	}
	contract.WriteError(w, http.StatusNotFound)
}

// postComment creates a general comment, a reply (parent) or an inline
// comment (anchor). An anchor outside the diff, or with the wrong line type
// or source path, is refused.
func (b *ctBBS) postComment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text   string `json:"text"`
		Parent *struct {
			ID int64 `json:"id"`
		} `json:"parent"`
		Anchor *struct {
			DiffType string `json:"diffType"`
			Path     string `json:"path"`
			SrcPath  string `json:"srcPath"`
			Line     int    `json:"line"`
			LineType string `json:"lineType"`
			FileType string `json:"fileType"`
		} `json:"anchor"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Text) == "" {
		contract.WriteError(w, http.StatusBadRequest)
		return
	}
	c := &ctComment{c: contract.Comment{ID: b.nextID, Author: b.pr.TokenUser, Body: in.Text, Created: time.Now().UTC()}}
	var parent *ctComment
	switch {
	case in.Parent != nil:
		if parent = b.byID[in.Parent.ID]; parent == nil {
			contract.WriteError(w, http.StatusNotFound)
			return
		}
	case in.Anchor != nil:
		a := in.Anchor
		want := ctAnchor(b.pr.Files, a.Path, a.Line)
		lt, inDiff := provider.LineType(""), false
		for i := range b.pr.Files {
			if f := &b.pr.Files[i]; f.Path == a.Path && !f.Binary {
				lt, inDiff = f.NewLines()[a.Line], true
			}
		}
		if !inDiff || lt == "" || a.FileType != "TO" || a.DiffType != "EFFECTIVE" ||
			a.LineType != want["lineType"] || a.SrcPath != ctString(want["srcPath"]) {
			contract.WriteError(w, http.StatusBadRequest)
			return
		}
		c.anchor = want
	}
	b.nextID++
	b.add(c, parent)
	ctJSON(w, http.StatusCreated, c.json())
}

func ctString(v any) string {
	s, _ := v.(string)
	return s
}

// serveComment answers GET and PUT of one comment; a PUT must carry the
// current version.
func (b *ctBBS) serveComment(w http.ResponseWriter, r *http.Request, id string) {
	n, err := strconv.ParseInt(id, 10, 64)
	c := b.byID[n]
	if err != nil || c == nil {
		contract.WriteError(w, http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ctJSON(w, http.StatusOK, c.json())
	case http.MethodPut:
		var in struct {
			Text    string `json:"text"`
			Version *int   `json:"version"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Version == nil {
			contract.WriteError(w, http.StatusBadRequest)
			return
		}
		if *in.Version != c.version {
			contract.WriteError(w, http.StatusConflict)
			return
		}
		c.c.Body, c.version = in.Text, c.version+1
		ctJSON(w, http.StatusOK, c.json())
	default:
		contract.WriteError(w, http.StatusMethodNotAllowed)
	}
}
