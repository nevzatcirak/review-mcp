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

// TestRepoToolsLine: every row of the table of the v2.4 spec V-8 §5. Nil and
// off/off give no line, so a result without the tools renders as in rc.1.
func TestRepoToolsLine(t *testing.T) {
	call := func(st string) llmrun.RepoToolCall {
		return llmrun.RepoToolCall{Tool: "read_file", Target: "a.go", Status: st}
	}
	for _, tc := range []struct {
		name string
		t    *llmrun.RepoTools
		want string
	}{
		{"nil", nil, ""},
		{"off", &llmrun.RepoTools{Status: "off", Reason: "off"}, ""},
		{"off_zero_reason", &llmrun.RepoTools{Status: "off"}, ""},
		{"used", &llmrun.RepoTools{Status: "used", Rounds: 2, Calls: []llmrun.RepoToolCall{call("ok"), call("ok"), call("ok")}},
			"Repository tools: 3 calls in 2 rounds"},
		{"used_singular", &llmrun.RepoTools{Status: "used", Rounds: 1, Calls: []llmrun.RepoToolCall{call("ok")}},
			"Repository tools: 1 call in 1 round"},
		{"used_failed", &llmrun.RepoTools{Status: "used", Rounds: 2, Calls: []llmrun.RepoToolCall{
			call("too_many_calls"), call("ok"), call("budget_exhausted"), call("too_many_calls"), call("not_available")}},
			"Repository tools: 5 calls in 2 rounds; 1 `not_available`; 1 `budget_exhausted`; 2 `too_many_calls`"},
		{"not_used", &llmrun.RepoTools{Status: "not_used"}, "Repository tools: allowed, not used"},
		{"skipped", &llmrun.RepoTools{Status: "skipped", Reason: "endpoint_no_tools"}, "Repository tools: skipped: `endpoint_no_tools`"},
		{"repo_context_off", &llmrun.RepoTools{Status: "off", Reason: "repo_context_off"}, "Repository tools: off: `repo_context_off`"},
	} {
		if got := RepoToolsLine(tc.t); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCoverageRepoToolsLinePlacement: the line follows the "Repository
// context" line, and for off/off the output is byte-identical to a result
// without the tools accounting.
func TestCoverageRepoToolsLinePlacement(t *testing.T) {
	render := func(tools *llmrun.RepoTools) string {
		c := llmrun.Coverage{Included: []string{"a.go"}, RepoContext: llmrun.RepoContext{Status: "used", Symbols: 1, References: 1, Files: 1, Tools: tools}}
		var b strings.Builder
		Coverage(&b, "## Coverage", &c)
		return b.String()
	}
	base := render(nil)
	if got := render(&llmrun.RepoTools{Status: "off", Reason: "off"}); got != base {
		t.Errorf("off/off changed the output:\n%s", got)
	}
	ctx := "- Repository context: 1 symbol, 1 reference from 1 file\n"
	got := render(&llmrun.RepoTools{Status: "not_used"})
	if want := strings.Replace(base, ctx, ctx+"- Repository tools: allowed, not used\n", 1); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
