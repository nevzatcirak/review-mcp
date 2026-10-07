package tools

import (
	"context"
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
)

// Bounds of the max_findings argument (the same range as
// review.max_findings).
const (
	MinMaxFindings = 1
	MaxMaxFindings = 20
)

// Fixed sentences for invalid pr_review arguments (X-6). They never echo the
// offending value.
const (
	InvalidOutputLanguageMessage = "output_language must be a locale code such as en-US or tr-TR"
	InvalidMaxFindingsMessage    = "max_findings must be an integer from 1 to 20"
)

// ArgumentError is an invalid tool argument. Its text is one of the fixed
// sentences above; UserMessage shows it as is.
type ArgumentError struct{ msg string }

func (e *ArgumentError) Error() string { return e.msg }

// PRReviewArgs are the arguments of pr_review (DQ-25).
type PRReviewArgs struct {
	PRURL             string
	ExtraInstructions string
	OutputLanguage    string
	// MaxFindings is nil when the argument was not given.
	MaxFindings *int
	Publish     bool
	// InlineFindings is nil when the argument was not given; review.
	// inline_findings then decides.
	InlineFindings *bool
}

// Validate checks the optional arguments before any network call. It returns
// an error whose text is one of the fixed sentences above.
func (a PRReviewArgs) Validate() error {
	if a.OutputLanguage != "" && !config.ValidLocale(a.OutputLanguage) {
		return &ArgumentError{InvalidOutputLanguageMessage}
	}
	if a.MaxFindings != nil && (*a.MaxFindings < MinMaxFindings || *a.MaxFindings > MaxMaxFindings) {
		return &ArgumentError{InvalidMaxFindingsMessage}
	}
	return nil
}

// ReviewDeps are the dependencies of PRReview.
type ReviewDeps struct {
	Config   *config.Config
	Resolver PRResolver
	// NewLLM builds the request-scoped chat client from the effective
	// configuration (wiring.NewLLM).
	NewLLM func(cfg *config.Config, logger *slog.Logger) (review.Completer, error)
	Logger *slog.Logger
	// Progress receives the review.Stage* words; nil is ignored.
	Progress func(stage string)
}

// ReviewCall is a pr_review call that passed every check that needs no
// network: the arguments, the LLM client construction and the URL
// resolution (including serve mode's credential check). Run does the rest:
// the context-window probe, the provider and LLM I/O, the publish and the
// rendering. The split is the job boundary of X-16: a check that fails here
// answers the MCP call at once, and only Run may continue in the
// background.
type ReviewCall struct {
	deps   ReviewDeps
	client review.Completer
	args   review.Args
}

// PreparePRReview runs the checks of a pr_review call that need no network.
// The error, if any, is classified; use UserMessage for its text.
func PreparePRReview(deps ReviewDeps, a PRReviewArgs) (*ReviewCall, error) {
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
	args := review.Args{
		PRURL:             a.PRURL,
		ExtraInstructions: a.ExtraInstructions,
		OutputLanguage:    a.OutputLanguage,
		Publish:           a.Publish,
	}
	if a.MaxFindings != nil {
		args.MaxFindings = *a.MaxFindings
	}
	args.InlineFindings = a.InlineFindings
	// The options the call leaves unset come from the configuration; the
	// persistent overview and the discussion budget have no tool argument.
	args = args.WithConfigDefaults(deps.Config)
	return &ReviewCall{deps: deps, client: client, args: args}, nil
}

// Run runs the review pipeline and renders the client-profile markdown.
// progress receives the review.Stage* words (nil is ignored); it replaces
// ReviewDeps.Progress. The error, if any, is classified.
func (c *ReviewCall) Run(ctx context.Context, progress func(stage string)) (*review.Result, string, error) {
	res, err := review.Run(ctx, review.Deps{
		Config:         c.deps.Config,
		Logger:         c.deps.Logger,
		Resolver:       c.deps.Resolver,
		LLM:            c.client,
		RenderProvider: render.Provider,
		RenderInline:   render.Inline,
		Progress:       progress,
	}, c.args)
	if err != nil {
		return nil, "", err
	}
	if progress != nil {
		progress(review.StageRendering)
	}
	return res, render.Client(res), nil
}

// PRReview validates the arguments, builds the LLM client for this call,
// runs the review pipeline and renders the client-profile markdown
// (PreparePRReview, then Run with ReviewDeps.Progress). The error, if any,
// is classified; use UserMessage for its text.
func PRReview(ctx context.Context, deps ReviewDeps, a PRReviewArgs) (*review.Result, string, error) {
	c, err := PreparePRReview(deps, a)
	if err != nil {
		return nil, "", err
	}
	return c.Run(ctx, deps.Progress)
}
