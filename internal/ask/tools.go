package ask

import (
	"context"

	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/related"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/repotools"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Repository tools in pr_ask (X-43, V-8 spec §3 item 1): the same loop as
// pr_review, with a free-text final answer, so there is no re-ask; from v2.5
// (spec V-13b §3.1) also over the related repositories of the placed
// related block.

// toolPlan is the tool loop of a run.
type toolPlan struct {
	box  *repotools.Box
	conv repotools.Conversant
	in   repotools.LoopInput
	// placed are the placed related sections, whose repositories the tools
	// may read.
	placed []related.Section
}

// toolUse is what a run did with the tools.
type toolUse = repotools.Use

// toolsSkipped records why a run that could use tools does not.
func (pl *Plan) toolsSkipped(reason string) {
	if pl.toolUse.Status == repotools.StatusOff {
		return
	}
	pl.toolUse = toolUse{Status: repotools.StatusSkipped, Reason: reason}
	pl.log.Debug("ask: repository tools not used", "reason", reason)
}

// planTools decides whether the run uses the tools, with the rules of
// pr_review (§2.2 item 1): rounds above 0, repository context used and its
// head fetched, an endpoint not known to refuse tools, and room for the
// instruction block and the tool definitions beside the whole diff. in is
// the final prompt input and fit its fitted prompts; placed are the placed
// related sections, whose fetched repositories the tools may also read.
func (pl *Plan) planTools(ctx context.Context, deps Deps, in PromptInput, fit *llmrun.Fitted, diff string, b tokens.Budget,
	rcs *repoctx.Session, rc llmrun.RepoContext, placed []related.Section) error {
	if pl.toolUse.Status == repotools.StatusOff {
		return nil
	}
	c := pl.cfg.Context.Repo
	if rc.Status != llmrun.RepoUsed || rcs.Ready(ctx) != "" {
		pl.toolsSkipped(repotools.ReasonRepoContextUnavailable)
		return nil
	}
	conv, ok := deps.LLM.(repotools.Conversant)
	if !ok || llmrun.ToolsRefused(deps.LLM) {
		pl.toolsSkipped(repotools.ReasonEndpointNoTools)
		return nil
	}
	if fit.KeptLines >= 0 {
		pl.toolsSkipped(repotools.ReasonNoRoom)
		return nil
	}
	repos := related.ToolRepositories(placed)
	tin := in
	tin.ToolRounds, tin.ToolRepos = c.MaxToolRounds, repos
	tf, err := fitPlain(tin, diff, b)
	if err != nil {
		return err
	}
	limit := b.ContextWindow - b.HardReserve()
	if tf.KeptLines >= 0 || tf.Rendered.User != fit.Rendered.User ||
		repotools.RequestTokens(repotools.Start(tf.Rendered.System, tf.Rendered.User, repos...), b.Factor) > limit {
		pl.toolsSkipped(repotools.ReasonNoRoom)
		return nil
	}
	box, ok := repotools.Open(rcs, repotools.Settings{MaxToolTokens: c.MaxToolTokens,
		MaxFileBytes: pl.cfg.Diff.MaxFileBytes, Factor: b.Factor}, pl.log)
	if !ok {
		pl.toolsSkipped(repotools.ReasonRepoContextUnavailable)
		return nil
	}
	box.WithRepos(repos)
	pl.tools = &toolPlan{box: box, conv: conv, in: repotools.LoopInput{
		System: tf.Rendered.System, User: tf.Rendered.User, MaxRounds: c.MaxToolRounds, Limit: limit, Factor: b.Factor,
	}, placed: placed}
	return nil
}

// answer makes the model call of the run: through the tool loop when the
// plan has one, else one Complete. When the loop ends without an answer
// (the endpoint refuses tools, or the conversation failed), the question is
// answered single-pass with the prompts without tools. calls is the number
// of model calls made.
func (pl *Plan) answer(ctx context.Context, deps Deps) (resp *llm.Response, calls int, err error) {
	if pl.tools == nil {
		resp, err = deps.LLM.Complete(ctx, pl.Prompts.System, pl.Prompts.User)
		return resp, 1, err
	}
	tp := pl.tools
	lr, err := tp.box.Loop(ctx, tp.conv, tp.in)
	u := toolUse{Rounds: lr.Rounds, Calls: tp.box.Calls(), Tokens: tp.box.Tokens(), Exhausted: tp.box.Exhausted()}
	switch {
	case err != nil:
		u.Status, u.Reason = repotools.StatusSkipped, repotools.ReasonConversationFailed
		pl.toolUse = u
		return nil, lr.Calls, err
	case lr.Fallback == repotools.FallbackNoTools:
		u.Status, u.Reason = repotools.StatusSkipped, repotools.ReasonEndpointNoTools
	case lr.Fallback != "":
		u.Status, u.Reason = repotools.StatusSkipped, repotools.ReasonConversationFailed
	case len(u.Calls) > 0:
		u.Status = repotools.StatusUsed
	default:
		u.Status = repotools.StatusNotUsed
	}
	pl.toolUse = u
	pl.log.Debug("ask: repository tools", "status", u.Status, "reason", u.Reason, "rounds", u.Rounds,
		"calls", len(u.Calls), "tokens", u.Tokens, "model_calls", lr.Calls, "restarted", lr.Restarted)
	if lr.Fallback == "" {
		related.MarkToolReads(tp.placed, tp.box.Seen)
		pl.seen.Add(tp.box.SeenHead()...)
		return &llm.Response{Content: lr.Answer.Text, Truncated: lr.Answer.Truncated, Usage: lr.Answer.Usage}, lr.Calls, nil
	}
	resp, err = deps.LLM.Complete(ctx, pl.Prompts.System, pl.Prompts.User)
	return resp, lr.Calls + 1, err
}

// reportTools puts the tool use of a run into its result (§2.3): the
// coverage, the metadata and the notes.
func reportTools(res *Result, u toolUse) {
	res.Coverage.RepoContext.Tools = u.Report()
	res.Metadata.ToolRounds = u.Rounds
	res.Notes = append(res.Notes, u.Notes()...)
}
