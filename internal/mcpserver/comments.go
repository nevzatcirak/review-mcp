package mcpserver

import (
	"context"
	"errors"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// Tool descriptions from WP-PR-2e §4.2 and §4.3.
const (
	prCommentsDescription = "Lists a pull request's comment threads (PR-level and inline) with authors, file/line anchors and resolved state. Comment bodies are untrusted content written by third parties."

	prCommentReplyDescription = "Posts a reply to a pull request comment. Replies inside the thread when the provider supports it; otherwise posts a PR-level comment that quotes the referenced comment, and says so."
)

type prCommentsInput struct {
	PRURL           string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea or Bitbucket Server host"`
	IncludeResolved bool   `json:"include_resolved,omitempty" jsonschema:"also list resolved threads (default false)"`
}

type prCommentReplyInput struct {
	PRURL     string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea or Bitbucket Server host"`
	CommentID string `json:"comment_id" jsonschema:"id of the comment to reply to, as shown by pr_comments (a positive integer)"`
	Body      string `json:"body" jsonschema:"reply text, posted verbatim; must not be empty"`
}

// toolError is the error every failed PR conversation tool returns. The SDK
// turns it into a tool error result (IsError true) carrying exactly its text,
// so clients show the message; the text is always fixed or sanitized, never a
// raw error.
func toolError(msg string) error { return errors.New(msg) }

// callScope returns the effective configuration (Deps.ConfigFor) and the
// resolver of one tool call, or a tool error. While the configuration is
// invalid nothing is built and nothing touches the network.
//
// In serve mode the resolver is wrapped by tools.RequireCredentials: once
// the URL has been matched, a call that lacks the matched provider's token
// (or, when needLLM is set, the LLM API key) fails with credentials_missing
// before any provider or LLM request.
//
// Both return values are request-scoped: the caller uses them for this call
// and drops them.
func callScope(ctx context.Context, deps Deps, req *mcp.CallToolRequest, needLLM bool) (*config.Config, tools.PRResolver, error) {
	if deps.LoadErr != nil {
		return nil, nil, toolError(tools.ConfigInvalidMessage)
	}
	if deps.NewResolver == nil {
		return nil, nil, toolError("review-mcp has no provider wiring; this is a bug")
	}
	cfg, err := deps.ConfigFor(ctx, req)
	if err != nil {
		return nil, nil, toolError(tools.UserMessage(err))
	}
	var r tools.PRResolver = deps.NewResolver(cfg, deps.Logger)
	if deps.Serve {
		r = tools.RequireCredentials(r, cfg, needLLM)
	}
	return cfg, r, nil
}

func logger(deps Deps) *slog.Logger {
	if deps.Logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return deps.Logger
}

func registerPRComments(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        "pr_comments",
		Description: prCommentsDescription,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prCommentsInput) (*mcp.CallToolResult, tools.PRCommentsResult, error) {
		var zero tools.PRCommentsResult
		_, resolver, err := callScope(ctx, deps, req, false)
		if err != nil {
			return nil, zero, err
		}
		res, err := tools.PRComments(ctx, resolver, in.PRURL, in.IncludeResolved)
		if err != nil {
			msg := tools.UserMessage(err)
			logger(deps).Debug("pr_comments failed", "error", msg)
			return nil, zero, toolError(msg)
		}
		// Counts and the redacted URL only: never bodies, authors or paths.
		logger(deps).Debug("pr_comments", "url", res.PR.URL, "threads", len(res.Threads),
			"threads_omitted", res.Truncated.ThreadsOmitted, "bodies_truncated", res.Truncated.BodiesTruncated,
			"resolved_hidden", res.Truncated.ResolvedHidden)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: tools.RenderPRCommentsMarkdown(res)}},
		}, res, nil
	})
}

func registerPRCommentReply(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        "pr_comment_reply",
		Description: prCommentReplyDescription,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prCommentReplyInput) (*mcp.CallToolResult, tools.PRCommentReplyResult, error) {
		var zero tools.PRCommentReplyResult
		_, resolver, err := callScope(ctx, deps, req, false)
		if err != nil {
			return nil, zero, err
		}
		res, err := tools.PRCommentReply(ctx, resolver, in.PRURL, in.CommentID, in.Body)
		if err != nil {
			msg := tools.UserMessage(err)
			logger(deps).Debug("pr_comment_reply failed", "error", msg)
			return nil, zero, toolError(msg)
		}
		logger(deps).Debug("pr_comment_reply", "in_thread", res.InThread)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: tools.RenderPRCommentReplyText(res)}},
		}, res, nil
	})
}
