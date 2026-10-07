package llm

import (
	"errors"
	"strconv"
)

// ErrorClass is the X-6 allowlist class of an LLM client error.
type ErrorClass string

// Error classes.
const (
	ClassAuth           ErrorClass = "llm_auth"
	ClassNotFound       ErrorClass = "llm_not_found"
	ClassRateLimited    ErrorClass = "llm_rate_limited"
	ClassContextTooLong ErrorClass = "llm_context_too_long"
	ClassBadRequest     ErrorClass = "llm_bad_request"
	ClassUpstream       ErrorClass = "llm_upstream"
	ClassTimeout        ErrorClass = "llm_timeout"
	ClassTransport      ErrorClass = "llm_transport"
	ClassProtocol       ErrorClass = "llm_protocol"
)

// Class sentinels for errors.Is.
var (
	ErrAuth           = &Error{Class: ClassAuth}
	ErrNotFound       = &Error{Class: ClassNotFound}
	ErrRateLimited    = &Error{Class: ClassRateLimited}
	ErrContextTooLong = &Error{Class: ClassContextTooLong}
	ErrBadRequest     = &Error{Class: ClassBadRequest}
	ErrUpstream       = &Error{Class: ClassUpstream}
	ErrTimeout        = &Error{Class: ClassTimeout}
	ErrTransport      = &Error{Class: ClassTransport}
	ErrProtocol       = &Error{Class: ClassProtocol}
)

// Config-key hints (fixed strings).
const (
	hintAPIKey        = "REVIEW_MCP_LLM_API_KEY" //nolint:gosec // an environment variable name, not a credential
	hintBaseURLModel  = "llm.base_url / llm.model"
	hintContextWindow = "llm.context_window"
)

// Error is a sanitized LLM client error. It never carries response bodies,
// request content, URLs or header values. Hint names the configuration key
// to check; Detail is a short fixed phrase (never derived from a body).
type Error struct {
	Class  ErrorClass
	Status int
	Hint   string
	Detail string
	// Sentence, when set, replaces the class sentence (the probe's fixed
	// sentences, which carry their own hint).
	Sentence string
}

var sentences = map[ErrorClass]string{
	ClassAuth:           "the LLM endpoint rejected the credentials",
	ClassNotFound:       "the LLM endpoint or model was not found",
	ClassRateLimited:    "the LLM endpoint rate-limited the request",
	ClassContextTooLong: "the request is too long for the model's context window",
	ClassBadRequest:     "the LLM endpoint rejected the request",
	ClassUpstream:       "the LLM endpoint reported an internal error",
	ClassTimeout:        "the LLM request timed out",
	ClassTransport:      "could not complete the request to the LLM endpoint",
	ClassProtocol:       "the LLM endpoint sent an unexpected response",
}

// Error returns the fixed sentence for the class, then " (HTTP n)" when
// Status is non-zero, then ": <Detail>" when set, then "; check <Hint>"
// when a hint is set.
func (e *Error) Error() string {
	s, ok := sentences[e.Class]
	if !ok {
		s = "LLM error"
	}
	if e.Sentence != "" {
		s = e.Sentence
	}
	if e.Status != 0 {
		s += " (HTTP " + strconv.Itoa(e.Status) + ")"
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.Hint != "" {
		s += "; check " + e.Hint
	}
	return s
}

// UserMessage is the sentence an entry point may show to a client (X-6).
func (e *Error) UserMessage() string { return e.Error() }

// Is reports whether target is an *Error of the same class. Status, Hint and
// Detail are ignored, so errors.Is(err, llm.ErrAuth) works for any auth error.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Class == e.Class
}

// Retryable reports whether the class is retried by the client (DQ-9 step 1).
func (c ErrorClass) Retryable() bool {
	switch c {
	case ClassRateLimited, ClassUpstream, ClassTimeout, ClassTransport:
		return true
	}
	return false
}

// ClassOf returns the class of err when it wraps an *Error.
func ClassOf(err error) (ErrorClass, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Class, true
	}
	return "", false
}
