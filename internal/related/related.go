// Package related reads the related pull requests of a review (X-41, v2.3
// spec V-13a): pull requests the caller names as read-only context of a
// primary pull request, on any configured provider. review-mcp never
// discovers them, never publishes to them, and reads only what the review
// needs: the pull request's title, state, draft flag, target branch, head
// revision and diff (no description, no comments).
//
// Every related URL goes through the run's resolver, the same one as the
// primary's: an URL that matches no configured provider is never sent
// anywhere, and in serve mode a provider without the caller's token is
// refused before any request. A related pull request that cannot be read
// never fails the run, and never means that it does not exist. Logs carry
// an item's index, provider kind, status and reason only: never its URL
// path, title or diff.
package related

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Limits of the related argument (spec §1.1).
const (
	// MaxItems is the most related pull requests of one call.
	MaxItems = 4
	// MaxURLBytes is the longest related URL.
	MaxURLBytes = 2048
)

// Status values of a related item (coverage.related[].status).
const (
	// StatusFull: every changed file was shown.
	StatusFull = "full"
	// StatusPartial: some changed files were listed by path only.
	StatusPartial = "partial"
	// StatusNotPlaced: read, but not in the prompt.
	StatusNotPlaced = "not_placed"
	// StatusFailed: not read (Reason says why).
	StatusFailed = "failed"
	// StatusIgnored: the primary pull request itself, or a duplicate.
	StatusIgnored = "ignored"
)

// Reason values (coverage.related[].reason).
const (
	ReasonURLNotConfigured   = "url_not_configured"
	ReasonURLMalformed       = "url_malformed"
	ReasonCredentialsMissing = "credentials_missing"
	ReasonNotFound           = "not_found"
	ReasonForbidden          = "forbidden"
	ReasonUnauthorized       = "unauthorized"
	ReasonUnreadable         = "unreadable"
	ReasonDuplicate          = "duplicate"
	ReasonPrimary            = "primary"
	ReasonDidNotFit          = "did_not_fit"
	ReasonReviewInParts      = "review_in_parts"
	// ReasonOff: related.max_tokens is 0, so nothing was read.
	//
	// DESIGN-QUESTION (WP-11a DQ-1): the spec turns related pull requests
	// off with related.max_tokens = 0 ("the argument is accepted, nothing is
	// read, one note") but names no status for the items, and every item
	// must be accounted for in coverage.related. They get not_placed with
	// this reason. Recommendation: keep; an item that is in the argument
	// but was not read or shown is not_placed, and the reason says why.
	ReasonOff = "off"
)

// Fixed argument errors (spec §1.1 item 2). They never repeat a value.
const (
	errTooMany     = "related must have at most 4 items"
	errCombination = "related cannot be combined with focus or findings"
)

// ArgumentError is an invalid related argument; its text is fixed.
type ArgumentError struct{ msg string }

func (e *ArgumentError) Error() string { return e.msg }

// Normalize returns the URL as the run uses it: an absolute http(s) URL
// with a host and no credentials, at most MaxURLBytes long, without its
// query and fragment, as net/url writes it. ok is false for anything else.
func Normalize(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxURLBytes {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return "", false
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return u.String(), true
}

// Validate checks the related argument before any network call, in the
// spec's order: at most MaxItems items, each a URL Normalize accepts (the
// error names its 0-based index), and no focus or findings with it
// (combined is true when the call has either). It returns the normalised
// URLs; an empty list is not given and returns nil. The error is an
// *ArgumentError.
func Validate(items []string, combined bool) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > MaxItems {
		return nil, &ArgumentError{errTooMany}
	}
	out := make([]string, len(items))
	for i, raw := range items {
		u, ok := Normalize(raw)
		if !ok {
			return nil, &ArgumentError{fmt.Sprintf("related[%d] must be an http or https pull request URL without credentials", i)}
		}
		out[i] = u
	}
	if combined {
		return nil, &ArgumentError{errCombination}
	}
	return out, nil
}

// Resolver maps a pull request URL to a reference and a provider: the run's
// resolver (provider.Resolver, wrapped in serve mode by the credential
// check). It performs no network I/O.
type Resolver interface {
	Resolve(rawURL string) (provider.PRRef, provider.Provider, error)
}

// Item is one related pull request of a run, in argument order.
type Item struct {
	// URL is the normalised URL the caller gave.
	URL string
	// Status and Reason are the item's outcome so far: StatusFailed and
	// StatusIgnored are final; a read item has Read set and gets its status
	// when the prompt is built (full, partial or not_placed).
	Status, Reason string
	// Read is set when the pull request and its diff were read.
	Read     bool
	Ref      provider.PRRef
	Provider provider.Provider
	PR       *provider.PullRequest
	Diff     *provider.Diff
}

// Read resolves and reads the related pull requests urls (normalised, in
// argument order) of the primary pull request primary (its normalised URL
// and its resolved reference). A URL equal to the primary's, or resolving
// to the same pull request, is ignored with ReasonPrimary; a repeat of an
// earlier item (by URL or by the pull request it resolves to) with
// ReasonDuplicate. Each other item is resolved through r, then read with
// GetPullRequest and GetDiff (include filters the files, as for the
// primary; nil keeps them all), sequentially, under ctx. A failure is the
// item's status and reason, never the run's error. With off set nothing is
// resolved or read: every item that is not ignored is not_placed with
// ReasonOff.
//
// DESIGN-QUESTION (WP-11a DQ-2): the spec ignores the primary URL and
// duplicates "after normalisation". Two different URLs can name the same
// pull request (".../pulls/7" and ".../pulls/7/files"), so an item is also
// ignored when it resolves to the primary's reference or to an earlier
// item's, before anything is read. Recommendation: keep; the same pull
// request is never read twice nor shown as its own context.
func Read(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, urls []string,
	include func(string) bool, off bool, log *slog.Logger) []*Item {
	return read(ctx, r, primaryURL, primary, urls, include, off, true, log)
}

// ReadPullRequests is Read without the diffs (pr_info, spec V-13a §3.2):
// each related pull request is resolved and read with GetPullRequest only;
// related.max_tokens does not apply, since nothing goes into a prompt.
//
// DESIGN-QUESTION (WP-11c DQ-2): related.max_tokens 0 turns related pull
// requests off for the prompts of pr_review and pr_ask. pr_info has no
// prompt and reads no diff, so it reads them whatever the budget.
// Recommendation: keep.
func ReadPullRequests(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, urls []string,
	log *slog.Logger) []*Item {
	return read(ctx, r, primaryURL, primary, urls, nil, false, false, log)
}

func read(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, urls []string,
	include func(string) bool, off, withDiff bool, log *slog.Logger) []*Item {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	items := make([]*Item, 0, len(urls))
	seenURL := map[string]bool{}
	var seenRefs []provider.PRRef
	for i, u := range urls {
		it := &Item{URL: u}
		items = append(items, it)
		switch {
		case u == primaryURL:
			it.Status, it.Reason = StatusIgnored, ReasonPrimary
		case seenURL[u]:
			it.Status, it.Reason = StatusIgnored, ReasonDuplicate
		case off:
			it.Status, it.Reason = StatusNotPlaced, ReasonOff
		}
		seenURL[u] = true
		if it.Status != "" {
			log.Debug("related pull request", "index", i, "status", it.Status, "reason", it.Reason)
			continue
		}
		ref, p, err := r.Resolve(u)
		if err != nil {
			it.Status, it.Reason = StatusFailed, reasonOf(err)
			log.Debug("related pull request", "index", i, "status", it.Status, "reason", it.Reason)
			continue
		}
		switch {
		case sameRef(ref, primary):
			it.Status, it.Reason = StatusIgnored, ReasonPrimary
		case containsRef(seenRefs, ref):
			it.Status, it.Reason = StatusIgnored, ReasonDuplicate
		}
		if it.Status != "" {
			log.Debug("related pull request", "index", i, "kind", string(ref.Kind), "status", it.Status, "reason", it.Reason)
			continue
		}
		seenRefs = append(seenRefs, ref)
		it.Ref, it.Provider = ref, p
		pr, err := p.GetPullRequest(ctx, ref)
		d := &provider.Diff{}
		if err == nil && withDiff {
			d, err = p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: include})
		}
		if err != nil {
			it.Status, it.Reason = StatusFailed, reasonOf(err)
			log.Debug("related pull request", "index", i, "kind", string(ref.Kind), "status", it.Status, "reason", it.Reason)
			continue
		}
		it.Read, it.PR, it.Diff = true, pr, d
		log.Debug("related pull request", "index", i, "kind", string(ref.Kind), "status", "read",
			"files", len(d.Files), "skipped", len(d.Skipped))
	}
	return items
}

// ReadItems are the items of items that were read, in order.
func ReadItems(items []*Item) []*Item {
	var out []*Item
	for _, it := range items {
		if it.Read {
			out = append(out, it)
		}
	}
	return out
}

func sameRef(a, b provider.PRRef) bool {
	return a.Kind == b.Kind && strings.EqualFold(a.Namespace, b.Namespace) && strings.EqualFold(a.Repo, b.Repo) &&
		a.Number == b.Number && a.Kind != ""
}

func containsRef(refs []provider.PRRef, ref provider.PRRef) bool {
	for _, r := range refs {
		if sameRef(r, ref) {
			return true
		}
	}
	return false
}

// classed is an error that names its X-6 class (tools.RequestError: the
// serve-mode credential check).
type classed interface{ ErrorClass() string }

// reasonOf maps a resolution or read error to a fixed reason (spec §1.2
// item 4): the URL classes, a missing serve-mode credential, not found, an
// authentication answer (403 forbidden, otherwise unauthorized), and
// unreadable for everything else.
func reasonOf(err error) string {
	var c classed
	if errors.As(err, &c) && c.ErrorClass() == ReasonCredentialsMissing {
		return ReasonCredentialsMissing
	}
	var pe *provider.Error
	if !errors.As(err, &pe) {
		return ReasonUnreadable
	}
	switch pe.Class {
	case provider.ClassURLNotConfigured:
		return ReasonURLNotConfigured
	case provider.ClassURLMalformed:
		return ReasonURLMalformed
	case provider.ClassNotFound:
		return ReasonNotFound
	case provider.ClassAuth:
		if pe.Status == 403 {
			return ReasonForbidden
		}
		return ReasonUnauthorized
	}
	return ReasonUnreadable
}

// Notes (spec §1.5 item 2, verbatim); url is the normalised URL, which the
// user gave.

// NoteFailed: the item could not be read.
func NoteFailed(url, reason string) string {
	return "Related pull request " + url + " could not be read (" + reason + ") and was not used."
}

// NoteIgnored: the item is the primary pull request or a duplicate.
func NoteIgnored(url string) string {
	return "Related pull request " + url + " was ignored: it is the reviewed pull request itself or a duplicate."
}

// NotePartial: n of the item's files were listed by path only.
func NotePartial(url string, n int) string {
	return fmt.Sprintf("Related pull request %s did not fit in full; %d of its files are listed by path only.", url, n)
}

// Notes of the whole set.
const (
	NoteNotPlaced = "The related pull requests did not fit the context window next to the diff and were not used."
	NoteInParts   = "The pull request was reviewed in parts; related pull requests need the whole change at once and were not used."
	NoteOff       = "related.max_tokens is 0, so related pull requests were not read."
)
