package mcpserver

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// NewHTTPHandler returns the MCP endpoint of serve mode: the SDK's
// streamable HTTP handler in stateless mode over one server built with
// deps (Serve forced on). Every POST gets a temporary session that ends with
// the request, so no session state outlives a request (X-10). The caller
// (internal/serve) puts the P6 §1.4 middleware in front of it.
//
// The SDK's own localhost protection is disabled: internal/serve's Host
// check replaces it. The SDK check ignores the port and applies whenever the
// local address is loopback, which would also reject a same-host
// TLS-terminating proxy in front of a non-loopback bind.
//
// PropagateRequestCancellation stays off (the default).
// DESIGN-QUESTION: should a client disconnect cancel the tool call? — chose
// no (the SDK default) because P6 §1.4 relies on the existing per-call
// deadlines to bound long calls and the option only covers protocol
// 2026-07-28 and later.
func NewHTTPHandler(deps Deps) http.Handler {
	deps.Serve = true
	s := New(deps)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		Logger:                     sdkLogger(deps.Logger, true),
		DisableLocalhostProtection: true,
	})
}

// methodCallTool is the JSON-RPC method of a tool call.
const methodCallTool = "tools/call"

// defaultConcurrentCalls sizes the gate when no configuration is present
// (the serve.max_concurrent_calls default).
const defaultConcurrentCalls = 4

// concurrencyGate limits concurrent tool calls to
// Config.Serve.MaxConcurrentCalls (P6 §1.4 step 6). Other methods
// (initialize, tools/list, ping, ...) are not counted. When every slot is
// taken the call fails at once with server_busy and does not run. The
// semaphore belongs to the one server New builds, so it is shared by every
// request of the process and holds no request data.
func concurrencyGate(deps Deps) mcp.Middleware {
	n := defaultConcurrentCalls
	if deps.Config != nil && deps.Config.Serve.MaxConcurrentCalls > 0 {
		n = deps.Config.Serve.MaxConcurrentCalls
	}
	sem := make(chan struct{}, n)
	log := logger(deps)
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodCallTool {
				return next(ctx, method, req)
			}
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				return next(ctx, method, req)
			default:
				log.Debug("tool call rejected: every slot is taken", "max_concurrent_calls", n)
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: tools.UserMessage(tools.ErrServerBusy)}},
				}, nil
			}
		}
	}
}

// llmFactory returns the LLM client factory of one call. In serve mode a call
// without an LLM API key gets a client that fails with credentials_missing
// and sends nothing. The resolver wrapper (tools.RequireCredentials) already
// rejects such a call right after URL resolution, before any provider I/O;
// this is the second line of defence, and it keeps the URL check first
// (tools.PRReview and tools.PRAsk build the client before resolving).
func llmFactory(deps Deps) func(*config.Config, *slog.Logger) (review.Completer, error) {
	if !deps.Serve {
		return deps.NewLLM
	}
	return func(cfg *config.Config, log *slog.Logger) (review.Completer, error) {
		if cfg == nil || !cfg.Secrets.LLMAPIKey.IsSet() {
			return missingKeyCompleter{}, nil
		}
		return deps.NewLLM(cfg, log)
	}
}

type missingKeyCompleter struct{}

func (missingKeyCompleter) Complete(context.Context, string, string) (*llm.Response, error) {
	return nil, tools.MissingLLMKey()
}
