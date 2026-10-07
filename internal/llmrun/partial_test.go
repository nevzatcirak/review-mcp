package llmrun

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// prepared is a diff with every coverage category present once or twice.
func prepared() *diffpipe.Prepared {
	return &diffpipe.Prepared{
		Included:      []string{"a.go", "b.go"},
		Clipped:       []string{"c.go"},
		DeletedListed: []string{"g.go", "h.go"},
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
			{Path: "huge.go", Reason: diffpipe.SkipTooLarge},
			{Path: "go.sum", Reason: provider.SkipFiltered},
			{Path: "vendor/x.js", Reason: provider.SkipFiltered},
		},
	}
}

// TestCoverageAccounting is the X-18 accounting: the mapping of every
// category, and reviewed + not reviewed == total.
func TestCoverageAccounting(t *testing.T) {
	c := BuildCoverage(prepared(), nil)
	// reviewed: a, b and the deletions listed by name g, h (X-20). Not
	// reviewed: clipped c; omitted d, e, f; skipped for size (big.sql),
	// limit (many1.go), too large for a chunk (huge.go), unreadable
	// (gone.go, odd.patch). Outside the count: img.png (binary), renamed.go
	// (empty diff) and the two filtered files.
	want := Coverage{}
	want.Partial, want.ReviewedFiles, want.NotReviewedFiles, want.TotalFiles = true, 4, 9, 13
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

// TestCoverageDeletedListedIsReviewed: X-20. A PR whose only deletions are
// listed by name is complete; the JSON carries them as deleted_listed.
func TestCoverageDeletedListedIsReviewed(t *testing.T) {
	c := BuildCoverage(&diffpipe.Prepared{Included: []string{"a.go"}, DeletedListed: []string{"gone.go"}}, nil)
	if c.Partial || c.ReviewedFiles != 2 || c.NotReviewedFiles != 0 || c.TotalFiles != 2 {
		t.Errorf("listed deletion: %+v", c)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"deleted_listed":["gone.go"]`) {
		t.Errorf("JSON lacks deleted_listed: %s", raw)
	}
	raw, _ = json.Marshal(BuildCoverage(&diffpipe.Prepared{}, nil))
	if !strings.Contains(string(raw), `"deleted_listed":[]`) {
		t.Errorf("empty deleted_listed is not an empty array: %s", raw)
	}
}

// TestTrimCoverageDeletedListed: a listed deletion stays reviewed only while
// its name is in the lines the guard kept; the others move to the front of
// Omitted.Deleted, in the section's order.
func TestTrimCoverageDeletedListed(t *testing.T) {
	text := "\n\n## File: 'a.go'\n\n@@ -1 +1 @@\n__new hunk__\n1 +x\n\n\nDeleted files:\n\ng1.go\ng2.go\ng3.go"
	lines := strings.Split(text, "\n")
	at := func(name string) int {
		for i, l := range lines {
			if l == name {
				return i
			}
		}
		t.Fatalf("no line %q", name)
		return 0
	}
	for _, tc := range []struct {
		name         string
		kept         int
		listed, left []string
	}{
		{"all kept", len(lines), []string{"g1.go", "g2.go", "g3.go"}, []string{"cut.go"}},
		{"cut after g2", at("g2.go") + 1, []string{"g1.go", "g2.go"}, []string{"g3.go", "cut.go"}},
		{"cut at g1", at("g1.go"), []string{}, []string{"g1.go", "g2.go", "g3.go", "cut.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Coverage{Included: []string{"a.go"}, Clipped: []string{}, DeletedListed: []string{"g1.go", "g2.go", "g3.go"},
				Omitted: OmittedFiles{Deleted: []string{"cut.go"}}}
			TrimCoverage(&c, text, tc.kept, map[string]provider.ChangeType{"a.go": provider.ChangeModified})
			if !slices.Equal(c.DeletedListed, tc.listed) || !slices.Equal(c.Omitted.Deleted, tc.left) {
				t.Errorf("DeletedListed %q, Omitted.Deleted %q; want %q, %q", c.DeletedListed, c.Omitted.Deleted, tc.listed, tc.left)
			}
			if c.ReviewedFiles != 1+len(tc.listed) || c.ReviewedFiles+c.NotReviewedFiles != c.TotalFiles {
				t.Errorf("summary: reviewed %d, not reviewed %d, total %d", c.ReviewedFiles, c.NotReviewedFiles, c.TotalFiles)
			}
		})
	}
	// Without the section in the text no name is reported as listed.
	c := Coverage{Included: []string{}, Clipped: []string{}, DeletedListed: []string{"g1.go"}}
	TrimCoverage(&c, "\n\n## File: 'a.go'\n", 2, nil)
	if len(c.DeletedListed) != 0 || !slices.Equal(c.Omitted.Deleted, []string{"g1.go"}) {
		t.Errorf("no section: %+v", c)
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
