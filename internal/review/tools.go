package review

import (
	"context"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/repotools"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Repository tools in pr_review (X-43, V-8 spec §2.2): in a review in one
// call with repository context, the model may read the pull request's head
// commit through tool calling before it answers.

// toolPlan is the tool loop of a review in one call.
type toolPlan struct {
	box  *repotools.Box
	conv repotools.Conversant
	in   repotools.LoopInput
}

// toolUse is what a run did with the tools.
type toolUse struct {
	status, reason string
	rounds         int
	calls          []repotools.Call
	tokens         int
	exhausted      bool
}

// toolsDefault is the tool use of a run before the plan decides: off when
// context.repo.max_tool_rounds is 0 or repository context is off, else
// skipped for a run that makes no model call (it stays so for an empty or
// unchanged review and a publish of supplied findings).
func toolsDefault(c config.ContextRepo) toolUse {
	if !c.Enabled || c.MaxToolRounds <= 0 {
		return toolUse{status: repotools.StatusOff, reason: repotools.ReasonOff}
	}
	return toolUse{status: repotools.StatusSkipped, reason: repotools.ReasonNoModelCall}
}

// toolsSkipped records why a run that could use tools does not.
func (pl *Plan) toolsSkipped(reason string) {
	if pl.toolUse.status == repotools.StatusOff {
		return
	}
	pl.toolUse = toolUse{status: repotools.StatusSkipped, reason: reason}
	pl.log.Debug("review: repository tools not used", "reason", reason)
}

// planTools decides whether the review in one call uses the tools (§2.2
// item 1): context.repo.max_tool_rounds above 0, repository context used
// and its head fetched, an endpoint not known to refuse tools, and room for
// the instruction block and the tool definitions beside the whole diff. in
// is the final prompt input and fit its fitted prompts; b is the budget the
// diff was prepared against.
func (pl *Plan) planTools(ctx context.Context, deps Deps, in PromptInput, fit *fitted, diff string, b tokens.Budget,
	rcs *repoctx.Session, rc llmrun.RepoContext) error {
	if pl.toolUse.status == repotools.StatusOff {
		return nil
	}
	c := pl.cfg.Context.Repo
	// Repository context must be in use, and its head fetched: a diff that
	// defines no symbol searched nothing yet, so the head is fetched now.
	if rc.Status != llmrun.RepoUsed || rcs.Ready(ctx) != "" {
		pl.toolsSkipped(repotools.ReasonRepoContextUnavailable)
		return nil
	}
	conv, ok := deps.LLM.(repotools.Conversant)
	if !ok || llmrun.ToolsRefused(deps.LLM) {
		pl.toolsSkipped(repotools.ReasonEndpointNoTools)
		return nil
	}
	// The diff wins (§2.2 item 5): a diff the single-pass request had to
	// trim, or one that the block or the tool definitions would trim, gets
	// no tool round.
	if fit.keptLines >= 0 {
		pl.toolsSkipped(repotools.ReasonNoRoom)
		return nil
	}
	tin := in
	tin.ToolRounds = c.MaxToolRounds
	tf, err := fitPrompts(tin, diff, b)
	if err != nil {
		return err
	}
	limit := b.ContextWindow - b.HardReserve()
	if tf.keptLines >= 0 || tf.prompts.User != fit.prompts.User ||
		repotools.RequestTokens(repotools.Start(tf.prompts.System, tf.prompts.User), b.Factor) > limit {
		pl.toolsSkipped(repotools.ReasonNoRoom)
		return nil
	}
	box, ok := repotools.Open(rcs, repotools.Settings{MaxToolTokens: c.MaxToolTokens,
		MaxFileBytes: pl.cfg.Diff.MaxFileBytes, Factor: b.Factor}, pl.log)
	if !ok {
		pl.toolsSkipped(repotools.ReasonRepoContextUnavailable)
		return nil
	}
	pl.tools = &toolPlan{box: box, conv: conv, in: repotools.LoopInput{
		System: tf.prompts.System, User: tf.prompts.User, MaxRounds: c.MaxToolRounds, Limit: limit, Factor: b.Factor,
	}}
	return nil
}

// converse answers the first call of a review in one call through the tool
// loop. When the loop ends without an answer (the endpoint refuses tools,
// or the conversation failed), the review answers single-pass with the
// prompts without tools, as it would have without them.
func (pl *Plan) converse(ctx context.Context, deps Deps, fit *fitted, a *partAnswer) (*llm.Response, error) {
	tp := pl.tools
	lr, err := tp.box.Loop(ctx, tp.conv, tp.in)
	a.calls += lr.Calls
	u := toolUse{rounds: lr.Rounds, calls: tp.box.Calls(), tokens: tp.box.Tokens(), exhausted: tp.box.Exhausted()}
	switch {
	case err != nil:
		u.status, u.reason = repotools.StatusSkipped, repotools.ReasonConversationFailed
		pl.toolUse = u
		return nil, err
	case lr.Fallback == repotools.FallbackNoTools:
		u.status, u.reason = repotools.StatusSkipped, repotools.ReasonEndpointNoTools
	case lr.Fallback != "":
		u.status, u.reason = repotools.StatusSkipped, repotools.ReasonConversationFailed
	case len(u.calls) > 0:
		u.status = repotools.StatusUsed
	default:
		u.status = repotools.StatusNotUsed
	}
	pl.toolUse = u
	pl.log.Debug("review: repository tools", "status", u.status, "reason", u.reason, "rounds", u.rounds,
		"calls", len(u.calls), "tokens", u.tokens, "model_calls", lr.Calls, "restarted", lr.Restarted)
	if lr.Fallback == "" {
		return &llm.Response{Content: lr.Answer.Text, Truncated: lr.Answer.Truncated, Usage: lr.Answer.Usage}, nil
	}
	resp, err := deps.LLM.Complete(ctx, fit.prompts.System, fit.prompts.User)
	a.calls++
	return resp, err
}
