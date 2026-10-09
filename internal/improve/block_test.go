package improve

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// TestReindent pins the re-indentation rule of a native suggestion block:
// the improved code moves by the indentation the quote lost, and every
// case that cannot be corrected safely is refused.
func TestReindent(t *testing.T) {
	for name, tc := range map[string]struct {
		real               []string
		existing, improved string
		want               string
		ok                 bool
	}{
		"dedented quote (spaces)": {
			real: []string{"        x = 1", "        y = 2"}, existing: "x = 1\ny = 2", improved: "x, y = 1, 2",
			want: "        x, y = 1, 2", ok: true,
		},
		"partly dedented quote": {
			real: []string{"        if a:", "            b()"}, existing: "    if a:\n        b()",
			improved: "    if a:\n        c()",
			want:     "        if a:\n            c()", ok: true,
		},
		"same indentation": {
			real: []string{"\t\tcall(i)"}, existing: "\t\tcall(i)", improved: "\t\tcall(i + 1)",
			want: "\t\tcall(i + 1)", ok: true,
		},
		"no indentation": {
			real: []string{"x := 1"}, existing: "x := 1", improved: "const x = 1",
			want: "const x = 1", ok: true,
		},
		"tabs": {
			real: []string{"\t\tcall(i)", "\t\tlog(i)"}, existing: "call(i)\nlog(i)", improved: "if call(i) {\n\tlog(i)\n}",
			want: "\t\tif call(i) {\n\t\t\tlog(i)\n\t\t}", ok: true,
		},
		"mixed real indentation, fully dedented": {
			real: []string{"\t  x()"}, existing: "x()", improved: "y()",
			want: "\t  y()", ok: true,
		},
		"mixed real indentation, dedent from the middle": {
			real: []string{"\t  x()"}, existing: "  x()", improved: "  y()", ok: false,
		},
		"tabs against spaces": {
			real: []string{"\tx()"}, existing: "    x()", improved: "    y()", ok: false,
		},
		"spaces against tabs": {
			real: []string{"    x()"}, existing: "\tx()", improved: "\ty()", ok: false,
		},
		"quote with more indentation than the real lines": {
			real: []string{"  x()"}, existing: "      x()", improved: "      y()", ok: false,
		},
		"quote indented, real lines not": {
			real: []string{"x()"}, existing: "\tx()", improved: "\ty()", ok: false,
		},
		"blank lines inside": {
			real: []string{"    a()", "", "    b()"}, existing: "a()\n\nb()", improved: "a()\n\n  \nb()\n",
			want: "    a()\n\n  \n    b()\n", ok: true,
		},
		"blank line in the real range with white space": {
			real: []string{"\tif x {", "\t ", "\t}"}, existing: "if x {\n\n}", improved: "if y {\n}",
			want: "\tif y {\n\t}", ok: true,
		},
		"CRLF quote and code": {
			real: []string{"    a()", "    b()"}, existing: "a()\r\nb()", improved: "c()\r\nd()\r\n",
			want: "    c()\n    d()\n", ok: true,
		},
		"code less indented than the quote keeps its relative place": {
			real: []string{"\t\tif a {", "\t\t\tb()", "\t\t}"}, existing: "\tif a {\n\t\tb()\n\t}", improved: "b()",
			want: "\tb()", ok: true,
		},
		"quote does not match the real lines": {
			real: []string{"    a()"}, existing: "b()", improved: "c()", ok: false,
		},
		"quote of another length": {
			real: []string{"    a()", "    b()"}, existing: "a()", improved: "c()", ok: false,
		},
	} {
		got, ok := reindent(tc.real, tc.existing, tc.improved)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%s: reindent = %q, %v; want %q, %v", name, got, ok, tc.want, tc.ok)
		}
	}
}

// blockHead is a tab-indented head file; blockPatch is its diff (new lines
// 3 to 8).
const (
	blockHead  = "package app\n\nfunc Run() {\n\tfor i := 0; i < 3; i++ {\n\t\tcall(i)\n\t\tlog(i)\n\t}\n}\n"
	blockPatch = "@@ -3,5 +3,6 @@\n func Run() {\n \tfor i := 0; i < 3; i++ {\n \t\tcall(i)\n+\t\tlog(i)\n \t}\n }\n"
)

// capsProvider is a provider that only reports its capabilities.
type capsProvider struct {
	provider.Provider
	caps provider.Capabilities
}

func (p capsProvider) Capabilities() provider.Capabilities { return p.caps }

// blockRender renders the change as the real renderer does: a suggestion
// block of the improved code with a native style, a diff block otherwise.
func blockRender(s *Suggestion, caps provider.Capabilities) string {
	if caps.NativeSuggestionStyle() != provider.SuggestionStyleNone {
		return "```suggestion\n" + strings.TrimSuffix(s.ImprovedCode, "\n") + "\n```"
	}
	var b strings.Builder
	b.WriteString("```diff\n")
	for _, l := range strings.Split(strings.TrimSuffix(s.ExistingCode, "\n"), "\n") {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range strings.Split(strings.TrimSuffix(s.ImprovedCode, "\n"), "\n") {
		b.WriteString("+" + l + "\n")
	}
	return b.String() + "```"
}

var githubCaps = provider.Capabilities{GFM: true, MarkdownTables: true, Labels: true, InlineComments: true,
	SuggestionBlocks: true, SuggestionStyle: provider.SuggestionStyleRange}

func blockSuggestion(existing, improved string, start, end int) Suggestion {
	return Suggestion{File: "src/app.go", Summary: "S " + existing, ExistingCode: existing, ImprovedCode: improved,
		StartLine: &start, EndLine: &end, Verified: true}
}

// planBlocks plans the inline comments of sugs against the head file (or,
// without head, the patch alone) for a provider with caps.
func planBlocks(t *testing.T, caps provider.Capabilities, head bool, sugs ...Suggestion) []inlineItem {
	t.Helper()
	fp := &provider.FilePatch{Path: "src/app.go", Type: provider.ChangeModified, Patch: blockPatch,
		HeadStatus: provider.ContentNotFetchedSizeCap}
	if head {
		content := blockHead
		fp.HeadContent, fp.HeadStatus = &content, provider.ContentFull
	}
	pl := &Plan{Result: &Result{Suggestions: sugs}, p: capsProvider{caps: caps},
		files: map[string]*provider.FilePatch{fp.Path: fp}, log: slog.New(slog.DiscardHandler)}
	items, sum := pl.planInline(blockRender, nil, false)
	if len(items) != len(sugs) || sum.Unanchorable != 0 {
		t.Fatalf("planned %d of %d suggestions (%+v)", len(items), len(sugs), sum)
	}
	for i := range sugs {
		if pl.Result.Suggestions[i].ImprovedCode != sugs[i].ImprovedCode {
			t.Errorf("suggestion %d: the result's improved code was changed to %q", i, pl.Result.Suggestions[i].ImprovedCode)
		}
	}
	return items
}

// change is the rendered change of an item's body (before the marker).
func change(it inlineItem) string {
	body, _, _ := strings.Cut(it.item.Body, "\n\n[//]: #")
	return body
}

// TestPlanInlineSuggestionBlocks: on a provider with the range style a
// verified, anchorable suggestion whose quote the model dedented gets a
// suggestion block re-indented to the head lines, on one line and on a
// range (the item then covers the range, EndLine). The real lines come from
// the head file, or from the patch when the head file was not fetched.
func TestPlanInlineSuggestionBlocks(t *testing.T) {
	sugs := []Suggestion{
		// One line, quoted without its two tabs.
		blockSuggestion("call(i)", "call(i + 1)", 5, 5),
		// Two lines, quoted with one tab of two; a blank line inside the
		// improved code stays blank.
		blockSuggestion("\tcall(i)\n\tlog(i)", "\tif call(i) {\n\n\t\tlog(i)\n\t}", 5, 6),
		// Quoted as it is.
		blockSuggestion("\t\tlog(i)", "\t\tlog(i + 1)", 6, 6),
	}
	want := []struct {
		line, end int
		change    string
	}{
		{5, 0, "```suggestion\n\t\tcall(i + 1)\n```"},
		{5, 6, "```suggestion\n\t\tif call(i) {\n\n\t\t\tlog(i)\n\t\t}\n```"},
		{6, 0, "```suggestion\n\t\tlog(i + 1)\n```"},
	}
	for _, head := range []bool{true, false} {
		items := planBlocks(t, githubCaps, head, sugs...)
		for i, it := range items {
			if it.item.Line != want[i].line || it.item.EndLine != want[i].end {
				t.Errorf("head %v, item %d: lines %d-%d, want %d-%d", head, i, it.item.Line, it.item.EndLine, want[i].line, want[i].end)
			}
			if got := change(it); got != want[i].change {
				t.Errorf("head %v, item %d: change\n%s\nwant\n%s", head, i, got, want[i].change)
			}
		}
	}
}

// TestPlanInlineDiffBlockFallback: when the re-indentation is not safe (the
// model added indentation, or used spaces for the file's tabs) the comment
// keeps the diff block with the code as the model wrote it; a provider
// without a suggestion style always gets the diff block, unchanged.
func TestPlanInlineDiffBlockFallback(t *testing.T) {
	added := blockSuggestion("\t\t\tcall(i)", "\t\t\tcall(i + 1)", 5, 5)
	spaces := blockSuggestion("    call(i)\n    log(i)", "    call(i + 1)\n    log(i)", 5, 6)
	dedented := blockSuggestion("call(i)", "call(i + 1)", 5, 5)
	items := planBlocks(t, githubCaps, true, added, spaces)
	if got, want := change(items[0]), "```diff\n-\t\t\tcall(i)\n+\t\t\tcall(i + 1)\n```"; got != want {
		t.Errorf("added indentation: change\n%s\nwant\n%s", got, want)
	}
	if got, want := change(items[1]), "```diff\n-    call(i)\n-    log(i)\n+    call(i + 1)\n+    log(i)\n```"; got != want {
		t.Errorf("spaces for tabs: change\n%s\nwant\n%s", got, want)
	}
	if items[1].item.EndLine != 6 {
		t.Errorf("the diff block's item does not cover the range: %+v", items[1].item)
	}
	for name, caps := range map[string]provider.Capabilities{"gitea": giteaCaps, "bitbucket": bbsCaps} {
		items := planBlocks(t, caps, true, dedented)
		if got, want := change(items[0]), "```diff\n-call(i)\n+call(i + 1)\n```"; got != want {
			t.Errorf("%s: change\n%s\nwant\n%s", name, got, want)
		}
	}
}
