package ask

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func pinFiles(paths ...string) []provider.FilePatch {
	out := make([]provider.FilePatch, len(paths))
	for i, p := range paths {
		out[i] = provider.FilePatch{Path: p}
	}
	return out
}

// TestPinnedFilesRule: full path or base name of at least 5 characters,
// case-sensitive, whole token (X-21).
func TestPinnedFilesRule(t *testing.T) {
	files := pinFiles("src/a.go", "cmd/main.go", "internal/server.go", "pkg/util.py", "docs/Guide.md", "lib/über.go")
	for _, tc := range []struct {
		question string
		want     []string
	}{
		// Full path, with the boundaries a sentence puts around it.
		{"Is src/a.go?", []string{"src/a.go"}},
		{"Is src/a.go, or not", []string{"src/a.go"}},
		{"Look at `src/a.go`.", []string{"src/a.go"}},
		{`What does "src/a.go" do...`, []string{"src/a.go"}},
		{"(src/a.go)", []string{"src/a.go"}},
		{"src/a.go", []string{"src/a.go"}},
		// Base name of at least 5 characters.
		{"Why does main.go exit?", []string{"cmd/main.go"}},
		{"server.go: is the lock held?", []string{"internal/server.go"}},
		{"Is über.go fine?", []string{"lib/über.go"}},
		// Several files, returned in input order.
		{"Compare server.go with src/a.go.", []string{"src/a.go", "internal/server.go"}},
		// Base names shorter than 5 characters pin nothing by themselves.
		{"Is a.go right?", nil},
		// Not a whole token.
		{"Is xmain.go right?", nil},
		{"Is main.go_old right?", nil},
		{"Is main.go-v2 right?", nil},
		{"Is main.go.bak right?", nil},
		{"Is pkg/main.go right?", nil},
		{"Is xsrc/a.go right?", nil},
		{"Is src/a.gox right?", nil},
		// A leading "./" before a full path is accepted; the character
		// before it follows the same rule.
		{"Is ./src/a.go right?", []string{"src/a.go"}},
		{"(./src/a.go)", []string{"src/a.go"}},
		{"Look at `./src/a.go`.", []string{"src/a.go"}},
		{"./src/a.go", []string{"src/a.go"}},
		{"Is ../src/a.go right?", nil},
		{"Is x./src/a.go right?", nil},
		{"Is .//src/a.go right?", nil},
		// Not before a base name.
		{"Why does ./main.go exit?", nil},
		// Case-sensitive.
		{"Why does Main.go exit?", nil},
		{"Is guide.md current?", nil},
		{"Is Guide.md current?", []string{"docs/Guide.md"}},
		// util.py has a 7-character base name.
		{"util.py!", []string{"pkg/util.py"}},
		{"No file here.", nil},
	} {
		if got := PinnedFiles(tc.question, files); !slices.Equal(got, tc.want) {
			t.Errorf("PinnedFiles(%q) = %q, want %q", tc.question, got, tc.want)
		}
	}
}

// pinHarness serves 30 files of about 700 tokens each with diff.max_tokens
// 2000, so the budget admits two files and the last ones are omitted.
func pinHarness() *harness {
	var files []provider.FilePatch
	for i := range 30 {
		files = append(files, provider.FilePatch{
			Path: fmt.Sprintf("src/f%02d.go", i), Type: provider.ChangeModified,
			Patch:      "@@ -1,2 +1,60 @@\n a\n" + strings.Repeat("+some added line of code here\n", 60),
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		})
	}
	files = append(files, sampleFiles()[2]) // vendor/lib.go, filtered by default
	h := newHarness("ok")
	h.prov.files = files
	capTokens := 2000
	h.deps.Config.Diff.MaxTokens = &capTokens
	return h
}

// TestQuestionNamedFileIsIncluded: a file the question names that the
// budget would leave out is admitted first, in one call; coverage stays
// consistent. A filtered file the question names stays filtered.
func TestQuestionNamedFileIsIncluded(t *testing.T) {
	run := func(question string) (*Result, *harness) {
		t.Helper()
		h := pinHarness()
		res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Question: question})
		if err != nil {
			t.Fatal(err)
		}
		if len(h.llm.calls) != 1 {
			t.Fatalf("LLM calls = %d, want 1 (pr_ask does not use parts)", len(h.llm.calls))
		}
		return res, h
	}

	base, _ := run("Is the retry bounded?")
	if slices.Contains(base.Coverage.Included, "src/f28.go") || !slices.Contains(base.Coverage.Omitted.Modified, "src/f28.go") {
		t.Fatalf("without naming it, src/f28.go is not omitted: %+v", base.Coverage)
	}

	// src/f28.go by full path, src/f17.go by its base name (6 characters);
	// pinned files keep their input order, so f17 comes before f28.
	res, h := run("Is the retry in src/f28.go bounded? And f17.go? See vendor/lib.go.")
	c := res.Coverage
	if len(c.Included) < 2 || !slices.Equal(c.Included[:2], []string{"src/f17.go", "src/f28.go"}) {
		t.Errorf("Included = %q, want src/f17.go and src/f28.go first, in input order", c.Included)
	}
	if !strings.Contains(h.llm.calls[0].user, "src/f28.go") {
		t.Errorf("the named file is not in the prompt")
	}
	if slices.Contains(c.Included, "vendor/lib.go") || len(c.Filtered) != 1 || c.Filtered[0].Path != "vendor/lib.go" {
		t.Errorf("a named filtered file was re-added: included %q, filtered %+v", c.Included, c.Filtered)
	}
	if c.ReviewedFiles+c.NotReviewedFiles != c.TotalFiles || c.TotalFiles != 30 || !c.Partial || c.ModelCalls != 1 {
		t.Errorf("coverage = %+v", c)
	}
	if len(c.Included) != len(base.Coverage.Included) {
		t.Errorf("pinning changed how many files fit: %d, before %d", len(c.Included), len(base.Coverage.Included))
	}
}
