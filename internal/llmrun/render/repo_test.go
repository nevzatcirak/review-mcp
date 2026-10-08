package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// TestCoverageRepositoryContextLine: the coverage section gains the
// "Repository context" line of RC-9 when repository context was used or
// skipped, and nothing at all when it is off (also for a zero value), so a
// result without the feature renders as it always did.
func TestCoverageRepositoryContextLine(t *testing.T) {
	render := func(rc llmrun.RepoContext) string {
		c := llmrun.Coverage{Included: []string{"a.go"}, RepoContext: rc}
		var b strings.Builder
		Coverage(&b, "## Coverage", &c)
		return b.String()
	}
	off := render(llmrun.RepoContext{Status: llmrun.RepoOff})
	if zero := render(llmrun.RepoContext{}); zero != off || strings.Contains(off, "Repository context") {
		t.Errorf("off renders a line:\n%s", off)
	}
	for _, tc := range []struct {
		rc   llmrun.RepoContext
		want string
	}{
		{llmrun.RepoContext{Status: "used", Symbols: 5, References: 7, Files: 3}, "- Repository context: 5 symbols, 7 references from 3 files\n"},
		{llmrun.RepoContext{Status: "used", Symbols: 1, References: 1, Files: 1}, "- Repository context: 1 symbol, 1 reference from 1 file\n"},
		{llmrun.RepoContext{Status: "used"}, "- Repository context: 0 symbols, 0 references from 0 files\n"},
		{llmrun.RepoContext{Status: "skipped", Reason: "auth"}, "- Repository context: skipped: `auth`\n"},
		{llmrun.RepoContext{Status: "skipped", Reason: "budget"}, "- Repository context: skipped: `budget`\n"},
	} {
		got := render(tc.rc)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%+v: missing %q in\n%s", tc.rc, tc.want, got)
		}
		// Only that line differs from the off rendering.
		if strings.Replace(got, tc.want, "", 1) != off {
			t.Errorf("%+v: more than the one line changed:\n%s", tc.rc, got)
		}
	}
}

func TestRepoContextZeroValueMarshalsAsOff(t *testing.T) {
	raw, err := json.Marshal(llmrun.Coverage{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"repo_context":{"status":"off","reason":"","symbols":0,"references":0,"files":0}`) {
		t.Errorf("coverage JSON = %s", raw)
	}
}
