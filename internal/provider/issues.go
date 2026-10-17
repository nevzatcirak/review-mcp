package provider

import (
	"context"
	"encoding/json"
	"unicode/utf8"
)

// Limits of an issue read (X-42, v2.2 spec V-2 §1.2).
const (
	// MaxIssueBodyBytes is the most of an issue's description GetIssue
	// keeps; a longer one is cut at a character boundary and ends with
	// IssueBodyCutMarker.
	MaxIssueBodyBytes = 64 * 1024
	// MaxIssueLabels is the most label names GetIssue keeps, in the host's
	// order.
	MaxIssueLabels = 20
)

// IssueBodyCutMarker ends an issue description that was cut at
// MaxIssueBodyBytes.
const IssueBodyCutMarker = "\n\n[review-mcp: the rest of this issue's description was cut at 64 KiB]"

// IssueRef identifies one issue on the provider's own host: the repository
// and the issue's number in it (GitLab's iid). The repository may differ
// from the pull request's; the host never does, since a Provider talks to
// one host only.
type IssueRef struct {
	Namespace, Repo string
	Number          int64
}

// IssueState is the state of an issue.
type IssueState string

// Issue states. A host state other than closed (GitLab's "opened") is open.
const (
	IssueOpen   IssueState = "open"
	IssueClosed IssueState = "closed"
)

// Issue is one issue of the host's own tracker. Title, Body and Labels are
// untrusted text written by the issue's author: nothing logs them, and a
// prompt carries them only fenced.
type Issue struct {
	Number int64
	Title  string
	// Body is the description as the host returns it (raw Markdown), at
	// most MaxIssueBodyBytes of it plus IssueBodyCutMarker.
	Body  string
	State IssueState
	// URL is the issue's web page: an http(s) URL without user info, or "".
	URL string
	// Labels are the label names, at most MaxIssueLabels of them; empty
	// names are dropped.
	Labels []string
}

// IssueReader is implemented by a Provider whose host has an issue tracker
// that the provider's token can read (Capabilities.Issues). Bitbucket
// Server has none and does not implement it.
type IssueReader interface {
	// GetIssue returns the issue ref names. It sends nothing for an invalid
	// ref (a protocol error). A number that names a pull request on a host
	// that serves pull requests through its issue endpoint is a
	// not_an_issue error. A 404 or a 403 keeps its class (not_found, auth):
	// the caller cannot tell an issue that does not exist from one the
	// token may not read, and treats both as unreadable. It is read-only
	// and logs nothing of the issue's text.
	GetIssue(ctx context.Context, ref IssueRef) (*Issue, error)
}

// ValidIssueRef reports whether ref can be sent: a namespace and a
// repository name that are not empty, "." or "..", and a positive number.
// A namespace may hold "/" (a nested GitLab group) where the provider
// accepts it; each provider checks that itself.
func ValidIssueRef(ref IssueRef) bool {
	bad := func(s string) bool { return s == "" || s == "." || s == ".." }
	return !bad(ref.Namespace) && !bad(ref.Repo) && ref.Number > 0
}

// NewIssue builds an Issue from a provider's raw values: the body is cut by
// CutIssueBody, the URL cleaned by CleanCheckURL, empty label names dropped
// and the rest cut to MaxIssueLabels. A state other than "closed" is open.
//
// DESIGN-QUESTION (WP-10a DQ-3): the spec names two states; a host state
// other than open and closed (GitLab's "locked" is one) has to map to one of
// them. It maps to open: such an issue has not been closed. Recommendation:
// keep it.
func NewIssue(number int64, title, body, state, rawURL string, labels []string) *Issue {
	is := &Issue{Number: number, Title: title, Body: CutIssueBody(body), State: IssueOpen, URL: CleanCheckURL(rawURL)}
	if state == string(IssueClosed) {
		is.State = IssueClosed
	}
	for _, l := range labels {
		if l == "" {
			continue
		}
		if len(is.Labels) == MaxIssueLabels {
			break
		}
		is.Labels = append(is.Labels, l)
	}
	return is
}

// CutIssueBody returns body when it has at most MaxIssueBodyBytes;
// otherwise its first MaxIssueBodyBytes, cut back to a character boundary,
// followed by IssueBodyCutMarker.
func CutIssueBody(body string) string {
	if len(body) <= MaxIssueBodyBytes {
		return body
	}
	n := MaxIssueBodyBytes
	for n > 0 && !utf8.RuneStart(body[n]) {
		n--
	}
	return body[:n] + IssueBodyCutMarker
}

// JSONSet reports whether a raw JSON field was present and not null: GitHub
// and Gitea mark an issue that is a pull request with a pull_request object
// (GitHub leaves the field out otherwise, Gitea sends null).
func JSONSet(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

// NotAnIssueError is the error of GetIssue for a number that names a pull
// request.
func NotAnIssueError() *Error {
	return &Error{Class: ClassNotAnIssue}
}
