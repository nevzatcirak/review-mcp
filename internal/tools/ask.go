package tools

import (
	"context"
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	askrender "github.com/nevzatcirak/review-mcp/internal/ask/render"
	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// PRAskArgs are the arguments of pr_ask (spec P5 §2.1).
type PRAskArgs struct {
	PRURL             string
	Question          string
	ExtraInstructions string
	OutputLanguage    string
	Publish           bool
}

// Validate checks the arguments before any network call: the question
// (ask.ErrQuestionEmpty, ask.ErrQuestionTooLong, whose fixed sentences never
// contain the question) and the output language. The question is checked
// first.
func (a PRAskArgs) Validate() error {
	if _, err := ask.ValidateQuestion(a.Question); err != nil {
		return err
	}
	if a.OutputLanguage != "" && !config.ValidLocale(a.OutputLanguage) {
		return &ArgumentError{InvalidOutputLanguageMessage}
	}
	return nil
}

// AskDeps are the dependencies of PRAsk.
type AskDeps struct {
	Config   *config.Config
	Resolver PRResolver
	// NewLLM builds the request-scoped chat client from the effective
	// configuration (wiring.NewLLM).
	NewLLM func(cfg *config.Config, logger *slog.Logger) (review.Completer, error)
	Logger *slog.Logger
	// Progress receives the ask.Stage* words and review.StageRendering; nil
	// is ignored.
	Progress func(stage string)
}

// AskCall is a pr_ask call that passed every check that needs no network
// (see ReviewCall): the arguments, the LLM client construction and the URL
// resolution.
type AskCall struct {
	deps   AskDeps
	client review.Completer
	args   ask.Args
}

// PreparePRAsk runs the checks of a pr_ask call that need no network. The
// error, if any, is classified; use UserMessage for its text.
func PreparePRAsk(deps AskDeps, a PRAskArgs) (*AskCall, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	client, err := deps.NewLLM(deps.Config, deps.Logger)
	if err != nil {
		return nil, err
	}
	resolver, err := pinResolution(deps.Resolver, a.PRURL)
	if err != nil {
		return nil, err
	}
	deps.Resolver = resolver
	return &AskCall{deps: deps, client: client, args: ask.Args{
		PRURL:             a.PRURL,
		Question:          a.Question,
		ExtraInstructions: a.ExtraInstructions,
		OutputLanguage:    a.OutputLanguage,
		Publish:           a.Publish,
	}}, nil
}

// Run runs the ask pipeline and renders the client-profile markdown.
// progress receives the ask.Stage* words and review.StageRendering (nil is
// ignored); it replaces AskDeps.Progress. The error, if any, is classified.
func (c *AskCall) Run(ctx context.Context, progress func(stage string)) (*ask.Result, string, error) {
	res, err := ask.Run(ctx, ask.Deps{
		Config:         c.deps.Config,
		Logger:         c.deps.Logger,
		Resolver:       c.deps.Resolver,
		LLM:            c.client,
		RenderProvider: askrender.Provider,
		Progress:       progress,
	}, c.args)
	if err != nil {
		return nil, "", err
	}
	if progress != nil {
		progress(review.StageRendering)
	}
	return res, askrender.Client(res), nil
}

// PRAsk validates the arguments, builds the LLM client for this call, runs
// the ask pipeline and renders the client-profile markdown (PreparePRAsk,
// then Run with AskDeps.Progress). The error, if any, is classified; use
// UserMessage for its text.
func PRAsk(ctx context.Context, deps AskDeps, a PRAskArgs) (*ask.Result, string, error) {
	c, err := PreparePRAsk(deps, a)
	if err != nil {
		return nil, "", err
	}
	return c.Run(ctx, deps.Progress)
}
