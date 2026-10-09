package improve

import (
	"fmt"
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// Notes the pipeline adds (v2 spec §1). They are fixed sentences with
// counts, part numbers and configured numbers only; none carries model, PR
// or diff text.
const (
	// NoteNoReviewableChanges: every file was filtered, skipped or empty,
	// so the model was not called.
	NoteNoReviewableChanges = llmrun.NoteNoReviewableChanges
	// NoteTruncated: a model answer was cut off by its output limit.
	NoteTruncated = "A model answer was cut off by its output limit; the suggestions may be incomplete."
	// NoteReasked: an answer was unparseable and the one re-ask of that
	// call was used.
	NoteReasked = "A model answer could not be parsed as YAML at first; that call's answer comes from a second attempt."
	// NoteDiffTrimmed: the request-size guard shortened the diff.
	NoteDiffTrimmed = llmrun.NoteDiffTrimmed
	// NoteDiscussionUnreadable: the PR's comments or the token's user could
	// not be read, so the suggestions may repeat points already raised.
	NoteDiscussionUnreadable = "The existing PR discussion could not be read; suggestions may repeat it."
	// NoteNotScoredOneCall is the self-review failure note of a run in one
	// call: its suggestions are kept unscored.
	//
	// DESIGN-QUESTION: the spec words the failure note per part ("Part I's
	// suggestions were not scored ..."); how does a run in one call say it?
	// — chose the same sentence with "The suggestions" for "Part I's
	// suggestions", because a run in one call has no part numbers anywhere
	// else in its result either.
	NoteNotScoredOneCall = "The suggestions were not scored (the self-review call failed)."
)

// noteNotScoredPart: the self-review call of part i failed; its suggestions
// are kept unscored (v2 spec §1.4, verbatim).
func noteNotScoredPart(i int) string {
	return fmt.Sprintf("Part %d's suggestions were not scored (the self-review call failed).", i)
}

// noteFailedPart: a part whose suggestion call failed (X-19). class is a
// fixed error class (llmrun.FailureClass), never error text.
func noteFailedPart(i, n int, class string) string {
	return fmt.Sprintf("Part %d of %d failed (%s); its files were not reviewed.", i, n, class)
}

// noteDiscussionLeftOut: threads that did not fit the discussion budget
// (pr_review's sentence).
func noteDiscussionLeftOut(n int) string {
	return llmrun.CountPhrase(n, "discussion thread was", "discussion threads were") +
		" left out of the prompt to stay within the discussion token budget."
}

// noteScoreDropped: suggestions whose self-review score is below
// improve.min_score (v2 spec §1.4, verbatim with the number and the
// threshold).
func noteScoreDropped(n, minScore int) string {
	return llmrun.CountPhrase(n, "suggestion was", "suggestions were") +
		" dropped by the self-review score (below " + strconv.Itoa(minScore) + ")."
}

// noteUnscored: suggestions the self-review answer gave no usable score
// (it did not mention them, or its entry did not match them), kept
// unscored; never silently dropped (v2 spec §1.4).
func noteUnscored(n int) string {
	return llmrun.CountPhrase(n, "suggestion got", "suggestions got") +
		" no usable self-review score and " + isAre(n) + " kept unscored."
}

// noteUnmatchedEntries: self-review entries that matched no suggestion (a
// number out of range or claimed twice, or a file or summary that differs
// from the numbered suggestion's).
func noteUnmatchedEntries(n int) string {
	return llmrun.CountPhrase(n, "self-review entry", "self-review entries") +
		" did not match a suggestion and " + wasWere(n) + " ignored."
}

// noteUnknownFiles: suggestions for a file the call was not shown (v2 spec
// §1.6).
func noteUnknownFiles(n int) string {
	return llmrun.CountPhrase(n, "suggestion", "suggestions") + " for a file that was not in the diff shown to the model " +
		wasWere(n) + " dropped."
}

// noteNoChange: suggestions whose improved code equals the existing code
// after whitespace normalisation (v2 spec §1.6).
func noteNoChange(n int) string {
	return llmrun.CountPhrase(n, "suggestion", "suggestions") + " whose improved code is the same as the existing code " +
		wasWere(n) + " dropped (no change)."
}

// noteIncomplete: suggestions without a file, a summary, the existing code
// or the improved code.
func noteIncomplete(n int) string {
	return llmrun.CountPhrase(n, "suggestion", "suggestions") + " without a file, a summary, or the existing or improved code " +
		wasWere(n) + " dropped."
}

// noteDuplicates: suggestions whose fingerprint (X-13) an earlier one has.
func noteDuplicates(n int) string {
	return llmrun.CountPhrase(n, "duplicate suggestion was", "duplicate suggestions were") +
		" dropped; the first of each is kept."
}

// noteTotalCap: suggestions beyond improve.max_suggestions after the merge
// (the X-19 sentence of pr_review's review.max_total_findings).
func noteTotalCap(n int) string {
	return llmrun.CountPhrase(n, "further suggestion was", "further suggestions were") +
		" not shown because of improve.max_suggestions."
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// MaxLabelRunes caps a suggestion label (v2 spec §1.6).
const MaxLabelRunes = 40

// Coverage is the coverage object of the other LLM tools (internal/llmrun).
type Coverage = llmrun.Coverage

// Result is the outcome of one pr_improve call (v2 spec §1.7). It is the
// MCP structuredContent of pr_improve; ResultSchema describes it.
type Result struct {
	// Suggestions are the merged suggestions, ranked across the parts: the
	// scored ones by score descending (ties: part order, then the model's
	// order), then the unscored ones in part order; without duplicates,
	// capped at improve.max_suggestions.
	Suggestions []Suggestion `json:"suggestions"`
	Coverage    Coverage     `json:"coverage"`
	// Notes are user-facing sentences.
	Notes    []string `json:"notes"`
	Metadata Metadata `json:"metadata"`
}

// Suggestion is one code suggestion.
type Suggestion struct {
	// File is upstream's relevant_file: one of the files whose diff the
	// suggestion's call was shown.
	File string `json:"file"`
	// Language is upstream's language, one line.
	Language string `json:"language"`
	// Label is upstream's label: one line, at most MaxLabelRunes runes.
	Label string `json:"label"`
	// Summary is upstream's one_sentence_summary, one line.
	Summary string `json:"summary"`
	// Content is upstream's suggestion_content.
	Content string `json:"content"`
	// ExistingCode is the code the suggestion replaces, as the model quoted
	// it, and ImprovedCode its replacement.
	ExistingCode string `json:"existing_code"`
	ImprovedCode string `json:"improved_code"`
	// StartLine and EndLine are the new-file lines of ExistingCode as the
	// self-review answer gave them (relevant_lines_start/end); nil when it
	// gave none, or none that make a range. WP-2g checks and corrects them.
	StartLine *int `json:"start_line"`
	EndLine   *int `json:"end_line"`
	// Score is the self-review score, 0 to 10; nil for an unscored
	// suggestion (its part's self-review call failed, or the answer gave it
	// no usable score).
	Score *int `json:"score"`
	// Why is the self-review's reason for the score; "" when unscored.
	Why string `json:"why"`
	// Verified reports that ExistingCode was found in the head file at
	// StartLine to EndLine (Y-10). It is filled by WP-2g; until then it is
	// always false.
	Verified bool `json:"verified"`
	// Anchor is where the suggestion is posted inline (Y-11). It is filled
	// by WP-2h; until then it is always nil.
	Anchor *Anchor `json:"anchor"`
}

// Anchor is the inline anchor of a published suggestion. It is a
// placeholder until WP-2h defines it: no field, and Suggestion.Anchor is
// always nil.
type Anchor struct{}

// Metadata describes the run. It holds names and numbers only.
type Metadata struct {
	Model string `json:"model"`
	// ContextWindow is the window budgeted for (X-15).
	ContextWindow int `json:"context_window"`
	// PromptTokens is the PromptTokens of the diff budget: the larger of
	// the suggestion scaffolding (empty diff, with the part line and the
	// repository-context reservation of a run in parts) and the self-review
	// scaffolding plus its room for the suggestions (diffPromptTokens).
	PromptTokens int `json:"prompt_tokens"`
	// DiffTokens is the estimate of the prepared diff, summed over the
	// parts.
	DiffTokens int `json:"diff_tokens"`
	// RequestTokens is the estimate of the largest suggestion request (0
	// without a model call).
	RequestTokens int  `json:"request_tokens"`
	FastPath      bool `json:"fast_path"`
	// LLMCalls is the number of chat completions made: the suggestion
	// calls, the self-review calls and the re-asks.
	LLMCalls int `json:"llm_calls"`
	// SelfReviewCalls is the part of LLMCalls made by the self-review
	// calls, re-asks included.
	SelfReviewCalls int `json:"self_review_calls"`
	// RepairTactic is how the first accepted answer was parsed: "direct" or
	// a repair tactic name; empty without an LLM call.
	RepairTactic string `json:"repair_tactic"`
	Reasked      bool   `json:"reasked"`
	Truncated    bool   `json:"truncated"`
	DiffTrimmed  bool   `json:"diff_trimmed"`
	// AlreadyDiscussed is the number of discussion threads shown to the
	// model (X-13).
	AlreadyDiscussed int `json:"already_discussed"`
}
