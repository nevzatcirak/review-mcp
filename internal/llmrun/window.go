package llmrun

import (
	"context"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
)

// WindowResolver is implemented by *llm.Client: it asks the endpoint for the
// context window to budget for (X-15).
type WindowResolver interface {
	ResolveContextWindow(ctx context.Context) (n int, source string, err error)
}

// SourceConfig is the source of a window taken from llm.context_window.
const SourceConfig = "config"

// ContextWindow is the one accessor for the context window a pipeline
// budgets for (X-15). A set llm.context_window always wins and no request is
// made. Otherwise c, the chat client of the call, resolves it from the
// endpoint (cached per process by the client). A c that cannot resolve (nil,
// or a test double) yields the "set llm.context_window" error. source is
// SourceConfig or the resolver's description.
//
// Pipelines call it after argument validation and URL resolution and before
// any provider request, so a failing probe sends nothing to the provider.
func ContextWindow(ctx context.Context, cfg *config.Config, c any) (n int, source string, err error) {
	if cfg.LLM.ContextWindow > 0 {
		return cfg.LLM.ContextWindow, SourceConfig, nil
	}
	r, ok := c.(WindowResolver)
	if !ok {
		return 0, "", llm.NoContextWindowError()
	}
	return r.ResolveContextWindow(ctx)
}
