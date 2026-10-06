package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// prAskDescription is the tool description from spec P5 §2.1.
const prAskDescription = "Answers a question about a pull request using the configured LLM, grounded in the PR's title, description and diff. Set publish=true to also post the question and answer as a PR comment. The PR content and the question are sent to the configured LLM endpoint."

type prAskInput struct {
	PRURL             string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea or Bitbucket Server host"`
	Question          string `json:"question" jsonschema:"the question to answer about the pull request, at most 8000 characters"`
	ExtraInstructions string `json:"extra_instructions,omitempty" jsonschema:"extra instructions for the model; replaces ask.extra_instructions for this call"`
	OutputLanguage    string `json:"output_language,omitempty" jsonschema:"locale code for the answer, for example en-US or tr-TR; replaces output.language for this call"`
	Publish           bool   `json:"publish,omitempty" jsonschema:"also post the question and answer as a PR comment (default false)"`
}

func registerPRAsk(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        "pr_ask",
		Description: prAskDescription,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
		// The output schema is inferred from ask.Result, which is the
		// structured content.
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prAskInput) (*mcp.CallToolResult, ask.Result, error) {
		var zero ask.Result
		log := logger(deps)
		// The degraded configuration check comes first: nothing is built and
		// nothing touches the network.
		cfg, resolver, err := callScope(ctx, deps, req, true)
		if err != nil {
			return nil, zero, err
		}
		if deps.NewLLM == nil {
			return nil, zero, toolError("review-mcp has no LLM wiring; this is a bug")
		}
		res, text, err := tools.PRAsk(ctx, tools.AskDeps{
			Config:   cfg,
			Resolver: resolver,
			NewLLM:   llmFactory(deps),
			Logger:   log,
			Progress: progressFunc(ctx, req, log, "pr_ask"),
		}, tools.PRAskArgs{
			PRURL: in.PRURL, Question: in.Question, ExtraInstructions: in.ExtraInstructions,
			OutputLanguage: in.OutputLanguage, Publish: in.Publish,
		})
		if err != nil {
			msg := tools.UserMessage(err)
			log.Debug("pr_ask failed", "error", msg)
			return nil, zero, toolError(msg)
		}
		// Counts only: never the question, the answer, prompts or PR content.
		log.Debug("pr_ask", "llm_calls", res.Metadata.LLMCalls, "notes", len(res.Notes),
			"truncated", res.Metadata.Truncated, "published", res.Publish != nil && res.Publish.Published)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, *res, nil
	})
}
