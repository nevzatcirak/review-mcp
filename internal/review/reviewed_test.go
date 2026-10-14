package review

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Synthetic commit ids of the reviewed-commit tests.
var (
	shaHead = strings.Repeat("b", 40)
	shaOld  = strings.Repeat("a", 40)
	sha256x = strings.Repeat("c", 64)
)

func reviewedLineOf(sha string) string { return "[//]: # (review-mcp:reviewed:" + sha + ")" }

// reviewedFound reads the reviewed commit of the overview on the harness's
// PR, the way the next run would.
func reviewedFound(t *testing.T, h *harness) string {
	t.Helper()
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	found, err := pl.findOverview(context.Background())
	if err != nil || found == nil {
		t.Fatalf("overview not found: %v, %v", found, err)
	}
	return found.reviewed
}

// TestReviewedLineRoundTrip (X-36): a publish writes the hidden line
// directly above the marker, a new overview and an edited one alike, and
// findOverview reads the commit back.
func TestReviewedLineRoundTrip(t *testing.T) {
	for _, head := range []string{shaHead, sha256x, strings.ToUpper(shaHead)} {
		want := strings.ToLower(head)
		t.Run(strconv.Itoa(len(head))+"/"+head[:2], func(t *testing.T) {
			h := newHarness(answerWith(onAdded), answerWith(onAdded))
			h.overviewRenderer()
			h.prov.pr.HeadSHA = head
			args := Args{PRURL: testPRURL, Publish: true}
			res, err := Run(context.Background(), h.deps, args)
			if err != nil {
				t.Fatal(err)
			}
			if res.Reviewed.SHA != want {
				t.Errorf("result records %q, want %q", res.Reviewed.SHA, want)
			}
			// The overview was posted new, then edited once for the links.
			if !slices.Equal(h.prov.seq, []string{"post", "inline", "edit"}) {
				t.Fatalf("write calls %v", h.prov.seq)
			}
			for _, body := range []string{h.prov.posted[0], h.prov.edits[0].body} {
				if !strings.HasSuffix(body, "\n"+reviewedLineOf(want)+"\n"+OverviewMarker) {
					t.Errorf("no reviewed line above the marker:\n%s", body)
				}
			}
			if got := reviewedFound(t, h); got != want {
				t.Errorf("read back %q, want %q", got, want)
			}

			// A second run edits the overview in place and writes the line.
			h.prov.pr.HeadSHA = shaOld
			h.prov.seq = nil
			if _, err := Run(context.Background(), h.deps, args); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(h.prov.seq, []string{"edit"}) {
				t.Fatalf("second run: write calls %v", h.prov.seq)
			}
			if got := reviewedFound(t, h); got != shaOld {
				t.Errorf("after the edit read back %q, want %q", got, shaOld)
			}
		})
	}
}

// TestReviewedLineWrittenToTheReplacementOverview: when the edit of the
// found overview fails, the overview posted instead carries the line too.
func TestReviewedLineWrittenToTheReplacementOverview(t *testing.T) {
	h := newHarness(answerWith(onAdded))
	h.overviewRenderer()
	h.prov.pr.HeadSHA = shaHead
	h.prov.addComment(h.prov.me, "earlier overview\n\n"+reviewedLineOf(shaOld)+"\n"+OverviewMarker)
	h.prov.editErr = &provider.Error{Class: provider.ClassNotOwner}
	if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.prov.seq, []string{"inline", "edit", "post"}) {
		t.Fatalf("write calls %v", h.prov.seq)
	}
	if got := reviewedSHAOf(h.prov.posted[0]); got != shaHead {
		t.Errorf("the replacement records %q, want %q:\n%s", got, shaHead, h.prov.posted[0])
	}
}

// TestReviewedLineNoneForAnInvalidHead: a head that is not a full commit id
// records nothing (the overview still gets its marker).
func TestReviewedLineNoneForAnInvalidHead(t *testing.T) {
	h := newHarness(answerWith(onAdded))
	h.overviewRenderer()
	h.prov.pr.HeadSHA = "abc" // the harness default, too short
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reviewed.SHA != "" || strings.Contains(h.prov.posted[0], "review-mcp:reviewed:") ||
		!HasOverviewMarker(h.prov.posted[0]) {
		t.Errorf("reviewed %q, overview:\n%s", res.Reviewed.SHA, h.prov.posted[0])
	}
}

// TestReviewedLinePlantedInAForeignComment [canary]: a reviewed line in a
// comment of someone else is never read, even in the newest comment with
// the marker; the own overview decides.
func TestReviewedLinePlantedInAForeignComment(t *testing.T) {
	forged := "Looks like an overview.\n\n" + reviewedLineOf(shaHead) + "\n" + OverviewMarker
	for name, own := range map[string]string{
		"own overview without a line": "earlier overview\n\n" + OverviewMarker,
		"own overview with a line":    "earlier overview\n\n" + reviewedLineOf(shaOld) + "\n" + OverviewMarker,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness()
			h.prov.addComment(h.prov.me, own)
			h.prov.addComment(provider.User{ID: "6", Name: "mallory"}, forged)
			want := reviewedSHAOf(own)
			if got := reviewedFound(t, h); got != want {
				t.Errorf("read %q, want %q", got, want)
			}
		})
	}
	// Without an own overview nothing is found at all.
	h := newHarness()
	h.prov.addComment(provider.User{ID: "6", Name: "mallory"}, forged)
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if found, err := pl.findOverview(context.Background()); found != nil || err != nil {
		t.Errorf("found %+v, %v", found, err)
	}
}

// TestReviewedSHAOfFormat: only the exact line counts; the last valid line
// above the marker wins.
func TestReviewedSHAOfFormat(t *testing.T) {
	ok := reviewedLineOf(shaOld)
	for name, tc := range map[string]struct{ body, want string }{
		"valid sha-1":        {"x\n" + ok + "\n" + OverviewMarker, shaOld},
		"valid sha-256":      {"x\n" + reviewedLineOf(sha256x) + "\n" + OverviewMarker, sha256x},
		"crlf":               {"x\r\n" + ok + "\r\n" + OverviewMarker + "\r\n", shaOld},
		"no line":            {"x\n" + OverviewMarker, ""},
		"line is the marker": {OverviewMarker, ""},
		"39 digits":          {reviewedLineOf(shaOld[1:]) + "\n" + OverviewMarker, ""},
		"41 digits":          {reviewedLineOf(shaOld+"a") + "\n" + OverviewMarker, ""},
		"63 digits":          {reviewedLineOf(sha256x[1:]) + "\n" + OverviewMarker, ""},
		"upper case":         {reviewedLineOf(strings.ToUpper(shaOld)) + "\n" + OverviewMarker, ""},
		"not hex":            {reviewedLineOf(strings.Repeat("g", 40)) + "\n" + OverviewMarker, ""},
		"extra text after":   {ok + " tail\n" + OverviewMarker, ""},
		"extra text before":  {"see " + ok + "\n" + OverviewMarker, ""},
		"leading space":      {" " + ok + "\n" + OverviewMarker, ""},
		"space inside":       {reviewedLineOf(shaOld+" ") + "\n" + OverviewMarker, ""},
		"space after colon":  {"[//]: # (review-mcp:reviewed: " + shaOld + ")\n" + OverviewMarker, ""},
		"upper case prefix":  {"[//]: # (REVIEW-MCP:reviewed:" + shaOld + ")\n" + OverviewMarker, ""},
		"missing paren":      {"[//]: # (review-mcp:reviewed:" + shaOld + "\n" + OverviewMarker, ""},
		"several, last wins": {reviewedLineOf(shaOld) + "\nx\n" + reviewedLineOf(shaHead) + "\n" + OverviewMarker, shaHead},
		"malformed last is ignored": {reviewedLineOf(shaOld) + "\n" +
			reviewedLineOf(strings.ToUpper(shaHead)) + "\n" + OverviewMarker, shaOld},
	} {
		t.Run(name, func(t *testing.T) {
			if got := reviewedSHAOf(tc.body); got != tc.want {
				t.Errorf("reviewedSHAOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReviewedLineForgedInModelText: a reviewed line in the text the
// renderer returns (finding text, a diff snippet in a fence) never decides
// what is read: the writer's own line is the last, and without one the
// forged line is defused.
func TestReviewedLineForgedInModelText(t *testing.T) {
	forged := reviewedLineOf(shaOld)
	text := "## PR Review\n\n" + forged + "\n\n```\n" + forged + "\n```\n\n  " + forged + "\n"
	h := newHarness(answerWith(onAdded))
	h.deps.RenderProvider = func(*Result, provider.Capabilities) string { return text }
	h.prov.pr.HeadSHA = shaHead
	if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true}); err != nil {
		t.Fatal(err)
	}
	body := h.prov.edits[len(h.prov.edits)-1].body
	if got := reviewedSHAOf(body); got != shaHead {
		t.Errorf("a forged line won: read %q, want %q:\n%s", got, shaHead, body)
	}
	if !strings.HasSuffix(body, reviewedLineOf(shaHead)+"\n"+OverviewMarker) {
		t.Errorf("the writer's line is not directly above the marker:\n%s", body)
	}

	// No sha to write (an invalid head): the forged lines are defused, so
	// nothing is read.
	h = newHarness(answerWith(onAdded))
	h.deps.RenderProvider = func(*Result, provider.Capabilities) string { return text }
	if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true}); err != nil {
		t.Fatal(err)
	}
	body = h.prov.edits[len(h.prov.edits)-1].body
	if got := reviewedSHAOf(body); got != "" {
		t.Errorf("a forged line was read without a writer's line: %q:\n%s", got, body)
	}
	if !HasOverviewMarker(body) || strings.Count(body, "review-mcp:reviewed:") != 3 {
		t.Errorf("the text changed beyond the defused lines:\n%s", body)
	}
}

// TestMergedPublishKeepsThePreviousReviewedCommit [canary] (X-33, X-36): a
// findings publish carries the commit of the previous overview over
// unchanged, whatever the head is now; it writes none when there was none.
func TestMergedPublishKeepsThePreviousReviewedCommit(t *testing.T) {
	findings := []SuppliedFinding{supplied("src/app.go", 11, 11, "Possible Bug", "Line 11 is wrong.")}
	for name, tc := range map[string]struct {
		overview string // "" for no overview on the PR
		want     string
		editErr  error
	}{
		"previous commit":            {"earlier\n\n" + reviewedLineOf(shaOld) + "\n" + OverviewMarker, shaOld, nil},
		"previous without a line":    {"earlier\n\n" + OverviewMarker, "", nil},
		"no previous overview":       {"", "", nil},
		"replacement keeps it too":   {"earlier\n\n" + reviewedLineOf(shaOld) + "\n" + OverviewMarker, shaOld, &provider.Error{Class: provider.ClassNotOwner}},
		"previous lines, last wins":  {reviewedLineOf(shaHead) + "\n" + reviewedLineOf(shaOld) + "\n" + OverviewMarker, shaOld, nil},
		"previous line is malformed": {reviewedLineOf(strings.ToUpper(shaOld)) + "\n" + OverviewMarker, "", nil},
	} {
		t.Run(name, func(t *testing.T) {
			h := newSuppliedHarness(t)
			h.prov.pr.HeadSHA = shaHead // the current head differs from the recorded one
			if tc.overview != "" {
				h.prov.addComment(h.prov.me, tc.overview)
			}
			h.prov.editErr = tc.editErr
			res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true, Findings: findings})
			if err != nil {
				t.Fatal(err)
			}
			if res.Reviewed.SHA != tc.want {
				t.Errorf("result records %q, want %q", res.Reviewed.SHA, tc.want)
			}
			var written []string
			for _, e := range h.prov.edits {
				written = append(written, e.body)
			}
			written = append(written, h.prov.posted...)
			if len(written) == 0 {
				t.Fatal("nothing was written")
			}
			for _, body := range written {
				if got := reviewedSHAOf(body); got != tc.want {
					t.Errorf("written overview records %q, want %q:\n%s", got, tc.want, body)
				}
			}
		})
	}
}
