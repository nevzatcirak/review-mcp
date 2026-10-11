package review

import (
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review/anchor"
)

// ownFinding is one of review-mcp's own inline finding comments on the PR
// (X-29): a comment of an inline thread, written by the token's own user,
// whose last line is a fingerprint marker. Its place is the thread's.
type ownFinding struct {
	// fp is the fingerprint of the marker (spec P7 §5.3).
	fp string
	// path is the thread's path, compared exactly with the finding's new
	// path (anchor.Anchor.Path).
	path string
	// start to line are the new-side lines the comment covers: line is the
	// thread's line (the last line of a range on GitHub) and start its
	// first line, Thread.StartLine where the provider reports one, else
	// line. line is 0 when the thread has no new-side line (a base-side
	// comment, or an unknown line); such a comment never overlaps.
	start, line int
	// outdated: the provider says the code under the comment changed.
	outdated bool
	// url is the comment's web URL, "" when the provider did not report it.
	url string
	// c is the comment, for the tie-break by age.
	c *provider.CommentItem
}

// ownFindingsOf collects review-mcp's own inline finding comments from the
// PR's threads (X-29, spec P7 §5.3), in the order of threads. Every comment
// of every inline thread is examined, not only the root: Gitea groups the
// review comments of one line into one thread, so ours may be a reply; a
// reply has the place of its thread. A fingerprint marker in anyone else's
// comment does not count: anyone can type it.
func ownFindingsOf(threads []provider.Thread, me provider.User) []ownFinding {
	var out []ownFinding
	for i := range threads {
		t := &threads[i]
		if t.Kind != provider.ThreadInline {
			continue
		}
		start := t.Line
		if t.StartLine > 0 && t.StartLine < t.Line {
			start = t.StartLine
		}
		for j := range t.Comments {
			c := &t.Comments[j]
			if !provider.IsUser(me, c.AuthorID, c.AuthorLogin) {
				continue
			}
			fp, ok := ParseFingerprintMarker(c.Body)
			if !ok {
				continue
			}
			out = append(out, ownFinding{fp: fp, path: t.Path, start: start, line: t.Line,
				outdated: t.Outdated, url: c.URL, c: c})
		}
	}
	return out
}

// overlaps reports whether the finding anchored at a lies on o's place
// (X-29): the same path, and the inclusive ranges [a.Line, max(a.Line,
// a.EndLine)] and [o.start, o.line] share a line. An outdated comment never
// overlaps (the code under it changed, so a finding there may be new); a
// resolved one does (a person closed it; posting again is noise). A
// comment with no new-side line (o.line 0: on the base side, or unknown)
// never overlaps, since a finding is always on the new side.
//
// The path is compared exactly: a finding's anchor carries the file's new
// path, which is where review-mcp posts its own comments, so a thread on a
// renamed file's old path is not the same place (the providers also list
// such a thread as outdated once the file is renamed under it).
func (o *ownFinding) overlaps(a anchor.Anchor) bool {
	if o.outdated || o.line <= 0 || o.path != a.Path {
		return false
	}
	return a.Line <= o.line && o.start <= max(a.Line, a.EndLine)
}

// duplicateOf returns the own finding comment that a finding anchored at a
// with fingerprint fp repeats, or nil when it is new.
//
// The primary rule is the place (X-29, overlaps): a model rewords a finding
// between runs, so its text is no key. When several own comments overlap,
// the one whose first line is closest to the finding's first line wins,
// then the oldest, then the first in the thread order: the first line is
// the place every provider knows (Gitea and Bitbucket Server post a range
// on its first line, GitHub reports start_line), so an own comment on a
// range and its rewording share it. The second rule is the exact
// fingerprint (spec P7 §5.3), for a finding whose comment is on the PR at
// another place or in an outdated thread; the oldest such comment wins.
func duplicateOf(own []ownFinding, a anchor.Anchor, fp string) *ownFinding {
	var best *ownFinding
	for i := range own {
		o := &own[i]
		if o.overlaps(a) && (best == nil || closerFirstLine(o, best, a.Line)) {
			best = o
		}
	}
	if best != nil {
		return best
	}
	for i := range own {
		o := &own[i]
		if o.fp == fp && (best == nil || llmrun.NewerComment(best.c, o.c)) {
			best = o
		}
	}
	return best
}

// closerFirstLine reports whether o's first line is closer to line than
// best's, or as close and o is the older comment.
func closerFirstLine(o, best *ownFinding, line int) bool {
	if d, e := absInt(o.start-line), absInt(best.start-line); d != e {
		return d < e
	}
	return llmrun.NewerComment(best.c, o.c)
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
