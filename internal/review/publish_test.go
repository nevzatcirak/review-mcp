package review

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

type testFinding struct {
	file, header, content string
	start, end            int
}

func answerWith(fs ...testFinding) string {
	var b strings.Builder
	b.WriteString("```yaml\nreview:\n  key_issues_to_review:\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "    - relevant_file: %s\n      issue_header: %s\n      issue_content: %s\n"+
			"      start_line: %d\n      end_line: %d\n", f.file, f.header, f.content, f.start, f.end)
	}
	b.WriteString("```\n")
	return b.String()
}

// Findings on sampleFiles: src/app.go's server hunk is lines 10 to 13 (11
// added); its full contents are known, so the prompt extends the hunk to
// lines 5 to 14. src/util.go's hunk is lines 1 to 3 (2 added).
var (
	onAdded    = testFinding{"src/app.go", "Off by one", "The loop is off by one " + answerMarker + ".", 11, 11}
	onContext  = testFinding{"src/app.go", "Context finding", "The unchanged line is now wrong.", 10, 12}
	onExtended = testFinding{"src/app.go", "Extended", "Only the extended context shows this line.", 8, 8}
	outside    = testFinding{"src/util.go", "Outside", "Far from the change.", 30, 31}
)

// overviewRenderer renders the parts of a result the publish tests check:
// each finding's link and the notes.
func (h *harness) overviewRenderer() {
	h.deps.RenderProvider = func(r *Result, _ provider.Capabilities) string {
		h.rendered = append(h.rendered, r)
		var b strings.Builder
		for _, ki := range r.Review.KeyIssuesToReview {
			link := ki.Link
			if ki.InlineURL != "" {
				link = ki.InlineURL
			}
			b.WriteString(ki.IssueHeader + " -> " + link + "\n")
		}
		for _, n := range r.Notes {
			b.WriteString("note: " + n + "\n")
		}
		return b.String()
	}
}

func TestPublishInlineComments(t *testing.T) {
	h := newHarness(answerWith(onAdded, onContext, onExtended, outside))
	h.overviewRenderer()
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true, MaxFindings: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.prov.seq, []string{"post", "inline", "edit"}) {
		t.Fatalf("write calls = %v, want the overview first, then the inline comments, then the overview edit", h.prov.seq)
	}
	if len(h.prov.inline) != 1 || len(h.prov.inline[0]) != 2 {
		t.Fatalf("inline items = %+v", h.prov.inline)
	}
	kis := res.Review.KeyIssuesToReview
	want := []provider.InlineComment{
		{Path: "src/app.go", Line: 11, LineType: provider.LineAdded,
			Body: "inline Off by one gfm=true\n\n" + FingerprintMarker(Fingerprint("src/app.go", "Off by one", kis[0].IssueContent))},
		{Path: "src/app.go", Line: 10, LineType: provider.LineContext,
			Body: "inline Context finding gfm=true\n\n" + FingerprintMarker(Fingerprint("src/app.go", "Context finding", kis[1].IssueContent))},
	}
	if !slices.Equal(h.prov.inline[0], want) {
		t.Errorf("inline items:\n got %+v\nwant %+v", h.prov.inline[0], want)
	}
	if in := res.Publish.Inline; in == nil || *in != (InlineSummary{Posted: 2, Unanchorable: 2}) {
		t.Errorf("inline = %+v", res.Publish.Inline)
	}
	if kis[0].InlineURL == "" || kis[1].InlineURL == "" || kis[2].InlineURL != "" || kis[3].InlineURL != "" {
		t.Errorf("inline URLs = %q %q %q %q", kis[0].InlineURL, kis[1].InlineURL, kis[2].InlineURL, kis[3].InlineURL)
	}
	// The extended-context line is visible to the model and its snippet is
	// verified from the head file, but it is not on the server's hunk.
	if kis[2].Snippet != "line 8" {
		t.Errorf("extended-context snippet = %q", kis[2].Snippet)
	}

	note := "2 findings could not be placed on a changed line and are listed in the overview only."
	if !slices.Equal(res.Notes, []string{note}) {
		t.Errorf("notes = %q", res.Notes)
	}
	// The first overview already carries the note; the edit links the
	// inline comments.
	posted, edited := h.prov.posted[0], h.prov.edits[0]
	if !strings.Contains(posted, "note: "+note) || strings.Contains(posted, kis[0].InlineURL) {
		t.Errorf("posted overview:\n%s", posted)
	}
	if edited.id != "42" || !strings.Contains(edited.body, "Off by one -> "+kis[0].InlineURL) ||
		!strings.Contains(edited.body, "Context finding -> "+kis[1].InlineURL) ||
		!strings.Contains(edited.body, "Extended -> "+kis[2].Link) || !strings.Contains(edited.body, "note: "+note) {
		t.Errorf("edited overview (%s):\n%s", edited.id, edited.body)
	}
	h.checkNoLeaks(t)
}

func TestPublishInlineRenamedFileCarriesOldPath(t *testing.T) {
	h := newHarness(answerWith(testFinding{"src/renamed.go", "Renamed", "Check the new name.", 2, 2}))
	h.prov.files = append(h.prov.files, provider.FilePatch{
		Path: "src/renamed.go", OldPath: "src/old_name.go", Type: provider.ChangeRenamed,
		Patch: "@@ -1,2 +1,2 @@\n a\n-b\n+c\n", HeadStatus: provider.ContentNotFetchedSizeCap,
	})
	if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true}); err != nil {
		t.Fatal(err)
	}
	if len(h.prov.inline) != 1 || len(h.prov.inline[0]) != 1 {
		t.Fatalf("inline items = %+v", h.prov.inline)
	}
	if it := h.prov.inline[0][0]; it.Path != "src/renamed.go" || it.OldPath != "src/old_name.go" || it.Line != 2 ||
		it.LineType != provider.LineAdded {
		t.Errorf("item = %+v", it)
	}
}

// TestPublishInlineFailuresKeepTheReview: no inline failure fails the review
// or removes the overview.
func TestPublishInlineFailuresKeepTheReview(t *testing.T) {
	failedNote := "1 finding could not be posted as an inline comment and is listed in the overview only."
	cases := []struct {
		name      string
		mutate    func(*fakeProvider)
		wantSeq   []string
		wantSum   *InlineSummary
		wantNotes []string
	}{
		{
			name: "the whole call fails",
			mutate: func(f *fakeProvider) {
				f.inlineErr = &provider.Error{Class: provider.ClassUpstream, Status: 502}
			},
			wantSeq:   []string{"post", "inline", "edit"},
			wantSum:   &InlineSummary{Failed: 2},
			wantNotes: []string{"2 findings could not be posted as inline comments and are listed in the overview only."},
		},
		{
			name: "one item fails",
			mutate: func(f *fakeProvider) {
				f.inlineResult = func(i int, _ provider.InlineComment) provider.InlineResult {
					if i == 1 {
						return provider.InlineResult{Error: "the server rejected the request"}
					}
					return provider.InlineResult{Posted: true, ID: "7", URL: "https://your-gitea.example/c/7"}
				}
			},
			wantSeq:   []string{"post", "inline", "edit"},
			wantSum:   &InlineSummary{Posted: 1, Failed: 1},
			wantNotes: []string{failedNote},
		},
		{
			name:      "the overview edit fails",
			mutate:    func(f *fakeProvider) { f.editErr = &provider.Error{Class: provider.ClassNotOwner} },
			wantSeq:   []string{"post", "inline", "edit"},
			wantSum:   &InlineSummary{Posted: 2},
			wantNotes: []string{NoteOverviewNotUpdated},
		},
		{
			name:    "the overview fails",
			mutate:  func(f *fakeProvider) { f.postErr = &provider.Error{Class: provider.ClassAuth, Status: 403} },
			wantSeq: []string{"post"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(answerWith(onAdded, onContext))
			c.mutate(h.prov)
			res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
			if err != nil {
				t.Fatalf("an inline failure failed the review: %v", err)
			}
			if len(res.Review.KeyIssuesToReview) != 2 {
				t.Fatalf("review = %+v", res.Review)
			}
			if !slices.Equal(h.prov.seq, c.wantSeq) {
				t.Errorf("write calls = %v, want %v", h.prov.seq, c.wantSeq)
			}
			if (res.Publish.Inline == nil) != (c.wantSum == nil) ||
				(c.wantSum != nil && *res.Publish.Inline != *c.wantSum) {
				t.Errorf("inline = %+v, want %+v", res.Publish.Inline, c.wantSum)
			}
			if !slices.Equal(res.Notes, c.wantNotes) {
				t.Errorf("notes = %q, want %q", res.Notes, c.wantNotes)
			}
			if c.wantSum == nil && res.Publish.Published {
				t.Errorf("publish = %+v", res.Publish)
			}
			h.checkNoLeaks(t)
		})
	}
}

// TestPublishOverviewFailureDropsInlineNotes: without an overview nothing
// is posted inline, and the notes that describe the overview are dropped.
func TestPublishOverviewFailureDropsInlineNotes(t *testing.T) {
	h := newHarness(answerWith(onAdded, outside))
	h.prov.postErr = errors.New("dial tcp: refused")
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.prov.inline) != 0 || res.Publish.Inline != nil || len(res.Notes) != 0 || res.Publish.Published {
		t.Errorf("inline calls %d, publish %+v, notes %q", len(h.prov.inline), res.Publish, res.Notes)
	}
}

func TestPublishInlineOff(t *testing.T) {
	off := false
	for name, mutate := range map[string]func(*harness, *Args){
		"inline_findings false": func(_ *harness, a *Args) { a.InlineFindings = &off },
		"no inline renderer":    func(h *harness, _ *Args) { h.deps.RenderInline = nil },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(answerWith(onAdded, outside))
			args := Args{PRURL: testPRURL, Publish: true}
			mutate(h, &args)
			res, err := Run(context.Background(), h.deps, args)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(h.prov.seq, []string{"post"}) || res.Publish.Inline != nil || len(res.Notes) != 0 ||
				!res.Publish.Published {
				t.Errorf("write calls %v, publish %+v, notes %q", h.prov.seq, res.Publish, res.Notes)
			}
		})
	}
	on := true
	h := newHarness(answerWith(onAdded))
	if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true, InlineFindings: &on}); err != nil {
		t.Fatal(err)
	}
	if len(h.prov.inline) != 1 {
		t.Errorf("inline_findings true posted %d batches", len(h.prov.inline))
	}
}

// TestPublishSkipsPostedFingerprints is the WP-PR-7e seam: a finding whose
// fingerprint is already on the PR is not posted again and is counted.
func TestPublishSkipsPostedFingerprints(t *testing.T) {
	h := newHarness(answerWith(onAdded, onContext))
	h.overviewRenderer()
	args := Args{PRURL: testPRURL, Publish: true}
	pl, err := Prepare(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	pl.postedFingerprints = map[string]bool{
		Fingerprint("src/app.go", "  OFF BY ONE ", "the loop is off by one "+answerMarker+"."): true,
	}
	res, err := pl.finish(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	if in := res.Publish.Inline; in == nil || *in != (InlineSummary{Posted: 1, SkippedDuplicate: 1}) {
		t.Errorf("inline = %+v", res.Publish.Inline)
	}
	if len(h.prov.inline) != 1 || len(h.prov.inline[0]) != 1 || h.prov.inline[0][0].Line != 10 {
		t.Errorf("inline items = %+v", h.prov.inline)
	}
	note := "1 finding was already posted on this PR and was not repeated."
	if !slices.Equal(res.Notes, []string{note}) || !strings.Contains(h.prov.posted[0], "note: "+note) {
		t.Errorf("notes = %q, overview %q", res.Notes, h.prov.posted)
	}
}

func TestPublishEmptyReviewHasNoInlineComments(t *testing.T) {
	h := newHarness(goodAnswer)
	h.deps.Config.Ignore.Glob = []string{"**"}
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.prov.seq, []string{"post"}) || res.Publish.Inline == nil || *res.Publish.Inline != (InlineSummary{}) {
		t.Errorf("write calls %v, inline %+v", h.prov.seq, res.Publish.Inline)
	}
}

// TestPrepareKeepsTheProviderHunks: preparing the prompt (context
// extension, numbering, the request-size guard) never rewrites the patches
// that anchors are resolved on.
func TestPrepareKeepsTheProviderHunks(t *testing.T) {
	h := newHarness(goodAnswer)
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pl.Prompts.User, "line 8") {
		t.Fatalf("the prompt has no extended context; the test proves nothing")
	}
	orig := sampleFiles()
	for i, f := range pl.d.Files {
		if f.Patch != orig[i].Patch {
			t.Errorf("%s: patch changed to %q", f.Path, f.Patch)
		}
	}
}

// TestPublishSanitizesSlashesByCapability [canary]: with QuickActions the
// overview (posted and edited) and every inline comment have no line that
// starts with "/"; without it all three are published as rendered.
func TestPublishSanitizesSlashesByCapability(t *testing.T) {
	startsWithSlash := func(body string) bool {
		return strings.HasPrefix(body, "/") || strings.Contains(body, "\n/") || strings.Contains(body, "\r/")
	}
	for _, qa := range []bool{false, true} {
		h := newHarness(answerWith(onAdded, onContext))
		h.prov.quickActions = qa
		h.deps.RenderProvider = func(*Result, provider.Capabilities) string { return "/close\noverview" }
		h.deps.RenderInline = func(*KeyIssue, provider.Capabilities) string { return "/assign me\ninline" }
		if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true}); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(h.prov.seq, []string{"post", "inline", "edit"}) || len(h.prov.inline) != 1 || len(h.prov.inline[0]) != 2 {
			t.Fatalf("QuickActions=%v: write calls = %v, inline = %+v", qa, h.prov.seq, h.prov.inline)
		}
		bodies := map[string]string{"overview": h.prov.posted[0], "overview edit": h.prov.edits[0].body}
		for i, it := range h.prov.inline[0] {
			bodies[fmt.Sprintf("inline %d", i)] = it.Body
		}
		for name, body := range bodies {
			if got := startsWithSlash(body); got != !qa {
				t.Errorf("QuickActions=%v: %s body has a line starting with /: %v\n%q", qa, name, got, body)
			}
		}
		if !qa && !strings.HasPrefix(h.prov.posted[0], "/close\n") {
			t.Errorf("without QuickActions the overview changed: %q", h.prov.posted[0])
		}
	}
}
