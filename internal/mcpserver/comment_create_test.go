package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

const (
	// createMarker is in the body of a pr_comment_create call; threadMarker
	// is in a comment someone else wrote on the PR; inlineMarker and
	// overviewMarker are in the model's finding and security text, so they
	// land in the inline comment and the overview. They reach the provider
	// or the LLM as designed and never the logs or an error text.
	createMarker   = "CREATE-BODY-MARKER-8d2f60-never-log"
	threadMarker   = "THREAD-MARKER-8d2f60-never-log"
	inlineMarker   = "INLINE-MARKER-8d2f60-never-log"
	overviewMarker = "OVERVIEW-MARKER-8d2f60-never-log"
)

const (
	bbsRepo = "/rest/api/1.0/projects/PRJ/repos/demo"
	bbsPR   = bbsRepo + "/pull-requests/7"
)

// bbsFiles are the head contents of the fake Bitbucket PR. src/app.go has
// line 5 changed, so its hunk spans lines 2 to 8 (3 lines of context):
// 5 is an added line, 2 to 4 and 6 to 8 are context lines, 1, 9 and 10 are
// outside the diff.
func bbsLines(changed string) string {
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		if i == 5 && changed != "" {
			b.WriteString(changed + "\n")
			continue
		}
		b.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	return b.String()
}

type bbsPost struct {
	path string
	body map[string]any
}

// fakeBBSHost is a Bitbucket Server with one PR: src/app.go modified,
// src/renamed.go moved from src/old_name.go, and the comment endpoint.
type fakeBBSHost struct {
	srv  *httptest.Server
	hits atomic.Int64

	mu       sync.Mutex
	requests []string
	posts    []bbsPost
}

func newFakeBBSHost(t *testing.T) *fakeBBSHost {
	t.Helper()
	f := &fakeBBSHost{}
	raw := map[string]string{
		"headsha:src/app.go":       bbsLines("line 5 changed"),
		"mergesha:src/app.go":      bbsLines(""),
		"headsha:src/renamed.go":   "a\nb\nC\nd\n",
		"mergesha:src/old_name.go": "a\nb\nc\nd\n",
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+fakeBitbkt {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		writeJ := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		p := r.URL.Path
		switch {
		case p == "/rest/api/1.0/application-properties":
			writeJ(map[string]any{"version": "8.9.0"})
		case r.Method == "GET" && p == bbsPR:
			writeJ(map[string]any{"title": "Add feature", "state": "OPEN",
				"author":  map[string]any{"user": map[string]any{"name": "jdoe"}},
				"fromRef": map[string]any{"displayId": "feature", "latestCommit": "headsha"},
				"toRef":   map[string]any{"displayId": "main", "latestCommit": "targetsha"}})
		case p == "/rest/api/latest/projects/PRJ/repos/demo/pull-requests/7/merge-base":
			writeJ(map[string]any{"id": "mergesha"})
		case r.Method == "GET" && p == bbsPR+"/changes":
			chg := func(typ, path, src string) map[string]any {
				c := map[string]any{"type": typ, "path": map[string]any{"toString": path}}
				if src != "" {
					c["srcPath"] = map[string]any{"toString": src}
				}
				return c
			}
			writeJ(map[string]any{"isLastPage": true, "values": []any{
				chg("MODIFY", "src/app.go", ""), chg("MOVE", "src/renamed.go", "src/old_name.go")}})
		case r.Method == "GET" && strings.HasPrefix(p, bbsRepo+"/raw/"):
			if c, ok := raw[r.URL.Query().Get("at")+":"+strings.TrimPrefix(p, bbsRepo+"/raw/")]; ok {
				_, _ = io.WriteString(w, c)
				return
			}
			http.NotFound(w, r)
		case r.Method == "POST" && p == bbsPR+"/comments":
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			f.mu.Lock()
			f.posts = append(f.posts, bbsPost{p, body})
			n := len(f.posts)
			f.mu.Unlock()
			writeJ(map[string]any{"id": 700 + n})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBBSHost) prURL() string { return f.srv.URL + "/projects/PRJ/repos/demo/pull-requests/7" }

func (f *fakeBBSHost) postLog() []bbsPost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.posts)
}

func (f *fakeBBSHost) commentRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if strings.HasSuffix(r, "/comments") {
			out = append(out, r)
		}
	}
	return out
}

func bbsEnv(f *fakeBBSHost) map[string]string {
	env := validEnv()
	env["REVIEW_MCP_BITBUCKET_SERVER_BASE_URL"] = f.srv.URL
	env["REVIEW_MCP_LOG_LEVEL"] = "debug"
	return env
}

// commentEndpoints returns the requests a Gitea fake received on its
// comment or review endpoints.
func commentEndpoints(g *fakeServer) []string {
	var out []string
	for _, r := range g.requestLog() {
		if strings.Contains(r, "/comments") || strings.Contains(r, "/reviews") {
			out = append(out, r)
		}
	}
	return out
}

func TestPRCommentCreateGitea(t *testing.T) {
	const body = "Please rename this. " + createMarker
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))

	t.Run("pr-level", func(t *testing.T) {
		res := callTool(t, cs, "pr_comment_create", map[string]any{"pr_url": reviewPRURL(g), "body": body})
		if res.IsError {
			t.Fatalf("tool error: %s", textOf(t, res))
		}
		var got tools.PRCommentCreateResult
		decodeStructured(t, res, &got)
		if got.Inline || got.ID != "55" || got.URL != "https://your-gitea.example/octo/demo/pulls/7#issuecomment-55" {
			t.Errorf("result = %+v", got)
		}
		if want := "Comment posted on the pull request.\n\nComment id: `55`\n"; textOf(t, res) != want {
			t.Errorf("text = %q", textOf(t, res))
		}
		if c := g.comments[55]; c.body != body {
			t.Errorf("posted body = %q", c.body)
		}
	})

	// Line 2 is the added line of the diff, lines 1 and 3 are its context.
	for _, tc := range []struct {
		name string
		line int
	}{{"added line", 2}, {"context line before", 1}, {"context line after", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(g.reviews)
			res := callTool(t, cs, "pr_comment_create", map[string]any{
				"pr_url": reviewPRURL(g), "body": body, "file": "src/app.go", "line": tc.line})
			if res.IsError {
				t.Fatalf("tool error: %s", textOf(t, res))
			}
			var got tools.PRCommentCreateResult
			decodeStructured(t, res, &got)
			if !got.Inline || got.ID == "" || !strings.HasPrefix(got.URL, "https://your-gitea.example/octo/demo/pulls/7") {
				t.Errorf("result = %+v", got)
			}
			if !strings.HasPrefix(textOf(t, res), "Comment posted on the line.\n\n") {
				t.Errorf("text = %q", textOf(t, res))
			}
			if len(g.reviews) != before+1 {
				t.Fatalf("reviews = %d, want %d", len(g.reviews), before+1)
			}
			cm := g.reviews[before]["comments"].([]any)
			if len(cm) != 1 {
				t.Fatalf("review has %d comments, want 1", len(cm))
			}
			c := cm[0].(map[string]any)
			if c["path"] != "src/app.go" || c["position"] != float64(tc.line) || c["body"] != body {
				t.Errorf("review comment = %v", c)
			}
		})
	}
}

// TestPRCommentCreateRefusesOutOfDiffLine [canary]: a line that is not part
// of the diff, a file the PR does not change and a path that only differs
// in form are refused with the fixed sentence, and not one request reaches
// a comment or review endpoint: the comment is never posted at PR level
// instead.
func TestPRCommentCreateRefusesOutOfDiffLine(t *testing.T) {
	cases := []struct {
		name string
		file string
		line int
	}{
		{"line past the hunk", "src/app.go", 4},
		{"line far past the end", "src/app.go", 999},
		{"file the PR does not change", "src/other.go", 2},
		{"path in another form", "./src/app.go", 2},
		{"path in another case", "SRC/app.go", 2},
	}
	t.Run("gitea", func(t *testing.T) {
		g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
		cs := connect(t, realDeps(reviewEnv(g, l), nil))
		for _, c := range cases {
			res := callTool(t, cs, "pr_comment_create", map[string]any{
				"pr_url": reviewPRURL(g), "body": "x " + createMarker, "file": c.file, "line": c.line})
			if !res.IsError || textOf(t, res) != tools.NotInDiffMessage {
				t.Errorf("%s: IsError=%v text=%q", c.name, res.IsError, textOf(t, res))
			}
		}
		if got := commentEndpoints(g); len(got) != 0 {
			t.Errorf("requests reached the comment endpoints: %v", got)
		}
		if w := g.writeLog(); len(w) != 0 {
			t.Errorf("write requests: %v", w)
		}
		if g.hits.Load() == 0 {
			t.Error("the diff was never read; the refusal would be vacuous")
		}
	})
	t.Run("bitbucket", func(t *testing.T) {
		b := newFakeBBSHost(t)
		cs := connect(t, realDeps(bbsEnv(b), nil))
		for _, c := range append(cases[1:], struct {
			name string
			file string
			line int
		}{"line before the hunk", "src/app.go", 1}, struct {
			name string
			file string
			line int
		}{"line after the hunk", "src/app.go", 9}) {
			res := callTool(t, cs, "pr_comment_create", map[string]any{
				"pr_url": b.prURL(), "body": "x", "file": c.file, "line": c.line})
			if !res.IsError || textOf(t, res) != tools.NotInDiffMessage {
				t.Errorf("%s: IsError=%v text=%q", c.name, res.IsError, textOf(t, res))
			}
		}
		if got := b.commentRequests(); len(got) != 0 {
			t.Errorf("requests reached the comment endpoint: %v", got)
		}
		if b.hits.Load() == 0 {
			t.Error("the diff was never read; the refusal would be vacuous")
		}
	})
}

func TestPRCommentCreateBitbucket(t *testing.T) {
	b := newFakeBBSHost(t)
	cs := connect(t, realDeps(bbsEnv(b), nil))
	call := func(args map[string]any) (*mcp.CallToolResult, tools.PRCommentCreateResult) {
		args["pr_url"] = b.prURL()
		res := callTool(t, cs, "pr_comment_create", args)
		var got tools.PRCommentCreateResult
		if !res.IsError {
			decodeStructured(t, res, &got)
		}
		return res, got
	}
	last := func() map[string]any {
		posts := b.postLog()
		return posts[len(posts)-1].body
	}

	t.Run("pr-level", func(t *testing.T) {
		res, got := call(map[string]any{"body": "general comment"})
		if res.IsError || got.Inline || got.ID != "701" || !strings.Contains(got.URL, "/pull-requests/7/overview?commentId=701") {
			t.Fatalf("result = %+v (%v)", got, res.IsError)
		}
		if body := last(); body["text"] != "general comment" || body["anchor"] != nil {
			t.Errorf("posted %v", body)
		}
	})

	// The line type is computed from the hunk: line 5 is added, 3 is context.
	for _, tc := range []struct {
		name string
		line int
		typ  string
	}{{"added line", 5, "ADDED"}, {"context line", 3, "CONTEXT"}, {"context line after the change", 8, "CONTEXT"}} {
		t.Run(tc.name, func(t *testing.T) {
			res, got := call(map[string]any{"body": "inline", "file": "src/app.go", "line": tc.line})
			if res.IsError || !got.Inline || got.ID == "" {
				t.Fatalf("result = %+v (%s)", got, textOf(t, res))
			}
			a, _ := last()["anchor"].(map[string]any)
			if a["path"] != "src/app.go" || a["line"] != float64(tc.line) || a["lineType"] != tc.typ ||
				a["fileType"] != "TO" || a["diffType"] != "EFFECTIVE" {
				t.Errorf("anchor = %v", a)
			}
			if s, ok := a["srcPath"]; ok && s != "" {
				t.Errorf("srcPath = %v for a file that was not renamed", s)
			}
		})
	}

	// A renamed file is anchored by its new path, with the old path as srcPath.
	t.Run("renamed file", func(t *testing.T) {
		res, got := call(map[string]any{"body": "on the rename", "file": "src/renamed.go", "line": 3})
		if res.IsError || !got.Inline {
			t.Fatalf("result = %+v (%s)", got, textOf(t, res))
		}
		a, _ := last()["anchor"].(map[string]any)
		if a["path"] != "src/renamed.go" || a["srcPath"] != "src/old_name.go" || a["line"] != float64(3) || a["lineType"] != "ADDED" {
			t.Errorf("anchor = %v", a)
		}
		// The old path is not a path of the PR's new side.
		res, _ = call(map[string]any{"body": "x", "file": "src/old_name.go", "line": 3})
		if !res.IsError || textOf(t, res) != tools.NotInDiffMessage {
			t.Errorf("old path: IsError=%v text=%q", res.IsError, textOf(t, res))
		}
	})
}

// createProvider is a provider with canned answers for pr_comment_create.
type createProvider struct {
	provider.Provider

	mu      sync.Mutex
	diff    *provider.Diff
	results []provider.InlineResult
	inlErr  error
	diffErr error
	calls   int
	inlined []provider.InlineComment
	posted  []string
}

func (c *createProvider) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	c.bump()
	return &provider.PullRequest{HeadSHA: "headsha"}, nil
}

func (c *createProvider) GetDiff(context.Context, provider.PRRef, *provider.PullRequest, provider.DiffOptions) (*provider.Diff, error) {
	c.bump()
	return c.diff, c.diffErr
}

func (c *createProvider) PostComment(_ context.Context, _ provider.PRRef, body string) (*provider.Comment, error) {
	c.bump()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.posted = append(c.posted, body)
	return &provider.Comment{ID: "9", URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-9"}, nil
}

func (c *createProvider) PostInlineComments(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, items []provider.InlineComment) ([]provider.InlineResult, error) {
	c.bump()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inlined = append(c.inlined, items...)
	return c.results, c.inlErr
}

func (c *createProvider) bump() {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
}

func (c *createProvider) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func appDiff() *provider.Diff {
	return &provider.Diff{Files: []provider.FilePatch{{
		Path: "src/app.go", Type: provider.ChangeModified,
		Patch: "@@ -1,3 +1,3 @@\n package main\n-var a = 1\n+var a = 2\n func main() {}\n",
	}}}
}

func TestPRCommentCreateProviderOutcomes(t *testing.T) {
	args := map[string]any{"pr_url": prURL, "body": "x", "file": "src/app.go", "line": 2}
	run := func(t *testing.T, cp *createProvider) *mcp.CallToolResult {
		t.Helper()
		deps, _ := withFake(depsFor(validEnv(), nil), cp)
		return callTool(t, connect(t, deps), "pr_comment_create", args)
	}

	// A comment the server refused is a tool error carrying the item's own
	// fixed sentence.
	t.Run("item not posted", func(t *testing.T) {
		const sentence = "the server rejected the comment"
		res := run(t, &createProvider{diff: appDiff(), results: []provider.InlineResult{{Error: sentence}}})
		if !res.IsError || textOf(t, res) != sentence || res.StructuredContent != nil {
			t.Errorf("IsError=%v text=%q", res.IsError, textOf(t, res))
		}
	})
	t.Run("batch error", func(t *testing.T) {
		pe := &provider.Error{Class: provider.ClassRateLimited, Status: 429}
		res := run(t, &createProvider{diff: appDiff(), inlErr: pe})
		if !res.IsError || textOf(t, res) != pe.Error() {
			t.Errorf("IsError=%v text=%q", res.IsError, textOf(t, res))
		}
	})
	t.Run("diff error", func(t *testing.T) {
		pe := &provider.Error{Class: provider.ClassAuth, Status: 401}
		cp := &createProvider{diffErr: pe}
		res := run(t, cp)
		if !res.IsError || textOf(t, res) != pe.Error() || len(cp.inlined) != 0 {
			t.Errorf("IsError=%v text=%q", res.IsError, textOf(t, res))
		}
	})
	t.Run("wrong number of results", func(t *testing.T) {
		res := run(t, &createProvider{diff: appDiff()})
		if !res.IsError || !strings.HasPrefix(textOf(t, res), "the server sent an unexpected response") {
			t.Errorf("IsError=%v text=%q", res.IsError, textOf(t, res))
		}
	})
	t.Run("posted item without id", func(t *testing.T) {
		cp := &createProvider{diff: appDiff(), results: []provider.InlineResult{{Posted: true}}}
		res := run(t, cp)
		if res.IsError {
			t.Fatalf("tool error: %s", textOf(t, res))
		}
		if textOf(t, res) != "Comment posted on the line.\n" {
			t.Errorf("text = %q", textOf(t, res))
		}
		if len(cp.inlined) != 1 || cp.inlined[0].Line != 2 || cp.inlined[0].LineType != provider.LineAdded {
			t.Errorf("inline items = %+v", cp.inlined)
		}
	})
	t.Run("pr-level url is the provider-built url", func(t *testing.T) {
		cp := &createProvider{}
		deps, _ := withFake(depsFor(validEnv(), nil), cp)
		res := callTool(t, connect(t, deps), "pr_comment_create", map[string]any{"pr_url": prURL, "body": "x"})
		if res.IsError || strings.Contains(mustJSON(t, res), fakeGitea) || !strings.Contains(mustJSON(t, res), `"url":"https://your-gitea.example/octo/demo/pulls/7#issuecomment-9"`) {
			t.Errorf("result = %s", mustJSON(t, res))
		}
	})
}

// TestPRCommentCreateValidationMakesNoProviderCall: an invalid call is
// refused with its fixed sentence before the provider is touched.
func TestPRCommentCreateValidationMakesNoProviderCall(t *testing.T) {
	cp := &createProvider{diff: appDiff()}
	deps, _ := withFake(depsFor(validEnv(), nil), cp)
	cs := connect(t, deps)
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty body", map[string]any{"body": ""}, tools.CreateBodyEmptyMessage},
		{"blank body", map[string]any{"body": " \n"}, tools.CreateBodyEmptyMessage},
		{"too long", map[string]any{"body": strings.Repeat("a", 20001)}, tools.CreateBodyTooLongMessage},
		{"marker line", map[string]any{"body": "hi\n[//]: # (review-mcp:overview:v1)"}, tools.CreateBodyMarkerMessage},
		{"line without file", map[string]any{"body": "x", "line": 2}, tools.CreateLineNeedsFile},
		{"file without line", map[string]any{"body": "x", "file": "src/app.go"}, tools.CreateFileNeedsLine},
		{"line zero", map[string]any{"body": "x", "file": "src/app.go", "line": 0}, tools.CreateLineInvalidMessage},
		{"negative line", map[string]any{"body": "x", "file": "src/app.go", "line": -1}, tools.CreateLineInvalidMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.args["pr_url"] = prURL
			res := callTool(t, cs, "pr_comment_create", tc.args)
			if !res.IsError || textOf(t, res) != tc.want {
				t.Errorf("IsError=%v text=%q, want %q", res.IsError, textOf(t, res), tc.want)
			}
		})
	}
	// A wrong type is rejected by the input schema, before the handler.
	if res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "pr_comment_create",
		Arguments: map[string]any{"pr_url": prURL, "body": "x", "file": "a.go", "line": "two"}}); err == nil && !res.IsError {
		t.Error("a string line was accepted")
	}
	if n := cp.total(); n != 0 {
		t.Errorf("provider called %d times for invalid arguments", n)
	}
}

func TestPRCommentCreateDegradedMakesNoRequests(t *testing.T) {
	cp := &createProvider{diff: appDiff()}
	deps, built := withFake(depsFor(invalidEnv(), nil), cp)
	res := callTool(t, connect(t, deps), "pr_comment_create", map[string]any{"pr_url": prURL, "body": "x"})
	if !res.IsError || textOf(t, res) != tools.ConfigInvalidMessage || built.Load() != 0 || cp.total() != 0 {
		t.Errorf("IsError=%v text=%q built=%d calls=%d", res.IsError, textOf(t, res), built.Load(), cp.total())
	}
}

// TestLeakPRCommentCreateEndToEnd [canary]: debug logging, every secret
// configured, and four markers: a thread body written by someone else, the
// model's finding (inline comment), the model's security text (overview)
// and the body of a pr_comment_create call. Each reaches the place it is
// meant for; none reaches the logs or an error text, and no secret appears
// anywhere.
func TestLeakPRCommentCreateEndToEnd(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	g := newFakeGiteaHost(t)
	g.plantComment("alice", 7, "Please double check the constant. "+threadMarker)
	answer := "```yaml\nreview:\n  estimated_effort_to_review: 2\n  relevant_tests: \"No\"\n" +
		"  key_issues_to_review:\n    - relevant_file: src/app.go\n      issue_header: Constant changed\n" +
		"      issue_content: The constant changed. " + inlineMarker + "\n      start_line: 2\n      end_line: 2\n" +
		"  security_concerns: Possible exposure. " + overviewMarker + "\n" +
		"  performance_concerns: \"No\"\n```\n"
	l := newFakeLLMHost(t, 200, answer)
	cs := connect(t, realDeps(reviewEnv(g, l), logger))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	secretURL := reviewPRURL(g) + "?access_token=" + fakeGitea
	rev := callTool(t, cs, "pr_review", map[string]any{"pr_url": secretURL, "publish": true})
	prLevel := callTool(t, cs, "pr_comment_create", map[string]any{"pr_url": secretURL, "body": "note " + createMarker})
	inline := callTool(t, cs, "pr_comment_create", map[string]any{"pr_url": secretURL, "body": "line note " + createMarker, "file": "src/app.go", "line": 3})
	refused := callTool(t, cs, "pr_comment_create", map[string]any{"pr_url": secretURL, "body": "x " + createMarker, "file": "src/app.go", "line": 40})
	invalid := callTool(t, cs, "pr_comment_create", map[string]any{"pr_url": secretURL, "body": "[//]: # (review-mcp:overview:v1) " + createMarker + "\n[//]: # (review-mcp:overview:v1)"})
	foreign := callTool(t, cs, "pr_comment_create", map[string]any{"pr_url": "https://other.example.net/x?token=" + fakeBitbkt, "body": createMarker})
	if rev.IsError || prLevel.IsError || inline.IsError || !refused.IsError || !invalid.IsError || !foreign.IsError {
		t.Fatalf("unexpected results: review=%v prLevel=%v inline=%v refused=%v invalid=%v foreign=%v",
			rev.IsError, prLevel.IsError, inline.IsError, refused.IsError, invalid.IsError, foreign.IsError)
	}

	// The markers reached the places they are meant for; otherwise the log
	// check below proves nothing.
	if b := l.recorded(); len(b) == 0 || !strings.Contains(b[0], threadMarker) {
		t.Fatal("the thread marker did not reach the LLM request")
	}
	var sent strings.Builder
	for _, b := range g.recorded() {
		sent.WriteString(b)
	}
	for what, m := range map[string]string{"inline finding": inlineMarker, "overview": overviewMarker, "create body": createMarker} {
		if !strings.Contains(sent.String(), m) {
			t.Fatalf("the %s marker never reached the provider", what)
		}
	}
	if !strings.Contains(textOf(t, rev), inlineMarker) {
		t.Fatal("the finding marker is missing from the review result")
	}

	logText := logs.String()
	if !strings.Contains(logText, "level=DEBUG") || !strings.Contains(logText, "pr_comment_create") {
		t.Fatalf("debug logging did not run; the leak check would be vacuous:\n%s", logText)
	}
	markers := []string{createMarker, threadMarker, inlineMarker, overviewMarker}
	surfaces := map[string]string{
		"logs": logText, "tools/list": mustJSON(t, list),
		"refused": mustJSON(t, refused), "invalid": mustJSON(t, invalid), "foreign": mustJSON(t, foreign),
		"prLevel": mustJSON(t, prLevel), "inline": mustJSON(t, inline),
	}
	for _, m := range markers {
		for what, s := range surfaces {
			if strings.Contains(s, m) {
				t.Errorf("marker %q leaked into %s", m, what)
			}
		}
	}
	surfaces["review"] = mustJSON(t, rev)
	for _, secret := range allSecrets {
		for what, s := range surfaces {
			if strings.Contains(s, secret) {
				t.Errorf("secret %q leaked into %s", secret, what)
			}
		}
		if strings.Contains(sent.String(), secret) {
			t.Errorf("secret %q reached a request body", secret)
		}
	}
}
