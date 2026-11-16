package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/tools/evalset/cases"
)

// The test binary doubles as a fake review-mcp: with FAKE_REVIEW_MCP=1 it
// answers version, diag cache, the help texts and the reviews from the
// corpus, deterministically. It refuses to review when any EVALSET_
// variable reached it.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_REVIEW_MCP") == "1" {
		os.Exit(fakeReviewMCP(os.Args[1:]))
	}
	os.Exit(m.Run())
}

const fakeModel = "reviewer-model-a"

func fakeReviewMCP(args []string) int {
	if log := os.Getenv("FAKE_LOG"); log != "" {
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil { //nolint:gosec // G304: a test file
			_, _ = fmt.Fprintln(f, strings.Join(args, " "))
			_ = f.Close()
		}
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(kv), "EVALSET_") {
			fmt.Fprintln(os.Stderr, "fake review-mcp: an EVALSET_ variable reached the binary")
			return 7
		}
	}
	switch strings.Join(args, " ") {
	case "version":
		fmt.Println("review-mcp 0.0.0-fake (test)")
		return 0
	case "diag cache":
		fmt.Printf("{\"cache_dir\": %q}\n", os.Getenv("FAKE_CACHE_DIR"))
		return 0
	case "diag review --help":
		if os.Getenv("FAKE_HELP") == "old" {
			fmt.Fprintln(os.Stderr, "review-mcp diag review <PR_URL> [--dry-run] [--json]")
		} else {
			fmt.Fprintln(os.Stderr, "review-mcp diag review <PR_URL> [--dry-run] [--json] [--repo-context on|off]")
		}
		return 2
	case "local review --help":
		fmt.Fprintln(os.Stderr, "review-mcp local review [--path DIR] [--base REF] [--json]")
		return 2
	}
	var prs prsFile
	if b, err := os.ReadFile(os.Getenv("FAKE_PRS")); err != nil || json.Unmarshal(b, &prs) != nil { //nolint:gosec // G703: a test file
		fmt.Fprintln(os.Stderr, "fake review-mcp: no FAKE_PRS")
		return 9
	}
	repoContext := "used"
	var id string
	switch {
	case len(args) >= 4 && args[0] == "diag" && args[1] == "review" && args[2] == "--json":
		for _, e := range prs.Cases {
			if e.URL == args[len(args)-1] {
				id = e.ID
			}
		}
		if slices.Contains(args, "--repo-context=off") {
			repoContext = "off"
		}
	case len(args) >= 7 && args[0] == "local" && args[1] == "review" && args[2] == "--json" && args[3] == "--base" && args[4] == "main" && args[5] == "--path":
		for _, e := range prs.Cases {
			if e.Path == args[6] {
				id = e.ID
			}
		}
		if os.Getenv("REVIEW_MCP_CONTEXT_REPO_ENABLED") == "false" {
			repoContext = "off"
		}
	}
	if id == "" {
		fmt.Fprintln(os.Stderr, "fake review-mcp: unknown arguments")
		return 2
	}
	if id == os.Getenv("FAKE_FAIL_CASE") {
		fmt.Fprintln(os.Stderr, "fake review-mcp: the model endpoint failed")
		return 1
	}
	all, err := cases.Load(os.DirFS(os.Getenv("FAKE_CORPUS")))
	if err != nil {
		return 9
	}
	var c *cases.Case
	for _, x := range all {
		if x.ID == id {
			c = x
		}
	}
	// With context, each issue is found at its location; without, the main
	// issue only by a mention from a changed file. Every review adds one
	// finding that matches nothing.
	var findings []finding
	changed := ""
	for p := range c.Head {
		if changed == "" || p < changed {
			changed = p
		}
	}
	for _, is := range c.Issues {
		if repoContext == "used" {
			findings = append(findings, finding{File: is.Path, Start: is.Lines[0], End: is.Lines[1], Header: "Issue " + is.ID, Content: "Found at its place."})
		} else if is.Severity != "low" {
			findings = append(findings, finding{Header: "Caller",
				Content: "Check " + filepath.Base(is.Path) + " for " + strings.Join(is.MustMention, " and ") + "."})
		}
	}
	findings = append(findings, finding{File: changed, Start: 1, End: 1, Header: "Naming", Content: "A clearer name would help."})
	res := map[string]any{
		"review":   map[string]any{"key_issues_to_review": findings},
		"coverage": map[string]any{"model_calls": 1, "repo_context": map[string]any{"status": repoContext, "symbols": 3, "references": 2, "files": 1}},
		"metadata": map[string]any{"model": fakeModel, "models_used": []string{fakeModel}, "llm_calls": 1, "request_tokens": 1234, "tool_rounds": 0},
	}
	b, _ := json.Marshal(res)
	fmt.Println(string(b))
	return 0
}

// fakeEnv points the fake binary at its inputs. The secrets are set in the
// process environment too, to show that the binary never inherits them.
func fakeEnv(t *testing.T, prs string, secrets ...string) {
	t.Helper()
	abs, err := filepath.Abs(corpusDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_REVIEW_MCP", "1")
	t.Setenv("FAKE_CORPUS", abs)
	t.Setenv("FAKE_PRS", prs)
	t.Setenv("FAKE_CACHE_DIR", t.TempDir())
	t.Setenv("FAKE_FAIL_CASE", "")
	t.Setenv("FAKE_HELP", "")
	t.Setenv("FAKE_LOG", "")
	for i := 0; i+1 < len(secrets); i += 2 {
		t.Setenv(secrets[i], secrets[i+1])
	}
}

// noSecret fails when a secret appears in any file under the directories
// or in the captured output.
func noSecret(t *testing.T, secrets []string, output string, dirs ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(output, s) {
			t.Errorf("a secret appears in the captured output")
		}
	}
	files := 0
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			files++
			b, err := os.ReadFile(p) //nolint:gosec // G304: a test directory
			if err != nil {
				return err
			}
			for _, s := range secrets {
				if strings.Contains(string(b), s) {
					t.Errorf("a secret appears in %s", p)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files == 0 {
		t.Fatalf("no file was written; the leak check would prove nothing")
	}
}

var e2eCases = "go-cascade-wrapped-error,go-clean-wrapped-error-twin,python-local-off-by-one"

func readMatches(t *testing.T, dir string) [][]string {
	t.Helper()
	rows, err := readCSV(filepath.Join(dir, "matches.csv"))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// checkScored checks the three outputs of a scored dry run and the rules
// the fake reviews exercise.
func checkScored(t *testing.T, out string, variants int) {
	t.Helper()
	for _, f := range []string{"report.md", "metrics.json", "matches.csv"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("%s is missing", f)
		}
	}
	var rep report
	if err := readJSON(filepath.Join(out, "metrics.json"), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Metrics) != variants || rep.Corpus.Cases != 3 || rep.Corpus.Issues != 3 || rep.Corpus.LowIssues != 1 {
		t.Errorf("metrics.json: %d variants, corpus %+v", len(rep.Metrics), rep.Corpus)
	}
	// With context every issue is found: recall 1. Without, the main
	// issues by mention: 2 of 3.
	if r := rep.Metrics["on"]["recall"].Mean; r == nil || *r != 1 {
		t.Errorf("recall on = %v, want 1", r)
	}
	if r := rep.Metrics["off"]["recall"].Mean; r == nil || *r != 0.6667 {
		t.Errorf("recall off = %v, want 0.6667", r)
	}
	if r := rep.Metrics["on"]["findings_per_clean_pr"].Mean; r == nil || *r != 1 {
		t.Errorf("findings per clean pull request = %v, want 1", r)
	}
	rules := map[string]int{}
	for _, row := range readMatches(t, out)[1:] {
		rules[row[8]]++
	}
	if rules[ruleLocation] == 0 || rules[ruleMention] == 0 || rules["missed"] == 0 {
		t.Errorf("matches.csv rules = %v; want location, mention and missed rows", rules)
	}
	md, _ := os.ReadFile(filepath.Join(out, "report.md")) //nolint:gosec // G304: a test directory
	for _, want := range []string{"| Precision |", "| Cascade recall |", "| 2,000-token case (go-large-cascade) |", "| Recall, tag body-only |", "of which 1 low"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("report.md lacks %q", want)
		}
	}
	if strings.Contains(string(md), "A clearer name would help") || strings.Contains(string(md), "Found at its place") {
		t.Errorf("report.md holds review text beyond titles")
	}
}

// TestEndToEndGitea is the dry run and the leak test of §2.4: setup on a
// fake Gitea, run with a fake review-mcp, score with a fake judge, over 3
// cases and the variants off and on. A distinctive Gitea token and judge
// key never appear in any file under the -out directories or in the
// captured output.
func TestEndToEndGitea(t *testing.T) {
	const token = "gitea-token-LEAKCHECK-5e3d1a77" //nolint:gosec // G101: a synthetic test value
	const key = "judge-key-LEAKCHECK-9b7c2f04"     //nolint:gosec // G101: a synthetic test value
	f := newFakeGitea(t, "evalbot", token)
	judge, calls := fakeJudge(t, key, "valid_unlisted")
	setupOut, runOut, scoreOut, cache := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	fakeEnv(t, filepath.Join(setupOut, "prs.json"), envGiteaToken, token, envJudgeKey, key)
	env := map[string]string{envGiteaToken: token, envJudgeURL: judge.URL + "/v1", envJudgeModel: "judge-model-b", envJudgeKey: key}
	var output strings.Builder
	step := func(want int, args ...string) {
		t.Helper()
		code, stdout, stderr := evalset(t, env, args...)
		output.WriteString(stdout + stderr)
		if code != want {
			t.Fatalf("%s: exit %d, want %d: %s%s", args[0], code, want, stdout, stderr)
		}
	}
	step(0, "setup", "-gitea-url", f.srv.URL, "-owner", "evalbot", "-corpus", corpusDir, "-cases", e2eCases, "-cache-dir", cache, "-out", setupOut)
	step(0, "run", "-bin", os.Args[0], "-prs", filepath.Join(setupOut, "prs.json"), "-variants", "off,on", "-cache-dir", cache, "-out", runOut)
	step(0, "score", "-runs", runOut, "-corpus", corpusDir, "-judge", "-out", scoreOut)
	checkScored(t, scoreOut, 2)
	if f.authOK == 0 || calls.Load() == 0 {
		t.Fatalf("the fake Gitea (%d) or the judge (%d) saw no authenticated request; the leak check would prove nothing", f.authOK, calls.Load())
	}
	for _, name := range []string{"sample.csv", "sample_key.csv", "valid_unlisted.csv", "README.txt"} {
		if _, err := os.Stat(filepath.Join(scoreOut, name)); err != nil {
			t.Errorf("%s is missing", name)
		}
	}
	var rf runFile
	if err := readJSON(filepath.Join(runOut, "run.json"), &rf); err != nil {
		t.Fatal(err)
	}
	if len(rf.Runs) != 6 || rf.BinaryVersion != "review-mcp 0.0.0-fake (test)" || rf.Variants[1].Flags[0] != "--repo-context=on" {
		t.Errorf("run.json: %d runs, version %q, variants %+v", len(rf.Runs), rf.BinaryVersion, rf.Variants)
	}
	for _, r := range rf.Runs {
		if !r.OK || r.Model != fakeModel || r.LLMCalls != 1 || r.WallMS < 0 {
			t.Errorf("run %+v", r)
		}
	}
	noSecret(t, []string{token, key}, output.String(), setupOut, runOut, scoreOut)

	// evalset agree over the sample: unlabeled, then labeled.
	code, stdout, _ := evalset(t, nil, "agree", "-sample", filepath.Join(scoreOut, "sample.csv"))
	if code != 0 || !strings.Contains(stdout, "judge agreement not measured") {
		t.Errorf("agree on an unlabeled sample: exit %d, %q", code, stdout)
	}
	rows, _ := readCSV(filepath.Join(scoreOut, "sample.csv"))
	for i := 1; i < len(rows); i++ {
		rows[i][7] = "valid_unlisted"
		if i == 1 {
			rows[i][7] = "invalid"
		}
	}
	if err := writeCSV(filepath.Join(scoreOut, "sample.csv"), rows); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = evalset(t, nil, "agree", "-sample", filepath.Join(scoreOut, "sample.csv"))
	n := len(rows) - 1
	if want := fmt.Sprintf("evalset agree: %d of %d items labeled (0 labels not understood); the judge agrees on %d", n, n, n-1); code != 0 || !strings.HasPrefix(stdout, want) {
		t.Errorf("agree: exit %d, %q; want %q", code, stdout, want)
	}
}

// TestEndToEndLocal runs the local mode (setup -local, run -mode local)
// with all three variants; it needs no git http-backend, so it runs on
// every OS.
func TestEndToEndLocal(t *testing.T) {
	needGit(t)
	const key = "judge-key-LOCAL-3f8e6d21" //nolint:gosec // G101: a synthetic test value
	judge, _ := fakeJudge(t, key, "invalid")
	clones, setupOut, runOut, scoreOut, cache := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	fakeEnv(t, filepath.Join(setupOut, "prs.json"), envJudgeKey, key)
	env := map[string]string{envJudgeURL: judge.URL + "/v1", envJudgeModel: "judge-model-b", envJudgeKey: key}
	var output strings.Builder
	for _, args := range [][]string{
		{"setup", "-local", clones, "-corpus", corpusDir, "-cases", e2eCases, "-cache-dir", cache, "-out", setupOut},
		{"run", "-bin", os.Args[0], "-prs", filepath.Join(setupOut, "prs.json"), "-mode", "local", "-variants", "off,on,on-10k", "-cache-dir", cache, "-out", runOut},
		{"score", "-runs", runOut, "-corpus", corpusDir, "-judge", "-out", scoreOut},
	} {
		code, stdout, stderr := evalset(t, env, args...)
		output.WriteString(stdout + stderr)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s%s", args[0], code, stdout, stderr)
		}
	}
	checkScored(t, scoreOut, 3)
	var rf runFile
	if err := readJSON(filepath.Join(runOut, "run.json"), &rf); err != nil {
		t.Fatal(err)
	}
	if rf.Mode != "local" || len(rf.Runs) != 9 || !slices.Contains(rf.Variants[2].Env, "REVIEW_MCP_CONTEXT_REPO_MAX_TOKENS=10000") {
		t.Errorf("run.json: mode %s, %d runs, variants %+v", rf.Mode, len(rf.Runs), rf.Variants)
	}
	noSecret(t, []string{key}, output.String(), setupOut, runOut, scoreOut)
}

// TestRunRefusesUnsupportedVariants: a variant the binary does not support
// is refused before any review.
func TestRunRefusesUnsupportedVariants(t *testing.T) {
	prs := filepath.Join(t.TempDir(), "prs.json")
	if err := writeJSON(prs, prsFile{Version: 1, Mode: "gitea", Cases: []prEntry{{ID: "go-clean-docs", URL: "http://gitea.example.com/o/r/pulls/1"}}}); err != nil {
		t.Fatal(err)
	}
	fakeEnv(t, prs)
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_HELP", "old")
	out := t.TempDir()
	code, _, stderr := evalset(t, nil, "run", "-bin", os.Args[0], "-prs", prs, "-variants", "off,on-10k", "-cache-dir", t.TempDir(), "-out", out)
	if code != 2 || !strings.Contains(stderr, "the binary does not support diag review (--repo-context is not in `diag review --help`)") {
		t.Errorf("old binary: exit %d, stderr %q", code, stderr)
	}
	code, _, stderr = evalset(t, nil, "run", "-bin", os.Args[0], "-prs", prs, "-variants", "off,impact", "-cache-dir", t.TempDir(), "-out", out)
	if code != 2 || !strings.Contains(stderr, `unknown variant "impact"; the built-in variants are off, on and on-10k`) {
		t.Errorf("unknown variant: exit %d, stderr %q", code, stderr)
	}
	calls, _ := os.ReadFile(log) //nolint:gosec // G304: a test file
	if strings.Contains(string(calls), "--json") {
		t.Errorf("a review ran before the refusal:\n%s", calls)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("-out holds %d entries after the refusal", len(entries))
	}
}

// TestFailedRunIsRecorded: a run whose binary fails is recorded as failed,
// stored without a result, and scored as "no answer".
func TestFailedRunIsRecorded(t *testing.T) {
	needGit(t)
	clones, setupOut, runOut, scoreOut, cache := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	fakeEnv(t, filepath.Join(setupOut, "prs.json"))
	t.Setenv("FAKE_FAIL_CASE", "go-clean-wrapped-error-twin")
	if code, _, stderr := evalset(t, nil, "setup", "-local", clones, "-corpus", corpusDir, "-cases", e2eCases, "-cache-dir", cache, "-out", setupOut); code != 0 {
		t.Fatal(stderr)
	}
	code, stdout, _ := evalset(t, nil, "run", "-bin", os.Args[0], "-prs", filepath.Join(setupOut, "prs.json"), "-variants", "on", "-cache-dir", cache, "-out", runOut)
	if code != 1 || !strings.Contains(stdout, "evalset run: 3 runs, 1 failed") {
		t.Errorf("run: exit %d, %q", code, stdout)
	}
	if code, _, stderr := evalset(t, nil, "score", "-runs", runOut, "-corpus", corpusDir, "-out", scoreOut); code != 0 {
		t.Fatal(stderr)
	}
	var rep report
	if err := readJSON(filepath.Join(scoreOut, "metrics.json"), &rep); err != nil {
		t.Fatal(err)
	}
	on := rep.Metrics["on"]
	if f := on["failed_runs"].Mean; f == nil || *f != 1 {
		t.Errorf("failed_runs %v", f)
	}
	// The failed clean twin is not a clean case with zero findings: the
	// clean figures are undefined, not 0 findings and 100% zero.
	if v := on["findings_per_clean_pr"].Mean; v != nil {
		t.Errorf("findings per clean pull request %v, want undefined", *v)
	}
	runs := rep.Cases["go-clean-wrapped-error-twin"]["on"]
	if len(runs) != 1 || !runs[0].Failed {
		t.Errorf("the case's run is %+v, want failed", runs)
	}
	found := false
	for _, row := range readMatches(t, scoreOut) {
		if row[0] == "go-clean-wrapped-error-twin" && row[8] == "failed" {
			found = true
		}
	}
	if !found {
		t.Errorf("matches.csv has no failed row for the case")
	}
}

// TestBuiltinVariantsCarryNoSecret: a variant's environment never carries a
// secret (amendment 2026-11-16 item 2).
func TestBuiltinVariantsCarryNoSecret(t *testing.T) {
	for _, mode := range []string{"gitea", "local"} {
		for _, v := range builtinVariants(mode) {
			for _, kv := range v.Env {
				k, _, _ := strings.Cut(kv, "=")
				for _, bad := range []string{"TOKEN", "KEY", "SECRET", "PASSWORD"} {
					if strings.Contains(k, bad) && k != maxTokensEnv {
						t.Errorf("%s variant %s sets %s", mode, v.Name, k)
					}
				}
			}
		}
	}
}
