package ask

import (
	"embed"
	"text/template"

	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/prompt"
	"github.com/nevzatcirak/review-mcp/internal/repotools"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// The ask prompts are adapted from PR-Agent
// (pr_agent/settings/pr_questions_prompts.toml at commit
// 8e5a9295973b24af4b70cafd0b660a230811ef9e, MIT; see NOTICE). The template
// files list the adaptations in their header comments, including the
// honesty deviation of spec P5 §1.1.
//
// The upstream template has no date, so the prompts do not depend on a
// clock.
//
//go:embed prompts/system.tmpl prompts/user.tmpl
var promptFiles embed.FS

// Template names.
const (
	systemTemplate = "system.tmpl"
	userTemplate   = "user.tmpl"
)

var templates = template.Must(prompt.New("ask").ParseFS(promptFiles, "prompts/*.tmpl"))

// PromptInput is everything the ask prompts depend on.
type PromptInput struct {
	// ExtraInstructions are the effective extra instructions (the per-call
	// argument, else ask.extra_instructions), before the output-language
	// instruction is added.
	ExtraInstructions string
	// RepoInstructions is the rendered block of the repository's extra
	// instructions (projctx.RepoGuidance); empty omits it. It follows the
	// extra instructions in the system prompt.
	RepoInstructions string
	// Language is the effective output language (the per-call argument,
	// else output.language). Empty means en-US.
	Language string
	// MainLanguage is the first diffpipe language group (DQ-2 ranking);
	// empty or filter.OtherLanguage omits the line.
	MainLanguage string
	Title        string
	// Branch is the PR's source branch (upstream get_pr_branch).
	Branch string
	// Description is the PR description, already clipped
	// (tokens.ClipDescription); the template trims it.
	Description string
	// Question is the validated question (Validate).
	Question string
	// RepoContext is the rendered repository-context block (repoctx.Render);
	// empty omits it. It is repository text, and part of the scaffolding.
	RepoContext string
	// Related is the rendered related block (related.Block: the fixed
	// sentence and the fenced related pull requests, X-41); empty omits it.
	// It precedes the repository context.
	Related string
	// ToolRounds is context.repo.max_tool_rounds for a run in which the
	// model may read the repository through tools (X-43): the system prompt
	// carries ToolsInstruction. 0 renders as before.
	ToolRounds int
	// ToolRepos are the related repositories the tools may also read
	// (v2.5 spec V-13b §3.1, related.ToolRepositories): the tools'
	// instruction names them. None renders as before.
	ToolRepos []repotools.Repo
	// Diff is the prepared plain diff; empty for the scaffolding
	// measurement.
	Diff string
}

// ToolsFinalAnswer is the last line of the tools' instruction block of
// pr_ask: the answer is free text.
const ToolsFinalAnswer = "Your final answer is the plain-text answer to the question, with no tool call in it."

// ToolsInstruction is the system prompt's block of a run with tools (X-43),
// naming the related repositories repos the tools may also read; "" for
// rounds 0.
func ToolsInstruction(rounds int, repos ...repotools.Repo) string {
	if rounds <= 0 {
		return ""
	}
	return repotools.QuestionInstruction(rounds, ToolsFinalAnswer, repos...)
}

// Prompts is one rendered system and user prompt pair.
type Prompts struct {
	System string
	User   string
}

// vars builds the template data. Every key a template uses must be set
// here: the engine runs with missingkey=error.
func (in *PromptInput) vars() map[string]any {
	lang := in.MainLanguage
	if lang == filter.OtherLanguage {
		lang = ""
	}
	return map[string]any{
		"extra_instructions": prompt.WithOutputLanguage(in.ExtraInstructions, in.Language),
		"repo_instructions":  in.RepoInstructions,
		"title":              in.Title,
		"branch":             in.Branch,
		"description":        in.Description,
		"language":           lang,
		"repo_context":       in.RepoContext,
		"related":            in.Related,
		"tools_instruction":  ToolsInstruction(in.ToolRounds, in.ToolRepos...),
		"diff":               in.Diff,
		"questions":          in.Question,
	}
}

// RenderPrompts renders the system and user prompts.
func RenderPrompts(in PromptInput) (Prompts, error) {
	vars := in.vars()
	sys, err := prompt.Execute(templates, systemTemplate, vars)
	if err != nil {
		return Prompts{}, err
	}
	usr, err := prompt.Execute(templates, userTemplate, vars)
	if err != nil {
		return Prompts{}, err
	}
	return Prompts{System: sys, User: usr}, nil
}

// ScaffoldingTokens renders the prompts of in with an empty diff and
// returns their request estimate (tokens.RequestTokens: both messages plus
// the framing allowance), the PromptTokens of the diff budget (P5 §1.2
// step 4). The question is part of the scaffolding.
func ScaffoldingTokens(in PromptInput, factor float64) (int, error) {
	in.Diff = ""
	p, err := RenderPrompts(in)
	if err != nil {
		return 0, err
	}
	return tokens.RequestTokens(p.System, p.User, factor), nil
}
