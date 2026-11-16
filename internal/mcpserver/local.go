package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// toolLocalReview is the name of the local review tool (X-52).
const toolLocalReview = "local_review"

// localReviewDescription is the tool description of v2.10 spec V-20 §2.1,
// verbatim. In stdio mode with jobs it gains localReviewJobSentence.
const localReviewDescription = "Reviews local work before it is pushed: the commits after the merge base with base " +
	"(default: the branch's upstream), plus uncommitted changes to tracked files; untracked files only with " +
	"include_untracked. Read-only: nothing is published and the repository is not changed. Use it before pushing; " +
	"use pr_review once a pull request exists."

// localReviewJobSentence ends the description when a slow review answers
// with a job id, as for pr_review; there is nothing to publish.
const localReviewJobSentence = " A review that takes longer than wait_seconds answers with a job_id instead: call job_result for the result."

// localGuidance is the one sentence the server instructions gain when
// local_review is registered (§2.1).
const localGuidance = "To review work that is not pushed yet, call local_review with the repository's path; it reads " +
	"the repository and changes nothing."

// localEnabled reports whether local_review is registered: local.enabled
// and stdio mode (§0 item 4: the server's disk is not the user's).
func localEnabled(deps Deps) bool {
	return deps.Config != nil && deps.Config.Local.Enabled && !deps.Serve
}

// localOnlyInstructions are the server instructions of a local-only start
// (WP-24d §2.3): exactly this paragraph, nothing else. The docs quote it
// verbatim.
const localOnlyInstructions = "review-mcp reviews local git work before it is pushed. No git provider is " +
	"configured, so only local_review is available: it reads the commits after the merge base with the base " +
	"and the uncommitted changes, and publishes nothing. Pull request tools appear once a provider (gitea, " +
	"bitbucket_server, github or gitlab) is configured."

// localOnlyPromptMessage is the InvalidParams message of the prompt when
// pr_url is given in a local-only start (WP-24d §2.4).
const localOnlyPromptMessage = "no git provider is configured; only local reviews are available (pass repo_path)"

// localOnly reports a local-only start: no provider is enabled, the
// configuration loaded without a problem and the transport is stdio. A
// degraded start (an invalid configuration) keeps the full set.
func localOnly(deps Deps) bool {
	return !deps.Serve && deps.LoadErr == nil && deps.Config.LocalOnly()
}

// localReviewInput are the arguments of local_review (§2.1). publish,
// findings, incremental and related do not exist for local work and are
// not in the schema.
type localReviewInput struct {
	RepoPath           string             `json:"repo_path" jsonschema:"absolute path of the git work tree to review, or of a directory in it (its top level is used)"`
	Base               string             `json:"base,omitempty" jsonschema:"the branch, tag or commit to compare with; the review covers the commits after the merge base with it. Default: the current branch's upstream, else origin/HEAD, origin/main, origin/master, main, master. Nothing is fetched: a remote-tracking base is the local copy"`
	IncludeUncommitted *bool              `json:"include_uncommitted,omitempty" jsonschema:"also review the uncommitted changes to tracked files, staged and unstaged (default true)"`
	IncludeUntracked   *bool              `json:"include_untracked,omitempty" jsonschema:"also review untracked files that no ignore rule excludes (default false); files whose names suggest secrets (.env, *.pem, *.key and the like) are always left out and counted"`
	Focus              string             `json:"focus,omitempty" jsonschema:"review only one area: security, architecture, performance, ux, tests, docs, the name of a custom focus defined in the configuration, or a description of at most 200 characters"`
	ExtraInstructions  string             `json:"extra_instructions,omitempty" jsonschema:"extra review instructions for the model; replaces review.extra_instructions for this call"`
	OutputLanguage     string             `json:"output_language,omitempty" jsonschema:"locale code for the review text, for example en-US or tr-TR; replaces output.language for this call"`
	MaxFindings        *int               `json:"max_findings,omitempty" jsonschema:"most key issues to return, 1 to 20; replaces review.max_findings for this call"`
	IssueContext       []issueContextItem `json:"issue_context,omitempty" jsonschema:"The issue or ticket this work implements, for example fetched from a tracker MCP server. Used to check that the change does what the issue asks. 1 to 3 issues. Ignored with focus"`
	WaitSeconds        *int               `json:"wait_seconds,omitempty" jsonschema:"seconds to wait for the result before answering with a job_id for job_result, 0 to 600; replaces llm.wait_seconds (default 45) for this call"`
}

func registerLocalReview(s *mcp.Server, deps Deps) {
	f := false
	mcp.AddTool(s, &mcp.Tool{
		Name:        toolLocalReview,
		Description: toolDescription(deps, localReviewDescription, localReviewJobSentence),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &f,
			OpenWorldHint:   &f,
		},
		OutputSchema: reviewOutputSchema(deps),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in localReviewInput) (*mcp.CallToolResult, any, error) {
		log := logger(deps)
		sc, err := callScope(ctx, deps, req, true)
		if err != nil {
			return nil, nil, err
		}
		if deps.NewLLM == nil {
			sc.release()
			return nil, nil, toolError("review-mcp has no LLM wiring; this is a bug")
		}
		// Everything that needs neither the model nor the network fails
		// here, before a job exists: the arguments, the LLM client, the
		// repository path, the base and the merge base, then wait_seconds.
		call, err := tools.PrepareLocalReview(ctx, tools.ReviewDeps{Config: sc.cfg, NewLLM: sc.newLLM, Logger: log},
			tools.LocalReviewArgs{
				RepoPath: in.RepoPath, Base: in.Base, IncludeUncommitted: in.IncludeUncommitted,
				IncludeUntracked: in.IncludeUntracked, Focus: in.Focus, ExtraInstructions: in.ExtraInstructions,
				OutputLanguage: in.OutputLanguage, MaxFindings: in.MaxFindings, IssueContext: issueContext(in.IssueContext),
			})
		wait := 0
		if err == nil && deps.background() {
			wait, err = tools.WaitSeconds(in.WaitSeconds, sc.cfg)
			if err != nil {
				call.Close()
			}
		}
		if err != nil {
			sc.release()
			msg := tools.UserMessage(err)
			log.Debug("local_review failed", "error", msg)
			return nil, nil, toolError(msg)
		}
		log.Debug("local_review started", tools.LocalLogFields(call.Info())...)
		res, out, aerr := answerCall(ctx, deps, req, toolLocalReview, sc, wait, func(ctx context.Context, progress func(string)) (jobOutcome, error) {
			res, text, err := call.Run(ctx, progress)
			if err != nil {
				msg := tools.UserMessage(err)
				log.Debug("local_review failed", "error", msg)
				return jobOutcome{}, errors.New(msg)
			}
			// Counts only: never the review text, prompts, paths or content.
			log.Debug("local_review", "findings", len(res.Review.KeyIssuesToReview), "llm_calls", res.Metadata.LLMCalls,
				"notes", len(res.Notes), "focused", res.Focus != "")
			return jobOutcome{text: text, out: *res}, nil
		})
		if aerr != nil {
			// The job queue refused the run, or the synchronous run failed
			// (which closed it already): Close is idempotent.
			call.Close()
		}
		return res, out, aerr
	})
}
