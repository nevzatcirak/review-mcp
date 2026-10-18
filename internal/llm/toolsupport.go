package llm

import (
	"errors"
	"strings"
)

// Endpoints that refuse tools (V-8 spec §1 item 4, owner default 5). There
// is no probe: the first request of a process to an endpoint and model
// carries the tools. When the endpoint refuses them, or its answer shows
// that the model ignores them, the process remembers "no tools" for that
// endpoint (the X-37 key) and model, and later conversations there make no
// tool request at all.

// ToolSupport is what the process knows of an endpoint and model's tool
// calling.
type ToolSupport int

// Tool support states.
const (
	ToolsUnknown ToolSupport = iota
	ToolsAccepted
	ToolsRefused
)

// String is the state as server_info names it.
func (s ToolSupport) String() string {
	switch s {
	case ToolsAccepted:
		return "accepted"
	case ToolsRefused:
		return "refused"
	}
	return "not known yet"
}

// ErrNoTools is Converse's error when the endpoint and model do not accept
// tool calls: they refused them now or earlier in the process. The caller
// continues single-pass with NoToolsNote; it is never a run's error.
var ErrNoTools = errors.New("llm: the endpoint does not accept tool calls")

// NoToolsNote is the note of a run that could not use tools (§1 item 4).
const NoToolsNote = "The LLM endpoint does not accept tool calls, so the review used the diff and repository context without reading further files."

// reasonNoTools is the internal reason of a failure that shows the
// endpoint does not accept tools; Converse turns it into ErrNoTools.
const reasonNoTools = "no_tools"

// mentionsTools is a case-insensitive in-memory match on "tool" or
// "function" in an error body; the body is never logged or returned.
//
// DESIGN-QUESTION (WP-12a DQ-5): a 400 whose body mentions the context
// length is that error even when it also mentions tools ("... including
// tool definitions"): the conversation is too long, not refused. A 400 or
// 422 that names neither stays the run's error, as the spec's rule reads.
// Recommendation: keep.
func mentionsTools(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "tool") || strings.Contains(s, "function")
}

// toolSupport returns what the process knows of model's tool calling on
// the limiter's endpoint.
func (l *limiter) toolSupport(model string) ToolSupport {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tools[model]
}

// setToolSupport records s for model on the limiter's endpoint.
func (l *limiter) setToolSupport(model string, s ToolSupport) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tools == nil {
		l.tools = map[string]ToolSupport{}
	}
	l.tools[model] = s
}

// ToolSupport returns what the process knows of the client's endpoint and
// model's tool calling.
func (c *Client) ToolSupport() ToolSupport {
	return c.limiter.toolSupport(c.cfg.Model)
}

// ToolsRefused reports whether the model the run is on is known not to
// accept tool calls; a run then makes no tool request (§2.2 item 1).
func (c *Chain) ToolsRefused() bool {
	c.mu.Lock()
	m := c.models[c.cur]
	c.mu.Unlock()
	return m.ToolSupport() == ToolsRefused
}
