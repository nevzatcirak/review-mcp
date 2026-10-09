package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/describe"
	"github.com/nevzatcirak/review-mcp/internal/jobs"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// Tool names of the LLM tools whose run may continue in the background.
const (
	toolPRReview   = "pr_review"
	toolPRAsk      = "pr_ask"
	toolPRDescribe = "pr_describe"
	toolJobResult  = "job_result"
)

// jobResultDescription is the tool description from spec P8 §2.3.
const jobResultDescription = "Returns the result of a long-running pr_review, pr_ask or pr_describe call that answered with a job_id, waiting up to wait_seconds for it to finish." + partialSentence + describePartialSentence

// Jobs is the background job store of stdio mode (X-16, P8 spec §2). A
// pr_review, pr_ask or pr_describe call starts its run as a job and waits at most
// wait_seconds for it; a run still going then answers with a job id that
// job_result reads later. The store lives as long as the stdio server: the
// caller creates it, passes it in Deps.Jobs, and closes it at shutdown,
// which cancels every running job.
type Jobs struct {
	store *jobs.Store[jobOutcome]
}

// NewJobs returns an empty job store. now is the clock of the 30-minute
// result expiry and of the elapsed times (nil: time.Now).
func NewJobs(now func() time.Time) *Jobs {
	return &Jobs{store: jobs.New[jobOutcome](jobs.Options{Now: now})}
}

// Close cancels every running job (SIGINT, SIGTERM, stdin EOF). It does not
// wait for them: a job never keeps the process alive. It is safe on nil.
func (j *Jobs) Close() {
	if j != nil {
		j.store.Close()
	}
}

// background reports whether pr_review, pr_ask and pr_describe may answer
// with a job id:
// stdio mode with a job store. serve mode never does (X-10: a request's
// credentials must not outlive it), so it ignores Jobs.
func (d Deps) background() bool { return !d.Serve && d.Jobs != nil }

// jobOutcome is a finished run: exactly what the synchronous handler
// returns, the client markdown and the structured value (a review.Result,
// an ask.Result or a describe.Result). Each answer builds a fresh *mcp.CallToolResult from it and
// hands the value to the SDK, which marshals and validates it the same way
// for the original tool and for job_result, so both carry the same bytes.
type jobOutcome struct {
	text string
	out  any
}

func (o jobOutcome) result() (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: o.text}}}, o.out, nil
}

// llmRun is the part of a pr_review, pr_ask or pr_describe call after its
// checks: the
// probe, the provider and LLM I/O, the publish and the rendering. Its error
// text is already the classified client sentence.
type llmRun func(ctx context.Context, progress func(stage string)) (jobOutcome, error)

// RunningResult is the structured content of a pr_review, pr_ask,
// pr_describe or job_result call whose run is still going (X-16).
type RunningResult struct {
	Status         string `json:"status"`
	JobID          string `json:"job_id"`
	Stage          string `json:"stage"`
	ElapsedSeconds int    `json:"elapsed_seconds"`
}

const (
	// statusRunning is RunningResult.Status.
	statusRunning = "running"
	// stageStarting is the stage reported before the run reported one.
	stageStarting = "starting"
)

// runningSchema is the JSON schema of RunningResult. It shares no required
// property with the review or the answer, so a result matches exactly one
// branch of the output schemas below.
func runningSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "the run is still going; call job_result with job_id",
		"properties": map[string]any{
			"status":          map[string]any{"type": "string", "enum": []any{statusRunning}, "description": "always running"},
			"job_id":          map[string]any{"type": "string", "description": "the id to pass to job_result"},
			"stage":           map[string]any{"type": "string", "description": "the current progress stage"},
			"elapsed_seconds": map[string]any{"type": "integer", "description": "seconds since the run started"},
		},
		"required":             []any{"status", "job_id", "stage", "elapsed_seconds"},
		"additionalProperties": false,
	}
}

// withRunning is the output schema of a tool whose structured content is
// one of the given results or, in stdio mode, the running status. The SDK
// validates every result against it, so the running status must be one of
// the branches; the finished result still validates against its own
// unchanged schema.
func withRunning(results ...any) map[string]any {
	return map[string]any{"type": "object", "oneOf": append(results, runningSchema())}
}

// askResultSchema is the schema the SDK infers from ask.Result, built the
// same way (jsonschema.For with default options), so pr_ask can declare it
// explicitly next to the running status.
func askResultSchema() *jsonschema.Schema {
	s, err := jsonschema.For[ask.Result](&jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("ask.Result schema: %v", err)) // a programming error, caught by every test
	}
	return s
}

// reviewOutputSchema and askOutputSchema are the declared output schemas of
// pr_review and pr_ask: the result alone in serve mode, the result or the
// running status in stdio mode.
func reviewOutputSchema(deps Deps) any {
	if deps.background() {
		return withRunning(review.ResultSchema())
	}
	return review.ResultSchema()
}

func askOutputSchema(deps Deps) any {
	if deps.background() {
		return withRunning(askResultSchema())
	}
	return askResultSchema()
}

// answerCall runs one pr_review, pr_ask or pr_describe call after its checks passed and
// owns sc from then on.
//
// serve mode (and a server without a job store) runs synchronously, as
// before X-16: wait_seconds is ignored and the stages go straight to the
// request's progress notifications.
//
// stdio mode starts the run as a job and waits for it up to wait seconds.
// The job context comes from the store, not from the request: it survives
// the call's answer and ends only at shutdown. It is started from
// context.Background(), not from ctx, so that the job keeps none of the
// request's context values (the SDK puts the JSON-RPC request id, its log
// level and its jsonrpc2 releaser there); the run reports its stages to the
// job only, and only a waiting handler turns them into notifications while
// its own request is open. The scope (the per-call config, its resolver and
// LLM client) moves into the job and is released when the run ends.
func answerCall(ctx context.Context, deps Deps, req *mcp.CallToolRequest, tool string, sc *scope, wait int, run llmRun) (*mcp.CallToolResult, any, error) {
	log := logger(deps)
	if !deps.background() {
		defer sc.release()
		o, err := run(ctx, progressFunc(ctx, req, log, tool))
		if err != nil {
			return nil, nil, toolError(err.Error())
		}
		return o.result()
	}
	id, err := deps.Jobs.store.Start(context.Background(), tool, func(jctx context.Context, progress func(string)) (jobOutcome, error) {
		defer sc.release()
		return run(jctx, progress)
	})
	if err != nil {
		// Too many jobs, or shutting down: a fixed sentence, nothing ran.
		sc.release()
		return nil, nil, toolError(err.Error())
	}
	log.Debug(tool+": run started in the background", "job_id", id)
	return deps.Jobs.answer(ctx, req, log, tool, id, wait)
}

// answer waits up to wait seconds for job id and returns its result: the
// original tool's result when done, its classified error when failed, and
// the running status otherwise. While it waits, the job's stages become
// progress notifications of this request when it carries a progress token.
func (j *Jobs) answer(ctx context.Context, req *mcp.CallToolRequest, log *slog.Logger, tool, id string, wait int) (*mcp.CallToolResult, any, error) {
	snap, err := j.store.WaitProgress(ctx, id, time.Duration(wait)*time.Second, progressFunc(ctx, req, log, tool))
	if errors.Is(err, jobs.ErrUnknown) {
		return nil, nil, toolError(jobs.UnknownSentence)
	}
	// Any other error is the request's context ending (the client cancelled
	// or went away): the snapshot is still current and the job goes on.
	switch snap.State {
	case jobs.StateDone:
		return snap.Result.result()
	case jobs.StateFailed:
		return nil, nil, toolError(snap.Message)
	}
	log.Debug(tool+": still running", "job_id", id, "stage", snap.Stage, "elapsed_seconds", snap.ElapsedSeconds())
	return runningResult(snap)
}

// runningResult is the answer for a job still running: a fixed sentence
// that names the stage, the elapsed time and the job id, and RunningResult.
func runningResult(snap jobs.Snapshot[jobOutcome]) (*mcp.CallToolResult, any, error) {
	noun := "review"
	switch snap.Tag {
	case toolPRAsk:
		noun = "answer"
	case toolPRDescribe:
		noun = "description"
	}
	stage := snap.Stage
	if stage == "" {
		stage = stageStarting
	}
	secs := snap.ElapsedSeconds()
	text := fmt.Sprintf("The %s is still running (stage: %s, %d s so far). Call `%s` with job_id `%s` to get the result.",
		noun, stage, secs, toolJobResult, snap.ID)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}},
		RunningResult{Status: statusRunning, JobID: snap.ID, Stage: stage, ElapsedSeconds: secs}, nil
}

type jobResultInput struct {
	JobID       string `json:"job_id" jsonschema:"the job_id a pr_review, pr_ask or pr_describe call answered with"`
	WaitSeconds *int   `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for the result, 0 to 600; replaces llm.wait_seconds (default 45) for this call"`
}

// registerJobResult adds job_result (stdio mode with a job store only). It
// reads no configuration besides llm.wait_seconds and works in a degraded
// start too: there are simply no jobs then.
func registerJobResult(s *mcp.Server, deps Deps) {
	f := false
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolJobResult,
		Description: jobResultDescription,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &f,
			OpenWorldHint:   &f,
		},
		OutputSchema: withRunning(review.ResultSchema(), askResultSchema(), describe.ResultSchema()),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in jobResultInput) (*mcp.CallToolResult, any, error) {
		wait, err := tools.WaitSeconds(in.WaitSeconds, deps.Config)
		if err != nil {
			return nil, nil, toolError(tools.UserMessage(err))
		}
		return deps.Jobs.answer(ctx, req, logger(deps), toolJobResult, in.JobID, wait)
	})
}
