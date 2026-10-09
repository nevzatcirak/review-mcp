package render

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

var update = flag.Bool("update", false, "rewrite the render goldens")

var _ review.ProviderRenderer = Provider

var (
	capsGitea = provider.Capabilities{GFM: true, MarkdownTables: true, Labels: true, InlineComments: true}
	capsBB    = provider.Capabilities{GFM: false, MarkdownTables: true, InlineComments: true}
)

func ip(n int) *int       { return &n }
func bp(b bool) *bool     { return &b }
func sp(s string) *string { return &s }

var allKeys = []string{review.KeyEffort, review.KeyRelevantTests, review.KeyKeyIssues, review.KeySecurityConcerns,
	review.KeyPerformanceConcerns}

func baseCoverage() review.Coverage {
	return review.Coverage{
		Included: []string{"cmd/app/main.go", "internal/util/strings_util.go"},
		Clipped:  []string{"internal/big/generated_like.go"},
		Omitted: review.OmittedFiles{
			Added:    []string{"docs/new.md"},
			Modified: []string{"internal/other/other.go"},
			Deleted:  []string{},
		},
		Skipped:  []review.SkippedFile{{Path: "assets/logo.png", Reason: provider.SkipBinary}},
		Filtered: []review.SkippedFile{{Path: "go.sum", Reason: "lockfile_or_minified"}},
	}
}

func basePR() review.PRInfo {
	return review.PRInfo{Kind: string(provider.KindGitea), URL: "https://your-gitea.example/org/repo/pulls/12", Number: 12,
		Title: "Add retry to the fetcher", HeadSHA: "abc123def4567890abc123def4567890abc123de"}
}

func fixtures() map[string]*review.Result {
	allFields := &review.Result{
		PR: basePR(), EnabledFields: allKeys, Coverage: baseCoverage(), Notes: []string{review.NoteDiffTrimmed},
		Review: &review.Review{
			EstimatedEffortToReview: ip(3),
			RelevantTests:           bp(true),
			SecurityConcerns:        sp("SQL injection: the query is built by string concatenation.\nUse bound parameters instead."),
			PerformanceConcerns:     sp("N+1 access: cmd/app/main.go loads each item with its own query.\nBatch the <ids> instead."),
			KeyIssuesToReview: []review.KeyIssue{
				{
					RelevantFile: "cmd/app/main.go", IssueHeader: "Possible Bug",
					IssueContent: "The retry loop never stops when `max` is 0, so it spins forever.\nSee the `for` loop.",
					StartLine:    10, EndLine: 12,
					Snippet: "for {\n\tif try() {\n\t\tbreak\n\t}\n}",
					Link:    "https://your-gitea.example/org/repo/src/commit/abc123/cmd/app/main.go#L10-L12",
				},
				{
					RelevantFile: "internal/util/strings_util.go", IssueHeader: "Resource leak",
					IssueContent: "The response body is not closed on the error path.",
					StartLine:    40, EndLine: 40,
					Snippet: "resp, err := http.Get(u)\nif err != nil {\n\treturn err\n}",
				},
			},
		},
	}
	noFindings := &review.Result{
		PR: basePR(), EnabledFields: allKeys, Coverage: baseCoverage(), Notes: []string{},
		Review: &review.Review{
			EstimatedEffortToReview: ip(1), RelevantTests: bp(false), SecurityConcerns: sp(review.SecurityNo),
			PerformanceConcerns: sp(review.PerformanceNo), KeyIssuesToReview: []review.KeyIssue{},
		},
	}
	noSnippet := &review.Result{
		PR: basePR(), EnabledFields: []string{review.KeyKeyIssues}, Coverage: baseCoverage(), Notes: []string{review.NoteTruncated},
		Review: &review.Review{KeyIssuesToReview: []review.KeyIssue{
			{
				RelevantFile: "cmd/app/main.go", IssueHeader: "Missing check",
				IssueContent: "The error from Close is ignored.", StartLine: 7, EndLine: 9,
				SnippetNote: review.SnippetNoteUnverified,
			},
		}},
	}
	nonEnglish := &review.Result{
		PR:            review.PRInfo{Kind: string(provider.KindBitbucketServer), URL: "https://bitbucket.example.com/projects/P/repos/r/pull-requests/5", Number: 5, Title: "Yeniden deneme ekle"},
		EnabledFields: allKeys, Coverage: baseCoverage(), Notes: []string{"Fark kısaltıldı; kapsam bölümüne bakın."},
		Review: &review.Review{
			EstimatedEffortToReview: ip(4), RelevantTests: bp(true),
			SecurityConcerns:    sp("Hassas bilgi ifşası: şifre günlüğe yazılıyor."),
			PerformanceConcerns: sp("src/main.py: her istekte dosya yeniden okunuyor."),
			KeyIssuesToReview: []review.KeyIssue{
				{
					RelevantFile: "src/main.py", IssueHeader: "Olası hata 🐞",
					IssueContent: "Döngü, `sınır` sıfır olduğunda sonsuza kadar sürer. 日本語のテスト。",
					StartLine:    3, EndLine: 4, Snippet: "while True:\n    pass",
				},
			},
		},
	}
	many := baseCoverage()
	for i := range 55 {
		many.Omitted.Modified = append(many.Omitted.Modified, fmt.Sprintf("pkg/mod%02d/file.go", i))
	}
	for i := range 4 {
		many.Filtered = append(many.Filtered, review.SkippedFile{Path: fmt.Sprintf("vendor/f%d.min.js", i), Reason: "lockfile_or_minified"})
	}
	coverageMany := &review.Result{
		PR: basePR(), EnabledFields: allKeys, Coverage: many, Notes: []string{review.NoteDiffTrimmed},
		Review: &review.Review{
			EstimatedEffortToReview: ip(5), RelevantTests: bp(false), SecurityConcerns: sp(review.SecurityNo),
			PerformanceConcerns: sp(review.PerformanceNo), KeyIssuesToReview: []review.KeyIssue{},
		},
	}
	// published is a result after an inline publish that updated the
	// overview of an earlier run (spec P7 §4.3): one finding posted inline,
	// one not on a changed line, one whose inline comment failed; the
	// "already discussed" count is set.
	published := &review.Result{
		PR: basePR(), EnabledFields: allKeys, Coverage: baseCoverage(),
		Notes:    []string{"1 finding could not be placed on a changed line and is listed in the overview only."},
		Metadata: review.Metadata{AlreadyDiscussed: 2},
		Review: &review.Review{
			EstimatedEffortToReview: ip(2), RelevantTests: bp(true), SecurityConcerns: sp(review.SecurityNo),
			PerformanceConcerns: sp(review.PerformanceNo),
			KeyIssuesToReview: []review.KeyIssue{
				{
					RelevantFile: "cmd/app/main.go", IssueHeader: "Possible Bug", IssueContent: "The loop never ends.",
					StartLine: 10, EndLine: 10, Snippet: "for {}",
					Link:         "https://your-gitea.example/org/repo/src/commit/abc123/cmd/app/main.go#L10",
					InlineURL:    "https://your-gitea.example/org/repo/pulls/12/files#issuecomment-901",
					InlineStatus: review.InlinePosted,
				},
				{
					RelevantFile: "internal/util/strings_util.go", IssueHeader: "Outside the diff",
					IssueContent: "An unchanged helper no longer fits.\nSee the caller.", StartLine: 80, EndLine: 82,
					SnippetNote:  review.SnippetNoteUnverified,
					Link:         "https://your-gitea.example/org/repo/src/commit/abc123/internal/util/strings_util.go#L80",
					InlineStatus: review.InlineUnanchorable,
				},
				{
					RelevantFile: "cmd/app/main.go", IssueHeader: "Leak", IssueContent: "The body is not closed.",
					StartLine: 12, EndLine: 12, Snippet: "resp, _ := get()",
					Link:         "https://your-gitea.example/org/repo/src/commit/abc123/cmd/app/main.go#L12",
					InlineStatus: review.InlineFailed,
				},
			},
		},
		Publish: &review.PublishResult{Published: true, Updated: true, CommentID: "55",
			URL:    "https://your-gitea.example/org/repo/pulls/12#issuecomment-55",
			Inline: &review.InlineSummary{Posted: 1, Unanchorable: 1, Failed: 1}},
	}
	// complete is the run without findings whose coverage has nothing
	// partial (filtered, binary and empty-diff files do not count, X-18):
	// no banner, the unscoped "nothing found" sentences.
	complete := noFindingsResult(completeCoverage())
	all := map[string]*review.Result{
		"all_fields": allFields, "no_findings": noFindings, "no_snippet": noSnippet,
		"non_english": nonEnglish, "coverage_many": coverageMany, "published": published,
		"complete": complete,
	}
	for _, r := range all {
		r.Metadata.ReviewedAt = "2026-10-06T09:30:15Z"
	}
	return all
}

// TestPerformanceFollowsEnabledFields: the performance row (or section)
// appears in every profile exactly when performance_concerns is an enabled
// field, next to the security row; a value without the field enabled is
// not shown (X-12).
func TestPerformanceFollowsEnabledFields(t *testing.T) {
	const concern = "unbounded loop in cmd/app/main.go"
	profiles := map[string]func(*review.Result) string{
		"client":    Client,
		"gitea":     func(r *review.Result) string { return Provider(r, capsGitea) },
		"bitbucket": func(r *review.Result) string { return Provider(r, capsBB) },
	}
	withoutPerf := []string{review.KeyEffort, review.KeyRelevantTests, review.KeyKeyIssues, review.KeySecurityConcerns}
	for _, tc := range []struct {
		name    string
		enabled []string
		value   string
		want    []string // in this order
		absent  []string
	}{
		{"on, no", allKeys, review.PerformanceNo, []string{textNoSecurity, textNoPerf}, []string{textPerf}},
		{"on, concern", allKeys, concern, []string{textNoSecurity, textPerf, concern}, []string{textNoPerf}},
		{"off, no", withoutPerf, review.PerformanceNo, []string{textNoSecurity}, []string{"erformance"}},
		{"off, concern", withoutPerf, concern, []string{textNoSecurity}, []string{"erformance", concern}},
		{"on, empty", allKeys, "  ", []string{textNoSecurity}, []string{"erformance"}},
	} {
		for profile, render := range profiles {
			res := &review.Result{PR: basePR(), EnabledFields: tc.enabled, Coverage: baseCoverage(), Notes: []string{},
				Review: &review.Review{SecurityConcerns: sp(review.SecurityNo), PerformanceConcerns: sp(tc.value),
					KeyIssuesToReview: []review.KeyIssue{}}}
			got := render(res)
			at := 0
			for _, w := range tc.want {
				i := strings.Index(got[at:], w)
				if i < 0 {
					t.Errorf("%s/%s: %q missing or out of order:\n%s", profile, tc.name, w, got)
					break
				}
				at += i + len(w)
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("%s/%s: %q present:\n%s", profile, tc.name, a, got)
				}
			}
		}
	}
}

func TestGoldens(t *testing.T) {
	profiles := map[string]func(*review.Result) string{
		"client":    Client,
		"gitea":     func(r *review.Result) string { return Provider(r, capsGitea) },
		"bitbucket": func(r *review.Result) string { return Provider(r, capsBB) },
	}
	for caseName, res := range fixtures() {
		for profile, render := range profiles {
			t.Run(profile+"/"+caseName, func(t *testing.T) {
				got := render(res)
				path := filepath.Join("testdata", profile+"_"+caseName+".md")
				if *update {
					if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Errorf("%s differs (run go test ./internal/review/render -update after checking the change)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
				}
			})
		}
	}
}

// tagStart matches a '<' that opens an HTML tag, comment, doctype or
// processing instruction and is not backslash-escaped.
var tagStart = regexp.MustCompile(`(^|[^\\])<[A-Za-z/!?]`)

// stripFences removes the fenced blocks of out.
func stripFences(out string) string {
	var kept []string
	open := 0
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimLeft(line, " ")
		n := len(t) - len(strings.TrimLeft(t, "`"))
		switch {
		case open == 0 && n >= 3:
			open = n
		case open > 0 && n >= open && strings.TrimLeft(t, "`") == "":
			open = 0
		case open == 0:
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// [canary] The client profile contains no HTML: tags injected into every
// dynamic string do not survive unescaped.
func TestClientHasNoHTML(t *testing.T) {
	const inj = `<details><b>x</b><script>alert(1)</script><img src=x onerror=y>`
	res := &review.Result{
		PR:            review.PRInfo{Kind: "gitea", URL: "https://example.com/" + inj, Number: 1, Title: "T " + inj},
		EnabledFields: allKeys,
		Coverage: review.Coverage{
			Included: []string{"a" + inj}, Clipped: []string{"c" + inj},
			Omitted:  review.OmittedFiles{Added: []string{"o" + inj}},
			Skipped:  []review.SkippedFile{{Path: "s" + inj, Reason: "r" + inj}},
			Filtered: []review.SkippedFile{{Path: "f" + inj, Reason: "r" + inj}},
		},
		Notes: []string{"note " + inj},
		Review: &review.Review{
			EstimatedEffortToReview: ip(2), RelevantTests: bp(true), SecurityConcerns: sp("Header: " + inj),
			PerformanceConcerns: sp("Perf: " + inj),
			KeyIssuesToReview: []review.KeyIssue{{
				RelevantFile: "file" + inj + ".go", IssueHeader: "H " + inj, IssueContent: "C " + inj + "\n" + inj,
				StartLine: 1, EndLine: 2, Snippet: "plain code", SnippetNote: "N " + inj,
				Link: "https://example.com/x?a=" + inj,
			}},
		},
	}
	out := Client(res)
	if loc := tagStart.FindStringIndex(stripFences(out)); loc != nil {
		t.Fatalf("client output holds an unescaped HTML tag near %q\n%s", stripFences(out)[loc[0]:min(loc[1]+30, len(stripFences(out)))], out)
	}
	for _, bad := range []string{"<details>", "<script>", "<b>"} {
		if strings.Contains(out, bad) {
			t.Errorf("client output contains %q", bad)
		}
	}
}

// fenceBlocks parses the backtick-fenced blocks of out the way CommonMark
// does (an opening run of at least 3 backticks closes on a run at least as
// long with nothing else) and returns each block's lines, indent removed.
func fenceBlocks(out string) [][]string {
	var blocks [][]string
	var cur []string
	open, indent := 0, 0
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimLeft(line, " ")
		n := len(t) - len(strings.TrimLeft(t, "`"))
		switch {
		case open == 0 && n >= 3:
			open, indent, cur = n, len(line)-len(t), nil
		case open > 0 && n >= open && strings.TrimLeft(t, "`") == "":
			blocks = append(blocks, cur)
			open = 0
		case open > 0:
			cur = append(cur, strings.TrimPrefix(line, strings.Repeat(" ", indent)))
		}
	}
	return blocks
}

// [canary] A snippet or finding text with ``` or ```` cannot break the fence.
func TestFenceCannotBeBroken(t *testing.T) {
	snippets := []string{
		"a := 1\n```\nb := 2",
		"x\n````\ny\n```` trailing\n`````\nz",
		"```go\nfmt.Println()\n```",
	}
	for _, snip := range snippets {
		res := &review.Result{
			PR: basePR(), EnabledFields: allKeys,
			Coverage: review.Coverage{Included: []string{"a.go"}},
			Review: &review.Review{
				EstimatedEffortToReview: ip(1), RelevantTests: bp(true), SecurityConcerns: sp("No"),
				KeyIssuesToReview: []review.KeyIssue{{
					RelevantFile: "a.go", IssueHeader: "H", IssueContent: "body with ``` and ```` inside",
					StartLine: 1, EndLine: 3, Snippet: snip, SnippetNote: "note ```",
				}},
			},
		}
		outs := map[string]string{"client": Client(res), "gitea": Provider(res, capsGitea), "bitbucket": Provider(res, capsBB)}
		for name, out := range outs {
			blocks := fenceBlocks(out)
			if len(blocks) != 1 {
				t.Fatalf("%s: %d fenced blocks, want exactly 1 for snippet %q\n%s", name, len(blocks), snip, out)
			}
			if got := strings.Join(blocks[0], "\n"); got != snip {
				t.Errorf("%s: fenced block = %q, want the snippet %q", name, got, snip)
			}
			// Outside the block no line opens another fence.
			for _, line := range strings.Split(stripFences(out), "\n") {
				if strings.HasPrefix(strings.TrimLeft(line, " "), "```") {
					t.Errorf("%s: a fence-like line outside the block: %q", name, line)
				}
			}
		}
	}
}

func TestCoverageListsAtMost50(t *testing.T) {
	res := fixtures()["coverage_many"]
	for name, out := range map[string]string{"client": Client(res), "gitea": Provider(res, capsGitea), "bitbucket": Provider(res, capsBB)} {
		listed := strings.Count(out, "pkg/mod") + strings.Count(out, "vendor/f") + strings.Count(out, "docs/new.md") +
			strings.Count(out, "internal/other/other.go") + strings.Count(out, "assets/logo.png") + strings.Count(out, "go.sum") +
			strings.Count(out, "generated_like.go")
		if listed != MaxListedFiles {
			t.Errorf("%s: %d files listed, want %d", name, listed, MaxListedFiles)
		}
		// 1 clipped + 1 added + 56 modified + 1 skipped + 5 filtered = 64 candidates.
		if !strings.Contains(out, "and 14 more") {
			t.Errorf("%s: want an 'and 14 more' line\n%s", name, out)
		}
		if !strings.Contains(out, "- Included: 3 files") || !strings.Contains(out, "- Omitted: 63 files") {
			t.Errorf("%s: counts missing\n%s", name, out)
		}
	}
}

func TestCoverageAlwaysPresent(t *testing.T) {
	res := &review.Result{PR: basePR(), EnabledFields: nil, Review: &review.Review{KeyIssuesToReview: []review.KeyIssue{}}}
	for name, out := range map[string]string{"client": Client(res), "gitea": Provider(res, capsGitea), "bitbucket": Provider(res, capsBB)} {
		if !strings.Contains(out, "Coverage") || !strings.Contains(out, "- Included: 0 files") || !strings.Contains(out, "- Omitted: 0 files") {
			t.Errorf("%s: coverage section missing\n%s", name, out)
		}
		if strings.Contains(out, "Notes") {
			t.Errorf("%s: notes section with no notes\n%s", name, out)
		}
	}
}

// The GFM profile keeps HTML only where the renderer writes it: injected
// tags are entities, and each key issue has its own <details>.
func TestGiteaEscapesInjectedHTML(t *testing.T) {
	const inj = `<script>alert(1)</script><b>x</b>`
	res := &review.Result{
		PR: basePR(), EnabledFields: allKeys, Coverage: review.Coverage{},
		Review: &review.Review{
			EstimatedEffortToReview: ip(2), RelevantTests: bp(true), SecurityConcerns: sp("Hdr: " + inj),
			PerformanceConcerns: sp("Perf: " + inj),
			KeyIssuesToReview: []review.KeyIssue{
				{RelevantFile: "a" + inj, IssueHeader: "H" + inj, IssueContent: inj, StartLine: 1, EndLine: 1, Snippet: "x", Link: "https://example.com/a'onclick='x"},
				{RelevantFile: "b.go", IssueHeader: "H2", IssueContent: "c"},
			},
		},
	}
	out := Provider(res, capsGitea)
	if strings.Contains(out, "<script>") || strings.Contains(out, "<b>x") {
		t.Errorf("injected tag survived\n%s", out)
	}
	if strings.Count(out, "<details>") != 2 { // one per finding, with or without a snippet
		t.Errorf("want one <details> per finding\n%s", out)
	}
	if strings.Contains(out, "onclick='x") {
		t.Errorf("link attribute not escaped\n%s", out)
	}
}

func TestBitbucketHasNoHTMLAndEscapesPipes(t *testing.T) {
	res := &review.Result{
		PR: basePR(), EnabledFields: allKeys, Coverage: review.Coverage{},
		Review: &review.Review{
			EstimatedEffortToReview: ip(2), RelevantTests: bp(true), SecurityConcerns: sp("No"),
			PerformanceConcerns: sp("loop | <b>x</b>"),
			KeyIssuesToReview:   []review.KeyIssue{{RelevantFile: "a|b.go", IssueHeader: "x | y <b>", IssueContent: "c", StartLine: 1, EndLine: 1}},
		},
	}
	out := Provider(res, capsBB)
	if tagStart.MatchString(out) {
		t.Errorf("HTML in the Bitbucket profile\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "1. ") && strings.Count(strings.ReplaceAll(line, `\|`, ""), "|") != 0 {
			t.Errorf("unescaped pipe in the findings index: %q", line)
		}
	}
	// Without table support the same data is a list.
	list := Provider(res, provider.Capabilities{})
	if strings.Contains(list, "|---|") {
		t.Errorf("table in a no-table profile\n%s", list)
	}
}

func TestEffortClampAndBars(t *testing.T) {
	for in, want := range map[int]string{0: "1/5 🔵⚪⚪⚪⚪", 3: "3/5 🔵🔵🔵⚪⚪", 9: "5/5 🔵🔵🔵🔵🔵"} {
		if got := effortValue(in); got != want {
			t.Errorf("effortValue(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeLink(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com/a(b)":   "https://example.com/a%28b%29",
		"javascript:alert(1)":        "",
		"//example.com/x":            "",
		"https://example.com/a b":    "https://example.com/a%20b",
		"http://example.com/<x>":     "http://example.com/%3Cx%3E",
		"data:text/html;base64,AAAA": "",
		"":                           "",
	} {
		if got := safeLink(in); got != want {
			t.Errorf("safeLink(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLangTag(t *testing.T) {
	for in, want := range map[string]string{"a/b.go": "go", "x.py": "python", "x.unknownext": "", "main.cpp": "c++"} {
		if got := langTag(in); got != want {
			t.Errorf("langTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNilAndEmpty(t *testing.T) {
	if Client(nil) != "" || Provider(nil, capsGitea) != "" {
		t.Error("nil result must render empty")
	}
	out := Client(&review.Result{PR: basePR()}) // nil Review
	if !strings.HasPrefix(out, "## PR Review\n") || !strings.Contains(out, "Coverage") {
		t.Errorf("unexpected output for a result without a review:\n%s", out)
	}
}

func TestPossibleBugSoftened(t *testing.T) {
	if got := issueHeader(" possible bug "); got != "Possible Issue" {
		t.Errorf("issueHeader = %q", got)
	}
}

func TestProviderName(t *testing.T) {
	for kind, want := range map[string]string{
		string(provider.KindGitea): "Gitea", string(provider.KindBitbucketServer): "Bitbucket Server",
		string(provider.KindGitHub): "GitHub", "other": "other",
	} {
		if got := providerName(kind); got != want {
			t.Errorf("providerName(%q) = %q, want %q", kind, got, want)
		}
	}
}
