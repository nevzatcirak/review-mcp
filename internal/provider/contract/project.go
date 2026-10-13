package contract

// The project-context scenario (X-32, WP-5c). Like RunReviewCI it is not a
// provider contract: it prepares pr_review and pr_improve (no model call)
// through the real provider and the fixture's fake server, so that the
// guidelines and the file tree are read at the base revision on every
// provider that has a Fixture. Only _test.go files import it.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/improve"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/projctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Markers and paths of projectPR.
const (
	projGuidelines = ".review-mcp/guidelines.md"
	projBaseRule   = "BASE-RULE-2c8e: handlers live in src/util."
	projHeadRule   = "HEAD-RULE-f41a: handlers live anywhere."
)

// projectPR edits the guidelines file, adds a file next to an unchanged one
// (src/util/helpers.go, an Unchanged file of samplePR) and adds a file in a
// directory that does not exist at the base.
func projectPR() Spec {
	pr := samplePR()
	pr.Title, pr.Description = "Move handlers", "Synthetic pull request of the project-context scenario."
	pr.Files = []File{
		{Path: projGuidelines, Type: provider.ChangeModified, Base: projBaseRule + "\n", Head: projHeadRule + "\n",
			Hunks: "@@ -1 +1 @@\n-" + projBaseRule + "\n+" + projHeadRule + "\n"},
		{Path: "src/util/new.go", Type: provider.ChangeAdded, Head: "package util\n", Hunks: "@@ -0,0 +1 @@\n+package util\n"},
		{Path: "web/pages/home.go", Type: provider.ChangeAdded, Head: "package pages\n", Hunks: "@@ -0,0 +1 @@\n+package pages\n"},
	}
	pr.Threads, pr.Reviewers, pr.Checks = nil, nil, nil
	return pr
}

// readProbe wraps the served provider and records the revision of every
// GetFileAt and ListDir.
type readProbe struct {
	provider.Provider
	mu   sync.Mutex
	revs []string
}

func (r *readProbe) GetFileAt(ctx context.Context, ref provider.PRRef, path, rev string) ([]byte, error) {
	r.mu.Lock()
	r.revs = append(r.revs, rev)
	r.mu.Unlock()
	return r.Provider.GetFileAt(ctx, ref, path, rev)
}

func (r *readProbe) ListDir(ctx context.Context, ref provider.PRRef, dir, rev string) (*provider.DirListing, error) {
	r.mu.Lock()
	r.revs = append(r.revs, rev)
	r.mu.Unlock()
	return r.Provider.ListDir(ctx, ref, dir, rev)
}

// RunReviewProject runs the project-context scenario of X-32 against f, for
// pr_review and pr_improve: the guidelines file the pull request edits is
// given to the model as the base has it, the touched directories are listed
// at the base (a new one as new), and nothing is read at the head.
func RunReviewProject(t *testing.T, f Fixture) {
	t.Helper()
	var tr Traits
	if dc, ok := f.(Declarer); ok {
		tr = dc.Traits()
	}
	s := &suite{f: f, tr: tr}
	wantTree := "```\n" + strings.Join([]string{
		".review-mcp/", "  guidelines.md",
		"src/util/", "  helpers.go",
		"web/pages/ (new directory)",
	}, "\n") + "\n```"

	check := func(t *testing.T, user, system string, cov llmrun.Coverage, probe *readProbe, base string) {
		t.Helper()
		header := projctx.GuidelinesHeader(projGuidelines)
		g, tree := strings.Index(user, header), strings.Index(user, projctx.TreeHeader)
		if g < 0 || tree < g {
			t.Fatalf("the prompt lacks the guidelines or the tree block (at %d, %d):\n%s", g, tree, user)
		}
		block := user[g:tree]
		if !strings.Contains(block, projBaseRule) || strings.Contains(block, projHeadRule) {
			t.Errorf("the guidelines block is not the base version:\n%s", block)
		}
		if !strings.Contains(user[tree:], wantTree) {
			t.Errorf("the tree block is not the base listing; want %q in:\n%s", wantTree, user[tree:])
		}
		if !strings.Contains(system, "looks misplaced") {
			t.Errorf("the system prompt lacks the convention sentence")
		}
		if cov.Guidelines == nil || cov.Guidelines.Path != projGuidelines || cov.Guidelines.Status != llmrun.ProjectUsed ||
			cov.FileTree == nil || *cov.FileTree != (llmrun.FileTree{Directories: 3, Status: llmrun.ProjectUsed}) {
			t.Errorf("coverage %+v %+v", cov.Guidelines, cov.FileTree)
		}
		probe.mu.Lock()
		defer probe.mu.Unlock()
		if len(probe.revs) == 0 {
			t.Error("nothing was read")
		}
		for _, r := range probe.revs {
			if r != base {
				t.Errorf("a read at %q, want only the base %q", r, base)
			}
		}
	}
	setup := func(t *testing.T) (*readProbe, provider.PRRef, *config.Config, string) {
		t.Helper()
		p, ref, _ := s.serve(t, projectPR())
		cfg := config.Defaults()
		cfg.LLM.BaseURL, cfg.LLM.Model, cfg.LLM.ContextWindow = "https://llm.example.com/v1", "test-model", 32000
		pr, err := p.GetPullRequest(t.Context(), ref)
		if err != nil {
			t.Fatalf("GetPullRequest: %v", err)
		}
		return &readProbe{Provider: p}, ref, cfg, pr.BaseSHA
	}

	t.Run("pr_review", func(t *testing.T) {
		probe, ref, cfg, base := setup(t)
		pl, err := review.Prepare(t.Context(), review.Deps{Config: cfg, Resolver: fixedResolver{ref, probe}, LLM: &cannedLLM{}},
			review.Args{PRURL: "https://git.example.com/octo/demo/pulls/7"})
		if err != nil {
			t.Fatalf("review.Prepare: %v", err)
		}
		check(t, pl.Prompts.User, pl.Prompts.System, pl.Result.Coverage, probe, base)
	})
	t.Run("pr_improve", func(t *testing.T) {
		probe, ref, cfg, base := setup(t)
		pl, err := improve.Prepare(t.Context(), improve.Deps{Config: cfg, Resolver: fixedResolver{ref, probe}, LLM: &cannedLLM{}},
			improve.Args{PRURL: "https://git.example.com/octo/demo/pulls/7"})
		if err != nil {
			t.Fatalf("improve.Prepare: %v", err)
		}
		check(t, pl.Prompts.User, pl.Prompts.System, pl.Result.Coverage, probe, base)
	})
}
