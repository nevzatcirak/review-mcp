package review

import "github.com/nevzatcirak/review-mcp/internal/llmrun"

// ErrorClass is the X-6 allowlist class of a review pipeline error.
type ErrorClass = llmrun.ErrorClass

// Error classes. The config-invalid and does-not-fit classes are shared
// with pr_ask (internal/llmrun).
const (
	// ClassConfigInvalid: the server runs with an invalid configuration
	// (step 1); nothing was sent anywhere.
	ClassConfigInvalid = llmrun.ClassConfigInvalid
	// ClassDoesNotFit: the prompt and the output reserve leave no room for
	// the diff, or nothing of it fits (steps 4 to 6).
	ClassDoesNotFit = llmrun.ClassDoesNotFit
	// ClassUnparseable: the answer had no non-empty review mapping, also
	// after the one re-ask (step 8).
	ClassUnparseable ErrorClass = "review_unparseable"
)

// Error is a classified review pipeline error. Its text is a fixed sentence;
// it never carries prompt, answer, diff or PR content.
type Error = llmrun.Error

// ErrFallbackEligible marks the failures DQ-9 lets a later fallback-model
// chain retry: the diff does not fit, nothing is left after budgeting, or
// the answer has no non-empty review mapping. v1 has no fallback chain; the
// pipeline uses it for the one same-model re-ask (step 8) and its classified
// errors match it with errors.Is.
var ErrFallbackEligible = llmrun.ErrFallbackEligible

// sentences are the fixed client-facing sentences of the review classes
// (X-6).
var sentences = map[ErrorClass]string{
	ClassConfigInvalid: llmrun.ConfigInvalidSentence,
	ClassDoesNotFit:    llmrun.DoesNotFitSentence,
	ClassUnparseable: "the model's answer could not be parsed as a review, also after one retry; " +
		"try again, or check that llm.model follows the YAML output instructions",
}

// Class sentinels for errors.Is.
var (
	ErrConfigInvalid = llmrun.ErrConfigInvalid
	ErrDoesNotFit    = llmrun.ErrDoesNotFit
	ErrUnparseable   = &Error{Class: ClassUnparseable, Message: sentences[ClassUnparseable], Eligible: true}
)

func doesNotFit(cause error) *Error { return llmrun.DoesNotFit(cause) }
