package llmrun

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// prepared is a diff with every coverage category present once or twice.
func prepared() *diffpipe.Prepared {
	return &diffpipe.Prepared{
		Included: []string{"a.go", "b.go"},
		Clipped:  []string{"c.go"},
		Omitted: diffpipe.Omitted{
			Added: []string{"d.go"}, Modified: []string{"e.go"}, Deleted: []string{"f.go"},
		},
		Skipped: []provider.SkippedFile{
			{Path: "img.png", Reason: provider.SkipBinary},
			{Path: "big.sql", Reason: provider.SkipSizeLimit},
			{Path: "many1.go", Reason: provider.SkipFileLimit},
			{Path: "gone.go", Reason: provider.SkipFetchFailed},
			{Path: "renamed.go", Reason: diffpipe.SkipEmptyDiff},
			{Path: "odd.patch", Reason: diffpipe.SkipUnparseablePatch},
			{Path: "go.sum", Reason: provider.SkipFiltered},
			{Path: "vendor/x.js", Reason: provider.SkipFiltered},
		},
	}
}

// TestCoverageAccounting is the X-18 accounting: the mapping of every
// category, and reviewed + not reviewed == total.
func TestCoverageAccounting(t *testing.T) {
	c := BuildCoverage(prepared(), nil)
	// reviewed: a, b. Not reviewed: clipped c; omitted d, e, f; skipped for
	// size (big.sql), limit (many1.go), unreadable (gone.go, odd.patch).
	// Outside the count: img.png (binary), renamed.go (empty diff) and the
	// two filtered files.
	want := Coverage{}
	want.Partial, want.ReviewedFiles, want.NotReviewedFiles, want.TotalFiles = true, 2, 8, 10
	if c.Partial != want.Partial || c.ReviewedFiles != want.ReviewedFiles ||
		c.NotReviewedFiles != want.NotReviewedFiles || c.TotalFiles != want.TotalFiles {
		t.Errorf("counts = partial %v, reviewed %d, not reviewed %d, total %d; want %v, %d, %d, %d",
			c.Partial, c.ReviewedFiles, c.NotReviewedFiles, c.TotalFiles,
			want.Partial, want.ReviewedFiles, want.NotReviewedFiles, want.TotalFiles)
	}
	if c.ReviewedFiles+c.NotReviewedFiles != c.TotalFiles {
		t.Errorf("reviewed %d + not reviewed %d != total %d", c.ReviewedFiles, c.NotReviewedFiles, c.TotalFiles)
	}
	if len(c.Filtered) != 2 {
		t.Errorf("filtered = %v", c.Filtered)
	}
}

// TestCoverageClippedAloneIsPartial: a clipped file alone makes the result
// partial, and it is not counted as reviewed.
func TestCoverageClippedAloneIsPartial(t *testing.T) {
	c := BuildCoverage(&diffpipe.Prepared{Included: []string{"a.go", "b.go"}, Clipped: []string{"c.go"}}, nil)
	if !c.Partial || c.ReviewedFiles != 2 || c.NotReviewedFiles != 1 || c.TotalFiles != 3 {
		t.Errorf("clipped alone: %+v", c)
	}
}

// TestCoverageFilteredOnlyIsComplete: files excluded on purpose (and files
// with nothing to review) do not make a result partial.
func TestCoverageFilteredOnlyIsComplete(t *testing.T) {
	c := BuildCoverage(&diffpipe.Prepared{
		Included: []string{"a.go"},
		Skipped: []provider.SkippedFile{
			{Path: "go.sum", Reason: provider.SkipFiltered},
			{Path: "logo.png", Reason: provider.SkipBinary},
			{Path: "moved.go", Reason: diffpipe.SkipEmptyDiff},
		},
	}, nil)
	if c.Partial || c.ReviewedFiles != 1 || c.NotReviewedFiles != 0 || c.TotalFiles != 1 {
		t.Errorf("filtered only: %+v", c)
	}
	// Nothing at all to review is complete, not partial.
	if empty := BuildCoverage(&diffpipe.Prepared{}, nil); empty.Partial || empty.TotalFiles != 0 {
		t.Errorf("empty: %+v", empty)
	}
}

// TestCoverageUnknownSkipReasonIsNotReviewed: a reason this code does not
// know counts as a lost file, never as fine.
func TestCoverageUnknownSkipReasonIsNotReviewed(t *testing.T) {
	c := BuildCoverage(&diffpipe.Prepared{
		Skipped: []provider.SkippedFile{{Path: "x.go", Reason: "some_future_reason"}},
	}, nil)
	if !c.Partial || c.NotReviewedFiles != 1 || c.TotalFiles != 1 {
		t.Errorf("unknown reason: %+v", c)
	}
}

// TestCoverageJSONFields: the structured result carries the four fields,
// also for a complete result (false and zero are values, not absences).
func TestCoverageJSONFields(t *testing.T) {
	raw, err := json.Marshal(BuildCoverage(&diffpipe.Prepared{Included: []string{"a.go"}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"partial": false, "reviewed_files": 1.0, "total_files": 1.0, "not_reviewed_files": 0.0} {
		if got, ok := m[k]; !ok || got != want {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, want)
		}
	}
}

// TestPartialNotes: the budget hint names diff.max_tokens only when that cap
// was the limit that applied; provider skips get their own sentence.
func TestPartialNotes(t *testing.T) {
	omitted := Coverage{Omitted: OmittedFiles{Modified: []string{"a.go"}}}
	window := tokens.Budget{ContextWindow: 20000, Factor: 0.3}
	capped := tokens.Budget{ContextWindow: 20000, Factor: 0.3, MaxDiffTokens: 2000}
	// A cap above what the window allows is not the limit that applied.
	loose := tokens.Budget{ContextWindow: 20000, Factor: 0.3, MaxDiffTokens: 19000}
	for _, tc := range []struct {
		name string
		c    Coverage
		b    tokens.Budget
		want []string
	}{
		{"cap applied", omitted, capped, []string{NoteRaiseLimit}},
		{"window applied", omitted, window, []string{NoteLargerWindow}},
		{"cap set but not the limit", omitted, loose, []string{NoteLargerWindow}},
		{"clipped", Coverage{Clipped: []string{"a.go"}}, capped, []string{NoteRaiseLimit}},
		{"provider skip only", Coverage{Skipped: []SkippedFile{{Path: "a.go", Reason: provider.SkipSizeLimit}}}, capped,
			[]string{NoteProviderSkips}},
		{"both", Coverage{Omitted: omitted.Omitted, Skipped: []SkippedFile{{Path: "a.go", Reason: provider.SkipFetchFailed}}},
			window, []string{NoteLargerWindow, NoteProviderSkips}},
		{"complete", Coverage{Included: []string{"a.go"}, Filtered: []SkippedFile{{Path: "go.sum", Reason: "x"}}}, capped, nil},
	} {
		got := PartialNotes(&tc.c, tc.b)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: notes = %q, want %q", tc.name, got, tc.want)
		}
	}
	if strings.Contains(NoteLargerWindow, "diff.max_tokens") || !strings.Contains(NoteRaiseLimit, "diff.max_tokens") {
		t.Error("only the cap hint may name diff.max_tokens")
	}
}
