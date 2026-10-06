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

// PRAsk validates the arguments, builds the LLM client for this call, runs
// the ask pipeline and renders the client-profile markdown. The error, if
// any, is classified; use UserMessage for its text.
func PRAsk(ctx context.Context, deps AskDeps, a PRAskArgs) (*ask.Result, string, error) {
	if err := a.Validate(); err != nil {
		return nil, "", err
	}
	client, err := deps.NewLLM(deps.Config, deps.Logger)
	if err != nil {
		return nil, "", err
	}
	res, err := ask.Run(ctx, ask.Deps{
		Config:         deps.Config,
		Logger:         deps.Logger,
		Resolver:       deps.Resolver,
		LLM:            client,
		RenderProvider: askrender.Provider,
		Progress:       deps.Progress,
	}, ask.Args{
		PRURL:             a.PRURL,
		Question:          a.Question,
		ExtraInstructions: a.ExtraInstructions,
		OutputLanguage:    a.OutputLanguage,
		Publish:           a.Publish,
	})
	if err != nil {
		return nil, "", err
	}
	if deps.Progress != nil {
		deps.Progress(review.StageRendering)
	}
	return res, askrender.Client(res), nil
}
