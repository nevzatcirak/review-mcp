package improve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/projctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Markers of the guidelines file at the base and at the head of a pull
// request that edits it (X-32).
const (
	baseRule       = "BASE-RULE-7e20: helpers live in src/util."
	headRule       = "HEAD-RULE-0b5d: helpers live anywhere."
	guidelinesPath = ".review-mcp/guidelines.md"
)

func prepare(t *testing.T, h *harness) *Plan {
	t.Helper()
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

// TestImproveGuidelinesReadAtBase: pr_improve gives the model the base
// version of a guidelines file the pull request edits, and the listing of
// the touched directories at the base; the head is never read, and the
// coverage reports both.
func TestImproveGuidelinesReadAtBase(t *testing.T) {
	h := newHarness(nil)
	h.prov.files = append(h.prov.files, provider.FilePatch{
		Path: guidelinesPath, Type: provider.ChangeModified, Additions: 1, Deletions: 1,
		Patch:      "@@ -1 +1 @@\n-" + baseRule + "\n+" + headRule + "\n",
		BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
	})
	h.prov.revs["base0"][guidelinesPath] = baseRule + "\n"
	head := sampleBaseTree()
	head[guidelinesPath] = headRule + "\n"
	h.prov.revs["abc"] = head

	pl := prepare(t, h)
	user := pl.Prompts.User
	header := projctx.GuidelinesHeader(guidelinesPath)
	g := strings.Index(user, header)
	tr := strings.Index(user, projctx.TreeHeader)
	diff := strings.Index(user, "The PR Diff:")
	if g < 0 || tr < g || diff < tr {
		t.Fatalf("order: guidelines %d, tree %d, diff %d", g, tr, diff)
	}
	if !strings.Contains(user[g:tr], baseRule) || strings.Contains(user[:diff], headRule) {
		t.Errorf("the guidelines block is not the base version:\n%s", user[g:tr])
	}
	// src holds two changed files, .review-mcp one.
	if want := "```\nsrc/\n  app.go\n  config.go\n.review-mcp/\n  guidelines.md\n```"; !strings.Contains(user[tr:diff], want) {
		t.Errorf("tree block:\n%s\nwant %q", user[tr:diff], want)
	}
	for _, r := range h.prov.reads {
		if !strings.HasSuffix(r, "@base0") {
			t.Errorf("read %q is not at the base revision", r)
		}
	}
	c := pl.Result.Coverage
	if c.Guidelines == nil || c.Guidelines.Path != guidelinesPath || c.Guidelines.Status != llmrun.ProjectUsed ||
		c.FileTree == nil || *c.FileTree != (llmrun.FileTree{Directories: 2, Status: llmrun.ProjectUsed}) {
		t.Errorf("coverage %+v %+v", c.Guidelines, c.FileTree)
	}
	if !strings.Contains(pl.Prompts.System, "A suggestion may say that a new file looks misplaced") {
		t.Errorf("the system prompt lacks the convention line")
	}
}

// TestImproveGuidelinesMissing: silent for the defaults, a note for a
// configured path; with both parts off nothing is read and the prompts
// carry neither block nor the convention line.
func TestImproveGuidelinesMissing(t *testing.T) {
	h := newHarness(nil)
	pl := prepare(t, h)
	if slices.Contains(pl.Result.Notes, projctx.NoteGuidelinesNotFound) || pl.Result.Coverage.Guidelines.Status != llmrun.ProjectNotFound {
		t.Errorf("defaults: notes %q, coverage %+v", pl.Result.Notes, *pl.Result.Coverage.Guidelines)
	}

	h = newHarness(nil)
	h.deps.Config.Review.GuidelinesPaths = []string{"docs/rules.md"}
	h.deps.Config.Review.GuidelinesPathsExplicit = true
	if pl := prepare(t, h); !slices.Contains(pl.Result.Notes, projctx.NoteGuidelinesNotFound) {
		t.Errorf("configured path: notes %q", pl.Result.Notes)
	}

	h = newHarness(nil)
	h.deps.Config.Review.GuidelinesPaths = []string{}
	h.deps.Config.Review.FileTree = false
	pl = prepare(t, h)
	if len(h.prov.reads) != 0 || strings.Contains(pl.Prompts.System, "looks misplaced") ||
		strings.Contains(pl.Prompts.User, projctx.TreeHeader) {
		t.Errorf("off: reads %q", h.prov.reads)
	}
	if pl.Result.Coverage.Guidelines.Status != llmrun.ProjectOff || pl.Result.Coverage.FileTree.Status != llmrun.ProjectOff {
		t.Errorf("off: coverage %+v %+v", *pl.Result.Coverage.Guidelines, *pl.Result.Coverage.FileTree)
	}
}

// improveBudgetHarness: a guidelines file of about 2500 tokens (within a
// guidelines budget of 3000) and a directory of 200 files (within a tree
// budget of 3000 tokens). The guidelines are large because pr_improve's
// prompt budget is at least the self-review's scaffolding plus the output
// reserve: smaller guidelines would never cost the diff any room.
func improveBudgetHarness(window int) *harness {
	h := newHarness(nil)
	var rules []string
	for i := range 130 {
		rules = append(rules, fmt.Sprintf("Rule %d: keep helpers small and name files after their type.", i))
	}
	h.prov.revs["base0"][guidelinesPath] = strings.Join(rules, "\n")
	for i := range 200 {
		h.prov.revs["base0"][fmt.Sprintf("src/generated_file_number_%03d.go", i)] = "x"
	}
	h.deps.Config.Review.MaxGuidelinesTokens = 3000
	h.deps.Config.Review.MaxTreeTokens = 3000
	h.deps.Config.LLM.ContextWindow = window
	return h
}

// TestImproveBudgetOrder: as in pr_review, the file tree gives way first
// and the guidelines are cut only after it; the diff is complete in both
// cases.
func TestImproveBudgetOrder(t *testing.T) {
	measure := func(tree, guidelines bool) *Plan {
		h := improveBudgetHarness(32000)
		h.deps.Config.Review.FileTree = tree
		if !guidelines {
			h.deps.Config.Review.GuidelinesPaths = nil
		}
		return prepare(t, h)
	}
	full, noTree, bare := measure(true, true), measure(false, true), measure(false, false)
	pFull, pG, pBare := full.Result.Metadata.PromptTokens, noTree.Result.Metadata.PromptTokens, bare.Result.Metadata.PromptTokens
	if full.Result.Coverage.FileTree.Status != llmrun.ProjectUsed || full.Result.Coverage.Guidelines.Status != llmrun.ProjectUsed ||
		pFull-pG < 1000 || pG-pBare < 800 {
		t.Fatalf("fixture: coverage %+v %+v, prompts %d, %d, %d", *full.Result.Coverage.Guidelines,
			*full.Result.Coverage.FileTree, pFull, pG, pBare)
	}
	reserve := tokens.Budget{MaxOutputTokens: full.Budget.MaxOutputTokens}.SoftReserve()
	diff := full.Result.Metadata.DiffTokens

	pl := prepare(t, improveBudgetHarness(reserve+pG+diff+200))
	if c := pl.Result.Coverage; c.FileTree.Status != llmrun.ProjectLeftOut || c.Guidelines.Status != llmrun.ProjectUsed || c.Partial ||
		!slices.Contains(pl.Result.Notes, projctx.NoteTreeDroppedForDiff) {
		t.Errorf("tree first: coverage %+v %+v, partial %v, notes %q", *c.Guidelines, *c.FileTree, c.Partial, pl.Result.Notes)
	}
	pl = prepare(t, improveBudgetHarness(reserve+pG+diff-(pG-pBare)/2))
	if c := pl.Result.Coverage; c.FileTree.Status != llmrun.ProjectLeftOut || c.Guidelines.Status != llmrun.ProjectCut || c.Partial ||
		!slices.Contains(pl.Result.Notes, projctx.NoteGuidelinesCutForDiff) {
		t.Errorf("then the guidelines: coverage %+v %+v, partial %v, notes %q", *c.Guidelines, *c.FileTree, c.Partial, pl.Result.Notes)
	}
}
