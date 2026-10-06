package review

import (
	"context"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// OverviewMarker is the last line of every overview comment the review
// posts (spec P7 §4.2). It is a CommonMark link reference definition, which
// renders as nothing.
const OverviewMarker = "[//]: # (review-mcp:overview:v1)"

// markerLinePrefix opens every marker line the review writes
// ("[//]: # (review-mcp:...)").
const markerLinePrefix = "[//]:"

// ContainsMarkerLine reports whether any line of body looks like a
// review-mcp marker: after trimming whitespace and lower-casing, it starts
// with a link reference definition of the "[//]:" form and mentions
// "review-mcp:". It is a deliberately wider net than HasOverviewMarker and
// ParseFingerprintMarker, which read only an exact last line: a body the
// review did not write must never carry anything either lookup might
// adopt, now or after a marker format change.
func ContainsMarkerLine(body string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		l := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(l, markerLinePrefix) && strings.Contains(l, "review-mcp:") {
			return true
		}
	}
	return false
}

// Notes of the persistent overview (spec P7 §4.2).
const (
	// NoteOverviewReplaced: the overview found on the PR could not be
	// edited, so a new one was posted (spec wording).
	NoteOverviewReplaced = "The previous overview could not be updated; a new one was posted."
	// NoteOverviewLookupFailed: the PR's comments or the token's user could
	// not be read, so an existing overview could not be found.
	NoteOverviewLookupFailed = "The existing overview could not be looked up; a new one was posted."
)

// noteOlderOverviews: several overviews of ours were found; the newest was
// updated and the others were left as they are.
func noteOlderOverviews(n int) string {
	return countPhrase(n, "older overview by the same user was", "older overviews by the same user were") +
		" left unchanged."
}

// HasOverviewMarker reports whether body's last line is exactly
// OverviewMarker. Trailing whitespace of the body (a final newline a server
// adds) and a trailing carriage return are ignored; anything else on the
// line is not.
func HasOverviewMarker(body string) bool {
	body = strings.TrimRight(body, " \t\r\n")
	last := body[strings.LastIndexByte(body, '\n')+1:]
	return strings.TrimRight(last, " \t\r") == OverviewMarker
}

// withOverviewMarker returns the overview body with the marker as its last
// line.
func withOverviewMarker(body string) string {
	return strings.TrimRight(body, " \t\r\n") + "\n\n" + OverviewMarker
}

// listThreads returns the PR's comment threads, reading them once per run:
// step 2 (the discussion and the duplicate check) and step 13 (the overview
// lookup) share the one read.
func (pl *Plan) listThreads(ctx context.Context) ([]provider.Thread, error) {
	if !pl.threadsRead {
		pl.threads, pl.threadsErr = pl.p.ListThreads(ctx, pl.ref)
		pl.threadsRead = true
	}
	return pl.threads, pl.threadsErr
}

// currentUser returns the token's own user, reading it once per run.
func (pl *Plan) currentUser(ctx context.Context) (provider.User, error) {
	if !pl.meRead {
		pl.me, pl.meErr = pl.p.CurrentUser(ctx)
		pl.meRead = true
	}
	return pl.me, pl.meErr
}

// foundOverview is an overview of ours on the PR.
type foundOverview struct {
	id, url string
	// older counts the other overviews of ours, left unchanged.
	older int
}

// findOverview looks up the overview to edit (spec P7 §4.2): among the
// roots of the PR's general threads, the comments whose last line is the
// marker and whose author is the token's own user; the newest of them. A
// comment by anyone else is never adopted, whatever it contains: another
// user could have planted the marker. The author is compared with
// provider.IsUser on CommentItem.AuthorID and AuthorLogin, which is the
// comparison EditComment's ownership check makes, so a found overview
// passes that check.
//
// It returns nil when none matches and an error when the threads or the
// user could not be read. ListThreads reads every page or fails (the
// providers stop at a page ceiling with an error rather than return a
// partial list), so an overview is never missed silently: on any error the
// caller posts a new overview.
func (pl *Plan) findOverview(ctx context.Context) (*foundOverview, error) {
	threads, err := pl.listThreads(ctx)
	if err != nil {
		return nil, err
	}
	me, err := pl.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	var newest *provider.CommentItem
	n := 0
	for i := range threads {
		t := &threads[i]
		if t.Kind != provider.ThreadGeneral || len(t.Comments) == 0 {
			continue
		}
		c := &t.Comments[0]
		if !HasOverviewMarker(c.Body) || !provider.IsUser(me, c.AuthorID, c.AuthorLogin) {
			continue
		}
		n++
		if newest == nil || newerComment(c, newest) {
			newest = c
		}
	}
	if newest == nil {
		return nil, nil
	}
	return &foundOverview{id: newest.ID, url: newest.URL, older: n - 1}, nil
}

// newerComment reports whether a was created after b; equal times fall
// back to the larger id (ids grow on both providers).
func newerComment(a, b *provider.CommentItem) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return idLess(b.ID, a.ID)
}

// idLess compares decimal ids numerically (a shorter id is smaller), and
// other ids lexically.
func idLess(a, b string) bool {
	if provider.IsPositiveInt(a) && provider.IsPositiveInt(b) && len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// readDiscussion reads the PR's threads for the prompt and the duplicate
// check (spec P7 §5): it fills pl.postedFingerprints when wantFingerprints
// is set and renders the discussion block within maxTokens (0 or less: no
// block). It reads nothing when neither is wanted. Any read error returns
// an empty discussion and NoteDiscussionUnreadable: the discussion is
// context, never a reason to fail the review. Without the token's own user
// the review's comments cannot be told from other people's, so a failed
// CurrentUser is treated as a failed read.
func (pl *Plan) readDiscussion(ctx context.Context, maxTokens int, wantFingerprints bool, factor float64) (discussion, []string) {
	if maxTokens <= 0 && !wantFingerprints {
		return discussion{}, nil
	}
	threads, err := pl.listThreads(ctx)
	var me provider.User
	if err == nil {
		me, err = pl.currentUser(ctx)
	}
	if err != nil {
		pl.log.Debug("review: discussion not read", "error", fixedError(err))
		return discussion{}, []string{NoteDiscussionUnreadable}
	}
	if wantFingerprints {
		pl.postedFingerprints = fingerprintsOf(threads, me)
	}
	d := renderDiscussion(humanThreads(threads, me), maxTokens, factor)
	pl.log.Debug("review: discussion read", "threads", len(threads), "shown", d.Included, "omitted", d.Omitted,
		"posted_fingerprints", len(pl.postedFingerprints))
	return d, nil
}
