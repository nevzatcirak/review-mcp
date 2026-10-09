package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/describe"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// prDescribeDescription is the tool description of pr_describe (v2 spec
// §3.7). It says that publishing writes to the pull request and that it is
// refused until WP-2d.
const prDescribeDescription = "Describes a pull request with the configured LLM: a title, the change types, a short summary and a walkthrough of the changed files. By default it only reads: nothing is written to the pull request. Publishing (publish=true) writes to the pull request (a comment, or a marked region of its description) and is not available yet: it is refused. The PR's title, description, branch names, commit messages and diff are sent to the configured LLM endpoint." + describePartialSentence

// describePartialSentence ends the pr_describe description (X-18, Y-7).
const describePartialSentence = " If the result says the description is partial, tell the user how many files were not described and never present the walkthrough as covering those files."

// prDescribeJobSentence ends the pr_describe description in stdio mode (see
// prReviewJobSentence).
const prDescribeJobSentence = " A description that takes longer than wait_seconds answers with a job_id instead: call job_result for the result."

type prDescribeInput struct {
	PRURL          string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea or Bitbucket Server host"`
	OutputLanguage string `json:"output_language,omitempty" jsonschema:"locale code for the description text, for example en-US or tr-TR; replaces output.language for this call"`
	Publish        bool   `json:"publish,omitempty" jsonschema:"write the description to the pull request (default false); not available yet: true is refused"`
	PublishMode    string `json:"publish_mode,omitempty" jsonschema:"where publish writes: comment (default) or description (a marked region of the PR description)"`
	UpdateTitle    bool   `json:"update_title,omitempty" jsonschema:"with publish and publish_mode=description, also replace the PR title with the generated one (default false)"`
	WaitSeconds    *int   `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for the result before answering with a job_id for job_result, 0 to 600; replaces llm.wait_seconds (default 45) for this call; ignored in serve mode"`
}

func registerPRDescribe(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPRDescribe,
		Description: toolDescription(deps, prDescribeDescription, prDescribeJobSentence),
		// Read-only: publish=false is the default and, until WP-2d, the only
		// path that runs (publish=true is refused before any request).
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
		OutputSchema: describeOutputSchema(deps),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prDescribeInput) (*mcp.CallToolResult, any, error) {
		log := logger(deps)
		// The degraded configuration check comes first: nothing is built and
		// nothing touches the network.
		sc, err := callScope(ctx, deps, req, true)
		if err != nil {
			return nil, nil, err
		}
		if deps.NewLLM == nil {
			sc.release()
			return nil, nil, toolError("review-mcp has no LLM wiring; this is a bug")
		}
		// Everything that fails without network I/O fails here, before a
		// job exists (see pr_review), the publish refusal included.
		call, err := tools.PreparePRDescribe(tools.DescribeDeps{
			Config:   sc.cfg,
			Resolver: sc.resolver,
			NewLLM:   sc.newLLM,
			Logger:   log,
		}, tools.PRDescribeArgs{
			PRURL: in.PRURL, OutputLanguage: in.OutputLanguage, Publish: in.Publish,
			PublishMode: in.PublishMode, UpdateTitle: in.UpdateTitle,
		})
		wait := 0
		if err == nil && deps.background() {
			wait, err = tools.WaitSeconds(in.WaitSeconds, sc.cfg)
		}
		if err != nil {
			sc.release()
			msg := tools.UserMessage(err)
			log.Debug("pr_describe failed", "error", msg)
			return nil, nil, toolError(msg)
		}
		return answerCall(ctx, deps, req, toolPRDescribe, sc, wait, func(ctx context.Context, progress func(string)) (jobOutcome, error) {
			res, text, err := call.Run(ctx, progress)
			if err != nil {
				msg := tools.UserMessage(err)
				log.Debug("pr_describe failed", "error", msg)
				return jobOutcome{}, errors.New(msg)
			}
			// Counts only: never the title, the summaries, prompts or PR
			// content.
			log.Debug("pr_describe", "files", len(res.Files), "llm_calls", res.Metadata.LLMCalls,
				"notes", len(res.Notes), "partial", res.Coverage.Partial)
			return jobOutcome{text: text, out: *res}, nil
		})
	})
}

// describeOutputSchema is the declared output schema of pr_describe: the
// result alone in serve mode, the result or the running status in stdio
// mode.
func describeOutputSchema(deps Deps) any {
	if deps.background() {
		return withRunning(describe.ResultSchema())
	}
	return describe.ResultSchema()
}
