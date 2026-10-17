package review

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
)

// relHead is the head revision of the related merge request of the tests.
const relHead = "1234567890abcdef1234567890abcdef12345678"

// recordLine is the related line of a record with the given URL and head.
func recordLine(t *testing.T, items ...relatedRecordItem) string {
	t.Helper()
	pl := &Plan{}
	for _, it := range items {
		pl.related = append(pl.related, &related.Item{URL: it.U, Read: true, PR: &provider.PullRequest{HeadSHA: it.H}})
	}
	line := pl.relatedRecordLine()
	if line == "" {
		t.Fatal("no related line")
	}
	return line
}

// TestRelatedRecordRoundTrip: the related line is written between the issue
// verdict and the held lines and read back from there; the verdict and the
// held lines are still read; a related line anywhere else, an invalid
// record or one of more than 4 items is never read.
func TestRelatedRecordRoundTrip(t *testing.T) {
	rel := recordLine(t, relatedRecordItem{U: relatedURL, H: relHead})
	verdict := issueLine(oldVerdict())
	held, _ := heldLines(&Result{Review: &Review{KeyIssuesToReview: []KeyIssue{{RelevantFile: "proto/user.proto",
		IssueHeader: "H", IssueContent: "C", StartLine: 1, EndLine: 1, Related: relatedURL}}}}, shaOld)
	if len(held) != 1 {
		t.Fatalf("held %q", held)
	}
	body := withTrailer("Body.", verdict, rel, held, shaOld)
	rec := relatedRecordOf(body)
	if rec == nil || len(rec.Items) != 1 || rec.Items[0] != (relatedRecordItem{U: relatedURL, H: relHead}) {
		t.Fatalf("record %+v\n%s", rec, body)
	}
	if v := issueVerdictOf(body); v == nil || v.Verdict != VerdictNotCompliant {
		t.Error("the issue verdict is no longer read next to the related line")
	}
	recs, _ := heldOf(body)
	if len(recs) != 1 || recs[0].R != relatedURL {
		t.Errorf("held records %+v, want one with r", recs)
	}
	if withTrailer("Body.", "", rel, nil, "") != withTrailer("Body.", "", "", nil, "") {
		t.Error("a related line without a reviewed commit")
	}
	reviewed := reviewedLineOf(shaOld)
	bad := func(js string) string {
		return relatedLinePrefix + base64.RawURLEncoding.EncodeToString([]byte(js)) + relatedLineSuffix
	}
	for name, b := range map[string]string{
		"forged in the body": withTrailer("Text.\n"+rel+"\nMore.", "", "", nil, shaOld),
		"a line between":     "Body.\n" + rel + "\nText.\n" + reviewed + "\n" + OverviewMarker,
		"another version":    "Body.\n" + bad(`{"v":2,"items":[{"u":"`+relatedURL+`","h":"`+relHead+`"}]}`) + "\n" + reviewed + "\n" + OverviewMarker,
		"no items":           "Body.\n" + bad(`{"v":1,"items":[]}`) + "\n" + reviewed + "\n" + OverviewMarker,
		"a short head":       "Body.\n" + bad(`{"v":1,"items":[{"u":"`+relatedURL+`","h":"abc"}]}`) + "\n" + reviewed + "\n" + OverviewMarker,
		"a URL with a query": "Body.\n" + bad(`{"v":1,"items":[{"u":"`+relatedURL+`?x=1","h":"`+relHead+`"}]}`) + "\n" + reviewed + "\n" + OverviewMarker,
		"five items": "Body.\n" + bad(`{"v":1,"items":[`+strings.Repeat(`{"u":"https://a.example/1","h":"`+relHead+`"},`, 4)+
			`{"u":"https://a.example/5","h":"`+relHead+`"}]}`) + "\n" + reviewed + "\n" + OverviewMarker,
	} {
		if got := relatedRecordOf(b); got != nil {
			t.Errorf("%s: read %+v", name, got)
		}
	}
}

// incRelatedHarness is incHarness whose earlier overview recorded the
// commit last and the given trailer lines above it, and whose call can
// read the related merge request.
func incRelatedHarness(last string, lines ...string) *harness {
	h := incHarness("")
	rel := &relProvider{files: []provider.FilePatch{patchOf("proto/user.proto", "PROTO-MARKER", 6)}}
	h.deps.Resolver = relResolver{primary: h.prov, rel: rel}
	body := "Earlier overview.\n\n"
	for _, l := range lines {
		body += l + "\n"
	}
	h.prov.addComment(h.prov.me, body+reviewedLineOf(last)+"\n"+OverviewMarker)
	return h
}

func incRelatedArgs(rel ...string) Args {
	on := true
	return Args{PRURL: testPRURL, Incremental: &on, Related: rel}
}

// TestRelatedIncrementalDecision: an incremental run with the recorded
// related set and heads continues from the record; any difference (a head,
// the set of URLs, a missing or extra record, a related pull request that
// could not be read) makes a full review with the note. Every result
// validates. [canary]
func TestRelatedIncrementalDecision(t *testing.T) {
	same := relatedRecordItem{U: relatedURL, H: relHead}
	const other = "https://gitlab.example/grp/other/-/merge_requests/8"
	cases := []struct {
		name    string
		last    string
		lines   []string
		related []string
		fail    bool
		mode    string
		note    bool
	}{
		{name: "same set, nothing changed", last: shaHead, lines: []string{recordLine(t, same)}, related: []string{relatedURL},
			mode: IncrementalUnchanged},
		{name: "same set, files changed", last: shaOld, lines: []string{recordLine(t, same)}, related: []string{relatedURL},
			mode: IncrementalChanged},
		{name: "a changed head", last: shaHead, lines: []string{recordLine(t, relatedRecordItem{U: relatedURL, H: strings.Repeat("9", 40)})},
			related: []string{relatedURL}, mode: IncrementalFull, note: true},
		{name: "another set", last: shaOld, lines: []string{recordLine(t, relatedRecordItem{U: other, H: relHead})},
			related: []string{relatedURL}, mode: IncrementalFull, note: true},
		{name: "no record, related now", last: shaOld, related: []string{relatedURL}, mode: IncrementalFull, note: true},
		{name: "a record, no related now", last: shaHead, lines: []string{recordLine(t, same)}, mode: IncrementalFull, note: true},
		{name: "a related pull request that failed", last: shaHead, lines: []string{recordLine(t, same)},
			related: []string{relatedURL}, fail: true, mode: IncrementalFull, note: true},
		{name: "no record, no related", last: shaOld, mode: IncrementalChanged},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := incRelatedHarness(c.last, c.lines...)
			if c.fail {
				base := h.deps.Resolver.(relResolver)
				h.deps.Resolver = failResolver{relResolver: base, errs: map[string]error{relatedURL: &provider.Error{Class: provider.ClassUpstream, Status: 500}}}
			}
			res, err := Run(context.Background(), h.deps, incRelatedArgs(c.related...))
			if err != nil {
				t.Fatal(err)
			}
			validateResult(t, res)
			if res.Incremental.Mode != c.mode {
				t.Errorf("mode %s, want %s", res.Incremental.Mode, c.mode)
			}
			if got := slices.Contains(res.Notes, NoteRelatedChanged); got != c.note {
				t.Errorf("note %v, want %v: %q", got, c.note, res.Notes)
			}
			if c.mode == IncrementalUnchanged {
				if e := res.Coverage.Related[0]; e.Status != "not_placed" || e.Reason != ReasonNothingToReview || e.HeadSHA != relHead {
					t.Errorf("entry %+v", e)
				}
			}
		})
	}
}

// TestRelatedHeldFindingIsCarried: a finding of the last review located in
// a related pull request (a held line with r) is carried, with its link
// into that pull request, only when the run's related set is the recorded
// one.
func TestRelatedHeldFindingIsCarried(t *testing.T) {
	held, _ := heldLines(&Result{Review: &Review{KeyIssuesToReview: []KeyIssue{{RelevantFile: "proto/user.proto",
		IssueHeader: "Field renamed", IssueContent: "Old name.", StartLine: 1, EndLine: 1, Related: relatedURL}}}}, shaOld)
	record := recordLine(t, relatedRecordItem{U: relatedURL, H: relHead})

	h := incRelatedHarness(shaOld, append([]string{record}, held...)...)
	res, err := Run(context.Background(), h.deps, incRelatedArgs(relatedURL))
	if err != nil {
		t.Fatal(err)
	}
	validateResult(t, res)
	var carried *KeyIssue
	for i := range res.Review.KeyIssuesToReview {
		if ki := &res.Review.KeyIssuesToReview[i]; ki.CarriedFrom != "" {
			carried = ki
		}
	}
	if carried == nil || carried.Related != relatedURL || carried.RelatedName != "svc#3" ||
		carried.Link != "https://gitlab.example/grp/svc/-/blob/"+relHead+"/proto/user.proto#L1" || carried.InlineStatus != "" {
		t.Fatalf("carried %+v", carried)
	}

	other := recordLine(t, relatedRecordItem{U: relatedURL, H: strings.Repeat("9", 40)})
	h = incRelatedHarness(shaOld, append([]string{other}, held...)...)
	res, err = Run(context.Background(), h.deps, incRelatedArgs(relatedURL))
	if err != nil {
		t.Fatal(err)
	}
	for _, ki := range res.Review.KeyIssuesToReview {
		if ki.CarriedFrom != "" {
			t.Errorf("a finding was carried although the related set changed: %+v", ki)
		}
	}
}

// TestRelatedRecordIsWritten: a published review with a related pull
// request that was read records it in the overview's trailer; without one
// nothing is recorded.
func TestRelatedRecordIsWritten(t *testing.T) {
	h, _ := relatedHarness(goodAnswer)
	h.prov.pr.HeadSHA = shaHead
	runRelated(t, h, Args{Related: []string{relatedURL}, Publish: true})
	body := h.prov.posted[len(h.prov.posted)-1]
	if rec := relatedRecordOf(body); rec == nil || len(rec.Items) != 1 || rec.Items[0].H != relHead {
		t.Errorf("record %+v in\n%s", rec, body)
	}
	h, _ = relatedHarness(goodAnswer)
	h.prov.pr.HeadSHA = shaHead
	runRelated(t, h, Args{Publish: true})
	if strings.Contains(h.prov.posted[len(h.prov.posted)-1], relatedLinePrefix) {
		t.Error("a related line without related pull requests")
	}
}
