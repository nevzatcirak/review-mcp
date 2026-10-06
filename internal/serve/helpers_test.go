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
}

func newFakeGitea(t *testing.T) *fakeGitea {
	t.Helper()
	f := &fakeGitea{arrived: make(chan struct{}, 1024)}
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
	_, _ = io.Copy(io.Discard, r.Body)
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
	switch {
	case strings.HasPrefix(p, giteaAPI+"/raw/"):
		_, _ = io.WriteString(w, "package main\nvar a = 2\nfunc main() {}\n")
	case strings.HasPrefix(p, giteaAPI+"/issues/") && strings.HasSuffix(p, "/comments"):
		// pr_comments: no PR-level comments.
		f.recordIssue(cred, p)
		_, _ = io.WriteString(w, "[]")
	case strings.HasPrefix(p, giteaAPI+"/pulls/") && strings.HasSuffix(p, "/reviews"):
		_, _ = io.WriteString(w, "[]")
	default:
		http.NotFound(w, r)
	}
}

var issuePathRE = regexp.MustCompile(`/issues/(\d+)/comments$`)

func (f *fakeGitea) recordIssue(cred, p string) {
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

	mu  sync.Mutex
	obs []observation
}

func newFakeLLM(t *testing.T) *fakeLLM {
	t.Helper()
	f := &fakeLLM{}
	f.srv, f.conns = startCounted(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": goodAnswer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
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
	env             map[string]string
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
