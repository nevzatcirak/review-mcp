package improve

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// TestRepoConfigChangesTheRun: keys of the file reach pr_improve: the
// suggestion cap and the score floor can only be tightened, the ignore list
// is added to, and the result says what was applied.
func TestRepoConfigChangesTheRun(t *testing.T) {
	h := newHarness(nil)
	h.prov.revs["base0"][".review-mcp.toml"] = "[improve]\nmax_suggestions = 3\nmin_score = 9\n[ignore]\nglob = [\"src/app_test.go\"]\n"
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if pl.maxTotal != 3 || pl.minScore != 9 {
		t.Errorf("max suggestions %d, min score %d, want the file's 3 and 9", pl.maxTotal, pl.minScore)
	}
	rc := pl.Result.RepoConfig
	if rc.Status != "applied" || !slices.Equal(rc.Applied, []string{"ignore.glob", "improve.max_suggestions", "improve.min_score"}) {
		t.Errorf("repo_config = %+v", rc)
	}
	if !slices.ContainsFunc(pl.Result.Coverage.Filtered, func(f llmrun.SkippedFile) bool { return f.Path == "src/app_test.go" }) {
		t.Errorf("filtered %v: ignore.glob of the file is not applied", pl.Result.Coverage.Filtered)
	}
	if !slices.Equal(h.prov.configReads, []string{".review-mcp.toml@base0"}) {
		t.Errorf("config reads %q, want one read at the base revision", h.prov.configReads)
	}

	// A looser value is capped by the operator's.
	h = newHarness(nil)
	h.prov.revs["base0"][".review-mcp.toml"] = "[improve]\nmax_suggestions = 30\nmin_score = 1\n"
	pl, err = Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if pl.maxTotal != 8 || pl.minScore != 7 {
		t.Errorf("max suggestions %d, min score %d, want the operator's 8 and 7", pl.maxTotal, pl.minScore)
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
