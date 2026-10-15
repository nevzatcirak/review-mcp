package review

import (
	"context"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// OverviewMarker is the last line of every overview comment the review
// posts (spec P7 §4.2). It is a CommonMark link reference definition, which
// renders as nothing.
const OverviewMarker = "[//]: # (review-mcp:overview:v1)"

// reviewedLinePrefix and reviewedLineSuffix enclose the hex commit id of the
// reviewed-commit line (X-36): "[//]: # (review-mcp:reviewed:<sha>)", written
// directly above OverviewMarker. It is a link reference definition too.
const (
	reviewedLinePrefix = "[//]: # (review-mcp:reviewed:"
	reviewedLineSuffix = ")"
)

// ValidReviewedSHA returns sha lower-cased when it is a full commit id (40
// or 64 hexadecimal characters, SHA-1 or SHA-256), else "".
func ValidReviewedSHA(sha string) string {
	sha = strings.ToLower(sha)
	if !isReviewedSHA(sha) {
		return ""
	}
	return sha
}

// isReviewedSHA reports whether s is exactly 40 or 64 lower-case hex digits.
func isReviewedSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

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
// OverviewMarker (llmrun.HasMarkerLastLine).
func HasOverviewMarker(body string) bool {
	return llmrun.HasMarkerLastLine(body, OverviewMarker)
}

// withOverviewMarker returns the overview body with the marker as its last
// line.
func withOverviewMarker(body string) string {
	return llmrun.WithMarker(body, OverviewMarker)
}

// withTrailer returns the overview body followed by its trailer: the held
// lines (spec v2.1 §7.2), the reviewed-commit line of sha and the overview
// marker, in that order, as the last lines. Held lines are written only
// with a valid sha. The body is model-influenced (finding text, a diff
// snippet in a fence), so first any line of it that looks like a marker
// line of review-mcp is defused: the reader takes the last reviewed-commit
// line, and when there is no sha to write there would be no later line to
// outvote a forged one; held lines are read from the trailer only, and a
// forged one must not look like part of it either.
func withTrailer(body string, held []string, sha string) string {
	body = defuseMarkerLines(body)
	if sha = ValidReviewedSHA(sha); sha != "" {
		trailer := append(append([]string(nil), held...), reviewedLinePrefix+sha+reviewedLineSuffix, OverviewMarker)
		return llmrun.WithMarker(body, strings.Join(trailer, "\n"))
	}
	return withOverviewMarker(body)
}

// defuseMarkerLines escapes the opening bracket of every line of body that
// ContainsMarkerLine would flag (after trimming and lower-casing, a "[//]:"
// line that mentions "review-mcp:"), so that it is no link reference
// definition and no reader accepts it: a reviewed-commit line, a held line
// or the marker itself.
func defuseMarkerLines(body string) string {
	if !strings.Contains(strings.ToLower(body), "review-mcp:") {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if ContainsMarkerLine(line) {
			at := strings.Index(line, "[")
			lines[i] = line[:at] + "\\" + line[at:]
		}
	}
	return strings.Join(lines, "\n")
}

// reviewedSHAOf returns the commit id of the reviewed-commit line of an
// overview body (X-36): the last line above the overview marker that is
// exactly "[//]: # (review-mcp:reviewed:<sha>)" with a 40 or 64 digit lower
// case hex sha. Anything else (another length, upper case, extra text or
// spaces) is ignored; "" when no line qualifies. A trailing carriage return
// is the only tolerated addition, as for the marker. Call it only on a body
// of our own overview (findOverview checks the marker and the author).
func reviewedSHAOf(body string) string {
	body = strings.TrimRight(body, " \t\r\n")
	i := strings.LastIndexByte(body, '\n')
	if i < 0 {
		return ""
	}
	sha := ""
	for line := range strings.SplitSeq(body[:i], "\n") { // above the marker line
		s, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), reviewedLinePrefix)
		if !ok {
			continue
		}
		if s, ok = strings.CutSuffix(s, reviewedLineSuffix); ok && isReviewedSHA(s) {
			sha = s
		}
	}
	return sha
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
	// reviewed is the commit the overview records as reviewed (X-36); ""
	// when it records none.
	reviewed string
	// held are the valid records of its held lines (spec v2.1 §7.2), in
	// the order of the overview.
	held []heldRecord
	// older counts the other overviews of ours, left unchanged.
	older int
}

// findOverview looks up the overview to edit (spec P7 §4.2): among the
// roots of the PR's general threads, the comments whose last line is the
// marker and whose author is the token's own user; the newest of them
// (llmrun.FindMarked, shared with pr_describe). The reviewed commit it
// records, and its held lines, are read from that comment only, so a line
// planted in anyone else's comment is never read.
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
	m := llmrun.FindMarked(threads, me, OverviewMarker)
	if m == nil {
		return nil, nil
	}
	held, invalid := heldOf(m.Body)
	if invalid > 0 {
		pl.log.Debug("review: held lines ignored", "invalid", invalid, "valid", len(held))
	}
	return &foundOverview{id: m.ID, url: m.URL, reviewed: reviewedSHAOf(m.Body), held: held, older: m.Older}, nil
}

// readDiscussion reads the PR's threads for the prompt and the duplicate
// check (spec P7 §5, X-29): it fills pl.own when wantFingerprints is set and renders the discussion block within maxTokens (0 or less: no
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
		pl.own = ownFindingsOf(threads, me)
	}
	d := renderDiscussion(humanThreads(threads, me), maxTokens, factor)
	pl.log.Debug("review: discussion read", "threads", len(threads), "shown", d.Included, "omitted", d.Omitted,
		"own_findings", len(pl.own))
	return d, nil
}
