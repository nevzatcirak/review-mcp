package render

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// TestPartialDescribeBanner pins the pr_describe banner (v2 spec §3.5,
// verbatim) with the singular forms, and that a complete result has none.
func TestPartialDescribeBanner(t *testing.T) {
	for _, tc := range []struct {
		c    llmrun.Coverage
		want string
	}{
		{llmrun.Coverage{Included: []string{"a", "b"}, Omitted: llmrun.OmittedFiles{Modified: []string{"c"}}},
			"**Partial description: 2 of 3 changed files were described. 1 file was not described (see Coverage).**"},
		{llmrun.Coverage{Included: []string{"a"}, Skipped: []llmrun.SkippedFile{{Path: "b", Reason: "not_returned"}, {Path: "c", Reason: "model_call_failed"}}},
			"**Partial description: 1 of 3 changed files was described. 2 files were not described (see Coverage).**"},
		{llmrun.Coverage{Clipped: []string{"a"}},
			"**Partial description: 0 of 1 changed file was described. 1 file was not described (see Coverage).**"},
		{llmrun.Coverage{Included: []string{"a"}, Skipped: []llmrun.SkippedFile{{Path: "img.png", Reason: "binary"}},
			Filtered: []llmrun.SkippedFile{{Path: "vendor/x.go", Reason: "glob"}}}, ""},
	} {
		if got := PartialDescribeBanner(&tc.c); got != tc.want {
			t.Errorf("banner = %q, want %q", got, tc.want)
		}
	}
}

// TestDescribeCoverageLine: a description in parts says "Described in N
// model calls."; everything else is the shared coverage section.
func TestDescribeCoverageLine(t *testing.T) {
	c := llmrun.Coverage{Included: []string{"a"}, ModelCalls: 3}
	var d, r strings.Builder
	DescribeCoverage(&d, "## Coverage", &c)
	Coverage(&r, "## Coverage", &c)
	if !strings.Contains(d.String(), "- Described in 3 model calls.\n") ||
		strings.Replace(d.String(), "Described in", "Reviewed in", 1) != r.String() {
		t.Errorf("describe coverage:\n%s\nreview coverage:\n%s", d.String(), r.String())
	}
}
