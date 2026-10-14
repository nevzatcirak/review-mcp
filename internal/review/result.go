package review

import (
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// Notes the pipeline adds (§4.3). Conversion adds the dropped-finding
// notes.
const (
	// NoteNoReviewableChanges: every file was filtered, skipped or empty,
	// so the model was not called (step 5).
	NoteNoReviewableChanges = llmrun.NoteNoReviewableChanges
	// NoteTruncated: the model stopped at its output limit (step 7).
	NoteTruncated = "The model's answer was cut off by its output limit; the review may be incomplete."
	// NoteReasked: the first answer was unparseable and the review comes
	// from the one re-ask (step 8).
	NoteReasked = "The model's first answer could not be parsed as YAML; this review comes from a second attempt."
	// NoteDiffTrimmed: the request-size guard shortened the diff (step 6).
	NoteDiffTrimmed = llmrun.NoteDiffTrimmed
	// noteClippedFormat: files included only in part (step 12).
	noteClippedFormat = llmrun.NoteClippedFormat
)

// Result is the outcome of one review (§4.3 step 12). It is the MCP
// structuredContent of pr_review (DQ-6); ResultSchema describes it.
type Result struct {
	PR PRInfo `json:"pr"`
	// EnabledFields are the review fields asked for, in descriptor order.
	EnabledFields []string `json:"enabled_fields"`
	Review        *Review  `json:"review"`
	Coverage      Coverage `json:"coverage"`
	// Notes are user-facing sentences: truncation, dropped findings,
	// clipped or trimmed files, the re-ask.
	Notes    []string `json:"notes"`
	Metadata Metadata `json:"metadata"`
	// CI is the head commit's CI summary (X-31); nil, and omitted, when
	// review.ci_status is off.
	CI *CI `json:"ci,omitempty"`
	// Publish is set when publishing was requested.
	Publish *PublishResult `json:"publish,omitempty"`
	// Focus echoes the focus of a focused run (X-33): the known value, or
	// the free text as cleaned (NormalizeFocus); omitted without one.
	Focus string `json:"focus,omitempty"`
	// Incremental says whether the run reviewed the whole pull request or
	// only the files that changed since the last review (X-36). Run always
	// sets it; it is a pointer so that the shared JSON form of a result
	// built elsewhere omits it.
	Incremental *Incremental `json:"incremental,omitempty"`
	// Reviewed is the commit record of the overview (X-36). It is not part
	// of the tool result: the renderers show it and the publish writes it.
	Reviewed ReviewedCommit `json:"-"`
}

// ReviewedCommit is the head commit a run reviewed, as the overview records
// it (X-36). An empty SHA records nothing.
type ReviewedCommit struct {
	// SHA is the recorded commit: the run's head, or for a merged publish
	// (X-33) the one the previous overview recorded.
	SHA string
	// Since is the commit an incremental run continues from
	// (Incremental.Since); "" for a full review.
	Since string
}

// PRInfo identifies the reviewed pull request.
type PRInfo struct {
	Kind string `json:"kind"`
	// URL is the PR URL with credentials and query values removed.
	URL    string `json:"url"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
	// HeadSHA is the head commit the review describes; the published
	// overview shows its short form.
	HeadSHA string `json:"head_sha,omitempty"`
}

// Coverage accounts for every changed file (X-3). The types live in
// internal/llmrun, shared with pr_ask; the JSON field names are unchanged.
type (
	Coverage     = llmrun.Coverage
	OmittedFiles = llmrun.OmittedFiles
	SkippedFile  = llmrun.SkippedFile
)

// Metadata describes the run. It holds names and numbers only.
type Metadata struct {
	Model string `json:"model"`
	// ContextWindow is llm.context_window.
	ContextWindow int `json:"context_window"`
	// PromptTokens is the request estimate of the scaffolding (empty diff).
	PromptTokens int `json:"prompt_tokens"`
	// DiffTokens is the estimate of the prepared diff.
	DiffTokens int `json:"diff_tokens"`
	// RequestTokens is the estimate of the final request (0 without an LLM
	// call).
	RequestTokens int  `json:"request_tokens"`
	FastPath      bool `json:"fast_path"`
	// LLMCalls is the number of chat completions made (0, 1 or 2).
	LLMCalls int `json:"llm_calls"`
	// RepairTactic is how the accepted answer was parsed: "direct" or a
	// repair tactic name; empty without an LLM call.
	RepairTactic string `json:"repair_tactic"`
	Reasked      bool   `json:"reasked"`
	Truncated    bool   `json:"truncated"`
	DiffTrimmed  bool   `json:"diff_trimmed"`
	// ReviewedAt is the run time, RFC 3339 in UTC (Deps.Clock).
	ReviewedAt string `json:"reviewed_at"`
	// AlreadyDiscussed is the "already discussed" count of X-13, shown in
	// the published overview when greater than 0. WP-PR-7e fills it; until
	// then it is 0.
	AlreadyDiscussed int `json:"already_discussed,omitempty"`
}

// PublishResult is the outcome of publishing (step 13): the posted or
// updated overview comment, or the classified error, and the inline
// comments. A failed publish never discards the review. The first four
// fields are those of llmrun.PublishResult, which pr_ask shares.
type PublishResult struct {
	Published bool   `json:"published"`
	CommentID string `json:"comment_id,omitempty"`
	URL       string `json:"url,omitempty"`
	Error     string `json:"error,omitempty"`
	// Updated is true when the overview of an earlier run was edited in
	// place (X-12) instead of a new one being posted.
	Updated bool `json:"updated,omitempty"`
	// Inline is set when the findings were considered for inline comments:
	// inline findings are on and the overview was posted, or an earlier
	// overview was found to update.
	Inline *InlineSummary `json:"inline,omitempty"`
}

// InlineSummary counts the findings of an inline publish (spec P7 §3.3).
// Every finding of the run is in exactly one of the counts; a finding
// carried forward from an earlier review (KeyIssue.CarriedFrom) is in none:
// it is never considered for an inline comment.
type InlineSummary struct {
	// Posted findings have an inline comment; KeyIssue.InlineURL links it
	// when the server reported a URL.
	Posted int `json:"posted"`
	// SkippedDuplicate findings were already on the PR and were not posted
	// again: one of review-mcp's own finding comments is at their place (an
	// open or resolved thread that is not outdated; X-29) or carries their
	// fingerprint. KeyIssue.InlineURL links that comment when its URL is
	// known.
	SkippedDuplicate int `json:"skipped_duplicate"`
	// Unanchorable findings have no line in the PR's diff; they are in the
	// overview only.
	Unanchorable int `json:"unanchorable"`
	// Failed findings were anchorable but their inline comment could not be
	// posted; they are in the overview only.
	Failed int `json:"failed"`
}

func nonNil[T any](s []T) []T { return llmrun.NonNil(s) }

// buildCoverage combines the prepared diff's accounting with the
// provider's skips (llmrun.BuildCoverage).
func buildCoverage(p *diffpipe.Prepared, f *filter.Filter) Coverage {
	return llmrun.BuildCoverage(p, f)
}
