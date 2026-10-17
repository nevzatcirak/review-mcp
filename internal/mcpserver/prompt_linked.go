package mcpserver

import (
	"context"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// promptLinked is the MCP prompt of linked pull requests (X-41, v2.3 spec
// V-13a §3.3): review each pull request of a set with the others as related
// context, and publish each review only to its own pull request.
const promptLinked = "review_linked_prs"

// Limits of the pr_urls argument.
const (
	minLinkedURLs = 2
	maxLinkedURLs = 5
)

// Fixed errors of the prompt's arguments. They never echo a value.
const (
	linkedURLsMessage = "pr_urls must hold 2 to 5 different http or https pull request URLs without credentials, " +
		"separated by commas, spaces or new lines"
	linkedPublishMessage = "publish must be true or false"
)

// registerLinkedPrompt registers the review_linked_prs prompt; queued adds
// rateLimitGuidance to its first step (X-37).
func registerLinkedPrompt(s *mcp.Server, deps Deps) {
	queued := rateLimitConfigured(deps.Config)
	s.AddPrompt(&mcp.Prompt{
		Name:  promptLinked,
		Title: "Review linked pull requests",
		Description: "Review 2 to 5 pull requests that belong together (for example a UI change and the backend " +
			"change it depends on), each with the others as related context, and publish each review to its own " +
			"pull request only.",
		Arguments: []*mcp.PromptArgument{
			{Name: "pr_urls", Description: "2 to 5 pull request URLs, separated by commas, spaces or new lines", Required: true},
			{Name: "publish", Description: "true to publish each review to its own pull request (default false)"},
		},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		var args map[string]string
		if req != nil && req.Params != nil {
			args = req.Params.Arguments
		}
		urls, ok := linkedURLs(args["pr_urls"])
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: linkedURLsMessage}
		}
		publish, ok := linkedPublish(args["publish"])
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: linkedPublishMessage}
		}
		return &mcp.GetPromptResult{
			Description: "Review of linked pull requests",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: linkedText(urls, publish, queued)},
			}},
		}, nil
	})
}

// linkedURLs parses pr_urls: 2 to 5 URLs separated by commas, spaces or new
// lines, each checked and written as promptPRURL does (no query or
// fragment).
//
// DESIGN-QUESTION (WP-11c DQ-3): the spec asks for 2 to 5 URLs but does not
// say what a URL given twice is. The same pull request twice would be
// reviewed twice with itself as related context (which pr_review ignores),
// so a repeat after normalisation is refused with the same fixed message.
// Recommendation: keep.
func linkedURLs(raw string) ([]string, bool) {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	if len(fields) < minLinkedURLs || len(fields) > maxLinkedURLs {
		return nil, false
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		u, ok := promptPRURL(f)
		if !ok {
			return nil, false
		}
		for _, seen := range out {
			if seen == u {
				return nil, false
			}
		}
		out = append(out, u)
	}
	return out, true
}

// linkedPublish parses publish: absent or blank is false; otherwise true or
// false, without regard to case or surrounding space.
func linkedPublish(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "false":
		return false, true
	case "true":
		return true, true
	}
	return false, false
}

// linkedText is the prompt's user message. URLs appear only inside JSON
// strings, as in multi_perspective_review.
func linkedText(urls []string, publish, queued bool) string {
	type call struct {
		PRURL   string   `json:"pr_url"`
		Related []string `json:"related"`
		Publish bool     `json:"publish,omitempty"`
	}
	var b strings.Builder
	b.WriteString("Review these " + strconv.Itoa(len(urls)) + " linked pull requests with the review-mcp tools, each with the " +
		"others as related context.\n\n")
	b.WriteString("1. Call pr_review once per pull request, in parallel if the endpoint allows it (for example in " +
		"subagents). ")
	if queued {
		b.WriteString(rateLimitGuidance + " ")
	}
	b.WriteString("Use these arguments:\n")
	for i, u := range urls {
		var rel []string
		for j, o := range urls {
			if j != i {
				rel = append(rel, o)
			}
		}
		b.WriteString("   - " + jsonText(call{PRURL: u, Related: rel, Publish: publish}) + "\n")
	}
	if publish {
		b.WriteString("2. Each call publishes its review to its own pull request only; nothing is published to the " +
			"related pull requests.\n")
	} else {
		b.WriteString("2. Do not publish: the user asked for the reviews only.\n")
	}
	b.WriteString("3. Never merge the reviews across pull requests: report each pull request's review on its own, " +
		"including the findings it lists in related pull requests, and say for each whether its related pull " +
		"requests were read in full (coverage.related).\n")
	b.WriteString("4. If a run said its review is partial, say how many files were not reviewed and never state that " +
		"those files have no issues.\n")
	return b.String()
}
