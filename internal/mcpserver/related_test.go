package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// Content of the related merge request of the fake GitLab host: an API
// description and an ordinary file, and markers that may reach a prompt but
// never a log.
const (
	relatedTitleMarker = "RELATED-TITLE-MARKER-5a1e"
	relatedDiffMarker  = "RELATED-DIFF-MARKER-77c0"
	relatedHead        = "1111111111111111111111111111111111111111"
)

// fakeGitLabHost serves merge request 3 of grp/svc: the merge request and
// its diffs; every other path is 404. It records each request with the
// value of its PRIVATE-TOKEN header.
type fakeGitLabHost struct {
	srv   *httptest.Server
	mu    sync.Mutex
	reqs  []string
	auths []string
	files []map[string]any
}

func newFakeGitLabHost(t *testing.T) *fakeGitLabHost {
	t.Helper()
	f := &fakeGitLabHost{files: []map[string]any{
		{"old_path": "api/openapi.yaml", "new_path": "api/openapi.yaml", "new_file": true,
			"diff": "@@ -0,0 +1,2 @@\n+openapi: 3.0.0\n+# " + relatedDiffMarker + "\n"},
		{"old_path": "src/main.go", "new_path": "src/main.go",
			"diff": "@@ -1,2 +1,2 @@\n package main\n-var a = 1\n+var a = 2\n"},
	}}
	const mr = "/api/v4/projects/grp%2Fsvc/merge_requests/3"
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.EscapedPath())
		f.auths = append(f.auths, r.Header.Get("PRIVATE-TOKEN"))
		f.mu.Unlock()
		if r.Header.Get("PRIVATE-TOKEN") != fakeGitLab {
			http.Error(w, "401", http.StatusUnauthorized)
			return
		}
		writeJ := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		switch r.URL.EscapedPath() {
		case mr:
			writeJ(map[string]any{
				"title": "Rename the field " + relatedTitleMarker, "state": "opened", "target_branch": "main",
				"source_branch": "rename", "sha": relatedHead, "web_url": f.srv.URL + "/grp/svc/-/merge_requests/3",
				"diff_refs": map[string]any{"base_sha": "2222222222222222222222222222222222222222", "head_sha": relatedHead,
					"start_sha": "2222222222222222222222222222222222222222"},
				"author": map[string]any{"username": "bob"},
			})
		case mr + "/diffs":
			writeJ(f.files)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitLabHost) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

// countingHost is a host that is not configured: it only counts requests.
func countingHost(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		http.NotFound(w, nil)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// relatedEnv is reviewEnv with the fake GitLab host configured next to the
// fake Gitea host, each with its own token.
func relatedEnv(g *fakeServer, gl *fakeGitLabHost, l *fakeLLMHost) map[string]string {
	env := reviewEnv(g, l)
	env["REVIEW_MCP_GITLAB_BASE_URL"] = gl.srv.URL
	return env
}

// TestRelatedAcrossProviders: the primary pull request on Gitea and a
// related merge request on GitLab are each read with their own provider's
// token; a related URL on a host that is not configured never causes a
// request; the logs carry no URL path, title or diff of a related item.
// [canary]
func TestRelatedAcrossProviders(t *testing.T) {
	g, gl, l := newFakeGiteaHost(t), newFakeGitLabHost(t), newFakeLLMHost(t, 200, goodAnswer)
	unconfigured, hits := countingHost(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cs := connect(t, realDeps(relatedEnv(g, gl, l), logger))
	glURL, noURL := gl.srv.URL+"/grp/svc/-/merge_requests/3", unconfigured.URL+"/grp/svc/-/merge_requests/9"
	out := callValid(t, cs, outputSchemas(t, cs), "pr_review", map[string]any{"pr_url": reviewPRURL(g),
		"related": []any{glURL + "?tab=changes", noURL}})
	cov, _ := out["coverage"].(map[string]any)
	rel := list(t, cov, "related")
	if len(rel) != 2 {
		t.Fatalf("coverage.related %v", rel)
	}
	first, _ := rel[0].(map[string]any)
	second, _ := rel[1].(map[string]any)
	if first["url"] != glURL || first["status"] != "full" || first["head_sha"] != relatedHead ||
		first["files_included"] != float64(2) || second["status"] != "failed" || second["reason"] != "url_not_configured" {
		t.Errorf("coverage.related %v", rel)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the host that is not configured got %d requests", n)
	}
	got := gl.requests()
	if len(got) < 2 || got[0] != "GET /api/v4/projects/grp%2Fsvc/merge_requests/3" ||
		got[1] != "GET /api/v4/projects/grp%2Fsvc/merge_requests/3/diffs" {
		t.Errorf("GitLab requests %q, want the merge request then its diffs", got)
	}
	for i, a := range gl.auths {
		if a != fakeGitLab {
			t.Errorf("GitLab request %d carried %q, not the GitLab token", i, a)
		}
	}
	for _, a := range g.authHeaders() {
		if strings.Contains(a, fakeGitLab) {
			t.Error("the GitLab token was sent to Gitea")
		}
	}
	text := logs.String()
	for _, leak := range []string{"grp/svc", "grp%2Fsvc/merge_requests/9", relatedTitleMarker, relatedDiffMarker} {
		if strings.Contains(text, leak) {
			t.Errorf("the logs carry %q", leak)
		}
	}
	for _, want := range []string{"index=0 kind=gitlab status=read", "index=1 status=failed reason=url_not_configured"} {
		if !strings.Contains(text, want) {
			t.Errorf("the logs lack %q", want)
		}
	}
}

// TestRelatedServeModeWithoutTheToken: in serve mode a related pull request
// on a provider the caller sent no token for is credentials_missing, gets no
// request, and the review still runs.
func TestRelatedServeModeWithoutTheToken(t *testing.T) {
	g, gl, l := newFakeGiteaHost(t), newFakeGitLabHost(t), newFakeLLMHost(t, 200, goodAnswer)
	env := relatedEnv(g, gl, l)
	for k := range env {
		if strings.HasSuffix(k, "_TOKEN") || k == "REVIEW_MCP_LLM_API_KEY" {
			delete(env, k)
		}
	}
	cfg, _, err := config.LoadWith(config.MemSource{Env: env}, config.LoadOptions{Mode: config.ModeServe})
	if err != nil {
		t.Fatal(err)
	}
	// The caller sent the Gitea token and the LLM key (ConfigFor puts them
	// into the call's configuration), not the GitLab one.
	cfg.Secrets.GiteaToken, cfg.Secrets.LLMAPIKey = config.NewSecret(fakeGitea), config.NewSecret(fakeLLMKey)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	resolver := tools.RequireCredentials(wiring.NewResolver(cfg, logger), cfg, true)
	call, err := tools.PreparePRReview(tools.ReviewDeps{Config: cfg, Resolver: resolver, NewLLM: wiring.NewLLM, Logger: logger},
		tools.PRReviewArgs{PRURL: reviewPRURL(g), Related: []string{gl.srv.URL + "/grp/svc/-/merge_requests/3"}})
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := call.Run(context.Background(), nil)
	if err != nil || res == nil || res.Review == nil {
		t.Fatalf("the review did not run: %v", err)
	}
	if n := len(gl.requests()); n != 0 {
		t.Errorf("GitLab got %d requests without the caller's token", n)
	}
	if !strings.Contains(logs.String(), "index=0 kind=gitlab status=failed reason=credentials_missing") &&
		!strings.Contains(logs.String(), "index=0 status=failed reason=credentials_missing") {
		t.Errorf("the logs lack the credentials_missing outcome:\n%s", logs.String())
	}
}

// TestRelatedArgumentErrors: the related argument is refused before any
// request, with fixed sentences that repeat no value.
func TestRelatedArgumentErrors(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"related": []any{"https://git.example/1", "https://git.example/2", "https://git.example/3",
			"https://git.example/4", "https://git.example/5"}}, "related must have at most 4 items"},
		{map[string]any{"related": []any{"https://git.example/1", "https://u:SECRET-VALUE@git.example/2"}},
			"related[1] must be an http or https pull request URL without credentials"},
		{map[string]any{"related": []any{"https://git.example/1"}, "focus": "security"},
			"related cannot be combined with focus or findings"},
		{map[string]any{"related": []any{"https://git.example/1"}, "findings": []any{}, "publish": true},
			"related cannot be combined with focus or findings"},
	}
	before := len(g.recorded())
	for _, c := range cases {
		c.args["pr_url"] = reviewPRURL(g)
		res := callTool(t, cs, "pr_review", c.args)
		if !res.IsError || textOf(t, res) != c.want {
			t.Errorf("%v: %v %q, want %q", c.args, res.IsError, textOf(t, res), c.want)
		}
	}
	if n := len(g.recorded()) - before; n != 0 {
		t.Errorf("%d requests for refused arguments", n)
	}
}

// TestAskRelatedValidates: pr_ask reads a related merge request on another
// provider with that provider's token and reports coverage.related; its
// results validate against the declared output schema in every case
// (without the argument, read, a host that is not configured, and off).
func TestAskRelatedValidates(t *testing.T) {
	g, gl, l := newFakeGiteaHost(t), newFakeGitLabHost(t), newFakeLLMHost(t, 200, "It matches.")
	unconfigured, hits := countingHost(t)
	cs := connect(t, realDeps(relatedEnv(g, gl, l), nil))
	schemas := outputSchemas(t, cs)
	glURL, noURL := gl.srv.URL+"/grp/svc/-/merge_requests/3", unconfigured.URL+"/grp/svc/-/merge_requests/9"

	out := callValid(t, cs, schemas, "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": "Does it match?"})
	if rel := list(t, out["coverage"].(map[string]any), "related"); len(rel) != 0 {
		t.Errorf("without the argument: %v", rel)
	}

	out = callValid(t, cs, schemas, "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": "Does it match?",
		"related": []any{glURL, noURL}})
	rel := list(t, out["coverage"].(map[string]any), "related")
	first, _ := rel[0].(map[string]any)
	second, _ := rel[1].(map[string]any)
	if len(rel) != 2 || first["status"] != "full" || first["head_sha"] != relatedHead || second["reason"] != "url_not_configured" {
		t.Errorf("coverage.related %v", rel)
	}
	if hits.Load() != 0 {
		t.Error("the host that is not configured got a request")
	}
	if !strings.Contains(l.lastBody(), relatedDiffMarker) {
		t.Error("the related diff did not reach the model")
	}

	env := relatedEnv(g, gl, l)
	env["REVIEW_MCP_RELATED_MAX_TOKENS"] = "0"
	cs = connect(t, realDeps(env, nil))
	out = callValid(t, cs, outputSchemas(t, cs), "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": "Does it match?",
		"related": []any{glURL}})
	first, _ = list(t, out["coverage"].(map[string]any), "related")[0].(map[string]any)
	if first["status"] != "not_placed" || first["reason"] != "off" {
		t.Errorf("off: %v", first)
	}
}

// lastBody is the body of the last request the fake server recorded.
func (f *fakeServer) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return ""
	}
	return f.bodies[len(f.bodies)-1]
}
