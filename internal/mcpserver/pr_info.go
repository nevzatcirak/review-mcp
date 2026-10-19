package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/related"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// prInfoDescription is the tool description (X-23).
const prInfoDescription = "Reports which branch a pull request merges into and who has reviewed or approved it: the human reviewers with their states, approval counts, required approvals and merge status where the provider exposes them, and the CI results (statuses and checks) the repository reported for the head commit. review-mcp's own reviews and comments are reported separately and never count as approvals. Read-only; no LLM call."

type prInfoInput struct {
	PRURL   string   `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea, Bitbucket Server, GitHub or GitLab host"`
	Related []string `json:"related,omitempty" jsonschema:"Up to 4 related pull request or repository URLs on configured providers, for example the backend change a UI pull request depends on: a pull request's title, state, draft flag, target branch and head are reported; a repository (optionally with a branch) is resolved and listed as not_read, never fetched, with a note when one is merged while this pull request is open or closed without being merged. No diff is read."`
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
		// The related argument is checked before anything is built or
		// requested (X-41).
		relatedURLs, err := related.Validate(in.Related, false)
		if err != nil {
			return nil, zero, toolError(err.Error())
		}
		sc, err := callScope(ctx, deps, req, false)
		if err != nil {
			return nil, zero, err
		}
		defer sc.release()
		res, err := tools.PRInfoFor(ctx, sc.cfg, sc.resolver, in.PRURL, relatedURLs, logger(deps))
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
