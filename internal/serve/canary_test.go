package serve

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/credentials"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// TestNoCrossTalkBetweenConcurrentCalls: [canary] (P6 §1.6 #1). 32
// concurrent pr_review calls go through the real HTTP server, middleware,
// ConfigFor, resolver, provider and LLM client. Each carries its own Gitea
// token and LLM key, paired with its own PR number. The fakes must see every
// credential only together with its paired PR.
func TestNoCrossTalkBetweenConcurrentCalls(t *testing.T) {
	const n = 32
	ts := startServer(t, serverOpts{env: map[string]string{"REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS": "32"}, level: slog.LevelInfo})

	// Hold the first provider request of every call until all 32 have
	// arrived, so the calls really overlap inside the handler.
	var arrived sync.WaitGroup
	arrived.Add(n)
	release := make(chan struct{})
	var once sync.Once
	go func() {
		arrived.Wait()
		once.Do(func() { close(release) })
	}()
	timedOut := make(chan struct{})
	go func() {
		select {
		case <-release:
		case <-time.After(20 * time.Second):
			close(timedOut)
			once.Do(func() { close(release) })
		}
	}()
	var seenMu sync.Mutex
	seen := map[int]bool{}
	hook := func(pr int) {
		seenMu.Lock()
		first := !seen[pr]
		seen[pr] = true
		seenMu.Unlock()
		if first {
			arrived.Done()
		}
		<-release
	}
	ts.gitea.onPR.Store(&hook)

	tok := func(i int) string { return fmt.Sprintf("gitea-token-%02d", i) }
	key := func(i int) string { return fmt.Sprintf("llm-key-%02d", i) }
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := ts.post(t, PathMCP, creds(tok(i), key(i)), callBody(i, "pr_review", map[string]any{"pr_url": ts.prURL(i)}))
			if resp.status != http.StatusOK {
				errs <- fmt.Sprintf("call %d: status %d", i, resp.status)
				return
			}
			var r toolResult
			m := rpcMessage(t, resp.body)
			if err := json.Unmarshal(m["result"], &r); err != nil || r.IsError {
				errs <- fmt.Sprintf("call %d: tool error %q", i, r.text())
				return
			}
			if !strings.Contains(r.text(), "Constant changed") {
				errs <- fmt.Sprintf("call %d: unexpected review text", i)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	select {
	case <-timedOut:
		t.Error("the 32 calls never overlapped (barrier timed out)")
	default:
	}

	check := func(who string, obs []observation, want func(int) string) {
		perPR := map[int]int{}
		for _, o := range obs {
			if o.pr < 1 || o.pr > n {
				t.Errorf("%s: request without a single identifiable PR (credential %q)", who, o.cred)
				continue
			}
			perPR[o.pr]++
			if o.cred != want(o.pr) {
				t.Errorf("%s: PR %d was requested with the credential of another call (%q)", who, o.pr, o.cred)
			}
		}
		for i := 1; i <= n; i++ {
			if perPR[i] == 0 {
				t.Errorf("%s: PR %d never seen", who, i)
			}
		}
	}
	check("gitea", ts.gitea.observations(), tok)
	check("llm", ts.llm.observations(), key)
}

// TestMissingGiteaHeaderFailsBeforeAnyIO: [canary] (P6 §1.6 #3).
func TestMissingGiteaHeaderFailsBeforeAnyIO(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})

	for _, tool := range []string{"pr_review", "pr_ask", "pr_comments", "pr_comment_create", "pr_comment_create inline"} {
		args := map[string]any{"pr_url": ts.prURL(3)}
		switch tool {
		case "pr_ask":
			args["question"] = "Why?"
		case "pr_comment_create":
			args["body"] = "x"
		case "pr_comment_create inline":
			args["body"], args["file"], args["line"] = "x", "src/app.go", 2
			tool = "pr_comment_create"
		}
		_, r := ts.callTool(t, creds("", "llm-key"), tool, args)
		if !r.IsError || r.text() != tools.MissingGiteaTokenMessage {
			t.Errorf("%s without the Gitea header: isError=%v text=%q", tool, r.IsError, r.text())
		}
	}
	if w, _ := ts.gitea.written(); len(w) != 0 {
		t.Errorf("write requests without a credential: %v", w)
	}
	// The LLM key is checked too, after the provider token.
	_, r := ts.callTool(t, creds("gitea-token", ""), "pr_review", map[string]any{"pr_url": ts.prURL(3)})
	if !r.IsError || r.text() != tools.MissingLLMAPIKeyMessage {
		t.Errorf("pr_review without the LLM header: %q", r.text())
	}
	if h := ts.gitea.hits.Load(); h != 0 {
		t.Errorf("the fake provider saw %d requests, want 0", h)
	}
	if h := ts.llm.hits.Load(); h != 0 {
		t.Errorf("the fake LLM saw %d requests, want 0", h)
	}
	// URL resolution comes first: a foreign URL is url_not_configured even
	// without any credential.
	_, r = ts.callTool(t, nil, "pr_comments", map[string]any{"pr_url": "https://other.example/octo/demo/pulls/1"})
	if !r.IsError || !strings.HasPrefix(r.text(), "the pull request URL does not match any configured provider") {
		t.Errorf("foreign URL: %q", r.text())
	}
	// With the header the same call goes through.
	_, r = ts.callTool(t, creds("gitea-token", ""), "pr_comments", map[string]any{"pr_url": ts.prURL(3)})
	if r.IsError || ts.gitea.hits.Load() == 0 {
		t.Errorf("pr_comments with the header: %q", r.text())
	}
}

// TestOriginAndHostChecks: [canary] (P6 §1.6 #5).
func TestOriginAndHostChecks(t *testing.T) {
	ts := startServer(t, serverOpts{
		env:   map[string]string{"REVIEW_MCP_SERVE_ALLOWED_ORIGINS": "https://app.example.com"},
		level: slog.LevelInfo,
	})
	body := callBody(1, "pr_comments", map[string]any{"pr_url": ts.prURL(1)})
	good := creds("gitea-token", "")

	rejected := []struct {
		name string
		hdr  map[string]string
		want string
	}{
		{"foreign origin", mergeHdr(good, map[string]string{"Origin": "https://attacker.example"}), BodyForbiddenOrigin},
		{"origin with path", mergeHdr(good, map[string]string{"Origin": "https://app.example.com/"}), BodyForbiddenOrigin},
		{"null origin", mergeHdr(good, map[string]string{"Origin": "null"}), BodyForbiddenOrigin},
		{"rebinding host", mergeHdr(good, map[string]string{"Host": "rebind.example"}), BodyForbiddenHost},
		{"rebinding host with port", mergeHdr(good, map[string]string{"Host": "rebind.example:" + ts.port}), BodyForbiddenHost},
		{"loopback host, wrong port", mergeHdr(good, map[string]string{"Host": "127.0.0.1:1"}), BodyForbiddenHost},
		{"loopback host, no port", mergeHdr(good, map[string]string{"Host": "localhost"}), BodyForbiddenHost},
	}
	for _, tc := range rejected {
		resp := ts.post(t, PathMCP, tc.hdr, body)
		if resp.status != http.StatusForbidden || resp.body != tc.want {
			t.Errorf("%s: status %d body %q", tc.name, resp.status, resp.body)
		}
	}
	if n := ts.resolves.Load(); n != 0 {
		t.Errorf("a tool ran %d times behind a rejected request", n)
	}
	if h := ts.gitea.hits.Load(); h != 0 {
		t.Errorf("the fake provider saw %d requests", h)
	}

	// Accepted: no Origin, a listed Origin, and every loopback spelling of
	// the Host with the right port.
	for _, hdr := range []map[string]string{
		good,
		mergeHdr(good, map[string]string{"Origin": "https://app.example.com"}),
		mergeHdr(good, map[string]string{"Host": "localhost:" + ts.port}),
		mergeHdr(good, map[string]string{"Host": "[::1]:" + ts.port}),
		mergeHdr(good, map[string]string{"Host": "127.0.0.2:" + ts.port}),
	} {
		resp := ts.post(t, PathMCP, hdr, body)
		if resp.status != http.StatusOK {
			t.Errorf("accepted request %v: status %d body %q", hdr, resp.status, resp.body)
		}
	}
	if ts.resolves.Load() != 5 {
		t.Errorf("accepted tool calls = %d, want 5", ts.resolves.Load())
	}
}

// TestAccessToken: [canary] (P6 §1.6 #6).
func TestAccessToken(t *testing.T) {
	const access = "serve-access-token-1234"
	ts := startServer(t, serverOpts{env: map[string]string{"REVIEW_MCP_SERVE_ACCESS_TOKEN": access}, level: slog.LevelInfo})
	body := callBody(1, "pr_comments", map[string]any{"pr_url": ts.prURL(1)})
	good := creds("gitea-token", "")

	for name, auth := range map[string]string{
		"missing":       "",
		"wrong":         "Bearer serve-access-token-1235",
		"prefix":        "Bearer serve-access-token-123",
		"longer":        "Bearer " + access + "5",
		"wrong scheme":  "Basic " + access,
		"no scheme":     access,
		"empty bearer":  "Bearer ",
		"gitea scheme":  "token " + access,
		"bearer, extra": "Bearer " + access + " x",
	} {
		hdr := good
		if auth != "" {
			hdr = mergeHdr(good, map[string]string{"Authorization": auth})
		}
		resp := ts.post(t, PathMCP, hdr, body)
		if resp.status != http.StatusUnauthorized || resp.body != BodyUnauthorized || resp.header.Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: status %d body %q WWW-Authenticate %q", name, resp.status, resp.body, resp.header.Get("WWW-Authenticate"))
		}
		if strings.Contains(resp.body, access) {
			t.Errorf("%s: token echoed", name)
		}
	}
	if n := ts.resolves.Load(); n != 0 || ts.gitea.hits.Load() != 0 {
		t.Errorf("a tool ran behind a rejected token (resolves %d, provider hits %d)", n, ts.gitea.hits.Load())
	}
	for _, auth := range []string{"Bearer " + access, "bearer " + access, "Bearer   " + access + " "} {
		resp := ts.post(t, PathMCP, mergeHdr(good, map[string]string{"Authorization": auth}), body)
		if resp.status != http.StatusOK {
			t.Errorf("%q: status %d", auth, resp.status)
		}
	}
	if ts.resolves.Load() != 3 {
		t.Errorf("accepted tool calls = %d, want 3", ts.resolves.Load())
	}
	// /healthz stays unauthenticated.
	if r := do(t, mustReq(t, http.MethodGet, ts.url+PathHealth)); r.status != http.StatusOK || r.body != BodyHealth {
		t.Errorf("healthz: %d %q", r.status, r.body)
	}
}

// TestEndToEndNoCredentialLeak: [canary] (P6 §1.6 #7). Debug logging, every
// credential header a distinct marker, a provider error that echoes every
// marker in its body, and server_info: no marker may appear in the logs, in
// any HTTP response body or in any error text.
func TestEndToEndNoCredentialLeak(t *testing.T) {
	const (
		giteaMarker  = "GITEA-MARKER-c41f9e"
		bbsMarker    = "BBS-MARKER-c41f9e"
		llmMarker    = "LLM-MARKER-c41f9e"
		accessMarker = "ACCESS-MARKER-c41f9e"
	)
	markers := []string{giteaMarker, bbsMarker, llmMarker, accessMarker}
	ts := startServer(t, serverOpts{
		env: map[string]string{
			"REVIEW_MCP_SERVE_ACCESS_TOKEN":        accessMarker,
			"REVIEW_MCP_LOG_LEVEL":                 "debug",
			"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com/bb",
		},
		level: slog.LevelDebug,
	})
	ts.gitea.leakBody = "denied " + strings.Join(markers, " ")
	ts.gitea.failStatus.Store(http.StatusInternalServerError)

	hdr := map[string]string{
		credentials.HeaderGiteaToken:           giteaMarker,
		credentials.HeaderBitbucketServerToken: bbsMarker,
		credentials.HeaderLLMAPIKey:            llmMarker,
		"Authorization":                        "Bearer " + accessMarker,
	}
	var bodies []string
	collect := func(r response) { bodies = append(bodies, r.body, fmt.Sprint(r.header)) }

	resp, r := ts.callTool(t, hdr, "pr_review", map[string]any{"pr_url": ts.prURL(4)})
	collect(resp)
	if !r.IsError || !strings.HasPrefix(r.text(), "the server reported an internal error") {
		t.Errorf("pr_review with a failing provider: %q", r.text())
	}
	resp, r = ts.callTool(t, hdr, "pr_ask", map[string]any{"pr_url": ts.prURL(4), "question": "Why?"})
	collect(resp)
	if !r.IsError {
		t.Error("pr_ask with a failing provider succeeded")
	}
	resp, r = ts.callTool(t, hdr, "pr_comments", map[string]any{"pr_url": ts.prURL(4)})
	collect(resp)
	if !r.IsError {
		t.Error("pr_comments with a failing provider succeeded")
	}
	resp, r = ts.callTool(t, hdr, "server_info", map[string]any{})
	collect(resp)
	if r.IsError || !strings.Contains(r.text(), "X-Review-MCP-Gitea-Token") {
		t.Errorf("server_info: %q", r.text())
	}
	// Rejections: a wrong token, a malformed header carrying a marker.
	collect(ts.post(t, PathMCP, mergeHdr(hdr, map[string]string{"Authorization": "Bearer " + accessMarker + "x"}), callBody(1, "server_info", nil)))
	collect(ts.post(t, PathMCP, mergeHdr(hdr, map[string]string{credentials.HeaderGiteaToken: giteaMarker + "\u00e9"}), callBody(1, "server_info", nil)))
	collect(ts.post(t, "/other", hdr, "{}"))

	if ts.gitea.hits.Load() == 0 {
		t.Fatal("the provider error was never forced; the check would be vacuous")
	}
	logs := ts.logs.String()
	if !strings.Contains(logs, "level=DEBUG") || !strings.Contains(logs, `msg="http request"`) {
		t.Fatalf("the log capture holds no debug or access lines; the check would be vacuous:\n%s", logs)
	}
	for _, m := range markers {
		if strings.Contains(logs, m) {
			t.Errorf("marker %s in the logs", m)
		}
		for i, b := range bodies {
			if strings.Contains(b, m) {
				t.Errorf("marker %s in response %d: %q", m, i, b)
			}
		}
	}
}

func mustReq(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// TestCommentCreateUsesThePerCallToken: pr_comment_create needs the provider
// token of its own request and no LLM key; the comment is posted with that
// token only.
func TestCommentCreateUsesThePerCallToken(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})

	for i, tok := range []string{"gitea-token-A", "gitea-token-B"} {
		pr := 5 + i
		_, r := ts.callTool(t, creds(tok, ""), "pr_comment_create", map[string]any{"pr_url": ts.prURL(pr), "body": "note " + tok})
		if r.IsError || !strings.HasPrefix(r.text(), "Comment posted on the pull request.") {
			t.Fatalf("PR-level call %d: isError=%v text=%q", i, r.IsError, r.text())
		}
		_, r = ts.callTool(t, creds(tok, ""), "pr_comment_create",
			map[string]any{"pr_url": ts.prURL(pr), "body": "line " + tok, "file": "src/app.go", "line": 2})
		if r.IsError || !strings.HasPrefix(r.text(), "Comment posted on the line.") {
			t.Fatalf("inline call %d: isError=%v text=%q", i, r.IsError, r.text())
		}
	}
	for _, o := range ts.gitea.observations() {
		if want := map[int]string{5: "gitea-token-A", 6: "gitea-token-B"}[o.pr]; o.cred != want {
			t.Errorf("PR %d was requested with %q, want %q", o.pr, o.cred, want)
		}
	}
	if h := ts.llm.hits.Load(); h != 0 {
		t.Errorf("the LLM saw %d requests for a comment call", h)
	}
	// A line outside the diff is refused and posts nothing more.
	before, _ := ts.gitea.written()
	_, r := ts.callTool(t, creds("gitea-token-A", ""), "pr_comment_create",
		map[string]any{"pr_url": ts.prURL(5), "body": "x", "file": "src/app.go", "line": 9})
	if after, _ := ts.gitea.written(); !r.IsError || r.text() != tools.NotInDiffMessage || len(after) != len(before) {
		t.Errorf("out-of-diff call: isError=%v text=%q, writes %d -> %d", r.IsError, r.text(), len(before), len(after))
	}
}

// TestEndToEndCommentContentNeverLogged: [canary] (P7 §6.4). Debug logging,
// every secret a marker, and four content markers: a thread body, the
// model's finding (inline comment), the model's security text (overview)
// and the body of pr_comment_create. Each reaches the place it is meant
// for; none reaches a log line, an HTTP response header or an error text,
// and no secret appears anywhere.
func TestEndToEndCommentContentNeverLogged(t *testing.T) {
	const (
		giteaMarker    = "GITEA-MARKER-5b7d21"
		llmMarker      = "LLM-MARKER-5b7d21"
		createMarker   = "CREATE-BODY-MARKER-5b7d21"
		threadMarker   = "THREAD-MARKER-5b7d21"
		inlineMarker   = "INLINE-MARKER-5b7d21"
		overviewMarker = "OVERVIEW-MARKER-5b7d21"
	)
	ts := startServer(t, serverOpts{level: slog.LevelDebug, env: map[string]string{"REVIEW_MCP_LOG_LEVEL": "debug"}})
	answer := "```yaml\nreview:\n  estimated_effort_to_review: 2\n  relevant_tests: \"No\"\n" +
		"  key_issues_to_review:\n    - relevant_file: src/app.go\n      issue_header: Constant changed\n" +
		"      issue_content: The constant changed. " + inlineMarker + "\n      start_line: 2\n      end_line: 2\n" +
		"  security_concerns: Possible exposure. " + overviewMarker + "\n  performance_concerns: \"No\"\n```\n"
	ts.llm.answer.Store(&answer)
	ts.gitea.plant("alice", 7, "Please double check the constant. "+threadMarker)
	hdr := creds(giteaMarker, llmMarker)

	var surfaces []string
	collect := func(resp response, r toolResult) toolResult {
		surfaces = append(surfaces, resp.body, fmt.Sprint(resp.header), r.text())
		return r
	}
	pr := ts.prURL(4)
	rev := collect(ts.callTool(t, hdr, "pr_review", map[string]any{"pr_url": pr, "publish": true}))
	prLevel := collect(ts.callTool(t, hdr, "pr_comment_create", map[string]any{"pr_url": pr, "body": "note " + createMarker}))
	inline := collect(ts.callTool(t, hdr, "pr_comment_create", map[string]any{"pr_url": pr, "body": "line " + createMarker, "file": "src/app.go", "line": 3}))
	refused := collect(ts.callTool(t, hdr, "pr_comment_create", map[string]any{"pr_url": pr, "body": createMarker, "file": "src/app.go", "line": 40}))
	invalid := collect(ts.callTool(t, hdr, "pr_comment_create", map[string]any{"pr_url": pr, "body": createMarker + "\n[//]: # (review-mcp:overview:v1)"}))
	if rev.IsError || prLevel.IsError || inline.IsError || !refused.IsError || !invalid.IsError {
		t.Fatalf("unexpected results: review=%v prLevel=%v inline=%v refused=%v invalid=%v",
			rev.IsError, prLevel.IsError, inline.IsError, refused.IsError, invalid.IsError)
	}
	// A provider error that echoes every marker.
	ts.gitea.leakBody = "denied " + strings.Join([]string{giteaMarker, llmMarker, createMarker}, " ")
	ts.gitea.failStatus.Store(http.StatusInternalServerError)
	failed := collect(ts.callTool(t, hdr, "pr_comment_create", map[string]any{"pr_url": pr, "body": createMarker}))
	if !failed.IsError {
		t.Fatal("the failing provider call succeeded")
	}

	// The markers reached the places they are meant for; otherwise the log
	// check below proves nothing.
	if o := ts.llm.observations(); len(o) == 0 {
		t.Fatal("the LLM received no request")
	}
	_, bodies := ts.gitea.written()
	sent := strings.Join(bodies, "\n")
	for what, m := range map[string]string{"inline finding": inlineMarker, "overview": overviewMarker, "create body": createMarker} {
		if !strings.Contains(sent, m) {
			t.Fatalf("the %s marker never reached the provider", what)
		}
	}
	logs := ts.logs.String()
	if !strings.Contains(logs, "level=DEBUG") || !strings.Contains(logs, "pr_comment_create") {
		t.Fatalf("debug logging did not run; the leak check would be vacuous:\n%s", logs)
	}
	surfaces = append(surfaces, logs)
	// The first three surfaces are the review call's own response, which
	// carries the model's words as its result.
	for i, s := range surfaces {
		for _, m := range []string{createMarker, threadMarker, inlineMarker, overviewMarker, giteaMarker, llmMarker} {
			if i < 3 && (m == inlineMarker || m == overviewMarker) {
				continue
			}
			if strings.Contains(s, m) {
				t.Errorf("marker %q leaked into surface %d: %.200q", m, i, s)
			}
		}
	}
}
