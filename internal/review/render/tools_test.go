package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// toolsRows are the rows of the tools line (v2.4 spec V-8 §5): the run's
// repo_context.tools, on the "complete" fixture.
func toolsRows() map[string]*llmrun.RepoTools {
	call := func(st string) llmrun.RepoToolCall {
		return llmrun.RepoToolCall{Tool: "read_file", Target: "internal/secret/path.go", Status: st}
	}
	return map[string]*llmrun.RepoTools{
		"tools_used": {Status: "used", Rounds: 2, Calls: []llmrun.RepoToolCall{call("ok"), call("ok"), call("ok")}},
		"tools_used_failed": {Status: "used", Rounds: 2, Calls: []llmrun.RepoToolCall{
			call("ok"), call("budget_exhausted"), call("not_available"), call("budget_exhausted")}},
		"tools_not_used":     {Status: "not_used"},
		"tools_skipped":      {Status: "skipped", Reason: "endpoint_no_tools"},
		"tools_repo_ctx_off": {Status: "off", Reason: "repo_context_off"},
	}
}

func completeWithTools(tools *llmrun.RepoTools) *review.Result {
	res := fixtures()["complete"]
	res.Coverage.RepoContext = llmrun.RepoContext{Status: llmrun.RepoUsed, Symbols: 2, References: 3, Files: 1, Tools: tools}
	return res
}

func TestToolsLineGoldens(t *testing.T) {
	profiles := map[string]func(*review.Result) string{
		"client":    Client,
		"gitea":     func(r *review.Result) string { return Provider(r, capsGitea) },
		"bitbucket": func(r *review.Result) string { return Provider(r, capsBB) },
	}
	for name, tools := range toolsRows() {
		for profile, render := range profiles {
			t.Run(profile+"/"+name, func(t *testing.T) {
				got := render(completeWithTools(tools))
				path := filepath.Join("testdata", profile+"_"+name+".md")
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
					t.Errorf("%s differs\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
				}
			})
		}
	}
}

// TestToolsOffIsByteIdentical: a result with tools off by configuration (and
// one without the accounting) renders as the rc.1 golden of the same
// result.
func TestToolsOffIsByteIdentical(t *testing.T) {
	for profile, render := range map[string]func(*review.Result) string{
		"client":    Client,
		"gitea":     func(r *review.Result) string { return Provider(r, capsGitea) },
		"bitbucket": func(r *review.Result) string { return Provider(r, capsBB) },
	} {
		want, err := os.ReadFile(filepath.Join("testdata", profile+"_complete.md")) //nolint:gosec // fixed testdata path
		if err != nil {
			t.Fatal(err)
		}
		for name, tools := range map[string]*llmrun.RepoTools{"nil": nil, "off": {Status: "off", Reason: "off"}} {
			res := fixtures()["complete"]
			res.Coverage.RepoContext.Tools = tools
			if got := render(res); got != string(want) {
				t.Errorf("%s/%s differs from the complete golden:\n%s", profile, name, got)
			}
		}
	}
}
