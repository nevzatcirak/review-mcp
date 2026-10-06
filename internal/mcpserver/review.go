package mcpserver

import (
	"context"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// prReviewDescription is the tool description from spec P4 §6.1.
const prReviewDescription = "Reviews a pull request with the configured LLM and returns a structured review (key issues, effort, tests, security) with code excerpts. Set publish=true to also post it as a PR comment. The PR's title, description and diff are sent to the configured LLM endpoint."

type prReviewInput struct {
	PRURL             string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea or Bitbucket Server host"`
	ExtraInstructions string `json:"extra_instructions,omitempty" jsonschema:"extra review instructions for the model; replaces review.extra_instructions for this call"`
	OutputLanguage    string `json:"output_language,omitempty" jsonschema:"locale code for the review text, for example en-US or tr-TR; replaces output.language for this call"`
	MaxFindings       *int   `json:"max_findings,omitempty" jsonschema:"most key issues to return, 1 to 20; replaces review.max_findings for this call"`
	Publish           bool   `json:"publish,omitempty" jsonschema:"also post the review as a PR comment (default false)"`
}

// progressTotal is the number of stages of a review or an answer.
const progressTotal = 4

// progressFunc returns the stage callback of one call, or nil when the client
// sent no progress token (MCP progress notifications need the token from the
// request's _meta). A failed notification is ignored: progress is a courtesy.
func progressFunc(ctx context.Context, req *mcp.CallToolRequest, log *slog.Logger, tool string) func(string) {
	token := req.Params.GetProgressToken()
	if token == nil || req.Session == nil {
		return nil
	}
	step := 0
	return func(stage string) {
		step++
		err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token, Message: stage, Progress: float64(step), Total: progressTotal,
		})
		if err != nil {
			log.Debug(tool+": progress notification not sent", "stage", stage)
		}
	}
}

func registerPRReview(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        "pr_review",
		Description: prReviewDescription,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
		OutputSchema: review.ResultSchema(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prReviewInput) (*mcp.CallToolResult, review.Result, error) {
		var zero review.Result
		log := logger(deps)
		// The degraded configuration check comes first: nothing is built and
		// nothing touches the network.
		resolver, err := resolverFor(deps)
		if err != nil {
			return nil, zero, err
		}
		if deps.NewLLM == nil {
			return nil, zero, toolError("review-mcp has no LLM wiring; this is a bug")
		}
		res, text, err := tools.PRReview(ctx, tools.ReviewDeps{
			Config:   deps.Config,
			Resolver: resolver,
			NewLLM:   deps.NewLLM,
			Logger:   log,
			Progress: progressFunc(ctx, req, log, "pr_review"),
		}, tools.PRReviewArgs{
			PRURL: in.PRURL, ExtraInstructions: in.ExtraInstructions, OutputLanguage: in.OutputLanguage,
			MaxFindings: in.MaxFindings, Publish: in.Publish,
		})
		if err != nil {
			msg := tools.UserMessage(err)
			log.Debug("pr_review failed", "error", msg)
			return nil, zero, toolError(msg)
		}
		// Counts and the redacted URL only: never the review text, prompts or
		// PR content.
		log.Debug("pr_review", "url", res.PR.URL, "findings", len(res.Review.KeyIssuesToReview),
			"llm_calls", res.Metadata.LLMCalls, "notes", len(res.Notes), "published", res.Publish != nil && res.Publish.Published)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, *res, nil
	})
}
