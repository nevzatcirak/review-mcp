package review

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
)

// Distinctive repository text of the fake trace: it must reach the prompt,
// never the logs (canary (l)).
const (
	impactCallerName = "CallerNameSentinel7f"
	impactCallerPath = "pkg/callersentinel/use.go"
	impactUseText    = "IMPACT-USE-SENTINEL-2b8d"
)

// fakeTracer is a repository-context backend that also traces: it returns
// res and records the queries.
type fakeTracer struct {
	fakeRepo
	res     gitctx.ImpactResult
	err     error
	queries []gitctx.ImpactQuery
}

func (f *fakeTracer) Trace(_ context.Context, _ gitctx.Checkout, q gitctx.ImpactQuery) (gitctx.ImpactResult, error) {
	f.queries = append(f.queries, q)
	return f.res, f.err
}

// sampleTrace is a trace from Alpha (pkg/a.go) to a caller and the
// caller's caller.
func sampleTrace() gitctx.ImpactResult {
	return gitctx.ImpactResult{
		Nodes: []gitctx.ImpactNode{
			{Name: "Alpha", Path: "pkg/a.go", Parent: -1, Rank: gitctx.RankSignature},
			{Name: impactCallerName, Path: impactCallerPath, DefLine: 5, DefText: "func " + impactCallerName + "() {",
				UseLine: 7, UseText: "a := 1\nb := 2\nAlpha() // " + impactUseText + "\nc := 3\nd := 4", Hop: 1, Parent: 0, Kind: gitctx.KindCall},
			{Name: "Outer", Path: "cmd/outer.go", DefLine: 3, DefText: "func Outer() {", UseLine: 4,
				UseText: "a()\nb()\n" + impactCallerName + "()\nc()\nd()", Hop: 2, Parent: 1, Kind: gitctx.KindCall, Entry: true,
				NotFollowed: gitctx.NotFollowedDepth},
		},
		Seeds: 1, Links: 2, DepthReached: 2, NotFollowed: 1, NotFollowedBy: map[string]int{gitctx.NotFollowedDepth: 1},
		EntryPointsReached: 1,
	}
}

// impactFiles change the signature of Alpha and Beta: existing names with
// callers, so the trace has seeds.
func impactFiles() []provider.FilePatch {
	mk := func(path, old, def string) provider.FilePatch {
		return provider.FilePatch{Path: path, Type: provider.ChangeModified,
			Patch:      "@@ -1,3 +1,3 @@\n a\n-" + old + "\n+" + def + "\n b\n",
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap}
	}
	return []provider.FilePatch{mk("pkg/a.go", "func Alpha(n int) {}", "func Alpha() {}"), mk("pkg/b.go", "func Beta(n int) {}", "func Beta() {}")}
}

func impactHarness(answers ...string) (*harness, *fakeTracer) {
	h := newHarness(answers...)
	h.prov.files = impactFiles()
	h.deps.Config.Context.Repo.Enabled = true
	h.deps.Config.Context.Impact.Enabled = true
	ft := &fakeTracer{res: sampleTrace()}
	h.deps.RepoContext = ft
	return h, ft
}

func TestImpactBlockInPrompt(t *testing.T) {
	h, ft := impactHarness(goodAnswer)
	pl := prepareOK(t, h)
	user := pl.Prompts.User
	for _, want := range []string{repoctx.ImpactHeader, "Alpha  (signature changed)  pkg/a.go", "← " + impactCallerName + "  " + impactCallerPath + ":5",
		"uses at :7:", impactUseText, "← Outer  cmd/outer.go:3  [entry point]", "uses at :4: " + impactCallerName + "()"} {
		if !strings.Contains(user, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, user)
		}
	}
	if strings.Index(user, repoctx.ImpactHeader) > strings.Index(user, "The PR code diff:") {
		t.Error("the impact block follows the diff")
	}
	if len(ft.queries) != 1 {
		t.Fatalf("traces = %d, want 1", len(ft.queries))
	}
	q := ft.queries[0]
	if q.Depth != 2 || q.MaxCallersPerNode != 5 || q.MaxLinks != 40 || len(q.Hunks["pkg/a.go"]) != 1 || q.Hunks["pkg/a.go"][0] != (gitctx.LineRange{From: 2, To: 2}) {
		t.Fatalf("query = %+v", q)
	}
	got := pl.Result.Coverage.Impact
	want := &llmrun.Impact{Status: llmrun.ImpactOn, Seeds: 1, Links: 2, DepthReached: 2, EntryPointsReached: 1,
		NotFollowed: llmrun.ImpactNotFollowed{Depth: 1}, ScopeSource: llmrun.ImpactScopeNone, MaxTokens: 4000}
	if *got != *want {
		t.Fatalf("coverage.impact = %+v, want %+v", got, want)
	}
	if pl.Result.Coverage.RepoContext.MaxTokens != 2000 {
		t.Fatalf("coverage.repo_context.max_tokens = %d", pl.Result.Coverage.RepoContext.MaxTokens)
	}
}

// TestImpactPathsReplaceForTheCall is canary (i): impact_paths replaces
// the configured paths for that call only.
func TestImpactPathsReplaceForTheCall(t *testing.T) {
	h, ft := impactHarness(goodAnswer)
	h.deps.Config.Context.Impact.Enabled = false
	h.deps.Config.Context.Impact.Paths = []string{"services"}
	pl := prepareWith(t, h, Args{ImpactPaths: []string{"billing/"}})
	if len(ft.queries) != 1 || !slices.Equal(ft.queries[0].Scope.Paths, []string{"billing/"}) {
		t.Fatalf("scope paths = %+v, want only the argument's", ft.queries)
	}
	if pl.Result.Coverage.Impact.ScopeSource != llmrun.ImpactScopeTool {
		t.Fatalf("scope source = %s", pl.Result.Coverage.Impact.ScopeSource)
	}
	if !slices.Equal(h.deps.Config.Context.Impact.Paths, []string{"services"}) {
		t.Fatalf("the operator configuration changed: %v", h.deps.Config.Context.Impact.Paths)
	}
	// The next call without the argument uses the configured paths again.
	h.deps.Config.Context.Impact.Enabled = true
	prepareOK(t, h)
	if len(ft.queries) != 2 || !slices.Equal(ft.queries[1].Scope.Paths, []string{"services"}) {
		t.Fatalf("second call scope = %+v", ft.queries[1].Scope)
	}
}

const callerAnswer = "```yaml\nreview:\n" +
	"  estimated_effort_to_review: 2\n" +
	"  relevant_tests: |\n    No\n" +
	"  key_issues_to_review:\n" +
	"    - relevant_file: " + impactCallerPath + "\n      issue_header: Possible Bug\n      issue_content: The caller still passes the old unit.\n" +
	"      start_line: 7\n      end_line: 7\n" +
	"    - relevant_file: pkg/a.go\n      issue_header: Style\n      issue_content: In the diff.\n" +
	"      start_line: 2\n      end_line: 2\n" +
	"  security_concerns: |\n    No\n" +
	"  performance_concerns: |\n    No\n```\n"

// TestCallerFindingOverviewOnly is canary (j): a finding in a caller is
// listed in the overview only, with a link to the line at the head commit;
// no inline placement is attempted for it.
func TestCallerFindingOverviewOnly(t *testing.T) {
	h, _ := impactHarness(callerAnswer)
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true}.WithConfigDefaults(h.deps.Config))
	if err != nil {
		t.Fatal(err)
	}
	kis := res.Review.KeyIssuesToReview
	if len(kis) != 2 {
		t.Fatalf("findings = %+v", kis)
	}
	c := kis[0]
	if !c.Caller || c.Link != "https://your-gitea.example/octo/demo/src/commit/abc/"+impactCallerPath+"#L7" || c.InlineStatus != "" {
		t.Fatalf("caller finding = %+v", c)
	}
	if kis[1].Caller {
		t.Fatalf("a finding in the diff is marked as a caller: %+v", kis[1])
	}
	for _, batch := range h.prov.inline {
		for _, it := range batch {
			if strings.Contains(it.Path, "callersentinel") {
				t.Fatalf("inline placement attempted for a caller: %+v", it)
			}
		}
	}
	validateResult(t, res)
}

// TestImpactLogsCountsOnly is canary (l): no node name, path or snippet
// in the logs at debug level.
func TestImpactLogsCountsOnly(t *testing.T) {
	h, _ := impactHarness(callerAnswer)
	if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL}); err != nil {
		t.Fatal(err)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "review: impact trace") {
		t.Fatalf("no impact log record:\n%s", logs)
	}
	for _, s := range []string{impactCallerName, impactCallerPath, impactUseText, "callersentinel", "cmd/outer.go", "Outer()"} {
		if strings.Contains(logs, s) {
			t.Fatalf("the logs carry %q:\n%s", s, logs)
		}
	}
}

// runImpact runs the review of h and validates the result against the
// output schema.
func runImpact(t *testing.T, h *harness, args Args) *Result {
	t.Helper()
	args.PRURL = testPRURL
	res, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	validateResult(t, res)
	return res
}

func TestImpactStatuses(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		h, ft := impactHarness(goodAnswer)
		h.deps.Config.Context.Impact.Enabled = false
		res := runImpact(t, h, Args{})
		if len(ft.queries) != 0 || res.Coverage.Impact.Status != llmrun.ImpactOff || strings.Contains(h.llm.calls[0].user, repoctx.ImpactHeader) {
			t.Fatalf("off: %+v", res.Coverage.Impact)
		}
	})
	t.Run("repo context off with impact_paths", func(t *testing.T) {
		h, ft := impactHarness(goodAnswer)
		h.deps.Config.Context.Repo.Enabled = false
		res := runImpact(t, h, Args{ImpactPaths: []string{"pkg"}})
		if len(ft.queries) != 0 || res.Coverage.Impact.Status != llmrun.ImpactRepoContextOff ||
			!slices.Contains(res.Notes, NoteImpactRepoContextOff) {
			t.Fatalf("repo off: %+v %v", res.Coverage.Impact, res.Notes)
		}
	})
	t.Run("repo context off from the configuration", func(t *testing.T) {
		h, _ := impactHarness(goodAnswer)
		h.deps.Config.Context.Repo.Enabled = false
		pl := prepareOK(t, h)
		if pl.Result.Coverage.Impact.Status != llmrun.ImpactRepoContextOff || slices.Contains(pl.Result.Notes, NoteImpactRepoContextOff) {
			t.Fatalf("repo off (config): %+v %v", pl.Result.Coverage.Impact, pl.Result.Notes)
		}
	})
	t.Run("no symbols", func(t *testing.T) {
		h, ft := impactHarness(goodAnswer)
		h.prov.files = []provider.FilePatch{{Path: "docs/guide.md", Type: provider.ChangeModified, Patch: "@@ -1 +1 @@\n-a\n+b\n"}}
		res := runImpact(t, h, Args{})
		if len(ft.queries) != 0 || res.Coverage.Impact.Status != llmrun.ImpactNoSymbols {
			t.Fatalf("no symbols: %+v", res.Coverage.Impact)
		}
	})
	t.Run("failed", func(t *testing.T) {
		h, ft := impactHarness(goodAnswer)
		ft.err = &gitctx.Error{Reason: gitctx.ReasonGitFailed}
		res := runImpact(t, h, Args{})
		if res.Coverage.Impact.Status != llmrun.ImpactFailed {
			t.Fatalf("failed: %+v", res.Coverage.Impact)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		h, ft := impactHarness(goodAnswer)
		ft.res.Truncated = true
		ft.res.NotFollowedBy[gitctx.NotFollowedBudget] = 3
		res := runImpact(t, h, Args{})
		im := res.Coverage.Impact
		if !im.Truncated || im.NotFollowed.Budget != 3 || !slices.Contains(res.Notes, NoteImpactTruncated) {
			t.Fatalf("truncated: %+v %v", im, res.Notes)
		}
	})
	t.Run("scope sources", func(t *testing.T) {
		h, _ := impactHarness(goodAnswer)
		h.deps.Config.Context.Impact.EntryPoints = []string{"cmd/**"}
		res := runImpact(t, h, Args{})
		if res.Coverage.Impact.ScopeSource != llmrun.ImpactScopeOperator {
			t.Fatalf("operator: %s", res.Coverage.Impact.ScopeSource)
		}
		h2, ft2 := impactHarness(goodAnswer)
		h2.prov.revs["base0"][".review-mcp.toml"] = "[context.impact]\nexclude_paths = [\"legacy\"]\n"
		res2 := runImpact(t, h2, Args{})
		if res2.Coverage.Impact.ScopeSource != llmrun.ImpactScopeRepoConfig || !slices.Contains(ft2.queries[0].Scope.ExcludePaths, "legacy") {
			t.Fatalf("repo_config: %s (%+v)", res2.Coverage.Impact.ScopeSource, res2.RepoConfig)
		}
		h3, _ := impactHarness(goodAnswer)
		h3.deps.Config.Context.Impact.Enabled = false
		res3 := runImpact(t, h3, Args{ImpactPaths: []string{"pkg"}})
		if res3.Coverage.Impact.ScopeSource != llmrun.ImpactScopeTool || res3.Coverage.Impact.Status != llmrun.ImpactOn {
			t.Fatalf("tool: %+v", res3.Coverage.Impact)
		}
	})
	t.Run("impact_paths with supplied findings", func(t *testing.T) {
		h, _ := impactHarness(goodAnswer)
		if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true, ImpactPaths: []string{"pkg"},
			Findings: []SuppliedFinding{supplied("pkg/a.go", 2, 2, "H", "C")}}); !errors.Is(err, ErrImpactPathsWithFindings) {
			t.Fatalf("impact_paths with findings: %v", err)
		}
	})
}

// TestImpactSuppliedFindingsSkipped is canary (s1) of WP-20c (review DQ-1
// of WP-20b): a run with supplied findings and the trace enabled traces
// nothing and says skipped_client_findings, never off; off stays for a
// trace that is not enabled.
func TestImpactSuppliedFindingsSkipped(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		h, ft := impactHarness(goodAnswer)
		h.deps.Config.Context.Impact.Enabled = enabled
		res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true,
			Findings: []SuppliedFinding{supplied("pkg/a.go", 2, 2, "H", "C")}})
		if err != nil {
			t.Fatal(err)
		}
		want := llmrun.ImpactOff
		if enabled {
			want = llmrun.ImpactSkippedClientFindings
		}
		if len(ft.queries) != 0 || res.Coverage.Impact.Status != want {
			t.Fatalf("enabled=%v: status %q, want %q (%d traces)", enabled, res.Coverage.Impact.Status, want, len(ft.queries))
		}
		validateResult(t, res)
	}
}

func TestValidateImpactPaths(t *testing.T) {
	if ValidateImpactPaths(nil) != nil || ValidateImpactPaths([]string{"a", "b/c/"}) != nil {
		t.Fatal("valid paths refused")
	}
	for _, bad := range [][]string{{}, {""}, {"/a"}, {"a/../b"}, {strings.Repeat("a", 201)}, slices.Repeat([]string{"a"}, 11), {".git"}, {"a\\b"}} {
		if ValidateImpactPaths(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestImpactInParts: the trace runs once; each part shows the chains whose
// seeds are in its own files.
func TestImpactInParts(t *testing.T) {
	h, ft := impactHarness(goodAnswer, goodAnswer, goodAnswer, goodAnswer)
	h.prov.files = toolBigFiles(6, 400)
	h.deps.Config.LLM.ContextWindow = 9000
	tr := sampleTrace()
	tr.Nodes[0].Path = h.prov.files[5].Path
	tr.Nodes[0].Name = "Delta"
	ft.res = tr
	// File 5 changes the signature of Delta, the seed of the chain.
	h.prov.files[5].Patch += "@@ -2,1 +402,1 @@\n-func Delta(n int) {}\n+func Delta() {}\n"
	pl := prepareWith(t, h, Args{MaxChunks: 4})
	if len(pl.parts) < 2 {
		t.Fatalf("parts = %d", len(pl.parts))
	}
	if len(ft.queries) != 1 {
		t.Fatalf("traces = %d, want 1", len(ft.queries))
	}
	with := 0
	for _, pt := range pl.parts {
		if strings.Contains(pt.fit.prompts.User, repoctx.ImpactHeader) {
			with++
			if !strings.Contains(pt.fit.prompts.User, h.prov.files[5].Path) {
				t.Error("the chain is in a part without its seed's file")
			}
		}
	}
	if with != 1 {
		t.Fatalf("the chain is in %d parts, want 1", with)
	}
}

// localLike is the fake provider without file-line URLs, as for local work.
type localLike struct{ *fakeProvider }

func (localLike) FileLineURL(provider.PRRef, *provider.PullRequest, string, int) string { return "" }

// TestCallerFindingWithoutURL: where the provider has no file-line URL
// (local work), a finding in a caller keeps its path and line and gets no
// link.
func TestCallerFindingWithoutURL(t *testing.T) {
	h, _ := impactHarness(callerAnswer)
	h.deps.Resolver = localResolverFor(localLike{h.prov})
	res := runImpact(t, h, Args{})
	c := res.Review.KeyIssuesToReview[0]
	if !c.Caller || c.Link != "" || c.RelevantFile != impactCallerPath || c.StartLine != 7 {
		t.Fatalf("caller finding = %+v", c)
	}
}

type fixedResolver struct{ p provider.Provider }

func (r fixedResolver) Resolve(string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: testPRURL}, r.p, nil
}

func localResolverFor(p provider.Provider) Resolver { return fixedResolver{p} }

// impactSentence is the focus sentence of V-18 §2.6, verbatim.
const impactSentence = "This review covers the effects of this change on the rest of the code only: callers and implementations that the change breaks or silently alters (arguments, return values, null or error cases, units, ordering, side effects, overridden behaviour), shown in the impact chains and the repository context. Report a problem only when this pull request causes it, and name the affected file and line."

func TestImpactFocus(t *testing.T) {
	if got, err := NormalizeFocus(" Impact ", nil); err != nil || got.Name != FocusImpact || got.Kind != FocusKindBuiltin {
		t.Fatalf("NormalizeFocus = %+v, %v", got, err)
	}
	h, _ := impactHarness(goodAnswer)
	res := runImpact(t, h, Args{Focus: FocusImpact})
	sys, user := h.llm.calls[0].system, h.llm.calls[0].user
	if !strings.Contains(sys, impactSentence) || !strings.Contains(user, repoctx.ImpactHeader) {
		t.Fatalf("focus impact: sentence %v, block %v", strings.Contains(sys, impactSentence), strings.Contains(user, repoctx.ImpactHeader))
	}
	if slices.Contains(res.Notes, NoteImpactFocusWithoutTrace) || res.Focus != FocusImpact {
		t.Fatalf("notes %v, focus %q", res.Notes, res.Focus)
	}
	// Without the trace: repository context only, and the note.
	for name, set := range map[string]func(h *harness){
		"trace off":        func(h *harness) { h.deps.Config.Context.Impact.Enabled = false },
		"repo context off": func(h *harness) { h.deps.Config.Context.Repo.Enabled = false },
	} {
		h, ft := impactHarness(goodAnswer)
		set(h)
		res := runImpact(t, h, Args{Focus: FocusImpact})
		if len(ft.queries) != 0 || !slices.Contains(res.Notes, NoteImpactFocusWithoutTrace) ||
			!strings.Contains(h.llm.calls[0].system, impactSentence) {
			t.Fatalf("%s: queries %d, notes %v", name, len(ft.queries), res.Notes)
		}
	}
	// Another focus with the trace off has no such note.
	h2, _ := impactHarness(goodAnswer)
	h2.deps.Config.Context.Impact.Enabled = false
	if res := runImpact(t, h2, Args{Focus: FocusTests}); slices.Contains(res.Notes, NoteImpactFocusWithoutTrace) {
		t.Fatal("the note on another focus")
	}
}

func TestImpactFocusFromRepoConfig(t *testing.T) {
	h, _ := impactHarness(goodAnswer)
	h.prov.revs["base0"][".review-mcp.toml"] = "[review]\nfocuses = [\"impact\", \"tests\"]\n" +
		"[[review.focus_rules]]\npaths = [\"pkg/**\"]\nfocus = \"impact\"\n"
	res := runImpact(t, h, Args{})
	for _, k := range []string{"review.focuses", "review.focus_rules"} {
		if !slices.Contains(res.RepoConfig.Applied, k) {
			t.Fatalf("%s not applied: %+v", k, res.RepoConfig)
		}
	}
}
