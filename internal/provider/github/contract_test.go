package github_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
	"github.com/nevzatcirak/review-mcp/internal/provider/github"
)

// TestContract runs the provider contract suite against a fake GitHub
// Enterprise Server API that serves each contract.Spec. The read path is
// WP-2j; the cases of the later work packages are declared pending.
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
		Pending: map[string]string{
			"capabilities":              "WP-2k",
			"threads":                   "WP-2k",
			"reply_in_thread":           "WP-2k",
			"general_reply":             "WP-2k",
			"edit_ownership":            "WP-2k",
			"inline_anchoring":          "WP-2l",
			"review_status":             "WP-2m",
			"update_pull_request":       "WP-2m",
			"errors/ListThreads":        "WP-2k",
			"errors/PostComment":        "WP-2k",
			"errors/ReplyToComment":     "WP-2k",
			"errors/EditComment":        "WP-2k",
			"errors/PostInlineComments": "WP-2l",
			"errors/UpdatePullRequest":  "WP-2m",
			"errors/GetReviewStatus":    "WP-2m",
		},
	}
}

func (ctFixture) Serve(t *testing.T, pr contract.Spec) (provider.Provider, provider.PRRef) {
	t.Helper()
	cfg := config.Defaults()
	// The web base is the fake's origin; the API base is derived from it
	// as for GitHub Enterprise Server ({base}/api/v3).
	cfg.GitHub.BaseURL = contract.StartServer(t, pr, &ctGitHub{t: t, pr: pr})
	cfg.Secrets.GitHubToken = config.NewSecret(pr.Env.Token)
	cfg.Diff.MaxFilesFullContent = pr.Env.MaxFiles
	cfg.Diff.MaxFileBytes = pr.Env.MaxFileBytes
	p, err := github.NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, provider.PRRef{Kind: provider.KindGitHub, Namespace: "octo", Repo: "demo", Number: 7}
}

// ctGitHub is a fake GitHub REST API v3 that serves one contract.Spec. It
// checks the headers of every request and answers any request it does not
// know with an error that fails the test.
type ctGitHub struct {
	t  *testing.T
	pr contract.Spec
	mu sync.Mutex
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

func (g *ctGitHub) prJSON() map[string]any {
	return map[string]any{
		"number": 7, "title": g.pr.Title, "body": g.pr.Description, "state": "open", "draft": g.pr.Draft,
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
		g.t.Errorf("unexpected request %s %s", r.Method, path)
		contract.WriteError(w, http.StatusMethodNotAllowed)
		return
	}
	switch {
	case path == ctAPI+"/user":
		ctJSON(w, http.StatusOK, ctUser(g.pr.TokenUser))
	case path == ctPull:
		ctJSON(w, http.StatusOK, g.prJSON())
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
