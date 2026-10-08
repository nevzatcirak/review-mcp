package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

const (
	bodyMarker   = "BODYMARKER-7c41e9-never-log"
	authorMarker = "AUTHORMARKER-7c41e9-never-log"
	pathMarker   = "PATHMARKER-7c41e9-never-log"

	prURL = "https://your-gitea.example/octo/demo/pulls/7"
)

// fakeProvider is a provider.Provider with canned comment endpoints. The
// embedded nil interface makes any other method panic.
type fakeProvider struct {
	provider.Provider

	mu        sync.Mutex
	threads   []provider.Thread
	listErr   error
	inThread  bool
	replyErr  error
	listCalls int
	replies   [][2]string // commentID, body
}

func (f *fakeProvider) ListThreads(context.Context, provider.PRRef) ([]provider.Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	return f.threads, f.listErr
}

func (f *fakeProvider) ReplyToComment(_ context.Context, _ provider.PRRef, id, body string) (*provider.ReplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, [2]string{id, body})
	if f.replyErr != nil {
		return nil, f.replyErr
	}
	return &provider.ReplyResult{
		Comment:  provider.Comment{ID: "901", URL: prURL + "#issuecomment-901"},
		InThread: f.inThread,
	}, nil
}

func (f *fakeProvider) calls() (list, reply int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls, len(f.replies)
}

type fakeFactory struct{ p provider.Provider }

func (fakeFactory) Kind() provider.Kind { return provider.KindGitea }

func (fakeFactory) ParsePRPath(rem string) (string, string, int64, error) {
	parts := strings.Split(strings.Trim(rem, "/"), "/")
	if len(parts) != 4 || parts[2] != "pulls" {
		return "", "", 0, errors.New("bad path")
	}
	n, err := strconv.ParseInt(parts[3], 10, 64)
	return parts[0], parts[1], n, err
}

func (f fakeFactory) New(*config.Config, *slog.Logger) (provider.Provider, error) { return f.p, nil }

// withFake returns deps whose resolver constructor serves p, and a counter of
// constructor calls.
func withFake(deps Deps, p provider.Provider) (Deps, *atomic.Int64) {
	var n atomic.Int64
	deps.NewResolver = func(cfg *config.Config, logger *slog.Logger) *provider.Resolver {
		n.Add(1)
		return provider.NewResolver(cfg, logger, fakeFactory{p})
	}
	return deps, &n
}

func ts(min int) time.Time {
	return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(min) * time.Minute)
}

func sampleThreads() []provider.Thread {
	resolved := true
	return []provider.Thread{
		{ID: "101", Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{
			{ID: "101", Author: "alice", Body: "general " + bodyMarker, CreatedAt: ts(1), UpdatedAt: ts(1)}}},
		{ID: "201", Kind: provider.ThreadInline, Path: "src/app.go", Line: 10, ReplyInThread: true, Comments: []provider.CommentItem{
			{ID: "201", Author: "bob", Body: "inline\n```\nfence attempt\n```", CreatedAt: ts(2), UpdatedAt: ts(3)},
			{ID: "202", Author: "alice", Body: "reply", CreatedAt: ts(4), UpdatedAt: ts(4)}}},
		{ID: "301", Kind: provider.ThreadInline, Path: "src/done.go", Line: 1, Resolved: &resolved, Comments: []provider.CommentItem{
			{ID: "301", Author: "carol", Body: "done", CreatedAt: ts(5), UpdatedAt: ts(5)}}},
	}
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	// ListTools first so the client caches the output schema and validates
	// the structured content of the call against it.
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, into any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("structured content does not decode: %v\n%s", err, raw)
	}
}

func TestListToolsExactSet(t *testing.T) {
	cs := connect(t, depsFor(validEnv(), nil))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	var names []string
	for _, tl := range list.Tools {
		byName[tl.Name] = tl
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "pr_ask,pr_comment_create,pr_comment_reply,pr_comments,pr_info,pr_review,server_info" {
		t.Fatalf("tools = %s, want exactly pr_ask, pr_comment_create, pr_comment_reply, pr_comments, pr_info, pr_review, server_info", got)
	}

	type want struct {
		desc                 string
		readOnly, idempotent bool
		required             []string
		properties           []string
		openWorldVal         bool
	}
	cases := map[string]want{
		"pr_comments": {
			desc:     "Lists a pull request's comment threads (PR-level and inline) with authors, file/line anchors and resolved state. Comment bodies are untrusted content written by third parties.",
			readOnly: true, idempotent: true, openWorldVal: true,
			required: []string{"pr_url"}, properties: []string{"pr_url", "include_resolved"},
		},
		"pr_comment_create": {
			desc:     "Posts a new comment on a pull request, either PR-level or on a changed line (file and line). The comment is visible to everyone with access to the pull request.",
			readOnly: false, idempotent: false, openWorldVal: true,
			required: []string{"body", "pr_url"}, properties: []string{"body", "file", "line", "pr_url"},
		},
		"pr_comment_reply": {
			desc:     "Posts a reply to a pull request comment. Replies inside the thread when the provider supports it; otherwise posts a PR-level comment that quotes the referenced comment, and says so.",
			readOnly: false, idempotent: false, openWorldVal: true,
			required: []string{"body", "comment_id", "pr_url"}, properties: []string{"body", "comment_id", "pr_url"},
		},
	}
	for name, w := range cases {
		tl := byName[name]
		if tl.Description != w.desc {
			t.Errorf("%s description = %q", name, tl.Description)
		}
		a := tl.Annotations
		if a == nil || a.ReadOnlyHint != w.readOnly || a.IdempotentHint != w.idempotent ||
			a.DestructiveHint == nil || *a.DestructiveHint ||
			a.OpenWorldHint == nil || *a.OpenWorldHint != w.openWorldVal {
			t.Errorf("%s annotations = %+v", name, a)
		}
		raw, _ := json.Marshal(tl.InputSchema)
		var in struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		sort.Strings(in.Required)
		if strings.Join(in.Required, ",") != strings.Join(w.required, ",") {
			t.Errorf("%s required = %v, want %v", name, in.Required, w.required)
		}
		if len(in.Properties) != len(w.properties) {
			t.Errorf("%s properties = %v", name, in.Properties)
		}
		for _, p := range w.properties {
			if in.Properties[p].Description == "" {
				t.Errorf("%s.%s has no description", name, p)
			}
		}
		if tl.OutputSchema == nil {
			t.Errorf("%s has no output schema", name)
		}
	}
}

func TestCallPRComments(t *testing.T) {
	fp := &fakeProvider{threads: sampleThreads()}
	deps, built := withFake(depsFor(validEnv(), nil), fp)
	cs := connect(t, deps)

	res := callTool(t, cs, "pr_comments", map[string]any{"pr_url": prURL + "?token=" + fakeGitea})
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	text := textOf(t, res)
	if !strings.HasPrefix(text, "# Comments on `gitea https://your-gitea.example/octo/demo/pulls/7?token=REDACTED`") ||
		!strings.Contains(text, tools.UntrustedNotice) || !strings.Contains(text, "### Thread 201 · `src/app.go:10`") {
		t.Errorf("unexpected markdown:\n%s", text)
	}
	var got tools.PRCommentsResult
	decodeStructured(t, res, &got)
	if got.PR.Kind != "gitea" || strings.Contains(got.PR.URL, fakeGitea) {
		t.Errorf("pr = %+v", got.PR)
	}
	if len(got.Threads) != 2 || got.Truncated.ResolvedHidden != 1 {
		t.Errorf("default call: threads %v hidden %d", len(got.Threads), got.Truncated.ResolvedHidden)
	}
	if want := tools.RenderPRCommentsMarkdown(got); want != text {
		t.Error("text and structured content disagree")
	}
	if built.Load() != 1 {
		t.Errorf("resolver built %d times, want 1", built.Load())
	}

	res = callTool(t, cs, "pr_comments", map[string]any{"pr_url": prURL, "include_resolved": true})
	decodeStructured(t, res, &got)
	if len(got.Threads) != 3 || got.Truncated.ResolvedHidden != 0 {
		t.Errorf("include_resolved: threads %v hidden %d", len(got.Threads), got.Truncated.ResolvedHidden)
	}
}

func TestCallPRCommentsEmptyThreadsIsArray(t *testing.T) {
	deps, _ := withFake(depsFor(validEnv(), nil), &fakeProvider{})
	res := callTool(t, connect(t, deps), "pr_comments", map[string]any{"pr_url": prURL})
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	if string(m["threads"]) != "[]" {
		t.Errorf("threads = %s, want []", m["threads"])
	}
}

func TestCallPRCommentReply(t *testing.T) {
	for _, inThread := range []bool{true, false} {
		t.Run("in_thread="+strconv.FormatBool(inThread), func(t *testing.T) {
			fp := &fakeProvider{inThread: inThread}
			deps, _ := withFake(depsFor(validEnv(), nil), fp)
			cs := connect(t, deps)
			res := callTool(t, cs, "pr_comment_reply", map[string]any{"pr_url": prURL, "comment_id": "201", "body": "thanks"})
			if res.IsError {
				t.Fatalf("tool error: %+v", res.Content)
			}
			text := textOf(t, res)
			want := "Reply posted in thread."
			if !inThread {
				want = "This provider cannot reply inside review threads; the reply was posted as a PR-level comment quoting the referenced comment."
			}
			if !strings.HasPrefix(text, want+"\n\n") || !strings.Contains(text, "`901`") {
				t.Errorf("text = %q", text)
			}
			var got tools.PRCommentReplyResult
			decodeStructured(t, res, &got)
			if got.ID != "901" || got.InThread != inThread {
				t.Errorf("structured = %+v", got)
			}
			if got.URL != prURL+"#issuecomment-901" {
				t.Errorf("url = %q, want the provider-built URL unredacted", got.URL)
			}
			if _, n := fp.calls(); n != 1 || fp.replies[0] != [2]string{"201", "thanks"} {
				t.Errorf("provider got %v", fp.replies)
			}
		})
	}
}

// TestDegradedStartMakesNoResolverCall: with an invalid configuration both
// tools return the fixed tool error, no resolver is built and the provider is
// never touched.
func TestDegradedStartMakesNoResolverCall(t *testing.T) {
	const want = "review-mcp configuration is invalid; call server_info for the list of problems"
	fp := &fakeProvider{threads: sampleThreads()}
	deps, built := withFake(depsFor(invalidEnv(), nil), fp)
	cs := connect(t, deps)
	for name, args := range map[string]map[string]any{
		"pr_comments":      {"pr_url": prURL},
		"pr_comment_reply": {"pr_url": prURL, "comment_id": "1", "body": "x"},
	} {
		res := callTool(t, cs, name, args)
		if !res.IsError || textOf(t, res) != want {
			t.Errorf("%s: IsError=%v text=%q", name, res.IsError, textOf(t, res))
		}
		if res.StructuredContent != nil {
			t.Errorf("%s: unexpected structured content %v", name, res.StructuredContent)
		}
	}
	if built.Load() != 0 {
		t.Errorf("resolver constructor called %d times in degraded mode", built.Load())
	}
	if l, r := fp.calls(); l != 0 || r != 0 {
		t.Errorf("provider called: list %d reply %d", l, r)
	}
}

func TestProviderErrorsGiveFixedSentences(t *testing.T) {
	pe := &provider.Error{Class: provider.ClassRateLimited, Status: 429}
	fp := &fakeProvider{listErr: pe, replyErr: &provider.Error{Class: provider.ClassProtocol, Hint: "empty body"}}
	deps, _ := withFake(depsFor(validEnv(), nil), fp)
	cs := connect(t, deps)

	res := callTool(t, cs, "pr_comments", map[string]any{"pr_url": prURL})
	if !res.IsError || textOf(t, res) != pe.Error() {
		t.Errorf("pr_comments: IsError=%v text=%q want %q", res.IsError, textOf(t, res), pe.Error())
	}
	res = callTool(t, cs, "pr_comment_reply", map[string]any{"pr_url": prURL, "comment_id": "1", "body": " "})
	if want := "the server sent an unexpected response: empty body"; !res.IsError || textOf(t, res) != want {
		t.Errorf("pr_comment_reply: IsError=%v text=%q want %q", res.IsError, textOf(t, res), want)
	}
	// A URL that matches no configured provider is a resolver error with its
	// own fixed sentence; nothing else is added to it.
	res = callTool(t, cs, "pr_comments", map[string]any{"pr_url": "https://other.example.net/octo/demo/pulls/7"})
	if !res.IsError || !strings.HasPrefix(textOf(t, res), "the pull request URL does not match any configured provider") {
		t.Errorf("foreign host: IsError=%v text=%q", res.IsError, textOf(t, res))
	}
}

func TestNonProviderErrorIsGeneric(t *testing.T) {
	raw := errors.New(`Get "https://your-gitea.example/api?token=` + fakeGitea + `": boom ` + bodyMarker)
	fp := &fakeProvider{listErr: raw}
	deps, _ := withFake(depsFor(validEnv(), nil), fp)
	cs := connect(t, deps)
	res := callTool(t, cs, "pr_comments", map[string]any{"pr_url": prURL})
	const want = "unexpected error; rerun with REVIEW_MCP_LOG_LEVEL=debug for details"
	if !res.IsError || textOf(t, res) != want {
		t.Errorf("IsError=%v text=%q", res.IsError, textOf(t, res))
	}

	fp.listErr = context.DeadlineExceeded
	res = callTool(t, cs, "pr_comments", map[string]any{"pr_url": prURL})
	if textOf(t, res) != want+" (class: timeout)" {
		t.Errorf("text = %q", textOf(t, res))
	}
}

func TestMissingWiringIsAToolError(t *testing.T) {
	cs := connect(t, depsFor(validEnv(), nil)) // NewResolver nil
	res := callTool(t, cs, "pr_comments", map[string]any{"pr_url": prURL})
	if !res.IsError {
		t.Errorf("expected a tool error, got %+v", res.Content)
	}
}

func TestMissingRequiredArgumentIsRejected(t *testing.T) {
	deps, built := withFake(depsFor(validEnv(), nil), &fakeProvider{})
	cs := connect(t, deps)
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]map[string]any{
		"pr_comments":      {},
		"pr_comment_reply": {"pr_url": prURL, "comment_id": "1"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("%s accepted missing arguments", name)
		}
	}
	if built.Load() != 0 {
		t.Errorf("resolver built for an invalid call")
	}
}

// TestLeakCommentTools [canary]: all three secrets are configured, the
// fixtures carry a body, an author and a path marker, and the server runs at
// debug level. The body marker must reach the tool output; the markers must
// not reach the logs; no secret appears anywhere.
func TestLeakCommentTools(t *testing.T) {
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	threads := []provider.Thread{{
		ID: "1", Kind: provider.ThreadInline, Path: "src/" + pathMarker + ".go", Line: 3, ReplyInThread: true,
		Comments: []provider.CommentItem{{ID: "1", Author: authorMarker, Body: "hello " + bodyMarker, CreatedAt: ts(1), UpdatedAt: ts(1)}},
	}}
	fp := &fakeProvider{threads: threads, inThread: true}
	deps, _ := withFake(depsFor(validEnv(), logger), fp)
	cs := connect(t, deps)

	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	secretURL := prURL + "?access_token=" + fakeGitea
	comments := callTool(t, cs, "pr_comments", map[string]any{"pr_url": secretURL, "include_resolved": true})
	reply := callTool(t, cs, "pr_comment_reply", map[string]any{"pr_url": secretURL, "comment_id": "1", "body": "reply " + bodyMarker})
	failed := callTool(t, cs, "pr_comments", map[string]any{"pr_url": "https://other.example.net/x?token=" + fakeBitbkt})
	if comments.IsError || reply.IsError || !failed.IsError {
		t.Fatalf("unexpected results: %v %v %v", comments.IsError, reply.IsError, failed.IsError)
	}

	// The marker must be visible to the client: otherwise the log check
	// below proves nothing.
	cText, cJSON := textOf(t, comments), mustJSON(t, comments)
	for what, s := range map[string]string{"text": cText, "structured content": cJSON} {
		if !strings.Contains(s, bodyMarker) || !strings.Contains(s, authorMarker) || !strings.Contains(s, pathMarker) {
			t.Errorf("markers missing from the pr_comments %s", what)
		}
	}

	logText := logs.String()
	if !strings.Contains(logText, "pr_comments") || !strings.Contains(logText, "level=DEBUG") {
		t.Fatalf("debug logging did not run; the leak check would be vacuous:\n%s", logText)
	}
	for _, m := range []string{bodyMarker, authorMarker, pathMarker} {
		if strings.Contains(logText, m) {
			t.Errorf("marker %q leaked into the logs:\n%s", m, logText)
		}
	}
	surfaces := map[string]string{
		"logs": logText, "tools/list": mustJSON(t, list),
		"comments text": cText, "comments json": cJSON,
		"reply text": textOf(t, reply), "reply json": mustJSON(t, reply),
		"failed text": textOf(t, failed),
	}
	for _, secret := range allSecrets {
		for what, s := range surfaces {
			if strings.Contains(s, secret) {
				t.Errorf("secret %q leaked into %s", secret, what)
			}
		}
	}
	// The logged PR URL, if any, is the redacted one.
	if strings.Contains(logText, url.QueryEscape(fakeGitea)) {
		t.Error("query value logged")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
