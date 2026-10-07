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
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
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
}

// errNoWiring reports a caller bug: Run needs a resolver and an LLM.
var errNoWiring = errors.New("ask: resolver or LLM dependency missing")

// Progress stages reported through Deps.Progress. They are fixed words,
// never PR content. The names match pr_review's.
const (
	StageFetching      = "fetching"
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
		pl.publish(ctx, deps, args)
		return res, nil
	}
	progress(deps, StageCallingModel)

	// Step 7: one call; the client owns the transport retries. There is no
	// re-ask, because there is nothing to parse.
	resp, err := deps.LLM.Complete(ctx, pl.Prompts.System, pl.Prompts.User)
	res.Metadata.LLMCalls++
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

	// Step 8: the result.
	res.Answer = answer

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
	factor := cfg.LLM.TokenEstimateFactor
	extra := cfg.Ask.ExtraInstructions
	if args.ExtraInstructions != "" {
		extra = args.ExtraInstructions
	}
	language := cfg.Output.Language
	if args.OutputLanguage != "" {
		language = args.OutputLanguage
	}

	// Step 3: resolve, fetch the PR and its filtered diff.
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
	d, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: flt.Include})
	if err != nil {
		return nil, err
	}
	progress(deps, StagePreparingDiff)
	in := PromptInput{
		ExtraInstructions: extra,
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
	// question is about the code, not about line anchors).
	prep, err := diffpipe.Prepare(diffpipe.Input{
		Files: d.Files, Skipped: d.Skipped, Mode: diffpipe.ModePlain, Budget: budget, Diff: cfg.Diff,
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
	}
	log.Debug("ask: diff prepared", "url", logging.RedactURL(ref.URL), "files", len(d.Files),
		"provider_skipped", len(d.Skipped), "included", len(prep.Included), "clipped", len(prep.Clipped),
		"fast_path", prep.FastPath, "prompt_tokens", promptTokens, "diff_tokens", prep.Tokens)

	pl := &Plan{Result: res, Budget: budget, ref: ref, p: p, log: log}
	if prep.Text == "" {
		pl.Empty = true
		return pl, nil
	}

	// Step 6: render the final prompts behind the request-size guard.
	fit, err := llmrun.Fit(prep.Text, budget, func(diff string) (llmrun.Rendered, error) {
		in := in
		in.Diff = diff
		pr, err := RenderPrompts(in)
		if err != nil {
			return llmrun.Rendered{}, err
		}
		return llmrun.Rendered{System: pr.System, User: pr.User}, nil
	})
	if err != nil {
		return nil, err
	}
	pl.Prompts = Prompts{System: fit.Rendered.System, User: fit.Rendered.User}
	res.Metadata.RequestTokens = fit.RequestTokens
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
	return pl, nil
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
