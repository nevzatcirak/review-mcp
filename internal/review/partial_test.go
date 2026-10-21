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

// fileLimitSkips are n files the provider skipped as file_limit.
func fileLimitSkips(n int) []provider.SkippedFile {
	var out []provider.SkippedFile
	for i := range n {
		out = append(out, provider.SkippedFile{Path: fmt.Sprintf("late/g%02d.go", i), Reason: provider.SkipFileLimit})
	}
	return out
}

// checkFileLimitNote checks the note of n files lost to diff.max_files_full_content
// (WP-14 A.2) and the check that the generic provider note stays out.
func checkFileLimitNote(t *testing.T, label string, res *Result, n int) {
	t.Helper()
	want := fmt.Sprintf(llmrun.NoteFileLimitFormat, llmrun.CountPhrase(n, "file", "files"))
	if !slices.Contains(res.Notes, want) || slices.Contains(res.Notes, llmrun.NoteProviderSkips) {
		t.Errorf("%s: notes = %q, want %q and no generic provider note", label, res.Notes, want)
	}
	if !res.Coverage.Partial {
		t.Errorf("%s: coverage is not partial", label)
	}
}

// TestFileLimitNoteReview (WP-14 A.2): a review that lost files to
// diff.max_files_full_content names the key; so do a focused run, a merged
// publish of supplied findings, and a review that skipped every file.
func TestFileLimitNoteReview(t *testing.T) {
	h := newHarness(goodAnswer)
	h.prov.skipped = fileLimitSkips(68)
	checkFileLimitNote(t, "review", runOK(t, h, Args{PRURL: testPRURL}), 68)

	h = newHarness(goodAnswer)
	h.prov.skipped = fileLimitSkips(2)
	checkFileLimitNote(t, "focused", runOK(t, h, Args{PRURL: testPRURL, Focus: "security"}), 2)

	h = newSuppliedHarness(t)
	h.prov.skipped = fileLimitSkips(1)
	findings := []SuppliedFinding{supplied("src/app.go", 11, 11, "Possible Bug", "Line 11 is wrong.")}
	res := runOK(t, h, Args{PRURL: testPRURL, Publish: true, Findings: findings})
	checkFileLimitNote(t, "merged publish", res, 1)

	// Nothing reviewable is left: no model call, the note is still given.
	h = newSuppliedHarness(t)
	h.prov.files = nil
	h.prov.skipped = fileLimitSkips(5)
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if !pl.Empty {
		t.Fatal("the run is not empty")
	}
	checkFileLimitNote(t, "all files skipped", pl.Result, 5)
}

// TestChunkedFileLimitNote (WP-14 A.2): a review in parts that used every
// part it may and still left files out, with files also lost to the file
// limit, has the budget hint and the file-limit note, in that order.
func TestChunkedFileLimitNote(t *testing.T) {
	h, _ := chunkHarness(t, nil)
	h.prov.files = bigFiles(30)
	h.prov.skipped = fileLimitSkips(7)
	h.deps.Config.Review.MaxChunks = 2
	res := runChunked(t, h, Args{})
	if len(res.Coverage.Omitted.Modified) == 0 {
		t.Fatalf("no file left out: %+v", res.Coverage)
	}
	want := []string{llmrun.NoteRaiseChunksLimit, fmt.Sprintf(llmrun.NoteFileLimitFormat, "7 files")}
	var got []string
	for _, n := range res.Notes {
		if n == want[0] || strings.Contains(n, "diff.max_files_full_content") || n == llmrun.NoteProviderSkips {
			got = append(got, n)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("notes = %q, want %q", got, want)
	}
}
