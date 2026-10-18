package llmrun

import (
	"context"

	"github.com/nevzatcirak/review-mcp/internal/llm"
)

// NextCompleter is a model client with a fallback chain (*llm.Chain): it
// can send a prompt to the next model of the chain when the answer of the
// current one stays unusable after the same-model re-ask (X-38, v2.1 spec
// V-6 §3.2 item 3).
type NextCompleter interface {
	CompleteNext(ctx context.Context, system, user string) (*llm.Response, bool, error)
}

// CompleteNext sends the original prompt once to the next model of c's
// chain. ok is false when c has no chain or no later model took the prompt;
// err is set only when ctx ended.
func CompleteNext(ctx context.Context, c any, system, user string) (resp *llm.Response, ok bool, err error) {
	n, isChain := c.(NextCompleter)
	if !isChain {
		return nil, false, nil
	}
	return n.CompleteNext(ctx, system, user)
}

// ToolRefuser is the part of a model chain that knows whether the model a
// run is on refuses tool calls (X-43): *llm.Chain implements it.
type ToolRefuser interface {
	ToolsRefused() bool
}

// ToolsRefused reports whether c is known to refuse tool calls; a
// completer that does not say is not.
func ToolsRefused(c any) bool {
	r, ok := c.(ToolRefuser)
	return ok && r.ToolsRefused()
}
