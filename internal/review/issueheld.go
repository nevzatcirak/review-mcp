package review

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/projctx"
)

// The issue verdict line (X-42, v2.2 spec V-2 §2.4): the overview records
// its compliance verdict in one hidden line of its trailer, so that a later
// incremental review, which shows the model the changed files only, can
// give it the verdict of the last review for the files it does not show.
// The line follows the rules of the held lines (X-36, held.go):
//
//	[//]: # (review-mcp:issue:<base64url of compact JSON, no padding>)
//
// It sits directly above the held lines (or, without them, directly above
// the reviewed-commit line), is written only with a reviewed commit, is at
// most maxIssueLineBytes long, and is read from the trailer of review-mcp's
// own overview only. The new verdict of a run replaces it.
const (
	issueLinePrefix = "[//]: # (review-mcp:issue:"
	issueLineSuffix = ")"
	// maxIssueLineBytes caps the verdict line (spec §2.4).
	maxIssueLineBytes = 4000
	// issueRecordVersion is the only record version.
	issueRecordVersion = 1
)

// EarlierVerdictHeader is the line above the fenced verdict of the last
// review in the user prompt of an incremental run (spec §2.4 wording).
const EarlierVerdictHeader = "The issue compliance verdict of the last review, for the files not shown (recorded by " +
	"review-mcp from an earlier answer; replace it with your own verdict for the whole pull request):"

// NoteChangedFilesOnly: an incremental run with an issue but no recorded
// verdict of the last review (spec §2.4, verbatim).
const NoteChangedFilesOnly = "Issue compliance was judged on the changed files only."

// issueRecord is the JSON of the verdict line.
type issueRecord struct {
	V       int            `json:"v"`
	Verdict string         `json:"verdict"`
	Issues  []IssueVerdict `json:"issues"`
}

// valid reports whether a record may be read: version 1, a known verdict,
// at most 3 issues with a ref, and lists and texts within the caps of a
// validated answer.
func (r *issueRecord) valid() bool {
	if r.V != issueRecordVersion || !slices.Contains(Verdicts, r.Verdict) || len(r.Issues) > maxComplianceIssueItems {
		return false
	}
	ok := func(s string) bool { return s != "" && len(s) <= maxComplianceItemBytes }
	for _, iv := range r.Issues {
		if !ok(iv.Ref) {
			return false
		}
		for _, l := range [][]string{iv.Met, iv.NotMet, iv.NeedsVerification} {
			if len(l) > maxComplianceListItems || slices.ContainsFunc(l, func(s string) bool { return !ok(s) }) {
				return false
			}
		}
	}
	return true
}

// issueLine returns the verdict line of c, "" without one. When the line
// would be longer than maxIssueLineBytes, requirements are dropped from the
// end of the longest list until it fits.
//
// DESIGN-QUESTION (WP-10b DQ-7): the spec caps the line at 4,000 bytes
// without saying what gives way. The line keeps the verdict and every
// reference, and drops requirements from the end of the longest list first,
// so that each issue keeps some of each list. Recommendation: keep.
func issueLine(c *Compliance) string {
	if c == nil {
		return ""
	}
	rec := issueRecord{V: issueRecordVersion, Verdict: c.Verdict, Issues: make([]IssueVerdict, len(c.Issues))}
	for i, iv := range c.Issues {
		rec.Issues[i] = IssueVerdict{Ref: iv.Ref, Met: slices.Clone(iv.Met), NotMet: slices.Clone(iv.NotMet),
			NeedsVerification: slices.Clone(iv.NeedsVerification)}
	}
	if !rec.valid() {
		return ""
	}
	for {
		line, ok := encodeIssueRecord(rec)
		if !ok {
			return ""
		}
		if len(line) <= maxIssueLineBytes {
			return line
		}
		if !dropLongest(rec.Issues) {
			return ""
		}
	}
}

// dropLongest removes the last item of the longest list of issues (the
// first such list on a tie); false when every list is empty.
func dropLongest(issues []IssueVerdict) bool {
	var longest *[]string
	for i := range issues {
		for _, l := range []*[]string{&issues[i].Met, &issues[i].NotMet, &issues[i].NeedsVerification} {
			if len(*l) > 0 && (longest == nil || len(*l) > len(*longest)) {
				longest = l
			}
		}
	}
	if longest == nil {
		return false
	}
	*longest = (*longest)[:len(*longest)-1]
	return true
}

func encodeIssueRecord(rec issueRecord) (string, bool) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return "", false
	}
	payload := base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return issueLinePrefix + payload + issueLineSuffix, true
}

// issueVerdictOf returns the verdict an overview body records: the line
// that is exactly a verdict line directly above the run of held lines (or
// the reviewed-commit line) of the trailer, when its record is valid; nil
// otherwise. A verdict-looking line anywhere else is never read. Call it
// only on a body of our own overview.
func issueVerdictOf(body string) *Compliance {
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
	p, ok := strings.CutPrefix(lines[at], issueLinePrefix)
	if !ok {
		return nil
	}
	if p, ok = strings.CutSuffix(p, issueLineSuffix); !ok || p == "" || len(lines[at]) > maxIssueLineBytes {
		return nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(p)
	if err != nil {
		return nil
	}
	var rec issueRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&rec) != nil || !rec.valid() {
		return nil
	}
	c := &Compliance{Verdict: rec.Verdict, Issues: rec.Issues}
	for i := range c.Issues {
		for _, l := range []*[]string{&c.Issues[i].Met, &c.Issues[i].NotMet, &c.Issues[i].NeedsVerification} {
			if *l == nil {
				*l = []string{}
			}
		}
	}
	return c
}

// earlierVerdictBlock is the prompt block of the last review's verdict:
// EarlierVerdictHeader and the verdict in a fence (it is model-written text
// that came from the issue); "" without one.
func earlierVerdictBlock(c *Compliance) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Verdict: " + c.Verdict + "\n")
	for _, iv := range c.Issues {
		b.WriteString("\nIssue " + iv.Ref + "\n")
		for _, l := range []struct {
			label string
			items []string
		}{{"Met", iv.Met}, {"Not met", iv.NotMet}, {"Needs verification", iv.NeedsVerification}} {
			if len(l.items) == 0 {
				continue
			}
			b.WriteString(l.label + ":\n")
			for _, it := range l.items {
				b.WriteString("- " + strings.ReplaceAll(it, "\n", " ") + "\n")
			}
		}
	}
	return projctx.Fenced(EarlierVerdictHeader, strings.TrimRight(b.String(), "\n"))
}
