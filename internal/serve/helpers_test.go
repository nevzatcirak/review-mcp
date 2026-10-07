package serve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/credentials"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// syncBuffer is a goroutine-safe log sink standing in for stderr.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// ---- fake Gitea --------------------------------------------------------

const giteaAPI = "/api/v1/repos/octo/demo"

var prPathRE = regexp.MustCompile(`^` + giteaAPI + `/pulls/(\d+)(\.diff|/files)?$`)

// observation is one request seen by a fake: the credential it carried and
// the pull request number it was about.
type observation struct {
	cred string
	pr   int
}

// fakeGitea serves octo/demo pull requests 1..N. Each PR's title carries
// "PR-<n>-TITLE" so the LLM request can be tied back to its PR.
type fakeGitea struct {
	srv   *httptest.Server
	conns *connCounter
	hits  atomic.Int64

	mu  sync.Mutex
	obs []observation

	// failStatus answers everything with that status and a body carrying
	// leakBody.
	failStatus atomic.Int32
	leakBody   string
	// onRequest, when set, runs first for every request (tests use it to
	// hold requests).
	onRequest atomic.Pointer[func()]
	// arrived counts requests that reached the handler.
	arrived chan struct{}
	// onPR, when set, runs before the PR metadata request of PR n is
	// answered.
	onPR atomic.Pointer[func(n int)]

	// The write surface (WP-PR-7f): PR-level comments (id -> comment) and
	// the review comments posted, all in memory. wmu guards them and
	// writes, the "METHOD path" of every non-GET request.
	wmu      sync.Mutex
	comments map[int]fakeComment
	nextID   int
	reviews  []map[string]any
	writes   []string
	bodies   []string
}

// fakeComment is a PR-level comment of the fake Gitea.
type fakeComment struct {
	body, login string
	userID      int
}

func newFakeGitea(t *testing.T) *fakeGitea {
	t.Helper()
	f := &fakeGitea{arrived: make(chan struct{}, 1024), comments: map[int]fakeComment{}, nextID: 50}
	f.srv, f.conns = startCounted(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitea) observations() []observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]observation(nil), f.obs...)
}

func prTitle(n int) string { return fmt.Sprintf("PR-%02d-TITLE", n) }

const prDiff = `diff --git a/src/app.go b/src/app.go
index 1111111..2222222 100644
--- a/src/app.go
+++ b/src/app.go
@@ -1,3 +1,3 @@
 package main
-var a = 1
+var a = 2
 func main() {}
`

func (f *fakeGitea) serve(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	select {
	case f.arrived <- struct{}{}:
	default:
	}
	if h := f.onRequest.Load(); h != nil {
		(*h)()
	}
	raw, _ := io.ReadAll(r.Body)
	if r.Method != http.MethodGet {
		f.wmu.Lock()
		f.writes = append(f.writes, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, string(raw))
		f.wmu.Unlock()
	}
	if st := int(f.failStatus.Load()); st != 0 {
		http.Error(w, f.leakBody, st)
		return
	}
	cred := strings.TrimPrefix(r.Header.Get("Authorization"), "token ")
	writeJ := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	p := r.URL.Path
	if m := prPathRE.FindStringSubmatch(p); m != nil {
		n, _ := strconv.Atoi(m[1])
		f.mu.Lock()
		f.obs = append(f.obs, observation{cred: cred, pr: n})
		f.mu.Unlock()
		switch m[2] {
		case "":
			if h := f.onPR.Load(); h != nil {
				(*h)(n)
			}
			writeJ(map[string]any{
				"title": prTitle(n), "body": "Description of " + prTitle(n), "state": "open",
				"html_url": "https://your-gitea.example/octo/demo/pulls/" + m[1], "merge_base": "mergesha",
				"user": map[string]any{"login": "alice"},
				"head": map[string]any{"ref": "feature", "sha": "headsha"},
				"base": map[string]any{"ref": "main", "sha": "basesha"},
			})
		case ".diff":
			_, _ = io.WriteString(w, prDiff)
		case "/files":
			if r.URL.Query().Get("page") != "1" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			writeJ([]any{map[string]any{"filename": "src/app.go", "status": "changed", "additions": 1, "deletions": 1, "changes": 2}})
		}
		return
	}
	if f.serveWrites(w, r, cred, raw) {
		return
	}
	switch {
	case strings.HasPrefix(p, giteaAPI+"/raw/"):
		_, _ = io.WriteString(w, "package main\nvar a = 2\nfunc main() {}\n")
	case strings.HasPrefix(p, giteaAPI+"/pulls/") && strings.HasSuffix(p, "/reviews"):
		_, _ = io.WriteString(w, "[]")
	default:
		http.NotFound(w, r)
	}
}

const fakeWeb = "https://your-gitea.example/octo/demo/pulls/"

// plant adds a PR-level comment written by another account.
func (f *fakeGitea) plant(login string, userID int, body string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.nextID++
	f.comments[f.nextID] = fakeComment{body: body, login: login, userID: userID}
}

func (f *fakeGitea) written() (writes, bodies []string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	return append([]string(nil), f.writes...), append([]string(nil), f.bodies...)
}

// serveWrites answers the comment, user and review endpoints. It reports
// whether it handled the request. The token's user is "review-bot" (id 42).
func (f *fakeGitea) serveWrites(w http.ResponseWriter, r *http.Request, cred string, raw []byte) bool {
	p := r.URL.Path
	writeJ := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	asJSON := func(id int, c fakeComment) map[string]any {
		return map[string]any{"id": id, "type": "comment", "body": c.body,
			"user":     map[string]any{"id": c.userID, "login": c.login},
			"html_url": fakeWeb + "7#issuecomment-" + strconv.Itoa(id), "pull_request_url": fakeWeb + "7"}
	}
	f.wmu.Lock()
	defer f.wmu.Unlock()
	switch {
	case r.Method == "GET" && p == "/api/v1/user":
		writeJ(map[string]any{"id": 42, "login": "review-bot"})
	case strings.HasPrefix(p, giteaAPI+"/issues/comments/"):
		id, _ := strconv.Atoi(strings.TrimPrefix(p, giteaAPI+"/issues/comments/"))
		c, ok := f.comments[id]
		if !ok {
			http.NotFound(w, r)
			return true
		}
		if r.Method == "PATCH" {
			c.body, _ = body["body"].(string)
			f.comments[id] = c
		}
		writeJ(asJSON(id, c))
	case strings.HasPrefix(p, giteaAPI+"/issues/") && strings.HasSuffix(p, "/comments"):
		f.recordIssueLocked(cred, p)
		if r.Method == "POST" {
			text, _ := body["body"].(string)
			f.nextID++
			f.comments[f.nextID] = fakeComment{body: text, login: "review-bot", userID: 42}
			writeJ(asJSON(f.nextID, f.comments[f.nextID]))
			return true
		}
		out := []any{}
		if r.URL.Query().Get("page") == "1" {
			for id := 1; id <= f.nextID; id++ {
				if c, ok := f.comments[id]; ok {
					out = append(out, asJSON(id, c))
				}
			}
		}
		writeJ(out)
	case r.Method == "POST" && strings.HasPrefix(p, giteaAPI+"/pulls/") && strings.HasSuffix(p, "/reviews"):
		rid := 300 + len(f.reviews)
		var cs []any
		comments, _ := body["comments"].([]any)
		for i, c := range comments {
			cm, _ := c.(map[string]any)
			cid := rid*10 + i
			cs = append(cs, map[string]any{"id": cid, "path": cm["path"], "body": cm["body"], "position": cm["new_position"],
				"user": map[string]any{"id": 42, "login": "review-bot"}, "html_url": fakeWeb + "7/files#issuecomment-" + strconv.Itoa(cid)})
		}
		f.reviews = append(f.reviews, map[string]any{"id": rid, "state": "COMMENT", "comments": cs})
		writeJ(map[string]any{"id": rid, "state": "COMMENT", "html_url": fakeWeb + "7#pullrequestreview-" + strconv.Itoa(rid)})
	case r.Method == "GET" && strings.HasPrefix(p, giteaAPI+"/pulls/") && strings.Contains(p, "/reviews/") && strings.HasSuffix(p, "/comments"):
		out := []any{}
		for _, rv := range f.reviews {
			if r.URL.Query().Get("page") == "1" && strings.Contains(p, "/reviews/"+strconv.Itoa(rv["id"].(int))+"/") {
				out = append(out, rv["comments"].([]any)...)
			}
		}
		writeJ(out)
	default:
		return false
	}
	return true
}

var issuePathRE = regexp.MustCompile(`/issues/(\d+)/comments$`)

func (f *fakeGitea) recordIssueLocked(cred, p string) {
	if m := issuePathRE.FindStringSubmatch(p); m != nil {
		n, _ := strconv.Atoi(m[1])
		f.mu.Lock()
		f.obs = append(f.obs, observation{cred: cred, pr: n})
		f.mu.Unlock()
	}
}

// ---- fake LLM ----------------------------------------------------------

var titleRE = regexp.MustCompile(`PR-(\d+)-TITLE`)

const goodAnswer = "```yaml\nreview:\n  estimated_effort_to_review: 2\n  relevant_tests: \"No\"\n" +
	"  key_issues_to_review:\n    - relevant_file: src/app.go\n      issue_header: Constant changed\n" +
	"      issue_content: The constant changed without a test.\n      start_line: 2\n      end_line: 2\n" +
	"  security_concerns: \"No\"\n```\n"

// fakeLLM answers every chat completion with goodAnswer and records the key
// it was called with and the PR number found in the prompt.
type fakeLLM struct {
	srv   *httptest.Server
	conns *connCounter
	hits  atomic.Int64
	// answer, when set, replaces goodAnswer.
	answer atomic.Pointer[string]
	// models counts the GETs of the model list (the context-window probe)
	// and modelKeys records the bearer credential of each; modelsStatus,
	// when non-zero, answers them with that status instead of the list.
	models       atomic.Int64
	modelsStatus atomic.Int64
	modelKeys    []string

	mu  sync.Mutex
	obs []observation
}

func newFakeLLM(t *testing.T) *fakeLLM {
	t.Helper()
	f := &fakeLLM{}
	f.srv, f.conns = startCounted(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			f.models.Add(1)
			f.mu.Lock()
			f.modelKeys = append(f.modelKeys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			f.mu.Unlock()
			if st := int(f.modelsStatus.Load()); st != 0 {
				w.WriteHeader(st)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"example-model","max_model_len":40000}]}`))
			return
		}
		f.hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		pr := -1
		if m := titleRE.FindAllSubmatch(body, -1); len(m) > 0 {
			pr, _ = strconv.Atoi(string(m[0][1]))
			for _, x := range m[1:] { // more than one PR in one prompt is cross-talk too
				if y, _ := strconv.Atoi(string(x[1])); y != pr {
					pr = -2
				}
			}
		}
		f.mu.Lock()
		f.obs = append(f.obs, observation{cred: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), pr: pr})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": f.reply()}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLLM) reply() string {
	if a := f.answer.Load(); a != nil {
		return *a
	}
	return goodAnswer
}

func (f *fakeLLM) observations() []observation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]observation(nil), f.obs...)
}

// ---- server under test -------------------------------------------------

type testServer struct {
	url      string // http://127.0.0.1:port
	port     string
	logs     *syncBuffer
	gitea    *fakeGitea
	llm      *fakeLLM
	resolves *atomic.Int64 // tool calls that reached the resolver factory
	srv      *Server
	cancel   context.CancelFunc
	done     chan error
}

type serverOpts struct {
	env map[string]string
	// unset names environment variables removed from the defaults.
	unset           []string
	level           slog.Level
	shutdownTimeout time.Duration
}

// startServer runs the real serve stack (middleware, MCP handler, ConfigFor,
// wiring.NewResolver and wiring.NewLLM) on a loopback port against fresh
// fakes. The configuration goes through config.LoadWith in serve mode.
func startServer(t *testing.T, o serverOpts) *testServer {
	t.Helper()
	g, l := newFakeGitea(t), newFakeLLM(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"REVIEW_MCP_LLM_BASE_URL":       l.srv.URL + "/v1",
		"REVIEW_MCP_LLM_MODEL":          "example-model",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
		"REVIEW_MCP_LLM_MAX_RETRIES":    "0",
		"REVIEW_MCP_GITEA_BASE_URL":     g.srv.URL,
		"REVIEW_MCP_SERVE_LISTEN":       ln.Addr().String(),
	}
	for k, v := range o.env {
		env[k] = v
	}
	for _, k := range o.unset {
		delete(env, k)
	}
	cfg, rep, err := config.LoadWith(config.MemSource{Env: env}, config.LoadOptions{Mode: config.ModeServe})
	if err != nil {
		_ = ln.Close()
		t.Fatalf("serve config: %v", err)
	}
	logs := &syncBuffer{}
	level := o.level
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: level}))
	resolves := &atomic.Int64{}
	srv, err := New(Options{
		Config: cfg, Report: rep, Logger: logger,
		NewResolver: func(c *config.Config, lg *slog.Logger) *provider.Resolver {
			resolves.Add(1)
			return wiring.NewResolver(c, lg)
		},
		NewLLM:          wiring.NewLLM,
		ShutdownTimeout: o.shutdownTimeout,
	})
	if err != nil {
		_ = ln.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ts := &testServer{
		url: "http://" + ln.Addr().String(), logs: logs, gitea: g, llm: l, resolves: resolves,
		srv: srv, cancel: cancel, done: make(chan error, 1),
	}
	_, ts.port, _ = net.SplitHostPort(ln.Addr().String())
	go func() { ts.done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		// Unused pooled client connections would count as active for up to
		// 5 s during the drain (net/http treats a new connection as idle
		// only after 5 s).
		httpClient.CloseIdleConnections()
		cancel()
		select {
		case <-ts.done:
		case <-time.After(40 * time.Second):
			t.Error("server did not stop")
		}
	})
	return ts
}

func (ts *testServer) prURL(n int) string {
	return ts.gitea.srv.URL + "/octo/demo/pulls/" + strconv.Itoa(n)
}

// ---- raw MCP over HTTP -------------------------------------------------

// response is one HTTP exchange.
type response struct {
	status int
	header http.Header
	body   string
}

var httpClient = &http.Client{Timeout: 60 * time.Second}

// post sends body to path with the MCP headers plus hdr. A "Host" entry in
// hdr sets the request's Host.
func (ts *testServer) post(t *testing.T, path string, hdr map[string]string, body string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) response {
	t.Helper()
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

func callBody(id int, tool string, args map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	return string(b)
}

// toolResult is the decoded result of a tools/call.
type toolResult struct {
	IsError bool `json:"isError"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Structured json.RawMessage `json:"structuredContent"`
}

func (r toolResult) text() string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// rpcMessage extracts the JSON-RPC message of a response: the data line of
// an SSE stream, or a plain JSON body.
func rpcMessage(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	payload := strings.TrimSpace(body)
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			payload = d
		}
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatalf("response is not a JSON-RPC message: %q", body)
	}
	return m
}

// callTool runs a tools/call and returns the HTTP response and the decoded
// result. It fails the test on a non-200 status or a JSON-RPC error.
func (ts *testServer) callTool(t *testing.T, hdr map[string]string, tool string, args map[string]any) (response, toolResult) {
	t.Helper()
	resp := ts.post(t, PathMCP, hdr, callBody(1, tool, args))
	if resp.status != http.StatusOK {
		t.Fatalf("tools/call %s: status %d body %q", tool, resp.status, resp.body)
	}
	m := rpcMessage(t, resp.body)
	if e, ok := m["error"]; ok {
		t.Fatalf("tools/call %s: JSON-RPC error %s", tool, e)
	}
	var r toolResult
	if err := json.Unmarshal(m["result"], &r); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return resp, r
}

// creds builds the credential headers of one client.
func creds(gitea, llmKey string) map[string]string {
	h := map[string]string{}
	if gitea != "" {
		h[credentials.HeaderGiteaToken] = gitea
	}
	if llmKey != "" {
		h[credentials.HeaderLLMAPIKey] = llmKey
	}
	return h
}

func mergeHdr(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
