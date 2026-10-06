package main

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type diffSkip struct{ Path, Reason string }

type diffOut struct {
	Budget struct {
		ContextWindow int     `json:"context_window"`
		SoftLimit     int     `json:"soft_limit"`
		HardLimit     int     `json:"hard_limit"`
		PromptTokens  int     `json:"prompt_tokens"`
		Factor        float64 `json:"factor"`
	} `json:"budget"`
	FastPath bool     `json:"fast_path"`
	Tokens   int      `json:"tokens"`
	Included []string `json:"included"`
	Omitted  struct {
		Added, Modified, Deleted []string
	} `json:"omitted"`
	Clipped   []string   `json:"clipped"`
	Skipped   []diffSkip `json:"skipped"`
	Filtered  []diffSkip `json:"filtered"`
	ElapsedMS int64      `json:"elapsed_ms"`
}

const diffSep = "--- prepared diff ---\n"

// runDiff runs diag diff on fixture PR n and decodes the header and text.
func runDiff(t *testing.T, env map[string]string, g *fakeHost, n int, args ...string) (diffOut, string, string) {
	t.Helper()
	url := g.srv.URL + "/gitea/octo/demo/pulls/" + strconv.Itoa(n)
	code, out, errs := diag(env, append([]string{"diff", url}, args...)...)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
	raw, rest := decodeReport(t, out)
	_ = raw
	var d diffOut
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&d); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rest, "\n"+diffSep) {
		t.Fatalf("no separator after the header: %q", rest[:min(len(rest), 80)])
	}
	return d, strings.TrimPrefix(rest, "\n"+diffSep), errs
}

func sortedCopy(s []string) []string {
	c := append([]string(nil), s...)
	sort.Strings(c)
	return c
}

func reasonOf(l []diffSkip, path string) string {
	for _, s := range l {
		if s.Path == path {
			return s.Reason
		}
	}
	return ""
}

// wantNonFiltered are the files of PR 8 that survive the file filter.
var wantNonFiltered = []string{"server/app.go", "server/fresh.go", "server/gone.go", "server/renamed.go", "server/util.go", "web/index.ts"}

func assertAccounting(t *testing.T, d diffOut) {
	t.Helper()
	var all []string
	all = append(all, d.Included...)
	all = append(all, d.Omitted.Added...)
	all = append(all, d.Omitted.Modified...)
	all = append(all, d.Omitted.Deleted...)
	all = append(all, d.Clipped...)
	for _, s := range d.Skipped {
		all = append(all, s.Path)
	}
	got, want := sortedCopy(all), sortedCopy(wantNonFiltered)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("accounting: files in lists %v, want each of %v exactly once", got, want)
	}
	if len(d.Filtered) != 3 {
		t.Errorf("filtered = %v", d.Filtered)
	}
}

func TestDiagDiffFastPath(t *testing.T) {
	g := newFakeGitea(t)
	d, text, _ := runDiff(t, diagEnv(g, nil), g, 8)
	if !d.FastPath || len(d.Omitted.Added)+len(d.Omitted.Modified)+len(d.Omitted.Deleted)+len(d.Clipped) != 0 {
		t.Errorf("want fast path with nothing omitted: %+v", d)
	}
	assertAccounting(t, d)
	if len(d.Included) != len(wantNonFiltered) || len(d.Skipped) != 0 {
		t.Errorf("included %v skipped %v", d.Included, d.Skipped)
	}
	if d.Budget.ContextWindow != 32000 || d.Budget.PromptTokens != 2056 || d.Budget.SoftLimit != 32000-1500-2056 || d.Budget.HardLimit != 32000-1000-2056 {
		t.Errorf("budget = %+v", d.Budget)
	}
	if d.Tokens <= 0 || d.Tokens >= d.Budget.SoftLimit {
		t.Errorf("tokens = %d", d.Tokens)
	}
	for _, p := range wantNonFiltered {
		if !strings.Contains(text, "## File: '"+p+"'") {
			t.Errorf("text lacks the section of %s", p)
		}
	}
	// Context extension ran: the extended head contains lines far from the hunk.
	if !strings.Contains(text, "server/app.go line 14 ") {
		t.Errorf("fast path should extend the hunk context")
	}
	if strings.Contains(text, "package-lock.json") || strings.Contains(text, "logo.png") {
		t.Error("filtered files must not be in the text")
	}
}

func TestDiagDiffFilteredReasons(t *testing.T) {
	g := newFakeGitea(t)
	d, _, _ := runDiff(t, diagEnv(g, nil), g, 8)
	for path, want := range map[string]string{
		"vendor/lib/dep.go": "ignore_glob",
		"package-lock.json": "lockfile_or_minified",
		"assets/logo.png":   "bad_extension",
	} {
		if got := reasonOf(d.Filtered, path); got != want {
			t.Errorf("filtered[%s] = %q, want %q", path, got, want)
		}
		if reasonOf(d.Skipped, path) != "" {
			t.Errorf("%s must not be listed under skipped too", path)
		}
	}
}

func TestDiagDiffCompressedPath(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "4096"
	d, text, _ := runDiff(t, env, g, 8, "--prompt-tokens", "1000")
	if code, out, _ := diag(env, "diff", g.srv.URL+"/gitea/octo/demo/pulls/8", "--prompt-tokens", "1000"); code == 0 {
		t.Logf("sample diag diff header (gitea fake, window 4096):\n%s", out[:strings.Index(out, diffSep)])
	}
	if d.FastPath {
		t.Fatal("want the compressed path")
	}
	assertAccounting(t, d)
	if len(d.Included) == 0 {
		t.Error("compressed path included nothing")
	}
	if len(d.Omitted.Added)+len(d.Omitted.Modified)+len(d.Omitted.Deleted) == 0 {
		t.Errorf("want omitted files: %+v", d.Omitted)
	}
	// The deleted file is dropped by deletion handling and listed.
	if len(d.Omitted.Deleted) != 1 || d.Omitted.Deleted[0] != "server/gone.go" {
		t.Errorf("omitted.deleted = %v", d.Omitted.Deleted)
	}
	if d.Tokens > d.Budget.HardLimit {
		t.Errorf("tokens %d exceed the hard limit %d", d.Tokens, d.Budget.HardLimit)
	}
	for _, p := range d.Included {
		if !strings.Contains(text, "## File: '"+p+"'") {
			t.Errorf("included %s missing from the text", p)
		}
	}
}

func TestDiagDiffNumberedMode(t *testing.T) {
	g := newFakeGitea(t)
	d, text, _ := runDiff(t, diagEnv(g, nil), g, 8, "--mode", "numbered")
	if !d.FastPath || !strings.Contains(text, "__new hunk__") || !strings.Contains(text, "__old hunk__") {
		t.Errorf("numbered text lacks hunk markers:\n%.400s", text)
	}
	if _, plain, _ := runDiff(t, diagEnv(g, nil), g, 8, "--mode", "plain"); strings.Contains(plain, "__new hunk__") {
		t.Error("plain mode must not use the decoupled format")
	}
	// Flags before the URL work too.
	url := g.giteaPR()
	url = strings.TrimSuffix(url, "7") + "8"
	code, out, errs := diag(diagEnv(g, nil), "diff", "--mode", "numbered", url)
	if code != 0 || !strings.Contains(out, "__new hunk__") {
		t.Errorf("flags before the URL: exit %d\n%s", code, errs)
	}
}

func TestDiagDiffLargePatchPolicy(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "4096"

	// clip: the single file is far larger than the budget.
	d, text, _ := runDiff(t, env, g, 9, "--prompt-tokens", "1000")
	if d.FastPath || len(d.Clipped) != 1 || d.Clipped[0] != "server/huge.go" || len(d.Included) != 0 {
		t.Errorf("clip policy: %+v", d)
	}
	if !strings.Contains(text, "...(truncated)") || d.Tokens > d.Budget.SoftLimit {
		t.Errorf("want a truncated diff within the soft limit (%d), tokens %d", d.Budget.SoftLimit, d.Tokens)
	}

	// skip: nothing fits, so the fixed sentence and exit 1.
	env["REVIEW_MCP_DIFF_LARGE_PATCH_POLICY"] = "skip"
	code, out, errs := diag(env, "diff", g.srv.URL+"/gitea/octo/demo/pulls/9", "--prompt-tokens", "1000")
	if code != 1 || out != "" || !hasLine(errs, doesNotFitMessage) {
		t.Errorf("skip policy: exit %d stdout %q stderr %q", code, out, errs)
	}
}

func TestDiagDiffBudgetDoesNotFit(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "4096"
	code, out, errs := diag(env, "diff", g.srv.URL+"/gitea/octo/demo/pulls/8", "--prompt-tokens", "3000")
	if code != 1 || out != "" || !hasLine(errs, doesNotFitMessage) {
		t.Errorf("exit %d stdout %q stderr %q", code, out, errs)
	}
}

func TestDiagDiffArraysNeverNull(t *testing.T) {
	g := newFakeGitea(t)
	code, out, errs := diag(diagEnv(g, nil), "diff", g.srv.URL+"/gitea/octo/demo/pulls/9")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	m, _ := decodeReport(t, out)
	for _, k := range []string{"included", "clipped", "skipped", "filtered"} {
		if _, ok := m[k].([]any); !ok {
			t.Errorf("%s = %#v, want an array", k, m[k])
		}
	}
	om := m["omitted"].(map[string]any)
	for _, k := range []string{"added", "modified", "deleted"} {
		if _, ok := om[k].([]any); !ok {
			t.Errorf("omitted.%s = %#v, want an array", k, om[k])
		}
	}
	for _, k := range []string{"budget", "fast_path", "tokens", "elapsed_ms"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	if !strings.Contains(out, "\n  \"budget\": {\n") {
		t.Errorf("want 2-space indentation:\n%.200s", out)
	}
}

func TestDiagDiffUsageErrors(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)
	u := g.srv.URL + "/gitea/octo/demo/pulls/8"
	for name, args := range map[string][]string{
		"no url":               {"diff"},
		"two urls":             {"diff", u, u},
		"bad mode":             {"diff", u, "--mode", "fancy"},
		"empty mode":           {"diff", u, "--mode", ""},
		"negative tokens":      {"diff", u, "--prompt-tokens", "-5"},
		"non-integer tokens":   {"diff", u, "--prompt-tokens", "abc"},
		"fractional tokens":    {"diff", u, "--prompt-tokens", "1.5"},
		"tokens without value": {"diff", u, "--prompt-tokens"},
		"unknown flag":         {"diff", u, "--bogus"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errs := diag(env, args...)
			if code != 2 || out != "" || !strings.Contains(errs, "usage:") {
				t.Errorf("exit %d stdout %q stderr %q", code, out, errs)
			}
		})
	}
	if n := g.requests(); n != 0 {
		t.Errorf("usage errors made %d requests", n)
	}
}

func TestDiagDiffInvalidConfigNoNetwork(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12"
	code, out, errs := diag(env, "diff", g.srv.URL+"/gitea/octo/demo/pulls/8")
	if code != 1 || out != "" || strings.TrimSpace(errs) == "" || g.requests() != 0 {
		t.Errorf("exit %d stdout %q stderr %q requests %d", code, out, errs, g.requests())
	}
}

func TestDiagDiffProviderError(t *testing.T) {
	g := newFakeGitea(t)
	g.failStatus.Store(401)
	code, out, errs := diag(diagEnv(g, nil), "diff", g.srv.URL+"/gitea/octo/demo/pulls/8")
	if code != 1 || out != "" || strings.Contains(errs, doesNotFitMessage) {
		t.Errorf("exit %d stdout %q stderr %q", code, out, errs)
	}
	assertNoLeak(t, "stderr", errs)
}

func TestDiagDiffUsageText(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"bogus"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "diag diff <PR_URL> [--mode plain|numbered] [--prompt-tokens N]") {
		t.Errorf("top-level usage lacks diag diff:\n%s", errb.String())
	}
	var o2, e2 bytes.Buffer
	if code := runDiag(nil, &o2, &e2, loaderFor(nil)); code != 2 || !strings.Contains(e2.String(), "diag diff <PR_URL>") {
		t.Errorf("diag usage lacks diag diff:\n%s", e2.String())
	}
}

// TestDiagDiffLeakInProcess [canary]: the diff body goes to stdout and never
// to the logs.
func TestDiagDiffLeakInProcess(t *testing.T) {
	g := newFakeGitea(t)
	_, text, errs := runDiff(t, diagEnv(g, nil), g, 8)
	if !strings.Contains(text, diffBodyMarker) {
		t.Fatal("vacuous check: the body marker is not in the diff on stdout")
	}
	if !strings.Contains(errs, "level=DEBUG") || strings.Contains(errs, diffBodyMarker) {
		t.Errorf("stderr must carry debug logs but never the diff body:\n%s", errs)
	}
}
