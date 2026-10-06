package review

// ErrorClass is the X-6 allowlist class of a review pipeline error.
type ErrorClass string

// Error classes.
const (
	// ClassConfigInvalid: the server runs with an invalid configuration
	// (step 1); nothing was sent anywhere.
	ClassConfigInvalid ErrorClass = "config_invalid"
	// ClassDoesNotFit: the prompt and the output reserve leave no room for
	// the diff, or nothing of it fits (steps 4 to 6).
	ClassDoesNotFit ErrorClass = "review_does_not_fit"
	// ClassUnparseable: the answer had no non-empty review mapping, also
	// after the one re-ask (step 8).
	ClassUnparseable ErrorClass = "review_unparseable"
)

// Class sentinels for errors.Is.
var (
	ErrConfigInvalid = &Error{Class: ClassConfigInvalid}
	ErrDoesNotFit    = &Error{Class: ClassDoesNotFit}
	ErrUnparseable   = &Error{Class: ClassUnparseable}
)

// sentences are the fixed client-facing sentences (X-6). The config-invalid
// sentence is the one the other tools return (tools.ConfigInvalidMessage);
// the does-not-fit sentence is the one diag diff prints.
var sentences = map[ErrorClass]string{
	ClassConfigInvalid: "review-mcp configuration is invalid; call server_info for the list of problems",
	ClassDoesNotFit: "the pull request diff does not fit the configured context window; " +
		"raise llm.context_window (REVIEW_MCP_LLM_CONTEXT_WINDOW) or narrow the pull request",
	ClassUnparseable: "the model's answer could not be parsed as a review, also after one retry; " +
		"try again, or check that llm.model follows the YAML output instructions",
}

// Error is a classified review pipeline error. Its text is a fixed sentence;
// it never carries prompt, answer, diff or PR content.
type Error struct {
	Class ErrorClass
	cause error
}

// Error returns the fixed sentence of the class.
func (e *Error) Error() string {
	if s, ok := sentences[e.Class]; ok {
		return s
	}
	return "review failed"
}

// UserMessage is the sentence an entry point may show to a client (X-6).
func (e *Error) UserMessage() string { return e.Error() }

// Unwrap returns the underlying cause (for example tokens.ErrDoesNotFit),
// for errors.Is; the cause's text is never shown.
func (e *Error) Unwrap() error { return e.cause }

// Is reports whether target is an *Error of the same class, or
// ErrFallbackEligible for the classes DQ-9 lets a fallback chain retry
// (the diff does not fit or nothing is left of it; the answer is
// unparseable).
func (e *Error) Is(target error) bool {
	if target == ErrFallbackEligible {
		return e.Class == ClassDoesNotFit || e.Class == ClassUnparseable
	}
	t, ok := target.(*Error)
	return ok && t.Class == e.Class
}

func doesNotFit(cause error) *Error { return &Error{Class: ClassDoesNotFit, cause: cause} }
