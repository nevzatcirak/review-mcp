package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// TestDryRunReportsRepositoryContext: the dry run shows the block's tokens
// and the symbol list (RC-9) when repository context is on, and has no such
// field when it is off, so the report of a run without the feature is
// unchanged.
func TestDryRunReportsRepositoryContext(t *testing.T) {
	rep := &repoctx.Report{Tokens: 321, Symbols: []string{"Alpha", "Beta"}}

	pl := &review.Plan{Result: &review.Result{}, RepoReport: rep}
	raw, err := json.Marshal(buildDryRunReport(pl, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"repo_context":{"tokens":321,"symbols":["Alpha","Beta"]}`) {
		t.Errorf("pr_review dry run: %s", raw)
	}
	pl.RepoReport = nil
	raw, _ = json.Marshal(buildDryRunReport(pl, time.Second))
	if strings.Contains(string(raw), `"repo_context":{"tokens"`) {
		t.Errorf("pr_review dry run with the feature off: %s", raw)
	}

	apl := &ask.Plan{Result: &ask.Result{}, RepoReport: rep}
	raw, _ = json.Marshal(buildAskDryRunReport(apl, time.Second))
	if !strings.Contains(string(raw), `"repo_context":{"tokens":321,"symbols":["Alpha","Beta"]}`) {
		t.Errorf("pr_ask dry run: %s", raw)
	}
	apl.RepoReport = nil
	raw, _ = json.Marshal(buildAskDryRunReport(apl, time.Second))
	if strings.Contains(string(raw), `"repo_context":{"tokens"`) {
		t.Errorf("pr_ask dry run with the feature off: %s", raw)
	}
}

// TestDiagReviewRepoContextFlag (RC-10): --repo-context overrides
// context.repo.enabled for the run, before or after the URL; anything but
// on/off is a usage error that sends nothing.
func TestDiagReviewRepoContextFlag(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	status := func(env map[string]string, args ...string) string {
		t.Helper()
		code, out, errs := diag(env, args...)
		if code != 0 {
			t.Fatalf("exit %d; stderr:\n%s", code, errs)
		}
		var d struct {
			Coverage struct {
				RepoContext struct {
					Status string `json:"status"`
				} `json:"repo_context"`
			} `json:"coverage"`
		}
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		return d.Coverage.RepoContext.Status
	}
	env := reviewDiagEnv(g, l)
	url := prURLOf(g, 8)
	// The fake diff defines no symbol, so "on" searches nothing and needs no git.
	if got := status(env, "review", url, "--dry-run"); got != "off" {
		t.Errorf("default: %q", got)
	}
	if got := status(env, "review", "--repo-context=on", url, "--dry-run"); got != "used" {
		t.Errorf("--repo-context=on before the URL: %q", got)
	}
	if got := status(env, "review", url, "--dry-run", "--repo-context", "on"); got != "used" {
		t.Errorf("--repo-context on after the URL: %q", got)
	}
	on := reviewDiagEnv(g, l)
	on["REVIEW_MCP_CONTEXT_REPO_ENABLED"] = "true"
	if got := status(on, "review", url, "--dry-run"); got != "used" {
		t.Errorf("enabled by the environment: %q", got)
	}
	if got := status(on, "review", url, "--dry-run", "--repo-context=off"); got != "off" {
		t.Errorf("--repo-context=off over the environment: %q", got)
	}
	code, _, errs := diag(env, "review", url, "--repo-context=maybe")
	if code != 2 || !strings.Contains(errs, "--repo-context must be on or off") || l.hits.Load() != 0 {
		t.Errorf("bad value: exit %d, stderr %q, model hits %d", code, errs, l.hits.Load())
	}
}

// TestDiagReviewJSON: --json prints the structured result; with --dry-run it
// is a usage error.
func TestDiagReviewJSON(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	env := reviewDiagEnv(g, l)
	code, out, errs := diag(env, "review", prURLOf(g, 8), "--json")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	var res struct {
		Review struct {
			Issues []struct {
				File string `json:"relevant_file"`
			} `json:"key_issues_to_review"`
		} `json:"review"`
		Coverage struct {
			RepoContext struct {
				Status string `json:"status"`
			} `json:"repo_context"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(res.Review.Issues) != 1 || res.Review.Issues[0].File != "server/app.go" || res.Coverage.RepoContext.Status != "off" {
		t.Errorf("result = %+v", res)
	}
	assertNoLeak(t, "stdout", out)
	if code, _, _ := diag(env, "review", prURLOf(g, 8), "--json", "--dry-run"); code != 2 {
		t.Errorf("--json with --dry-run: exit %d", code)
	}
}
