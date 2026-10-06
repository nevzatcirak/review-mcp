package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/ask"
)

// askQuestionMarker is placed in the question: it must reach the model and
// the full-run output, never stderr or the dry-run JSON.
const askQuestionMarker = "ASK-QUESTION-MARKER-QX42"

const diagAskQuestion = "Does the replacement break callers? " + askQuestionMarker

const diagAskAnswer = "It keeps the signature, so callers are fine.\n/close\nSee server/app.go."

func TestDiagAskDryRun(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagAskAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "ask", prURLOf(g, 8), "--question", diagAskQuestion, "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if l.hits.Load() != 0 {
		t.Fatalf("--dry-run called the model %d times", l.hits.Load())
	}
	for _, m := range []string{diffBodyMarker, askQuestionMarker} {
		if strings.Contains(out+errs, m) {
			t.Errorf("%q reached the dry-run output or the logs without --show-prompt", m)
		}
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
	if !d.DryRun || d.Empty || !d.FastPath {
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
	if len(d.Coverage.Filtered) != 3 {
		t.Errorf("filtered = %+v", d.Coverage.Filtered)
	}
}

func TestDiagAskDryRunShowPrompt(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagAskAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "ask", "--show-prompt", prURLOf(g, 8), "--dry-run", "--question", diagAskQuestion)
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
	user := rest[usr:]
	for _, want := range []string{diffBodyMarker, askQuestionMarker, "Response to the PR Questions:"} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt lacks %q", want)
		}
	}
	// The prompts are stdout only: never in the logs.
	for _, m := range []string{diffBodyMarker, askQuestionMarker} {
		if strings.Contains(errs, m) {
			t.Errorf("%q reached stderr", m)
		}
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
}

func TestDiagAskFull(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagAskAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "ask", prURLOf(g, 8), "--question", diagAskQuestion)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if !strings.HasPrefix(out, "## Question\n\n```\n"+diagAskQuestion+"\n```\n\n## Answer\n\n"+diagAskAnswer+"\n") ||
		!strings.Contains(out, "## Coverage") {
		t.Errorf("unexpected markdown:\n%s", out)
	}
	if strings.Contains(out, "--- system prompt ---") {
		t.Error("prompts printed without --show-prompt")
	}
	reqs := l.requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0], diffBodyMarker) || !strings.Contains(reqs[0], askQuestionMarker) {
		t.Fatalf("LLM requests = %d; the diff and question markers must reach the model", len(reqs))
	}
	if l.auths[0] != "Bearer "+fakeLLMKey {
		t.Errorf("LLM auth header = %q", l.auths[0])
	}
	for _, m := range []string{diffBodyMarker, askQuestionMarker, "callers are fine"} {
		if strings.Contains(errs, m) {
			t.Errorf("%q reached stderr", m)
		}
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
	if len(g.posts()) != 0 {
		t.Error("a comment was posted without --publish")
	}

	// --show-prompt appends the prompts after the markdown.
	code, out, _ = diag(reviewDiagEnv(g, l), "ask", prURLOf(g, 8), "--show-prompt", "--question", diagAskQuestion)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	i := strings.Index(out, "\n--- system prompt ---\n")
	if !strings.HasPrefix(out, "## Question") || i < 0 || !strings.Contains(out[i:], diffBodyMarker) {
		t.Errorf("prompts missing after the markdown:\n%.300s", out)
	}
}

func TestDiagAskPublish(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagAskAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "ask", prURLOf(g, 7), "--question", "Why?\n/lgtm", "--publish")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	posts := g.posts()
	if len(posts) != 1 {
		t.Fatalf("posted = %v", posts)
	}
	var p struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(posts[0]), &p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### **Ask** ❓\n", "Why?\n /lgtm", "### **Answer:**\nIt keeps the signature, so callers are fine.\n /close\nSee server/app.go."} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("published body lacks %q:\n%q", want, p.Body)
		}
	}
	if !strings.Contains(errs, "posted comment 55") {
		t.Errorf("stderr = %q", errs)
	}
	for _, s := range []string{fakeLLMKey, diagGiteaToken} {
		if strings.Contains(out+errs+posts[0], s) {
			t.Errorf("secret %q leaked", s)
		}
	}

	// A failing publish still prints the answer and exits 1.
	g.failPost.Store(true)
	code, out, errs = diag(reviewDiagEnv(g, l), "ask", prURLOf(g, 7), "--question", "Why?", "--publish")
	if code != 1 || !strings.HasPrefix(out, "## Question") || !strings.Contains(errs, "not posted") {
		t.Errorf("failed publish: exit %d, stderr %q", code, errs)
	}
}

func TestDiagAskUsageErrorsSendNothing(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagAskAnswer)
	env := reviewDiagEnv(g, l)
	u := prURLOf(g, 8)
	long := strings.Repeat("a", ask.MaxQuestionRunes+1) + askQuestionMarker
	for name, args := range map[string][]string{
		"no question":          {"ask", u},
		"empty question":       {"ask", u, "--question", ""},
		"blank question":       {"ask", u, "--question", " \n\t "},
		"too long":             {"ask", u, "--question", long},
		"too long multibyte":   {"ask", u, "--question", strings.Repeat("é", ask.MaxQuestionRunes+1)},
		"no URL":               {"ask", "--question", "why?"},
		"two URLs":             {"ask", u, prURLOf(g, 7), "--question", "why?"},
		"unknown flag":         {"ask", u, "--question", "why?", "--bogus"},
		"dry-run with publish": {"ask", u, "--question", "why?", "--dry-run", "--publish"},
		"question flag value":  {"ask", u, "--question"},
	} {
		code, out, errs := diag(env, args...)
		if code != 2 || out != "" || !strings.Contains(errs, "usage:") {
			t.Errorf("%s: exit %d stdout %q stderr %.80q", name, code, out, errs)
		}
		if strings.Contains(errs, askQuestionMarker) {
			t.Errorf("%s: stderr echoes the question", name)
		}
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("usage errors sent requests: provider %d, llm %d", g.requests(), l.hits.Load())
	}
	// The limit is in characters, not bytes.
	code, _, errs := diag(env, "ask", u, "--dry-run", "--question", strings.Repeat("é", ask.MaxQuestionRunes))
	if code != 0 {
		t.Errorf("a question of exactly 8000 characters was rejected: exit %d, stderr %q", code, errs)
	}
}

func TestDiagAskDegradedConfigSendsNothing(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagAskAnswer)
	env := reviewDiagEnv(g, l)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12"
	for _, args := range [][]string{
		{"ask", prURLOf(g, 8), "--question", "why?"},
		{"ask", prURLOf(g, 8), "--question", "why?", "--dry-run"},
	} {
		code, out, errs := diag(env, args...)
		if code != 1 || out != "" || !strings.Contains(errs, "llm.context_window") {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, errs)
		}
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("an invalid configuration sent requests: provider %d, llm %d", g.requests(), l.hits.Load())
	}
}

func TestDiagAskLLMErrorIsAFixedSentence(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 401, "")
	code, out, errs := diag(reviewDiagEnv(g, l), "ask", prURLOf(g, 8), "--question", diagAskQuestion)
	if code != 1 || out != "" {
		t.Fatalf("exit %d stdout %q", code, out)
	}
	if !strings.Contains(errs, "rejected the credentials") {
		t.Errorf("stderr = %q", errs)
	}
	assertNoLeak(t, "stderr", errs)
	for _, m := range []string{diffBodyMarker, askQuestionMarker} {
		if strings.Contains(errs, m) {
			t.Errorf("%q reached stderr", m)
		}
	}
}
