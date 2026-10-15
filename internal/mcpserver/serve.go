package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"

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

// Defaults of the gate when no configuration is present (the
// serve.max_concurrent_calls and serve.max_queued_calls defaults).
const (
	defaultConcurrentCalls = 4
	defaultQueuedCalls     = 32
)

// concurrencyGate limits concurrent tool calls to
// Config.Serve.MaxConcurrentCalls (P6 §1.4 step 6). Other methods
// (initialize, tools/list, ping, ...) are not counted. When every slot is
// taken the call waits for one, in arrival order, for as long as its context
// lives; only when Config.Serve.MaxQueuedCalls calls already wait (0: none
// may) does it fail at once with server_busy and not run (v2.1 spec V-6
// §6.3). The gate belongs to the one server New builds, so it is shared by
// every request of the process and holds no request data.
//
// DESIGN-QUESTION (WP-8e DQ-2): a call whose context ends while it waits
// leaves the queue and returns the context's error, not a tool result: its
// caller has gone, and a slot that was handed to it in the same instant goes
// to the next call. server_info is a tool call and waits like the others
// (before WP-8e it was refused with server_busy when the gate was full).
// Recommendation: keep.
func concurrencyGate(deps Deps) mcp.Middleware {
	n, queued := defaultConcurrentCalls, defaultQueuedCalls
	if deps.Config != nil {
		if deps.Config.Serve.MaxConcurrentCalls > 0 {
			n = deps.Config.Serve.MaxConcurrentCalls
		}
		queued = deps.Config.Serve.MaxQueuedCalls
	}
	gate := newSlotGate(n, queued)
	log := logger(deps)
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodCallTool {
				return next(ctx, method, req)
			}
			switch err := gate.acquire(ctx, func(waiting int) {
				log.Debug("tool call waits for a free slot", "waiting_calls", waiting, "max_queued_calls", queued)
			}); {
			case err == nil:
				defer gate.release()
				return next(ctx, method, req)
			case errors.Is(err, errGateFull):
				log.Debug("tool call rejected: every slot is taken and the queue is full",
					"max_concurrent_calls", n, "max_queued_calls", queued)
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: tools.UserMessage(tools.ErrServerBusy)}},
				}, nil
			default:
				log.Debug("tool call left the queue: its context ended")
				return nil, err
			}
		}
	}
}

// errGateFull is acquire's refusal: every slot is taken and the queue is
// full.
var errGateFull = errors.New("every slot is taken and the queue is full")

// slotGate is a counting semaphore whose waiters are served first come,
// first served and whose queue is bounded.
type slotGate struct {
	mu      sync.Mutex
	slots   int
	maxWait int
	running int
	// waiters hold the queued calls in arrival order; a closed channel hands
	// its call the slot of the call that released it.
	waiters []chan struct{}
}

func newSlotGate(slots, maxWait int) *slotGate {
	return &slotGate{slots: slots, maxWait: maxWait}
}

// acquire takes a slot, waiting for one in arrival order; onWait (when not
// nil) is called with the number of waiting calls, this one included, once
// the call is queued. It fails with errGateFull when no slot is free and
// maxWait calls already wait, and with ctx's error when ctx ends while it
// waits.
func (g *slotGate) acquire(ctx context.Context, onWait func(waiting int)) error {
	g.mu.Lock()
	if g.running < g.slots && len(g.waiters) == 0 {
		g.running++
		g.mu.Unlock()
		return nil
	}
	if len(g.waiters) >= g.maxWait {
		g.mu.Unlock()
		return errGateFull
	}
	ch := make(chan struct{})
	g.waiters = append(g.waiters, ch)
	waiting := len(g.waiters)
	g.mu.Unlock()
	if onWait != nil {
		onWait(waiting)
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		for i, w := range g.waiters {
			if w == ch {
				g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
				g.mu.Unlock()
				return ctx.Err()
			}
		}
		g.mu.Unlock()
		// The slot was handed over as the context ended: pass it on.
		g.release()
		return ctx.Err()
	}
}

// release gives the slot to the longest waiting call, or frees it.
func (g *slotGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.waiters) > 0 {
		close(g.waiters[0])
		g.waiters = g.waiters[1:]
		return
	}
	g.running--
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

// ResolveContextWindow fails like Complete: without a key there is no probe.
func (missingKeyCompleter) ResolveContextWindow(context.Context) (int, string, error) {
	return 0, "", tools.MissingLLMKey()
}
