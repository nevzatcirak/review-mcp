package review

import (
	"embed"
	"fmt"
	"text/template"

	"github.com/nevzatcirak/review-mcp/internal/prompt"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// The review prompts are adapted from PR-Agent
// (pr_agent/settings/pr_reviewer_prompts.toml and prompt_fragments.toml at
// commit 8e5a9295973b24af4b70cafd0b660a230811ef9e, MIT; see NOTICE). The
// template files list the adaptations in their header comments.
//
//go:embed prompts/system.tmpl prompts/user.tmpl
var promptFiles embed.FS

// Template names.
const (
	systemTemplate = "system.tmpl"
	userTemplate   = "user.tmpl"
)

// ResponseLine is the user prompt's final instruction line, followed by an
// open ```yaml fence (DQ-7). The re-ask note is inserted before it.
const ResponseLine = "Response (should be a valid YAML, and nothing else):"

var templates = template.Must(prompt.New("review").ParseFS(promptFiles, "prompts/*.tmpl"))

// PromptInput is everything the review prompts depend on.
type PromptInput struct {
	Toggles     Toggles
	MaxFindings int
	// ExtraInstructions are the effective extra instructions (the per-call
	// argument, else review.extra_instructions), before the output-language
	// instruction is added.
	ExtraInstructions string
	// RepoInstructions is the rendered block of the repository's extra
	// instructions (projctx.RepoGuidance); empty omits it. It follows the
	// extra instructions in the system prompt, and is part of the
	// scaffolding.
	RepoInstructions string
	// DocsStyle is the rendered documentation-style block (docsStyleBlock);
	// empty omits it. Only a review with focus docs has one.
	DocsStyle string
	// Language is the effective output language (the per-call argument,
	// else output.language). Empty means en-US.
	Language string
	Title    string
	// Branch is the PR's source branch (upstream get_pr_branch).
	Branch string
	// Description is the PR description, already clipped
	// (tokens.ClipDescription); the template trims it.
	Description string
	// Issue is the rendered issue block (issuectx: its fixed header and the
	// fenced issues, X-42); empty omits it. It is untrusted text placed in
	// the room the diff leaves, and it comes with Toggles.Issue, which asks
	// for issue_compliance and adds IssueComplianceSentence to the system
	// prompt.
	Issue string
	// EarlierVerdict is the rendered verdict of the last review
	// (earlierVerdictBlock) of an incremental run with an issue, for the
	// files the run does not show (spec V-2 §2.4); empty omits it. It
	// follows the issue block.
	EarlierVerdict string
	// Related is the rendered related block (RelatedHeader and the fenced
	// related pull requests, X-41); empty omits it. It follows the issue and
	// precedes the discussion.
	//
	// DESIGN-QUESTION (WP-11a DQ-4): the spec orders the related block after
	// the issue block and before repository context, but those two sit apart
	// in the user prompt (the discussion, the CI results, the guidelines and
	// the file tree are between them). The block goes right after the issue
	// blocks: both are context the caller gave about the change, and the
	// budget order (issue, related, repository context) is the spec's.
	// Recommendation: keep.
	Related string
	// Discussion is the rendered existing-discussion block (DiscussionHeader
	// and the fenced threads, see renderDiscussion); empty omits it. It is
	// third-party text, and part of the scaffolding the diff budget reserves.
	Discussion string
	// CI is the rendered CI block (CIHeader and the fenced failing and
	// pending checks, see CIBlock); empty omits it. It is third-party text,
	// and part of the scaffolding.
	CI string
	// Guidelines is the rendered guidelines block (projctx.GuidelinesHeader
	// and the fenced guidelines read at the base revision); FileTree the
	// rendered file-tree block (projctx.TreeHeader and the fenced listing of
	// the touched directories). Empty omits each. They are repository text
	// (X-32), and part of the scaffolding.
	Guidelines string
	FileTree   string
	// RepoContext is the rendered repository-context block (repoctx.Header
	// and the fenced uses of the changed symbols, see repoctx.Render); empty
	// omits it. It is repository text, and part of the scaffolding.
	RepoContext string
	// Focus is the normalized focus of a focused run (X-33,
	// NormalizeFocus); empty reviews everything and renders as before. A
	// known value adds its fixed sentence to the system prompt; a free text
	// adds the free-text sentence there and its fenced block (FocusHeader)
	// to the user prompt.
	Focus string
	// Incremental marks an incremental run (X-36), whose diff holds only
	// the files that changed since the last review: the system prompt
	// carries IncrementalSentence. False renders as before.
	Incremental bool
	// Date is the prompt date (prompt.Date).
	Date string
	// PartHeader is the part line of a review in several parts (PartHeader);
	// empty for a review in one call, which then renders as before.
	PartHeader string
	// Diff is the prepared diff; empty for the scaffolding measurement.
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
		"extra_instructions":  prompt.WithOutputLanguage(in.ExtraInstructions, in.Language),
		"repo_instructions":   in.RepoInstructions,
		"docs_style":          in.DocsStyle,
		"schema":              SchemaText(in.Toggles, in.MaxFindings),
		"example":             ExampleYAML(in.Toggles),
		"date":                in.Date,
		"title":               in.Title,
		"branch":              in.Branch,
		"description":         in.Description,
		"issue":               in.Issue,
		"issue_verdict":       in.EarlierVerdict,
		"related":             in.Related,
		"related_instruction": relatedInstruction(in.Related),
		"issue_instruction":   issueInstruction(in.Issue),
		"discussion":          in.Discussion,
		"ci":                  in.CI,
		"guidelines":          in.Guidelines,
		"file_tree":           in.FileTree,
		"repo_context":        in.RepoContext,
		"focus":               focusInstruction(in.Focus),
		"focus_block":         focusBlock(in.Focus),
		"incremental":         incrementalInstruction(in.Incremental),
		"part_header":         in.PartHeader,
		"diff":                in.Diff,
	}
}

// relatedInstruction is RelatedInstruction with a related block, ""
// otherwise.
func relatedInstruction(block string) string {
	if block != "" {
		return RelatedInstruction
	}
	return ""
}

// issueInstruction is the system prompt's paragraph of a run with an issue
// block, "" otherwise.
func issueInstruction(block string) string {
	if block != "" {
		return IssueComplianceSentence
	}
	return ""
}

// incrementalInstruction is the system prompt's sentence of an incremental
// run, "" otherwise.
func incrementalInstruction(on bool) string {
	if on {
		return IncrementalSentence
	}
	return ""
}

// PartHeader is the line the user prompt carries before the diff when a
// review is made in n > 1 parts (v1.1 spec WP-11e2, verbatim); i is the
// 1-based part number.
func PartHeader(i, n int) string {
	return fmt.Sprintf("This pull request is large and is reviewed in %d parts. This is part %d of %d. "+
		"Review only the files in the diff below; the other files are reviewed separately.", n, i, n)
}

// RenderPrompts renders the system and user prompts.
func RenderPrompts(in PromptInput) (Prompts, error) {
	return renderWith(templates, in.vars())
}

// ScaffoldingTokens renders the prompts of in with an empty diff and
// returns their request estimate (tokens.RequestTokens: both messages plus
// the framing allowance), the PromptTokens of the diff budget (§4.3 step
// 4).
//
// DESIGN-QUESTION: does PromptTokens include the 48-token message framing
// allowance? — chose yes (it is tokens.RequestTokens of the empty-diff
// prompts) because the step 6 guard checks tokens.RequestTokens of the
// final prompts, so leaving the framing out would let the budget admit a
// diff the guard then has to trim.
func ScaffoldingTokens(in PromptInput, factor float64) (int, error) {
	in.Diff = ""
	p, err := RenderPrompts(in)
	if err != nil {
		return 0, err
	}
	return tokens.RequestTokens(p.System, p.User, factor), nil
}

func renderWith(set *template.Template, vars map[string]any) (Prompts, error) {
	sys, err := prompt.Execute(set, systemTemplate, vars)
	if err != nil {
		return Prompts{}, err
	}
	usr, err := prompt.Execute(set, userTemplate, vars)
	if err != nil {
		return Prompts{}, err
	}
	return Prompts{System: sys, User: usr}, nil
}
