package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

const (
	// descMarker is placed in the PR description: it must reach the LLM
	// request body and nothing else.
	descMarker = "DESCRIPTION-MARKER-3e9b1f-llm-only"
	// answerMarker is placed in the model's answer (an unparseable one in
	// the failure test) and in error bodies.
	answerMarker = "ANSWER-MARKER-3e9b1f-never-surface"
	// argMarker is placed in tool arguments.
	argMarker = "ARGUMENT-MARKER-3e9b1f-never-log"
	// titleMarker and branchMarker are in the PR title and the source branch
	// name; diffMarker is in a changed line. They reach the LLM and the
	// result, never the logs.
	titleMarker  = "TITLE-MARKER-3e9b1f"
	branchMarker = "BRANCH-MARKER-3e9b1f"
	diffMarker   = "DIFF-MARKER-3e9b1f"
	// headerMarker, contentMarker and securityMarker are in the fake model's
	// answer: they reach the tool result, never the logs.
	headerMarker      = "HEADER-MARKER-3e9b1f"
	contentMarker     = "CONTENT-MARKER-3e9b1f"
	securityMarker    = "SECURITY-MARKER-3e9b1f"
	performanceMarker = "PERFORMANCE-MARKER-3e9b1f"
)

const reviewDiff = `diff --git a/src/app.go b/src/app.go
index 1111111..2222222 100644
--- a/src/app.go
+++ b/src/app.go
@@ -1,3 +1,3 @@
 package main
-var a = 1
+var a = 2 // ` + diffMarker + `
 func main() {}
`

const goodAnswer = "```yaml\nreview:\n  estimated_effort_to_review: 2\n  relevant_tests: \"No\"\n" +
	"  key_issues_to_review:\n    - relevant_file: src/app.go\n      issue_header: Constant changed " + headerMarker + "\n" +
	"      issue_content: The constant changed without a test. " + contentMarker + "\n      start_line: 2\n      end_line: 2\n" +
	"  security_concerns: Possible exposure. " + securityMarker + "\n" +
	"  performance_concerns: One query per item in src/app.go. " + performanceMarker + "\n```\n"

// fakeServer counts requests and records bodies.
type fakeServer struct {
	srv   *httptest.Server
	conns *connCounter
	hits  atomic.Int64

	mu     sync.Mutex
	bodies []string
	auths  []string
	// writes records "METHOD path" of every non-GET request, in order;
	// requests records every request.
	writes   []string
	requests []string

	// The fake Gitea's publish state: the PR-level comments (id -> body,
	// author login), the posted reviews and their comments.
	comments    map[int64]giteaComment
	nextComment int64
	reviews     []map[string]any
	// ghost lists a second changed file that the diff does not contain: the
	// provider cannot read it, so the result is partial (X-18).
	ghost bool
	// large, when set, replaces the PR's one file with these added files
	// (path -> head content), for a review in parts (X-19).
	large map[string]string
}

// setGhost makes the files endpoint list a file the provider cannot read.
func (f *fakeServer) setGhost() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ghost = true
}

type giteaComment struct {
	body, login string
	userID      int64
}

func (f *fakeServer) record(r *http.Request) string {
	f.hits.Add(1)
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, string(b))
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
	}
	return string(b)
}

// requestLog returns "METHOD path" of every request received, in order.
func (f *fakeServer) requestLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeServer) writeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

// overviewBodies returns the bodies of the PR-level comments that carry
// the overview marker, by id order.
func (f *fakeServer) overviewBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := int64(1); id <= f.nextComment; id++ {
		if c, ok := f.comments[id]; ok && review.HasOverviewMarker(c.body) {
			out = append(out, c.body)
		}
	}
	return out
}

// plantComment adds a PR-level comment by another account.
func (f *fakeServer) plantComment(login string, userID int64, body string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextComment++
	f.comments[f.nextComment] = giteaComment{body: body, login: login, userID: userID}
	return f.nextComment
}

func (f *fakeServer) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func (f *fakeServer) authHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

// newFakeGiteaHost serves octo/demo#7 with the description marker. It
// covers the whole publish flow (spec P7 §3.3, §4.2): the token's user
// (/api/v1/user, "review-bot", id 42), the PR-level comments (listing,
// posting, reading and editing, kept in memory), and the reviews that carry
// the inline comments.
func newFakeGiteaHost(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{comments: map[int64]giteaComment{}, nextComment: 54}
	const api = "/api/v1/repos/octo/demo"
	const web = "https://your-gitea.example/octo/demo/pulls/7"
	f.srv, f.conns = startCounted(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := f.record(r)
		var body map[string]any
		_ = json.Unmarshal([]byte(raw), &body)
		if r.Header.Get("Authorization") != "token "+fakeGitea {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		writeJ := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		p := r.URL.Path
		switch {
		case r.Method == "GET" && p == api+"/pulls/7":
			writeJ(map[string]any{
				"title": "Change the constant " + titleMarker, "body": "Please look. " + descMarker, "state": "open",
				"html_url": "https://your-gitea.example/octo/demo/pulls/7", "merge_base": "mergesha",
				"user": map[string]any{"login": "alice"},
				"head": map[string]any{"ref": "feature-" + branchMarker, "sha": "headsha"},
				"base": map[string]any{"ref": "main", "sha": "basesha"},
			})
		case r.Method == "GET" && p == api+"/pulls/7.diff" && f.large != nil:
			_, _ = io.WriteString(w, largeDiff(f.large))
		case r.Method == "GET" && p == api+"/pulls/7/files" && f.large != nil:
			files := []any{}
			if r.URL.Query().Get("page") == "1" {
				for _, path := range slices.Sorted(maps.Keys(f.large)) {
					n := strings.Count(f.large[path], "\n")
					files = append(files, map[string]any{"filename": path, "status": "added", "additions": n, "deletions": 0, "changes": n})
				}
			}
			writeJ(files)
		case r.Method == "GET" && strings.HasPrefix(p, api+"/raw/") && f.large != nil:
			content, ok := f.large[strings.TrimPrefix(p, api+"/raw/")]
			if !ok || r.URL.Query().Get("ref") != "headsha" {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, content)
		case r.Method == "GET" && p == api+"/pulls/7.diff":
			_, _ = io.WriteString(w, reviewDiff)
		case r.Method == "GET" && p == api+"/pulls/7/files":
			if r.URL.Query().Get("page") != "1" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			files := []any{map[string]any{"filename": "src/app.go", "status": "changed", "additions": 1, "deletions": 1, "changes": 2}}
			f.mu.Lock()
			if f.ghost {
				files = append(files, map[string]any{"filename": "src/other.go", "status": "changed", "additions": 1, "deletions": 1, "changes": 2})
			}
			f.mu.Unlock()
			writeJ(files)
		case r.Method == "GET" && p == api+"/raw/src/app.go":
			if r.URL.Query().Get("ref") == "headsha" {
				_, _ = io.WriteString(w, "package main\nvar a = 2 // "+diffMarker+"\nfunc main() {}\n")
			} else {
				_, _ = io.WriteString(w, "package main\nvar a = 1\nfunc main() {}\n")
			}
		case r.Method == "GET" && p == "/api/v1/user":
			writeJ(map[string]any{"id": 42, "login": "review-bot"})
		case r.Method == "POST" && p == api+"/issues/7/comments":
			text, _ := body["body"].(string)
			f.mu.Lock()
			f.nextComment++
			id := f.nextComment
			f.comments[id] = giteaComment{body: text, login: "review-bot", userID: 42}
			f.mu.Unlock()
			writeJ(map[string]any{"id": id, "html_url": web + "#issuecomment-" + strconv.FormatInt(id, 10)})
		case r.Method == "GET" && p == api+"/issues/7/comments":
			out := []any{}
			f.mu.Lock()
			if r.URL.Query().Get("page") == "1" {
				for id := int64(1); id <= f.nextComment; id++ {
					if c, ok := f.comments[id]; ok {
						out = append(out, f.issueComment(id, c))
					}
				}
			}
			f.mu.Unlock()
			writeJ(out)
		case strings.HasPrefix(p, api+"/issues/comments/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(p, api+"/issues/comments/"), 10, 64)
			f.mu.Lock()
			c, ok := f.comments[id]
			if ok && r.Method == "PATCH" {
				c.body, _ = body["body"].(string)
				f.comments[id] = c
			}
			f.mu.Unlock()
			switch {
			case !ok:
				http.NotFound(w, r)
			case r.Method == "GET":
				writeJ(f.issueComment(id, c))
			default:
				writeJ(map[string]any{"id": id})
			}
		case r.Method == "POST" && p == api+"/pulls/7/reviews":
			f.mu.Lock()
			rid := 300 + len(f.reviews)
			var cs []any
			comments, _ := body["comments"].([]any)
			for i, c := range comments {
				cm, _ := c.(map[string]any)
				cid := rid*10 + i
				cs = append(cs, map[string]any{"id": cid, "path": cm["path"], "body": cm["body"], "position": cm["new_position"],
					"user": map[string]any{"id": 42, "login": "review-bot"}, "html_url": web + "/files#issuecomment-" + strconv.Itoa(cid)})
			}
			f.reviews = append(f.reviews, map[string]any{"id": rid, "state": "COMMENT", "comments": cs})
			f.mu.Unlock()
			writeJ(map[string]any{"id": rid, "state": "COMMENT", "html_url": web + "#pullrequestreview-" + strconv.Itoa(rid)})
		case r.Method == "GET" && p == api+"/pulls/7/reviews":
			out := []any{}
			f.mu.Lock()
			if r.URL.Query().Get("page") == "1" {
				for _, rv := range f.reviews {
					out = append(out, map[string]any{"id": rv["id"], "state": rv["state"]})
				}
			}
			f.mu.Unlock()
			writeJ(out)
		case r.Method == "GET" && strings.HasPrefix(p, api+"/pulls/7/reviews/") && strings.HasSuffix(p, "/comments"):
			out := []any{}
			f.mu.Lock()
			for _, rv := range f.reviews {
				if r.URL.Query().Get("page") == "1" && p == api+"/pulls/7/reviews/"+strconv.Itoa(rv["id"].(int))+"/comments" {
					out = append(out, rv["comments"].([]any)...)
				}
			}
			f.mu.Unlock()
			writeJ(out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// issueComment is the API form of a PR-level comment of the fake Gitea.
func (f *fakeServer) issueComment(id int64, c giteaComment) map[string]any {
	const web = "https://your-gitea.example/octo/demo/pulls/7"
	return map[string]any{"id": id, "type": "comment", "body": c.body,
		"user":     map[string]any{"id": c.userID, "login": c.login},
		"html_url": web + "#issuecomment-" + strconv.FormatInt(id, 10), "pull_request_url": web}
}

// fakeLLMHost answers every chat completion with the given status and body.
type fakeLLMHost struct {
	fakeServer
	status int
	answer string // chat content when status is 200; raw body otherwise
}

func newFakeLLMHost(t *testing.T, status int, answer string) *fakeLLMHost {
	t.Helper()
	f := &fakeLLMHost{status: status, answer: answer}
	f.srv, f.conns = startCounted(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		if f.status != http.StatusOK {
			http.Error(w, f.answer, f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": f.answer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// reviewEnv is a valid configuration (all secrets set) pointing at the fakes.
func reviewEnv(g *fakeServer, l *fakeLLMHost) map[string]string {
	env := validEnv()
	env["REVIEW_MCP_GITEA_BASE_URL"] = g.srv.URL
	env["REVIEW_MCP_LLM_BASE_URL"] = l.srv.URL + "/v1"
	env["REVIEW_MCP_LOG_LEVEL"] = "debug"
	return env
}

func reviewPRURL(g *fakeServer) string { return g.srv.URL + "/octo/demo/pulls/7" }

// realDeps wires the real resolver and LLM client factories.
func realDeps(env map[string]string, logger *slog.Logger) Deps {
	d := depsFor(env, logger)
	d.NewResolver = wiring.NewResolver
	d.NewLLM = wiring.NewLLM
	return d
}

func connectWith(t *testing.T, deps Deps, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := New(deps).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, opts).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestPRReviewToolDefinition(t *testing.T) {
	cs := connect(t, realDeps(validEnv(), nil))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var tl *mcp.Tool
	for _, x := range list.Tools {
		if x.Name == "pr_review" {
			tl = x
		}
	}
	if tl == nil {
		t.Fatal("pr_review is not registered")
	}
	const want = "Reviews a pull request with the configured LLM and returns a structured review (key issues, effort, tests, security, performance) with code excerpts. Set publish=true to also post it: one overview comment that later runs edit in place, and the findings on changed lines as inline comments. The PR's title, description, existing comments and diff are sent to the configured LLM endpoint. If the result says the review is partial, tell the user how many files were not reviewed and never state that those files have no issues."
	if tl.Description != want {
		t.Errorf("description = %q", tl.Description)
	}
	a := tl.Annotations
	if a == nil || a.ReadOnlyHint || a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint ||
		a.OpenWorldHint == nil || !*a.OpenWorldHint {
		t.Errorf("annotations = %+v", a)
	}
	raw, _ := json.Marshal(tl.InputSchema)
	var in struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	var props []string
	for k, v := range in.Properties {
		props = append(props, k)
		if v["description"] == nil || v["description"] == "" {
			t.Errorf("property %s has no description", k)
		}
	}
	sort.Strings(props)
	if got := strings.Join(props, ","); got != "extra_instructions,inline_findings,max_findings,output_language,pr_url,publish,wait_seconds" {
		t.Errorf("properties = %s", got)
	}
	if strings.Join(in.Required, ",") != "pr_url" {
		t.Errorf("required = %v, want only pr_url", in.Required)
	}
	// The output schema is the one generated from the descriptors.
	gotOut, _ := json.Marshal(tl.OutputSchema)
	wantOut, _ := json.Marshal(review.ResultSchema())
	var a1, a2 any
	_ = json.Unmarshal(gotOut, &a1)
	_ = json.Unmarshal(wantOut, &a2)
	if !reflect.DeepEqual(a1, a2) {
		t.Errorf("output schema differs from review.ResultSchema():\n%s\n%s", gotOut, wantOut)
	}
}

func TestPRReviewCall(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))

	res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "max_findings": 3})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	text := textOf(t, res)
	var got review.Result
	decodeStructured(t, res, &got)
	if want := render.Client(&got); want != text {
		t.Errorf("text is not the client rendering of the structured result:\n%s\n---\n%s", text, want)
	}
	if !strings.HasPrefix(text, "## PR Review") || !strings.Contains(text, "Constant changed") {
		t.Errorf("unexpected markdown:\n%s", text)
	}
	if got.Review == nil || len(got.Review.KeyIssuesToReview) != 1 || got.Metadata.LLMCalls != 1 || got.Publish != nil {
		t.Errorf("structured = %+v", got)
	}
	if ki := got.Review.KeyIssuesToReview[0]; ki.Snippet == "" || ki.Link == "" {
		t.Errorf("finding has no snippet or link: %+v", ki)
	}

	// One chat request, carrying the marker, authorized with the LLM key.
	bodies := l.recorded()
	if len(bodies) != 1 || !strings.Contains(bodies[0], descMarker) {
		t.Fatalf("LLM requests = %d; description marker present = %v", len(bodies), len(bodies) == 1 && strings.Contains(bodies[0], descMarker))
	}
	if h := l.authHeaders()[0]; h != "Bearer "+fakeLLMKey {
		t.Errorf("LLM Authorization header = %q", h)
	}
	for _, s := range []string{fakeGitea, fakeBitbkt, fakeLLMKey} {
		if strings.Contains(bodies[0], s) {
			t.Errorf("a secret reached the LLM request body")
		}
	}
	// max_findings reaches the prompt.
	if !strings.Contains(bodies[0], "3") {
		t.Error("request body is empty of the max_findings value")
	}
	// Nothing was posted without publish.
	for _, b := range g.recorded() {
		if strings.Contains(b, "PR Review") {
			t.Error("a comment was posted without publish")
		}
	}
}

// TestPRReviewPublish runs the whole publish flow end to end with the real
// Gitea provider: the first call looks up an earlier overview (none),
// posts the overview with the marker, posts the finding on the changed line
// as an inline comment and edits the overview with its link; the second
// call finds that overview by marker and author, posts its inline comment
// and edits the same overview once. A foreign comment carrying the marker
// is never touched. Inline deduplication is WP-PR-7e, so the second call
// posts its inline comment again.
func TestPRReviewPublish(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	foreignBody := "Not ours.\n\n" + review.OverviewMarker
	foreign := g.plantComment("mallory", 77, foreignBody)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	call := func() review.Result {
		t.Helper()
		res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "publish": true})
		if res.IsError {
			t.Fatalf("tool error: %s", textOf(t, res))
		}
		var got review.Result
		decodeStructured(t, res, &got)
		if !strings.Contains(textOf(t, res), "### Publishing") {
			t.Errorf("the client text has no publish summary")
		}
		return got
	}
	const api = "/api/v1/repos/octo/demo"
	first := call()
	if p := first.Publish; p == nil || !p.Published || p.Updated || p.CommentID != "56" || p.Error != "" ||
		p.Inline == nil || *p.Inline != (review.InlineSummary{Posted: 1}) {
		t.Fatalf("first publish = %+v (inline %+v)", first.Publish, first.Publish.Inline)
	}
	if ki := first.Review.KeyIssuesToReview[0]; ki.InlineStatus != review.InlinePosted || ki.InlineURL == "" {
		t.Errorf("finding = %+v", ki)
	}
	wantFirst := []string{"POST " + api + "/issues/7/comments", "POST " + api + "/pulls/7/reviews", "PATCH " + api + "/issues/comments/56"}
	if got := g.writeLog(); !slices.Equal(got, wantFirst) {
		t.Errorf("first writes %v, want %v", got, wantFirst)
	}
	ov := g.overviewBodies()
	if len(ov) != 2 || ov[0] != foreignBody || !review.HasOverviewMarker(ov[1]) ||
		!strings.Contains(ov[1], first.Review.KeyIssuesToReview[0].InlineURL) || !strings.Contains(ov[1], "Constant changed") {
		t.Fatalf("overviews after the first call: %q", ov)
	}

	second := call()
	if p := second.Publish; p == nil || !p.Published || !p.Updated || p.CommentID != "56" ||
		p.Inline == nil || *p.Inline != (review.InlineSummary{SkippedDuplicate: 1}) {
		t.Errorf("second publish = %+v", second.Publish)
	}
	// The finding is already on the PR with its fingerprint (WP-PR-7e): the
	// second run posts no review, only the in-place overview edit.
	wantSecond := append(wantFirst, "PATCH "+api+"/issues/comments/56")
	if got := g.writeLog(); !slices.Equal(got, wantSecond) {
		t.Errorf("writes %v, want %v", got, wantSecond)
	}
	if ov := g.overviewBodies(); len(ov) != 2 || ov[0] != foreignBody {
		t.Errorf("overviews after the second call: %d (the foreign comment %d must stay as it is)", len(ov), foreign)
	}
}

// TestPRReviewPerformanceToggle: review.require_performance (X-12) switches
// the performance field end to end. On (the default), the schema line and
// the example line reach the prompt and the model's answer reaches the
// result, the client markdown and the published overview; off, the prompt
// has neither line and no profile shows a performance row, even when the
// model answers the field anyway.
func TestPRReviewPerformanceToggle(t *testing.T) {
	const schemaLine = `performance_concerns: str = Field(description=\"Does this PR introduce code with a likely performance problem`
	const exampleLine = `\n  performance_concerns: |\n    No\n`
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprintf("require_performance=%v", on), func(t *testing.T) {
			g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
			env := reviewEnv(g, l)
			if !on {
				env["REVIEW_MCP_REVIEW_REQUIRE_PERFORMANCE"] = "false"
			}
			cs := connect(t, realDeps(env, nil))
			res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "publish": true})
			if res.IsError {
				t.Fatalf("tool error: %s", textOf(t, res))
			}
			bodies := l.recorded()
			if len(bodies) != 1 {
				t.Fatalf("LLM requests = %d", len(bodies))
			}
			var req struct {
				Messages []struct{ Content string } `json:"messages"`
			}
			if err := json.Unmarshal([]byte(bodies[0]), &req); err != nil || len(req.Messages) == 0 {
				t.Fatalf("LLM request: %v", err)
			}
			system := req.Messages[0].Content
			if got := strings.Contains(system, strings.ReplaceAll(schemaLine, `\"`, `"`)); got != on {
				t.Errorf("schema line in the system prompt = %v, want %v", got, on)
			}
			if got := strings.Contains(system, strings.ReplaceAll(exampleLine, `\n`, "\n")); got != on {
				t.Errorf("example line in the system prompt = %v, want %v", got, on)
			}
			var overview string
			for _, b := range g.recorded() {
				if strings.Contains(b, "PR Review") {
					overview = b
				}
			}
			if overview == "" {
				t.Fatal("no overview was posted")
			}
			var got review.Result
			decodeStructured(t, res, &got)
			surfaces := map[string]string{"text": textOf(t, res), "structured": mustJSON(t, res.StructuredContent), "overview": overview}
			for what, s := range surfaces {
				if c := strings.Contains(s, performanceMarker); c != on {
					t.Errorf("performance answer in the %s = %v, want %v", what, c, on)
				}
			}
			for _, what := range []string{"text", "overview"} {
				if c := strings.Contains(surfaces[what], "Performance concerns"); c != on {
					t.Errorf("performance row in the %s = %v, want %v", what, c, on)
				}
			}
		})
	}
}

// TestPRReviewLinksOnlyFilesOfThePR: findings on a path outside the PR (a
// ".." path and a file the PR does not contain) carry no link in the
// structured result, the client markdown or the published comment, while the
// finding on a PR file keeps its link (architect review C1).
func TestPRReviewLinksOnlyFilesOfThePR(t *testing.T) {
	answer := "```yaml\nreview:\n  key_issues_to_review:\n" +
		"    - relevant_file: src/app.go\n      issue_header: Real\n      issue_content: On a PR file.\n      start_line: 2\n      end_line: 2\n" +
		"    - relevant_file: ../../evil/x.go\n      issue_header: Escaping\n      issue_content: Outside the repository.\n      start_line: 3\n      end_line: 3\n" +
		"    - relevant_file: not/in/pr.go\n      issue_header: Missing\n      issue_content: Not in the PR.\n      start_line: 4\n      end_line: 4\n" +
		"```\n"
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, answer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "publish": true})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	var got review.Result
	decodeStructured(t, res, &got)
	if got.Review == nil || len(got.Review.KeyIssuesToReview) != 3 {
		t.Fatalf("structured = %+v", got.Review)
	}
	for _, ki := range got.Review.KeyIssuesToReview {
		if want := ki.RelevantFile == "src/app.go"; (ki.Link != "") != want {
			t.Errorf("finding on %q: link %q, want a link = %v", ki.RelevantFile, ki.Link, want)
		}
	}
	const linkPart = "/src/commit/headsha/"
	const inlinePart = "/files#issuecomment-"
	var published string
	for _, b := range g.recorded() {
		if strings.Contains(b, "Escaping") && strings.Contains(b, review.OverviewMarker) {
			published = b // the last overview body: the edit with the inline link
		}
	}
	if published == "" {
		t.Fatal("the review was not published")
	}
	for name, out := range map[string]string{"client markdown": textOf(t, res), "published comment": published} {
		// The finding on src/app.go is on a changed line, so it links its
		// inline comment instead of its file line (spec P7 §3.3).
		if n := strings.Count(out, linkPart) + strings.Count(out, inlinePart); n != 1 {
			t.Errorf("%s has %d finding links, want exactly 1 (src/app.go)", name, n)
		}
		for _, bad := range []string{linkPart + "..", "evil/x.go#L", "not/in/pr.go#L"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s links a file outside the PR (%q)", name, bad)
			}
		}
	}
}

// TestPRReviewValidation: invalid arguments are rejected with fixed sentences
// before any request to either fake.
func TestPRReviewValidation(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"language", map[string]any{"output_language": "not a locale " + argMarker}, tools.InvalidOutputLanguageMessage},
		{"language underscore", map[string]any{"output_language": "tr_TR"}, tools.InvalidOutputLanguageMessage},
		{"max_findings zero", map[string]any{"max_findings": 0}, tools.InvalidMaxFindingsMessage},
		{"max_findings 21", map[string]any{"max_findings": 21}, tools.InvalidMaxFindingsMessage},
		{"max_findings negative", map[string]any{"max_findings": -1}, tools.InvalidMaxFindingsMessage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.args["pr_url"] = reviewPRURL(g)
			res := callTool(t, cs, "pr_review", c.args)
			if !res.IsError || textOf(t, res) != c.want {
				t.Errorf("IsError=%v text=%q, want %q", res.IsError, textOf(t, res), c.want)
			}
		})
	}
	if g.hits.Load() != 0 || l.hits.Load() != 0 {
		t.Errorf("requests before validation: provider %d, llm %d", g.hits.Load(), l.hits.Load())
	}
	// The boundary values are accepted.
	for _, n := range []int{1, 20} {
		res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "max_findings": n, "output_language": "tr-TR"})
		if res.IsError {
			t.Errorf("max_findings %d rejected: %s", n, textOf(t, res))
		}
	}
}

// TestPRReviewDegradedMakesNoRequests [canary]: with an invalid configuration
// the standard "call server_info" error is returned and neither the provider
// nor the LLM receives a request.
func TestPRReviewDegradedMakesNoRequests(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	env := reviewEnv(g, l)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12" // invalid, secrets stay set
	deps := realDeps(env, nil)
	if deps.LoadErr == nil {
		t.Fatal("the configuration should be invalid")
	}
	cs := connect(t, deps)
	res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "publish": true})
	const want = "review-mcp configuration is invalid; call server_info for the list of problems"
	if !res.IsError || textOf(t, res) != want {
		t.Errorf("IsError=%v text=%q", res.IsError, textOf(t, res))
	}
	if res.StructuredContent != nil {
		t.Errorf("unexpected structured content %v", res.StructuredContent)
	}
	if g.hits.Load() != 0 || l.hits.Load() != 0 {
		t.Errorf("degraded config made requests: provider %d, llm %d", g.hits.Load(), l.hits.Load())
	}
}

func TestPRReviewClassifiedErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"auth", 401, "denied " + answerMarker + " " + fakeLLMKey, "rejected the credentials"},
		{"unparseable", 200, "no yaml here " + answerMarker, "could not be parsed as a review"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, l := newFakeGiteaHost(t), newFakeLLMHost(t, c.status, c.body)
			cs := connect(t, realDeps(reviewEnv(g, l), nil))
			res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g)})
			text := textOf(t, res)
			if !res.IsError || !strings.Contains(text, c.want) {
				t.Errorf("IsError=%v text=%q, want it to contain %q", res.IsError, text, c.want)
			}
			if strings.Contains(text, answerMarker) || strings.Contains(text, fakeLLMKey) {
				t.Errorf("error text leaks: %q", text)
			}
			if c.name == "unparseable" && l.hits.Load() != 2 {
				t.Errorf("LLM calls = %d, want 2 (one re-ask)", l.hits.Load())
			}
		})
	}
}

// TestPRReviewProgress: with a progress token the four stages arrive in order;
// without one nothing is sent.
func TestPRReviewProgress(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	var mu sync.Mutex
	var msgs []string
	var progress []float64
	opts := &mcp.ClientOptions{ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, r.Params.Message)
		progress = append(progress, r.Params.Progress)
	}}
	cs := connectWith(t, realDeps(reviewEnv(g, l), nil), opts)

	params := &mcp.CallToolParams{Name: "pr_review", Arguments: map[string]any{"pr_url": reviewPRURL(g)}}
	params.SetProgressToken("tok-1")
	res, err := cs.CallTool(context.Background(), params)
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(msgs) >= 4 })
	mu.Lock()
	got := strings.Join(msgs, "|")
	mu.Unlock()
	if got != "fetching|preparing diff|calling model|rendering" {
		t.Errorf("stages = %q", got)
	}
	if !sort.Float64sAreSorted(progress) || progress[0] != 1 {
		t.Errorf("progress values = %v", progress)
	}

	mu.Lock()
	msgs, progress = nil, nil
	mu.Unlock()
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "pr_review", Arguments: map[string]any{"pr_url": reviewPRURL(g)}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(msgs) != 0 {
		t.Errorf("progress sent without a token: %v", msgs)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestLeakPRReviewEndToEnd [canary]: a fake provider and a fake LLM are
// reached through the real wiring; every secret is set and debug logging is
// on. The description marker must reach the LLM request body, and must appear
// neither in the logs nor in any result or error text; no secret appears
// anywhere. Together with TestSDKLogsCarryNoArgumentsOrResults it closes
// the X-8 entry criterion of P4 (spec 0.1 and 6.3).
func TestLeakPRReviewEndToEnd(t *testing.T) {
	type variant struct {
		name        string
		llmStatus   int
		llmBody     string
		wantError   bool
		wantMarkers bool // the description marker reaches the LLM
	}
	for _, v := range []variant{
		{"success", 200, goodAnswer + "\n# " + answerMarker, false, true},
		{"llm auth error", 401, "denied " + answerMarker + " " + fakeLLMKey, true, true},
		{"llm server error", 500, "boom " + answerMarker + " " + descMarker + " " + contentMarker, true, true},
		{"unparseable answer", 200, "not yaml " + answerMarker + " " + descMarker + " " + headerMarker + " " + securityMarker, true, true},
	} {
		t.Run(v.name, func(t *testing.T) {
			var logs syncBuffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			g, l := newFakeGiteaHost(t), newFakeLLMHost(t, v.llmStatus, v.llmBody)
			env := reviewEnv(g, l)
			env["REVIEW_MCP_LLM_MAX_RETRIES"] = "0"
			cs := connect(t, realDeps(env, logger))

			secretURL := reviewPRURL(g) + "?access_token=" + fakeGitea
			list, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			res := callTool(t, cs, "pr_review", map[string]any{
				"pr_url": secretURL, "extra_instructions": "be brief " + argMarker, "publish": v.llmStatus == 200,
			})
			if res.IsError != v.wantError {
				t.Fatalf("IsError = %v: %s", res.IsError, textOf(t, res))
			}
			// The PR markers reached the LLM.
			if v.wantMarkers {
				bodies := l.recorded()
				if len(bodies) == 0 {
					t.Fatal("the LLM received no request")
				}
				for _, m := range []string{descMarker, titleMarker, branchMarker, diffMarker, argMarker} {
					if !strings.Contains(bodies[0], m) {
						t.Fatalf("marker %q did not reach the LLM request body", m)
					}
				}
			}
			logText := logs.String()
			if !strings.Contains(logText, "level=DEBUG") || !strings.Contains(logText, "pr_review") {
				t.Fatalf("debug logging did not run; the leak check would be vacuous:\n%s", logText)
			}
			allMarkers := []string{descMarker, titleMarker, branchMarker, diffMarker, argMarker,
				answerMarker, headerMarker, contentMarker, securityMarker, performanceMarker}
			if !v.wantError {
				// The model's words reach the tool result, as text and as
				// structured content; the PR title and the changed line too.
				text, structured := textOf(t, res), mustJSON(t, res.StructuredContent)
				for _, m := range []string{headerMarker, contentMarker, securityMarker, performanceMarker, titleMarker, diffMarker} {
					if !strings.Contains(text, m) || !strings.Contains(structured, m) {
						t.Errorf("marker %q is missing from the result text or structured content", m)
					}
				}
			}
			// The log capture never carries PR text, arguments or the model's
			// words; an error result never carries them either.
			surfaces := map[string]string{"logs": logText, "tools/list": mustJSON(t, list)}
			if v.wantError {
				surfaces["error result"] = mustJSON(t, res)
			}
			for _, m := range allMarkers {
				for what, s := range surfaces {
					if strings.Contains(s, m) {
						t.Errorf("marker %q leaked into %s", m, what)
					}
				}
			}
			surfaces["result"] = mustJSON(t, res)
			for _, secret := range allSecrets {
				for what, s := range surfaces {
					if strings.Contains(s, secret) {
						t.Errorf("secret %q leaked into %s", secret, what)
					}
				}
			}
			// Nothing the fakes received besides the LLM body carries the other
			// provider's or the LLM's secret.
			for _, b := range g.recorded() {
				if strings.Contains(b, fakeLLMKey) {
					t.Error("the LLM key reached the provider")
				}
			}
			for _, h := range g.authHeaders() {
				if strings.Contains(h, fakeLLMKey) || strings.Contains(h, fakeBitbkt) {
					t.Error("a foreign secret reached the provider")
				}
			}
		})
	}
}

// TestSDKLogsCarryNoArgumentsOrResults [canary]: with log.level=debug, tool
// arguments and results carrying markers pass through the real MCP server;
// the capture of everything logged (the SDK's records and ours) contains no
// marker. It covers pr_review, pr_ask and pr_comments.
func TestSDKLogsCarryNoArgumentsOrResults(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	deps := realDeps(reviewEnv(g, l), logger)
	fp := &fakeProvider{threads: sampleThreads()}
	deps2, _ := withFake(depsFor(validEnv(), logger), fp)

	// pr_review through the real wiring, markers in every argument.
	cs := connect(t, deps)
	rv := callTool(t, cs, "pr_review", map[string]any{
		"pr_url": reviewPRURL(g) + "?x=" + argMarker, "extra_instructions": argMarker, "output_language": "tr-TR",
	})
	if rv.IsError {
		t.Fatalf("pr_review: %s", textOf(t, rv))
	}
	if !strings.Contains(textOf(t, rv), headerMarker) || !strings.Contains(textOf(t, rv), titleMarker) {
		t.Fatal("the answer and title markers must reach the result, or the log check is vacuous")
	}
	// A rejected call with markers in arguments.
	bad := callTool(t, cs, "pr_review", map[string]any{"pr_url": argMarker, "output_language": "bad " + argMarker})
	if !bad.IsError {
		t.Fatal("expected a tool error")
	}
	// pr_ask through the real wiring, markers in every argument and in the
	// question; the fake model's answer carries a marker too.
	ak := callTool(t, cs, "pr_ask", map[string]any{
		"pr_url": reviewPRURL(g) + "?x=" + argMarker, "question": questionMarker, "extra_instructions": argMarker,
		"output_language": "tr-TR",
	})
	if ak.IsError {
		t.Fatalf("pr_ask: %s", textOf(t, ak))
	}
	if !strings.Contains(textOf(t, ak), headerMarker) || !strings.Contains(textOf(t, ak), questionMarker) {
		t.Fatal("the answer and question markers must reach the pr_ask result, or the log check is vacuous")
	}
	badAsk := callTool(t, cs, "pr_ask", map[string]any{"pr_url": argMarker, "question": questionMarker + " ", "output_language": "bad " + argMarker})
	if !badAsk.IsError {
		t.Fatal("expected a tool error from pr_ask")
	}
	// A second tool, with markers in its arguments and in its result.
	cs2 := connect(t, deps2)
	cm := callTool(t, cs2, "pr_comment_reply", map[string]any{"pr_url": prURL, "comment_id": "1", "body": argMarker})
	pc := callTool(t, cs2, "pr_comments", map[string]any{"pr_url": prURL, "include_resolved": true})
	if cm.IsError || pc.IsError || !strings.Contains(textOf(t, pc), bodyMarker) {
		t.Fatalf("setup: %v %v", cm.IsError, pc.IsError)
	}

	logText := logs.String()
	if !strings.Contains(logText, "level=DEBUG") || !strings.Contains(logText, "server session connected") {
		t.Fatalf("the capture holds no SDK records; the check would be vacuous:\n%s", logText)
	}
	for _, m := range []string{argMarker, bodyMarker, descMarker, answerMarker, authorMarker, pathMarker,
		titleMarker, branchMarker, diffMarker, headerMarker, contentMarker, securityMarker, questionMarker, askAnswerMarker} {
		if strings.Contains(logText, m) {
			t.Errorf("marker %q appears in the log capture", m)
		}
	}
}

// TestSDKLogFilter drops everything but identifiers: attributes that could
// hold arguments or results, and the error text of the SDK's one record that
// formats a result (jsonrpc2 internal error, internal/jsonrpc2/conn.go:700).
func TestSDKLogFilter(t *testing.T) {
	var logs syncBuffer
	base := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sdk := sdkLogger(base, false)
	sdk.Error("jsonrpc2 internal error", "error", fmt.Errorf("handler returned a result: %#v", map[string]string{"k": argMarker}))
	sdk.Debug("a debug record", "arguments", argMarker, "result", bodyMarker, "session_id", "abc")
	sdk.With("bound", argMarker).Info("bound attr", "request_id", "7")
	sdk.WithGroup("g").Info("grouped", "uri", "file:///x")
	sdk.Warn("calling notifications/x: boom", "error", "boom")
	out := logs.String()
	for _, m := range []string{argMarker, bodyMarker} {
		if strings.Contains(out, m) {
			t.Errorf("filtered log still contains %q:\n%s", m, out)
		}
	}
	for _, want := range []string{"jsonrpc2 internal error", "details withheld", "session_id=abc", "request_id=7", "uri=file:///x"} {
		if !strings.Contains(out, want) {
			t.Errorf("filtered log lacks %q:\n%s", want, out)
		}
	}
	if sdkLogger(nil, false) != nil || sdkLogger(nil, true) != nil {
		t.Error("a nil logger must stay nil")
	}
}
