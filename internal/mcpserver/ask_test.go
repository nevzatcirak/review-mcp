package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	askrender "github.com/nevzatcirak/review-mcp/internal/ask/render"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

const (
	// questionMarker is placed in the question: it must reach the LLM request
	// body and the result, never the logs or an error.
	questionMarker = "QUESTION-MARKER-3e9b1f"
	// askAnswerMarker is placed in the fake model's answer: it reaches the
	// result, never the logs.
	askAnswerMarker = "ASKANSWER-MARKER-3e9b1f"
)

// askAnswer is a model answer with a quick-action line.
const askAnswer = "The constant changed from 1 to 2. " + askAnswerMarker + "\n/close\nDone."

const askQuestion = "Does this change break callers? " + questionMarker

// postedComments returns the bodies of the comments posted to the fake
// provider (the JSON "body" field of every POST to the comments endpoint).
func postedComments(t *testing.T, g *fakeServer) []string {
	t.Helper()
	var out []string
	for _, raw := range g.recorded() {
		var p struct {
			Body string `json:"body"`
		}
		if json.Unmarshal([]byte(raw), &p) == nil && p.Body != "" {
			out = append(out, p.Body)
		}
	}
	return out
}

func TestPRAskToolDefinition(t *testing.T) {
	cs := connect(t, realDeps(validEnv(), nil))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var tl *mcp.Tool
	for _, x := range list.Tools {
		if x.Name == "pr_ask" {
			tl = x
		}
	}
	if tl == nil {
		t.Fatal("pr_ask is not registered")
	}
	const want = "Answers a question about a pull request using the configured LLM, grounded in the PR's title, description and diff. Set publish=true to also post the question and answer as a PR comment. The PR content and the question are sent to the configured LLM endpoint."
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
	if got := strings.Join(props, ","); got != "extra_instructions,output_language,pr_url,publish,question" {
		t.Errorf("properties = %s", got)
	}
	sort.Strings(in.Required)
	if strings.Join(in.Required, ",") != "pr_url,question" {
		t.Errorf("required = %v, want pr_url and question", in.Required)
	}
	// The output schema describes the structured content.
	rawOut, _ := json.Marshal(tl.OutputSchema)
	var out struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(rawOut, &out); err != nil {
		t.Fatal(err)
	}
	var outProps []string
	for k := range out.Properties {
		outProps = append(outProps, k)
	}
	sort.Strings(outProps)
	if got := strings.Join(outProps, ","); got != "answer,coverage,metadata,notes,publish,question" {
		t.Errorf("output schema properties = %s", got)
	}
}

func TestPRAskCall(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, askAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))

	res := callTool(t, cs, "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "output_language": "tr-TR"})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	text := textOf(t, res)
	var got ask.Result
	decodeStructured(t, res, &got)
	if want := askrender.Client(&got); want != text {
		t.Errorf("text is not the client rendering of the structured result:\n%s\n---\n%s", text, want)
	}
	if !strings.HasPrefix(text, "## Question\n\n```\n"+askQuestion+"\n```\n\n## Answer\n\n") ||
		!strings.Contains(text, "## Coverage") || !strings.Contains(text, askAnswerMarker) {
		t.Errorf("unexpected markdown:\n%s", text)
	}
	// The client profile shows the model's answer as it is.
	if !strings.Contains(text, "\n/close\n") {
		t.Errorf("the client profile changed the answer:\n%s", text)
	}
	if got.Question != askQuestion || got.Answer != askAnswer || got.Metadata.LLMCalls != 1 || got.Publish != nil ||
		len(got.Coverage.Included) != 1 || got.Coverage.Included[0] != "src/app.go" {
		t.Errorf("structured = %+v", got)
	}

	bodies := l.recorded()
	if len(bodies) != 1 {
		t.Fatalf("LLM requests = %d, want 1", len(bodies))
	}
	for _, m := range []string{questionMarker, descMarker, titleMarker, branchMarker, diffMarker, "tr-TR"} {
		if !strings.Contains(bodies[0], m) {
			t.Errorf("%q did not reach the LLM request body", m)
		}
	}
	if h := l.authHeaders()[0]; h != "Bearer "+fakeLLMKey {
		t.Errorf("LLM Authorization header = %q", h)
	}
	for _, s := range []string{fakeGitea, fakeBitbkt, fakeLLMKey} {
		if strings.Contains(bodies[0], s) {
			t.Errorf("a secret reached the LLM request body")
		}
	}
	// Nothing was posted without publish.
	if c := postedComments(t, g); len(c) != 0 {
		t.Errorf("a comment was posted without publish: %q", c)
	}
}

// TestPRAskPublishSanitizesQuickActions [canary]: through the real wiring the
// published comment carries the answer's "\n/close" as "\n /close" and the
// question's slash lines too; the client text keeps the answer as it is.
func TestPRAskPublishSanitizesQuickActions(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, "/first\nFine.\n/close\r/assign me")
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	res := callTool(t, cs, "pr_ask", map[string]any{
		"pr_url": reviewPRURL(g), "question": "Why?\n/lgtm", "publish": true,
	})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	var got ask.Result
	decodeStructured(t, res, &got)
	if got.Publish == nil || !got.Publish.Published || got.Publish.CommentID != "55" {
		t.Errorf("publish = %+v", got.Publish)
	}
	posted := postedComments(t, g)
	if len(posted) != 1 {
		t.Fatalf("comments posted = %d, want 1", len(posted))
	}
	body := posted[0]
	for _, want := range []string{"### **Ask** ❓\n", "Why?\n /lgtm", "### **Answer:**\n /first\nFine.\n /close\r /assign me", "### 📂 Coverage"} {
		if !strings.Contains(body, want) {
			t.Errorf("published body lacks %q:\n%q", want, body)
		}
	}
	for _, line := range strings.FieldsFunc(body, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if strings.HasPrefix(line, "/") {
			t.Errorf("a published line starts with /: %q", line)
		}
	}
	if !strings.Contains(textOf(t, res), "\n/close\r/assign me") {
		t.Errorf("the client text changed the answer:\n%q", textOf(t, res))
	}
}

// TestPRAskValidation: invalid arguments are rejected with fixed sentences
// before any request to either fake.
func TestPRAskValidation(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, askAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty question", map[string]any{"question": ""}, ask.ErrQuestionEmpty.Error()},
		{"blank question", map[string]any{"question": " \t\n "}, ask.ErrQuestionEmpty.Error()},
		{"too long", map[string]any{"question": strings.Repeat("a", ask.MaxQuestionRunes) + "b" + questionMarker}, ask.ErrQuestionTooLong.Error()},
		{"too long runes", map[string]any{"question": strings.Repeat("é", ask.MaxQuestionRunes+1)}, ask.ErrQuestionTooLong.Error()},
		{"language", map[string]any{"question": "ok?", "output_language": "not a locale " + argMarker}, tools.InvalidOutputLanguageMessage},
		{"language underscore", map[string]any{"question": "ok?", "output_language": "tr_TR"}, tools.InvalidOutputLanguageMessage},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.args["pr_url"] = reviewPRURL(g)
			res := callTool(t, cs, "pr_ask", c.args)
			text := textOf(t, res)
			if !res.IsError || text != c.want {
				t.Errorf("IsError=%v text=%q, want %q", res.IsError, text, c.want)
			}
			for _, m := range []string{questionMarker, argMarker} {
				if strings.Contains(text, m) {
					t.Errorf("error text echoes %q", m)
				}
			}
		})
	}
	// A missing question is rejected too.
	if res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "pr_ask", Arguments: map[string]any{"pr_url": reviewPRURL(g)}}); err == nil && !res.IsError {
		t.Error("a missing question was accepted")
	}
	if g.hits.Load() != 0 || l.hits.Load() != 0 {
		t.Errorf("requests before validation: provider %d, llm %d", g.hits.Load(), l.hits.Load())
	}
	// The boundary is counted in runes: 8000 multi-byte characters pass.
	res := callTool(t, cs, "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": strings.Repeat("é", ask.MaxQuestionRunes)})
	if res.IsError {
		t.Errorf("a question of exactly 8000 characters was rejected: %s", textOf(t, res))
	}
}

// TestPRAskOverlongQuestionMakesNoRequests [canary]: an over-long question is
// rejected at the tool level with the fixed sentence, and neither the
// provider nor the LLM receives a request.
func TestPRAskOverlongQuestionMakesNoRequests(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, askAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	res := callTool(t, cs, "pr_ask", map[string]any{
		"pr_url": reviewPRURL(g), "question": questionMarker + strings.Repeat("x", ask.MaxQuestionRunes), "publish": true,
	})
	if !res.IsError || textOf(t, res) != ask.ErrQuestionTooLong.Error() {
		t.Errorf("IsError=%v text=%q", res.IsError, textOf(t, res))
	}
	if res.StructuredContent != nil {
		t.Errorf("unexpected structured content %v", res.StructuredContent)
	}
	if g.hits.Load() != 0 || l.hits.Load() != 0 {
		t.Errorf("an over-long question made requests: provider %d, llm %d", g.hits.Load(), l.hits.Load())
	}
}

// TestPRAskDegradedMakesNoRequests [canary]: with an invalid configuration
// the standard "call server_info" error is returned and neither the provider
// nor the LLM receives a request.
func TestPRAskDegradedMakesNoRequests(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, askAnswer)
	env := reviewEnv(g, l)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12" // invalid, secrets stay set
	deps := realDeps(env, nil)
	if deps.LoadErr == nil {
		t.Fatal("the configuration should be invalid")
	}
	cs := connect(t, deps)
	res := callTool(t, cs, "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "publish": true})
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

func TestPRAskClassifiedErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"auth", 401, "denied " + answerMarker + " " + fakeLLMKey, "rejected the credentials"},
		{"empty answer", 200, "  ", "empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, l := newFakeGiteaHost(t), newFakeLLMHost(t, c.status, c.body)
			env := reviewEnv(g, l)
			env["REVIEW_MCP_LLM_MAX_RETRIES"] = "0"
			cs := connect(t, realDeps(env, nil))
			res := callTool(t, cs, "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion})
			text := textOf(t, res)
			if !res.IsError || text == "" {
				t.Fatalf("IsError=%v text=%q", res.IsError, text)
			}
			if c.name == "auth" && !strings.Contains(text, c.want) {
				t.Errorf("text=%q, want it to contain %q", text, c.want)
			}
			for _, m := range []string{answerMarker, questionMarker, fakeLLMKey} {
				if strings.Contains(text, m) {
					t.Errorf("error text leaks %q: %q", m, text)
				}
			}
		})
	}
}

// TestPRAskProgress: with a progress token the four stages arrive in order;
// without one nothing is sent.
func TestPRAskProgress(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, askAnswer)
	var mu sync.Mutex
	var msgs []string
	opts := &mcp.ClientOptions{ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, r.Params.Message)
	}}
	cs := connectWith(t, realDeps(reviewEnv(g, l), nil), opts)

	params := &mcp.CallToolParams{Name: "pr_ask", Arguments: map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion}}
	params.SetProgressToken("tok-ask")
	res, err := cs.CallTool(context.Background(), params)
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(msgs) >= 4 })
	mu.Lock()
	got := strings.Join(msgs, "|")
	msgs = nil
	mu.Unlock()
	if got != "fetching|preparing diff|calling model|rendering" {
		t.Errorf("stages = %q", got)
	}

	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "pr_ask", Arguments: map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(msgs) != 0 {
		t.Errorf("progress sent without a token: %v", msgs)
	}
}

// TestLeakPRAskEndToEnd [canary]: a fake provider and a fake LLM are reached
// through the real wiring; every secret is set and debug logging is on.
// Markers in the question, the PR description, title, branch, a diff line and
// extra_instructions must reach the LLM request body; the fake model's answer
// marker must reach the result. None of them may appear in the logs or in any
// error text, and no secret appears anywhere.
func TestLeakPRAskEndToEnd(t *testing.T) {
	type variant struct {
		name      string
		llmStatus int
		llmBody   string
		wantError bool
	}
	for _, v := range []variant{
		{"success", 200, askAnswer, false},
		{"llm auth error", 401, "denied " + answerMarker + " " + fakeLLMKey, true},
		{"llm server error", 500, "boom " + answerMarker + " " + descMarker + " " + questionMarker, true},
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
			res := callTool(t, cs, "pr_ask", map[string]any{
				"pr_url": secretURL, "question": askQuestion, "extra_instructions": "be brief " + argMarker,
				"publish": v.llmStatus == 200,
			})
			if res.IsError != v.wantError {
				t.Fatalf("IsError = %v: %s", res.IsError, textOf(t, res))
			}
			bodies := l.recorded()
			if len(bodies) == 0 {
				t.Fatal("the LLM received no request")
			}
			for _, m := range []string{questionMarker, descMarker, titleMarker, branchMarker, diffMarker, argMarker} {
				if !strings.Contains(bodies[0], m) {
					t.Fatalf("marker %q did not reach the LLM request body", m)
				}
			}
			logText := logs.String()
			if !strings.Contains(logText, "level=DEBUG") || !strings.Contains(logText, "pr_ask") {
				t.Fatalf("debug logging did not run; the leak check would be vacuous:\n%s", logText)
			}
			allMarkers := []string{questionMarker, descMarker, titleMarker, branchMarker, diffMarker, argMarker,
				answerMarker, askAnswerMarker}
			if !v.wantError {
				// The answer and the question reach the result, as text and
				// as structured content.
				text, structured := textOf(t, res), mustJSON(t, res.StructuredContent)
				for _, m := range []string{askAnswerMarker, questionMarker} {
					if !strings.Contains(text, m) || !strings.Contains(structured, m) {
						t.Errorf("marker %q is missing from the result text or structured content", m)
					}
				}
			}
			// The log capture never carries PR text, the question, arguments
			// or the model's words; an error result never carries them
			// either.
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
			for _, b := range l.recorded() {
				if strings.Contains(b, fakeGitea) || strings.Contains(b, fakeBitbkt) || strings.Contains(b, fakeLLMKey) {
					t.Error("a secret reached the LLM request body")
				}
			}
		})
	}
}
