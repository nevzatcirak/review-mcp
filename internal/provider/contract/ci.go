package contract

// The pr_review CI scenario (X-31, WP-5b). Like RunReviewDuplicates it is
// not a provider contract: it prepares pr_review (review.Prepare, no model
// call) through the real provider and the fixture's fake server, so the CI
// summary, the prompt block and the overview line are checked on every
// provider that has a Fixture. Only _test.go files import it.

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
)

// ciProbe wraps the served provider: it counts GetChecks calls and can
// report another head commit, which the host does not know (a failed read).
type ciProbe struct {
	provider.Provider
	headSHA string
	calls   int
}

func (c *ciProbe) GetPullRequest(ctx context.Context, ref provider.PRRef) (*provider.PullRequest, error) {
	pr, err := c.Provider.GetPullRequest(ctx, ref)
	if err == nil && c.headSHA != "" {
		cp := *pr
		cp.HeadSHA = c.headSHA
		pr = &cp
	}
	return pr, err
}

func (c *ciProbe) GetChecks(ctx context.Context, ref provider.PRRef, sha string) (*provider.Checks, error) {
	c.calls++
	return c.Provider.GetChecks(ctx, ref, sha)
}

// RunReviewCI runs the CI scenario of X-31 against f: the five states of
// the summary, the prompt block, the overview line, the switch that turns
// the feature off and a hostile check name.
func RunReviewCI(t *testing.T, f Fixture) {
	t.Helper()
	var tr Traits
	if dc, ok := f.(Declarer); ok {
		tr = dc.Traits()
	}
	s := &suite{f: f, tr: tr}
	// Every host reports these four states.
	build := plainCheck("build", "https://ci.example.com/build/1", provider.CheckSuccess)
	tests := plainCheck("unit tests", "https://ci.example.com/test/1", provider.CheckFailure)
	lint := plainCheck("lint", "https://ci.example.com/lint/1", provider.CheckPending)
	docs := plainCheck("docs", "", provider.CheckNeutral)

	prepare := func(t *testing.T, checks []Check, mutate func(*config.Config, *ciProbe)) (*review.Plan, *ciProbe, provider.Provider) {
		t.Helper()
		pr := dupPR(false)
		pr.Checks = checks
		p, ref, _ := s.serve(t, pr)
		probe := &ciProbe{Provider: p}
		cfg := config.Defaults()
		cfg.LLM.BaseURL, cfg.LLM.Model, cfg.LLM.ContextWindow = "https://llm.example.com/v1", "test-model", 32000
		if mutate != nil {
			mutate(cfg, probe)
		}
		pl, err := review.Prepare(t.Context(), review.Deps{
			Config: cfg, Resolver: fixedResolver{ref, probe}, LLM: &cannedLLM{},
		}, review.Args{PRURL: "https://git.example.com/octo/demo/pulls/7"})
		if err != nil {
			t.Fatalf("review.Prepare: %v", err)
		}
		return pl, probe, p
	}
	overviews := func(pl *review.Plan, p provider.Provider) []string {
		return []string{render.Provider(pl.Result, p.Capabilities()), render.Client(pl.Result)}
	}
	wantAll := func(t *testing.T, what string, texts []string, wants ...string) {
		t.Helper()
		for _, text := range texts {
			for _, w := range wants {
				if !strings.Contains(text, w) {
					t.Errorf("%s lacks %q:\n%s", what, w, text)
				}
			}
		}
	}

	t.Run("failing", func(t *testing.T) {
		pl, _, p := prepare(t, []Check{build, tests, lint, docs}, nil)
		ci := pl.Result.CI
		if ci == nil || ci.State != review.CIFailing {
			t.Fatalf("ci = %+v, want failing", ci)
		}
		if want := (review.CICounts{Success: 1, Failure: 1, Pending: 1, Neutral: 1}); ci.Counts != want {
			t.Errorf("counts = %+v, want %+v", ci.Counts, want)
		}
		if len(ci.Checks) != 4 {
			t.Fatalf("%d checks listed, want 4", len(ci.Checks))
		}
		lead := []string{ci.Checks[0].Name, ci.Checks[1].Name}
		slices.Sort(lead)
		if !slices.Equal(lead, []string{"lint", "unit tests"}) {
			t.Errorf("the first two checks are %q, want the failing and the pending one", lead)
		}
		wantAll(t, "overview", overviews(pl, p), "CI: 1 failing, 1 pending, 2 passing",
			"[unit tests](https://ci.example.com/test/1)")
		wantAll(t, "prompt", []string{pl.Prompts.User}, review.CIHeader, "unit tests: failure", "lint: pending",
			"passing (success, neutral or skipped): 2")
		if strings.Contains(pl.Prompts.User, "build: ") || strings.Contains(pl.Prompts.User, "docs: ") {
			t.Errorf("the prompt lists a passing check:\n%s", pl.Prompts.User)
		}
		if !strings.Contains(pl.Prompts.System, "Never claim that a check passes or fails") {
			t.Errorf("the system prompt lacks the CI sentence")
		}
	})
	t.Run("pending", func(t *testing.T) {
		pl, _, p := prepare(t, []Check{build, lint}, nil)
		if ci := pl.Result.CI; ci == nil || ci.State != review.CIPending {
			t.Fatalf("ci = %+v, want pending", ci)
		}
		wantAll(t, "overview", overviews(pl, p), "CI: 0 failing, 1 pending, 1 passing")
		wantAll(t, "prompt", []string{pl.Prompts.User}, review.CIHeader, "lint: pending")
	})
	t.Run("passing", func(t *testing.T) {
		pl, _, p := prepare(t, []Check{build, docs}, nil)
		if ci := pl.Result.CI; ci == nil || ci.State != review.CIPassing {
			t.Fatalf("ci = %+v, want passing", ci)
		}
		wantAll(t, "overview", overviews(pl, p), "CI: 0 failing, 0 pending, 2 passing")
		wantAll(t, "prompt", []string{pl.Prompts.User}, review.CIHeader, "passing (success, neutral or skipped): 2")
	})
	t.Run("none", func(t *testing.T) {
		pl, _, p := prepare(t, nil, nil)
		if ci := pl.Result.CI; ci == nil || ci.State != review.CINone || len(ci.Checks) != 0 {
			t.Fatalf("ci = %+v, want none", ci)
		}
		wantAll(t, "overview", overviews(pl, p), "CI: no checks reported")
		if strings.Contains(pl.Prompts.User, review.CIHeader) {
			t.Errorf("the prompt has a CI block without checks")
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		// A head commit the host does not know: GetChecks answers not_found.
		pl, _, p := prepare(t, []Check{build, tests}, func(_ *config.Config, probe *ciProbe) {
			probe.headSHA = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
		})
		ci := pl.Result.CI
		if ci == nil || ci.State != review.CIUnreadable || ci.Note != review.NoteCIUnreadable || len(ci.Checks) != 0 {
			t.Fatalf("ci = %+v, want unreadable with the fixed note", ci)
		}
		wantAll(t, "overview", overviews(pl, p), "CI status could not be read")
		if strings.Contains(pl.Prompts.User, review.CIHeader) {
			t.Errorf("the prompt has a CI block for an unreadable CI")
		}
	})
	t.Run("off", func(t *testing.T) {
		pl, probe, p := prepare(t, []Check{build, tests}, func(cfg *config.Config, _ *ciProbe) {
			cfg.Review.CIStatus = false
		})
		if pl.Result.CI != nil || probe.calls != 0 {
			t.Errorf("ci = %+v after %d GetChecks calls, want none and no call", pl.Result.CI, probe.calls)
		}
		for _, o := range overviews(pl, p) {
			if strings.Contains(o, "CI") {
				t.Errorf("the overview mentions CI with review.ci_status off:\n%s", o)
			}
		}
		if strings.Contains(pl.Prompts.User, review.CIHeader) {
			t.Errorf("the prompt has a CI block with review.ci_status off")
		}
	})
	t.Run("hostile_name", func(t *testing.T) {
		hostile := plainCheck("x](https://example.com/evil) <img src=x onerror=1> **bold** [y](z)", "https://ci.example.com/h/1",
			provider.CheckFailure)
		pl, _, p := prepare(t, []Check{hostile}, nil)
		for _, o := range overviews(pl, p) {
			for _, bad := range []string{"<img", "](https://example.com/evil)", "**bold**", "[y]"} {
				if unescaped(o, bad) {
					t.Errorf("the overview carries %q from a check name:\n%s", bad, o)
				}
			}
		}
		if !strings.Contains(pl.Prompts.User, review.CIHeader) {
			t.Errorf("the prompt has no CI block")
		}
	})
}

// unescaped reports whether text holds bad at a place where no backslash
// escapes its first character: markup that would take effect.
func unescaped(text, bad string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], bad)
		if i < 0 {
			return false
		}
		i += from
		if i == 0 || text[i-1] != '\\' {
			return true
		}
		from = i + 1
	}
}
