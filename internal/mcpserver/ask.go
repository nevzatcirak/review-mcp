package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// prAskDescription is the tool description from spec P5 §2.1.
const prAskDescription = "Answers a question about a pull request using the configured LLM, grounded in the PR's title, description and diff. Set publish=true to also post the question and answer as a PR comment. The PR content and the question are sent to the configured LLM endpoint." + partialSentence

// prAskJobSentence ends the pr_ask description in stdio mode (see
// prReviewJobSentence).
const prAskJobSentence = " An answer that takes longer than wait_seconds answers with a job_id instead: call job_result for the result. The answer keeps running and, with publish=true, still posts its comment even if job_result is never called."

type prAskInput struct {
	PRURL             string `json:"pr_url" jsonschema:"URL of the pull request, on a configured Gitea or Bitbucket Server host"`
	Question          string `json:"question" jsonschema:"the question to answer about the pull request, at most 8000 characters"`
	ExtraInstructions string `json:"extra_instructions,omitempty" jsonschema:"extra instructions for the model; replaces ask.extra_instructions for this call"`
	OutputLanguage    string `json:"output_language,omitempty" jsonschema:"locale code for the answer, for example en-US or tr-TR; replaces output.language for this call"`
	Publish           bool   `json:"publish,omitempty" jsonschema:"also post the question and answer as a PR comment (default false)"`
	WaitSeconds       *int   `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for the result before answering with a job_id for job_result, 0 to 600; replaces llm.wait_seconds (default 45) for this call; ignored in serve mode"`
}

func registerPRAsk(s *mcp.Server, deps Deps) {
	f, tr := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolPRAsk,
		Description: toolDescription(deps, prAskDescription, prAskJobSentence),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			DestructiveHint: &f,
			OpenWorldHint:   &tr,
		},
		// The output schema is the one the SDK infers from ask.Result, the
		// structured content, declared explicitly so that stdio mode can add
		// the running status.
		OutputSchema: askOutputSchema(deps),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in prAskInput) (*mcp.CallToolResult, any, error) {
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
		// job exists (see pr_review).
		call, err := tools.PreparePRAsk(tools.AskDeps{
			Config:   sc.cfg,
			Resolver: sc.resolver,
			NewLLM:   sc.newLLM,
			Logger:   log,
		}, tools.PRAskArgs{
			PRURL: in.PRURL, Question: in.Question, ExtraInstructions: in.ExtraInstructions,
			OutputLanguage: in.OutputLanguage, Publish: in.Publish,
		})
		wait := 0
		if err == nil && deps.background() {
			wait, err = tools.WaitSeconds(in.WaitSeconds, sc.cfg)
		}
		if err != nil {
			sc.release()
			msg := tools.UserMessage(err)
			log.Debug("pr_ask failed", "error", msg)
			return nil, nil, toolError(msg)
		}
		return answerCall(ctx, deps, req, toolPRAsk, sc, wait, func(ctx context.Context, progress func(string)) (jobOutcome, error) {
			res, text, err := call.Run(ctx, progress)
			if err != nil {
				msg := tools.UserMessage(err)
				log.Debug("pr_ask failed", "error", msg)
				return jobOutcome{}, errors.New(msg)
			}
			// Counts only: never the question, the answer, prompts or PR
			// content.
			log.Debug("pr_ask", "llm_calls", res.Metadata.LLMCalls, "notes", len(res.Notes),
				"truncated", res.Metadata.Truncated, "published", res.Publish != nil && res.Publish.Published)
			return jobOutcome{text: text, out: *res}, nil
		})
	})
}
