package main

import (
	"strings"
	"testing"
)

const windowModels = `{"data":[{"id":"example-model","max_model_len":40000}]}`

func unsetWindowEnv(g *fakeHost, l *fakeLLM) map[string]string {
	env := reviewDiagEnv(g, l)
	delete(env, "REVIEW_MCP_LLM_CONTEXT_WINDOW")
	return env
}

// X-15: --context-window overrides llm.context_window, and with it diag diff
// needs no LLM access.
func TestDiagDiffContextWindowFlag(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	l.modelsBody = windowModels
	t.Run("overrides the configured value", func(t *testing.T) {
		d, _, _ := runDiff(t, reviewDiagEnv(g, l), g, 8, "--context-window", "5000")
		if d.Budget.ContextWindow != 5000 {
			t.Errorf("context window = %d, want the flag's 5000", d.Budget.ContextWindow)
		}
	})
	t.Run("works with the key unset in config", func(t *testing.T) {
		d, _, _ := runDiff(t, unsetWindowEnv(g, l), g, 8, "--context-window", "6000")
		if d.Budget.ContextWindow != 6000 {
			t.Errorf("context window = %d, want 6000", d.Budget.ContextWindow)
		}
	})
	if n := l.hits.Load(); n != 0 {
		t.Errorf("the LLM endpoint received %d requests, want 0 with --context-window", n)
	}
}

func TestDiagDiffContextWindowFlagIsValidated(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	for _, v := range []string{"abc", "4095", "-1", "0"} {
		code, out, errs := diag(unsetWindowEnv(g, l), "diff", prURLOf(g, 8), "--context-window", v)
		if code != 2 || out != "" || !strings.Contains(errs, "--context-window must be an integer >= 4096") {
			t.Errorf("--context-window %s: exit %d stdout %q stderr %q", v, code, out, errs)
		}
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("usage errors sent requests: provider %d, llm %d", g.requests(), l.hits.Load())
	}
}

// Without the flag and without llm.context_window the diff budget uses the
// window read from the endpoint (90 % of 40000).
func TestDiagDiffResolvesTheWindowFromTheEndpoint(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	l.modelsBody = windowModels
	d, _, _ := runDiff(t, unsetWindowEnv(g, l), g, 8)
	if d.Budget.ContextWindow != 36000 {
		t.Errorf("context window = %d, want 36000", d.Budget.ContextWindow)
	}
	if n := l.hits.Load(); n != 1 {
		t.Errorf("LLM requests = %d, want the one model-list probe", n)
	}
}

// A failing probe is a fixed sentence and sends nothing to the provider.
func TestDiagFailedProbeSendsNothingToTheProvider(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	l.modelsStatus = 401
	env := unsetWindowEnv(g, l)
	for name, args := range map[string][]string{
		"diff":          {"diff", prURLOf(g, 8)},
		"review":        {"review", prURLOf(g, 8)},
		"review dryrun": {"review", prURLOf(g, 8), "--dry-run"},
		"ask":           {"ask", prURLOf(g, 8), "--question", "Why?"},
		"ask dryrun":    {"ask", prURLOf(g, 8), "--question", "Why?", "--dry-run"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errs := diag(env, args...)
			if code != 1 || out != "" || !strings.Contains(errs, "rejected the credentials") {
				t.Errorf("exit %d stdout %q stderr %q", code, out, errs)
			}
			assertNoLeak(t, "stderr", errs)
		})
	}
	if g.requests() != 0 {
		t.Errorf("provider requests = %d, want 0", g.requests())
	}
	for _, b := range l.requests() {
		if strings.Contains(b, "messages") {
			t.Error("a completion was requested")
		}
	}
}

func TestDiagDryRunProbesOnlyWhenTheWindowIsUnset(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	l.modelsBody = windowModels
	if code, _, errs := diag(reviewDiagEnv(g, l), "review", prURLOf(g, 8), "--dry-run"); code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if n := l.hits.Load(); n != 0 {
		t.Errorf("a dry run with llm.context_window set sent %d LLM requests, want 0", n)
	}
	code, out, errs := diag(unsetWindowEnv(g, l), "review", prURLOf(g, 8), "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, `"context_window": 36000`) {
		t.Errorf("the dry run does not report the resolved window:\n%.600s", out)
	}
	if n := l.hits.Load(); n != 1 {
		t.Errorf("LLM requests = %d, want 1 (the probe)", n)
	}
	for _, b := range l.requests() {
		if strings.Contains(b, "messages") {
			t.Error("a dry run requested a completion")
		}
	}
}

func TestDiagReviewResolvesTheWindowThroughTheRecorder(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	l.modelsBody = windowModels
	code, out, errs := diag(unsetWindowEnv(g, l), "review", prURLOf(g, 8), "--show-prompt")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "--- system prompt ---") {
		t.Error("the prompts were not shown")
	}
	if n := len(l.requests()); n != 2 { // the probe, then one completion
		t.Errorf("LLM requests = %d, want 2", n)
	}
}
