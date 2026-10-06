// Package llmrun holds the YAML-free parts of the LLM-backed tool pipelines
// that pr_review and pr_ask share: the coverage accounting (X-3), the
// request-size guard, the notes both pipelines add, the classified pipeline
// errors (X-6) and the publish-failure handling.
//
// It exists so that internal/ask can reuse this machinery without importing
// internal/review, which depends on the YAML repair code (spec P5 §3).
// Nothing here may import YAML or repair code.
package llmrun

import "errors"

// ErrorClass is the X-6 allowlist class of a pipeline error.
type ErrorClass string

// Shared error classes.
const (
	// ClassConfigInvalid: the server runs with an invalid configuration;
	// nothing was sent anywhere.
	ClassConfigInvalid ErrorClass = "config_invalid"
	// ClassDoesNotFit: the prompt and the output reserve leave no room for
	// the diff, or nothing of it fits. pr_review and pr_ask share it, so
	// the value names the diff, not a tool (architect review, P5 C1).
	ClassDoesNotFit ErrorClass = "diff_does_not_fit"
)

// Fixed client-facing sentences of the shared classes (X-6). The
// config-invalid sentence is the one the other tools return
// (tools.ConfigInvalidMessage); the does-not-fit sentence is the one diag
// diff prints.
const (
	ConfigInvalidSentence = "review-mcp configuration is invalid; call server_info for the list of problems"
	DoesNotFitSentence    = "the pull request diff does not fit the configured context window; " +
		"raise llm.context_window (REVIEW_MCP_LLM_CONTEXT_WINDOW) or narrow the pull request"
)

// ErrFallbackEligible marks the failures DQ-9 lets a later fallback-model
// chain retry: the diff does not fit, nothing is left after budgeting, or
// (pr_review) the answer has no non-empty review mapping. v1 has no
// fallback chain; classified errors match it with errors.Is.
var ErrFallbackEligible = errors.New("review: fallback eligible")

// Class sentinels of the shared classes, for errors.Is.
var (
	ErrConfigInvalid = &Error{Class: ClassConfigInvalid, Message: ConfigInvalidSentence}
	ErrDoesNotFit    = &Error{Class: ClassDoesNotFit, Message: DoesNotFitSentence, Eligible: true}
)

// Error is a classified pipeline error. Its text is a fixed sentence; it
// never carries prompt, answer, diff or PR content.
type Error struct {
	Class ErrorClass
	// Message is the fixed client-facing sentence. Empty falls back to the
	// sentence of a shared class.
	Message string
	// Eligible marks the class as ErrFallbackEligible.
	Eligible bool
	cause    error
}

// Error returns the fixed sentence of the class.
func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	switch e.Class {
	case ClassConfigInvalid:
		return ConfigInvalidSentence
	case ClassDoesNotFit:
		return DoesNotFitSentence
	}
	return "request failed"
}

// UserMessage is the sentence an entry point may show to a client (X-6).
func (e *Error) UserMessage() string { return e.Error() }

// Unwrap returns the underlying cause (for example tokens.ErrDoesNotFit),
// for errors.Is; the cause's text is never shown.
func (e *Error) Unwrap() error { return e.cause }

// Is reports whether target is an *Error of the same class, or
// ErrFallbackEligible for an eligible class.
func (e *Error) Is(target error) bool {
	if target == ErrFallbackEligible {
		return e.Eligible || e.Class == ClassDoesNotFit
	}
	t, ok := target.(*Error)
	return ok && t.Class == e.Class
}

// WithCause returns a copy of e that wraps cause.
func (e *Error) WithCause(cause error) *Error {
	c := *e
	c.cause = cause
	return &c
}

// DoesNotFit returns the does-not-fit error wrapping cause.
func DoesNotFit(cause error) *Error { return ErrDoesNotFit.WithCause(cause) }
