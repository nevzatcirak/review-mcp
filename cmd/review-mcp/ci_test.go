package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/describe"
	"github.com/nevzatcirak/review-mcp/internal/improve"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Tokens a pipeline has in its environment. CI mode must never read them:
// the provider token comes only from REVIEW_MCP_* (V-4 §1.2 item 5).
const (
	ciGitHubToken = "FAKE-ci-github-token-QX42-never-read" //nolint:gosec // synthetic test value
	ciJobToken    = "FAKE-ci-job-token-QX42-never-read"    //nolint:gosec // synthetic test value
)

// ciAllowedVariables are the only environment variables CI mode may read
// (V-4 §1.2, §1.4, §1.5).
var ciAllowedVariables = []string{
	"GITEA_ACTIONS", "GITHUB_ACTIONS", "GITHUB_SERVER_URL", "GITHUB_REPOSITORY",
	"GITHUB_EVENT_NAME", "GITHUB_EVENT_PATH", "GITLAB_CI", "CI_SERVER_URL",
	"CI_MERGE_REQUEST_PROJECT_PATH", "CI_MERGE_REQUEST_IID", "GITHUB_STEP_SUMMARY", "RUNNER_TEMP",
}

// ciVars is a pipeline environment for runCI that records every variable
// read.
type ciVars struct {
	mu   sync.Mutex
	vars map[string]string
	read []string
}

func newCIVars(vars map[string]string) *ciVars {
	all := map[string]string{"GITHUB_TOKEN": ciGitHubToken, "CI_JOB_TOKEN": ciJobToken}
	for k, v := range vars {
		all[k] = v
	}
	return &ciVars{vars: all}
}

func (v *ciVars) get(key string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.read = append(v.read, key)
	return v.vars[key]
}

func (v *ciVars) keys() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.read...)
}

// ciRunResult is one in-process "review-mcp ci" run.
type ciRunResult struct {
	code       int
	out, errs  string
	loads      int
	variables  []string
	lastStderr string
}

// ciCall runs "review-mcp ci args..." in-process with the configuration env
// and the pipeline variables vars, counting the configuration loads.
func ciCall(t *testing.T, env, vars map[string]string, args ...string) ciRunResult {
	t.Helper()
	var out, errb bytes.Buffer
	loads := 0
	load := func() (*config.Config, *config.Report, error) {
		loads++
		return loaderFor(env)()
	}
	v := newCIVars(vars)
	code := runCI(args, &out, &errb, load, v.get)
	return ciRunResult{code: code, out: out.String(), errs: errb.String(), loads: loads,
		variables: v.keys(), lastStderr: lastLine(errb.String())}
}

// lastLine is the last non-empty line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// assertCIClean checks that no secret reached stdout or stderr, and that
// only the allowed pipeline variables were read.
func assertCIClean(t *testing.T, r ciRunResult) {
	t.Helper()
	for _, s := range append(slices.Clone(diagSecrets), ciGitHubToken, ciJobToken) {
		if strings.Contains(r.out, s) || strings.Contains(r.errs, s) {
			t.Errorf("output leaks %q", s)
		}
	}
	for _, k := range r.variables {
		if !slices.Contains(ciAllowedVariables, k) {
			t.Errorf("ci read the environment variable %s", k)
		}
	}
}

// validateCIOutput validates the --json output against a tool's declared
// output schema the way the MCP SDK validates a tool result (see
// mcpserver.validateOutput, WP-10d).
func validateCIOutput(t *testing.T, schema map[string]any, out string) {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("output schema does not decode: %v", err)
	}
	resolved, err := s.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		t.Fatalf("output schema does not resolve: %v", err)
	}
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("stdout is not one JSON value: %v\n%.300s", err, out)
	}
	if err := resolved.ApplyDefaults(&v); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(&v); err != nil {
		t.Errorf("--json output does not match the declared schema: %v", err)
	}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// ciIssueMarker is in the issue context file fixtures. It reaches the model
// and never the output of a usage error.
const ciIssueMarker = "FAKE-ISSUE-TITLE-QX42"

func TestCIUsageErrorsLoadNothing(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	env := reviewDiagEnv(g, l)
	pr := prURLOf(g, 7)
	tooMany := `[{"key":"A-1","title":"t"},{"key":"A-2","title":"t"},{"key":"A-3","title":"t"},{"key":"A-4","title":"t"}]`
	related := []string{}
	for range 5 {
		related = append(related, "--related", prURLOf(g, 8))
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no command", nil, "usage:"},
		{"unknown command", []string{"ask", "--pr-url", pr}, "usage:"},
		{"unknown flag", []string{"review", "--pr-url", pr, "--bogus"}, "flag provided but not defined"},
		{"flag of another command", []string{"improve", "--pr-url", pr, "--focus", "security"}, "flag provided but not defined"},
		{"positional argument", []string{"review", pr}, "ci review: unexpected argument"},
		{"short timeout", []string{"review", "--pr-url", pr, "--timeout", "30s"}, "--timeout must be"},
		{"bad timeout", []string{"review", "--pr-url", pr, "--timeout", "soon"}, "invalid value"},
		{"max findings", []string{"review", "--pr-url", pr, "--max-findings", "0"}, "ci review: max_findings must be an integer from 1 to 20"},
		{"output language", []string{"describe", "--pr-url", pr, "--output-language", "not a locale"}, "ci describe: output_language must be"},
		{"focus with publish", []string{"review", "--pr-url", pr, "--focus", "security", "--publish"}, "ci review: a focused review does not publish"},
		{"bad focus", []string{"review", "--pr-url", pr, "--focus", "a\nb"}, "ci review: focus must be"},
		{"too many related", append([]string{"review", "--pr-url", pr}, related...), "ci review: related"},
		{"related with focus", []string{"review", "--pr-url", pr, "--focus", "security", "--related", prURLOf(g, 8)}, "ci review: related"},
		{"missing issue file", []string{"review", "--pr-url", pr, "--issue-context-file", filepath.Join(t.TempDir(), "none.json")}, "--issue-context-file: the file cannot be read"},
		{"empty issue path", []string{"review", "--pr-url", pr, "--issue-context-file", ""}, "--issue-context-file: the path is empty"},
		{"invalid issue file", []string{"review", "--pr-url", pr, "--issue-context-file", writeFile(t, "bad.json", `{"key":"`+ciIssueMarker+`"}`)}, "--issue-context-file: the file must hold one JSON array"},
		{"unknown issue field", []string{"describe", "--pr-url", pr, "--issue-context-file", writeFile(t, "u.json", `[{"key":"A-1","title":"t","`+ciIssueMarker+`":1}]`)}, "--issue-context-file: the file must hold one JSON array"},
		{"empty issue list", []string{"review", "--pr-url", pr, "--issue-context-file", writeFile(t, "e.json", `[]`)}, "issue_context must hold 1 to 3 issues"},
		{"too many issues", []string{"review", "--pr-url", pr, "--issue-context-file", writeFile(t, "m.json", tooMany)}, "issue_context must hold 1 to 3 issues"},
		{"issue without title", []string{"review", "--pr-url", pr, "--issue-context-file", writeFile(t, "t.json", `[{"key":"`+ciIssueMarker+`"}]`)}, "issue_context[1].title is required"},
		{"large issue file", []string{"review", "--pr-url", pr, "--issue-context-file", writeFile(t, "l.json", `[{"key":"A-1","title":"`+strings.Repeat("x", ciMaxIssueFileBytes)+`"}]`)}, "the file is larger than 1 MiB"},
		{"no pull request", []string{"review"}, "no pull request: pass --pr-url (detected: none)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ciCall(t, env, nil, tc.args...)
			if r.code != 2 || r.out != "" || !strings.Contains(r.errs, tc.want) {
				t.Errorf("exit %d, stdout %q, stderr lacks %q:\n%s", r.code, r.out, tc.want, r.errs)
			}
			if r.loads != 0 {
				t.Errorf("a usage error loaded the configuration %d times", r.loads)
			}
			if strings.Contains(r.errs, ciIssueMarker) {
				t.Error("a usage error echoed the issue context file")
			}
			assertCIClean(t, r)
		})
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("usage errors sent requests: provider %d, llm %d", g.requests(), l.hits.Load())
	}
}

func TestCIDispatch(t *testing.T) {
	var out, errb bytes.Buffer
	loads := 0
	load := func() (*config.Config, *config.Report, error) { loads++; return loaderFor(validEnv())() }
	if code := runWith([]string{"ci"}, strings.NewReader(""), &out, &errb, load); code != 2 || loads != 0 ||
		!strings.Contains(errb.String(), "review-mcp ci review") {
		t.Errorf("exit %d loads %d stderr %q", code, loads, errb.String())
	}
	errb.Reset()
	usage(&errb)
	if !strings.Contains(errb.String(), "  ci <review|describe|improve>") {
		t.Errorf("the top-level usage does not list ci:\n%s", errb.String())
	}
}

func TestCIReview(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	r := ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", prURLOf(g, 8))
	if r.code != 0 || r.loads != 1 {
		t.Fatalf("exit %d, loads %d; stderr:\n%s", r.code, r.loads, r.errs)
	}
	if !strings.HasPrefix(r.out, "## PR Review") || !strings.Contains(r.out, "Check the replacement") {
		t.Errorf("unexpected markdown:\n%s", r.out)
	}
	if r.lastStderr != "ci review: printed only (no --publish)" {
		t.Errorf("final line = %q", r.lastStderr)
	}
	if len(g.posts()) != 0 {
		t.Error("ci review posted without --publish")
	}
	if strings.Contains(r.errs, diffBodyMarker) || strings.Contains(r.errs, "Check the replacement") {
		t.Error("diff or review text reached stderr")
	}
	assertCIClean(t, r)

	// --json prints the pr_review structured result, valid against the
	// declared output schema.
	r = ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", prURLOf(g, 8), "--json", "--max-findings", "3",
		"--output-language", "tr-TR", "--incremental", "--timeout", "2m")
	if r.code != 0 {
		t.Fatalf("--json: exit %d; stderr:\n%s", r.code, r.errs)
	}
	validateCIOutput(t, review.ResultSchema(), r.out)
	var res review.Result
	if err := json.Unmarshal([]byte(r.out), &res); err != nil || res.Review == nil || len(res.Review.KeyIssuesToReview) != 1 {
		t.Errorf("--json = %v %+v", err, res)
	}
	assertCIClean(t, r)
}

func TestCIReviewPublish(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	r := ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", prURLOf(g, 7), "--publish")
	if r.code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", r.code, r.errs)
	}
	posts := g.posts()
	if len(posts) != 1 || !strings.Contains(posts[0], "PR Review") {
		t.Fatalf("posted = %v", posts)
	}
	if r.lastStderr != "ci review: posted overview 55" {
		t.Errorf("final line = %q", r.lastStderr)
	}
	for _, s := range []string{fakeLLMKey, diagGiteaToken, ciGitHubToken, ciJobToken} {
		if strings.Contains(posts[0], s) {
			t.Errorf("the posted comment leaks %q", s)
		}
	}
	assertCIClean(t, r)

	// A failed publish still prints the review and exits 1.
	g.failPost.Store(true)
	r = ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", prURLOf(g, 7), "--publish")
	if r.code != 1 || !strings.HasPrefix(r.out, "## PR Review") || !strings.HasPrefix(r.lastStderr, "ci review: the result was not posted: ") {
		t.Errorf("failed publish: exit %d, final line %q", r.code, r.lastStderr)
	}
	assertCIClean(t, r)
}

func TestCIIssueContextFile(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	file := writeFile(t, "issue.json", `[{"key":"PROJ-1","title":"`+ciIssueMarker+`","acceptance_criteria":["one","two"]}]`)
	r := ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", prURLOf(g, 8), "--issue-context-file", file, "--json")
	if r.code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", r.code, r.errs)
	}
	reqs := l.requests()
	if len(reqs) != 1 || !strings.Contains(reqs[0], ciIssueMarker) {
		t.Fatalf("the issue did not reach the model (%d requests)", len(reqs))
	}
	var res review.Result
	if err := json.Unmarshal([]byte(r.out), &res); err != nil || res.Issue == nil || res.Issue.Source != "argument" {
		t.Errorf("issue = %v %+v", err, res.Issue)
	}
	if strings.Contains(r.errs, ciIssueMarker) {
		t.Error("the issue text reached stderr")
	}
	assertCIClean(t, r)
}

func TestCIDescribe(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagDescribeAnswer)
	r := ciCall(t, reviewDiagEnv(g, l), nil, "describe", "--pr-url", prURLOf(g, 7))
	if r.code != 0 || !strings.HasPrefix(r.out, "## Title\n\nRename and extend\n") {
		t.Fatalf("exit %d; stdout:\n%s\nstderr:\n%s", r.code, r.out, r.errs)
	}
	if r.lastStderr != "ci describe: printed only (no --publish)" || len(g.posts())+len(g.patches()) != 0 {
		t.Errorf("final line %q; posts %d, patches %d", r.lastStderr, len(g.posts()), len(g.patches()))
	}
	assertCIClean(t, r)

	// --publish writes the description region, as pr_describe does with
	// publish_mode=description; the title is not changed.
	r = ciCall(t, reviewDiagEnv(g, l), nil, "describe", "--pr-url", prURLOf(g, 7), "--publish", "--json")
	if r.code != 0 {
		t.Fatalf("--publish: exit %d; stderr:\n%s", r.code, r.errs)
	}
	validateCIOutput(t, describe.ResultSchema(), r.out)
	var res describe.Result
	if err := json.Unmarshal([]byte(r.out), &res); err != nil || res.Publish == nil || res.Publish.Mode != "description" ||
		!res.Publish.Published || res.Publish.TitleUpdated {
		t.Fatalf("publish = %v %+v", err, res.Publish)
	}
	patches := g.patches()
	if len(patches) != 1 || !strings.Contains(patches[0], "Rename the file") || strings.Contains(patches[0], `"title"`) {
		t.Errorf("patches = %v", patches)
	}
	if r.lastStderr != "ci describe: posted description" {
		t.Errorf("final line = %q", r.lastStderr)
	}
	assertCIClean(t, r)
}

func TestCIImprove(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, diagImproveAnswer)
	r := ciCall(t, reviewDiagEnv(g, l), nil, "improve", "--pr-url", prURLOf(g, 7), "--json")
	if r.code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", r.code, r.errs)
	}
	validateCIOutput(t, improve.ResultSchema(), r.out)
	if r.lastStderr != "ci improve: printed only (no --publish)" || len(g.posts()) != 0 {
		t.Errorf("final line %q; posts %d", r.lastStderr, len(g.posts()))
	}
	assertCIClean(t, r)

	r = ciCall(t, reviewDiagEnv(g, l), nil, "improve", "--pr-url", prURLOf(g, 7), "--publish")
	if r.code != 0 || !strings.HasPrefix(r.out, "## Suggestions") {
		t.Fatalf("--publish: exit %d; stderr:\n%s", r.code, r.errs)
	}
	if posts := g.posts(); len(posts) != 1 || !strings.Contains(posts[0], "Name the constant") {
		t.Errorf("posted = %v", posts)
	}
	if r.lastStderr != "ci improve: posted overview 55" {
		t.Errorf("final line = %q", r.lastStderr)
	}
	assertCIClean(t, r)
}

func TestCIRunFailures(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 401, "")
	r := ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", prURLOf(g, 8))
	if r.code != 1 || r.out != "" || !strings.HasPrefix(r.lastStderr, "ci review: ") ||
		!strings.Contains(r.lastStderr, "rejected the credentials") {
		t.Errorf("LLM 401: exit %d stdout %q final line %q", r.code, r.out, r.lastStderr)
	}
	assertCIClean(t, r)

	// An unconfigured host is refused by the resolver: nothing is sent.
	before := g.requests() + l.hits.Load()
	r = ciCall(t, reviewDiagEnv(g, l), nil, "review", "--pr-url", "https://other.example.net/octo/demo/pulls/7")
	want := (&provider.Error{Class: provider.ClassURLNotConfigured}).Error()
	if r.code != 1 || !strings.HasPrefix(r.lastStderr, "ci review: "+want) {
		t.Errorf("unconfigured host: exit %d final line %q", r.code, r.lastStderr)
	}
	if after := g.requests() + l.hits.Load(); after != before {
		t.Errorf("an unconfigured host caused %d requests", after-before)
	}

	// An invalid configuration exits 1 and sends nothing.
	bad := reviewDiagEnv(g, l)
	bad["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12"
	before = g.requests() + l.hits.Load()
	r = ciCall(t, bad, nil, "review", "--pr-url", prURLOf(g, 8))
	if r.code != 1 || r.out != "" || !strings.Contains(r.errs, "llm.context_window") || g.requests()+l.hits.Load() != before {
		t.Errorf("invalid config: exit %d stderr %q", r.code, r.errs)
	}
}

// TestCITimeout: the --timeout deadline ends the run with exit 1 and the
// timeout class, whatever the cut-off request reported.
func TestCITimeout(t *testing.T) {
	g := newFakeGitea(t)
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(release) })
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_BASE_URL"] = hung.URL + "/v1"
	cfg, _, err := loaderFor(env)()
	if err != nil {
		t.Fatal(err)
	}
	o := &ciOptions{cmd: ciReview, timeout: 300 * time.Millisecond}
	o.setPRURL(prURLOf(g, 8))
	var out, errb bytes.Buffer
	start := time.Now()
	code := ciExecute(context.Background(), o, cfg, logging.New(&errb, logLevel(cfg, nil)), newCIVars(nil).get, &out, &errb)
	if code != 1 || out.Len() != 0 {
		t.Fatalf("exit %d stdout %q", code, out.String())
	}
	if got := lastLine(errb.String()); got != "ci review: "+ciTimeoutLine {
		t.Errorf("final line = %q", got)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("the run took %v after a 300ms timeout", d)
	}
}

func TestCIRepoCacheDefault(t *testing.T) {
	tmp := t.TempDir()
	for _, tc := range []struct {
		name, set, runnerTemp, want string
	}{
		{"runner temp", "", tmp, filepath.Join(tmp, "review-mcp-repos")},
		{"configured wins", filepath.Join(tmp, "own"), tmp, filepath.Join(tmp, "own")},
		{"no runner temp", "", "", ""},
		{"relative runner temp", "", "relative/dir", ""},
	} {
		cfg := &config.Config{}
		cfg.Context.Repo.CacheDir = tc.set
		ciRepoCacheDefault(cfg, newCIVars(map[string]string{"RUNNER_TEMP": tc.runnerTemp}).get)
		if cfg.Context.Repo.CacheDir != tc.want {
			t.Errorf("%s: cache_dir = %q, want %q", tc.name, cfg.Context.Repo.CacheDir, tc.want)
		}
	}
}

// TestE2ECILeak [canary] runs the real binary's ci commands with debug
// logging, the pipeline's own tokens in the environment and a publish, and
// scans both streams for every secret.
func TestE2ECILeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the build-and-run end-to-end test in -short mode")
	}
	bin := buildBinary(t)
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	env := reviewDiagEnv(g, l) // includes REVIEW_MCP_LOG_LEVEL=debug
	env["GITHUB_TOKEN"], env["CI_JOB_TOKEN"] = ciGitHubToken, ciJobToken
	secrets := append(slices.Clone(diagSecrets), ciGitHubToken, ciJobToken)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"ci", "review", "--pr-url", prURLOf(g, 7), "--publish"}, 0},
		{[]string{"ci", "review", "--pr-url", prURLOf(g, 8), "--json"}, 0},
		{[]string{"ci", "review", "--pr-url", "https://other.example.net/octo/demo/pulls/7"}, 1},
		{[]string{"ci", "review"}, 2},
	} {
		code, out, errs := runBinary(t, bin, env, tc.args...)
		if code != tc.code {
			t.Errorf("%v: exit %d, want %d; stderr:\n%s", tc.args, code, tc.code, errs)
		}
		for _, s := range secrets {
			if strings.Contains(out, s) || strings.Contains(errs, s) {
				t.Errorf("%v leaks %q", tc.args, s)
			}
		}
		if tc.code == 0 && !strings.Contains(errs, "level=DEBUG") {
			t.Errorf("%v: expected debug logs on stderr", tc.args)
		}
	}
	for _, p := range g.posts() {
		for _, s := range secrets {
			if strings.Contains(p, s) {
				t.Errorf("a posted comment leaks %q", s)
			}
		}
	}
}

// TestCINeverReadsCITokens [canary]: the pipeline's own tokens
// (GITHUB_TOKEN, CI_JOB_TOKEN) are never read, in any command or path; the
// provider token comes only from REVIEW_MCP_* (V-4 §1.2 item 5). The
// mapping is the pipeline's, explicit.
func TestCINeverReadsCITokens(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	summary := writeFile(t, "summary.md", "")
	vars := with(actionsVars(t, "pull_request"), "GITEA_ACTIONS", "true", "GITHUB_SERVER_URL", g.giteaBase(),
		"GITHUB_STEP_SUMMARY", summary, "RUNNER_TEMP", t.TempDir())
	for _, args := range [][]string{
		{"review", "--publish", "--fail-on", "any"},
		{"review", "--pr-url", prURLOf(g, 8)},
		{"describe"},
		{"improve", "--json"},
		{"review", "--timeout", "1s"},
	} {
		r := ciCall(t, reviewDiagEnv(g, l), vars, args...)
		for _, k := range r.variables {
			if k == "GITHUB_TOKEN" || k == "CI_JOB_TOKEN" || !slices.Contains(ciAllowedVariables, k) {
				t.Errorf("%v: read the environment variable %s", args, k)
			}
		}
		assertCIClean(t, r)
	}
	data, err := os.ReadFile(summary) //nolint:gosec // G304: a file under t.TempDir()
	if err != nil || strings.Contains(string(data), ciGitHubToken) || strings.Contains(string(data), ciJobToken) {
		t.Errorf("the step summary: %v", err)
	}
}
