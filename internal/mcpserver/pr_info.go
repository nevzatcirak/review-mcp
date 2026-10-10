package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// prInfoDescription is the tool description (X-23).
const prInfoDescription = "Reports which branch a pull request merges into and who has reviewed or approved it: the human reviewers with their states, approval counts, required approvals and merge status where the provider exposes them. review-mcp's own reviews and comments are reported separately and never count as approvals. Read-only; no LLM call."

type prInfoInput struct {
	PRURL string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea, Bitbucket Server or GitHub host"`
}

func registerPRInfo(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        "pr_info",
		Description: prInfoDescription,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prInfoInput) (*mcp.CallToolResult, tools.PRInfoResult, error) {
		var zero tools.PRInfoResult
		sc, err := callScope(ctx, deps, req, false)
		if err != nil {
			return nil, zero, err
		}
		defer sc.release()
		res, err := tools.PRInfoTool(ctx, sc.resolver, in.PRURL, logger(deps))
		if err != nil {
			msg := tools.UserMessage(err)
			logger(deps).Debug("pr_info failed", "error", msg)
			return nil, zero, toolError(msg)
		}
		// Counts and the redacted URL only: never titles, names or bodies.
		logger(deps).Debug("pr_info", "url", res.PR.URL, "reviewers", len(res.Reviewers),
			"mergeable_known", res.Mergeable != nil, "required_known", res.RequiredApprovals != nil,
			"notes", len(res.Notes))
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: tools.RenderPRInfoMarkdown(res)}},
		}, res, nil
	})
}
