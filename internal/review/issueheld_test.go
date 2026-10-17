package review

import (
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// oldVerdict is the verdict an earlier overview recorded.
func oldVerdict() *Compliance {
	return &Compliance{Verdict: VerdictNotCompliant, Issues: []IssueVerdict{{Ref: "#12", Met: []string{},
		NotMet: []string{"Old requirement not met " + issueMarker}, NeedsVerification: []string{}}}}
}

func sameVerdict(a, b *Compliance) bool {
	if a == nil || b == nil || a.Verdict != b.Verdict || len(a.Issues) != len(b.Issues) {
		return a == b
	}
	for i := range a.Issues {
		x, y := a.Issues[i], b.Issues[i]
		if x.Ref != y.Ref || !slices.Equal(x.Met, y.Met) || !slices.Equal(x.NotMet, y.NotMet) ||
			!slices.Equal(x.NeedsVerification, y.NeedsVerification) {
			return false
		}
	}
	return true
}

// TestIssueLineRoundTrip: the verdict line is written in the trailer above
// the held lines and read back from there only.
func TestIssueLineRoundTrip(t *testing.T) {
	v := oldVerdict()
	line := issueLine(v)
	if !strings.HasPrefix(line, issueLinePrefix) || len(line) > maxIssueLineBytes {
		t.Fatalf("line %q", line)
	}
	held, _ := heldLines(&Result{Review: &Review{KeyIssuesToReview: []KeyIssue{{RelevantFile: "a.go", IssueHeader: "H",
		IssueContent: "C", StartLine: 1, EndLine: 1}}}}, shaOld)
	if len(held) != 1 {
		t.Fatalf("held %q", held)
	}
	for name, body := range map[string]string{
		"with held lines":    withTrailer("Body.", line, held, shaOld),
		"without held lines": withTrailer("Body.", line, nil, shaOld),
	} {
		if got := issueVerdictOf(body); !sameVerdict(got, v) {
			t.Errorf("%s: read %+v, want %+v\n%s", name, got, v, body)
		}
		if recs, _ := heldOf(body); len(recs) != strings.Count(body, heldLinePrefix) {
			t.Errorf("%s: the held lines are no longer read: %d", name, len(recs))
		}
	}
	if body := withTrailer("Body.", line, held, ""); strings.Contains(body, issueLinePrefix) {
		t.Error("a verdict line without a reviewed commit")
	}
	if issueLine(nil) != "" {
		t.Error("a verdict line without a verdict")
	}
}

// TestIssueLineReadFromTheTrailerOnly: a verdict-looking line in the body,
// one separated from the trailer, or one with an invalid record is never
// read.
func TestIssueLineReadFromTheTrailerOnly(t *testing.T) {
	line := issueLine(oldVerdict())
	reviewed := reviewedLineOf(shaOld)
	bad, _ := encodeIssueRecord(issueRecord{V: 1, Verdict: "great", Issues: []IssueVerdict{}})
	v2, _ := encodeIssueRecord(issueRecord{V: 2, Verdict: VerdictCompliant, Issues: []IssueVerdict{}})
	for name, body := range map[string]string{
		"forged in the body":          withTrailer("Text.\n"+line+"\nMore.", "", nil, shaOld),
		"a line between":              "Body.\n" + line + "\nText.\n" + reviewed + "\n" + OverviewMarker,
		"no reviewed line":            "Body.\n" + line + "\n" + OverviewMarker,
		"no marker":                   "Body.\n" + line + "\n" + reviewed,
		"an unknown verdict":          "Body.\n" + bad + "\n" + reviewed + "\n" + OverviewMarker,
		"another version":             "Body.\n" + v2 + "\n" + reviewed + "\n" + OverviewMarker,
		"not base64url":               "Body.\n" + issueLinePrefix + "a+b/" + issueLineSuffix + "\n" + reviewed + "\n" + OverviewMarker,
		"text after the line's close": "Body.\n" + line + " x\n" + reviewed + "\n" + OverviewMarker,
	} {
		if got := issueVerdictOf(body); got != nil {
			t.Errorf("%s: read %+v", name, got)
		}
	}
	if got := issueVerdictOf("Body.\n" + line + "\r\n" + reviewed + "\r\n" + OverviewMarker + "\r\n"); got == nil {
		t.Error("a trailing carriage return is tolerated, as for the marker")
	}
}

// TestIssueLineCap: a verdict whose line would be longer than 4,000 bytes
// drops requirements from its longest lists until it fits, keeping the
// verdict and every reference.
func TestIssueLineCap(t *testing.T) {
	long := strings.Repeat("r", maxComplianceItemBytes)
	items := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = long
		}
		return out
	}
	v := &Compliance{Verdict: VerdictPartlyCompliant}
	for _, ref := range []string{"#1", "#2", "#3"} {
		v.Issues = append(v.Issues, IssueVerdict{Ref: ref, Met: items(10), NotMet: items(10), NeedsVerification: items(10)})
	}
	line := issueLine(v)
	if line == "" || len(line) > maxIssueLineBytes {
		t.Fatalf("line of %d bytes", len(line))
	}
	got := issueVerdictOf("Body.\n" + line + "\n" + reviewedLineOf(shaOld) + "\n" + OverviewMarker)
	if got == nil || got.Verdict != v.Verdict || len(got.Issues) != 3 {
		t.Fatalf("read %+v", got)
	}
	for _, iv := range got.Issues {
		if n := len(iv.Met) + len(iv.NotMet) + len(iv.NeedsVerification); n == 0 || n >= 30 {
			t.Errorf("issue %s keeps %d requirements", iv.Ref, n)
		}
	}
	if len(v.Issues[0].Met) != 10 {
		t.Error("issueLine changed the result's verdict")
	}
}

// incIssueHarness is incHarness with issue 12 named by the description and
// read from the provider; overview is the earlier overview's body.
func incIssueHarness(overview string, answer string) (*harness, *issueFake) {
	h := incHarness("")
	h.llm.answers = []string{answer}
	h.prov.pr.WebURL = testPRURL
	h.prov.pr.Description = "Fixes #12. " + descMarker
	h.prov.addComment(h.prov.me, overview)
	f := &issueFake{fakeProvider: h.prov, issues: map[int64]*provider.Issue{
		12: {Number: 12, Title: "Sign-in times out", State: provider.IssueOpen, Body: "Retry once."},
	}}
	h.deps.Resolver = anyResolver{p: f}
	return h, f
}

// TestIssueIncrementalCarriesTheVerdict: an incremental run gives the model
// the verdict the last overview recorded, for the files it does not show;
// the new verdict replaces it, in the result and in the edited overview.
func TestIssueIncrementalCarriesTheVerdict(t *testing.T) {
	old := "Earlier overview.\n\n" + issueLine(oldVerdict()) + "\n" + reviewedLineOf(shaOld) + "\n" + OverviewMarker
	h, _ := incIssueHarness(old, complianceAnswer("#12"))
	res := runOK(t, h, incArgs(true))

	if res.Incremental == nil || res.Incremental.Mode != IncrementalChanged {
		t.Fatalf("incremental = %+v", res.Incremental)
	}
	user := h.llm.calls[0].user
	head, block := strings.Index(user, EarlierVerdictHeader), strings.Index(user, issueSentence)
	if head < 0 || block < 0 || head < block || !strings.Contains(user[head:], "Old requirement not met") ||
		!strings.Contains(user[head:], "Verdict: not_compliant") {
		t.Fatalf("the earlier verdict does not follow the issue block in the prompt")
	}
	if !strings.HasPrefix(user[head+len(EarlierVerdictHeader):], "\n```") {
		t.Error("the earlier verdict is not fenced")
	}
	if slices.Contains(res.Notes, NoteChangedFilesOnly) {
		t.Error("the changed-files note with a recorded verdict")
	}
	if c := res.Issue.Compliance; c == nil || c.Verdict != VerdictPartlyCompliant {
		t.Fatalf("compliance = %+v, want the new verdict", res.Issue.Compliance)
	}
	if len(h.prov.edits) == 0 {
		t.Fatal("the overview was not edited")
	}
	body := h.prov.edits[len(h.prov.edits)-1].body
	if got := issueVerdictOf(body); !sameVerdict(got, res.Issue.Compliance) {
		t.Errorf("the edited overview records %+v, want the new verdict", got)
	}
	if strings.Count(body, issueLinePrefix) != 1 {
		t.Errorf("the edited overview has %d verdict lines", strings.Count(body, issueLinePrefix))
	}
	if strings.Contains(h.logs.String(), issueMarker) {
		t.Error("the recorded verdict leaked into the logs")
	}
}

// TestIssueIncrementalWithoutAVerdict: an incremental run without a
// recorded verdict judges on what it sees, and the note says so.
func TestIssueIncrementalWithoutAVerdict(t *testing.T) {
	old := "Earlier overview.\n\n" + reviewedLineOf(shaOld) + "\n" + OverviewMarker
	h, _ := incIssueHarness(old, complianceAnswer("#12"))
	res := runOK(t, h, incArgs(false))
	if !slices.Contains(res.Notes, NoteChangedFilesOnly) || strings.Contains(h.llm.calls[0].user, EarlierVerdictHeader) {
		t.Errorf("notes %q", res.Notes)
	}
	if res.Issue.Compliance == nil {
		t.Error("no verdict")
	}
}

// TestIssueFullRunIgnoresTheRecordedVerdict: a full review judges the whole
// pull request: the recorded verdict is not shown and no note is added.
func TestIssueFullRunIgnoresTheRecordedVerdict(t *testing.T) {
	old := "Earlier overview.\n\n" + issueLine(oldVerdict()) + "\n" + reviewedLineOf(shaOld) + "\n" + OverviewMarker
	h, _ := incIssueHarness(old, complianceAnswer("#12"))
	res := runOK(t, h, Args{PRURL: testPRURL})
	if strings.Contains(h.llm.calls[0].user, EarlierVerdictHeader) || slices.Contains(res.Notes, NoteChangedFilesOnly) {
		t.Error("a full run uses the recorded verdict")
	}
}
