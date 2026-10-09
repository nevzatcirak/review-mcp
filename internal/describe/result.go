package describe

import (
	"fmt"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// Notes the pipeline adds (v2 spec §3). They are fixed sentences with
// counts and part numbers only; none carries model, PR or diff text.
const (
	// NoteNoReviewableChanges: every file was filtered, skipped or empty,
	// so the model was not called.
	NoteNoReviewableChanges = llmrun.NoteNoReviewableChanges
	// NoteTruncated: a model answer was cut off by its output limit.
	NoteTruncated = "The model's answer was cut off by its output limit; the description may be incomplete."
	// NoteReasked: an answer was unparseable and the one re-ask was used.
	NoteReasked = "The model's first answer could not be parsed as YAML; the description comes from a second attempt."
	// NoteDiffTrimmed: the request-size guard shortened the diff.
	NoteDiffTrimmed = llmrun.NoteDiffTrimmed
	// NoteReduceFailed: the reduce call of a description in parts failed
	// (v2 spec §3.3, verbatim); type and title are null and the
	// description is the files' titles.
	NoteReduceFailed = "The summary could not be generated; the walkthrough lists the described files."
	// NoteReduceWithoutSummaries: the reduce call was sent the files'
	// titles only, because the summaries did not fit the context window.
	NoteReduceWithoutSummaries = "The summary was generated from the files' titles only; their summaries did not fit the context window."
	// NoteCommitsUnavailable: the commit messages could not be read; the
	// prompts have no commit-message block.
	NoteCommitsUnavailable = "The commit messages could not be read from the provider; the description was generated without them."
)

// AllowedTypes are the PR types of the schema (upstream's PRType values),
// in schema order. A type outside them is dropped with a note.
var AllowedTypes = []string{"Bug fix", "Tests", "Enhancement", "Documentation", "Other"}

// SkipNotReturned is the coverage skip reason of a file whose diff the model
// was shown but that got no usable walkthrough entry (Y-7): it is not
// described. llmrun.Tally counts it as not reviewed, as it does every reason
// it does not know.
const SkipNotReturned = "not_returned"

// MaxLabelRunes caps a file label (v2 spec §3.4).
const MaxLabelRunes = 40

// noteFailedPart: a part whose model call failed (X-19). class is a fixed
// error class (llmrun.FailureClass), never error text.
func noteFailedPart(i, n int, class string) string {
	return fmt.Sprintf("Part %d of %d failed (%s); its files were not described.", i, n, class)
}

// noteTypesDropped: type values outside AllowedTypes.
func noteTypesDropped(n int) string {
	return llmrun.CountPhrase(n, "type value", "type values") + " outside the allowed list (" +
		strings.Join(AllowedTypes, ", ") + ") " + wasWere(n) + " dropped."
}

// noteUnknownFiles: walkthrough entries whose path was not among the files
// the call was shown (v2 spec §3.4).
func noteUnknownFiles(n int) string {
	return llmrun.CountPhrase(n, "walkthrough entry", "walkthrough entries") + " for a file that was not in the diff shown to the model " +
		wasWere(n) + " dropped."
}

// noteDuplicateFiles: a second entry for a path already described.
func noteDuplicateFiles(n int) string {
	return llmrun.CountPhrase(n, "duplicate walkthrough entry", "duplicate walkthrough entries") + " " + wasWere(n) +
		" dropped; the first entry of each file is kept."
}

// noteNotReturned: files the model was shown and did not describe (Y-7).
func noteNotReturned(n int) string {
	return llmrun.CountPhrase(n, "file was", "files were") +
		" shown to the model but got no walkthrough entry; listed as not described (see Coverage)."
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

// Coverage is the coverage object of pr_review and pr_ask (internal/llmrun).
// For pr_describe "reviewed" reads "described": ReviewedFiles counts the
// described files, NotReviewedFiles the files not described.
type Coverage = llmrun.Coverage

// Result is the outcome of one pr_describe call (v2 spec §3.6). It is the
// MCP structuredContent of pr_describe; ResultSchema describes it.
type Result struct {
	// Title is the generated title; nil when it was not generated (the
	// reduce call failed, the answer had none, or no model call was made).
	Title *string `json:"title"`
	// Type is the validated list of PR types (AllowedTypes); nil when it
	// was not generated.
	Type []string `json:"type"`
	// Description is the generated summary (up to four bullets); nil when
	// it was not generated. After a failed reduce call it is the described
	// files' titles, one per line.
	Description *string `json:"description"`
	// Files is the walkthrough: one entry per described file, in the order
	// the model returned them, parts in order.
	Files    []File   `json:"files"`
	Coverage Coverage `json:"coverage"`
	// Notes are user-facing sentences.
	Notes    []string `json:"notes"`
	Metadata Metadata `json:"metadata"`
}

// File is one walkthrough entry.
type File struct {
	Path string `json:"path"`
	// Title is upstream's changes_title, Summary its changes_summary and
	// Label its label (free text, one line, at most MaxLabelRunes).
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Label   string `json:"label"`
}

// Metadata describes the run. It holds names and numbers only.
type Metadata struct {
	Model string `json:"model"`
	// ContextWindow is the window budgeted for (X-15).
	ContextWindow int `json:"context_window"`
	// PromptTokens is the request estimate of the scaffolding (empty diff),
	// with the part line for a description in parts.
	PromptTokens int `json:"prompt_tokens"`
	// DiffTokens is the estimate of the prepared diff, summed over the
	// parts.
	DiffTokens int `json:"diff_tokens"`
	// RequestTokens is the estimate of the largest request with a diff (0
	// without a model call).
	RequestTokens int  `json:"request_tokens"`
	FastPath      bool `json:"fast_path"`
	// LLMCalls is the number of chat completions made, re-asks and the
	// reduce call included.
	LLMCalls int `json:"llm_calls"`
	// CommitMessages is the number of commit messages read from the
	// provider (before the token cap).
	CommitMessages int `json:"commit_messages"`
	// RepairTactic is how the first accepted answer was parsed: "direct" or
	// a repair tactic name; empty without an LLM call.
	RepairTactic string `json:"repair_tactic"`
	Reasked      bool   `json:"reasked"`
	Truncated    bool   `json:"truncated"`
	DiffTrimmed  bool   `json:"diff_trimmed"`
}
