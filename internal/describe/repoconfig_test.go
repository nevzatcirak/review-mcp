package describe

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// TestRepoConfigChangesTheRun: keys of the file reach pr_describe: the
// language of the prompt and the ignore list, and the result says what was
// applied. Nothing but the one read at the base revision is made.
func TestRepoConfigChangesTheRun(t *testing.T) {
	h := newHarness(nil)
	h.prov.repoFiles = map[string]string{".review-mcp.toml@base0": "[output]\nlanguage = \"tr\"\n[ignore]\nglob = [\"src/app_test.go\"]\n"}
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pl.Result.Notes[0], "were applied: output.language, ignore.glob.") {
		t.Errorf("notes %q", pl.Result.Notes)
	}
	rc := pl.Result.RepoConfig
	if rc.Status != "applied" || !slices.Equal(rc.Applied, []string{"output.language", "ignore.glob"}) {
		t.Errorf("repo_config = %+v", rc)
	}
	if !slices.ContainsFunc(pl.Result.Coverage.Filtered, func(f llmrun.SkippedFile) bool { return f.Path == "src/app_test.go" }) {
		t.Errorf("filtered %v: ignore.glob of the file is not applied", pl.Result.Coverage.Filtered)
	}
	if pl.in.Language != "tr" {
		t.Errorf("language %q", pl.in.Language)
	}
	if !slices.Equal(h.prov.reads, []string{".review-mcp.toml@base0"}) {
		t.Errorf("reads %q, want one read at the base revision", h.prov.reads)
	}
}

func TestRepoConfigAbsentMakesNoNote(t *testing.T) {
	h := newHarness(nil)
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Result.RepoConfig.Status != "absent" || slices.ContainsFunc(pl.Result.Notes, func(n string) bool { return strings.Contains(n, ".review-mcp.toml") }) {
		t.Errorf("repo_config %+v notes %q", pl.Result.RepoConfig, pl.Result.Notes)
	}
}
