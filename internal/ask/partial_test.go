package ask

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// TestPartialAnswerCountsAndHint: a diff.max_tokens cap that leaves files out
// makes the answer partial, the counts add up and the hint names the cap; a
// complete run is not partial.
func TestPartialAnswerCountsAndHint(t *testing.T) {
	var files []provider.FilePatch
	for i := range 30 {
		files = append(files, provider.FilePatch{
			Path: fmt.Sprintf("src/f%02d.go", i), Type: provider.ChangeModified,
			Patch:      "@@ -1,2 +1,60 @@\n a\n" + strings.Repeat("+some added line of code here\n", 60),
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		})
	}
	run := func(cap *int, window int) *Result {
		t.Helper()
		h := newHarness("ok")
		h.prov.files = files
		h.deps.Config.Diff.MaxTokens = cap
		h.deps.Config.LLM.ContextWindow = window
		res, err := Run(context.Background(), h.deps, h.args())
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	hints := func(res *Result) []string {
		return slices.DeleteFunc(slices.Clone(res.Notes), func(n string) bool {
			return n != llmrun.NoteRaiseLimit && n != llmrun.NoteLargerWindow
		})
	}

	capTokens := 2000
	res := run(&capTokens, 32000)
	c := res.Coverage
	if !c.Partial || c.NotReviewedFiles == 0 || c.TotalFiles != len(files) ||
		c.ReviewedFiles+c.NotReviewedFiles != c.TotalFiles || c.ReviewedFiles != len(c.Included) {
		t.Errorf("capped coverage: %+v", c)
	}
	if got := hints(res); !slices.Equal(got, []string{llmrun.NoteRaiseLimit}) {
		t.Errorf("capped hint = %q", got)
	}

	res = run(nil, 8000)
	if !res.Coverage.Partial {
		t.Errorf("small window: %+v", res.Coverage)
	}
	if got := hints(res); !slices.Equal(got, []string{llmrun.NoteLargerWindow}) {
		t.Errorf("window hint = %q", got)
	}

	res = run(nil, 200000)
	if c := res.Coverage; c.Partial || c.ReviewedFiles != len(files) || c.TotalFiles != len(files) || len(hints(res)) != 0 {
		t.Errorf("complete run: %+v, notes %q", c, res.Notes)
	}
}
