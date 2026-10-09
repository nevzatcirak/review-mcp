package tools

import (
	"context"
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/describe"
	describerender "github.com/nevzatcirak/review-mcp/internal/describe/render"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Values of the publish_mode argument of pr_describe (v2 spec §3.7).
const (
	PublishModeComment     = "comment"
	PublishModeDescription = "description"
)

// Fixed sentences for invalid pr_describe arguments (X-6). They never echo
// the offending value.
const (
	InvalidPublishModeMessage = "publish_mode must be comment or description"
	InvalidUpdateTitleMessage = "update_title needs publish=true and publish_mode=description"
	// DescribePublishUnavailableMessage refuses publish=true until
	// publishing lands (WP-2d).
	//
	// DESIGN-QUESTION: what does publish=true do before WP-2d? — chose a
	// refusal with this fixed sentence before any network call, so that a
	// client never believes something was written; the alternatives were
	// to ignore the flag (silently not doing what was asked) or to return
	// the description with a note (the run's cost for a call the client
	// will likely repeat).
	DescribePublishUnavailableMessage = "publishing pr_describe results is not available yet; call pr_describe with publish=false (the default) and nothing is written to the pull request"
)

// PRDescribeArgs are the arguments of pr_describe (v2 spec §3.7).
type PRDescribeArgs struct {
	PRURL          string
	OutputLanguage string
	Publish        bool
	// PublishMode is "" (comment), PublishModeComment or
	// PublishModeDescription.
	PublishMode string
	UpdateTitle bool
}

// Validate checks the arguments before any network call, in order: the
// output language, publish_mode, update_title (only with publish=true and
// publish_mode=description), then publish itself, which is refused until
// WP-2d. Its error text is one of the fixed sentences above.
func (a PRDescribeArgs) Validate() error {
	if a.OutputLanguage != "" && !config.ValidLocale(a.OutputLanguage) {
		return &ArgumentError{InvalidOutputLanguageMessage}
	}
	mode := a.PublishMode
	if mode == "" {
		mode = PublishModeComment
	}
	if mode != PublishModeComment && mode != PublishModeDescription {
		return &ArgumentError{InvalidPublishModeMessage}
	}
	if a.UpdateTitle && (!a.Publish || mode != PublishModeDescription) {
		return &ArgumentError{InvalidUpdateTitleMessage}
	}
	if a.Publish {
		return &ArgumentError{DescribePublishUnavailableMessage}
	}
	return nil
}

// DescribeDeps are the dependencies of PRDescribe.
type DescribeDeps struct {
	Config   *config.Config
	Resolver PRResolver
	// NewLLM builds the request-scoped chat client from the effective
	// configuration (wiring.NewLLM).
	NewLLM func(cfg *config.Config, logger *slog.Logger) (review.Completer, error)
	Logger *slog.Logger
	// Progress receives the describe.Stage* words; nil is ignored.
	Progress func(stage string)
}

// DescribeCall is a pr_describe call that passed every check that needs no
// network (see ReviewCall): the arguments, the LLM client construction and
// the URL resolution.
type DescribeCall struct {
	deps   DescribeDeps
	client review.Completer
	args   describe.Args
}

// PreparePRDescribe runs the checks of a pr_describe call that need no
// network. The error, if any, is classified; use UserMessage for its text.
func PreparePRDescribe(deps DescribeDeps, a PRDescribeArgs) (*DescribeCall, error) {
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
	return &DescribeCall{deps: deps, client: client, args: describe.Args{
		PRURL: a.PRURL, OutputLanguage: a.OutputLanguage,
	}}, nil
}

// Run runs the describe pipeline and renders the client-profile markdown.
// progress receives the describe.Stage* words (nil is ignored); it replaces
// DescribeDeps.Progress. The error, if any, is classified.
func (c *DescribeCall) Run(ctx context.Context, progress func(stage string)) (*describe.Result, string, error) {
	res, err := describe.Run(ctx, describe.Deps{
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
		progress(describe.StageRendering)
	}
	return res, describerender.Client(res), nil
}

// PRDescribe validates the arguments, builds the LLM client for this call,
// runs the describe pipeline and renders the client-profile markdown
// (PreparePRDescribe, then Run with DescribeDeps.Progress). The error, if
// any, is classified; use UserMessage for its text.
func PRDescribe(ctx context.Context, deps DescribeDeps, a PRDescribeArgs) (*describe.Result, string, error) {
	c, err := PreparePRDescribe(deps, a)
	if err != nil {
		return nil, "", err
	}
	return c.Run(ctx, deps.Progress)
}
