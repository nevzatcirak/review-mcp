package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/improve"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// prImproveDescription is the tool description of pr_improve (v2 spec
// §1.8). It says that publishing is refused until WP-2h.
const prImproveDescription = "Suggests code changes for a pull request with the configured LLM: each suggestion has the existing and the improved code, a label and a self-review score from 0 to 10; suggestions scoring below improve.min_score are dropped and counted in a note, and suggestions the self-review could not score are kept and marked unscored. By default it only reads: nothing is written to the pull request. Publishing (publish=true) is not available yet: it is refused. The PR's title, description, branch names, existing comments and diff are sent to the configured LLM endpoint." + partialSentence

// prImproveJobSentence ends the pr_improve description in stdio mode (see
// prReviewJobSentence).
const prImproveJobSentence = " A run that takes longer than wait_seconds answers with a job_id instead: call job_result for the result."

type prImproveInput struct {
	PRURL          string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea or Bitbucket Server host"`
	OutputLanguage string `json:"output_language,omitempty" jsonschema:"locale code for the suggestion text, for example en-US or tr-TR; replaces output.language for this call"`
	Publish        bool   `json:"publish,omitempty" jsonschema:"post the suggestions to the pull request (default false); not available yet: true is refused"`
	WaitSeconds    *int   `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for the result before answering with a job_id for job_result, 0 to 600; replaces llm.wait_seconds (default 45) for this call; ignored in serve mode"`
}

func registerPRImprove(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPRImprove,
		Description: toolDescription(deps, prImproveDescription, prImproveJobSentence),
		// Read-only: publish=false is the default and, until WP-2h, the only
		// path that runs (publish=true is refused before any request).
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
		OutputSchema: improveOutputSchema(deps),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prImproveInput) (*mcp.CallToolResult, any, error) {
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
		call, err := tools.PreparePRImprove(tools.ImproveDeps{
			Config:   sc.cfg,
			Resolver: sc.resolver,
			NewLLM:   sc.newLLM,
			Logger:   log,
		}, tools.PRImproveArgs{PRURL: in.PRURL, OutputLanguage: in.OutputLanguage, Publish: in.Publish})
		wait := 0
		if err == nil && deps.background() {
			wait, err = tools.WaitSeconds(in.WaitSeconds, sc.cfg)
		}
		if err != nil {
			sc.release()
			msg := tools.UserMessage(err)
			log.Debug("pr_improve failed", "error", msg)
			return nil, nil, toolError(msg)
		}
		return answerCall(ctx, deps, req, toolPRImprove, sc, wait, func(ctx context.Context, progress func(string)) (jobOutcome, error) {
			res, text, err := call.Run(ctx, progress)
			if err != nil {
				msg := tools.UserMessage(err)
				log.Debug("pr_improve failed", "error", msg)
				return jobOutcome{}, errors.New(msg)
			}
			// Counts only: never the suggestions, prompts or PR content.
			log.Debug("pr_improve", "suggestions", len(res.Suggestions), "llm_calls", res.Metadata.LLMCalls,
				"notes", len(res.Notes), "partial", res.Coverage.Partial)
			return jobOutcome{text: text, out: *res}, nil
		})
	})
}

// improveOutputSchema is the declared output schema of pr_improve: the
// result alone in serve mode, the result or the running status in stdio
// mode.
func improveOutputSchema(deps Deps) any {
	if deps.background() {
		return withRunning(improve.ResultSchema())
	}
	return improve.ResultSchema()
}
