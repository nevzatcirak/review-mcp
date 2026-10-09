package mcpserver

import (
	"context"
	"errors"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/describe"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// prReviewDescription is the tool description from spec P4 §6.1.
//
// In stdio mode the description gains prReviewJobSentence (X-16).
const prReviewDescription = "Reviews a pull request with the configured LLM and returns a structured review (key issues, effort, tests, security, performance) with code excerpts. Set publish=true to also post it: one overview comment that later runs edit in place, and the findings on changed lines as inline comments. The PR's title, description, existing comments and diff are sent to the configured LLM endpoint." + partialSentence

// partialSentence ends the pr_review, pr_ask and job_result descriptions
// (X-18). The job sentence of stdio mode follows it.
const partialSentence = " If the result says the review is partial, tell the user how many files were not reviewed and never state that those files have no issues."

// prReviewJobSentence ends the pr_review description in stdio mode, where a
// slow review answers with a job id (P8 spec §2.4).
const prReviewJobSentence = " A review that takes longer than wait_seconds answers with a job_id instead: call job_result for the result. The review keeps running and, with publish=true, still posts its comments even if job_result is never called."

type prReviewInput struct {
	PRURL             string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea or Bitbucket Server host"`
	ExtraInstructions string `json:"extra_instructions,omitempty" jsonschema:"extra review instructions for the model; replaces review.extra_instructions for this call"`
	OutputLanguage    string `json:"output_language,omitempty" jsonschema:"locale code for the review text, for example en-US or tr-TR; replaces output.language for this call"`
	MaxFindings       *int   `json:"max_findings,omitempty" jsonschema:"most key issues to return, 1 to 20; replaces review.max_findings for this call"`
	Publish           bool   `json:"publish,omitempty" jsonschema:"also post the review: the overview comment, and with inline_findings the findings on changed lines as inline comments (default false)"`
	InlineFindings    *bool  `json:"inline_findings,omitempty" jsonschema:"with publish, post each finding that falls on a changed line as an inline comment; replaces review.inline_findings for this call"`
	WaitSeconds       *int   `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for the result before answering with a job_id for job_result, 0 to 600; replaces llm.wait_seconds (default 45) for this call; ignored in serve mode"`
}

// progressTotal is the number of stages of a review or an answer.
const progressTotal = 4

// toolDescription returns desc, plus jobSentence when the tool may answer
// with a job id.
func toolDescription(deps Deps, desc, jobSentence string) string {
	if deps.background() {
		return desc + jobSentence
	}
	return desc
}

// progressFunc returns the stage callback of one call, or nil when the client
// sent no progress token (MCP progress notifications need the token from the
// request's _meta). A failed notification is ignored: progress is a courtesy.
func progressFunc(ctx context.Context, req *mcp.CallToolRequest, log *slog.Logger, tool string) func(string) {
	token := req.Params.GetProgressToken()
	if token == nil || req.Session == nil {
		return nil
	}
	step, extra, parts, summary := 0, 0, 0, 0
	return func(stage string) {
		step++
		// The repository-context fetch is one more stage, reported only
		// when it happens (X-22). A review in N parts reports one "calling
		// model (part I of N)" stage per part instead of one "calling
		// model" (X-19), so its total is the other stages plus N.
		if stage == review.StageRepoContext {
			extra = 1
		}
		// The reduce call of a description in parts (Y-6) is one more
		// stage after the parts.
		if stage == describe.StageSummarizing {
			summary = 1
		}
		if n, ok := review.StageParts(stage); ok {
			parts = max(parts, n-1)
		}
		total := progressTotal + extra + parts + summary
		err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token, Message: stage, Progress: float64(step), Total: float64(total),
		})
		if err != nil {
			log.Debug(tool+": progress notification not sent", "stage", stage)
		}
	}
}

func registerPRReview(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPRReview,
		Description: toolDescription(deps, prReviewDescription, prReviewJobSentence),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
		OutputSchema: reviewOutputSchema(deps),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prReviewInput) (*mcp.CallToolResult, any, error) {
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
		// job exists: the arguments, the LLM client, the URL resolution and
		// (serve) the credentials, then wait_seconds.
		call, err := tools.PreparePRReview(tools.ReviewDeps{
			Config:   sc.cfg,
			Resolver: sc.resolver,
			NewLLM:   sc.newLLM,
			Logger:   log,
		}, tools.PRReviewArgs{
			PRURL: in.PRURL, ExtraInstructions: in.ExtraInstructions, OutputLanguage: in.OutputLanguage,
			MaxFindings: in.MaxFindings, Publish: in.Publish, InlineFindings: in.InlineFindings,
		})
		wait := 0
		if err == nil && deps.background() {
			wait, err = tools.WaitSeconds(in.WaitSeconds, sc.cfg)
		}
		if err != nil {
			sc.release()
			msg := tools.UserMessage(err)
			log.Debug("pr_review failed", "error", msg)
			return nil, nil, toolError(msg)
		}
		return answerCall(ctx, deps, req, toolPRReview, sc, wait, func(ctx context.Context, progress func(string)) (jobOutcome, error) {
			res, text, err := call.Run(ctx, progress)
			if err != nil {
				msg := tools.UserMessage(err)
				log.Debug("pr_review failed", "error", msg)
				return jobOutcome{}, errors.New(msg)
			}
			// Counts and the redacted URL only: never the review text,
			// prompts or PR content.
			log.Debug("pr_review", "url", res.PR.URL, "findings", len(res.Review.KeyIssuesToReview),
				"llm_calls", res.Metadata.LLMCalls, "notes", len(res.Notes), "published", res.Publish != nil && res.Publish.Published)
			return jobOutcome{text: text, out: *res}, nil
		})
	})
}
