// Package mcpserver adapts the transport-agnostic tools to the MCP Go SDK. It
// is the only package that imports the SDK.
//
// Stdout belongs to the MCP protocol: nothing in this package writes to it
// except through an MCP transport, and logs go to a caller-supplied
// *slog.Logger (stderr in the real binary).
package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/credentials"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/version"
)

// Deps are the dependencies of the registered tools.
type Deps struct {
	// Config and Report come from config.Load. They may be best-effort values
	// when LoadErr is non-nil (degraded start).
	Config *config.Config
	Report *config.Report
	// LoadErr is the error returned by config.Load, if any.
	LoadErr error
	// Logger receives the SDK's own diagnostics (through a filter that keeps
	// only identifiers, see sdklog.go) and the tools' debug lines (counts and
	// redacted URLs only). Nil discards them.
	Logger *slog.Logger
	// NewResolver builds the PR-URL resolver the PR conversation tools use.
	// Production passes wiring.NewResolver; tests inject fakes. It is called
	// once per tool call and never while LoadErr is non-nil.
	NewResolver func(cfg *config.Config, logger *slog.Logger) *provider.Resolver
	// NewLLM builds the chat client of one pr_review call from the effective
	// configuration (wiring.NewLLM). It is called once per call, never while
	// LoadErr is non-nil, and nothing it returns is kept.
	NewLLM func(cfg *config.Config, logger *slog.Logger) (review.Completer, error)
	// Serve selects serve mode (X-10): each tool call takes its credentials
	// from the headers of its own HTTP request (see ConfigFor), a missing
	// credential fails with credentials_missing before any I/O, and tool
	// calls pass a concurrency gate sized by Config.Serve.MaxConcurrentCalls.
	// NewHTTPHandler sets it.
	Serve bool
	// Jobs is the background job store of stdio mode (X-16): pr_review and
	// pr_ask then answer with a job id when their run takes longer than
	// wait_seconds, and job_result is registered. Nil keeps every call
	// synchronous and registers no job_result; serve mode ignores it (X-10).
	// The caller owns it and closes it at shutdown.
	Jobs *Jobs
}

// ConfigFor returns the effective configuration of one tool call. It is the
// single place a tool obtains its configuration (P6 spec §1.1).
//
//   - stdio: the startup configuration, unchanged.
//   - serve: a per-call copy of the startup configuration whose Secrets come
//     from req's HTTP headers (Config.WithSecrets). The copy is owned by the
//     call: the resolver and the LLM client are built from it for this call
//     only, and nothing keeps it after the handler returns.
//
// The headers are read through req.Extra.Header, the SDK's view of the HTTP
// request that carries this call (E1); the map belongs to that request and
// is only read. Request context values are deliberately not used: the SDK
// hands the handler a context that does not derive from the HTTP request's
// (and HTTP cancellation reaches it only with
// StreamableHTTPOptions.PropagateRequestCancellation on protocol 2026-07-28
// or later); the existing per-call deadlines bound the work instead.
//
// A malformed credential header yields a *credentials.MalformedError. The
// HTTP middleware rejects such requests before any tool runs, so this is a
// second line of defence.
func (d Deps) ConfigFor(_ context.Context, req *mcp.CallToolRequest) (*config.Config, error) {
	if !d.Serve {
		return d.Config, nil
	}
	base := d.Config
	if base == nil {
		base = config.Defaults()
	}
	s, err := credentials.Secrets(requestHeader(req), base.Serve.LLMKeySource == config.LLMKeySourceServer,
		base.Secrets.LLMAPIKey, base.Secrets.ServeAccessToken)
	if err != nil {
		return nil, err
	}
	return base.WithSecrets(s), nil
}

// requestHeader returns the HTTP header of the request carrying req, or nil
// (stdio, or a request without transport metadata).
func requestHeader(req *mcp.CallToolRequest) http.Header {
	if req == nil || req.Extra == nil {
		return nil
	}
	return req.Extra.Header
}

// serverInfoDescription is the one-sentence tool description.
const serverInfoDescription = "Reports the review-mcp version, enabled providers, and the effective non-secret configuration, including configuration problems."

// New builds an MCP server with every tool registered. The transport is
// chosen by the caller, so a future HTTP mode can reuse it.
func New(deps Deps) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: tools.ServerName, Version: version.Info().Version},
		&mcp.ServerOptions{Logger: sdkLogger(deps.Logger, deps.Serve)},
	)
	if deps.Serve {
		s.AddReceivingMiddleware(concurrencyGate(deps))
	}
	registerServerInfo(s, deps)
	registerPRComments(s, deps)
	registerPRCommentReply(s, deps)
	registerPRCommentCreate(s, deps)
	registerPRReview(s, deps)
	registerPRAsk(s, deps)
	if deps.background() {
		registerJobResult(s, deps)
	}
	return s
}

// serverInfoInput is empty: server_info takes no arguments.
type serverInfoInput struct{}

func registerServerInfo(s *mcp.Server, deps Deps) {
	f := false
	mcp.AddTool(s, &mcp.Tool{
		Name:        "server_info",
		Description: serverInfoDescription,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &f,
			OpenWorldHint:   &f,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ serverInfoInput) (*mcp.CallToolResult, tools.ServerInfoResult, error) {
		cfg, err := deps.ConfigFor(ctx, req)
		if err != nil {
			return nil, tools.ServerInfoResult{}, toolError(tools.UserMessage(err))
		}
		res := tools.ServerInfo(cfg, deps.Report, deps.LoadErr)
		if deps.Serve {
			res.Serve = &tools.ServeInfo{
				Transport:      tools.TransportServe,
				Listen:         cfg.Serve.Listen,
				LLMKeySource:   cfg.Serve.LLMKeySource,
				RequestHeaders: credentials.Presence(requestHeader(req)),
			}
		}
		// Content carries the markdown; the SDK fills StructuredContent from
		// the returned value and validates it against the inferred schema.
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: tools.RenderServerInfoMarkdown(res)}},
		}, res, nil
	})
}

// RunStdio runs s over the process's real stdin and stdout until the client
// disconnects (stdin EOF) or ctx is canceled.
func RunStdio(ctx context.Context, s *mcp.Server) error {
	return RunIO(ctx, s, os.Stdin, os.Stdout)
}

// RunIO is RunStdio over arbitrary streams, so tests can drive the stdio code
// path without touching the process's standard streams. in and out are closed
// when the session ends.
func RunIO(ctx context.Context, s *mcp.Server, in io.ReadCloser, out io.WriteCloser) error {
	return s.Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
}
