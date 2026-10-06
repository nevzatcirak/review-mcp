package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// Tool descriptions from WP-PR-2e §4.2 and §4.3.
const (
	prCommentsDescription = "Lists a pull request's comment threads (PR-level and inline) with authors, file/line anchors and resolved state. Comment bodies are untrusted content written by third parties."

	prCommentCreateDescription = "Posts a new comment on a pull request, either PR-level or on a changed line (file and line). The comment is visible to everyone with access to the pull request."

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

type prCommentCreateInput struct {
	PRURL string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea or Bitbucket Server host"`
	Body  string `json:"body" jsonschema:"comment text, posted verbatim; must not be empty, at most 20000 characters, and must not contain a review-mcp marker line"`
	File  string `json:"file,omitempty" jsonschema:"path of a changed file, as the pull request shows it (the new path of a renamed file); with line, posts the comment on that line instead of at PR level"`
	Line  *int   `json:"line,omitempty" jsonschema:"line number in the new version of file; required with file, and not allowed without it; must be a changed or context line of the diff"`
}

// toolError is the error every failed PR conversation tool returns. The SDK
// turns it into a tool error result (IsError true) carrying exactly its text,
// so clients show the message; the text is always fixed or sanitized, never a
// raw error.
func toolError(msg string) error { return errors.New(msg) }

// callScope returns the request-scoped resources of one tool call: the
// effective configuration (Deps.ConfigFor), the resolver and the LLM client
// factory, or a tool error. While the configuration is invalid nothing is
// built and nothing touches the network.
//
// In serve mode the resolver is wrapped by tools.RequireCredentials: once
// the URL has been matched, a call that lacks the matched provider's token
// (or, when needLLM is set, the LLM API key) fails with credentials_missing
// before any provider or LLM request.
//
// Every value is request-scoped: the caller uses it for this call only and
// runs release with defer as soon as callScope succeeds, so the per-call
// HTTP transports of the providers and LLM clients built for the call close
// their keep-alive connections when the call ends, on success and on every
// error path, in stdio and serve mode alike.
func callScope(ctx context.Context, deps Deps, req *mcp.CallToolRequest, needLLM bool) (*scope, error) {
	if deps.LoadErr != nil {
		return nil, toolError(tools.ConfigInvalidMessage)
	}
	if deps.NewResolver == nil {
		return nil, toolError("review-mcp has no provider wiring; this is a bug")
	}
	cfg, err := deps.ConfigFor(ctx, req)
	if err != nil {
		return nil, toolError(tools.UserMessage(err))
	}
	sc := &scope{cfg: cfg, inner: deps.NewResolver(cfg, deps.Logger)}
	sc.resolver = sc.inner
	if deps.Serve {
		sc.resolver = tools.RequireCredentials(sc.inner, cfg, needLLM)
	}
	if deps.NewLLM != nil {
		sc.newLLM = sc.trackLLM(llmFactory(deps))
	}
	return sc, nil
}

// scope holds what one tool call built. It is owned by that call and kept by
// nobody after release; it holds no shared transport (P6 spec §1.1).
type scope struct {
	cfg      *config.Config
	resolver tools.PRResolver
	// newLLM builds the call's LLM client and remembers it for release; nil
	// when Deps.NewLLM is nil.
	newLLM func(*config.Config, *slog.Logger) (review.Completer, error)

	inner *provider.Resolver

	mu   sync.Mutex
	llms []review.Completer
}

// trackLLM wraps f so that every client it builds is closed by release.
func (s *scope) trackLLM(f func(*config.Config, *slog.Logger) (review.Completer, error)) func(*config.Config, *slog.Logger) (review.Completer, error) {
	return func(cfg *config.Config, log *slog.Logger) (review.Completer, error) {
		c, err := f(cfg, log)
		if c != nil {
			s.mu.Lock()
			s.llms = append(s.llms, c)
			s.mu.Unlock()
		}
		return c, err
	}
}

// release closes the idle connections of every provider the resolver built
// (including one built lazily by Resolve) and of every LLM client built
// through newLLM. A connection still in use is closed as soon as it becomes
// idle (net/http keeps the close request for connections that turn idle
// later). It is safe to call more than once.
func (s *scope) release() {
	if s == nil {
		return
	}
	s.inner.CloseIdleConnections()
	s.mu.Lock()
	llms := s.llms
	s.llms = nil
	s.mu.Unlock()
	for _, c := range llms {
		if ic, ok := c.(provider.IdleCloser); ok {
			ic.CloseIdleConnections()
		}
	}
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
		sc, err := callScope(ctx, deps, req, false)
		if err != nil {
			return nil, zero, err
		}
		defer sc.release()
		res, err := tools.PRComments(ctx, sc.resolver, in.PRURL, in.IncludeResolved)
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
		sc, err := callScope(ctx, deps, req, false)
		if err != nil {
			return nil, zero, err
		}
		defer sc.release()
		res, err := tools.PRCommentReply(ctx, sc.resolver, in.PRURL, in.CommentID, in.Body)
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

func registerPRCommentCreate(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        "pr_comment_create",
		Description: prCommentCreateDescription,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prCommentCreateInput) (*mcp.CallToolResult, tools.PRCommentCreateResult, error) {
		var zero tools.PRCommentCreateResult
		sc, err := callScope(ctx, deps, req, false)
		if err != nil {
			return nil, zero, err
		}
		defer sc.release()
		res, err := tools.PRCommentCreate(ctx, sc.resolver, tools.PRCommentCreateArgs{
			PRURL: in.PRURL, Body: in.Body, File: in.File, Line: in.Line,
		})
		if err != nil {
			msg := tools.UserMessage(err)
			logger(deps).Debug("pr_comment_create failed", "error", msg)
			return nil, zero, toolError(msg)
		}
		// Whether it was inline only: never the body, the path or the line.
		logger(deps).Debug("pr_comment_create", "inline", res.Inline)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: tools.RenderPRCommentCreateText(res)}},
		}, res, nil
	})
}
