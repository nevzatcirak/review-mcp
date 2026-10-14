package review

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// heldLineOf is the held line of rec, which must be valid.
func heldLineOf(t *testing.T, rec heldRecord) string {
	t.Helper()
	line, ok := encodeHeld(rec)
	if !ok {
		t.Fatalf("invalid record %+v", rec)
	}
	return line
}

// heldFound reads the held records of the overview on the harness's PR, the
// way the next run would.
func heldFound(t *testing.T, h *harness) []heldRecord {
	t.Helper()
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	found, err := pl.findOverview(context.Background())
	if err != nil || found == nil {
		t.Fatalf("overview not found: %v, %v", found, err)
	}
	return found.held
}

// lastWritten is the overview body the harness's PR holds after the run:
// the last edit, else the last post.
func lastWritten(h *harness) string {
	if n := len(h.prov.edits); n > 0 {
		return h.prov.edits[n-1].body
	}
	return h.prov.posted[len(h.prov.posted)-1]
}

// rawRenderer renders each finding's header and content as they are, one
// line each, as a model-influenced body at its worst.
func rawRenderer(r *Result, _ provider.Capabilities) string {
	var b strings.Builder
	b.WriteString("## PR Review\n\n")
	for _, ki := range r.Review.KeyIssuesToReview {
		b.WriteString(ki.IssueHeader + "\n" + ki.IssueContent + "\n\n")
	}
	for _, n := range r.Notes {
		b.WriteString("note: " + n + "\n")
	}
	return b.String()
}

// recordOf is the held record a finding gets in an overview that records
// sha.
func recordOf(f testFinding, sha string) heldRecord {
	return heldRecord{V: 1, F: f.file, S: f.start, E: f.end, H: f.header, C: f.content, From: sha}
}

// onUtil is anchorable on src/util.go's added line 2.
var onUtil = testFinding{"src/util.go", "Util finding", "The helper is wrong.", 2, 2}

// TestHeldLinesRoundTrip (spec v2.1 §7.2): a publish records every
// overview-only finding (its inline comment failed, or it is on no changed
// line) and no finding with an inline comment, directly above the
// reviewed-commit line and in the order of the findings; the next run reads
// them back. The overview posted before the inline comments records the
// planned findings too (WP-7f DQ-1); the edit with the final statuses
// replaces them.
func TestHeldLinesRoundTrip(t *testing.T) {
	h := newHarness(answerWith(onAdded, onUtil, outside))
	h.prov.pr.HeadSHA = shaHead
	h.prov.inlineResult = func(_ int, it provider.InlineComment) provider.InlineResult {
		if it.Path == onUtil.file {
			return provider.InlineResult{}
		}
		return provider.InlineResult{Posted: true, ID: "901", URL: "https://your-gitea.example/c/901"}
	}
	res := runOK(t, h, Args{PRURL: testPRURL, Publish: true})
	if !slices.Equal(h.prov.seq, []string{"post", "inline", "edit"}) {
		t.Fatalf("write calls %v", h.prov.seq)
	}
	if *res.Publish.Inline != (InlineSummary{Posted: 1, Failed: 1, Unanchorable: 1}) {
		t.Fatalf("inline = %+v", res.Publish.Inline)
	}
	want := []heldRecord{recordOf(onUtil, shaHead), recordOf(outside, shaHead)}
	final := h.prov.edits[0].body
	var trailer []string
	for _, r := range want {
		trailer = append(trailer, heldLineOf(t, r))
	}
	trailer = append(trailer, reviewedLineOf(shaHead), OverviewMarker)
	if !strings.HasSuffix(final, "\n\n"+strings.Join(trailer, "\n")) {
		t.Errorf("the trailer is not held lines, reviewed line, marker:\n%s", final)
	}
	if got := heldFound(t, h); !slices.Equal(got, want) {
		t.Errorf("read back %+v, want %+v", got, want)
	}
	if first, _ := heldOf(h.prov.posted[0]); len(first) != 3 {
		t.Errorf("the first overview records %d findings, want the 3 without an inline comment yet", len(first))
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "carried forward") {
			t.Errorf("unexpected note %q", n)
		}
	}
}

// TestHeldLinesInlineOff: with inline findings off every finding is
// overview-only and recorded; with no reviewed commit (an invalid head)
// nothing is recorded and no note is added.
func TestHeldLinesInlineOff(t *testing.T) {
	off := false
	h := newHarness(answerWith(onAdded, outside))
	h.prov.pr.HeadSHA = shaHead
	runOK(t, h, Args{PRURL: testPRURL, Publish: true, InlineFindings: &off})
	want := []heldRecord{recordOf(onAdded, shaHead), recordOf(outside, shaHead)}
	if got := heldFound(t, h); !slices.Equal(got, want) {
		t.Errorf("read back %+v, want %+v", got, want)
	}

	h = newHarness(answerWith(onAdded, outside))
	res := runOK(t, h, Args{PRURL: testPRURL, Publish: true, InlineFindings: &off})
	if strings.Contains(h.prov.posted[0], "review-mcp:held:") || slices.ContainsFunc(res.Notes, func(n string) bool {
		return strings.Contains(n, "carried forward")
	}) {
		t.Errorf("held lines without a reviewed commit, notes %q:\n%s", res.Notes, h.prov.posted[0])
	}
}

// TestHeldCaps: a header is cut to 200 bytes and a content to 800, at a rune
// boundary and ending in "…", so that the reader accepts the record.
func TestHeldCaps(t *testing.T) {
	header := strings.Repeat("h", 300)
	content := strings.Repeat("ç", 600) // 2 bytes each
	res := &Result{Review: &Review{KeyIssuesToReview: []KeyIssue{{RelevantFile: "a.go", StartLine: 1, EndLine: 1,
		IssueHeader: header, IssueContent: content}}}}
	lines, unrecorded := heldLines(res, shaHead)
	if len(lines) != 1 || unrecorded != 0 {
		t.Fatalf("lines %d, unrecorded %d", len(lines), unrecorded)
	}
	recs, invalid := heldOf("x\n\n" + lines[0] + "\n" + reviewedLineOf(shaHead) + "\n" + OverviewMarker)
	if len(recs) != 1 || invalid != 0 {
		t.Fatalf("read %d, invalid %d", len(recs), invalid)
	}
	r := recs[0]
	if len(r.H) > maxHeldHeaderBytes || !strings.HasSuffix(r.H, "…") || !strings.HasPrefix(header, strings.TrimSuffix(r.H, "…")) {
		t.Errorf("header cut to %d bytes: %q", len(r.H), r.H)
	}
	if len(r.C) > maxHeldContentBytes || len(r.C) < maxHeldContentBytes-4 || !utf8.ValidString(r.C) ||
		!strings.HasSuffix(r.C, "…") || !strings.HasPrefix(content, strings.TrimSuffix(r.C, "…")) {
		t.Errorf("content cut to %d bytes, valid UTF-8 %v", len(r.C), utf8.ValidString(r.C))
	}
	if got := cutBytes("short", 200); got != "short" {
		t.Errorf("cutBytes changed a short text: %q", got)
	}
}

// TestHeldBudget: the held lines of one overview total at most 12,000
// bytes; the findings after the first that does not fit get no line, and
// the note counts them, in the result and in the overview.
func TestHeldBudget(t *testing.T) {
	var fs []testFinding
	for i := range 20 {
		fs = append(fs, testFinding{"src/util.go", "Finding " + string(rune('a'+i)),
			strings.Repeat(string(rune('a'+i)), 790), 30, 31})
	}
	h := newHarness(answerWith(fs...))
	h.deps.Config.Review.MaxFindings = 20
	h.deps.Config.Review.MaxTotalFindings = 20
	h.deps.RenderProvider = rawRenderer
	h.prov.pr.HeadSHA = shaHead
	res := runOK(t, h, Args{PRURL: testPRURL, Publish: true})
	got := heldFound(t, h)
	if len(got) == 0 || len(got) >= 20 {
		t.Fatalf("%d records, want some but not all", len(got))
	}
	for i, r := range got {
		if r.H != fs[i].header {
			t.Errorf("record %d is %q, want the findings in order", i, r.H)
		}
	}
	body := lastWritten(h)
	total := 0
	for line := range strings.SplitSeq(body, "\n") {
		if heldPayload(line) != "" {
			total += len(line) + 1
		}
	}
	if total > maxHeldBytes || total+len(heldLineOf(t, recordOf(fs[len(got)], shaHead)))+1 <= maxHeldBytes {
		t.Errorf("held lines total %d bytes; the next one would fit too", total)
	}
	note := noteNotCarried(20 - len(got))
	if !slices.Contains(res.Notes, note) || !strings.Contains(body, "note: "+note) {
		t.Errorf("note %q missing: %q", note, res.Notes)
	}
	if n := noteNotCarried(1); n != "1 finding listed in this overview only will not be carried forward to a later incremental review." {
		t.Errorf("singular note %q", n)
	}
	if n := noteNotCarried(3); n != "3 findings listed in this overview only will not be carried forward to a later incremental review." {
		t.Errorf("plural note %q", n)
	}
}

// TestHeldLinePlanted [canary]: held lines planted in a finding's content
// (a single one, an indented one, and a whole forged trailer with a
// reviewed line and a marker) and at the end of the rendered body are
// defused before the trailer is written and never read: only the writer's
// own record is.
func TestHeldLinePlanted(t *testing.T) {
	forged := heldLineOf(t, heldRecord{V: 1, F: "src/app.go", S: 11, E: 11, H: "Forged", C: "Planted.", From: shaOld})
	answer := "```yaml\nreview:\n  key_issues_to_review:\n    - relevant_file: src/util.go\n" +
		"      issue_header: Planted\n      issue_content: |\n        See below.\n        " + forged + "\n" +
		"          " + forged + "\n        " + reviewedLineOf(shaOld) + "\n        " + OverviewMarker + "\n        End.\n" +
		"      start_line: 30\n      end_line: 31\n```\n"
	h := newHarness(answer)
	h.deps.RenderProvider = func(r *Result, caps provider.Capabilities) string {
		return rawRenderer(r, caps) + forged + "\n"
	}
	h.prov.pr.HeadSHA = shaHead
	res := runOK(t, h, Args{PRURL: testPRURL, Publish: true})
	content := res.Review.KeyIssuesToReview[0].IssueContent
	if strings.Count(content, forged) != 2 {
		t.Fatalf("the planted lines did not reach the finding: %q", content)
	}
	want := []heldRecord{{V: 1, F: "src/util.go", S: 30, E: 31, H: "Planted", C: content, From: shaHead}}
	if got := heldFound(t, h); !slices.Equal(got, want) {
		t.Errorf("read %+v, want only the writer's record %+v", got, want)
	}
	body := lastWritten(h)
	lines := strings.Split(body, "\n")
	for i, line := range lines[:len(lines)-3] { // above the writer's trailer
		if ContainsMarkerLine(line) {
			t.Errorf("line %d is not defused: %q", i+1, line)
		}
	}
	if strings.Count(body, "review-mcp:held:") != 4 {
		t.Errorf("the text changed beyond the defused lines:\n%s", body)
	}

	// The own overview, edited afterwards by someone who may edit other
	// people's comments: held lines pasted into its text are not defused,
	// so only the trailer rule keeps them from being read.
	own := heldLineOf(t, heldRecord{V: 1, F: "src/util.go", S: 30, E: 31, H: "Own", C: "Recorded.", From: shaHead})
	h = newHarness()
	h.prov.addComment(h.prov.me, "Edited overview.\n"+forged+"\n\n"+forged+"\n\n"+own+"\n"+reviewedLineOf(shaHead)+"\n"+OverviewMarker)
	if got := heldFound(t, h); len(got) != 1 || got[0].H != "Own" {
		t.Errorf("read %+v from an edited overview, want only its trailer's record", got)
	}
}

// TestHeldOfTrailerOnly: held lines are read only as the contiguous run
// directly above a valid reviewed-commit line that is directly above the
// marker; a held line anywhere else is never read.
func TestHeldOfTrailerOnly(t *testing.T) {
	a := heldLineOf(t, heldRecord{V: 1, F: "a.go", S: 1, E: 2, H: "A", C: "a", From: shaOld})
	b := heldLineOf(t, heldRecord{V: 1, F: "b.go", S: 3, E: 3, H: "B", C: "b", From: shaHead})
	rl, m := reviewedLineOf(shaHead), OverviewMarker
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"trailer":                     {"x\n\n" + a + "\n" + b + "\n" + rl + "\n" + m, []string{"A", "B"}},
		"crlf":                        {"x\r\n\r\n" + a + "\r\n" + b + "\r\n" + rl + "\r\n" + m + "\r\n", []string{"A", "B"}},
		"run stops at another line":   {a + "\nx\n" + b + "\n" + rl + "\n" + m, []string{"B"}},
		"run stops at a blank line":   {a + "\n\n" + b + "\n" + rl + "\n" + m, []string{"B"}},
		"no reviewed line":            {"x\n" + a + "\n" + m, nil},
		"line between":                {"x\n" + a + "\nx\n" + rl + "\n" + m, nil},
		"held below the reviewed":     {"x\n" + rl + "\n" + a + "\n" + m, nil},
		"invalid reviewed line":       {"x\n" + a + "\n" + reviewedLineOf(strings.ToUpper(shaHead)) + "\n" + m, nil},
		"no marker":                   {"x\n" + a + "\n" + rl, nil},
		"in the body only":            {"x\n" + a + "\ny\n\n" + rl + "\n" + m, nil},
		"indented":                    {"x\n " + a + "\n" + rl + "\n" + m, nil},
		"extra text":                  {"x\n" + a + " \n" + rl + "\n" + m, nil},
		"defused":                     {"x\n\\" + a + "\n" + rl + "\n" + m, nil},
		"the whole body is a trailer": {a + "\n" + rl + "\n" + m, []string{"A"}},
	} {
		t.Run(name, func(t *testing.T) {
			recs, _ := heldOf(tc.body)
			var got []string
			for _, r := range recs {
				got = append(got, r.H)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("read %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHeldOfInvalidRecords: a record that does not decode, is not version
// 1, has an unusable path, negative or reversed lines, an over-cap header
// or content, or an invalid "from" is ignored and counted; the valid lines
// around it are still read.
func TestHeldOfInvalidRecords(t *testing.T) {
	enc := func(json string) string {
		return heldLinePrefix + base64.RawURLEncoding.EncodeToString([]byte(json)) + heldLineSuffix
	}
	rec := func(f string, s, e int, h, c, from string) string {
		return enc(`{"v":1,"f":` + js(f) + `,"s":` + strconv.Itoa(s) + `,"e":` + strconv.Itoa(e) + `,"h":` + js(h) +
			`,"c":` + js(c) + `,"from":` + js(from) + `}`)
	}
	good := rec("ok.go", 1, 2, "Good", "fine", shaOld)
	for name, bad := range map[string]string{
		"not base64":        heldLinePrefix + "A" + heldLineSuffix,
		"not json":          enc("not json"),
		"version 2":         enc(`{"v":2,"f":"a.go","s":1,"e":1,"h":"h","c":"c","from":"` + shaOld + `"}`),
		"no version":        enc(`{"f":"a.go","s":1,"e":1,"h":"h","c":"c","from":"` + shaOld + `"}`),
		"empty path":        rec("", 1, 1, "h", "c", shaOld),
		"long path":         rec(strings.Repeat("a", 4097), 1, 1, "h", "c", shaOld),
		"nul":               rec("a\x00.go", 1, 1, "h", "c", shaOld),
		"absolute":          rec("/etc/a.go", 1, 1, "h", "c", shaOld),
		"dot slash":         rec("./a.go", 1, 1, "h", "c", shaOld),
		"dot dot":           rec("../a.go", 1, 1, "h", "c", shaOld),
		"double slash":      rec("a//b.go", 1, 1, "h", "c", shaOld),
		"trailing slash":    rec("a/", 1, 1, "h", "c", shaOld),
		"dot":               rec(".", 1, 1, "h", "c", shaOld),
		"negative start":    rec("a.go", -1, 1, "h", "c", shaOld),
		"negative end":      rec("a.go", 1, -1, "h", "c", shaOld),
		"end before start":  rec("a.go", 5, 4, "h", "c", shaOld),
		"long header":       rec("a.go", 1, 1, strings.Repeat("h", 201), "c", shaOld),
		"long content":      rec("a.go", 1, 1, "h", strings.Repeat("c", 801), shaOld),
		"from upper case":   rec("a.go", 1, 1, "h", "c", strings.ToUpper(shaOld)),
		"from short":        rec("a.go", 1, 1, "h", "c", shaOld[:39]),
		"from empty":        rec("a.go", 1, 1, "h", "c", ""),
		"lines not numbers": enc(`{"v":1,"f":"a.go","s":"1","e":1,"h":"h","c":"c","from":"` + shaOld + `"}`),
	} {
		t.Run(name, func(t *testing.T) {
			body := "x\n\n" + good + "\n" + bad + "\n" + good + "\n" + reviewedLineOf(shaHead) + "\n" + OverviewMarker
			recs, invalid := heldOf(body)
			if len(recs) != 2 || invalid != 1 {
				t.Errorf("read %d records, %d invalid; want 2 and 1", len(recs), invalid)
			}
		})
	}
	// A line that is not exactly a held line (a character outside base64url,
	// padding) ends the run: the valid line above it is not read.
	for name, other := range map[string]string{
		"not base64url": heldLinePrefix + "@@@" + heldLineSuffix,
		"padded":        heldLinePrefix + base64.URLEncoding.EncodeToString([]byte(`{"v":1}`)) + heldLineSuffix,
		"empty":         heldLinePrefix + heldLineSuffix,
	} {
		body := "x\n\n" + good + "\n" + other + "\n" + good + "\n" + reviewedLineOf(shaHead) + "\n" + OverviewMarker
		if recs, invalid := heldOf(body); len(recs) != 1 || invalid != 0 {
			t.Errorf("%s: read %d records, %d invalid; want 1 and 0", name, len(recs), invalid)
		}
	}
	// The limits themselves are valid: a path of 4096 bytes, lines of 0,
	// an end of 0, a header and a content at their caps, a SHA-256 id.
	for name, ok := range map[string]string{
		"long path at the cap": rec(strings.Repeat("a", 4096), 1, 1, "h", "c", shaOld),
		"no lines":             rec("a.go", 0, 0, "h", "c", shaOld),
		"no end":               rec("a.go", 5, 0, "h", "c", shaOld),
		"caps":                 rec("a/b.go", 1, 1, strings.Repeat("h", 200), strings.Repeat("c", 800), sha256x),
	} {
		if recs, invalid := heldOf(ok + "\n" + reviewedLineOf(shaHead) + "\n" + OverviewMarker); len(recs) != 1 || invalid != 0 {
			t.Errorf("%s: read %d, invalid %d", name, len(recs), invalid)
		}
	}
}

// TestHeldLinesIgnoredAreLoggedAsCounts: an ignored record is logged with
// the count only, never its content.
func TestHeldLinesIgnoredAreLoggedAsCounts(t *testing.T) {
	bad := heldLinePrefix + base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"f":"/secret-path.go","s":1,"e":1,`+
		`"h":"SECRET-HEADER","c":"SECRET-CONTENT","from":"`+shaOld+`"}`)) + heldLineSuffix
	h := newHarness()
	h.prov.addComment(h.prov.me, "earlier\n\n"+bad+"\n"+reviewedLineOf(shaOld)+"\n"+OverviewMarker)
	if got := heldFound(t, h); len(got) != 0 {
		t.Errorf("read %+v", got)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "held lines ignored") || !strings.Contains(logs, "invalid=1") {
		t.Errorf("no count logged:\n%s", logs)
	}
	for _, s := range []string{"SECRET", "secret-path"} {
		if strings.Contains(logs, s) {
			t.Errorf("the log holds record content %q", s)
		}
	}
}

// TestMergedPublishHeldLines (X-33): a merged publish records its own
// overview-only findings, from the commit it carries over; the found
// overview's held lines are not carried (the client's set replaces its
// findings). Without a found commit it records none.
func TestMergedPublishHeldLines(t *testing.T) {
	earlier := heldLineOf(t, heldRecord{V: 1, F: "src/util.go", S: 30, E: 31, H: "Earlier", C: "From before.", From: shaOld})
	findings := []SuppliedFinding{supplied("src/util.go", 30, 31, "Supplied", "Off the diff.")}
	h := newSuppliedHarness(t)
	h.prov.pr.HeadSHA = shaHead
	h.prov.addComment(h.prov.me, "earlier\n\n"+earlier+"\n"+reviewedLineOf(shaOld)+"\n"+OverviewMarker)
	runOK(t, h, Args{PRURL: testPRURL, Publish: true, Findings: findings})
	want := []heldRecord{{V: 1, F: "src/util.go", S: 30, E: 31, H: "Supplied", C: "Off the diff.", From: shaOld}}
	if got := heldFound(t, h); !slices.Equal(got, want) {
		t.Errorf("read %+v, want %+v", got, want)
	}

	h = newSuppliedHarness(t)
	h.prov.pr.HeadSHA = shaHead
	runOK(t, h, Args{PRURL: testPRURL, Publish: true, Findings: findings})
	if strings.Contains(h.prov.posted[0], "review-mcp:held:") {
		t.Errorf("held lines without a carried-over commit:\n%s", h.prov.posted[0])
	}
}

// js is the JSON form of v.
func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
