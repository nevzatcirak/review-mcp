package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// ciReviewAnswer is a review answer with the given key issues block and
// security concerns, and an optional issue_compliance verdict.
func ciReviewAnswer(findings bool, security, verdict string) string {
	issues := "  key_issues_to_review: []\n"
	if findings {
		issues = "  key_issues_to_review:\n    - relevant_file: server/app.go\n      issue_header: Check the replacement\n" +
			"      issue_content: The replaced line needs a test.\n      start_line: 1\n      end_line: 1\n"
	}
	a := "```yaml\nreview:\n  estimated_effort_to_review: 3\n  relevant_tests: \"No\"\n" + issues +
		"  security_concerns: \"" + security + "\"\n"
	if verdict != "" {
		a += "  issue_compliance:\n    issues:\n    - ref: |\n        PROJ-1\n      met: []\n      not_met:\n" +
			"      - |\n        The change is missing\n      needs_verification: []\n    verdict: " + verdict + "\n"
	}
	return a + "```\n"
}

func TestCIExitCodes(t *testing.T) {
	issue := writeFile(t, "issue.json", `[{"key":"PROJ-1","title":"Add the feature"}]`)
	partialEnv := func(env map[string]string) map[string]string {
		// With this diff budget PR 9's one large file is reviewed only in
		// part: the coverage is partial.
		env["REVIEW_MCP_DIFF_MAX_TOKENS"] = "1000"
		return env
	}
	for _, tc := range []struct {
		name   string
		answer string
		env    func(map[string]string) map[string]string
		args   []string
		code   int
		reason string
	}{
		{"findings without fail-on", ciReviewAnswer(true, "Yes, an injection", "not_compliant"), nil, []string{"--issue-context-file", issue}, 0, ""},
		{"any with a finding", ciReviewAnswer(true, "No", ""), nil, []string{"--fail-on", "any"}, 3, "--fail-on any is met (1 findings)"},
		{"any without findings", ciReviewAnswer(false, "No", ""), nil, []string{"--fail-on", "any"}, 0, ""},
		{"security concerns", ciReviewAnswer(false, "Yes, the token is logged", ""), nil, []string{"--fail-on", "security"}, 3, "--fail-on security is met (security concerns)"},
		{"security no", ciReviewAnswer(true, "No", ""), nil, []string{"--fail-on", "security"}, 0, ""},
		{"security focus", ciReviewAnswer(true, "No", ""), nil, []string{"--fail-on", "security", "--focus", "security"}, 3,
			"--fail-on security is met (1 findings of a security-focused review)"},
		{"other focus", ciReviewAnswer(true, "No", ""), nil, []string{"--fail-on", "security", "--focus", "tests"}, 0, ""},
		{"not compliant", ciReviewAnswer(false, "No", "not_compliant"), nil, []string{"--fail-on", "issue-not-compliant", "--issue-context-file", issue}, 3,
			"--fail-on issue-not-compliant is met (verdict not_compliant)"},
		{"partly compliant", ciReviewAnswer(true, "No", "partly_compliant"), nil, []string{"--fail-on", "issue-not-compliant", "--issue-context-file", issue}, 0, ""},
		{"no issue", ciReviewAnswer(true, "No", ""), nil, []string{"--fail-on", "issue-not-compliant"}, 0, ""},
		{"partial without the flag", ciReviewAnswer(false, "No", ""), partialEnv, nil, 0, ""},
		{"partial", ciReviewAnswer(false, "No", ""), partialEnv, []string{"--fail-on-partial"}, 3, "--fail-on-partial is met (the coverage is partial)"},
		{"full coverage", ciReviewAnswer(false, "No", ""), nil, []string{"--fail-on-partial"}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, l := newFakeGitea(t), newFakeLLM(t, 200, tc.answer)
			env := reviewDiagEnv(g, l)
			if tc.env != nil {
				env = tc.env(env)
			}
			pr := 8
			if tc.env != nil {
				pr = 9
			}
			r := ciCall(t, env, nil, append([]string{"review", "--pr-url", prURLOf(g, pr), "--json"}, tc.args...)...)
			if r.code != tc.code {
				t.Fatalf("exit %d, want %d; stderr:\n%s", r.code, tc.code, r.errs)
			}
			// The result is printed whatever the exit code.
			var res review.Result
			if err := json.Unmarshal([]byte(r.out), &res); err != nil {
				t.Fatalf("stdout is not the result: %v", err)
			}
			validateCIOutput(t, review.ResultSchema(), r.out)
			if tc.reason != "" && r.lastStderr != "ci review: exit 3: "+tc.reason {
				t.Errorf("final line = %q", r.lastStderr)
			}
			if tc.code == 0 && r.lastStderr != "ci review: printed only (no --publish)" {
				t.Errorf("final line = %q", r.lastStderr)
			}
			assertCIClean(t, r)
		})
	}
}

// TestCIFailOnPostsFirst: exit 3 comes after the review was printed and
// posted.
func TestCIFailOnPostsFirst(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	r := ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", prURLOf(g, 7), "--publish", "--fail-on", "any")
	if r.code != 3 || !strings.HasPrefix(r.out, "## PR Review") || len(g.posts()) != 1 {
		t.Fatalf("exit %d, posts %d; stderr:\n%s", r.code, len(g.posts()), r.errs)
	}
	lines := strings.Split(strings.TrimRight(r.errs, "\n"), "\n")
	if n := len(lines); n < 2 || lines[n-2] != "ci review: posted overview 55" || lines[n-1] != "ci review: exit 3: --fail-on any is met (1 findings)" {
		t.Errorf("last lines = %q", lines[max(0, len(lines)-2):])
	}

	// A failed publish is a failed run: exit 1 wins over --fail-on.
	g.failPost.Store(true)
	r = ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", prURLOf(g, 7), "--publish", "--fail-on", "any")
	if r.code != 1 || !strings.HasPrefix(r.lastStderr, "ci review: the result was not posted: ") {
		t.Errorf("failed publish: exit %d, final line %q", r.code, r.lastStderr)
	}
}

func TestCIFailOnPartialOtherCommands(t *testing.T) {
	for _, tc := range []struct{ cmd, answer string }{{"describe", diagDescribeAnswer}, {"improve", diagImproveAnswer}} {
		g, l := newFakeGitea(t), newFakeLLM(t, 200, tc.answer)
		env := reviewDiagEnv(g, l)
		r := ciCall(t, env, nil, tc.cmd, "--pr-url", prURLOf(g, 7), "--fail-on-partial")
		if r.code != 0 {
			t.Errorf("%s full coverage: exit %d; stderr:\n%s", tc.cmd, r.code, r.errs)
		}
		env["REVIEW_MCP_DIFF_MAX_TOKENS"] = "1000"
		r = ciCall(t, env, nil, tc.cmd, "--pr-url", prURLOf(g, 9), "--fail-on-partial")
		if r.code != 3 || r.lastStderr != "ci "+tc.cmd+": exit 3: --fail-on-partial is met (the coverage is partial)" {
			t.Errorf("%s partial: exit %d, final line %q; stderr:\n%s", tc.cmd, r.code, r.lastStderr, r.errs)
		}
	}
}

func TestCIFailOnUsage(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	for _, args := range [][]string{
		{"review", "--pr-url", prURLOf(g, 8), "--fail-on", "high"},
		{"review", "--pr-url", prURLOf(g, 8), "--fail-on", ""},
		{"describe", "--pr-url", prURLOf(g, 8), "--fail-on", "any"},
		{"improve", "--pr-url", prURLOf(g, 8), "--fail-on", "any"},
		{"review", "--pr-url", prURLOf(g, 8), "--fail-on-partial=maybe"},
	} {
		r := ciCall(t, reviewDiagEnv(g, l), nil, args...)
		if r.code != 2 || r.loads != 0 || r.out != "" {
			t.Errorf("%v: exit %d, loads %d; stderr:\n%s", args, r.code, r.loads, r.errs)
		}
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("requests sent: provider %d, llm %d", g.requests(), l.hits.Load())
	}
}

// TestCIFailOnCarriedFindings: findings an incremental review carried
// forward (X-36) count as findings of the run.
func TestCIFailOnCarriedFindings(t *testing.T) {
	res := &review.Result{Review: &review.Review{KeyIssuesToReview: []review.KeyIssue{
		{RelevantFile: "a.go", IssueHeader: "Old", CarriedFrom: "0123456789abcdef0123456789abcdef01234567"},
	}}}
	var errb bytes.Buffer
	reason := ciFailReason(&ciOptions{failOn: ciFailOnAny}, &ciOutcome{review: res}, logging.New(&errb, 0), "ci review: ")
	if reason != "--fail-on any is met (1 findings)" {
		t.Errorf("reason = %q", reason)
	}
}

// TestCIFailOnWarnsWhenUnreachable: a level the review cannot meet is
// logged as a warning, and the exit code stays 0.
func TestCIFailOnWarnsWhenUnreachable(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, ciReviewAnswer(true, "No", ""))
	env := reviewDiagEnv(g, l)
	env["REVIEW_MCP_REVIEW_REQUIRE_SECURITY"] = "false"
	r := ciCall(t, env, nil, "review", "--pr-url", prURLOf(g, 8), "--fail-on", "security")
	if r.code != 0 || !strings.Contains(r.errs, "the security_concerns field is off") {
		t.Errorf("exit %d; stderr:\n%s", r.code, r.errs)
	}
	r = ciCall(t, env, nil, "review", "--pr-url", prURLOf(g, 8), "--fail-on", "issue-not-compliant")
	if r.code != 0 || !strings.Contains(r.errs, "no issue was checked") {
		t.Errorf("exit %d; stderr:\n%s", r.code, r.errs)
	}
}

func TestCIStepSummary(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	summary := writeFile(t, "summary.md", "# earlier step\n")
	vars := map[string]string{"GITHUB_STEP_SUMMARY": summary}
	r := ciCall(t, reviewDiagEnv(g, l), vars, "review", "--pr-url", prURLOf(g, 7), "--publish", "--json")
	if r.code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", r.code, r.errs)
	}
	data, err := os.ReadFile(summary) //nolint:gosec // G304: a file under t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	// The markdown is appended, also with --json, after what earlier steps
	// wrote.
	if !strings.HasPrefix(got, "# earlier step\n## PR Review") || !strings.Contains(got, "Check the replacement") || !strings.HasSuffix(got, "\n") {
		t.Errorf("summary =\n%s", got)
	}
	for _, s := range append(append([]string{}, diagSecrets...), ciGitHubToken, ciJobToken) {
		if strings.Contains(got, s) {
			t.Errorf("the step summary leaks %q", s)
		}
	}
	assertCIClean(t, r)

	// A summary that cannot be written is a warning: the run still exits 0
	// and its final line is unchanged.
	vars["GITHUB_STEP_SUMMARY"] = t.TempDir()
	r = ciCall(t, reviewDiagEnv(g, l), vars, "review", "--pr-url", prURLOf(g, 8))
	if r.code != 0 || !strings.Contains(r.errs, "the step summary could not be written") ||
		r.lastStderr != "ci review: printed only (no --publish)" {
		t.Errorf("exit %d; stderr:\n%s", r.code, r.errs)
	}

	// Without the variable nothing is written anywhere.
	r = ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", prURLOf(g, 8))
	if r.code != 0 || strings.Contains(r.errs, "step summary") {
		t.Errorf("exit %d; stderr:\n%s", r.code, r.errs)
	}
}
