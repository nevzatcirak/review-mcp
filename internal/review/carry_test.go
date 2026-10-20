package review

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Carrying overview-only findings forward (spec v2.1 §7.3, WP-7f) on the
// fake provider, over three runs on the two-file pull request of the
// incremental tests: file B (incPathSame) and file A (incPathEdited).
const shaThird = "dddddddddddddddddddddddddddddddddddddddd"

// incSameThird is file B's content at shaThird: changed since shaHead.
const incSameThird = "package x\n\nfunc Third() {} // " + incMarkSame + "\n"

// carryHarness serves the pull request at shaOld, where A has the content
// incEditedLast and B its final one, with the model answers in order.
func carryHarness(answers ...string) *harness {
	h := newHarness(answers...)
	h.prov.pr.HeadSHA = shaOld
	h.prov.files = []provider.FilePatch{
		incFile(incPathSame, incSameBase, incSameHead),
		incFile(incPathEdited, incEditedBase, incEditedLast),
	}
	base := map[string]string{"src/other.go": "package x\n", "pkg/other.go": "package x\n"}
	rev := func(same, edited string) map[string]string {
		m := map[string]string{incPathSame: same, incPathEdited: edited}
		for k, v := range base {
			m[k] = v
		}
		return m
	}
	h.prov.revs = map[string]map[string]string{
		"base0":  rev(incSameBase, incEditedBase),
		shaOld:   rev(incSameHead, incEditedLast),
		shaHead:  rev(incSameHead, incEditedHead),
		shaThird: rev(incSameThird, incEditedHead),
	}
	return h
}

// moveHead moves the harness's pull request to head with the given contents
// of B and A.
func (h *harness) moveHead(head, same, edited string) {
	h.prov.pr.HeadSHA = head
	h.prov.files = []provider.FilePatch{
		incFile(incPathSame, incSameBase, same),
		incFile(incPathEdited, incEditedBase, edited),
	}
	h.prov.inline, h.prov.seq = nil, nil
}

// TestCarryForward: a full review whose inline comment for file B's finding
// fails records B's finding in a held line; a second, incremental review
// after a change to A only carries B's finding forward (after the new
// finding, with carried_from, the note and the count, never inline, and in
// a held line again with its original commit); a third after a change to B
// drops the record, and B is reviewed again.
func TestCarryForward(t *testing.T) {
	again := testFinding{incPathEdited, "Edited again", "Still on the changed file.", 3, 3}
	h := carryHarness(answerWith(onSame, onEdited), answerWith(again), answerWith())

	// Run 1: full, at shaOld; B's inline comment fails.
	h.prov.inlineResult = func(_ int, it provider.InlineComment) provider.InlineResult {
		if it.Path == incPathSame {
			return provider.InlineResult{}
		}
		return provider.InlineResult{Posted: true, ID: "901", URL: "https://your-gitea.example/c/901"}
	}
	full := false
	res := runOK(t, h, Args{PRURL: testPRURL, Publish: true, Incremental: &full})
	if *res.Publish.Inline != (InlineSummary{Posted: 1, Failed: 1}) {
		t.Fatalf("run 1: inline = %+v", res.Publish.Inline)
	}
	recB := heldRecord{V: 1, F: incPathSame, S: 3, E: 3, H: onSame.header, C: onSame.content, From: shaOld}
	if got := heldFound(t, h); !slices.Equal(got, []heldRecord{recB}) {
		t.Fatalf("run 1: held %+v, want B's finding only", got)
	}
	h.prov.inlineResult = nil

	// Run 2: incremental, only A changed since shaOld.
	h.moveHead(shaHead, incSameHead, incEditedHead)
	res = runOK(t, h, incArgs(true))
	if res.Incremental == nil || res.Incremental.Mode != IncrementalChanged || res.Incremental.CarriedFindings != 1 {
		t.Fatalf("run 2: incremental = %+v", res.Incremental)
	}
	issues := res.Review.KeyIssuesToReview
	if len(issues) != 2 || issues[0].IssueHeader != again.header || issues[0].CarriedFrom != "" {
		t.Fatalf("run 2: findings %+v, want the new one first", issues)
	}
	carried := issues[1]
	want := KeyIssue{RelevantFile: incPathSame, StartLine: 3, EndLine: 3, IssueHeader: onSame.header,
		IssueContent: onSame.content, Link: h.prov.FileLineURL(provider.PRRef{}, nil, incPathSame, 3), CarriedFrom: shaOld}
	if !reflect.DeepEqual(carried, want) {
		t.Errorf("run 2: carried\n%+v\nwant\n%+v", carried, want)
	}
	note := "1 finding of earlier reviews on unchanged files is carried forward; it was not reviewed again."
	if !slices.Contains(res.Notes, note) {
		t.Errorf("run 2: notes %q lack %q", res.Notes, note)
	}
	if n := noteCarried(2); n != "2 findings of earlier reviews on unchanged files are carried forward; they were not reviewed again." {
		t.Errorf("plural note %q", n)
	}
	for _, call := range h.llm.calls {
		if strings.Contains(call.user, onSame.content) {
			t.Errorf("the model saw the carried finding")
		}
	}
	// Never inline: the one inline post is the new finding on A, and the
	// carried finding is in no inline count.
	for _, items := range h.prov.inline {
		for _, it := range items {
			if it.Path != incPathEdited {
				t.Errorf("run 2: inline comment on %s", it.Path)
			}
		}
	}
	if *res.Publish.Inline != (InlineSummary{Posted: 1}) || carried.InlineStatus != "" {
		t.Errorf("run 2: inline = %+v, carried status %q", res.Publish.Inline, carried.InlineStatus)
	}
	if got := heldFound(t, h); !slices.Equal(got, []heldRecord{recB}) {
		t.Errorf("run 2: held %+v, want B's record with its original commit", got)
	}
	if got := reviewedSHAOf(lastWritten(h)); got != shaHead {
		t.Errorf("run 2: the overview records %q", got)
	}

	// Run 3: incremental, only B changed since shaHead: its record is
	// dropped and B is reviewed again.
	h.moveHead(shaThird, incSameThird, incEditedHead)
	res = runOK(t, h, incArgs(true))
	if res.Incremental == nil || res.Incremental.Mode != IncrementalChanged || res.Incremental.CarriedFindings != 0 {
		t.Fatalf("run 3: incremental = %+v", res.Incremental)
	}
	for _, ki := range res.Review.KeyIssuesToReview {
		if ki.CarriedFrom != "" {
			t.Errorf("run 3: carried %+v from a changed file", ki)
		}
	}
	if last := h.llm.calls[len(h.llm.calls)-1]; !strings.Contains(last.user, incMarkSame) {
		t.Errorf("run 3: the model did not see B")
	}
	if got := heldFound(t, h); len(got) != 0 {
		t.Errorf("run 3: held %+v, want none", got)
	}
}

// TestCarryForwardOnlyIncremental: a full run carries nothing, although the
// overview it edits holds a record of an unchanged file; a record of a
// deleted file or of a file no longer in the pull request is dropped.
func TestCarryForwardOnlyIncremental(t *testing.T) {
	gone := heldRecord{V: 1, F: "src/gone.go", S: 1, E: 1, H: "Gone", C: "No longer here.", From: shaOld}
	recB := heldRecord{V: 1, F: incPathSame, S: 3, E: 3, H: "B", C: "On B.", From: shaOld}
	overview := "earlier\n\n" + heldLineOf(t, recB) + "\n" + heldLineOf(t, gone) + "\n" + reviewedLineOf(shaOld) + "\n" + OverviewMarker

	h := carryHarness(answerWith())
	h.moveHead(shaHead, incSameHead, incEditedHead)
	h.prov.addComment(h.prov.me, overview)
	full := false
	res := runOK(t, h, Args{PRURL: testPRURL, Incremental: &full})
	if len(res.Review.KeyIssuesToReview) != 0 || res.Incremental.CarriedFindings != 0 {
		t.Errorf("full run carried %+v", res.Review.KeyIssuesToReview)
	}

	h = carryHarness(answerWith())
	h.moveHead(shaHead, incSameHead, incEditedHead)
	h.prov.addComment(h.prov.me, overview)
	res = runOK(t, h, incArgs(false))
	if len(res.Review.KeyIssuesToReview) != 1 || res.Review.KeyIssuesToReview[0].IssueHeader != "B" {
		t.Errorf("incremental run carried %+v, want B's record only", res.Review.KeyIssuesToReview)
	}

	// B deleted, and absent at the last review too: unchanged, but dropped.
	h = carryHarness(answerWith())
	h.moveHead(shaHead, incSameHead, incEditedHead)
	h.prov.files[0] = provider.FilePatch{Path: incPathSame, Type: provider.ChangeDeleted, Deletions: 3,
		Patch: "@@ -1,3 +0,0 @@\n-package x\n-\n-func Old() {}\n"}
	delete(h.prov.revs[shaOld], incPathSame)
	delete(h.prov.revs[shaHead], incPathSame)
	h.prov.addComment(h.prov.me, overview)
	res = runOK(t, h, incArgs(false))
	if res.Incremental.UnchangedFiles != 1 || len(res.Review.KeyIssuesToReview) != 0 {
		t.Errorf("deleted file: incremental %+v, findings %+v", res.Incremental, res.Review.KeyIssuesToReview)
	}
}

// TestCarryForwardNotFocused: a focused incremental run carries nothing,
// although the overview holds a record of an unchanged file (WP-7f DQ-4,
// overridden).
func TestCarryForwardNotFocused(t *testing.T) {
	recB := heldRecord{V: 1, F: incPathSame, S: 3, E: 3, H: "B", C: "On B.", From: shaOld}
	h := carryHarness(answerWith())
	h.moveHead(shaHead, incSameHead, incEditedHead)
	h.prov.addComment(h.prov.me, "earlier\n\n"+heldLineOf(t, recB)+"\n"+reviewedLineOf(shaOld)+"\n"+OverviewMarker)
	args := incArgs(false)
	args.Focus = "security"
	res := runOK(t, h, args)
	if res.Incremental == nil || res.Incremental.Mode != IncrementalChanged || res.Incremental.CarriedFindings != 0 {
		t.Fatalf("incremental = %+v", res.Incremental)
	}
	if len(res.Review.KeyIssuesToReview) != 0 {
		t.Errorf("focused run carried %+v", res.Review.KeyIssuesToReview)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "carried forward") {
			t.Errorf("focused run notes %q", n)
		}
	}
}

// TestInlineFailureClass (WP-8a): publish.inline.failure_class is the class
// text of the first failed inline item; empty when none failed or when the
// first failed without a provider class. It is a fixed class string: the
// provider's message never reaches it.
func TestInlineFailureClass(t *testing.T) {
	const secret = "token abc123 in a provider message"
	fail := func(class string) func(int, provider.InlineComment) provider.InlineResult {
		return func(_ int, it provider.InlineComment) provider.InlineResult {
			if it.Path == incPathSame {
				return provider.InlineResult{Error: secret, Class: class, Reason: provider.InlineReasonFailed}
			}
			return provider.InlineResult{Posted: true, ID: "901", URL: "https://your-gitea.example/c/901"}
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*fakeProvider)
		want   string
		failed int
	}{
		{"no failure", func(*fakeProvider) {}, "", 0},
		{"one item failed", func(f *fakeProvider) { f.inlineResult = fail("rate_limited 403") }, "rate_limited 403", 1},
		{"failed without a class", func(f *fakeProvider) { f.inlineResult = fail("") }, "", 1},
		{"the whole call failed", func(f *fakeProvider) {
			f.inlineErr = &provider.Error{Class: provider.ClassUpstream, Status: 502, Hint: secret}
		}, "upstream 502", 2},
		{"an unclassified error", func(f *fakeProvider) { f.inlineErr = errors.New(secret) }, "unknown", 2},
	} {
		h := carryHarness(answerWith(onSame, onEdited))
		tc.mutate(h.prov)
		full := false
		res := runOK(t, h, Args{PRURL: testPRURL, Publish: true, Incremental: &full})
		in := res.Publish.Inline
		if in == nil || in.Failed != tc.failed || in.FailureClass != tc.want {
			t.Errorf("%s: inline = %+v, want %d failed, class %q", tc.name, in, tc.failed, tc.want)
		}
		if b, _ := json.Marshal(res.Publish); strings.Contains(string(b), secret) {
			t.Errorf("%s: the provider's message is in the result", tc.name)
		}
	}
}
