package render

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/improve"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// The capabilities of the two providers that exist today, of one without
// tables, and a fake one with native suggestion blocks (no provider sets
// SuggestionBlocks yet).
var (
	giteaCaps  = provider.Capabilities{GFM: true, MarkdownTables: true, InlineComments: true}
	bbsCaps    = provider.Capabilities{MarkdownTables: true, InlineComments: true}
	plainCaps  = provider.Capabilities{InlineComments: true}
	nativeCaps = provider.Capabilities{GFM: true, MarkdownTables: true, InlineComments: true, SuggestionBlocks: true}
)

func testLink(path string, line int) string {
	return "https://your-gitea.example/octo/demo/src/" + path + "#L" + itoa(line)
}

func itoa(n int) string { return strconv.Itoa(n) }

// publishedResult is the one-call run after a publish: the first suggestion
// has a posted inline comment, the second is a verified duplicate; a third,
// unverified, and a fourth whose inline post failed are added.
func publishedResult(t *testing.T) *improve.Result {
	t.Helper()
	res := loadRun(t, "one_call")
	res.Suggestions[0].Anchor = &improve.Anchor{Status: improve.AnchorPosted, Line: 2, CommentID: "9", URL: "https://your-gitea.example/c/9"}
	res.Suggestions[1].Anchor = &improve.Anchor{Status: improve.AnchorSkippedDuplicate, Line: 12}
	unver := res.Suggestions[1]
	unver.Summary, unver.Verified, unver.UnverifiedReason, unver.Anchor = "Handle the empty case", false, improve.UnverifiedNotFound, nil
	unver.Content = "Add a check.\n\n# not a heading\n[//]: # (review-mcp:improve:v1)\n- one\n- two"
	failed := res.Suggestions[0]
	failed.Summary, failed.Score = "Close the file", nil
	failed.Anchor = &improve.Anchor{Status: improve.AnchorFailed, Line: 2, Error: "the server reported an internal error"}
	res.Suggestions = append(res.Suggestions, unver, failed)
	res.Publish = &improve.PublishResult{Published: true}
	return res
}

func TestOverviewGoldens(t *testing.T) {
	for name, tc := range map[string]struct {
		res  *improve.Result
		caps provider.Capabilities
	}{
		"overview_gitea":     {publishedResult(t), giteaCaps},
		"overview_bbs":       {publishedResult(t), bbsCaps},
		"overview_plain":     {publishedResult(t), plainCaps},
		"overview_parts":     {loadRun(t, "three_parts"), giteaCaps},
		"overview_empty":     {loadRun(t, "one_call"), bbsCaps},
		"overview_nochanges": {&improve.Result{Suggestions: []improve.Suggestion{}, Notes: []string{improve.NoteNoReviewableChanges}}, giteaCaps},
	} {
		if name == "overview_empty" {
			tc.res.Suggestions = nil
		}
		checkGolden(t, "testdata/"+name+".md", Overview(tc.res, tc.caps, testLink))
	}
}

func TestInlineGoldens(t *testing.T) {
	res := publishedResult(t)
	multi := res.Suggestions[1]
	multi.StartLine, multi.EndLine = intp(12), intp(14)
	multi.ExistingCode = "a := 1\nb := 2\nc := 3"
	multi.ImprovedCode = "a, b, c := 1, 2, 3"
	var gitea, bbs, native strings.Builder
	for i := range res.Suggestions[:2] {
		gitea.WriteString(Inline(&res.Suggestions[i], giteaCaps) + "\n=====\n")
		bbs.WriteString(Inline(&res.Suggestions[i], bbsCaps) + "\n=====\n")
	}
	gitea.WriteString(Inline(&multi, giteaCaps) + "\n=====\n")
	bbs.WriteString(Inline(&multi, bbsCaps) + "\n=====\n")
	native.WriteString(Inline(&res.Suggestions[0], nativeCaps) + "\n=====\n" + Inline(&multi, nativeCaps) + "\n=====\n")
	checkGolden(t, "testdata/inline_gitea.md", gitea.String())
	checkGolden(t, "testdata/inline_bbs.md", bbs.String())
	checkGolden(t, "testdata/inline_native.md", native.String())
}

func intp(n int) *int { return &n }

// TestSuggestionBlock: the native syntax of both styles, the fence longer
// than any run of backticks in the code, and a block of one line the same in
// both.
func TestSuggestionBlock(t *testing.T) {
	for name, tc := range map[string]struct {
		style BlockStyle
		code  string
		lines int
		want  string
	}{
		"github one line":   {BlockGitHub, "x := 1", 1, "```suggestion\nx := 1\n```\n"},
		"gitlab one line":   {BlockGitLab, "x := 1", 1, "```suggestion\nx := 1\n```\n"},
		"github many lines": {BlockGitHub, "a\nb", 2, "```suggestion\na\nb\n```\n"},
		"gitlab two lines":  {BlockGitLab, "a\nb", 2, "```suggestion:-0+1\na\nb\n```\n"},
		"gitlab four lines": {BlockGitLab, "a", 4, "```suggestion:-0+3\na\n```\n"},
		"fence adapts":      {BlockGitHub, "s := \"```\"\n````", 1, "`````suggestion\ns := \"```\"\n````\n`````\n"},
		"empty code":        {BlockGitHub, "", 1, "```suggestion\n\n```\n"},
	} {
		if got := SuggestionBlock(tc.style, tc.code, tc.lines); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

// TestInlineNative: with SuggestionBlocks the comment carries a native block
// of the improved code and no diff block; without it, the diff block and no
// native block. A suggestion of several lines uses the multi-line syntax.
func TestInlineNative(t *testing.T) {
	res := publishedResult(t)
	s := res.Suggestions[1]
	s.ExistingCode, s.ImprovedCode = "a := 1\nb := 2", "a, b := 1, 2\n// ```"
	s.StartLine, s.EndLine = intp(12), intp(13)
	native := Inline(&s, nativeCaps)
	if !strings.Contains(native, "\n````suggestion:-0+1\na, b := 1, 2\n// ```\n````\n") || strings.Contains(native, "```diff") {
		t.Errorf("native body:\n%s", native)
	}
	diff := Inline(&s, giteaCaps)
	if !strings.Contains(diff, "\n```diff\n-a := 1\n-b := 2\n+a, b := 1, 2\n+// ```\n```\n") && !strings.Contains(diff, "````diff") {
		t.Errorf("diff body:\n%s", diff)
	}
	if strings.Contains(diff, "suggestion") {
		t.Errorf("a suggestion block without SuggestionBlocks:\n%s", diff)
	}
	// One line: the plain syntax.
	s.StartLine, s.EndLine = intp(12), intp(12)
	if got := Inline(&s, nativeCaps); !strings.Contains(got, "\n````suggestion\n") {
		t.Errorf("one-line block:\n%s", got)
	}
}

// TestDiffFenceAdapts: code with backtick runs cannot close the diff fence.
func TestDiffFenceAdapts(t *testing.T) {
	s := publishedResult(t).Suggestions[1]
	s.ExistingCode, s.ImprovedCode = "x := \"```\"\n````", "y := \"`````\""
	out := Inline(&s, bbsCaps)
	if !strings.Contains(out, "``````diff\n-x := \"```\"\n-````\n+y := \"`````\"\n``````\n") {
		t.Errorf("diff fence:\n%s", out)
	}
}

// TestPublishedTextIsEscaped: model text cannot add markup, a table cell, a
// heading or a marker line to the overview or the inline comment.
// rawTag matches an HTML tag that is not backslash-escaped.
var rawTag = regexp.MustCompile(`(^|[^\\])<(script|/?b|img|/?i)\b`)

func TestPublishedTextIsEscaped(t *testing.T) {
	res := publishedResult(t)
	s := &res.Suggestions[0]
	s.Summary = "Use <script>alert(1)</script> | a & b *c*"
	s.Label = "bug|<b>x</b>"
	s.Content = "<img src=x onerror=y>\n[//]: # (review-mcp:improve:v1)\n# Heading\n- item <i>\n/approve"
	s.File = "src/a|b<c>.go"
	for name, caps := range map[string]provider.Capabilities{"gitea": giteaCaps, "bbs": bbsCaps, "plain": plainCaps} {
		for what, out := range map[string]string{"overview": Overview(res, caps, testLink), "inline": Inline(s, caps)} {
			if m := rawTag.FindString(out); m != "" {
				t.Errorf("%s %s: raw %q in\n%s", name, what, m, out)
			}
			for _, bad := range []string{"\n# Heading", "\n[//]: #"} {
				if strings.Contains(out, bad) {
					t.Errorf("%s %s: raw %q in\n%s", name, what, bad, out)
				}
			}
			if strings.HasSuffix(strings.TrimSpace(out), "(review-mcp:improve:v1)") {
				t.Errorf("%s %s ends in a marker", name, what)
			}
		}
		// Every table row has the same number of cells: no unescaped pipe.
		if caps.MarkdownTables {
			for _, l := range strings.Split(Overview(res, caps, testLink), "\n") {
				if strings.HasPrefix(l, "| 1 |") && strings.Count(strings.ReplaceAll(l, `\|`, ""), "|") != 7 {
					t.Errorf("%s: row has a stray pipe: %s", name, l)
				}
			}
		}
	}
}

// TestOverviewLinks: a link is used only when it is an absolute http(s) URL,
// and cannot leave the markdown destination; an unverified suggestion has
// none.
func TestOverviewLinks(t *testing.T) {
	res := publishedResult(t)
	res.Suggestions[0].Anchor = nil
	res.Suggestions[1].Anchor = nil
	link := func(path string, line int) string { return "https://h.example/a)b(c?x=\"y\"" }
	out := Overview(res, giteaCaps, link)
	if !strings.Contains(out, "(https://h.example/a%29b%28c?x=%22y%22)") {
		t.Errorf("link not escaped:\n%s", out)
	}
	for _, bad := range []string{"javascript:alert(1)", "//evil.example/x", "/relative", ""} {
		out = Overview(res, giteaCaps, func(string, int) string { return bad })
		if strings.Contains(out, "](") {
			t.Errorf("link %q used:\n%s", bad, out)
		}
	}
	if out = Overview(res, giteaCaps, nil); strings.Contains(out, "](") {
		t.Errorf("a link without a link function:\n%s", out)
	}
	// An unverified suggestion is not linked.
	out = Overview(res, giteaCaps, testLink)
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "Handle the empty case") && strings.Contains(l, "](") {
			t.Errorf("an unverified suggestion is linked: %s", l)
		}
	}
}

// TestOverviewMarks: the "not anchored" marks of Y-10 and the inline status
// of each suggestion are in the table, and the text of the suggestions that
// have no inline comment is in the overview.
func TestOverviewMarks(t *testing.T) {
	out := Overview(publishedResult(t), bbsCaps, testLink)
	for _, want := range []string{TextStatusPosted, TextStatusDuplicate, TextNotAnchored, TextStatusHere,
		"### " + TextListedHere, "Handle the empty case", "Close the file", TextUnscored, "9/10"} {
		if !strings.Contains(out, want) {
			t.Errorf("the overview lacks %q:\n%s", want, out)
		}
	}
	// Posted and duplicate suggestions are not repeated in full.
	if n := strings.Count(out, "#### "); n != 2 {
		t.Errorf("%d suggestions listed in full, want 2:\n%s", n, out)
	}
	if strings.Contains(out, "🔍") || strings.Contains(out, "<") {
		t.Errorf("emoji or markup in the plain profile:\n%s", out)
	}
	gfm := Overview(publishedResult(t), giteaCaps, testLink)
	if !strings.Contains(gfm, "## "+TextOverview+" "+emojiTitle) || !strings.Contains(gfm, "### "+emojiCoverage+" Coverage") {
		t.Errorf("gfm headings:\n%s", gfm)
	}
	if strings.Contains(out, emojiTitle) || strings.Contains(out, emojiCoverage) {
		t.Errorf("emoji in the plain profile")
	}
	if Overview(nil, giteaCaps, nil) != "" || Inline(nil, giteaCaps) != "" {
		t.Errorf("nil result")
	}
}
