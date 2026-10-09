package describe

import (
	"embed"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/nevzatcirak/review-mcp/internal/prompt"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// The describe prompts are adapted from PR-Agent
// (pr_agent/settings/pr_description_prompts.toml at commit
// 8e5a9295973b24af4b70cafd0b660a230811ef9e, MIT; see NOTICE). The template
// files list the adaptations in their header comments; the reduce prompts
// (reduce_*.tmpl) are ours and say so.
//
// The upstream template has no date, so the prompts do not depend on a
// clock.
//
//go:embed prompts/system.tmpl prompts/user.tmpl prompts/reduce_system.tmpl prompts/reduce_user.tmpl
var promptFiles embed.FS

// Template names.
const (
	systemTemplate       = "system.tmpl"
	userTemplate         = "user.tmpl"
	reduceSystemTemplate = "reduce_system.tmpl"
	reduceUserTemplate   = "reduce_user.tmpl"
)

// ResponseLine is the user prompt's final instruction line, followed by an
// open ```yaml fence (DQ-7). The re-ask note is inserted before it.
const ResponseLine = "Response (should be a valid YAML, and nothing else):"

// ReaskNote is the sentence of the one re-ask after an unparseable answer
// (as pr_review's), inserted on its own line before ResponseLine.
const ReaskNote = "Note: your previous answer could not be parsed as YAML. Answer again with valid YAML only, following the schema exactly."

var templates = template.Must(prompt.New("describe").ParseFS(promptFiles, "prompts/*.tmpl"))

// PromptInput is everything the describe prompts depend on.
type PromptInput struct {
	// Language is the effective output language (the per-call argument,
	// else output.language). Empty means en-US.
	Language string
	Title    string
	// Branch is the PR's source branch (upstream get_pr_branch) and
	// TargetBranch its target branch (ours; empty omits the line).
	Branch       string
	TargetBranch string
	// Description is the PR description, already clipped
	// (tokens.ClipDescription); the template trims it.
	Description string
	// CommitMessages is the commit-message block, already numbered and
	// clipped (CommitBlock); empty omits it.
	CommitMessages string
	// FilesOnly selects the part-mode variant (Y-6): the answer is the
	// pr_files walkthrough only.
	FilesOnly bool
	// PartHeader is the part line of a description in several parts
	// (PartHeader); empty for a description in one call.
	PartHeader string
	// Diff is the prepared plain diff; empty for the scaffolding
	// measurement.
	Diff string
}

// Prompts is one rendered system and user prompt pair.
type Prompts struct {
	System string
	User   string
}

// vars builds the template data. Every key a template uses must be set
// here: the engine runs with missingkey=error.
func (in *PromptInput) vars() map[string]any {
	return map[string]any{
		// pr_describe has no extra instructions of its own: the block
		// carries the output-language instruction only (see system.tmpl).
		"extra_instructions": prompt.WithOutputLanguage("", in.Language),
		"title":              in.Title,
		"branch":             in.Branch,
		"target_branch":      in.TargetBranch,
		"description":        in.Description,
		"commit_messages":    in.CommitMessages,
		"files_only":         in.FilesOnly,
		"part_header":        in.PartHeader,
		"diff":               in.Diff,
	}
}

// PartHeader is the line the user prompt carries before the diff when a
// pull request is described in n > 1 parts; i is the 1-based part number.
// It is pr_review's sentence with "described" for "reviewed".
func PartHeader(i, n int) string {
	return fmt.Sprintf("This pull request is large and is described in %d parts. This is part %d of %d. "+
		"Describe only the files in the diff below; the other files are described separately.", n, i, n)
}

// RenderPrompts renders the system and user prompts of a call with a diff
// (one call, or one part).
func RenderPrompts(in PromptInput) (Prompts, error) {
	return render(systemTemplate, userTemplate, in.vars())
}

// ReduceInput is everything the reduce prompts depend on: the PR text of
// PromptInput (its diff fields are ignored) and the files walkthrough.
type ReduceInput struct {
	PR PromptInput
	// Walkthrough is the rendered files walkthrough (Walkthrough).
	Walkthrough string
}

// RenderReducePrompts renders the system and user prompts of the reduce
// call.
func RenderReducePrompts(in ReduceInput) (Prompts, error) {
	vars := in.PR.vars()
	vars["walkthrough"] = in.Walkthrough
	return render(reduceSystemTemplate, reduceUserTemplate, vars)
}

func render(system, user string, vars map[string]any) (Prompts, error) {
	sys, err := prompt.Execute(templates, system, vars)
	if err != nil {
		return Prompts{}, err
	}
	usr, err := prompt.Execute(templates, user, vars)
	if err != nil {
		return Prompts{}, err
	}
	return Prompts{System: sys, User: usr}, nil
}

// ScaffoldingTokens renders the prompts of in with an empty diff and
// returns their request estimate (tokens.RequestTokens), the PromptTokens
// of the diff budget.
func ScaffoldingTokens(in PromptInput, factor float64) (int, error) {
	in.Diff = ""
	p, err := RenderPrompts(in)
	if err != nil {
		return 0, err
	}
	return tokens.RequestTokens(p.System, p.User, factor), nil
}

// withReaskNote returns user with ReaskNote on its own line right before
// the last ResponseLine.
func withReaskNote(user string) (string, error) {
	i := strings.LastIndex(user, "\n"+ResponseLine)
	if i < 0 {
		return "", errors.New("describe: the user prompt has no response line")
	}
	return user[:i+1] + ReaskNote + "\n" + user[i+1:], nil
}

// CommitBlock is the commit-message block of the prompts: the messages,
// oldest first, each trimmed and numbered "N. " as upstream's GitHub
// provider does (get_commit_messages), joined by line breaks, then clipped
// to maxTokens (diff.max_commits_tokens) with tokens.ClipCommits. Empty
// messages are skipped; no message gives "".
func CommitBlock(messages []string, maxTokens int, factor float64) string {
	var lines []string
	for _, m := range messages {
		if m = strings.TrimSpace(m); m != "" {
			lines = append(lines, fmt.Sprintf("%d. %s", len(lines)+1, m))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return tokens.ClipCommits(strings.Join(lines, "\n"), maxTokens, factor)
}

// Walkthrough renders the files walkthrough of the reduce call: per file, in
// order, a "## File: '<path>'" line (the diff's file marker), "Title: " and
// the file's title, and, with summaries, "Summary:" and the summary on the
// lines below. Files are separated by a blank line.
func Walkthrough(files []File, summaries bool) string {
	var b strings.Builder
	for i, f := range files {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## File: '" + f.Path + "'\nTitle: " + f.Title)
		if summaries && f.Summary != "" {
			b.WriteString("\nSummary:\n" + f.Summary)
		}
	}
	return b.String()
}
