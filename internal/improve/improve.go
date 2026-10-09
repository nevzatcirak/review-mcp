// Package improve implements the pr_improve pipeline (v2 spec §1, X-27,
// design Y-8 and Y-9): the adapted code-suggestion and self-review prompts
// and Run, which asks the model for code suggestions and has a second call
// score them.
//
// It runs the same fetch, filter, budget and prepare steps as pr_review,
// with the numbered diff, the existing-discussion block (X-13) and, when
// context.repo.enabled is set, the repository context (X-22). A diff that
// leaves files out is improved in parts (X-19). Every call with a diff (the
// one call, or each part) is followed by one self-review call on the same
// diff and that call's suggestions; suggestions scoring below
// improve.min_score are dropped with a note, and a failed self-review keeps
// the suggestions unscored. The suggestions of every part are ranked
// together (score descending, the unscored last), deduplicated by their
// X-13 fingerprint and capped at improve.max_suggestions.
//
// The kept suggestions are verified against the head file (v2 spec §2,
// Y-10): the quoted code must be at the self-review's lines, or uniquely
// elsewhere in the file (the range is then corrected). Nothing is published
// yet (WP-2h): every suggestion has no anchor.
//
// Dependency rule (deps_test.go): nothing in this package's dependency
// closure may import internal/review, internal/ask, internal/describe or
// the tool and transport layers. What it shares with pr_review lives in
// internal/llmrun (coverage, parts, the discussion block, the fingerprint)
// and internal/repoctx; it parses YAML with internal/yamlrepair, as
// pr_review does.
package improve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/prompt"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Resolver maps a PR URL to a reference and a provider (X-2). It has the
// shape of tools.PRResolver and is implemented by *provider.Resolver.
type Resolver interface {
	Resolve(rawURL string) (provider.PRRef, provider.Provider, error)
}

// Completer is one chat completion. It is implemented by *llm.Client, which
// owns the transport retries (DQ-9 step 1).
type Completer interface {
	Complete(ctx context.Context, system, user string) (*llm.Response, error)
}

// Deps are the injectable dependencies of Run.
type Deps struct {
	// Config is the effective configuration and ConfigErr its load error;
	// a nil Config or a non-nil ConfigErr is the degraded mode.
	Config    *config.Config
	ConfigErr error
	// Logger receives debug lines with names and numbers only (X-8). Nil
	// discards.
	Logger   *slog.Logger
	Resolver Resolver
	LLM      Completer
	// Clock supplies the prompt date; nil is the wall clock.
	Clock prompt.Clock
	// Progress, when set, is called with a Stage* word (or CallingModelPart,
	// ScoringPart) as the pipeline advances. Nil is ignored. It must not
	// block.
	Progress func(stage string)
	// RepoContext is the git backend of repository context (X-22); nil
	// selects the real one. It is used only when context.repo.enabled is
	// set.
	RepoContext repoctx.Backend
}

// Args are the per-call arguments. Zero values fall back to the
// configuration.
type Args struct {
	PRURL string
	// OutputLanguage replaces output.language when non-empty. It applies to
	// every call of the run, the self-review calls included.
	OutputLanguage string
	// MaxChunks is the most suggestion calls (parts) of the run (X-19); 0
	// or less takes review.max_chunks, and 1 makes one call.
	MaxChunks int
}

// errNoWiring reports a caller bug: Run needs a resolver and an LLM.
var errNoWiring = errors.New("improve: resolver or LLM dependency missing")

// Progress stages reported through Deps.Progress. They are fixed words,
// never PR content. The first five are pr_review's.
const (
	StageFetching      = "fetching"
	StageRepoContext   = "fetching repository context"
	StagePreparingDiff = "preparing diff"
	StageCallingModel  = "calling model"
	StageRendering     = "rendering"
	// StageScoring is the self-review call of a run in one call; a run in
	// parts reports ScoringPart after each part's CallingModelPart. It is
	// reported only when there is a suggestion to score.
	StageScoring = "scoring suggestions"
)

// stageCallingModelPartFormat is the progress stage of part I of N; it is
// pr_review's text, so a progress reporter sizes its total the same way.
const stageCallingModelPartFormat = StageCallingModel + " (part %d of %d)"

// CallingModelPart is the progress stage of the suggestion call of part i
// of a run in n > 1 parts: "calling model (part I of N)".
func CallingModelPart(i, n int) string {
	return fmt.Sprintf(stageCallingModelPartFormat, i, n)
}

// ScoringPart is the progress stage of the self-review call of part i of a
// run in n > 1 parts: "scoring suggestions (part I of N)".
func ScoringPart(i, n int) string {
	return fmt.Sprintf(StageScoring+" (part %d of %d)", i, n)
}

// IsScoringStage reports whether stage is a self-review stage
// (StageScoring or ScoringPart), so a progress reporter can count it.
func IsScoringStage(stage string) bool {
	return stage == StageScoring || strings.HasPrefix(stage, StageScoring+" (part ")
}

// Error classes of the improve pipeline.
const (
	// ClassUnparseable: the suggestion answer had no code_suggestions list,
	// also after the one re-ask.
	ClassUnparseable llmrun.ErrorClass = "improve_unparseable"
)

// ErrUnparseable is the classified error of an unusable answer. Its text is
// a fixed sentence.
var ErrUnparseable = &llmrun.Error{Class: ClassUnparseable, Eligible: true,
	Message: "the model's answer could not be parsed as code suggestions, also after one retry; " +
		"try again, or check that llm.model follows the YAML output instructions"}

// Plan is the outcome of the steps up to the model calls. Run continues
// from it; diag improve --dry-run prints it.
type Plan struct {
	// Result is the run so far: coverage, notes and the metadata known
	// before the model calls.
	Result *Result
	// Budget is the token budget the diff was prepared against.
	Budget tokens.Budget
	// Prompts are the final suggestion prompts of the first call (of part 1
	// for a run in parts). They are zero when Empty is set. Callers must
	// never log them (X-8).
	Prompts Prompts
	// Empty reports that nothing is left to review after filtering, so no
	// model call is made.
	Empty bool
	// RepoReport is the repository-context detail for diag improve
	// --dry-run; nil when repository context is off.
	RepoReport *repoctx.Report

	// parts are the suggestion calls: one for a run in one call, N for a
	// run in parts (X-19).
	parts []*part
	// chunks is the packing of a run in parts; nil for one call.
	chunks *diffpipe.Chunks
	flt    *filter.Filter
	// repoCtx is the repository context of a run in parts, summed over the
	// parts; zero for a run in one call.
	repoCtx llmrun.RepoContext
	ref     provider.PRRef
	p       provider.Provider
	// files are the diff's files by path: the head content the suggestions
	// are verified against (v2 spec §2).
	files map[string]*provider.FilePatch
	// language is the effective output language, for the self-review.
	language string
	minScore int
	maxTotal int
	log      *slog.Logger
}

// part is one suggestion call and its self-review.
type part struct {
	prep *diffpipe.Prepared
	fit  *fitted
	// cov holds the part's own files after the request-size guard (a run in
	// parts only), as in pr_review.
	cov Coverage
}

func progress(deps Deps, stage string) {
	if deps.Progress != nil {
		deps.Progress(stage)
	}
}

// Run improves one pull request. It returns a classified error
// (*llmrun.Error, *provider.Error, *llm.Error) or the result.
func Run(ctx context.Context, deps Deps, args Args) (*Result, error) {
	if deps.Config == nil || deps.ConfigErr != nil {
		return nil, llmrun.ErrConfigInvalid
	}
	if deps.Resolver == nil || deps.LLM == nil {
		return nil, errNoWiring
	}
	pl, err := Prepare(ctx, deps, args)
	if err != nil {
		return nil, err
	}
	if pl.Empty {
		pl.Result.Notes = append(pl.Result.Notes, NoteNoReviewableChanges)
		return pl.Result, nil
	}
	return pl.finish(ctx, deps)
}

// Prepare runs the steps up to the model calls: configuration, resolution,
// the pull request and its filtered diff, repository context, the
// discussion, token measurement, diff preparation (in parts when needed) and
// the request-size guard. It sends nothing to the model and needs no
// Deps.LLM (besides the context-window probe when llm.context_window is
// unset).
func Prepare(ctx context.Context, deps Deps, args Args) (*Plan, error) {
	if deps.Config == nil || deps.ConfigErr != nil {
		return nil, llmrun.ErrConfigInvalid
	}
	if deps.Resolver == nil {
		return nil, errNoWiring
	}
	cfg := deps.Config
	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	factor := cfg.LLM.TokenEstimateFactor
	language := cfg.Output.Language
	if args.OutputLanguage != "" {
		language = args.OutputLanguage
	}
	maxChunks := args.MaxChunks
	if maxChunks <= 0 {
		maxChunks = cfg.Review.MaxChunks
	}

	// Resolve, fetch the PR and its filtered diff.
	progress(deps, StageFetching)
	flt, err := filter.New(cfg)
	if err != nil {
		return nil, err
	}
	ref, p, err := deps.Resolver.Resolve(args.PRURL)
	if err != nil {
		return nil, err
	}
	// The context window comes from llm.context_window or, when unset, from
	// the endpoint (X-15), before any provider request.
	window, _, err := llmrun.ContextWindow(ctx, cfg, deps.LLM)
	if err != nil {
		return nil, err
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return nil, err
	}
	d, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: flt.Include})
	if err != nil {
		return nil, err
	}

	// Repository context (X-22), exactly as pr_review opens it: the head is
	// fetched now when the diff defines a symbol to look for, and with
	// review.max_chunks > 1 the block's budget is reserved in every part.
	var rcs *repoctx.Session
	reserve := 0
	if cfg.Context.Repo.Enabled {
		rcs = repoctx.Open(cfg, deps.RepoContext, ref, p, pr.HeadSHA, d.Files, d.Skipped, flt, log)
		if len(rcs.Symbols(d.Files)) > 0 {
			progress(deps, StageRepoContext)
			if rcs.Ready(ctx) == "" && maxChunks > 1 {
				reserve = rcs.Settings().MaxTokens
			}
		}
	}
	progress(deps, StagePreparingDiff)

	pl := &Plan{ref: ref, p: p, flt: flt, language: language, minScore: cfg.Improve.MinScore,
		maxTotal: cfg.Improve.MaxSuggestions, files: fileIndex(d.Files), log: log}
	// The PR's discussion (X-13), budgeted by review.max_discussion_tokens;
	// a failure never fails the run.
	disc, discNotes := pl.readDiscussion(ctx, cfg.Review.MaxDiscussionTokens, factor)

	in := PromptInput{
		Language:       language,
		Title:          pr.Title,
		Date:           prompt.Date(deps.Clock),
		Branch:         pr.SourceBranch,
		TargetBranch:   pr.TargetBranch,
		Description:    tokens.ClipDescription(pr.Description, cfg.Diff.MaxDescriptionTokens, factor),
		Discussion:     disc.Block,
		MaxSuggestions: cfg.Improve.MaxSuggestionsPerPart,
	}
	budget := tokens.Budget{
		ContextWindow:   window,
		MaxOutputTokens: cfg.LLM.MaxOutputTokens,
		Factor:          factor,
		MaxDiffTokens:   tokens.Cap(cfg.Diff.MaxTokens),
	}
	promptTokens, err := diffPromptTokens(in, budget, 0)
	if err != nil {
		return nil, err
	}
	budget.PromptTokens = promptTokens
	if budget.RequireCapacity() != nil && in.Discussion != "" {
		// The discussion is optional: a context window too small for it
		// still reviews the diff, without it (as pr_review).
		disc = llmrun.Discussion{Omitted: disc.Included + disc.Omitted}
		in.Discussion = ""
		if promptTokens, err = diffPromptTokens(in, budget, 0); err != nil {
			return nil, err
		}
		budget.PromptTokens = promptTokens
	}
	if err := budget.RequireCapacity(); err != nil {
		return nil, llmrun.DoesNotFit(err)
	}

	// The numbered diff (ModeNumbered, pr_review's): the self-review reads
	// the new-file line numbers off it. It is prepared against the budget
	// without the repository-context reservation (base) or with it, and the
	// reservation is dropped when it leaves no capacity or no diff, as in
	// pr_review.
	base := budget
	withReserve := func(r int) tokens.Budget {
		b := base
		b.PromptTokens += r
		return b
	}
	if reserve > 0 && withReserve(reserve).RequireCapacity() != nil {
		reserve = 0
	}
	dIn := diffpipe.Input{Files: d.Files, Skipped: d.Skipped, Mode: diffpipe.ModeNumbered, Budget: base, Diff: cfg.Diff}
	prepare := func(r int) (*diffpipe.Prepared, error) {
		in := dIn
		in.Budget = withReserve(r)
		return diffpipe.Prepare(in)
	}
	prep, err := prepare(reserve)
	if reserve > 0 && (errors.Is(err, tokens.ErrDoesNotFit) || (err == nil && prep.Text == "")) {
		reserve = 0
		prep, err = prepare(0)
	}
	if err != nil {
		if errors.Is(err, tokens.ErrDoesNotFit) {
			return nil, llmrun.DoesNotFit(err)
		}
		return nil, err
	}
	budget = withReserve(reserve)
	res := &Result{
		Suggestions: []Suggestion{},
		Coverage:    llmrun.BuildCoverage(prep, flt),
		Notes:       []string{},
		Metadata: Metadata{
			Model: cfg.LLM.Model, ContextWindow: window, PromptTokens: promptTokens,
			DiffTokens: prep.Tokens, FastPath: prep.FastPath, AlreadyDiscussed: disc.Included,
		},
	}
	res.Notes = append(res.Notes, discNotes...)
	if disc.Omitted > 0 {
		res.Notes = append(res.Notes, noteDiscussionLeftOut(disc.Omitted))
	}
	log.Debug("improve: diff prepared", "url", logging.RedactURL(ref.URL), "files", len(d.Files),
		"provider_skipped", len(d.Skipped), "included", len(prep.Included), "clipped", len(prep.Clipped),
		"fast_path", prep.FastPath, "prompt_tokens", promptTokens, "diff_tokens", prep.Tokens,
		"discussion_threads", disc.Included, "discussion_omitted", disc.Omitted)

	pl.Result, pl.Budget = res, budget
	if prep.Text == "" {
		pl.Empty = true
		if rcs != nil {
			// No model call, so no context: skipped, not off.
			res.Coverage.RepoContext = llmrun.RepoContext{Status: llmrun.RepoSkipped, Reason: repoctx.ReasonNothingToReview}
		}
		res.Notes = append(res.Notes, llmrun.PartialNotes(&res.Coverage, budget)...)
		return pl, nil
	}

	// A diff that leaves files out is improved in parts when
	// review.max_chunks allows it (X-19), exactly as pr_review packs them;
	// otherwise, and when the packing yields one part, the run is the one
	// call below. When the reservation made the diff leave files out and no
	// plan in parts results, the diff is prepared again without it: the
	// reservation must never cost a file.
	types := changeTypes(d.Files)
	if maxChunks > 1 && llmrun.LeavesFilesOut(prep) {
		ok, err := pl.planParts(ctx, dIn, in, maxChunks, rcs, reserve, types)
		if err != nil {
			return nil, err
		}
		if ok {
			return pl, nil
		}
		if reserve > 0 {
			if prep, err = prepare(0); err != nil {
				if errors.Is(err, tokens.ErrDoesNotFit) {
					return nil, llmrun.DoesNotFit(err)
				}
				return nil, err
			}
			budget = base
			pl.Budget = budget
			res.Coverage, res.Metadata.DiffTokens, res.Metadata.FastPath = llmrun.BuildCoverage(prep, flt), prep.Tokens, prep.FastPath
			if llmrun.LeavesFilesOut(prep) {
				if ok, err = pl.planParts(ctx, dIn, in, maxChunks, rcs, 0, types); err != nil {
					return nil, err
				} else if ok {
					return pl, nil
				}
			}
		}
	}

	// One call: the final prompts behind the request-size guard.
	fit, err := fitPrompts(in, prep.Text, budget)
	if err != nil {
		return nil, err
	}
	var repoNotes []string
	if rcs != nil {
		// The block goes into the room the diff leaves (placeRepo); the
		// prompt tokens then include it.
		pin, pfit, o := placeRepo(ctx, rcs, in, diffFiles(d.Files, prep.Included, prep.Clipped), nil, prep.Text, fit, budget)
		fit = pfit
		if pin.RepoContext != "" {
			if res.Metadata.PromptTokens, err = diffPromptTokens(pin, budget, 0); err != nil {
				return nil, err
			}
		}
		res.Coverage.RepoContext, repoNotes = repoctx.Summarize([]repoctx.Outcome{o})
		pl.RepoReport = repoctx.ReportOf([]repoctx.Outcome{o})
		log.Debug("improve: repository context", "status", res.Coverage.RepoContext.Status,
			"reason", res.Coverage.RepoContext.Reason, "symbols", o.Symbols, "references", o.References,
			"files", o.Files, "omitted", o.Omitted)
	}
	pl.parts = []*part{{prep: prep, fit: fit}}
	pl.Prompts = fit.prompts
	res.Metadata.RequestTokens = fit.requestTokens
	res.Coverage.ModelCalls = 1
	if fit.keptLines >= 0 {
		llmrun.TrimCoverage(&res.Coverage, prep.Text, fit.keptLines, types)
		res.Metadata.DiffTrimmed = true
		res.Metadata.DiffTokens = tokens.Estimate(fit.diff, factor)
		res.Notes = append(res.Notes, NoteDiffTrimmed)
		log.Debug("improve: diff trimmed by the request-size guard", "kept_lines", fit.keptLines,
			"request_tokens", fit.requestTokens)
	}
	if n := len(res.Coverage.Clipped); n > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(llmrun.NoteClippedFormat,
			llmrun.CountPhrase(n, "file was", "files were")))
	}
	res.Notes = append(res.Notes, llmrun.PartialNotes(&res.Coverage, budget)...)
	res.Notes = append(res.Notes, repoNotes...)
	return pl, nil
}

// fileIndex maps the files by their (new) path.
func fileIndex(files []provider.FilePatch) map[string]*provider.FilePatch {
	m := make(map[string]*provider.FilePatch, len(files))
	for i := range files {
		m[files[i].Path] = &files[i]
	}
	return m
}

func changeTypes(files []provider.FilePatch) map[string]provider.ChangeType {
	types := map[string]provider.ChangeType{}
	for _, f := range files {
		types[f.Path] = f.Type
	}
	return types
}
