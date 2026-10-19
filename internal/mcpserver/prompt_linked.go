package mcpserver

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/related"
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
	// linkedReposMessage is the refusal of repo_urls: every pull request's
	// call gets the other pull requests and the repositories as related, 4
	// at most (X-41).
	linkedReposMessage = "repo_urls must hold different http or https repository URLs without credentials, " +
		"separated by commas, spaces or new lines, and with pr_urls at most 5 in all"
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
			{Name: "repo_urls", Description: "repositories that may use what the pull requests change (a repository URL, " +
				"optionally with a branch), given to every review as related context and never reviewed themselves; with " +
				"pr_urls at most 5 URLs in all; separated by commas, spaces or new lines"},
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
		repos, ok := linkedRepos(args["repo_urls"], len(urls))
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: linkedReposMessage}
		}
		publish, ok := linkedPublish(args["publish"])
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: linkedPublishMessage}
		}
		return &mcp.GetPromptResult{
			Description: "Review of linked pull requests",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: linkedText(urls, repos, publish, queued)},
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

// linkedRepos parses repo_urls (V-13b spec §4 item 3): none, or different
// http or https repository URLs, each normalised as the related argument
// would (related.Validate: query and fragment dropped, a Bitbucket Server
// branch kept), with the pull requests at most related.MaxItems+1 in all,
// so that every pull request's call has at most related.MaxItems related
// items.
//
// DESIGN-QUESTION (WP-13d DQ-2): the spec says the prompt "accepts
// repository URLs among the set". A URL cannot be told from a pull
// request's without the providers (an opaque URL is a valid pr_urls entry
// today), and the prompt resolves nothing, so repositories get their own
// argument, repo_urls, and pr_urls keeps its meaning. Recommendation: keep.
func linkedRepos(raw string, prs int) ([]string, bool) {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	if len(fields) == 0 {
		return nil, true
	}
	if prs+len(fields) > related.MaxItems+1 {
		return nil, false
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) > maxPromptURLBytes {
			return nil, false
		}
		norm, err := related.Validate([]string{f}, false)
		if err != nil || len(norm) != 1 || slices.Contains(out, norm[0]) {
			return nil, false
		}
		out = append(out, norm[0])
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
func linkedText(urls, repos []string, publish, queued bool) string {
	type call struct {
		PRURL   string   `json:"pr_url"`
		Related []string `json:"related"`
		Publish bool     `json:"publish,omitempty"`
	}
	var b strings.Builder
	b.WriteString("Review these " + strconv.Itoa(len(urls)) + " linked pull requests with the review-mcp tools, each with the " +
		"others as related context.\n\n")
	if len(repos) > 0 {
		b.WriteString("The repositories below are related context only (they may use what the pull requests change). " +
			"Pass them in related as given and never review them, call pr_review only for the pull requests.\n\n")
	}
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
		rel = append(rel, repos...)
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
	if len(repos) > 0 {
		b.WriteString("   For each related repository say whether it was searched and at which commit (coverage.related); " +
			"no_use_found means only that text search found no use of the changed identifiers, never that nothing is affected.\n")
	}
	b.WriteString("4. If a run said its review is partial, say how many files were not reviewed and never state that " +
		"those files have no issues.\n")
	return b.String()
}
