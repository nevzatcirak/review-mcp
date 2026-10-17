package review

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"path"
	"strings"
	"unicode/utf8"
)

// Held lines (X-36, spec v2.1 §7.2, WP-7f): every finding the overview lists
// that has no inline comment of review-mcp on the pull request is recorded
// in the overview body, one hidden line per finding, so that a later
// incremental review can list it again when its file did not change. A held
// line is a link reference definition like the marker:
//
//	[//]: # (review-mcp:held:<base64url of compact JSON, no padding>)
//
// The held lines sit directly above the reviewed-commit line, which sits
// directly above the overview marker; they are read from that trailer of
// review-mcp's own overview only.
const (
	heldLinePrefix = "[//]: # (review-mcp:held:"
	heldLineSuffix = ")"
)

// Limits of a held record (spec §7.2 items 3 and 5).
const (
	// maxHeldHeaderBytes and maxHeldContentBytes cap a record's header and
	// content; a longer text is cut at a rune boundary and ends in "…".
	maxHeldHeaderBytes  = 200
	maxHeldContentBytes = 800
	// maxHeldBytes caps the held lines of one overview, each counted with
	// its line feed.
	maxHeldBytes = 12000
	// maxHeldPathBytes caps a record's path.
	maxHeldPathBytes = 4096
	// heldVersion is the only record version.
	heldVersion = 1
)

// heldEllipsis ends a cut header or content.
const heldEllipsis = "…"

// heldRecord is one overview-only finding as a held line records it. The
// field order is the spec's JSON order.
type heldRecord struct {
	V    int    `json:"v"`
	F    string `json:"f"`
	S    int    `json:"s"`
	E    int    `json:"e"`
	H    string `json:"h"`
	C    string `json:"c"`
	From string `json:"from"`
	// R is the related pull request's URL of a finding located there
	// (X-41, v2.3 spec V-13a §2.3 item 1); absent otherwise.
	R string `json:"r,omitempty"`
}

// noteNotCarried: overview-only findings that got no held line (the
// 12,000-byte budget, or a record that would not be read back), so that a
// later incremental review does not list them again.
func noteNotCarried(n int) string {
	return countPhrase(n, "finding listed in this overview only", "findings listed in this overview only") +
		" will not be carried forward to a later incremental review."
}

// overviewOnly reports whether the overview is the only place of a finding
// (spec §7.1): it was carried forward from an earlier review, or it has no
// inline comment of review-mcp on the pull request, because inline findings
// were off or not yet posted (no status), it is on no changed line, or its
// inline comment failed.
//
// DESIGN-QUESTION (WP-7f DQ-1): a publish without an overview to edit posts
// the overview before the inline comments, so the findings planned inline
// have no status yet when it is first rendered. They are treated as
// overview-only (no status = inline off), so that first body records them;
// the edit that follows the inline post renders again with the final
// statuses and rewrites the held lines. When that edit fails, a later
// incremental review may list a finding that also has an inline comment,
// rather than drop one that has none. Recommendation: keep (never lose a
// finding).
func overviewOnly(ki *KeyIssue) bool {
	if ki.CarriedFrom != "" {
		return true
	}
	switch ki.InlineStatus {
	case "", InlineUnanchorable, InlineFailed:
		return true
	}
	return false
}

// heldLines returns the held lines of res's overview-only findings, in the
// order of the findings, for an overview that records the commit sha (each
// new finding's "from"; a carried finding keeps its own), and the number of overview-only findings that got
// none: those after the first line that would take the lines over
// maxHeldBytes, and any whose record would not be read back (heldValid).
// Without a valid sha nothing is recorded and nothing is counted: an
// overview without a reviewed commit makes the next run a full review.
//
// DESIGN-QUESTION (WP-7f DQ-2): the spec names the budget as the reason for
// a finding without a held line. A record the reader would ignore (a path
// that is not a cleaned relative path, such as a model's "./a.go", or
// lines that end before they start) is not written either, and counted in
// the same note: the finding will not be carried forward, and the note says
// so. Without a reviewed commit (a partial review, a merged publish without
// a found commit) no note is added: the next run is a full review that
// reviews every file again, so nothing is lost. Recommendation: keep.
func heldLines(res *Result, sha string) (lines []string, unrecorded int) {
	sha = ValidReviewedSHA(sha)
	if sha == "" || res.Review == nil {
		return nil, 0
	}
	total, full := 0, false
	for i := range res.Review.KeyIssuesToReview {
		ki := &res.Review.KeyIssuesToReview[i]
		if !overviewOnly(ki) {
			continue
		}
		from := sha
		if ki.CarriedFrom != "" {
			from = ki.CarriedFrom
		}
		rec := heldRecord{V: heldVersion, F: ki.RelevantFile, S: ki.StartLine, E: ki.EndLine,
			H: cutBytes(ki.IssueHeader, maxHeldHeaderBytes), C: cutBytes(ki.IssueContent, maxHeldContentBytes), From: from,
			R: ki.Related}
		line, ok := encodeHeld(rec)
		switch {
		case !ok || full:
			unrecorded++
		case total+len(line)+1 > maxHeldBytes:
			full = true
			unrecorded++
		default:
			total += len(line) + 1
			lines = append(lines, line)
		}
	}
	return lines, unrecorded
}

// encodeHeld returns the held line of rec, or false when rec would not be
// read back (heldValid).
func encodeHeld(rec heldRecord) (string, bool) {
	if !rec.valid() {
		return "", false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return "", false
	}
	payload := base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return heldLinePrefix + payload + heldLineSuffix, true
}

// decodeHeld returns the record of a held line's payload, or false when it
// does not decode or is not a valid record.
func decodeHeld(payload string) (heldRecord, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(payload)
	if err != nil {
		return heldRecord{}, false
	}
	var rec heldRecord
	if err := json.Unmarshal(raw, &rec); err != nil || !rec.valid() {
		return heldRecord{}, false
	}
	return rec, true
}

// valid reports whether a record may be read (spec §7.2 item 5): version
// 1; a path that is not empty, at most maxHeldPathBytes, without NUL and a
// cleaned relative path; lines not negative, and the end not before the
// start when both are given; the header and the content within their caps;
// "from" a full lower-case commit id.
func (r heldRecord) valid() bool {
	return r.V == heldVersion && heldPath(r.F) && r.S >= 0 && r.E >= 0 && (r.S == 0 || r.E == 0 || r.E >= r.S) &&
		len(r.H) <= maxHeldHeaderBytes && len(r.C) <= maxHeldContentBytes && isReviewedSHA(r.From) && validRelatedURL(r.R, true)
}

// heldPath reports whether p is a path a held record may name.
func heldPath(p string) bool {
	return p != "" && len(p) <= maxHeldPathBytes && !strings.ContainsRune(p, 0) && !path.IsAbs(p) &&
		path.Clean(p) == p && p != "." && p != ".." && !strings.HasPrefix(p, "../")
}

// cutBytes returns s when it has at most n bytes, else its longest prefix
// that ends at a rune boundary and leaves room for heldEllipsis, followed by
// it: the result never exceeds n bytes, so the reader accepts it.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := n - len(heldEllipsis)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + heldEllipsis
}

// heldOf returns the held records of an overview body (spec §7.2 item 5):
// only the contiguous run of lines that are exactly a held line directly
// above the reviewed-commit line, which is directly above the marker. A
// held-looking line anywhere else is never read. Lines whose record is not
// valid are skipped and counted in invalid. A trailing carriage return is
// tolerated on every line, as for the marker. Call it only on a body of our
// own overview (findOverview checks the marker and the author).
func heldOf(body string) (recs []heldRecord, invalid int) {
	lines := strings.Split(strings.TrimRight(body, " \t\r\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	n := len(lines)
	if n < 3 || lines[n-1] != OverviewMarker {
		return nil, 0
	}
	sha, ok := strings.CutPrefix(lines[n-2], reviewedLinePrefix)
	if !ok {
		return nil, 0
	}
	if sha, ok = strings.CutSuffix(sha, reviewedLineSuffix); !ok || !isReviewedSHA(sha) {
		return nil, 0
	}
	start := n - 2
	for start > 0 && heldPayload(lines[start-1]) != "" {
		start--
	}
	for _, line := range lines[start : n-2] {
		rec, ok := decodeHeld(heldPayload(line))
		if !ok {
			invalid++
			continue
		}
		recs = append(recs, rec)
	}
	return recs, invalid
}

// heldPayload returns the payload of a line that is exactly a held line
// (the prefix, one or more base64url characters, the suffix), else "".
func heldPayload(line string) string {
	p, ok := strings.CutPrefix(line, heldLinePrefix)
	if !ok {
		return ""
	}
	if p, ok = strings.CutSuffix(p, heldLineSuffix); !ok || p == "" {
		return ""
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return ""
		}
	}
	return p
}
