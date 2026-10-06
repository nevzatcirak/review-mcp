package review

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
	"github.com/nevzatcirak/review-mcp/internal/tokens"
	"github.com/nevzatcirak/review-mcp/internal/yamlrepair"
)

// Resolver maps a PR URL to a reference and a provider (X-2). It has the
// shape of tools.PRResolver and is implemented by *provider.Resolver; it is
// declared here so this package does not import internal/tools.
type Resolver interface {
	Resolve(rawURL string) (provider.PRRef, provider.Provider, error)
}

// Completer is one chat completion. It is implemented by *llm.Client, which
// owns the transport retries (DQ-9 step 1).
type Completer interface {
	Complete(ctx context.Context, system, user string) (*llm.Response, error)
}

// ProviderRenderer renders a result as a published PR comment for a
// provider's capabilities (DQ-16 provider profile; WP-PR-4d).
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
	// Clock supplies the prompt date; nil is the wall clock.
	Clock prompt.Clock
	// RenderProvider renders the comment published with Args.Publish.
	RenderProvider ProviderRenderer
	// Progress, when set, is called with a Stage* word as the pipeline
	// advances (the pr_review tool turns them into MCP progress
	// notifications). Nil is ignored. It must not block.
	Progress func(stage string)
}

// Args are the per-call arguments (DQ-25, X-1). Zero values fall back to
// the configuration.
//
// DESIGN-QUESTION: can a call clear review.extra_instructions with an
// empty extra_instructions argument? — chose no (empty means "not given")
// because the MCP arguments are optional strings and the spec defines no
// separate "unset" form; validating output_language and the 1-20 range of
// max_findings is the tool layer's job (WP-PR-4e).
type Args struct {
	PRURL string
	// ExtraInstructions replaces review.extra_instructions when non-empty.
	ExtraInstructions string
	// OutputLanguage replaces output.language when non-empty.
	OutputLanguage string
	// MaxFindings replaces review.max_findings when positive.
	MaxFindings int
	// Publish also posts the review as a PR comment.
	Publish bool
}

// errNoWiring reports a caller bug: Run needs a resolver and an LLM.
var errNoWiring = errors.New("review: resolver or LLM dependency missing")

// Progress stages reported through Deps.Progress (and by the pr_review tool
// for the last one). They are fixed words, never PR content.
const (
	StageFetching      = "fetching"
	StagePreparingDiff = "preparing diff"
	StageCallingModel  = "calling model"
	StageRendering     = "rendering"
)

// Plan is the outcome of steps 1 to 6 of the pipeline: everything up to the
// model call. Run continues from it; diag review --dry-run prints it.
type Plan struct {
	// Result is the review so far: PR, coverage, notes and the metadata
	// known before the model call (prompt, diff and request tokens).
	Result *Result
	// Budget is the token budget the diff was prepared against.
	Budget tokens.Budget
	// Prompts are the final prompts of the first model call. They are zero
	// when Empty is set. Callers must never log them (X-8).
	Prompts Prompts
	// Empty reports that nothing reviewable is left after filtering, so no
	// model call is made (step 5).
	Empty bool

	fit         *fitted
	ref         provider.PRRef
	p           provider.Provider
	pr          *provider.PullRequest
	d           *provider.Diff
	toggles     Toggles
	maxFindings int
	log         *slog.Logger
}

func progress(deps Deps, stage string) {
	if deps.Progress != nil {
		deps.Progress(stage)
	}
}

// Run reviews one pull request (spec P4 §4.3). It returns a classified
// error (*Error, *provider.Error, *llm.Error) or the result; a failed
// publish is reported in the result, never as an error.
func Run(ctx context.Context, deps Deps, args Args) (*Result, error) {
	if deps.Config == nil || deps.ConfigErr != nil {
		return nil, ErrConfigInvalid
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
		// Nothing to review: no LLM call (P3 review, 3d DESIGN-QUESTION 4).
		// DESIGN-QUESTION: is this empty review published when publish is
		// set? — chose yes because step 13 applies to every returned
		// review and the comment then shows the coverage and the note.
		res.Review = &Review{KeyIssuesToReview: []KeyIssue{}}
		res.Notes = append(res.Notes, NoteNoReviewableChanges)
		publish(ctx, deps, log, args, pl.ref, pl.p, res)
		return res, nil
	}
	progress(deps, StageCallingModel)
	return pl.finish(ctx, deps, args)
}

// Prepare runs steps 1 to 6 of the pipeline: configuration, resolution,
// diff fetch, description, token measurement, diff preparation and the
// request-size guard. It sends nothing to the model and needs no
// Deps.LLM.
func Prepare(ctx context.Context, deps Deps, args Args) (*Plan, error) {
	// Step 1: config.
	if deps.Config == nil || deps.ConfigErr != nil {
		return nil, ErrConfigInvalid
	}
	if deps.Resolver == nil {
		return nil, errNoWiring
	}
	cfg := deps.Config
	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	toggles := TogglesFrom(cfg.Review)
	factor := cfg.LLM.TokenEstimateFactor
	maxFindings := cfg.Review.MaxFindings
	if args.MaxFindings > 0 {
		maxFindings = args.MaxFindings
	}
	extra := cfg.Review.ExtraInstructions
	if args.ExtraInstructions != "" {
		extra = args.ExtraInstructions
	}
	language := cfg.Output.Language
	if args.OutputLanguage != "" {
		language = args.OutputLanguage
	}

	// Step 2: resolve, fetch the PR and its filtered diff.
	progress(deps, StageFetching)
	flt, err := filter.New(cfg)
	if err != nil {
		return nil, err
	}
	ref, p, err := deps.Resolver.Resolve(args.PRURL)
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
	progress(deps, StagePreparingDiff)

	// Step 3: description.
	in := PromptInput{
		Toggles:           toggles,
		MaxFindings:       maxFindings,
		ExtraInstructions: extra,
		Language:          language,
		Title:             pr.Title,
		Branch:            pr.SourceBranch,
		Description:       tokens.ClipDescription(pr.Description, cfg.Diff.MaxDescriptionTokens, factor),
		Date:              prompt.Date(deps.Clock),
	}

	// Step 4: measure the scaffolding and check the budget.
	promptTokens, err := ScaffoldingTokens(in, factor)
	if err != nil {
		return nil, err
	}
	budget := tokens.Budget{
		ContextWindow:   cfg.LLM.ContextWindow,
		MaxOutputTokens: cfg.LLM.MaxOutputTokens,
		PromptTokens:    promptTokens,
		Factor:          factor,
	}
	if err := budget.RequireCapacity(); err != nil {
		return nil, doesNotFit(err)
	}

	// Step 5: prepare the numbered diff.
	prep, err := diffpipe.Prepare(diffpipe.Input{
		Files: d.Files, Skipped: d.Skipped, Mode: diffpipe.ModeNumbered, Budget: budget, Diff: cfg.Diff,
	})
	if err != nil {
		if errors.Is(err, tokens.ErrDoesNotFit) {
			return nil, doesNotFit(err)
		}
		return nil, err
	}
	res := &Result{
		PR:            PRInfo{Kind: string(ref.Kind), URL: logging.RedactURL(ref.URL), Number: ref.Number, Title: pr.Title},
		Coverage:      buildCoverage(prep, flt),
		Notes:         []string{},
		EnabledFields: []string{},
		Metadata: Metadata{
			Model: cfg.LLM.Model, ContextWindow: cfg.LLM.ContextWindow, PromptTokens: promptTokens,
			DiffTokens: prep.Tokens, FastPath: prep.FastPath,
		},
	}
	for _, f := range enabledFields(toggles) {
		res.EnabledFields = append(res.EnabledFields, f.key)
	}
	log.Debug("review: diff prepared", "url", logging.RedactURL(ref.URL), "files", len(d.Files),
		"provider_skipped", len(d.Skipped), "included", len(prep.Included), "clipped", len(prep.Clipped),
		"fast_path", prep.FastPath, "prompt_tokens", promptTokens, "diff_tokens", prep.Tokens)

	pl := &Plan{Result: res, Budget: budget, ref: ref, p: p, pr: pr, d: d, toggles: toggles,
		maxFindings: maxFindings, log: log}
	if prep.Text == "" {
		pl.Empty = true
		return pl, nil
	}

	// Step 6: render the final prompts behind the request-size guard.
	fit, err := fitPrompts(in, prep.Text, budget)
	if err != nil {
		return nil, err
	}
	pl.fit = fit
	pl.Prompts = fit.prompts
	res.Metadata.RequestTokens = fit.requestTokens
	if fit.keptLines >= 0 {
		types := map[string]provider.ChangeType{}
		for _, f := range d.Files {
			types[f.Path] = f.Type
		}
		trimCoverage(&res.Coverage, prep.Text, fit.keptLines, types)
		res.Metadata.DiffTrimmed = true
		res.Metadata.DiffTokens = tokens.Estimate(fit.diff, factor)
		res.Notes = append(res.Notes, NoteDiffTrimmed)
		log.Debug("review: diff trimmed by the request-size guard", "kept_lines", fit.keptLines,
			"request_tokens", fit.requestTokens)
	}
	if n := len(res.Coverage.Clipped); n > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(noteClippedFormat, countPhrase(n, "file was", "files were")))
	}
	return pl, nil
}

// finish runs steps 7 to 13 of a non-empty plan.
func (pl *Plan) finish(ctx context.Context, deps Deps, args Args) (*Result, error) {
	res, log, fit := pl.Result, pl.log, pl.fit
	ref, p, pr, d := pl.ref, pl.p, pl.pr, pl.d
	toggles, maxFindings := pl.toggles, pl.maxFindings

	// Steps 7 and 8: call the model, parse, re-ask once on a parse failure.
	keys := RepairKeys(toggles)
	var rev *Review
	var conv *Conversion
	for attempt := range 2 {
		user := fit.prompts.User
		if attempt == 1 {
			user = fit.reaskUser
			res.Metadata.Reasked = true
		}
		resp, err := deps.LLM.Complete(ctx, fit.prompts.System, user)
		res.Metadata.LLMCalls++
		if err != nil {
			return nil, err
		}
		// Truncated describes the answer that is converted: a cut-off first
		// answer followed by a complete re-ask is not truncated.
		res.Metadata.Truncated = resp.Truncated
		data, trace := yamlrepair.Load(strings.TrimSpace(resp.Content), keys)
		res.Metadata.RepairTactic = trace.Tactic
		log.Debug("review: answer loaded", "attempt", attempt+1, "repair_tactic", trace.Tactic,
			"truncated", resp.Truncated, "prompt_tokens_reported", resp.Usage.PromptTokens,
			"completion_tokens_reported", resp.Usage.CompletionTokens)
		// Step 9: validate and convert.
		rev, conv, err = Convert(data, toggles, maxFindings)
		if err == nil {
			break
		}
		if attempt == 1 {
			return nil, ErrUnparseable.WithCause(err)
		}
	}
	for _, w := range conv.Warnings {
		log.Debug("review: field warning", "warning", w)
	}
	if res.Metadata.Truncated {
		res.Notes = append(res.Notes, NoteTruncated)
	}
	if res.Metadata.Reasked {
		res.Notes = append(res.Notes, NoteReasked)
	}
	res.Notes = append(res.Notes, conv.Notes...)

	// Steps 10 and 11: snippets and links.
	files := map[string]*provider.FilePatch{}
	for i := range d.Files {
		files[d.Files[i].Path] = &d.Files[i]
	}
	unverified := 0
	for i := range rev.KeyIssuesToReview {
		ki := &rev.KeyIssuesToReview[i]
		// DESIGN-QUESTION: how is relevant_file matched to a changed file? —
		// chose an exact match (after trimming) against the paths of the
		// PR's reviewable files, because any normalization (a "./" or "a/"
		// prefix, a basename) could pick the wrong file and show unrelated
		// lines; an unmatched file keeps the finding without a snippet or link.
		fp := files[ki.RelevantFile]
		ki.Snippet, ki.SnippetNote = snippet(fp, ki.StartLine, ki.EndLine)
		if ki.SnippetNote == SnippetNoteUnverified {
			unverified++
		}
		// The path is model-authored: link only a file the PR contains. A
		// hallucinated path (for example one with ".." segments, which
		// browsers resolve to another location on the same host) keeps its
		// file and line text but gets no link.
		if fp != nil {
			ki.Link = p.FileLineURL(ref, pr, ki.RelevantFile, ki.StartLine)
		}
	}
	res.Review = rev
	log.Debug("review: done", "findings", len(rev.KeyIssuesToReview), "unverified_snippets", unverified,
		"llm_calls", res.Metadata.LLMCalls, "reasked", res.Metadata.Reasked)

	// Step 13: publish.
	publish(ctx, deps, log, args, ref, p, res)
	return res, nil
}

// publishFailedMessage is shown for a publish error that is not a
// classified provider error.
const publishFailedMessage = "the review could not be posted as a PR comment"

// publish posts the provider-profile rendering when requested (step 13) and
// records the outcome in res. It never fails the review.
func publish(ctx context.Context, deps Deps, log *slog.Logger, args Args, ref provider.PRRef, p provider.Provider, res *Result) {
	if !args.Publish {
		return
	}
	res.Publish = &PublishResult{}
	var render func(provider.Capabilities) string
	if deps.RenderProvider != nil {
		render = func(caps provider.Capabilities) string { return deps.RenderProvider(res, caps) }
	}
	llmrun.PostResult(ctx, log, ref, p, res.Publish, publishFailedMessage, render)
}
