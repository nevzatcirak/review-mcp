package review

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// bigFiles are n modified files of about 700 tokens each.
func bigFiles(n int) []provider.FilePatch {
	var files []provider.FilePatch
	for i := range n {
		files = append(files, provider.FilePatch{
			Path: fmt.Sprintf("src/f%02d.go", i), Type: provider.ChangeModified,
			Patch:      "@@ -1,2 +1,60 @@\n a\n" + strings.Repeat("+some added line of code here\n", 60),
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		})
	}
	return files
}

// TestPartialCoverageAndHint: a diff.max_tokens cap that leaves files out
// makes the result partial, the counts add up, and the hint names
// diff.max_tokens. Without the cap, a context window that is too small gives
// the hint without it. A complete run has no partial note.
func TestPartialCoverageAndHint(t *testing.T) {
	files := bigFiles(30)
	prepare := func(cap *int, window int) *Result {
		t.Helper()
		h := newHarness(goodAnswer)
		h.prov.files = files
		h.deps.Config.Diff.MaxTokens = cap
		h.deps.Config.LLM.ContextWindow = window
		// One call (review.max_chunks 1), as in v1.0; the hints of a review
		// in parts are tested in chunked_test.go.
		pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL, MaxChunks: 1})
		if err != nil {
			t.Fatal(err)
		}
		return pl.Result
	}
	check := func(name string, res *Result, wantNote string) {
		t.Helper()
		c := res.Coverage
		if !c.Partial || c.NotReviewedFiles == 0 || c.TotalFiles != len(files) ||
			c.ReviewedFiles+c.NotReviewedFiles != c.TotalFiles || c.ReviewedFiles != len(c.Included) {
			t.Errorf("%s: coverage counts = partial %v, reviewed %d, not reviewed %d, total %d", name,
				c.Partial, c.ReviewedFiles, c.NotReviewedFiles, c.TotalFiles)
		}
		got := slices.DeleteFunc(slices.Clone(res.Notes), func(n string) bool {
			return n != llmrun.NoteRaiseLimit && n != llmrun.NoteLargerWindow
		})
		if !slices.Equal(got, []string{wantNote}) {
			t.Errorf("%s: partial notes = %q, want %q", name, got, wantNote)
		}
	}

	capTokens := 2000
	check("cap", prepare(&capTokens, 32000), llmrun.NoteRaiseLimit)
	check("window", prepare(nil, 8000), llmrun.NoteLargerWindow)

	res := prepare(nil, 200000)
	if c := res.Coverage; c.Partial || c.NotReviewedFiles != 0 || c.ReviewedFiles != len(files) || c.TotalFiles != len(files) {
		t.Errorf("complete run: %+v", c)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "To review every file") {
			t.Errorf("complete run has the partial hint: %q", n)
		}
	}
}

// TestFilteredAndBinaryFilesDoNotMakeAPartialRun: a file the ignore rules
// exclude and a binary file the provider skips are listed in the coverage
// but are not reviewable changes (X-18).
func TestFilteredAndBinaryFilesDoNotMakeAPartialRun(t *testing.T) {
	h := newHarness(goodAnswer)
	h.prov.files = append(bigFiles(2), provider.FilePatch{Path: "go.sum", Type: provider.ChangeModified,
		Patch: "@@ -1 +1 @@\n-a\n+b\n"})
	h.prov.skipped = []provider.SkippedFile{{Path: "logo.png", Reason: provider.SkipBinary}}
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	c := pl.Result.Coverage
	if c.Partial || c.TotalFiles != 2 || c.ReviewedFiles != 2 || len(c.Filtered) != 1 || len(c.Skipped) != 1 {
		t.Errorf("filtered and binary files made the run partial: %+v", c)
	}
}
