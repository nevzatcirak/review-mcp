package repocfg

import (
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

func impactOperator() *config.Config {
	c := operatorConfig()
	c.Context.Repo.Enabled = true
	c.Context.Impact.Enabled = true
	c.Context.Impact.Depth = 2
	c.Context.Impact.MaxCallersPerNode = 5
	c.Context.Impact.MaxLinks = 40
	c.Context.Impact.Paths = []string{"services"}
	c.Context.Impact.ExcludePaths = []string{"generated"}
	c.Context.Impact.EntryPoints = []string{"api/**"}
	return c
}

// TestImpactRepoConfigCannotTurnOn is canary (g): the repository config
// cannot turn the trace on, only off.
func TestImpactRepoConfigCannotTurnOn(t *testing.T) {
	base := impactOperator()
	base.Context.Impact.Enabled = false
	o := Apply(base, readBase(t, "[context.impact]\nenabled = true\n"))
	if o.Config.Context.Impact.Enabled || !slices.Contains(o.Result.Ignored, "context.impact.enabled") {
		t.Fatalf("repository turned the trace on: enabled=%v applied=%v ignored=%v", o.Config.Context.Impact.Enabled, o.Result.Applied, o.Result.Ignored)
	}
	o = Apply(impactOperator(), readBase(t, "[context.impact]\nenabled = false\n"))
	if o.Config.Context.Impact.Enabled || !slices.Contains(o.Result.Applied, "context.impact.enabled") {
		t.Fatalf("repository could not turn the trace off: %+v", o.Result)
	}
}

// TestImpactRepoConfigOnlyLowers is canary (h): the repository config can
// only lower depth (and the other numbers).
func TestImpactRepoConfigOnlyLowers(t *testing.T) {
	o := Apply(impactOperator(), readBase(t, "[context.impact]\ndepth = 3\nmax_callers_per_node = 20\nmax_links = 200\n"))
	if got := o.Config.Context.Impact; got.Depth != 2 || got.MaxCallersPerNode != 5 || got.MaxLinks != 40 {
		t.Fatalf("a higher value was accepted: %+v", got)
	}
	o = Apply(impactOperator(), readBase(t, "[context.impact]\ndepth = 1\nmax_callers_per_node = 3\nmax_links = 10\n"))
	if got := o.Config.Context.Impact; got.Depth != 1 || got.MaxCallersPerNode != 3 || got.MaxLinks != 10 {
		t.Fatalf("a lower value was not applied: %+v", got)
	}
	o = Apply(impactOperator(), readBase(t, "[context.impact]\ndepth = 4\n"))
	if o.Config.Context.Impact.Depth != 2 || !slices.Contains(o.Result.Ignored, "context.impact.depth") {
		t.Fatalf("an out-of-range depth was not ignored: %+v %+v", o.Config.Context.Impact, o.Result)
	}
}

func TestImpactRepoConfigLists(t *testing.T) {
	base := impactOperator()
	o := Apply(base, readBase(t, `[context.impact]
paths = ["billing/", "orders"]
exclude_paths = ["legacy"]
entry_points = ["web/**/*Controller.java"]
`))
	got := o.Config.Context.Impact
	if !slices.Equal(got.Paths, []string{"billing/", "orders"}) || !slices.Equal(got.ExcludePaths, []string{"generated", "legacy"}) ||
		!slices.Equal(got.EntryPoints, []string{"web/**/*Controller.java"}) {
		t.Fatalf("lists: %+v", got)
	}
	if !slices.Equal(base.Context.Impact.Paths, []string{"services"}) || !slices.Equal(base.Context.Impact.ExcludePaths, []string{"generated"}) {
		t.Fatalf("operator configuration changed: %+v", base.Context.Impact)
	}
	for _, bad := range []string{
		`paths = ["/abs"]`, `paths = ["a/../b"]`, `paths = [""]`, `exclude_paths = ["a\\b"]`, `paths = [".git/hooks"]`,
		`entry_points = ["["]`, `paths = "one"`, `paths = [` + strings.Repeat(`"a",`, 21) + `]`,
	} {
		o := Apply(impactOperator(), readBase(t, "[context.impact]\n"+bad+"\n"))
		if len(o.Result.Ignored) == 0 || !slices.Equal(o.Config.Context.Impact.Paths, []string{"services"}) || !slices.Equal(o.Config.Context.Impact.ExcludePaths, []string{"generated"}) {
			t.Fatalf("%s: accepted: %+v %+v", bad, o.Result, o.Config.Context.Impact)
		}
	}
	// Other context keys stay operator-only.
	o = Apply(impactOperator(), readBase(t, "[context.impact]\nmax_tokens = 600\nenabled_extra = true\n[context.repo]\nenabled = false\n"))
	if o.Config.Context.Impact.MaxTokens != 4000 || !o.Config.Context.Repo.Enabled || len(o.Result.Applied) != 0 {
		t.Fatalf("operator-only key applied: %+v", o.Result)
	}
}

// TestPathPrefixRulesAgree keeps config.PathPrefixProblem and
// ValidPathPrefix in agreement.
func TestPathPrefixRulesAgree(t *testing.T) {
	for _, p := range []string{"a", "a/", "a/b", "a//b", "/a", "a\\b", ".", "..", "a/./b", "a/../b", ".git", "x/.GIT/y", "", "/", "a\tb", strings.Repeat("a", 4097), "ü/ö"} {
		if (config.PathPrefixProblem(p) == "") != ValidPathPrefix(p) {
			t.Errorf("%q: config %q, repocfg %v", p, config.PathPrefixProblem(p), ValidPathPrefix(p))
		}
	}
}
