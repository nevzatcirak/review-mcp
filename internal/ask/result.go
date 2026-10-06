package ask

import "github.com/nevzatcirak/review-mcp/internal/llmrun"

// Notes the pipeline adds (spec P5 §1.2).
const (
	// NoteNoReviewableChanges: every file was filtered, skipped or empty,
	// so the model was not called (step 5).
	NoteNoReviewableChanges = llmrun.NoteNoReviewableChanges
	// NoteTruncated: the model stopped at its output limit (step 7). The
	// answer is kept as it is.
	NoteTruncated = "The answer was cut off by the model's output limit."
	// NoteDiffTrimmed: the request-size guard shortened the diff (step 6).
	NoteDiffTrimmed = llmrun.NoteDiffTrimmed
)

// Coverage, OmittedFiles, SkippedFile and PublishResult are shared with
// pr_review (internal/llmrun); the JSON field names are the same.
type (
	Coverage      = llmrun.Coverage
	OmittedFiles  = llmrun.OmittedFiles
	SkippedFile   = llmrun.SkippedFile
	PublishResult = llmrun.PublishResult
)

// Result is the outcome of one question (spec P5 §1.2 step 8). It is the
// MCP structuredContent of pr_ask.
type Result struct {
	// Question is the validated question (trimmed, valid UTF-8).
	Question string `json:"question"`
	// Answer is the model's trimmed answer; empty when no model call was
	// made (Metadata.LLMCalls is 0).
	Answer   string   `json:"answer"`
	Coverage Coverage `json:"coverage"`
	// Notes are user-facing sentences: truncation, clipped or trimmed
	// files, nothing left to ask about.
	Notes    []string `json:"notes"`
	Metadata Metadata `json:"metadata"`
	// Publish is set when publishing was requested.
	Publish *PublishResult `json:"publish,omitempty"`
}

// Metadata describes the run. It holds names and numbers only.
type Metadata struct {
	Model string `json:"model"`
	// ContextWindow is llm.context_window.
	ContextWindow int `json:"context_window"`
	// PromptTokens is the request estimate of the scaffolding (empty diff,
	// question included).
	PromptTokens int `json:"prompt_tokens"`
	// DiffTokens is the estimate of the prepared diff.
	DiffTokens int `json:"diff_tokens"`
	// RequestTokens is the estimate of the final request (0 without an LLM
	// call).
	RequestTokens int  `json:"request_tokens"`
	FastPath      bool `json:"fast_path"`
	// LLMCalls is the number of chat completions made (0 or 1).
	LLMCalls    int  `json:"llm_calls"`
	Truncated   bool `json:"truncated"`
	DiffTrimmed bool `json:"diff_trimmed"`
}
