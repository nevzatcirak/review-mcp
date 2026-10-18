package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/describe"
	"github.com/nevzatcirak/review-mcp/internal/improve"
	"github.com/nevzatcirak/review-mcp/internal/jobs"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// Tool names of the LLM tools whose run may continue in the background.
const (
	toolPRReview   = "pr_review"
	toolPRAsk      = "pr_ask"
	toolPRDescribe = "pr_describe"
	toolPRImprove  = "pr_improve"
	toolJobResult  = "job_result"
)

// jobResultDescription is the tool description from spec P8 §2.3.
const jobResultDescription = "Returns the result of a long-running pr_review, pr_ask, pr_describe or pr_improve call that answered with a job_id, waiting up to wait_seconds for it to finish." + partialSentence + describePartialSentence

// Jobs is the background job store of stdio mode (X-16, P8 spec §2). A
// pr_review, pr_ask, pr_describe or pr_improve call starts its run as a job
// and waits at most wait_seconds for it; a run still going then answers with a job id that
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

// background reports whether pr_review, pr_ask, pr_describe and pr_improve
// may answer with a job id:
// stdio mode with a job store. serve mode never does (X-10: a request's
// credentials must not outlive it), so it ignores Jobs.
func (d Deps) background() bool { return !d.Serve && d.Jobs != nil }

// jobOutcome is a finished run: exactly what the synchronous handler
// returns, the client markdown and the structured value (a review.Result,
// an ask.Result, a describe.Result or an improve.Result). Each answer builds a fresh *mcp.CallToolResult from it and
// hands the value to the SDK, which marshals and validates it the same way
// for the original tool and for job_result, so both carry the same bytes.
type jobOutcome struct {
	text string
	out  any
}

func (o jobOutcome) result() (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: o.text}}}, o.out, nil
}

// llmRun is the part of a pr_review, pr_ask, pr_describe or pr_improve call
// after its checks: the
// probe, the provider and LLM I/O, the publish and the rendering. Its error
// text is already the classified client sentence.
type llmRun func(ctx context.Context, progress func(stage string)) (jobOutcome, error)

// RunningResult is the structured content of a pr_review, pr_ask,
// pr_describe, pr_improve or job_result call whose run is still going or
// still waits for a free slot (X-16, WP-8e).
//
// DESIGN-QUESTION (WP-8e DQ-1): the queue position of a queued job is in the
// answer's sentence only (spec §6.2 item 2); RunningResult gets no new
// field, so the schema changes by the one status value. Recommendation:
// keep; add a queue_position field if a client needs to read it.
type RunningResult struct {
	Status         string `json:"status"`
	JobID          string `json:"job_id"`
	Stage          string `json:"stage"`
	ElapsedSeconds int    `json:"elapsed_seconds"`
}

const (
	// statusRunning and statusQueued are the values of RunningResult.Status.
	statusRunning = "running"
	statusQueued  = "queued"
	// stageStarting is the stage reported before the run reported one.
	stageStarting = "starting"
)

// runningSchema is the JSON schema of RunningResult. It shares no required
// property with the review or the answer, so a result matches exactly one
// branch of the output schemas below.
func runningSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "the run is still going or waits for a free slot; call job_result with job_id",
		"properties": map[string]any{
			"status":          map[string]any{"type": "string", "enum": []any{statusRunning, statusQueued}, "description": "running, or queued while the run waits for a free slot"},
			"job_id":          map[string]any{"type": "string", "description": "the id to pass to job_result"},
			"stage":           map[string]any{"type": "string", "description": "the current progress stage"},
			"elapsed_seconds": map[string]any{"type": "integer", "description": "seconds since the run started, or since it was queued while it waits"},
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
	// The inferred status of repo_config is a plain string; give it its
	// values and description, as the other tools' schemas do.
	if rc := s.Properties["repo_config"]; rc != nil && rc.Properties["status"] != nil {
		st := rc.Properties["status"]
		st.Enum = nil
		for _, v := range repocfg.Statuses() {
			st.Enum = append(st.Enum, v)
		}
	}
	// coverage.related (X-41) is always present in pr_ask: it gets the
	// declared schema of pr_review's (additionalProperties false, the reason
	// enum) and is required.
	if cov := s.Properties["coverage"]; cov != nil {
		var rel jsonschema.Schema
		raw, err := json.Marshal(review.RelatedCoverageSchema())
		if err != nil || json.Unmarshal(raw, &rel) != nil {
			panic("coverage.related schema") // a programming error, caught by every test
		}
		cov.Properties["related"] = &rel
		if !slices.Contains(cov.Required, "related") {
			cov.Required = append(cov.Required, "related")
		}
		// coverage.repo_context.tools (X-43) gets the declared schema of
		// pr_review's (the status, reason and call status enums).
		if rc := cov.Properties["repo_context"]; rc != nil {
			var tools jsonschema.Schema
			raw, err := json.Marshal(llmrun.RepoToolsSchema("repository tools (X-43): whether the model read the pull request's head commit through tool calls, and every call"))
			if err != nil || json.Unmarshal(raw, &tools) != nil {
				panic("coverage.repo_context.tools schema") // a programming error, caught by every test
			}
			rc.Properties["tools"] = &tools
			if !slices.Contains(rc.Required, "tools") {
				rc.Required = append(rc.Required, "tools")
			}
		}
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

// answerCall runs one pr_review, pr_ask, pr_describe or pr_improve call
// after its checks passed and owns sc from then on.
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
		// The queue is full, or shutting down: a fixed sentence, nothing ran.
		sc.release()
		return nil, nil, toolError(err.Error())
	}
	log.Debug(tool+": run started in the background", "job_id", id)
	return deps.Jobs.answer(ctx, req, log, tool, id, wait)
}

// answer waits up to wait seconds for job id and returns its result: the
// original tool's result when done, its classified error when failed, and
// the running or queued status otherwise. While it waits, the job's stages become
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
	log.Debug(tool+": still going", "job_id", id, "state", string(snap.State), "stage", snap.Stage, "elapsed_seconds", snap.ElapsedSeconds())
	return runningResult(snap)
}

// runningResult is the answer for a job still running or queued: a fixed
// sentence (that of a running job names the stage, the elapsed time and the
// job id; that of a queued job the queue position and the job id), and
// RunningResult.
func runningResult(snap jobs.Snapshot[jobOutcome]) (*mcp.CallToolResult, any, error) {
	noun := "review"
	switch snap.Tag {
	case toolPRAsk:
		noun = "answer"
	case toolPRDescribe:
		noun = "description"
	case toolPRImprove:
		noun = "suggestion job"
	}
	stage := snap.Stage
	if stage == "" {
		stage = stageStarting
	}
	secs := snap.ElapsedSeconds()
	if snap.State == jobs.StateQueued {
		text := fmt.Sprintf("The %s is waiting for a free slot (position %d in the queue). Call `%s` with job_id `%s` to get the result.",
			noun, snap.Position, toolJobResult, snap.ID)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}},
			RunningResult{Status: statusQueued, JobID: snap.ID, Stage: stage, ElapsedSeconds: secs}, nil
	}
	text := fmt.Sprintf("The %s is still running (stage: %s, %d s so far). Call `%s` with job_id `%s` to get the result.",
		noun, stage, secs, toolJobResult, snap.ID)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}},
		RunningResult{Status: statusRunning, JobID: snap.ID, Stage: stage, ElapsedSeconds: secs}, nil
}

type jobResultInput struct {
	JobID       string `json:"job_id" jsonschema:"the job_id a pr_review, pr_ask, pr_describe or pr_improve call answered with"`
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
		OutputSchema: withRunning(review.ResultSchema(), askResultSchema(), describe.ResultSchema(), improve.ResultSchema()),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in jobResultInput) (*mcp.CallToolResult, any, error) {
		wait, err := tools.WaitSeconds(in.WaitSeconds, deps.Config)
		if err != nil {
			return nil, nil, toolError(tools.UserMessage(err))
		}
		return deps.Jobs.answer(ctx, req, logger(deps), toolJobResult, in.JobID, wait)
	})
}
