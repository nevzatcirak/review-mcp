package review

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/related"
)

// The related record (X-41, v2.3 spec V-13a §2.3): the overview records the
// related pull requests it was made with, by URL and head revision, in one
// hidden line of its trailer, so that a later incremental review can tell
// whether any of them changed:
//
//	[//]: # (review-mcp:related:<base64url of {"v":1,"items":[{"u":url,"h":head}]}, no padding>)
//
// It follows the rules of the held lines: written only with a reviewed
// commit and when at least one related pull request was read, defused in
// the body, and read from the trailer of review-mcp's own overview only. It
// sits after the issue verdict line and before the held lines.
const (
	relatedLinePrefix = "[//]: # (review-mcp:related:"
	relatedLineSuffix = ")"
	// maxRelatedLineBytes caps the line (spec §2.3 item 2).
	maxRelatedLineBytes  = 10000
	relatedRecordVersion = 1
)

// NoteRelatedChanged: an incremental run whose related pull requests
// differ from the last review's record (spec §2.3 item 3, verbatim).
const NoteRelatedChanged = "A related pull request changed since the last review, so the whole pull request was reviewed."

// relatedRecord is the JSON of the related line.
type relatedRecord struct {
	V     int                 `json:"v"`
	Items []relatedRecordItem `json:"items"`
}

type relatedRecordItem struct {
	U string `json:"u"`
	H string `json:"h"`
}

// valid reports whether a record may be read: version 1, 1 to
// related.MaxItems items, each a related URL (validRelatedURL) with a full
// lower-case hexadecimal head revision, no URL twice.
func (r *relatedRecord) valid() bool {
	if r.V != relatedRecordVersion || len(r.Items) == 0 || len(r.Items) > related.MaxItems {
		return false
	}
	seen := map[string]bool{}
	for _, it := range r.Items {
		if !validRelatedURL(it.U, false) || !isReviewedSHA(it.H) || seen[it.U] {
			return false
		}
		seen[it.U] = true
	}
	return true
}

// validRelatedURL reports whether u is a URL a record may hold: as
// related.Normalize writes it, at most related.MaxURLBytes; "" only when
// empty is allowed.
func validRelatedURL(u string, empty bool) bool {
	if u == "" {
		return empty
	}
	n, ok := related.Normalize(u)
	return ok && n == u
}

// relatedRecordLine returns the related line of the run: the read related
// pull requests, in argument order, with their head revisions; "" when none
// was read, or when the line would be longer than maxRelatedLineBytes.
//
// DESIGN-QUESTION (WP-11b DQ-1): the spec limits the line to 10,000 bytes
// without saying what gives way. Four URLs of up to 2,048 bytes can exceed
// it; the line is then not written, so the next incremental run finds no
// record for its related pull requests and reviews in full (a doubt is a
// change). Recommendation: keep; dropping some items would record a set the
// run did not use.
func (pl *Plan) relatedRecordLine() string {
	var rec relatedRecord
	rec.V = relatedRecordVersion
	for _, it := range related.ReadItems(pl.related) {
		rec.Items = append(rec.Items, relatedRecordItem{U: it.URL, H: it.PR.HeadSHA})
	}
	if len(rec.Items) == 0 || !rec.valid() {
		return ""
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(rec) != nil {
		return ""
	}
	line := relatedLinePrefix + base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))) +
		relatedLineSuffix
	if len(line) > maxRelatedLineBytes {
		return ""
	}
	return line
}

// relatedPayload returns the payload of a line that is exactly a related
// line, else "".
func relatedPayload(line string) string {
	p, ok := strings.CutPrefix(line, relatedLinePrefix)
	if !ok {
		return ""
	}
	if p, ok = strings.CutSuffix(p, relatedLineSuffix); !ok || p == "" || len(line) > maxRelatedLineBytes {
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

// relatedRecordOf returns the related record of an overview body: the line
// that is exactly a related line directly above the run of held lines (or
// the reviewed-commit line) of the trailer, when its record is valid; nil
// otherwise. A related-looking line anywhere else is never read. Call it
// only on a body of our own overview.
func relatedRecordOf(body string) *relatedRecord {
	lines := strings.Split(strings.TrimRight(body, " \t\r\n"), "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	n := len(lines)
	if n < 3 || lines[n-1] != OverviewMarker || !strings.HasPrefix(lines[n-2], reviewedLinePrefix) {
		return nil
	}
	at := n - 3
	for at >= 0 && heldPayload(lines[at]) != "" {
		at--
	}
	if at < 0 {
		return nil
	}
	p := relatedPayload(lines[at])
	if p == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(p)
	if err != nil {
		return nil
	}
	var rec relatedRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&rec) != nil || !rec.valid() {
		return nil
	}
	return &rec
}

// relatedChanged reports whether the run's related pull requests differ
// from the last review's record (spec §2.3 item 3): one of them could not be
// read now; the record is missing while the run read some, or present while
// it read none; or the sets of URLs (in any order) or any head revision
// differ. Ignored items (the primary itself, duplicates) do not count.
func (pl *Plan) relatedChanged() bool {
	for _, it := range pl.related {
		if it.Status == related.StatusFailed {
			return true
		}
	}
	read := related.ReadItems(pl.related)
	rec := pl.lastRelated
	switch {
	case rec == nil:
		return len(read) > 0
	case len(read) != len(rec.Items):
		return true
	}
	heads := map[string]string{}
	for _, it := range rec.Items {
		heads[it.U] = it.H
	}
	for _, it := range read {
		if h, ok := heads[it.URL]; !ok || h != it.PR.HeadSHA {
			return true
		}
	}
	return false
}

// carryRelated returns the carried finding of a held record located in a
// related pull request (h.R): only when the run's related set equals the
// recorded one and h.R is one of its read items; its link then comes from
// that pull request's provider, when the file is one of its changed files.
func (pl *Plan) carryRelated(h heldRecord) (KeyIssue, bool) {
	if pl.lastRelated == nil || pl.relatedChanged() {
		return KeyIssue{}, false
	}
	for _, it := range related.ReadItems(pl.related) {
		if it.URL != h.R {
			continue
		}
		ki := KeyIssue{RelevantFile: h.F, StartLine: h.S, EndLine: h.E, IssueHeader: h.H, IssueContent: h.C,
			CarriedFrom: h.From, Related: it.URL, RelatedName: it.Ref.Repo + "#" + strconv.FormatInt(it.Ref.Number, 10)}
		for i := range it.Diff.Files {
			if it.Diff.Files[i].Path == h.F {
				ki.Link = it.Provider.FileLineURL(it.Ref, it.PR, h.F, h.S)
				break
			}
		}
		return ki, true
	}
	return KeyIssue{}, false
}
