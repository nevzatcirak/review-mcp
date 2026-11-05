package mcpserver

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// Tool descriptions of v2.8 spec V-15 §2.4, verbatim.
const (
	prListReviewRequestsDescription = "Lists the open pull requests that ask you (the token's user) for a review on the configured providers, newest update first; with include_assigned also those assigned to you. Use an item's url with pr_review, pr_info or pr_ask. Read-only; no LLM call."

	prListAuthoredDescription = "Lists the open pull requests you (the token's user) opened on the configured providers, newest update first. Use an item's url with pr_info to see their reviewers and CI. Read-only; no LLM call."

	// listGuidance is the sentence of the server instructions after the
	// first line (spec §2.4).
	listGuidance = "To find pull requests to work on, call pr_list_review_requests or pr_list_authored."
)

type prListReviewRequestsInput struct {
	Provider        string `json:"provider,omitempty" jsonschema:"restrict the list to one provider: gitea, bitbucket_server, github or gitlab; default every configured provider"`
	Limit           *int   `json:"limit,omitempty" jsonschema:"most pull requests to list, 1 to 100 (default 30); a value outside the range is refused"`
	IncludeAssigned bool   `json:"include_assigned,omitempty" jsonschema:"also list pull requests assigned to you (default false); Bitbucket Server has no assignees"`
}

type prListAuthoredInput struct {
	Provider string `json:"provider,omitempty" jsonschema:"restrict the list to one provider: gitea, bitbucket_server, github or gitlab; default every configured provider"`
	Limit    *int   `json:"limit,omitempty" jsonschema:"most pull requests to list, 1 to 100 (default 30); a value outside the range is refused"`
}

// listKinds are the values of the provider argument.
var listKinds = []any{string(provider.KindGitea), string(provider.KindBitbucketServer), string(provider.KindGitHub),
	string(provider.KindGitLab)}

// listInputSchema is the inferred input schema of In with the provider
// enum.
func listInputSchema[In any]() *jsonschema.Schema {
	s, err := jsonschema.For[In](&jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("pull request list input schema: %v", err)) // a programming error, caught by every test
	}
	s.Properties["provider"].Enum = listKinds
	return s
}

// listOutputSchema is the inferred output schema with the enums of role,
// provider kind, status and roles.
func listOutputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[tools.PRListResult](&jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("pull request list output schema: %v", err)) // a programming error, caught by every test
	}
	s.Properties["role"].Enum = []any{tools.ListRoleReviewRequests, tools.ListRoleAuthored}
	items := s.Properties["items"].Items
	items.Properties["provider"].Enum = listKinds
	items.Properties["roles"].Items.Enum = []any{string(provider.ListReviewer), string(provider.ListAssignee),
		string(provider.ListAuthor)}
	st := s.Properties["providers"].Items
	st.Properties["kind"].Enum = listKinds
	st.Properties["status"].Enum = []any{tools.ListStatusOK, tools.ListStatusFailed, tools.ListStatusUnsupported,
		tools.ListStatusNoToken}
	return s
}

// registerPRList registers one list tool. args turns the call's arguments
// into the engine's.
func registerPRList[In any](s *mcp.Server, deps Deps, name, description string, args func(In) tools.PRListArgs) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:         name,
		Description:  description,
		InputSchema:  listInputSchema[In](),
		OutputSchema: listOutputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, tools.PRListResult, error) {
		var zero tools.PRListResult
		sc, err := callScope(ctx, deps, req, false)
		if err != nil {
			return nil, zero, err
		}
		defer sc.release()
		res, err := tools.PRList(ctx, tools.ListSourceOf(sc.resolver), args(in), logger(deps))
		if err != nil {
			msg := tools.UserMessage(err)
			logger(deps).Debug(name+" failed", "error", msg)
			return nil, zero, toolError(msg)
		}
		// The engine has logged the counts; never a title, a name or a URL.
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: tools.RenderPRListMarkdown(res)}},
		}, res, nil
	})
}

func registerPRListReviewRequests(s *mcp.Server, deps Deps) {
	registerPRList(s, deps, "pr_list_review_requests", prListReviewRequestsDescription,
		func(in prListReviewRequestsInput) tools.PRListArgs {
			return tools.PRListArgs{Role: tools.ListRoleReviewRequests, Provider: in.Provider, Limit: in.Limit,
				IncludeAssigned: in.IncludeAssigned}
		})
}

func registerPRListAuthored(s *mcp.Server, deps Deps) {
	registerPRList(s, deps, "pr_list_authored", prListAuthoredDescription, func(in prListAuthoredInput) tools.PRListArgs {
		return tools.PRListArgs{Role: tools.ListRoleAuthored, Provider: in.Provider, Limit: in.Limit}
	})
}
