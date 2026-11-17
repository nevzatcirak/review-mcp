package review

import (
	"context"
	"slices"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// The impact trace in the review pipeline (X-50, v2.10 spec V-18 §2.3 to
// §2.5 and §6 items 3 and 5). repoctx traces and renders; this file decides
// whether the trace runs, places its block in the room the diff leaves
// (after the repository-context block, the diff always wins), assigns the
// chains of a review in parts to the parts that hold their seeds, places a
// finding in a caller in the overview, and reports coverage.impact.

// Bounds of the impact_paths argument (§2.3).
const (
	MaxImpactPaths     = 10
	MaxImpactPathBytes = 200
)

// Fixed refusals and notes of the impact trace. They never echo a value.
var (
	// ErrImpactPathsInvalid: impact_paths broke its rules.
	ErrImpactPathsInvalid = invalidArgument("impact_paths must hold 1 to 10 repository-relative directory prefixes of at most " +
		"200 bytes each, without a leading \"/\", \"\\\", \".\" or \"..\" segments")
	// ErrImpactPathsWithFindings: a merged publish sends no prompt, so a
	// scope for the trace is a mistake.
	ErrImpactPathsWithFindings = invalidArgument("impact_paths cannot be combined with findings: a publish of supplied " +
		"findings calls no model and traces nothing")
)

// NoteImpactRepoContextOff: impact_paths was given, but repository context
// is off, so no trace runs.
const NoteImpactRepoContextOff = "impact_paths was ignored: the impact trace needs repository context " +
	"(context.repo.enabled), which is off."

// NoteImpactTruncated: the trace or its block was cut.
const NoteImpactTruncated = "The impact trace was cut by its budget (context.impact.max_links, context.impact.max_tokens " +
	"or the time limit); some callers are not shown."

// ValidateImpactPaths checks the impact_paths argument; nil (not given) is
// valid.
func ValidateImpactPaths(paths []string) error {
	if paths == nil {
		return nil
	}
	if len(paths) == 0 || len(paths) > MaxImpactPaths {
		return ErrImpactPathsInvalid
	}
	for _, p := range paths {
		if len(p) > MaxImpactPathBytes || config.PathPrefixProblem(p) != "" {
			return ErrImpactPathsInvalid
		}
	}
	return nil
}

// impactRun is the impact trace of one review.
type impactRun struct {
	status, scopeSource string
	set                 repoctx.ImpactSettings
	res                 gitctx.ImpactResult
	// omitted counts the links the prompts left out (their trees did not
	// fit), cut whether a token budget cut a block, placed the trees placed.
	omitted, placed int
	cut             bool
	// nodes are the files of the trace's links (hop > 0), for the caller
	// findings.
	nodes map[string]bool
}

// wantImpact reports whether a run asks for the trace: the operator turned
// it on (a repository file may only have turned it off), or the call gave
// impact_paths.
func wantImpact(cfg *config.Config, args Args) bool {
	return cfg.Context.Impact.Enabled || args.ImpactPaths != nil
}

// impactScopeSource says where the run's scope came from.
func impactScopeSource(args Args, rc *repocfg.Outcome, operator *config.Config) string {
	if args.ImpactPaths != nil {
		return llmrun.ImpactScopeTool
	}
	if rc != nil && slices.ContainsFunc(rc.Result.Applied, func(k string) bool {
		return k == "context.impact.paths" || k == "context.impact.exclude_paths" || k == "context.impact.entry_points"
	}) {
		return llmrun.ImpactScopeRepoConfig
	}
	if o := operator.Context.Impact; len(o.Paths)+len(o.ExcludePaths)+len(o.EntryPoints) > 0 {
		return llmrun.ImpactScopeOperator
	}
	return llmrun.ImpactScopeNone
}

// planImpact decides on the trace of a run and runs it once on files (the
// pull request's files the review covers). It returns nil when the trace
// is not wanted. A run with supplied findings traces nothing: its status
// says so (skipped_client_findings), never off (review DQ-1 of WP-20b).
func (pl *Plan) planImpact(ctx context.Context, cfg *config.Config, args Args, operator *config.Config,
	rcs *repoctx.Session, files []provider.FilePatch, supplied bool) *impactRun {
	if !wantImpact(cfg, args) {
		return nil
	}
	ir := &impactRun{scopeSource: impactScopeSource(args, pl.repo, operator),
		set: repoctx.ImpactSettingsFrom(cfg.Context.Impact, args.ImpactPaths)}
	if supplied {
		ir.status = llmrun.ImpactSkippedClientFindings
		return ir
	}
	if rcs == nil {
		ir.status = llmrun.ImpactRepoContextOff
		return ir
	}
	t := rcs.Trace(ctx, files, ir.set)
	ir.status, ir.res = t.Status, t.Result
	ir.nodes = map[string]bool{}
	for _, n := range t.Result.Nodes {
		if n.Hop > 0 && n.Path != "" {
			ir.nodes[n.Path] = true
		}
	}
	pl.log.Debug("review: impact trace", "status", t.Status, "seeds", t.Result.Seeds, "links", t.Result.Links,
		"depth_reached", t.Result.DepthReached, "not_followed", t.Result.NotFollowed, "truncated", t.Result.Truncated)
	return ir
}

// placeImpact puts the impact block of the seeds keep accepts into one
// prompt (in, fitted as fit0) when the diff leaves room for it: at most
// min(context.impact.max_tokens, the room below the soft reserve), whole
// trees, and only when the prompt then still holds the whole diff. It
// returns the prompt input and the fitted prompt to use.
func (pl *Plan) placeImpact(ir *impactRun, in PromptInput, diff string, fit0 *fitted, b tokens.Budget,
	keep func(gitctx.ImpactNode) bool) (PromptInput, *fitted, error) {
	if ir == nil || ir.status != llmrun.ImpactOn {
		return in, fit0, nil
	}
	room := b.ContextWindow - b.SoftReserve() - fit0.requestTokens
	if fit0.keptLines >= 0 {
		room = 0
	}
	blk := repoctx.RenderImpact(ir.res, keep, min(ir.set.MaxTokens, room), b.Factor)
	ir.omitted += blk.NodesOmitted
	if blk.TreesOmitted > 0 {
		ir.cut = true
	}
	if blk.Text == "" {
		return in, fit0, nil
	}
	cand := in
	cand.Impact = blk.Text
	f, err := fitPrompts(cand, diff, b)
	if err != nil {
		return in, fit0, err
	}
	if f.keptLines >= 0 {
		// The block would make the guard trim the diff: the diff wins, and
		// the links of the trees it would have shown are left out too.
		ir.omitted += countLinks(ir.res, keep) - blk.NodesOmitted
		ir.cut = true
		return in, fit0, nil
	}
	ir.placed += blk.Trees
	pl.seen.Add(blk.Paths...)
	return cand, f, nil
}

// countLinks counts the links of the trees whose seed keep accepts.
func countLinks(res gitctx.ImpactResult, keep func(gitctx.ImpactNode) bool) int {
	root := make([]int, len(res.Nodes))
	n := 0
	for i, nd := range res.Nodes {
		if nd.Parent < 0 {
			root[i] = i
			continue
		}
		root[i] = root[nd.Parent]
		if keep == nil || keep(res.Nodes[root[i]]) {
			n++
		}
	}
	return n
}

// seedsIn keeps the seeds defined in one of paths: the chains of a part of
// a review in parts (§2.5).
func seedsIn(paths []string) func(gitctx.ImpactNode) bool {
	return func(n gitctx.ImpactNode) bool { return slices.Contains(paths, n.Path) }
}

// impactCoverage is coverage.impact of the run.
func (pl *Plan) impactCoverage() *llmrun.Impact {
	maxTokens := 0
	if pl.cfg != nil {
		maxTokens = pl.cfg.Context.Impact.MaxTokens
	}
	ir := pl.impact
	if ir == nil {
		return &llmrun.Impact{Status: llmrun.ImpactOff, ScopeSource: llmrun.ImpactScopeNone, MaxTokens: maxTokens}
	}
	c := &llmrun.Impact{Status: ir.status, ScopeSource: ir.scopeSource, MaxTokens: ir.set.MaxTokens}
	if ir.status == llmrun.ImpactOn {
		r := ir.res
		c.Seeds, c.Links, c.DepthReached, c.EntryPointsReached, c.Truncated = r.Seeds, r.Links, r.DepthReached, r.EntryPointsReached, r.Truncated
		c.NotFollowed = repoctx.ImpactNotFollowed(r, ir.omitted)
	}
	return c
}

// impactNotes are the notes of the run's trace, and of a run with focus
// impact whose trace did not run (§2.6: it proceeds with repository
// context only).
func (pl *Plan) impactNotes() []string {
	ir := pl.impact
	var notes []string
	if ir != nil && ir.status == llmrun.ImpactRepoContextOff && pl.args.ImpactPaths != nil {
		notes = append(notes, NoteImpactRepoContextOff)
	}
	if ir != nil && ir.status == llmrun.ImpactOn && (ir.res.Truncated || ir.res.NotFollowedBy[gitctx.NotFollowedBudget] > 0 || ir.cut) {
		notes = append(notes, NoteImpactTruncated)
	}
	if pl.args.Focus == FocusImpact && (ir == nil || ir.status == llmrun.ImpactOff || ir.status == llmrun.ImpactRepoContextOff ||
		ir.status == llmrun.ImpactFailed) {
		notes = append(notes, NoteImpactFocusWithoutTrace)
	}
	return notes
}

// stampCoverage adds what every pr_review coverage carries: coverage.impact
// and the effective context.repo.max_tokens (§6 item 5).
func (pl *Plan) stampCoverage(c *Coverage) {
	c.Impact = pl.impactCoverage()
	if pl.cfg != nil {
		c.RepoContext.MaxTokens = pl.cfg.Context.Repo.MaxTokens
	}
}

// isCaller reports whether a finding is located in a caller the trace
// found: a file that is not in the pull request and is a node of the trace.
func (pl *Plan) isCaller(ki *KeyIssue, files map[string]*provider.FilePatch) bool {
	return pl.impact != nil && files[ki.RelevantFile] == nil && pl.impact.nodes[ki.RelevantFile]
}
