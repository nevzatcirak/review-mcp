package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/improve"
)

// diagImproveAnswer suggests one change to src/app.go of PR 7 of the fake
// Gitea. The fake model gives every call this answer, so the self-review
// matches no suggestion and the suggestion is kept unscored.
const diagImproveAnswer = "```yaml\ncode_suggestions:\n- relevant_file: src/app.go\n  language: go\n" +
	"  existing_code: |\n    var a = 2\n  suggestion_content: Name the constant.\n" +
	"  improved_code: |\n    const answer = 2\n  one_sentence_summary: Name the constant\n  label: general\n```\n"

func TestDiagImproveDryRun(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagImproveAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "improve", prURLOf(g, 7), "--dry-run", "--show-prompt")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if l.hits.Load() != 0 {
		t.Fatalf("--dry-run called the model %d times", l.hits.Load())
	}
	m, rest := decodeReport(t, out)
	if m["dry_run"] != true || m["empty"] != false {
		t.Errorf("report = %v", m)
	}
	if !strings.Contains(rest, "\n--- user prompt ---\n") || !strings.Contains(rest, "The PR Diff:\n======\n") ||
		!strings.Contains(rest, "__new hunk__") {
		t.Errorf("prompts missing or without the numbered diff:\n%.600s", rest)
	}
	if strings.Contains(errs, diagMarker) {
		t.Errorf("PR text reached stderr")
	}
	assertNoLeak(t, "stderr", errs)

	code, out, errs = diag(reviewDiagEnv(g, l), "improve", prURLOf(g, 7), "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if _, rest := decodeReport(t, out); strings.TrimSpace(rest) != "" {
		t.Errorf("output after the JSON without --show-prompt: %q", rest)
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
}

func TestDiagImproveFull(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagImproveAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "improve", prURLOf(g, 7))
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if !strings.HasPrefix(out, "## Suggestions\n\n### 1. Name the constant\n") || !strings.Contains(out, "- Score: unscored") ||
		!strings.Contains(out, "## Coverage") {
		t.Errorf("unexpected markdown:\n%s", out)
	}
	if l.hits.Load() != 2 {
		t.Errorf("model calls = %d, want the suggestion call and its self-review", l.hits.Load())
	}
	if len(g.posts()) != 0 {
		t.Error("diag improve posted a comment")
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)

	code, out, _ = diag(reviewDiagEnv(g, l), "improve", prURLOf(g, 7), "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var res improve.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Suggestions) != 1 || res.Suggestions[0].Score != nil {
		t.Errorf("--json = %v %+v", err, res)
	}

	if code, _, _ := diag(reviewDiagEnv(g, l), "improve", prURLOf(g, 7), "--json", "--dry-run"); code != 2 {
		t.Errorf("--json with --dry-run: exit %d, want 2", code)
	}
}
