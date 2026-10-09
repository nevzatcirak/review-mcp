package tools

import (
	"context"
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/improve"
	improverender "github.com/nevzatcirak/review-mcp/internal/improve/render"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// ImprovePublishUnavailableMessage refuses publish=true until publishing
// lands (WP-2h), as pr_describe refused it before WP-2d: before any network
// call, so that a client never believes something was written.
const ImprovePublishUnavailableMessage = "publishing pr_improve results is not available yet; call pr_improve with publish=false (the default) and nothing is written to the pull request"

// PRImproveArgs are the arguments of pr_improve (v2 spec §1.8).
type PRImproveArgs struct {
	PRURL          string
	OutputLanguage string
	Publish        bool
}

// Validate checks the arguments before any network call, in order: the
// output language, then publish, which is refused until WP-2h. Its error
// text is one of the fixed sentences.
func (a PRImproveArgs) Validate() error {
	if a.OutputLanguage != "" && !config.ValidLocale(a.OutputLanguage) {
		return &ArgumentError{InvalidOutputLanguageMessage}
	}
	if a.Publish {
		return &ArgumentError{ImprovePublishUnavailableMessage}
	}
	return nil
}

// ImproveDeps are the dependencies of PRImprove.
type ImproveDeps struct {
	Config   *config.Config
	Resolver PRResolver
	// NewLLM builds the request-scoped chat client from the effective
	// configuration (wiring.NewLLM).
	NewLLM func(cfg *config.Config, logger *slog.Logger) (review.Completer, error)
	Logger *slog.Logger
	// Progress receives the improve.Stage* words; nil is ignored.
	Progress func(stage string)
}

// ImproveCall is a pr_improve call that passed every check that needs no
// network (see ReviewCall): the arguments, the LLM client construction and
// the URL resolution.
type ImproveCall struct {
	deps   ImproveDeps
	client review.Completer
	args   improve.Args
}

// PreparePRImprove runs the checks of a pr_improve call that need no
// network. The error, if any, is classified; use UserMessage for its text.
func PreparePRImprove(deps ImproveDeps, a PRImproveArgs) (*ImproveCall, error) {
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
	return &ImproveCall{deps: deps, client: client, args: improve.Args{
		PRURL: a.PRURL, OutputLanguage: a.OutputLanguage,
	}}, nil
}

// Run runs the improve pipeline and renders the client markdown. progress
// receives the improve.Stage* words (nil is ignored); it replaces
// ImproveDeps.Progress. The error, if any, is classified.
func (c *ImproveCall) Run(ctx context.Context, progress func(stage string)) (*improve.Result, string, error) {
	res, err := improve.Run(ctx, improve.Deps{
		Config:   c.deps.Config,
		Logger:   c.deps.Logger,
		Resolver: c.deps.Resolver,
		LLM:      c.client,
		Progress: progress,
	}, c.args)
	if err != nil {
		return nil, "", err
	}
	if progress != nil {
		progress(improve.StageRendering)
	}
	return res, improverender.Client(res), nil
}

// PRImprove validates the arguments, builds the LLM client for this call,
// runs the improve pipeline and renders the client markdown
// (PreparePRImprove, then Run with ImproveDeps.Progress). The error, if
// any, is classified; use UserMessage for its text.
func PRImprove(ctx context.Context, deps ImproveDeps, a PRImproveArgs) (*improve.Result, string, error) {
	c, err := PreparePRImprove(deps, a)
	if err != nil {
		return nil, "", err
	}
	return c.Run(ctx, deps.Progress)
}
