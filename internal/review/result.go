package review

import (
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Notes the pipeline adds (§4.3). Conversion adds the dropped-finding
// notes.
const (
	// NoteNoReviewableChanges: every file was filtered, skipped or empty,
	// so the model was not called (step 5).
	NoteNoReviewableChanges = "No reviewable changes after filtering."
	// NoteTruncated: the model stopped at its output limit (step 7).
	NoteTruncated = "The model's answer was cut off by its output limit; the review may be incomplete."
	// NoteReasked: the first answer was unparseable and the review comes
	// from the one re-ask (step 8).
	NoteReasked = "The model's first answer could not be parsed as YAML; this review comes from a second attempt."
	// NoteDiffTrimmed: the request-size guard shortened the diff (step 6).
	NoteDiffTrimmed = "The diff was shortened to fit the context window; the coverage section lists the files that are incomplete or left out."
	// noteClippedFormat: files included only in part (step 12).
	noteClippedFormat = "%s included only in part (clipped) to fit the context window."
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
	// Publish is set when publishing was requested.
	Publish *PublishResult `json:"publish,omitempty"`
}

// PRInfo identifies the reviewed pull request.
type PRInfo struct {
	Kind string `json:"kind"`
	// URL is the PR URL with credentials and query values removed.
	URL    string `json:"url"`
	Number int64  `json:"number"`
	Title  string `json:"title"`
}

// Coverage accounts for every changed file (X-3): the files whose diff the
// model saw (Included, Clipped), the files left out for budget (Omitted),
// the files skipped for other reasons (Skipped) and the filtered files
// (Filtered, with the filter's reason).
type Coverage struct {
	Included []string      `json:"included"`
	Clipped  []string      `json:"clipped"`
	Omitted  OmittedFiles  `json:"omitted"`
	Skipped  []SkippedFile `json:"skipped"`
	Filtered []SkippedFile `json:"filtered"`
}

// OmittedFiles are the files left out of the diff for budget, by change
// type (renamed files count as modified).
type OmittedFiles struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Deleted  []string `json:"deleted"`
}

// SkippedFile is a file left out for a reason other than budget.
type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

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
}

// PublishResult is the outcome of publishing (step 13): the posted comment,
// or the classified error. A failed publish never discards the review.
type PublishResult struct {
	Published bool   `json:"published"`
	CommentID string `json:"comment_id,omitempty"`
	URL       string `json:"url,omitempty"`
	Error     string `json:"error,omitempty"`
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// buildCoverage combines the prepared diff's accounting with the
// provider's skips; filtered files get the filter's reason (as diag diff
// reports them).
func buildCoverage(p *diffpipe.Prepared, f *filter.Filter) Coverage {
	c := Coverage{
		Included: nonNil(append([]string(nil), p.Included...)),
		Clipped:  nonNil(append([]string(nil), p.Clipped...)),
		Omitted: OmittedFiles{
			Added:    nonNil(append([]string(nil), p.Omitted.Added...)),
			Modified: nonNil(append([]string(nil), p.Omitted.Modified...)),
			Deleted:  nonNil(append([]string(nil), p.Omitted.Deleted...)),
		},
		Skipped:  []SkippedFile{},
		Filtered: []SkippedFile{},
	}
	for _, s := range p.Skipped {
		if s.Reason != provider.SkipFiltered {
			c.Skipped = append(c.Skipped, SkippedFile{Path: s.Path, Reason: s.Reason})
			continue
		}
		reason := provider.SkipFiltered
		if f != nil {
			if included, why := f.Explain(s.Path); !included && why != "" {
				reason = why
			}
		}
		c.Filtered = append(c.Filtered, SkippedFile{Path: s.Path, Reason: reason})
	}
	return c
}
