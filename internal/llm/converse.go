package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Tool calling (X-43, V-8 spec §1): a conversation in which the model may
// call the caller's tools. Complete stays the single-pass path; Converse
// sends one turn of a conversation through the same retries and limiter.

// MaxToolArgumentBytes caps the arguments of one tool call (§1 item 5).
const MaxToolArgumentBytes = 4 << 10

// Message roles of a conversation.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ToolChoice tells the model whether it may call tools.
type ToolChoice string

// Tool choices.
const (
	ToolChoiceAuto ToolChoice = "auto"
	ToolChoiceNone ToolChoice = "none"
)

// Tool is a function the model may call. Parameters is its JSON Schema.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// ToolCall is a call the model made.
type ToolCall struct {
	// ID is the endpoint's call id, echoed in the tool result.
	ID   string
	Name string
	// Arguments are the arguments as the model sent them, sent back
	// unchanged in the conversation.
	Arguments string
	// Args is Arguments as a JSON object, nil when they are not one or
	// exceed MaxToolArgumentBytes; such a call is not run (§1 item 5).
	Args json.RawMessage
}

// Message is one message of a conversation after the system text: a user
// message, an assistant message (its text and tool calls) or a tool result
// (ToolCallID and Content).
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// Conversation is the state Converse sends: the system text, the ordered
// messages, the tools and the tool choice.
type Conversation struct {
	System     string
	Messages   []Message
	Tools      []Tool
	ToolChoice ToolChoice
}

// Turn is one answer of the model in a conversation.
type Turn struct {
	Text      string
	ToolCalls []ToolCall
	// Truncated is true when finish_reason was "length".
	Truncated bool
	Usage     Usage
}

// Message returns the turn as the assistant message to append to the
// conversation.
func (t Turn) Message() Message {
	return Message{Role: RoleAssistant, Content: t.Text, ToolCalls: t.ToolCalls}
}

// ToolResult returns the tool message answering call id with content.
func ToolResult(id, content string) Message {
	return Message{Role: RoleTool, ToolCallID: id, Content: content}
}

// started reports whether the model has answered in the conversation: a
// later turn must stay on the model that answered (§1 item 3).
func (cv Conversation) started() bool {
	for _, m := range cv.Messages {
		if m.Role == RoleAssistant {
			return true
		}
	}
	return false
}

// estimate is the conversation's prompt estimate under factor
// (llm.token_estimate_factor): every text the request carries.
func (cv Conversation) estimate(factor float64) int {
	n := tokens.Estimate(cv.System, factor)
	for _, m := range cv.Messages {
		n += tokens.Estimate(m.Content, factor)
		for _, tc := range m.ToolCalls {
			n += tokens.Estimate(tc.Name, factor) + tokens.Estimate(tc.Arguments, factor)
		}
	}
	for _, t := range cv.Tools {
		n += tokens.Estimate(t.Name, factor) + tokens.Estimate(t.Description, factor) +
			tokens.Estimate(string(t.Parameters), factor)
	}
	return n
}

// Converse sends the conversation and returns the model's next turn. It
// goes through the same retries and rate limiter as Complete (§1 item 3);
// the request differs from Complete's only by the further messages, the
// tools and the tool choice.
func (c *Client) Converse(ctx context.Context, conv Conversation) (Turn, error) {
	var out Turn
	err := c.do(ctx, exchange{
		build: func() ([]byte, error) { return c.buildConversation(conv) },
		parse: func(data []byte) (outcome, *Error) {
			t, le := parseTurn(data)
			if le != nil {
				return outcome{}, le
			}
			out = t
			return outcome{usage: t.Usage, truncated: t.Truncated, attrs: []any{"tool_calls", len(t.ToolCalls)}}, nil
		},
	})
	if err != nil {
		return Turn{}, err
	}
	return out, nil
}

// buildConversation builds a fresh body for conv, like buildRequest.
//
// DESIGN-QUESTION (WP-12a DQ-2): the tool choice is sent only with tools,
// since endpoints refuse a tool_choice without tools; the last call of a
// loop sends the tools with "none", because a conversation that holds tool
// calls must still declare them. Recommendation: keep.
func (c *Client) buildConversation(conv Conversation) ([]byte, error) {
	r := chatRequest{
		Model:    c.cfg.Model,
		Messages: make([]message, 0, len(conv.Messages)+1),
	}
	r.Messages = append(r.Messages, message{Role: "system", Content: conv.System})
	for _, m := range conv.Messages {
		wm := message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, toolCall{ID: tc.ID, Type: "function",
				Function: toolCallFunction{Name: tc.Name, Arguments: tc.Arguments}})
		}
		r.Messages = append(r.Messages, wm)
	}
	for _, t := range conv.Tools {
		r.Tools = append(r.Tools, tool{Type: "function",
			Function: toolFunction(t)})
	}
	if len(r.Tools) > 0 {
		r.ToolChoice = string(conv.ToolChoice)
	}
	c.sampling(&r)
	return json.Marshal(r)
}

// parseTurn reads an answer of a conversation. Unlike Complete's, its text
// may be empty when it calls tools.
func parseTurn(data []byte) (Turn, *Error) {
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		// The decoder error is not propagated: syntax errors quote bytes.
		return Turn{}, &Error{Class: ClassProtocol, Detail: "response is not valid JSON of the expected shape"}
	}
	if len(cr.Choices) == 0 {
		return Turn{}, &Error{Class: ClassProtocol, Detail: "response has no choices"}
	}
	ch := cr.Choices[0]
	t := Turn{
		Text:      contentText(ch.Message.Content),
		ToolCalls: parseToolCalls(ch.Message.ToolCalls),
		Truncated: ch.FinishReason == "length",
		Usage: Usage{
			PromptTokens:     cr.Usage.PromptTokens,
			CompletionTokens: cr.Usage.CompletionTokens,
			TotalTokens:      cr.Usage.TotalTokens,
		},
	}
	if len(t.ToolCalls) == 0 && strings.TrimSpace(t.Text) == "" {
		return Turn{}, &Error{Class: ClassProtocol, Detail: "response content is empty"}
	}
	return t, nil
}

// parseToolCalls reads the tool calls of an answer. A call without an id
// or a function name, or of another type, cannot be answered and is
// dropped; a list that is not a list yields none.
func parseToolCalls(raw json.RawMessage) []ToolCall {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []ToolCall
	for _, it := range items {
		var w struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		}
		if json.Unmarshal(it, &w) != nil || w.ID == "" || w.Function.Name == "" ||
			(w.Type != "" && w.Type != "function") {
			continue
		}
		args := argumentsText(w.Function.Arguments)
		out = append(out, ToolCall{ID: w.ID, Name: w.Function.Name, Arguments: args, Args: argumentsObject(args)})
	}
	return out
}

// argumentsText is the arguments as a string: the content of a JSON string
// (the format's form), or an object as it was sent (a form some endpoints
// use).
func argumentsText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		return s
	}
	return string(raw)
}

// argumentsObject returns the arguments as a JSON object, or nil when they
// are not one or are too long.
//
// DESIGN-QUESTION (WP-12a DQ-3): empty arguments are read as {} (endpoints
// send "" for a call without arguments), so a tool whose parameters are all
// optional can be called that way; the tool then judges a missing required
// parameter itself. Recommendation: keep.
func argumentsObject(s string) json.RawMessage {
	if len(s) > MaxToolArgumentBytes {
		return nil
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return json.RawMessage("{}")
	}
	if t[0] != '{' || !json.Valid([]byte(t)) {
		return nil
	}
	return json.RawMessage(t)
}
