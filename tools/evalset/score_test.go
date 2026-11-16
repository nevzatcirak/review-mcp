package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/tools/evalset/cases"
)

// scoreCase is a synthetic case for the scorer tests: a high cascade issue
// in src/billing/renewal.go and a low local issue in src/api/handler.go.
func scoreCase() *cases.Case {
	return &cases.Case{ID: "go-cascade-sample", Category: "cascade", Issues: []cases.Issue{
		{ID: "renewal", Severity: "high", Kind: "cascade", Path: "src/billing/renewal.go", Lines: [2]int{20, 24}, Hops: 2,
			MustMention: []string{"RenewalJob", "renew", "currency"}},
		{ID: "handler", Severity: "low", Kind: "local", Path: "src/api/handler.go", Lines: [2]int{10, 10},
			MustMention: []string{"Handle", "status"}},
	}}
}

func TestMatchRules(t *testing.T) {
	tests := []struct {
		name string
		f    finding
		want string // issue id or "" for none
		rule string
	}{
		{"location inside", finding{File: "src/billing/renewal.go", Start: 21, End: 22}, "renewal", ruleLocation},
		{"location widened below", finding{File: "src/billing/renewal.go", Start: 17, End: 17}, "renewal", ruleLocation},
		{"location widened above", finding{File: "src/billing/renewal.go", Start: 27, End: 30}, "renewal", ruleLocation},
		{"location past the widening", finding{File: "src/billing/renewal.go", Start: 28, End: 30}, "", ""},
		{"location before the widening", finding{File: "src/billing/renewal.go", Start: 10, End: 16}, "", ""},
		{"location with a leading ./", finding{File: "./src/billing/renewal.go", Start: 20}, "renewal", ruleLocation},
		{"location without an end line", finding{File: "src/billing/renewal.go", Start: 24}, "renewal", ruleLocation},
		{"other file, other lines", finding{File: "src/billing/other.go", Start: 20}, "", ""},
		{"mention: base name and a word", finding{File: "src/billing/service.go", Start: 5, Content: "renewal.go still stores the default currency"}, "renewal", ruleMention},
		{"mention: identifier and a word", finding{File: "src/billing/service.go", Header: "RenewalJob", Content: "keeps the old currency"}, "renewal", ruleMention},
		{"mention: two identifiers", finding{File: "src/api/routes.go", Content: "Handle in handler.go"}, "handler", ruleMention},
		{"mention: overview only", finding{Content: "RenewalJob does not renew with the currency"}, "renewal", ruleMention},
		// (c2) one entry is not enough: the file name alone does not match.
		{"mention: base name only", finding{File: "src/billing/service.go", Content: "see renewal.go"}, "", ""},
		{"mention: identifier only", finding{File: "src/billing/service.go", Content: "RenewalJob looks odd"}, "", ""},
		{"mention: words only, no anchor", finding{File: "src/billing/service.go", Content: "renew keeps the currency"}, "", ""},
		{"mention: identifier is case-sensitive", finding{File: "src/billing/service.go", Content: "renewaljob keeps the currency"}, "", ""},
		{"mention: words are case-insensitive", finding{File: "src/billing/service.go", Content: "RenewalJob keeps the Currency"}, "renewal", ruleMention},
		{"mention: identifier inside a longer one", finding{File: "src/billing/service.go", Content: "RenewalJobs keep the currency"}, "", ""},
		{"no mention for a finding located elsewhere in the issue file", finding{File: "src/billing/renewal.go", Start: 90, Content: "RenewalJob and currency"}, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := scoreCase()
			got, _ := matchRun(c, []finding{tc.f})
			id := ""
			if got[0].issue >= 0 {
				id = c.Issues[got[0].issue].ID
			}
			if id != tc.want || got[0].rule != tc.rule {
				t.Errorf("matched %q by %q, want %q by %q", id, got[0].rule, tc.want, tc.rule)
			}
		})
	}
}

// TestMentionNeedsTwoEntries is canary (c2): a finding that only names the
// issue's file must not match by the mention rule.
func TestMentionNeedsTwoEntries(t *testing.T) {
	c := scoreCase()
	got, matched := matchRun(c, []finding{{File: "src/billing/service.go", Start: 3, Header: "Stale caller", Content: "Look at renewal.go."}})
	if got[0].issue >= 0 || matched[0] != "" {
		t.Errorf("a finding naming only the file matched issue %s by %s; the mention rule needs two entries", c.Issues[got[0].issue].ID, got[0].rule)
	}
}

func TestOneFindingNeverMatchesTwoIssues(t *testing.T) {
	c := &cases.Case{ID: "x", Category: "local", Issues: []cases.Issue{
		{ID: "a", Severity: "medium", Kind: "local", Path: "f.go", Lines: [2]int{10, 10}, MustMention: []string{"A", "b"}},
		{ID: "b", Severity: "high", Kind: "local", Path: "f.go", Lines: [2]int{12, 12}, MustMention: []string{"B", "c"}},
	}}
	// One finding near both: it goes to the high issue; the medium one is
	// missed.
	got, matched := matchRun(c, []finding{{File: "f.go", Start: 11}})
	if got[0].issue != 1 || matched[0] != "" || matched[1] != ruleLocation {
		t.Errorf("one finding: issue %d, matched %q", got[0].issue, matched)
	}
	// Two findings: each issue gets one, the nearer first.
	got, matched = matchRun(c, []finding{{File: "f.go", Start: 10}, {File: "f.go", Start: 12}})
	if got[0].issue != 0 || got[1].issue != 1 || matched[0] == "" || matched[1] == "" {
		t.Errorf("two findings: issues %d and %d, matched %q", got[0].issue, got[1].issue, matched)
	}
	// Two findings on one issue: the second is a false positive.
	c.Issues = c.Issues[:1]
	got, _ = matchRun(c, []finding{{File: "f.go", Start: 10}, {File: "f.go", Start: 11}})
	if got[0].issue != 0 || got[1].issue != -1 {
		t.Errorf("two findings on one issue: %d and %d", got[0].issue, got[1].issue)
	}
}

// tallyOf scores runs of one variant and repeat.
func tallyOf(runs ...*runScore) *tally {
	t := &tally{}
	for _, rs := range runs {
		t.add(rs)
	}
	return t
}

func scoredRun(c *cases.Case, ok bool, fs ...finding) *runScore {
	rs := &runScore{rec: runRecord{Case: c.ID, Variant: "on", Repeat: 1, OK: ok}, c: c, matched: make([]string, len(c.Issues))}
	if ok {
		rs.findings, rs.matched = matchRun(c, fs)
	}
	return rs
}

func metric(t *testing.T, key string, tl *tally) (float64, bool) {
	t.Helper()
	for _, d := range metricDefs {
		if d.key == key {
			return d.f(tl)
		}
	}
	t.Fatalf("no metric %s", key)
	return 0, false
}

func TestFailedRunIsNotZeroFindings(t *testing.T) {
	c := scoreCase()
	clean := &cases.Case{ID: "go-clean-sample", Category: "clean"}
	ok := scoredRun(c, true, finding{File: "src/billing/renewal.go", Start: 20})
	tl := tallyOf(ok, scoredRun(c, false), scoredRun(clean, false))
	if tl.failed != 2 || tl.runs != 3 {
		t.Fatalf("failed %d of %d runs, want 2 of 3", tl.failed, tl.runs)
	}
	// The failed runs add no issues: recall is over the answered run only.
	if r, _ := metric(t, "recall", tl); r != 0.5 {
		t.Errorf("recall %v, want 0.5 (1 of the answered run's 2 issues)", r)
	}
	// A failed clean run is not a clean run with zero findings.
	if _, defined := metric(t, "findings_per_clean_pr", tl); defined {
		t.Errorf("findings per clean pull request is defined with only a failed clean run")
	}
	if _, defined := metric(t, "clean_zero_share", tl); defined {
		t.Errorf("clean zero share is defined with only a failed clean run")
	}
	if f, _ := metric(t, "failed_runs", tl); f != 2 {
		t.Errorf("failed_runs %v", f)
	}
}

func TestCleanCaseFalsePositives(t *testing.T) {
	clean := &cases.Case{ID: "go-clean-sample", Category: "clean"}
	c := scoreCase()
	tl := tallyOf(
		scoredRun(clean, true, finding{File: "a.go", Start: 1}, finding{File: "b.go", Start: 2}),
		scoredRun(clean, true),
		scoredRun(c, true, finding{File: "src/billing/renewal.go", Start: 22}),
	)
	if v, _ := metric(t, "findings_per_clean_pr", tl); v != 1 {
		t.Errorf("findings per clean pull request %v, want 1 (2 findings over 2 clean runs)", v)
	}
	if v, _ := metric(t, "clean_zero_share", tl); v != 0.5 {
		t.Errorf("clean zero share %v, want 0.5", v)
	}
	if v, _ := metric(t, "precision", tl); v != 1.0/3 {
		t.Errorf("precision %v, want 1/3 (the clean findings are false positives)", v)
	}
	if v, _ := metric(t, "severity_weighted_recall", tl); v != 3.0/4 {
		t.Errorf("severity-weighted recall %v, want 3/4 (high 3 found, low 1 missed)", v)
	}
	if v, _ := metric(t, "cascade_recall", tl); v != 1 {
		t.Errorf("cascade recall %v", v)
	}
	if v, _ := metric(t, "recall_hops_2", tl); v != 1 {
		t.Errorf("recall hops 2 %v", v)
	}
	if v, _ := metric(t, "recall_low", tl); v != 0 {
		t.Errorf("recall low %v", v)
	}
}

func TestTagAndLargeCaseRows(t *testing.T) {
	body := scoreCase()
	body.Tags = []string{"body-only"}
	large := scoreCase()
	large.ID = largeCaseID
	tl := tallyOf(scoredRun(body, true, finding{File: "src/billing/renewal.go", Start: 20}), scoredRun(large, true))
	if v, _ := metric(t, "recall_body_only", tl); v != 0.5 {
		t.Errorf("body-only recall %v, want 0.5", v)
	}
	if v, ok := metric(t, "recall_2000_token_case", tl); !ok || v != 0 {
		t.Errorf("2,000-token case recall %v (%v), want 0", v, ok)
	}
	if v, _ := metric(t, "cascade_recall", tl); v != 0.5 {
		t.Errorf("the large case is not in cascade recall: %v", v)
	}
}

func TestParseVerdict(t *testing.T) {
	ids := []string{"renewal", "handler"}
	tests := []struct{ in, verdict, reason string }{
		{`{"verdict": "matches:renewal", "reason": "same caller"}`, "matches:renewal", "same caller"},
		{"Sure.\n```json\n{\"verdict\":\"valid_unlisted\",\"reason\":\"a real leak\"}\n```", "valid_unlisted", "a real leak"},
		{`{"verdict": "INVALID", "reason": "not a problem"}`, "invalid", "not a problem"},
		{"invalid: the code is fine", "invalid", "the code is fine"},
		{"matches:handler because of the status", "matches:handler", "because of the status"},
		{`{"verdict": "matches:other", "reason": "x"}`, "unparsed", "x"},
		{"I am not sure", "unparsed", ""},
	}
	for _, tc := range tests {
		v, r := parseVerdict(tc.in, ids)
		if v != tc.verdict || r != tc.reason {
			t.Errorf("parseVerdict(%q) = %q, %q; want %q, %q", tc.in, v, r, tc.verdict, tc.reason)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	// The expected text is the output of diff -u for the same files.
	a := splitLines("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\no\np\n")
	b := splitLines("a\nb\nc\nD\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\no\np\nq\n")
	got := unifiedDiff("a/x", "b/x", a, b)
	want := "--- a/x\n+++ b/x\n@@ -1,7 +1,7 @@\n a\n b\n c\n-d\n+D\n e\n f\n g\n@@ -14,3 +14,4 @@\n n\n o\n p\n+q\n"
	if got != want {
		t.Errorf("unifiedDiff =\n%s\nwant\n%s", got, want)
	}
	if got := unifiedDiff("/dev/null", "b/n", nil, []string{"x"}); got != "--- /dev/null\n+++ b/n\n@@ -0,0 +1,1 @@\n+x\n" {
		t.Errorf("new file diff = %q", got)
	}
}

// writeRuns writes a minimal run directory: one run of a case, with the
// reviewer model recorded.
func writeRuns(t *testing.T, model string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "go-clean-docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	res := `{"review":{"key_issues_to_review":[{"relevant_file":"README.md","issue_header":"Typo","issue_content":"A word is misspelled.","start_line":1,"end_line":1}]},"coverage":{"repo_context":{"status":"used"}},"metadata":{"model":"` + model + `","llm_calls":1}}`
	if err := os.WriteFile(filepath.Join(dir, "go-clean-docs", "on-1.json"), []byte(res), 0o600); err != nil {
		t.Fatal(err)
	}
	rf := runFile{Version: 1, Mode: "gitea", BinaryVersion: "review-mcp test", Repeat: 1,
		Variants: []variant{{Name: "on", Flags: []string{"--repo-context=on"}, RepoContext: true}},
		Runs:     []runRecord{{Case: "go-clean-docs", Variant: "on", Repeat: 1, File: "go-clean-docs/on-1.json", OK: true, Model: model, LLMCalls: 1, Findings: 1}}}
	if err := writeJSON(filepath.Join(dir, "run.json"), rf); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeJudge is an OpenAI-compatible endpoint that answers every request
// with the given verdict and counts the requests that carry the key.
func fakeJudge(t *testing.T, key, verdict string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"verdict\":\"` + verdict + `\",\"reason\":\"checked by the fake judge\"}"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestJudgeRefusedWithSameModel is canary (c3): the judge is refused, before
// any request, when its model is the reviewer model recorded in the runs.
func TestJudgeRefusedWithSameModel(t *testing.T) {
	const key = "judge-key-c3-41aa"
	for _, judgeModel := range []string{"model-a", "MODEL-A", "provider/model-a"} {
		t.Run(judgeModel, func(t *testing.T) {
			srv, calls := fakeJudge(t, key, "invalid")
			env := map[string]string{envJudgeURL: srv.URL + "/v1", envJudgeModel: judgeModel, envJudgeKey: key}
			code, _, stderr := evalset(t, env, "score", "-runs", writeRuns(t, "model-a"), "-corpus", corpusDir, "-judge",
				"-cache-dir", t.TempDir(), "-out", t.TempDir())
			if code != 2 || !strings.Contains(stderr, "-judge is refused: the judge model is the reviewer model recorded in the runs") {
				t.Errorf("exit %d, stderr %q; want the judge refused", code, stderr)
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("the judge got %d requests before the refusal", n)
			}
		})
	}
	// A different model is accepted and asked.
	srv, calls := fakeJudge(t, key, "invalid")
	env := map[string]string{envJudgeURL: srv.URL + "/v1", envJudgeModel: "model-b", envJudgeKey: key}
	code, stdout, stderr := evalset(t, env, "score", "-runs", writeRuns(t, "model-a"), "-corpus", corpusDir, "-judge",
		"-cache-dir", t.TempDir(), "-out", t.TempDir())
	if code != 0 || calls.Load() != 1 {
		t.Errorf("model-b: exit %d, %d requests: %s%s", code, calls.Load(), stdout, stderr)
	}
}

func TestJudgeNeedsItsSettings(t *testing.T) {
	code, _, stderr := evalset(t, map[string]string{envJudgeModel: "m"}, "score", "-runs", writeRuns(t, "model-a"), "-corpus", corpusDir,
		"-judge", "-cache-dir", t.TempDir(), "-out", t.TempDir())
	if code != 2 || !strings.Contains(stderr, "-judge needs EVALSET_JUDGE_BASE_URL, EVALSET_JUDGE_MODEL and EVALSET_JUDGE_API_KEY") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestScoreIsDeterministic(t *testing.T) {
	runs := writeRuns(t, "model-a")
	var outs []string
	for range 2 {
		out := t.TempDir()
		if code, _, stderr := evalset(t, nil, "score", "-runs", runs, "-corpus", corpusDir, "-cache-dir", t.TempDir(), "-out", out); code != 0 {
			t.Fatalf("score: %s", stderr)
		}
		var all string
		for _, f := range []string{"report.md", "metrics.json", "matches.csv"} {
			b, err := os.ReadFile(filepath.Join(out, f)) //nolint:gosec // G304: a test directory
			if err != nil {
				t.Fatal(err)
			}
			all += string(b)
		}
		outs = append(outs, all)
	}
	if outs[0] != outs[1] {
		t.Errorf("two scorings of the same runs differ")
	}
}
