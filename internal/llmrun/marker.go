package llmrun

import (
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// This file holds the persistent-comment logic that pr_review's overview
// (X-12) and pr_describe's description comment share: a comment of ours is
// recognised by a marker on its last line plus its author, the newest one is
// edited in place, and older ones are counted and never touched. The marker
// of each tool is a CommonMark link reference definition, which renders as
// nothing.

// HasMarkerLastLine reports whether body's last line is exactly marker.
// Trailing whitespace of the body (a final newline a server adds) and a
// trailing carriage return are ignored; anything else on the line is not.
func HasMarkerLastLine(body, marker string) bool {
	body = strings.TrimRight(body, " \t\r\n")
	last := body[strings.LastIndexByte(body, '\n')+1:]
	return strings.TrimRight(last, " \t\r") == marker
}

// WithMarker returns body with marker as its last line.
func WithMarker(body, marker string) string {
	return strings.TrimRight(body, " \t\r\n") + "\n\n" + marker
}

// MarkedComment is a comment of ours on the PR that carries a marker.
type MarkedComment struct {
	ID, URL string
	// Older counts the other comments of ours with the marker, which are
	// left unchanged.
	Older int
}

// FindMarked looks up the comment to edit: among the roots of the PR's
// general threads, the comments whose last line is marker and whose author
// is me; the newest of them. A comment by anyone else is never adopted,
// whatever it contains: another user could have planted the marker. The
// author is compared with provider.IsUser on CommentItem.AuthorID and
// AuthorLogin, which is the comparison EditComment's ownership check makes,
// so a found comment passes that check. It returns nil when none matches.
func FindMarked(threads []provider.Thread, me provider.User, marker string) *MarkedComment {
	var newest *provider.CommentItem
	n := 0
	for i := range threads {
		t := &threads[i]
		if t.Kind != provider.ThreadGeneral || len(t.Comments) == 0 {
			continue
		}
		c := &t.Comments[0]
		if !HasMarkerLastLine(c.Body, marker) || !provider.IsUser(me, c.AuthorID, c.AuthorLogin) {
			continue
		}
		n++
		if newest == nil || NewerComment(c, newest) {
			newest = c
		}
	}
	if newest == nil {
		return nil
	}
	return &MarkedComment{ID: newest.ID, URL: newest.URL, Older: n - 1}
}

// NewerComment reports whether a was created after b; equal times fall back
// to the larger id (ids grow on both providers).
func NewerComment(a, b *provider.CommentItem) bool {
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
