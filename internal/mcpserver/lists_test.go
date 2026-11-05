package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/credentials"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// Distinctive values of the list tests: tokens for all four providers, and
// titles and author names that must reach the result and never the logs.
const (
	listGiteaToken = "LISTTOKEN-gitea-8c41f0"  //nolint:gosec // synthetic test value
	listBBSToken   = "LISTTOKEN-bbs-8c41f0"    //nolint:gosec // synthetic test value
	listGitHubTok  = "LISTTOKEN-github-8c41f0" //nolint:gosec // synthetic test value
	listGitLabTok  = "LISTTOKEN-gitlab-8c41f0" //nolint:gosec // synthetic test value

	listTitleMarker  = "LISTTITLE-8c41f0"
	listAuthorMarker = "LISTAUTHOR-8c41f0"
)

var listTokens = []string{listGiteaToken, listBBSToken, listGitHubTok, listGitLabTok}

// listHost is one fixture server for the four providers' list endpoints
// (placeholder paths below one loopback listener). bodies maps a provider
// kind and a role to the items the host answers with; status forces an HTTP
// status on a kind's list endpoint.
type listHost struct {
	srv *httptest.Server

	mu     sync.Mutex
	bodies map[string]map[string][]string // kind -> role -> items (raw JSON)
	status map[string]int                 // kind -> forced status
	hits   []listHit
}

type listHit struct{ kind, path, auth string }

func (h *listHost) hitsOf(kind string) []listHit {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []listHit
	for _, x := range h.hits {
		if x.kind == kind {
			out = append(out, x)
		}
	}
	return out
}

func (h *listHost) set(kind, role string, items ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.bodies[kind] == nil {
		h.bodies[kind] = map[string][]string{}
	}
	h.bodies[kind][role] = items
}

func (h *listHost) items(kind, role string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.bodies[kind][role], ",")
}

func newListHost(t *testing.T) *listHost {
	t.Helper()
	h := &listHost{bodies: map[string]map[string][]string{}, status: map[string]int{}}
	h.srv = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *listHost) serve(w http.ResponseWriter, r *http.Request) {
	p, q := r.URL.Path, r.URL.Query()
	kind := ""
	switch {
	case strings.HasPrefix(p, "/gitea/"):
		kind = "gitea"
	case strings.HasPrefix(p, "/bb/"):
		kind = "bitbucket_server"
	case strings.HasPrefix(p, "/ghapi/"):
		kind = "github"
	case strings.HasPrefix(p, "/gl/"):
		kind = "gitlab"
	}
	auth := r.Header.Get("Authorization") + "|" + r.Header.Get("PRIVATE-TOKEN")
	h.mu.Lock()
	h.hits = append(h.hits, listHit{kind, p, auth})
	forced := h.status[kind]
	h.mu.Unlock()
	reply := func(body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
	if forced != 0 && !strings.HasSuffix(p, "/version") && !strings.HasSuffix(p, "/user") {
		http.Error(w, "forced", forced)
		return
	}
	switch p {
	case "/gitea/api/v1/version":
		reply(`{"version":"1.21.11"}`)
	case "/gitea/api/v1/repos/issues/search":
		role := map[bool]string{true: "reviewer"}[q.Get("review_requested") == "true"]
		if q.Get("assigned") == "true" {
			role = "assignee"
		}
		if q.Get("created") == "true" {
			role = "author"
		}
		reply("[" + h.items("gitea", role) + "]")
	case "/bb/rest/api/1.0/dashboard/pull-requests":
		w.Header().Set("X-AUSERNAME", "me")
		w.Header().Set("X-AUSERID", "5")
		reply(`{"isLastPage":true,"values":[` + h.items("bitbucket_server", strings.ToLower(q.Get("role"))) + `]}`)
	case "/ghapi/search/issues":
		role := "author"
		switch qs := q.Get("q"); {
		case strings.Contains(qs, "review-requested:"):
			role = "reviewer"
		case strings.Contains(qs, "assignee:"):
			role = "assignee"
		}
		reply(`{"total_count":0,"incomplete_results":false,"items":[` + h.items("github", role) + `]}`)
	case "/ghapi/user":
		reply(`{"login":"selfy"}`)
	case "/gl/api/v4/user":
		reply(`{"id":9,"username":"me"}`)
	case "/gl/api/v4/merge_requests":
		role := "author"
		switch {
		case q.Get("reviewer_id") != "":
			role = "reviewer"
		case q.Get("scope") == "assigned_to_me":
			role = "assignee"
		}
		reply("[" + h.items("gitlab", role) + "]")
	default:
		http.NotFound(w, r)
	}
}

// env is a stdio configuration whose four providers are this host's.
func (h *listHost) env() map[string]string {
	env := validEnv()
	env["REVIEW_MCP_GITEA_BASE_URL"] = h.srv.URL + "/gitea"
	env["REVIEW_MCP_GITEA_TOKEN"] = listGiteaToken
	env["REVIEW_MCP_BITBUCKET_SERVER_BASE_URL"] = h.srv.URL + "/bb"
	env["REVIEW_MCP_BITBUCKET_SERVER_TOKEN"] = listBBSToken
	env["REVIEW_MCP_GITHUB_BASE_URL"] = h.srv.URL + "/gh"
	env["REVIEW_MCP_GITHUB_API_URL"] = h.srv.URL + "/ghapi"
	env["REVIEW_MCP_GITHUB_SELF_LOGIN"] = "selfy"
	env["REVIEW_MCP_GITHUB_TOKEN"] = listGitHubTok
	env["REVIEW_MCP_GITLAB_BASE_URL"] = h.srv.URL + "/gl"
	env["REVIEW_MCP_GITLAB_TOKEN"] = listGitLabTok
	return env
}

// Items of the fixture host, one per provider.

func giteaItem(n int, title, author, updated, draft string) string {
	pr := `{}`
	if draft != "" {
		pr = `{"draft":` + draft + `}`
	}
	return fmt.Sprintf(`{"number":%d,"title":%q,"updated_at":%q,"user":{"login":%q},"repository":{"owner":"octo","name":"demo"},"pull_request":%s}`,
		n, title, updated, author, pr)
}

func bbItem(id int, title, author string, updated time.Time, status string) string {
	return fmt.Sprintf(`{"id":%d,"title":%q,"draft":false,"updatedDate":%d,"toRef":{"displayId":"main","repository":{"slug":"web","project":{"key":"PRJ"}}},`+
		`"author":{"user":{"name":%q}},"reviewers":[{"user":{"name":"me","id":5},"status":%q}]}`,
		id, title, updated.UnixMilli(), author, status)
}

func (h *listHost) ghItem(n int, title, author, updated string) string {
	return fmt.Sprintf(`{"number":%d,"title":%q,"updated_at":%q,"user":{"login":%q},"repository_url":%q,"pull_request":{}}`,
		n, title, updated, author, h.srv.URL+"/ghapi/repos/octo/api")
}

func glItem(iid int, title, author, updated string) string {
	return fmt.Sprintf(`{"iid":%d,"title":%q,"draft":true,"updated_at":%q,"target_branch":"main","references":{"full":"grp/sub/proj!%d"},"author":{"username":%q}}`,
		iid, title, updated, iid, author)
}

// logBuffer is a goroutine-safe log sink.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func debugLogger(l *logBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func listDeps(h *listHost, logger *slog.Logger) Deps {
	d := depsFor(h.env(), logger)
	d.NewResolver = wiring.NewResolver
	return d
}

// listResult is the decoded structured result of a list tool.
type listResult struct {
	Role  string `json:"role"`
	Items []struct {
		Provider     string   `json:"provider"`
		URL          string   `json:"url"`
		Namespace    string   `json:"namespace"`
		Repo         string   `json:"repo"`
		Number       int64    `json:"number"`
		Title        string   `json:"title"`
		Author       string   `json:"author"`
		TargetBranch *string  `json:"target_branch"`
		Draft        *bool    `json:"draft"`
		UpdatedAt    string   `json:"updated_at"`
		Roles        []string `json:"roles"`
		MyStatus     *string  `json:"my_status"`
	} `json:"items"`
	Providers []struct {
		Kind    string  `json:"kind"`
		Status  string  `json:"status"`
		Reason  *string `json:"reason"`
		Count   int     `json:"count"`
		HasMore bool    `json:"has_more"`
	} `json:"providers"`
	Notes []string `json:"notes"`
}

func (r listResult) status(kind string) (status string, reason *string, count int) {
	for _, p := range r.Providers {
		if p.Kind == kind {
			return p.Status, p.Reason, p.Count
		}
	}
	return "", nil, 0
}

// callList calls a list tool, validates its structured result against the
// declared output schema and returns it decoded with the markdown.
func callList(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (listResult, string) {
	t.Helper()
	out := callValid(t, cs, outputSchemas(t, cs), tool, args)
	raw, _ := json.Marshal(out)
	var r listResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	res := callTool(t, cs, tool, args)
	return r, textOf(t, res)
}

// TestListToolsDeclared: both tools are registered with the spec's
// descriptions, the read-only annotations, and input and output schemas.
func TestListToolsDeclared(t *testing.T) {
	cs := connect(t, depsFor(validEnv(), nil))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"pr_list_review_requests": prListReviewRequestsDescription, "pr_list_authored": prListAuthoredDescription}
	if prListReviewRequestsDescription != "Lists the open pull requests that ask you (the token's user) for a review on the configured providers, newest update first; with include_assigned also those assigned to you. Use an item's url with pr_review, pr_info or pr_ask. Read-only; no LLM call." ||
		prListAuthoredDescription != "Lists the open pull requests you (the token's user) opened on the configured providers, newest update first. Use an item's url with pr_info to see their reviewers and CI. Read-only; no LLM call." {
		t.Error("a description differs from the spec")
	}
	found := 0
	for _, tl := range list.Tools {
		d, ok := want[tl.Name]
		if !ok {
			continue
		}
		found++
		a := tl.Annotations
		if tl.Description != d || a == nil || !a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint ||
			a.OpenWorldHint == nil || !*a.OpenWorldHint {
			t.Errorf("%s: description %q, annotations %+v", tl.Name, tl.Description, a)
		}
		in, _ := json.Marshal(tl.InputSchema)
		var s struct {
			Required   []string                  `json:"required"`
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(in, &s); err != nil {
			t.Fatal(err)
		}
		wantProps := 2
		if tl.Name == "pr_list_review_requests" {
			wantProps = 3
		}
		if len(s.Required) != 0 || len(s.Properties) != wantProps || s.Properties["provider"]["enum"] == nil {
			t.Errorf("%s: input schema %s", tl.Name, in)
		}
		for name, p := range s.Properties {
			if p["description"] == "" {
				t.Errorf("%s.%s has no description", tl.Name, name)
			}
		}
		if tl.OutputSchema == nil {
			t.Errorf("%s has no output schema", tl.Name)
		}
	}
	if found != 2 {
		t.Fatalf("found %d list tools", found)
	}
	if !strings.Contains(cs.InitializeResult().Instructions, "review-mcp reviews, describes and discusses pull requests on Gitea, Bitbucket Server, GitHub and GitLab with a configured LLM. To find pull requests to work on, call pr_list_review_requests or pr_list_authored.\n\n") {
		t.Errorf("instructions:\n%s", cs.InitializeResult().Instructions)
	}
}

// TestListResultsValidate: the structured results validate against the
// declared output schema: normal, empty, one provider failed,
// include_assigned merged, the Bitbucket note, every nullable field null.
func TestListResultsValidate(t *testing.T) {
	old := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	t.Run("normal", func(t *testing.T) {
		h := newListHost(t)
		h.set("gitea", "reviewer", giteaItem(7, "gitea title", "alice", "2026-11-04T10:00:00Z", "true"))
		h.set("bitbucket_server", "reviewer", bbItem(3, "bbs title", "bob", old, "NEEDS_WORK"))
		h.set("github", "reviewer", h.ghItem(12, "gh title", "carol", "2026-11-04T11:00:00Z"))
		h.set("gitlab", "reviewer", glItem(4, "gl title", "dave", "2026-11-02T08:00:00Z"))
		cs := connect(t, listDeps(h, nil))
		r, md := callList(t, cs, "pr_list_review_requests", nil)
		if r.Role != "review_requests" || len(r.Items) != 4 || len(r.Providers) != 4 {
			t.Fatalf("result = %+v", r)
		}
		var order []string
		for _, it := range r.Items {
			order = append(order, it.Provider)
		}
		if want := []string{"github", "gitea", "bitbucket_server", "gitlab"}; !slices.Equal(order, want) {
			t.Errorf("order = %v, want newest update first %v", order, want)
		}
		for _, p := range r.Providers {
			if p.Status != "ok" || p.Reason != nil || p.Count != 1 || p.HasMore {
				t.Errorf("provider %+v", p)
			}
		}
		if !slices.Contains(r.Notes, tools.NoteGitHubReviewRequests) {
			t.Errorf("notes = %q", r.Notes)
		}
		if !strings.Contains(md, "`gh title`") || !strings.Contains(md, "[PRJ/web!3](") || !strings.Contains(md, "[grp/sub/proj!4](") {
			t.Errorf("markdown:\n%s", md)
		}
		// Each provider received its own token, and the built URLs are the
		// configured hosts' pull request URLs.
		for kind, tok := range map[string]string{"gitea": listGiteaToken, "bitbucket_server": listBBSToken, "github": listGitHubTok, "gitlab": listGitLabTok} {
			hits := h.hitsOf(kind)
			if len(hits) == 0 || !strings.Contains(hits[len(hits)-1].auth, tok) {
				t.Errorf("%s hits %+v lack its token", kind, hits)
			}
		}
		for _, it := range r.Items {
			if it.Provider == "github" && it.URL != h.srv.URL+"/gh/octo/api/pull/12" {
				t.Errorf("GitHub URL = %q", it.URL)
			}
			if it.Provider == "bitbucket_server" && (it.MyStatus == nil || *it.MyStatus != "needs_work" || it.TargetBranch == nil) {
				t.Errorf("Bitbucket item = %+v", it)
			}
		}
	})

	t.Run("empty", func(t *testing.T) {
		h := newListHost(t)
		cs := connect(t, listDeps(h, nil))
		r, md := callList(t, cs, "pr_list_authored", nil)
		if r.Role != "authored" || r.Items == nil || len(r.Items) != 0 || len(r.Providers) != 4 {
			t.Fatalf("result = %+v", r)
		}
		if !strings.Contains(md, "You have no open pull requests.") {
			t.Errorf("markdown:\n%s", md)
		}
	})

	t.Run("one provider failed", func(t *testing.T) {
		h := newListHost(t)
		h.set("gitea", "reviewer", giteaItem(7, "gitea title", "alice", "2026-11-04T10:00:00Z", "false"))
		h.status["gitlab"] = http.StatusForbidden
		cs := connect(t, listDeps(h, nil))
		r, md := callList(t, cs, "pr_list_review_requests", map[string]any{"limit": 10})
		st, reason, _ := r.status("gitlab")
		if st != "failed" || reason == nil || *reason != "forbidden" || len(r.Items) != 1 {
			t.Fatalf("gitlab = %s %v, items %d", st, reason, len(r.Items))
		}
		if !slices.Contains(r.Notes, fmt.Sprintf(tools.NoteListProviderFailedFormat, "GitLab", "forbidden")) ||
			!strings.Contains(md, "`gitlab`: failed (forbidden)") {
			t.Errorf("notes %q\n%s", r.Notes, md)
		}
	})

	t.Run("include_assigned merged", func(t *testing.T) {
		h := newListHost(t)
		both := giteaItem(7, "both", "alice", "2026-11-04T10:00:00Z", "false")
		h.set("gitea", "reviewer", both)
		h.set("gitea", "assignee", both, giteaItem(8, "assigned", "bob", "2026-11-03T10:00:00Z", "false"))
		h.set("bitbucket_server", "reviewer", bbItem(3, "bbs title", "bob", old, "APPROVED"))
		cs := connect(t, listDeps(h, nil))
		r, _ := callList(t, cs, "pr_list_review_requests", map[string]any{"provider": "gitea", "include_assigned": true})
		if len(r.Items) != 2 || !slices.Equal(r.Items[0].Roles, []string{"reviewer", "assignee"}) ||
			!slices.Equal(r.Items[1].Roles, []string{"assignee"}) {
			t.Fatalf("items = %+v", r.Items)
		}
		if len(r.Providers) != 1 || r.Providers[0].Count != 2 {
			t.Errorf("providers = %+v", r.Providers)
		}
	})

	t.Run("bitbucket note", func(t *testing.T) {
		h := newListHost(t)
		h.set("bitbucket_server", "reviewer", bbItem(3, "bbs title", "bob", old, "UNAPPROVED"))
		cs := connect(t, listDeps(h, nil))
		r, _ := callList(t, cs, "pr_list_review_requests", map[string]any{"provider": "bitbucket_server", "include_assigned": true})
		st, reason, _ := r.status("bitbucket_server")
		if st != "ok" || reason != nil || !slices.Contains(r.Notes, tools.NoteListNoAssignees) {
			t.Errorf("status %s %v, notes %q", st, reason, r.Notes)
		}
		for _, hit := range h.hitsOf("bitbucket_server") {
			if !strings.Contains(hit.path, "dashboard") {
				t.Errorf("unexpected request %q", hit.path)
			}
		}
	})

	t.Run("every nullable field null", func(t *testing.T) {
		h := newListHost(t)
		// Gitea without a draft flag: target_branch, draft and my_status
		// are all null.
		h.set("gitea", "author", giteaItem(7, "t", "alice", "2026-11-04T10:00:00Z", ""))
		cs := connect(t, listDeps(h, nil))
		r, _ := callList(t, cs, "pr_list_authored", map[string]any{"provider": "gitea"})
		if len(r.Items) != 1 || r.Items[0].TargetBranch != nil || r.Items[0].Draft != nil || r.Items[0].MyStatus != nil {
			t.Fatalf("item = %+v", r.Items)
		}
		if st, reason, _ := r.status("gitea"); st != "ok" || reason != nil {
			t.Errorf("status %s %v", st, reason)
		}
	})
}

// TestListArgumentsRefused: bad arguments are tool errors with fixed text,
// before any request.
func TestListArgumentsRefused(t *testing.T) {
	h := newListHost(t)
	env := h.env()
	delete(env, "REVIEW_MCP_GITHUB_BASE_URL")
	delete(env, "REVIEW_MCP_GITHUB_API_URL")
	delete(env, "REVIEW_MCP_GITHUB_SELF_LOGIN")
	delete(env, "REVIEW_MCP_GITHUB_TOKEN")
	d := depsFor(env, nil)
	d.NewResolver = wiring.NewResolver
	cs := connect(t, d)
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"limit zero":         {map[string]any{"limit": 0}, tools.ListLimitMessage},
		"limit too large":    {map[string]any{"limit": 101}, tools.ListLimitMessage},
		"provider not found": {map[string]any{"provider": "github"}, "the github provider is not configured; configured providers: gitea, bitbucket_server, gitlab"},
	} {
		res := callTool(t, cs, "pr_list_authored", tc.args)
		if !res.IsError || textOf(t, res) != tc.want {
			t.Errorf("%s: error %v, text %q, want %q", name, res.IsError, textOf(t, res), tc.want)
		}
	}
	if len(h.hits) != 0 {
		t.Errorf("a refused call sent %d requests", len(h.hits))
	}
}

// TestListLeaks: [canary l] with distinctive tokens for all four providers,
// the results of both tools (text and structured) and the debug logs
// contain none of them, and no title or author name reaches the logs; the
// same when providers fail.
func TestListLeaks(t *testing.T) {
	for _, failing := range []bool{false, true} {
		t.Run(fmt.Sprintf("failing=%v", failing), func(t *testing.T) {
			h := newListHost(t)
			h.set("gitea", "reviewer", giteaItem(7, listTitleMarker+"-gitea", listAuthorMarker+"-gitea", "2026-11-04T10:00:00Z", "false"))
			h.set("bitbucket_server", "reviewer", bbItem(3, listTitleMarker+"-bbs", listAuthorMarker+"-bbs", time.Now(), "UNAPPROVED"))
			h.set("github", "reviewer", h.ghItem(12, listTitleMarker+"-gh", listAuthorMarker+"-gh", "2026-11-04T11:00:00Z"))
			h.set("gitlab", "reviewer", glItem(4, listTitleMarker+"-gl", listAuthorMarker+"-gl", "2026-11-02T08:00:00Z"))
			h.set("gitea", "assignee", giteaItem(8, listTitleMarker+"-gitea2", listAuthorMarker+"-gitea2", "2026-11-04T09:00:00Z", "false"))
			h.set("gitea", "author", giteaItem(9, listTitleMarker+"-gitea3", listAuthorMarker+"-gitea3", "2026-11-04T08:00:00Z", "false"))
			if failing {
				h.status["gitlab"] = http.StatusUnauthorized
				h.status["github"] = http.StatusForbidden
			}
			var logs logBuffer
			cs := connect(t, listDeps(h, debugLogger(&logs)))

			var seen strings.Builder
			for _, call := range []struct {
				tool string
				args map[string]any
			}{
				{"pr_list_review_requests", map[string]any{"include_assigned": true}},
				{"pr_list_authored", nil},
			} {
				res := callTool(t, cs, call.tool, call.args)
				if res.IsError {
					t.Fatalf("%s: %s", call.tool, textOf(t, res))
				}
				raw, _ := json.Marshal(res.StructuredContent)
				seen.WriteString(textOf(t, res) + "\n" + string(raw) + "\n")
			}
			if !strings.Contains(seen.String(), listTitleMarker+"-gitea") {
				t.Fatalf("the result does not carry the titles:\n%s", seen.String())
			}
			log := logs.String()
			if !strings.Contains(log, "pull request list") {
				t.Errorf("the tools logged nothing:\n%s", log)
			}
			for _, tok := range listTokens {
				if strings.Contains(seen.String(), tok) {
					t.Errorf("a result contains the token %q", tok)
				}
				if strings.Contains(log, tok) {
					t.Errorf("the log contains the token %q:\n%s", tok, log)
				}
			}
			for _, s := range []string{listTitleMarker, listAuthorMarker} {
				if strings.Contains(log, s) {
					t.Errorf("the log contains %q:\n%s", s, log)
				}
			}
		})
	}
}

// headerTransport adds fixed headers to every request.
type headerTransport map[string]string

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// TestListServeNoToken: [canary k] in serve mode a provider whose token
// header the request lacks is no_token, nothing is built for it and no
// request reaches its fixture; the providers with a token are listed.
func TestListServeNoToken(t *testing.T) {
	h := newListHost(t)
	h.set("gitea", "author", giteaItem(7, "gitea title", "alice", "2026-11-04T10:00:00Z", "false"))
	h.set("github", "author", h.ghItem(12, "gh title", "carol", "2026-11-04T11:00:00Z"))
	h.set("gitlab", "author", glItem(4, "gl title", "dave", "2026-11-02T08:00:00Z"))

	env := h.env()
	delete(env, "REVIEW_MCP_GITHUB_SELF_LOGIN") // not allowed in serve mode: GET /user gives the login
	for _, k := range []string{"REVIEW_MCP_GITEA_TOKEN", "REVIEW_MCP_BITBUCKET_SERVER_TOKEN", "REVIEW_MCP_GITHUB_TOKEN", "REVIEW_MCP_GITLAB_TOKEN",
		"REVIEW_MCP_LLM_API_KEY"} {
		delete(env, k)
	}
	deps := serveDeps(t, env)
	deps.NewResolver = wiring.NewResolver
	srv := httptest.NewServer(NewHTTPHandler(deps))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	hdr := headerTransport{credentials.HeaderGiteaToken: listGiteaToken, credentials.HeaderGitLabToken: listGitLabTok}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: &http.Client{Transport: hdr}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	r, md := callList(t, cs, "pr_list_authored", nil)
	for _, kind := range []string{"bitbucket_server", "github"} {
		if st, reason, count := r.status(kind); st != "no_token" || reason != nil || count != 0 {
			t.Errorf("%s = %s %v %d, want no_token", kind, st, reason, count)
		}
		if hits := h.hitsOf(kind); len(hits) != 0 {
			t.Errorf("%s: %d requests reached the fixture: %+v", kind, len(hits), hits)
		}
	}
	for _, kind := range []string{"gitea", "gitlab"} {
		if st, _, count := r.status(kind); st != "ok" || count != 1 {
			t.Errorf("%s = %s %d", kind, st, count)
		}
	}
	if len(r.Items) != 2 || !strings.Contains(md, "`github`: no token in this request") {
		t.Errorf("items %d\n%s", len(r.Items), md)
	}
	if hits := h.hitsOf("gitea"); len(hits) == 0 || !strings.Contains(hits[0].auth, listGiteaToken) {
		t.Errorf("Gitea hits = %+v", hits)
	}
	// Naming a provider without a token is no_token too, not a refusal.
	r, _ = callList(t, cs, "pr_list_authored", map[string]any{"provider": "github"})
	if st, _, _ := r.status("github"); st != "no_token" || len(h.hitsOf("github")) != 0 {
		t.Errorf("github = %s, hits %d", st, len(h.hitsOf("github")))
	}
}
