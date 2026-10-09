package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/describe"
)

// diagDescribeAnswer describes the two reviewable files of PR 7 of the fake
// Gitea (src/renamed.go and src/app.go).
const diagDescribeAnswer = "```yaml\ntype:\n- Enhancement\ndescription: |\n  - Rename a file\ntitle: |\n  Rename and extend\n" +
	"pr_files:\n- filename: src/renamed.go\n  changes_title: Rename the file\n  changes_summary: \"- Renamed\"\n  label: refactoring\n" +
	"- filename: src/app.go\n  changes_title: Extend the app\n  changes_summary: \"- One line\"\n  label: enhancement\n```\n"

func TestDiagDescribeDryRun(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagDescribeAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "describe", prURLOf(g, 7), "--dry-run", "--show-prompt")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if l.hits.Load() != 0 {
		t.Fatalf("--dry-run called the model %d times", l.hits.Load())
	}
	m, rest := decodeReport(t, out)
	if m["dry_run"] != true || m["commit_messages"] != float64(2) {
		t.Errorf("report = %v", m)
	}
	if !strings.Contains(rest, "\n--- user prompt ---\n") || !strings.Contains(rest, "Commit messages:\n=====\n1. First commit\n2. Second commit") {
		t.Errorf("prompts missing or without the commit block:\n%.600s", rest)
	}
	// The prompts carry the PR text on stdout only, never in the logs.
	if strings.Contains(errs, diagMarker) {
		t.Errorf("PR text reached stderr")
	}
	assertNoLeak(t, "stderr", errs)

	// Without --show-prompt nothing but the report is printed.
	code, out, errs = diag(reviewDiagEnv(g, l), "describe", prURLOf(g, 7), "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if _, rest := decodeReport(t, out); strings.TrimSpace(rest) != "" {
		t.Errorf("output after the JSON without --show-prompt: %q", rest)
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
}

func TestDiagDescribeFull(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagDescribeAnswer)
	code, out, errs := diag(reviewDiagEnv(g, l), "describe", prURLOf(g, 7))
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if !strings.HasPrefix(out, "## Title\n\nRename and extend\n") || !strings.Contains(out, "## Walkthrough") ||
		!strings.Contains(out, "## Coverage") {
		t.Errorf("unexpected markdown:\n%s", out)
	}
	if len(g.posts()) != 0 {
		t.Error("diag describe posted a comment")
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)

	code, out, _ = diag(reviewDiagEnv(g, l), "describe", prURLOf(g, 7), "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var res describe.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Title == nil || len(res.Files) != 2 {
		t.Errorf("--json = %v %+v", err, res)
	}

	if code, _, _ := diag(reviewDiagEnv(g, l), "describe", prURLOf(g, 7), "--json", "--dry-run"); code != 2 {
		t.Errorf("--json with --dry-run: exit %d, want 2", code)
	}
}
