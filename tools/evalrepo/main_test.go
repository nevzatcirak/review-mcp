package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sentinel = "REVIEW-TEXT-SENTINEL-5d1c"

// fakeBin stands for the review-mcp binary: it answers "diag cache" and
// "diag review --json --repo-context=<mode> <url>" from a script.
type fakeBin struct {
	cache string
	calls [][]string
	// fail makes the review with this "mode url" fail.
	fail map[string]bool
}

func (f *fakeBin) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if args[1] == "cache" {
		return []byte(`{"cache_dir": ` + quote(f.cache) + `, "repos": []}`), nil
	}
	mode := strings.TrimPrefix(args[3], "--repo-context=")
	url := args[4]
	if f.fail[mode+" "+url] {
		return nil, errors.New("exit status 1")
	}
	findings := `{"relevant_file":"a.go","issue_header":"Check ` + sentinel + `","issue_content":"Line one\nline two","start_line":3,"end_line":4}`
	status := `{"status":"off","reason":"","symbols":0,"references":0,"files":0}`
	if mode == "on" {
		findings += `,{"relevant_file":"b.go","issue_header":"Caller breaks, with a \"quote\", comma","issue_content":"x","start_line":9,"end_line":9}`
		status = `{"status":"used","reason":"","symbols":2,"references":3,"files":2}`
	}
	return []byte(`{"review":{"key_issues_to_review":[` + findings + `]},"coverage":{"repo_context":` + status + `}}`), nil
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func runEval(t *testing.T, r runner, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb, r)
	return code, out.String(), errb.String()
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: test output
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestLayoutAndRatingSheet (-blind=false, the plain layout): both modes of every pull request are stored, the
// comparison names both, and the rating sheet has exactly one row per finding
// with the three rating columns empty. The review text reaches only the files
// under -out.
func TestLayoutAndRatingSheet(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "eval")
	list := filepath.Join(tmp, "urls.txt")
	if err := os.WriteFile(list, []byte("# pull requests\nhttps://git.example/o/r/pulls/1\n\nhttps://git.example/o/r/pulls/2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fb := &fakeBin{cache: filepath.Join(tmp, "cache")}
	code, stdout, stderr := runEval(t, fb, "-blind=false", "-out", out, "-urls", list, "https://git.example/o/r/pulls/3")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout+stderr, sentinel) {
		t.Error("review text reached stdout or stderr")
	}
	for _, n := range []string{"pr-01", "pr-02", "pr-03"} {
		for _, f := range []string{"off.json", "on.json", "compare.md"} {
			if _, err := os.Stat(filepath.Join(out, n, f)); err != nil {
				t.Errorf("missing %s/%s", n, f)
			}
		}
	}
	cmp, _ := os.ReadFile(filepath.Join(out, "pr-01", "compare.md")) //nolint:gosec // G304: test output
	for _, want := range []string{"## Without repository context", "## With repository context", "Repository context: off",
		"Repository context: used (2 symbols, 3 references from 2 files)", "> Line one", sentinel} {
		if !strings.Contains(string(cmp), want) {
			t.Errorf("compare.md lacks %q:\n%s", want, cmp)
		}
	}
	rows := readCSV(t, filepath.Join(out, "ratings.csv"))
	// 3 pull requests x (1 finding off + 2 findings on).
	if len(rows) != 1+3*3 {
		t.Fatalf("%d rows, want 10", len(rows))
	}
	if _, err := os.Stat(filepath.Join(out, "key.csv")); err == nil {
		t.Error("a plain run wrote key.csv")
	}
	for _, row := range rows[1:] {
		if len(row) != 12 || row[9] != "" || row[10] != "" || row[11] != "" {
			t.Errorf("row %v: the rating columns must be empty", row)
		}
	}
	if r := rows[3]; r[0] != "1" || r[2] != "on" || r[3] != "2" || r[4] != "b.go" || r[7] != `Caller breaks, with a "quote", comma` || r[8] != "used" {
		t.Errorf("row = %v", r)
	}
	// The binary was called with the real code path and without publishing.
	for _, c := range fb.calls {
		if c[1] == "review" && (c[2] != "--json" || strings.Contains(strings.Join(c, " "), "--publish")) {
			t.Errorf("call %v", c)
		}
	}
}

// TestRefusesWithoutOut: no -out, no run, nothing called.
func TestRefusesWithoutOut(t *testing.T) {
	fb := &fakeBin{cache: t.TempDir()}
	code, _, stderr := runEval(t, fb, "https://git.example/o/r/pulls/1")
	if code != 2 || !strings.Contains(stderr, "-out is required") || len(fb.calls) != 0 {
		t.Errorf("exit %d, stderr %q, calls %v", code, stderr, fb.calls)
	}
}

// TestRefusesInsideCache [canary]: -out inside the cache directory (or the
// cache itself, also through a symbolic link or a relative path) is refused
// before anything is written or any review is run.
func TestRefusesInsideCache(t *testing.T) {
	tmp := t.TempDir()
	cache := filepath.Join(tmp, "cache")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	linked := os.Symlink(cache, link) == nil
	cases := []string{cache, filepath.Join(cache, "eval"), filepath.Join(cache, "a", "b"), filepath.Join(cache, "x", "..", "eval")}
	if linked {
		cases = append(cases, filepath.Join(link, "eval"))
	}
	for _, out := range cases {
		fb := &fakeBin{cache: cache}
		code, _, stderr := runEval(t, fb, "-out", out, "https://git.example/o/r/pulls/1")
		if code != 2 || !strings.Contains(stderr, "inside the repository-context cache directory") {
			t.Errorf("-out %s: exit %d, stderr %q", out, code, stderr)
		}
		for _, c := range fb.calls {
			if c[1] == "review" {
				t.Errorf("-out %s: a review was run", out)
			}
		}
	}
	// -cache-dir works without asking the binary.
	fb := &fakeBin{cache: tmp}
	code, _, _ := runEval(t, fb, "-cache-dir", cache, "-out", filepath.Join(cache, "eval"), "https://git.example/o/r/pulls/1")
	if code != 2 || len(fb.calls) != 0 {
		t.Errorf("-cache-dir: exit %d, calls %v", code, fb.calls)
	}
	// A sibling whose name starts like the cache is not inside it.
	sib := filepath.Join(tmp, "cache-eval")
	fb = &fakeBin{cache: cache}
	if code, _, stderr := runEval(t, fb, "-out", sib, "https://git.example/o/r/pulls/1"); code != 0 {
		t.Errorf("sibling directory refused: exit %d, %s", code, stderr)
	}
}

func TestRefusesNonEmptyOutAndEmptyList(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "eval")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "old.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fb := &fakeBin{cache: filepath.Join(tmp, "cache")}
	if code, _, stderr := runEval(t, fb, "-out", out, "https://git.example/o/r/pulls/1"); code != 2 || !strings.Contains(stderr, "not empty") {
		t.Errorf("non-empty out: exit %d, %q", code, stderr)
	}
	if code, _, stderr := runEval(t, fb, "-out", filepath.Join(tmp, "new")); code != 2 || !strings.Contains(stderr, "no pull request URL") {
		t.Errorf("no URLs: exit %d, %q", code, stderr)
	}
}

// TestFailedReviewIsReportedNotFatal: a failed review is a fixed sentence and
// exit 1 after the rest ran; its file is missing and compare.md says so.
func TestFailedReviewIsReportedNotFatal(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "eval")
	fb := &fakeBin{cache: filepath.Join(tmp, "cache"), fail: map[string]bool{"on https://git.example/o/r/pulls/1": true}}
	code, _, stderr := runEval(t, fb, "-out", out, "https://git.example/o/r/pulls/1", "https://git.example/o/r/pulls/2")
	if code != 1 || !strings.Contains(stderr, "pull request 1: the review with context on failed") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "pr-01", "on.json")); err == nil {
		t.Error("a failed review left a result file")
	}
	if _, err := os.Stat(filepath.Join(out, "pr-02", "on.json")); err != nil {
		t.Error("the second pull request was not reviewed")
	}
	cmp, _ := os.ReadFile(filepath.Join(out, "pr-01", "compare.md")) //nolint:gosec // G304: test output
	if !strings.Contains(string(cmp), "The review failed") {
		t.Errorf("compare.md = %s", cmp)
	}
}

// blindFixture runs the blind harness over n pull requests.
func blindFixture(t *testing.T, args ...string) (out string, fb *fakeBin) {
	t.Helper()
	tmp := t.TempDir()
	out = filepath.Join(tmp, "eval")
	fb = &fakeBin{cache: filepath.Join(tmp, "cache")}
	urls := []string{}
	for i := 1; i <= 6; i++ {
		urls = append(urls, "https://git.example/o/r/pulls/"+string(rune('0'+i)))
	}
	code, stdout, stderr := runEval(t, fb, append(append([]string{"-out", out}, args...), urls...)...)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "blind sheet, seed ") {
		t.Errorf("the seed is not printed: %q", stdout)
	}
	return out, fb
}

// TestBlindSheet [canary]: by default the sheet is shuffled, shows a sample id
// instead of the mode, and no cell of it says off, on or the coverage status;
// key.csv maps every sample back, and README.txt has the seed. The check is on
// cell values (the fixture's finding text has no such words), so a finding
// that merely contains the word "on" would not fool it.
func TestBlindSheet(t *testing.T) {
	out, _ := blindFixture(t, "-seed", "42")
	sheet := readCSV(t, filepath.Join(out, "ratings.csv"))
	key := readCSV(t, filepath.Join(out, "key.csv"))
	if len(sheet) != 1+6*3 || len(key) != len(sheet) {
		t.Fatalf("sheet %d rows, key %d rows; want 19", len(sheet), len(key))
	}
	if want := []string{"sample", "pr", "url", "file", "start_line", "end_line", "header", "cross_file", "correct", "new"}; strings.Join(sheet[0], ",") != strings.Join(want, ",") {
		t.Errorf("header = %v", sheet[0])
	}
	for _, row := range sheet {
		for _, cell := range row {
			switch cell {
			case "off", "on", "used", "skipped", "mode", "repo_context":
				t.Errorf("the sheet reveals the mode: %v", row)
			}
		}
	}
	// key.csv maps each sample to its mode; the sheet row and the key row agree
	// on the pull request, and each pull request has 1 "off" and 2 "on" samples.
	modes := map[string]int{}
	for i := 1; i < len(sheet); i++ {
		if sheet[i][0] != key[i][0] || sheet[i][1] != key[i][1] {
			t.Errorf("row %d: sheet %v, key %v", i, sheet[i], key[i])
		}
		modes[key[i][2]]++
	}
	if modes["off"] != 6 || modes["on"] != 12 {
		t.Errorf("modes in the key = %v", modes)
	}
	// It is shuffled: not the order of the runs (pr 1 off, pr 1 on, ...).
	inOrder := true
	for i := 2; i < len(key); i++ {
		if key[i][1] < key[i-1][1] {
			inOrder = false
		}
	}
	if inOrder {
		t.Error("the rows are not shuffled")
	}
	readme, _ := os.ReadFile(filepath.Join(out, "README.txt")) //nolint:gosec // G304: test output
	if !strings.Contains(string(readme), "42") || !strings.Contains(string(readme), "reveals which") {
		t.Errorf("README.txt = %s", readme)
	}
}

// TestBlindShuffleIsDeterministicPerSeed: the same seed gives the same sheet
// and key, another seed another order.
func TestBlindShuffleIsDeterministicPerSeed(t *testing.T) {
	read := func(seed string) string {
		out, _ := blindFixture(t, "-seed", seed)
		a, _ := os.ReadFile(filepath.Join(out, "ratings.csv")) //nolint:gosec // G304: test output
		b, _ := os.ReadFile(filepath.Join(out, "key.csv"))     //nolint:gosec // G304: test output
		return string(a) + "\n" + string(b)
	}
	first, second := read("7"), read("7")
	if first != second {
		t.Error("the same seed gave different sheets")
	}
	if first == read("8") {
		t.Error("different seeds gave the same sheet")
	}
	items := make([]item, 20)
	for i := range items {
		items[i].pr = i
	}
	a, b := shuffle(items, 3), shuffle(items, 3)
	for i := range a {
		if a[i].pr != b[i].pr {
			t.Fatal("shuffle is not deterministic")
		}
	}
}
