package gitctx

import (
	"errors"
	"strings"
)

// Fixed reasons. An *Error carries exactly one of them and nothing else.
// The first seven classify a failed git command (§3 WP-11a); the others
// come from this package's own checks.
const (
	ReasonAuth        = "auth"
	ReasonNotFound    = "not_found"
	ReasonTimeout     = "timeout"
	ReasonTooLarge    = "too_large"
	ReasonSHAMismatch = "sha_mismatch"
	ReasonRedirect    = "redirect"
	ReasonGitFailed   = "git_failed"

	// ReasonGitUnavailable: git is missing or older than MinGitVersion
	// (RC-2). It is a note, never a failure of the review.
	ReasonGitUnavailable = "git_unavailable"
	// ReasonBusy: another process held the repository's lock for the whole
	// fetch timeout.
	ReasonBusy = "busy"
	// ReasonCache: the cache directory cannot be created or used, or it is
	// not a review-mcp cache.
	ReasonCache = "cache_unusable"
	// ReasonUnsupported: the repository cannot be fetched (an unknown
	// provider kind, an unusable base URL or name, an invalid head SHA).
	ReasonUnsupported = "unsupported"
)

// notes are the fixed sentences for each reason, for the review notes and
// the coverage line (RC-9).
var notes = map[string]string{
	ReasonAuth:           "repository context skipped: the git server did not accept the token",
	ReasonNotFound:       "repository context skipped: the repository or the pull request ref was not found",
	ReasonTimeout:        "repository context skipped: fetching took longer than context.repo.fetch_timeout_seconds",
	ReasonTooLarge:       "repository context skipped: the repository is larger than context.repo.max_repo_mb",
	ReasonSHAMismatch:    "repository context skipped: the pull request head changed while it was fetched",
	ReasonRedirect:       "repository context skipped: the git server answered with a redirect, which is not followed",
	ReasonGitFailed:      "repository context skipped: git failed",
	ReasonGitUnavailable: "repository context skipped: git " + MinGitVersion + " or later is required",
	ReasonBusy:           "repository context skipped: the cached repository is in use by another process",
	ReasonCache:          "repository context skipped: the cache directory cannot be used",
	ReasonUnsupported:    "repository context skipped: this repository cannot be fetched with git",
}

// Error is a failure of this package. It carries only a fixed reason; git's
// stderr and every other untrusted or secret text is never part of it.
type Error struct {
	Reason string
}

func (e *Error) Error() string { return "repository context: " + e.Reason }

// Note returns the fixed sentence for the reason.
func (e *Error) Note() string { return Note(e.Reason) }

// Note returns the fixed sentence for reason.
func Note(reason string) string {
	if n, ok := notes[reason]; ok {
		return n
	}
	return notes[ReasonGitFailed]
}

// ReasonOf returns the reason of an *Error in err's chain, and git_failed for
// any other non-nil error.
func ReasonOf(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ReasonGitFailed
}

func fail(reason string) error { return &Error{Reason: reason} }

// classified is the result of reading a failed git command's stderr. The
// stderr text itself is dropped by the caller right after this.
type classified struct {
	reason string
	// unauthorized is true for an HTTP 401 (git reports it as a failed
	// credential prompt): only that triggers the next auth scheme.
	unauthorized bool
}

// classifyStderr maps git's stderr to a fixed reason. Unknown text is
// git_failed.
func classifyStderr(stderr string) classified {
	s := strings.ToLower(stderr)
	has := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return true
			}
		}
		return false
	}
	switch {
	case has("terminal prompts disabled", "authentication failed", "could not read username",
		"could not read password", "returned error: 401"):
		return classified{reason: ReasonAuth, unauthorized: true}
	case has("returned error: 403"):
		return classified{reason: ReasonAuth}
	case has("returned error: 30"):
		return classified{reason: ReasonRedirect}
	case has("couldn't find remote ref", "not found", "returned error: 404", "returned error: 410"):
		return classified{reason: ReasonNotFound}
	case has("timed out", "timeout was reached"):
		return classified{reason: ReasonTimeout}
	}
	return classified{reason: ReasonGitFailed}
}
