package render

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

var update = flag.Bool("update", false, "rewrite the render goldens")

// toolsRows are the rows of the tools line (v2.4 spec V-8 §5).
func toolsRows() map[string]*llmrun.RepoTools {
	call := func(st string) llmrun.RepoToolCall {
		return llmrun.RepoToolCall{Tool: "search", Target: "needle", Status: st}
	}
	return map[string]*llmrun.RepoTools{
		"tools_used":         {Status: "used", Rounds: 1, Calls: []llmrun.RepoToolCall{call("ok")}},
		"tools_used_failed":  {Status: "used", Rounds: 2, Calls: []llmrun.RepoToolCall{call("ok"), call("too_many_calls"), call("too_many_calls")}},
		"tools_not_used":     {Status: "not_used"},
		"tools_skipped":      {Status: "skipped", Reason: "no_room"},
		"tools_repo_ctx_off": {Status: "off", Reason: "repo_context_off"},
	}
}

func withTools(tools *llmrun.RepoTools) *ask.Result {
	res := completeSample()
	res.Coverage.RepoContext = llmrun.RepoContext{Status: llmrun.RepoUsed, Symbols: 1, References: 2, Files: 1, Tools: tools}
	return res
}

func TestToolsLineGoldens(t *testing.T) {
	profiles := map[string]func(*ask.Result) string{
		"client":    Client,
		"gitea":     func(r *ask.Result) string { return Provider(r, capsGitea) },
		"bitbucket": func(r *ask.Result) string { return Provider(r, capsBB) },
	}
	for name, tools := range toolsRows() {
		for profile, render := range profiles {
			t.Run(profile+"/"+name, func(t *testing.T) {
				got := render(withTools(tools))
				path := filepath.Join("testdata", profile+"_"+name+".md")
				if *update {
					if err := os.MkdirAll("testdata", 0o750); err != nil {
						t.Fatal(err)
					}
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

// TestToolsOffIsByteIdentical: tools off by configuration, and no tools
// accounting at all, render the same bytes.
func TestToolsOffIsByteIdentical(t *testing.T) {
	for profile, render := range map[string]func(*ask.Result) string{
		"client":    Client,
		"gitea":     func(r *ask.Result) string { return Provider(r, capsGitea) },
		"bitbucket": func(r *ask.Result) string { return Provider(r, capsBB) },
	} {
		want := render(withTools(nil))
		if got := render(withTools(&llmrun.RepoTools{Status: "off", Reason: "off"})); got != want {
			t.Errorf("%s: off/off differs from no accounting:\n%s", profile, got)
		}
	}
}
