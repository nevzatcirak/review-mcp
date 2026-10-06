package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeLLM is an OpenAI-compatible chat endpoint that counts requests and
// records their bodies.
type fakeLLM struct {
	srv    *httptest.Server
	hits   atomic.Int64
	status int
	answer string

	mu     sync.Mutex
	bodies []string
	auths  []string
}

func newFakeLLM(t *testing.T, status int, answer string) *fakeLLM {
	t.Helper()
	f := &fakeLLM{status: status, answer: answer}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if f.status != http.StatusOK {
			http.Error(w, "denied "+diagMarker+" "+fakeLLMKey, f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": f.answer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLLM) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

const reviewAnswer = "```yaml\nreview:\n  estimated_effort_to_review: 3\n  relevant_tests: \"No\"\n" +
	"  key_issues_to_review:\n    - relevant_file: server/app.go\n      issue_header: Check the replacement\n" +
	"      issue_content: The replaced line needs a test.\n      start_line: 1\n      end_line: 1\n" +
	"  security_concerns: \"No\"\n```\n"

func reviewDiagEnv(g *fakeHost, l *fakeLLM) map[string]string {
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_BASE_URL"] = l.srv.URL + "/v1"
	return env
}

func prURLOf(g *fakeHost, n int) string {
	return g.srv.URL + "/gitea/octo/demo/pulls/" + strconv.Itoa(n)
}

type dryRunOut struct {
	DryRun bool `json:"dry_run"`
	Empty  bool `json:"empty"`
	PR     struct {
		Kind   string `json:"kind"`
		Number int    `json:"number"`
	} `json:"pr"`
	Budget struct {
		ContextWindow int `json:"context_window"`
		SoftLimit     int `json:"soft_limit"`
		HardLimit     int `json:"hard_limit"`
		PromptTokens  int `json:"prompt_tokens"`
	} `json:"budget"`
	Tokens struct {
		Prompt        int `json:"prompt"`
		Diff          int `json:"diff"`
		Request       int `json:"request"`
		ContextWindow int `json:"context_window"`
	} `json:"tokens"`
	FastPath bool `json:"fast_path"`
	Coverage struct {
		Included []string   `json:"included"`
		Filtered []diffSkip `json:"filtered"`
	} `json:"coverage"`
	Notes []string `json:"notes"`
}

func TestDiagReviewDryRun(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "review", prURLOf(g, 8), "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if l.hits.Load() != 0 {
		t.Fatalf("--dry-run called the model %d times", l.hits.Load())
	}
	if strings.Contains(out+errs, diffBodyMarker) {
		t.Error("diff text reached the dry-run output or the logs without --show-prompt")
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
	var d dryRunOut
	if err := json.NewDecoder(strings.NewReader(out)).Decode(&d); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	_, rest := decodeReport(t, out)
	if strings.TrimSpace(rest) != "" {
		t.Errorf("output after the JSON without --show-prompt: %q", rest)
	}
	if !d.DryRun || d.Empty || d.PR.Kind != "gitea" || d.PR.Number != 8 || !d.FastPath {
		t.Errorf("report = %+v", d)
	}
	if d.Tokens.Prompt <= 0 || d.Tokens.Diff <= 0 || d.Tokens.Request <= d.Tokens.Prompt || d.Tokens.ContextWindow != 32000 {
		t.Errorf("tokens = %+v", d.Tokens)
	}
	if d.Budget.PromptTokens != d.Tokens.Prompt || d.Budget.SoftLimit != 32000-1500-d.Tokens.Prompt ||
		d.Budget.HardLimit != 32000-1000-d.Tokens.Prompt {
		t.Errorf("budget = %+v", d.Budget)
	}
	if got := strings.Join(sortedCopy(d.Coverage.Included), ","); got != strings.Join(wantNonFiltered, ",") {
		t.Errorf("included = %s", got)
	}
	if len(d.Coverage.Filtered) != 3 || reasonOf(d.Coverage.Filtered, "package-lock.json") == "" {
		t.Errorf("filtered = %+v", d.Coverage.Filtered)
	}
}

func TestDiagReviewDryRunShowPrompt(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "review", "--show-prompt", prURLOf(g, 8), "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if l.hits.Load() != 0 {
		t.Fatalf("--dry-run called the model")
	}
	_, rest := decodeReport(t, out)
	sys := strings.Index(rest, "\n--- system prompt ---\n")
	usr := strings.Index(rest, "\n--- user prompt ---\n")
	if sys < 0 || usr < sys {
		t.Fatalf("prompt separators missing or out of order:\n%.300s", rest)
	}
	system, user := rest[sys:usr], rest[usr:]
	if !strings.Contains(system, "PR Reviewer") && !strings.Contains(system, "review") {
		t.Errorf("system prompt looks wrong: %.200s", system)
	}
	if !strings.Contains(user, diffBodyMarker) || !strings.Contains(user, "Response (should be a valid YAML, and nothing else):") {
		t.Errorf("user prompt does not carry the diff and the response line")
	}
	// The prompts are stdout only: never in the logs.
	if strings.Contains(errs, diffBodyMarker) {
		t.Error("the prompt text reached stderr")
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
}

func TestDiagReviewFull(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "review", prURLOf(g, 8))
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if !strings.HasPrefix(out, "## PR Review") || !strings.Contains(out, "Check the replacement") || !strings.Contains(out, "server/app.go") {
		t.Errorf("unexpected markdown:\n%s", out)
	}
	if strings.Contains(out, "--- system prompt ---") {
		t.Error("prompts printed without --show-prompt")
	}
	reqs := l.requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0], diffBodyMarker) {
		t.Fatalf("LLM requests = %d; the diff marker must reach the model", len(reqs))
	}
	if l.auths[0] != "Bearer "+fakeLLMKey {
		t.Errorf("LLM auth header = %q", l.auths[0])
	}
	if strings.Contains(errs, diffBodyMarker) || strings.Contains(errs, "Check the replacement") {
		t.Error("diff or review text reached stderr")
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
	if len(g.posts()) != 0 {
		t.Error("a comment was posted without --publish")
	}

	// --show-prompt appends the prompts after the markdown.
	code, out, _ = diag(reviewDiagEnv(g, l), "review", prURLOf(g, 8), "--show-prompt")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	i := strings.Index(out, "\n--- system prompt ---\n")
	if !strings.HasPrefix(out, "## PR Review") || i < 0 || !strings.Contains(out[i:], diffBodyMarker) {
		t.Errorf("prompts missing after the markdown:\n%.300s", out)
	}
}

func TestDiagReviewPublish(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "review", prURLOf(g, 7), "--publish")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	posts := g.posts()
	if len(posts) != 1 || !strings.Contains(posts[0], "PR Review") {
		t.Fatalf("posted = %v", posts)
	}
	if !strings.Contains(errs, "posted comment 55") {
		t.Errorf("stderr = %q", errs)
	}
	for _, s := range []string{fakeLLMKey, diagGiteaToken} {
		if strings.Contains(out+errs+posts[0], s) {
			t.Errorf("secret %q leaked", s)
		}
	}

	// A failing publish still prints the review and exits 1.
	g.failPost.Store(true)
	code, out, errs = diag(reviewDiagEnv(g, l), "review", prURLOf(g, 7), "--publish")
	if code != 1 || !strings.HasPrefix(out, "## PR Review") || !strings.Contains(errs, "not posted") {
		t.Errorf("failed publish: exit %d, stderr %q", code, errs)
	}
}

func TestDiagReviewUsageErrorsSendNothing(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	env := reviewDiagEnv(g, l)
	for _, args := range [][]string{
		{"review"},
		{"review", prURLOf(g, 8), prURLOf(g, 7)},
		{"review", prURLOf(g, 8), "--dry-run", "--publish"},
		{"review", prURLOf(g, 8), "--bogus"},
	} {
		code, out, errs := diag(env, args...)
		if code != 2 || out != "" || !strings.Contains(errs, "usage:") {
			t.Errorf("%v: exit %d stdout %q stderr %.80q", args, code, out, errs)
		}
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("usage errors sent requests: provider %d, llm %d", g.requests(), l.hits.Load())
	}
}

func TestDiagReviewDegradedConfigSendsNothing(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	env := reviewDiagEnv(g, l)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12"
	for _, args := range [][]string{{"review", prURLOf(g, 8)}, {"review", prURLOf(g, 8), "--dry-run"}} {
		code, out, errs := diag(env, args...)
		if code != 1 || out != "" || !strings.Contains(errs, "llm.context_window") {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, errs)
		}
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("an invalid configuration sent requests: provider %d, llm %d", g.requests(), l.hits.Load())
	}
}

func TestDiagReviewLLMErrorIsAFixedSentence(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 401, "")
	code, out, errs := diag(reviewDiagEnv(g, l), "review", prURLOf(g, 8))
	if code != 1 || out != "" {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	if !strings.Contains(errs, "rejected the credentials") {
		t.Errorf("stderr = %q", errs)
	}
	assertNoLeak(t, "stderr", errs)
	if strings.Contains(errs, diffBodyMarker) {
		t.Error("diff text reached stderr")
	}
}
