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

// PRReview validates the arguments, builds the LLM client for this call,
// runs the review pipeline and renders the client-profile markdown. The
// error, if any, is classified; use UserMessage for its text.
func PRReview(ctx context.Context, deps ReviewDeps, a PRReviewArgs) (*review.Result, string, error) {
	if err := a.Validate(); err != nil {
		return nil, "", err
	}
	client, err := deps.NewLLM(deps.Config, deps.Logger)
	if err != nil {
		return nil, "", err
	}
	args := review.Args{
		PRURL:             a.PRURL,
		ExtraInstructions: a.ExtraInstructions,
		OutputLanguage:    a.OutputLanguage,
		Publish:           a.Publish,
	}
	if a.MaxFindings != nil {
		args.MaxFindings = *a.MaxFindings
	}
	res, err := review.Run(ctx, review.Deps{
		Config:         deps.Config,
		Logger:         deps.Logger,
		Resolver:       deps.Resolver,
		LLM:            client,
		RenderProvider: render.Provider,
		Progress:       deps.Progress,
	}, args)
	if err != nil {
		return nil, "", err
	}
	if deps.Progress != nil {
		deps.Progress(review.StageRendering)
	}
	return res, render.Client(res), nil
}
