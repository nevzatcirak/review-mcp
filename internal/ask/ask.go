// Package ask implements the pr_ask pipeline (spec P5 §1): the adapted
// question prompts and Run, which answers one question about a pull
// request. It runs the same fetch, filter, budget and prepare steps as
// pr_review, but with the plain diff view and a free-text answer: no
// schema, no YAML, no repair chain (X-5).
//
// Hard rule (spec P5 §3): nothing in this package's dependency closure may
// import YAML or repair code, which includes internal/review. What the two
// pipelines share lives in internal/llmrun. deps_test.go enforces the rule.
package ask

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/projctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/repotools"
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

// ProviderRenderer renders a result as a published PR comment for a
// provider's capabilities (DQ-16 provider profile; WP-PR-5b).
type ProviderRenderer func(res *Result, caps provider.Capabilities) string

// Deps are the injectable dependencies of Run.
type Deps struct {
	// Config is the effective configuration and ConfigErr its load error;
	// a nil Config or a non-nil ConfigErr is the degraded mode (step 1).
	Config    *config.Config
	ConfigErr error
	// Logger receives debug lines with names and numbers only (X-8). Nil
	// discards.
	Logger   *slog.Logger
	Resolver Resolver
	LLM      Completer
	// RenderProvider renders the comment published with Args.Publish.
	RenderProvider ProviderRenderer
	// Progress, when set, is called with a Stage* word as the pipeline
	// advances. Nil is ignored. It must not block.
	Progress func(stage string)
	// RepoContext is the git backend of repository context (X-22); nil
	// selects the real one (gitctx.New from the configuration). It is used
	// only when context.repo.enabled is set. Tests substitute a fake.
	RepoContext repoctx.Backend
}

// Args are the per-call arguments (DQ-25, X-1). Zero values fall back to
// the configuration.
//
// DESIGN-QUESTION: can a call clear ask.extra_instructions with an empty
// extra_instructions argument? — chose no (empty means "not given"), as
// pr_review does, because the MCP arguments are optional strings and the
// spec defines no separate "unset" form; validating output_language is the
// tool layer's job (WP-PR-5b).
type Args struct {
	PRURL string
	// Question is the question to answer; ValidateQuestion applies.
	Question string
	// ExtraInstructions replaces ask.extra_instructions when non-empty.
	ExtraInstructions string
	// OutputLanguage replaces output.language when non-empty.
	OutputLanguage string
	// Publish also posts the question and answer as a PR comment.
	Publish bool
	// Related are the normalised URLs of the related argument (X-41,
	// related.Validate): read-only context on any configured provider; nil
	// means not given.
	Related []string
}

// errNoWiring reports a caller bug: Run needs a resolver and an LLM.
var errNoWiring = errors.New("ask: resolver or LLM dependency missing")

// Progress stages reported through Deps.Progress. They are fixed words,
// never PR content. The names match pr_review's.
const (
	StageFetching = "fetching"
	// StageRepoContext is reported, only when context.repo.enabled is set and
	// the diff defines symbols to look for, while the pull request head is
	// fetched (X-22). It precedes StagePreparingDiff.
	StageRepoContext   = "fetching repository context"
	StagePreparingDiff = "preparing diff"
	StageCallingModel  = "calling model"
)

// publishFailedMessage is shown for a publish error that is not a
// classified provider error.
const publishFailedMessage = "the answer could not be posted as a PR comment"

// Plan is the outcome of steps 1 to 6 of the pipeline: everything up to the
// model call. Run continues from it; diag ask --dry-run prints it.
type Plan struct {
	// Result is the answer so far: question, coverage, notes and the
	// metadata known before the model call (prompt, diff and request
	// tokens).
	Result *Result
	// Budget is the token budget the diff was prepared against.
	Budget tokens.Budget
	// Prompts are the final prompts of the model call. They are zero when
	// Empty is set. Callers must never log them (X-8).
	Prompts Prompts
	// Empty reports that nothing is left to ask about after filtering, so
	// no model call is made (step 5).
	Empty bool

	// RepoReport is the repository-context detail for diag ask --dry-run;
	// nil when repository context is off.
	RepoReport *repoctx.Report

	// tools is the tool loop of the run (X-43), nil when the run does not
	// use tools; toolUse is what the run did with them.
	tools   *toolPlan
	toolUse toolUse
	// placed are the sections of the placed related block, for the links
	// of the consumer citations in the answer (linkConsumers).
	placed []related.Section
	cfg    *config.Config
	// seen are the files the prompt showed the model so far (V-13b §6 item
	// 1): the diff as read, the files of the placed repository-context
	// hits, and the head commit's files the repository tools showed it.
	// unseen adds the related block's.
	seen llmrun.Seen

	ref provider.PRRef
	p   provider.Provider
	log *slog.Logger
}

func progress(deps Deps, stage string) {
	if deps.Progress != nil {
		deps.Progress(stage)
	}
}

// Run answers one question about a pull request. It returns a classified
// error (*llmrun.Error, *QuestionError, *provider.Error, *llm.Error) or the
// result; a failed publish is reported in the result, never as an error.
func Run(ctx context.Context, deps Deps, args Args) (*Result, error) {
	res, err := run(ctx, deps, args)
	if res != nil {
		// The rate-limit waits of the run's client (X-37, v2.1 spec V-6
		// §2.3): in the result, after the run, so a published comment
		// does not carry them.
		res.Metadata.RateLimitWaits, res.Metadata.RateLimitWaitMS = llmrun.RateLimitWaits(deps.LLM)
		if note := llmrun.RateLimitNote(res.Metadata.RateLimitWaitMS); note != "" {
			res.Notes = append(res.Notes, note)
		}
		// The models that served the run (X-38, v2.1 spec V-6 §3.4).
		fb := llmrun.Fallback(deps.LLM, res.Metadata.Model, res.Metadata.LLMCalls)
		if fb.Model != "" {
			res.Metadata.Model = fb.Model
		}
		res.Metadata.ModelsUsed, res.Metadata.FallbackCalls = fb.ModelsUsed, fb.FallbackCalls
		res.Notes = append(res.Notes, fb.Notes...)
		if res.Coverage.Related == nil {
			// coverage.related is always present in pr_ask.
			res.Coverage.Related = []llmrun.RelatedItem{}
		}
	}
	return res, err
}

// run is Run without the rate-limit report.
func run(ctx context.Context, deps Deps, args Args) (*Result, error) {
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
	res, log := pl.Result, pl.log
	if pl.Empty {
		// Nothing to ask about: no LLM call (step 5).
		// DESIGN-QUESTION: is this answerless result published when publish
		// is set? — chose yes, as pr_review does for an empty review,
		// because step 9 applies to every returned result and the comment
		// then shows the question, the coverage and the note.
		res.Notes = append(res.Notes, NoteNoReviewableChanges)
		reportTools(res, pl.toolUse)
		pl.publish(ctx, deps, args)
		return res, nil
	}
	progress(deps, StageCallingModel)

	// Step 7: one call, or the rounds of the tool loop (X-43); the client
	// owns the transport retries. There is no re-ask, because there is
	// nothing to parse.
	resp, calls, err := pl.answer(ctx, deps)
	res.Metadata.LLMCalls += calls
	if err != nil {
		return nil, err
	}
	answer := strings.TrimSpace(resp.Content)
	if answer == "" {
		// The client already rejects empty content; this guards other
		// Completer implementations.
		return nil, &llm.Error{Class: llm.ClassProtocol, Detail: "response content is empty"}
	}
	res.Metadata.Truncated = resp.Truncated
	if resp.Truncated {
		res.Notes = append(res.Notes, NoteTruncated)
	}
	log.Debug("ask: answer received", "truncated", resp.Truncated,
		"prompt_tokens_reported", resp.Usage.PromptTokens, "completion_tokens_reported", resp.Usage.CompletionTokens)

	// Step 8: the result, with what the run did with the repository tools
	// (X-43), before the publish, whose comment carries the notes. The paths
	// the run never saw are found in the model's own text, before the
	// consumer citations become links.
	unseen := pl.unseen(answer)
	res.Answer = llmrun.WithUnseen(linkConsumers(answer, pl.placed), unseen)
	res.AnswerUnverifiedPaths = unseen
	reportTools(res, pl.toolUse)

	// Step 9: publish.
	pl.publish(ctx, deps, args)
	return res, nil
}

// Prepare runs steps 1 to 6 of the pipeline: configuration, question
// validation, resolution, diff fetch, description, token measurement, diff
// preparation and the request-size guard. It sends nothing to the model and
// needs no Deps.LLM. A rejected question or an invalid configuration is
// reported before any request is made.
func Prepare(ctx context.Context, deps Deps, args Args) (*Plan, error) {
	// Step 1: config.
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

	// Step 2: validate the question, before any network call.
	question, err := ValidateQuestion(args.Question)
	if err != nil {
		return nil, err
	}

	// Step 3: resolve and read the PR.
	progress(deps, StageFetching)
	ref, p, err := deps.Resolver.Resolve(args.PRURL)
	if err != nil {
		return nil, err
	}
	// The context window comes from llm.context_window or, when unset, from
	// the endpoint (X-15). The probe runs before any provider request, so a
	// failing probe sends nothing to the provider.
	window, _, err := llmrun.ContextWindow(ctx, cfg, deps.LLM)
	if err != nil {
		return nil, err
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return nil, err
	}

	// The repository config (X-40): read once at the base revision, then the
	// run continues on the merged copy of the configuration.
	rc := repocfg.ForRun(ctx, log, cfg, p, ref, pr)
	if len(args.Related) > 0 {
		// The call's own related wins over the declarations: the operator's
		// switch changes nothing, so it gets no note (V-13b §6 item 3).
		rc.CallRelated()
	}
	// operator keeps the operator's configuration: a related repository is
	// searched with its filter.
	operator := cfg
	cfg = rc.Config
	factor := cfg.LLM.TokenEstimateFactor
	extra := cfg.Ask.ExtraInstructions
	if args.ExtraInstructions != "" {
		extra = args.ExtraInstructions
	}
	language := cfg.Output.Language
	if args.OutputLanguage != "" {
		language = args.OutputLanguage
	}
	flt, err := filter.New(cfg)
	if err != nil {
		return nil, err
	}
	d, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: flt.Include})
	if err != nil {
		return nil, err
	}
	// The related pull requests (X-41, spec V-13a §3.1), read as pr_review
	// reads them: through the run's resolver, with the run's filter; a
	// failure is the item's status, never the run's error. Related
	// repositories (v2.5 spec V-13b) are resolved here and, as in pr_review,
	// fetched and searched only when the related block is placed.
	// Without a related argument, the related repositories the repository
	// config declares are read instead (V-13b §3.2; rc.Related holds them
	// only with related.from_repo_config). A call's own related always wins.
	var rel []*related.Item
	primaryURL, _ := related.Normalize(args.PRURL)
	switch {
	case len(args.Related) > 0:
		rel = related.Read(ctx, deps.Resolver, primaryURL, ref, args.Related, flt.Include, cfg.Related.MaxTokens == 0,
			related.RepositoriesFor(cfg, operator, deps.RepoContext), log)
	case len(rc.Related) > 0:
		rel = related.ReadDeclared(ctx, deps.Resolver, primaryURL, ref, rc.Related, cfg.Related.MaxTokens == 0,
			related.RepositoriesFor(cfg, operator, deps.RepoContext), log)
	}
	// Repository context (X-22): the head is fetched now, under its own
	// progress stage, when the diff defines a symbol to look for. pr_ask
	// makes one call and never reserves room for the block: the diff always
	// wins (RC-8).
	var rcs *repoctx.Session
	if cfg.Context.Repo.Enabled {
		rcs = repoctx.Open(cfg, deps.RepoContext, ref, p, pr.HeadSHA, d.Files, d.Skipped, flt, log)
		if len(rcs.Symbols(d.Files)) > 0 {
			progress(deps, StageRepoContext)
			rcs.Ready(ctx)
		}
	}
	progress(deps, StagePreparingDiff)
	in := PromptInput{
		ExtraInstructions: extra,
		RepoInstructions:  projctx.RepoGuidance(cfg.RepoConfig.Path, cfg.Ask.RepoInstructions),
		Language:          language,
		MainLanguage:      diffpipe.MainLanguage(d.Files),
		Title:             pr.Title,
		Branch:            pr.SourceBranch,
		Description:       tokens.ClipDescription(pr.Description, cfg.Diff.MaxDescriptionTokens, factor),
		Question:          question,
	}

	// Step 4: measure the scaffolding (the question is part of it) and
	// check the budget.
	promptTokens, err := ScaffoldingTokens(in, factor)
	if err != nil {
		return nil, err
	}
	budget := tokens.Budget{
		ContextWindow:   window,
		MaxOutputTokens: cfg.LLM.MaxOutputTokens,
		PromptTokens:    promptTokens,
		Factor:          factor,
		MaxDiffTokens:   tokens.Cap(cfg.Diff.MaxTokens),
	}
	if err := budget.RequireCapacity(); err != nil {
		return nil, llmrun.DoesNotFit(err)
	}

	// Step 5: prepare the plain diff (ModePlain, never ModeNumbered: the
	// question is about the code, not about line anchors). The files the
	// question names go first (X-21); pr_ask makes one call and never
	// reviews in parts.
	pinned := PinnedFiles(question, d.Files)
	prep, err := diffpipe.Prepare(diffpipe.Input{
		Files: d.Files, Skipped: d.Skipped, Incomplete: d.Incomplete, Mode: diffpipe.ModePlain, Budget: budget, Diff: cfg.Diff,
		Pinned: pinned,
	})
	if err != nil {
		if errors.Is(err, tokens.ErrDoesNotFit) {
			return nil, llmrun.DoesNotFit(err)
		}
		return nil, err
	}
	res := &Result{
		Question: question,
		Coverage: llmrun.BuildCoverage(prep, flt),
		Notes:    []string{},
		Metadata: Metadata{
			Model: cfg.LLM.Model, ContextWindow: window, PromptTokens: promptTokens,
			DiffTokens: prep.Tokens, FastPath: prep.FastPath,
		},
		RepoConfig: rc.Result,
	}
	// The notes of the repository config come first (X-40).
	res.Notes = append(res.Notes, rc.Notes...)
	// Changed files the provider could not list at all (GitHub lists at
	// most 3000) are named by its fixed note.
	res.Notes = append(res.Notes, d.Notes...)
	log.Debug("ask: diff prepared", "url", logging.RedactURL(ref.URL), "files", len(d.Files),
		"provider_skipped", len(d.Skipped), "included", len(prep.Included), "clipped", len(prep.Clipped),
		"fast_path", prep.FastPath, "prompt_tokens", promptTokens, "diff_tokens", prep.Tokens, "pinned", len(pinned))

	pl := &Plan{Result: res, Budget: budget, ref: ref, p: p, log: log, cfg: cfg, toolUse: repotools.Default(cfg.Context.Repo)}
	pl.seen.AddDiff(d)
	if prep.Text == "" {
		pl.Empty = true
		related.Mark(rel, related.StatusNotPlaced, related.ReasonNothingToReview)
		reportRelated(res, rel, nil)
		if rcs != nil {
			res.Coverage.RepoContext = llmrun.RepoContext{Status: llmrun.RepoSkipped, Reason: repoctx.ReasonNothingToReview}
		}
		res.Notes = append(res.Notes, llmrun.PartialNotes(&res.Coverage, budget)...)
		return pl, nil
	}

	// Step 6: render the final prompts behind the request-size guard.
	fit, err := fitPlain(in, prep.Text, budget)
	if err != nil {
		return nil, err
	}
	// The related block (X-41) goes first into the room the diff leaves,
	// before the repository context, in plain mode like the diff; it never
	// trims the diff.
	//
	// DESIGN-QUESTION (WP-11c DQ-4): the spec gives pr_ask "the same
	// reading, budget, fence and placement" as pr_review, whose related
	// diffs are numbered so that a finding can name real lines. pr_ask has
	// no findings and its own diff is plain (X-21), so its related diffs are
	// plain too. Recommendation: keep.
	//
	// DESIGN-QUESTION (WP-13b DQ-6): pr_ask's repository items are WP-13d's
	// (its instruction and cited links). Read no longer fetches, so without
	// the search a readable repository item would keep no status; pr_ask
	// searches them as pr_review does, for the surface of its whole diff, and
	// their sections go into the same block. Recommendation: keep; WP-13d
	// adds the instruction.
	related.SearchFiles(ctx, rel, d.Files, flt, cfg.Related.MaxIdentifiers, fit.KeptLines < 0, log)
	placed, err := related.Place(rel, cfg.Related.MaxTokens, diffpipe.ModePlain, cfg, flt, fit.KeptLines < 0,
		func(block string, repositories bool) (bool, error) {
			cand := in
			cand.Related, cand.RelatedRepositories = block, repositories
			f, err := fitPlain(cand, prep.Text, budget)
			if err != nil || f.KeptLines >= 0 {
				return false, err
			}
			in, fit = cand, f
			return true, nil
		})
	if err != nil {
		return nil, err
	}
	if placed != nil {
		if res.Metadata.PromptTokens, err = ScaffoldingTokens(in, factor); err != nil {
			return nil, err
		}
	}
	var repoNotes []string
	if rcs != nil {
		// The block goes into the room the diff leaves (X-22, RC-8): the
		// diff always wins. The symbols are those of the files in the
		// prepared diff, the question-named files included.
		var files []provider.FilePatch
		for i := range d.Files {
			if slices.Contains(prep.Included, d.Files[i].Path) || slices.Contains(prep.Clipped, d.Files[i].Path) {
				files = append(files, d.Files[i])
			}
		}
		var fit1 *llmrun.Fitted
		var in1 PromptInput
		o := rcs.Attach(ctx, files, nil, budget, fit.RequestTokens, fit.KeptLines >= 0, func(block string) (bool, error) {
			cand := in
			cand.RepoContext = block
			f, err := fitPlain(cand, prep.Text, budget)
			if err != nil {
				return false, err
			}
			in1, fit1 = cand, f
			return f.KeptLines >= 0, nil
		})
		if o.Block != "" {
			in, fit = in1, fit1
			if res.Metadata.PromptTokens, err = ScaffoldingTokens(in1, factor); err != nil {
				return nil, err
			}
		}
		res.Coverage.RepoContext, repoNotes = repoctx.Summarize([]repoctx.Outcome{o})
		pl.RepoReport = repoctx.ReportOf([]repoctx.Outcome{o})
		pl.seen.Add(o.Paths...)
		log.Debug("ask: repository context", "status", res.Coverage.RepoContext.Status,
			"reason", res.Coverage.RepoContext.Reason, "symbols", o.Symbols, "references", o.References,
			"files", o.Files, "omitted", o.Omitted)
	}
	if rcs != nil {
		if err := pl.planTools(ctx, deps, in, fit, prep.Text, budget, rcs, res.Coverage.RepoContext, placed); err != nil {
			return nil, err
		}
	}
	pl.Prompts = Prompts{System: fit.Rendered.System, User: fit.Rendered.User}
	res.Metadata.RequestTokens = fit.RequestTokens
	// pr_ask makes one model call (X-21); a plan without one (Empty) keeps 0.
	res.Coverage.ModelCalls = 1
	if fit.KeptLines >= 0 {
		types := map[string]provider.ChangeType{}
		for _, f := range d.Files {
			types[f.Path] = f.Type
		}
		llmrun.TrimCoverage(&res.Coverage, prep.Text, fit.KeptLines, types)
		res.Metadata.DiffTrimmed = true
		res.Metadata.DiffTokens = tokens.Estimate(fit.Diff, factor)
		res.Notes = append(res.Notes, NoteDiffTrimmed)
		log.Debug("ask: diff trimmed by the request-size guard", "kept_lines", fit.KeptLines,
			"request_tokens", fit.RequestTokens)
	}
	if n := len(res.Coverage.Clipped); n > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(llmrun.NoteClippedFormat,
			llmrun.CountPhrase(n, "file was", "files were")))
	}
	res.Notes = append(res.Notes, llmrun.PartialNotes(&res.Coverage, budget)...)
	res.Notes = append(res.Notes, repoNotes...)
	reportRelated(res, rel, placed)
	pl.placed = placed
	return pl, nil
}

// unseen returns the paths the answer names in a code span that the run
// never saw (V-13b §6 item 1, llmrun.Seen.Unseen): seen are the files of
// pl.seen and those of the placed related sections, each under its label
// (related.AddSeen, after the tools marked their reads). pr_ask sends no
// guidelines and no file tree.
func (pl *Plan) unseen(answer string) []string {
	related.AddSeen(&pl.seen, pl.placed)
	paths := pl.seen.Unseen(answer)
	pl.log.Debug("ask: unseen paths", "paths", len(paths))
	return paths
}

// reportRelated sets coverage.related ([] without the argument) and adds
// the related notes (spec V-13a §1.5).
func reportRelated(res *Result, items []*related.Item, placed []related.Section) {
	res.Coverage.Related = related.Coverage(items, placed)
	res.Notes = append(res.Notes, related.Notes(items, placed)...)
}

// fitPlain renders the final prompts of in for diff behind the request-size
// guard (llmrun.Fit).
func fitPlain(in PromptInput, diff string, b tokens.Budget) (*llmrun.Fitted, error) {
	return llmrun.Fit(diff, b, func(diff string) (llmrun.Rendered, error) {
		in := in
		in.Diff = diff
		pr, err := RenderPrompts(in)
		if err != nil {
			return llmrun.Rendered{}, err
		}
		return llmrun.Rendered{System: pr.System, User: pr.User}, nil
	})
}

// publish posts the provider-profile rendering when requested (step 9) and
// records the outcome in the result. It never fails the run and never
// discards the answer.
func (pl *Plan) publish(ctx context.Context, deps Deps, args Args) {
	if !args.Publish {
		return
	}
	res := pl.Result
	res.Publish = &PublishResult{}
	var render func(provider.Capabilities) string
	if deps.RenderProvider != nil {
		render = func(caps provider.Capabilities) string { return deps.RenderProvider(res, caps) }
	}
	llmrun.PostResult(ctx, pl.log, pl.ref, pl.p, res.Publish, publishFailedMessage, render)
}
