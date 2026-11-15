package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/provider/local"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// maxPromptPathBytes caps the repo_path argument of the prompt.
const maxPromptPathBytes = 4096

// localPrompt answers multi_perspective_review for local work (v2.10 spec
// V-20 §2.2): repo_path is refused in serve mode and while local.enabled is
// off; the local source is opened (the path, local.allowed_roots and the
// base are checked, and a refusal is InvalidParams with its fixed
// sentence); the focuses come from the repository config read at the merge
// base and from the local diff's files, by the rules of a pull request.
func localPrompt(ctx context.Context, deps Deps, req *mcp.GetPromptRequest, repoPath, base, focuses string, queued bool) (*mcp.GetPromptResult, error) {
	invalid := func(msg string) error { return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: msg} }
	switch {
	case deps.Serve:
		return nil, invalid(promptServeLocalMessage)
	case !localEnabled(deps):
		return nil, invalid(promptLocalOffMessage)
	case len(repoPath) > maxPromptPathBytes || strings.ContainsAny(repoPath, "\x00\n\r"):
		return nil, invalid("repo_path must be an absolute path")
	}
	read := func(list bool) (promptContext, error) {
		return readLocalPromptContext(ctx, deps, repoPath, base, list)
	}
	var plan focusPlan
	if strings.TrimSpace(focuses) == "" {
		pc, err := read(true)
		if err != nil {
			return nil, invalid(tools.UserMessage(err))
		}
		plan = planFocuses(pc)
	} else {
		var customs []config.CustomFocus
		if deps.Config != nil {
			customs = deps.Config.Review.CustomFocuses
		}
		names, ok := promptFocuses(focuses, customs)
		if !ok && promptFocusesWellFormed(focuses) {
			pc, err := read(false)
			if err != nil {
				return nil, invalid(tools.UserMessage(err))
			}
			customs = pc.customs
			names, ok = promptFocuses(focuses, customs)
		}
		if !ok {
			return nil, invalid(focusesRefusal(customs))
		}
		plan = requestedPlan(names)
	}
	return &mcp.GetPromptResult{
		Description: "Multi-perspective review of local work",
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: localPerspectiveText(repoPath, base, plan, queued)},
		}},
	}, nil
}

// readLocalPromptContext opens the local source of repoPath and base and
// reads what planFocuses needs: the repository config at the merge base and
// the local diff's files (no content is read). An error is a refusal of the
// path or the base; a failed read of the configuration or the files is
// promptContext.failed, as for a pull request.
func readLocalPromptContext(ctx context.Context, deps Deps, repoPath, base string, list bool) (promptContext, error) {
	var pc promptContext
	cfg := deps.Config
	if deps.LoadErr != nil || cfg == nil {
		return pc, nil
	}
	ctx, cancel := context.WithTimeout(ctx, promptReadTimeout)
	defer cancel()
	opts := local.OptionsFrom(cfg)
	opts.AllowedRoots = cfg.Local.AllowedRoots
	opts.Logger = logger(deps)
	req := local.DefaultRequest(repoPath)
	req.Base = base
	src, err := local.Open(ctx, opts, req)
	if err != nil {
		return pc, err
	}
	defer func() { _ = src.Close() }()
	if !cfg.RepoConfig.Enabled {
		// As for a pull request: nothing more is read, the built-in default.
		return pc, nil
	}
	pc.failed = true
	ref := src.Ref()
	pr, err := src.GetPullRequest(ctx, ref)
	if err != nil {
		return pc, nil
	}
	out := repocfg.ForRun(ctx, logger(deps), cfg, src, ref, pr)
	if out.Result.Status == repocfg.StatusUnreadable {
		return pc, nil
	}
	pc.customs = out.Config.Review.CustomFocuses
	pc.repoFocuses = validRepoFocuses(out.Focuses, pc.customs)
	pc.rules = out.Config.Review.FocusRules
	if list && (pc.repoFocuses == nil || len(pc.rules) > 0) {
		if pc.changed, err = changedFiles(ctx, out.Config, src, ref, pr); err != nil {
			pc.repoFocuses, pc.rules, pc.changed = nil, nil, nil
			return pc, nil
		}
		pc.docsChanged = slices.ContainsFunc(pc.changed, filter.IsDocumentation)
	}
	pc.failed = false
	return pc, nil
}

// localPerspectiveText is the prompt's user message for local work: one
// local_review call per focus, then the merged findings shown to the user.
// Nothing is published. repoPath and base appear only inside JSON strings.
func localPerspectiveText(repoPath, base string, plan focusPlan, queued bool) string {
	type call struct {
		RepoPath string `json:"repo_path"`
		Base     string `json:"base,omitempty"`
		Focus    string `json:"focus"`
	}
	var b strings.Builder
	b.WriteString("Review the local work in " + jsonText(repoPath) + " from several perspectives with the review-mcp " +
		"tools, then show one merged review. Nothing is published: local_review only reads the repository.\n\n")
	b.WriteString("1. Run one focused review per area with local_review, in parallel if you can (for example in " +
		"subagents). ")
	if queued {
		b.WriteString(rateLimitGuidance + " ")
	}
	b.WriteString("Use these arguments:\n")
	for _, e := range plan.entries {
		b.WriteString("   - " + jsonText(call{RepoPath: repoPath, Base: base, Focus: e.name}) + "\n")
	}
	b.WriteString("   Why these areas:\n")
	for _, e := range plan.entries {
		b.WriteString("   - " + e.name + " — " + e.why + "\n")
	}
	if len(plan.cut) > 0 {
		fmt.Fprintf(&b, "   The list is cut at %d areas; not included: %s.\n", maxPromptFocuses, strings.Join(plan.cut, ", "))
	}
	if plan.readFailed {
		b.WriteString("   The repository config could not be read; the default areas are used.\n")
	}
	b.WriteString("2. Merge the findings of all runs into one list. Keep each finding's relevant_file, start_line, " +
		"end_line, issue_header and issue_content as the run returned them, and note the run's focus. Drop " +
		"duplicates: when two runs report the same problem at the same place, keep the clearer finding.\n")
	b.WriteString("3. Show the user the merged findings, grouped by file, with each finding's focus and its " +
		"path:line. There is nothing to publish.\n")
	b.WriteString("4. If a run said its review is partial, say how many files were not reviewed and never state " +
		"that those files have no issues. If a run left untracked files out because their names suggest secrets, " +
		"say so.\n")
	return b.String()
}
