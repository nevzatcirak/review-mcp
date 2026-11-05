package review

import (
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/projctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// The documentation style from a repository file (X-51, v2.9 spec V-19
// §2.2). The markers are distinctive so that a leak shows.
const (
	styleFilePath  = "docs/STYLE.md"
	fileStyleBase  = "STYLE-FILE-AT-BASE-QX4"
	fileStyleHead  = "STYLE-FILE-AT-HEAD-QX4"
	textStyleQX4   = "STYLE-TEXT-QX4"
	docStandardTxt = "DOC-STANDARD-INSTRUCTIONS-QX4"
)

// styleHarness is a harness whose repository config names the style file,
// which holds fileStyleBase at the base revision and fileStyleHead at the
// head (the pull request changed it).
func styleHarness() *harness {
	h := newHarness(goodAnswer)
	h.prov.revs["base0"][styleFilePath] = "Write in the present tense. " + fileStyleBase + "\n"
	h.prov.revs["abc"] = map[string]string{styleFilePath: "Write in the past tense. " + fileStyleHead + "\n"}
	withRepoConfig(h, "[review]\ndocs_style_file = \""+styleFilePath+"\"\n")
	return h
}

func styleFileReads(h *harness) []string {
	var out []string
	for _, r := range h.prov.reads {
		if strings.HasPrefix(r, "file "+styleFilePath+"@") {
			out = append(out, strings.TrimPrefix(r, "file "+styleFilePath+"@"))
		}
	}
	return out
}

// TestDocsStyleFileIsReadAtTheBaseRevision [canary i]: the style comes from
// the base revision, once; the head's version of the file (the pull request
// changed it) is never read.
func TestDocsStyleFileIsReadAtTheBaseRevision(t *testing.T) {
	h := styleHarness()
	pl := prepareWith(t, h, Args{Focus: "docs"})
	if !strings.Contains(pl.Prompts.User, fileStyleBase) || strings.Contains(pl.Prompts.User+pl.Prompts.System, fileStyleHead) {
		t.Errorf("the style is not the base revision's:\n%s", pl.Prompts.User)
	}
	if got := styleFileReads(h); !slices.Equal(got, []string{"base0"}) {
		t.Errorf("style file reads at %q, want one read at base0", got)
	}
	want := llmrun.DocsStyle{Source: "file", Bytes: len("Write in the present tense. " + fileStyleBase)}
	if c := pl.Result.Coverage.DocsStyle; c == nil || *c != want {
		t.Errorf("coverage.docs_style = %+v, want %+v", c, want)
	}
}

// TestDocsStyleFileOnlyWhenTheFocusUsesIt: no read for a focus that does not
// use the style, and no coverage entry.
func TestDocsStyleFileOnlyWhenTheFocusUsesIt(t *testing.T) {
	for _, focus := range []string{"", "security", "naming of things"} {
		h := styleHarness()
		pl := prepareWith(t, h, Args{Focus: focus})
		if got := styleFileReads(h); len(got) != 0 {
			t.Errorf("focus %q read the style file: %q", focus, got)
		}
		if pl.Result.Coverage.DocsStyle != nil || strings.Contains(pl.Prompts.User+pl.Prompts.System, fileStyleBase) {
			t.Errorf("focus %q: coverage %+v", focus, pl.Result.Coverage.DocsStyle)
		}
	}
}

// customDocsHarness adds a repository custom focus with docs_style = true.
func customDocsHarness(styleSet bool) *harness {
	h := newHarness(goodAnswer)
	h.prov.revs["base0"][styleFilePath] = "Define terms on first use. " + fileStyleBase + "\n"
	cfg := "[review]\n"
	if styleSet {
		cfg += "docs_style_file = \"" + styleFilePath + "\"\n"
	}
	cfg += "[[review.custom_focuses]]\nname = \"doc-standard\"\ninstructions = \"" + docStandardTxt + "\"\ndocs_style = true\n"
	withRepoConfig(h, cfg)
	return h
}

// TestDocsStyleInTheUserPromptOnly [canary l]: for the docs focus and for a
// custom focus with docs_style = true the style text is in the user prompt,
// under its fixed header, and not in the system prompt, which keeps one
// fixed sentence about it. Without a style the system prompt has no
// sentence.
func TestDocsStyleInTheUserPromptOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		h     *harness
		focus string
	}{
		"docs":                {styleHarness(), "docs"},
		"custom docs_style":   {customDocsHarness(true), "doc-standard"},
		"custom, mixed case":  {customDocsHarness(true), "Doc-Standard"},
		"docs, style by text": {textStyleHarness(), "docs"},
	} {
		t.Run(name, func(t *testing.T) {
			pl := prepareWith(t, tc.h, Args{Focus: tc.focus})
			sys, usr := pl.Prompts.System, pl.Prompts.User
			if strings.Contains(sys, projctx.DocsStyleHeader) || strings.Contains(sys, fileStyleBase) || strings.Contains(sys, textStyleQX4) ||
				strings.Contains(sys, "present tense") || strings.Contains(sys, "Define terms") {
				t.Errorf("the style reached the system prompt:\n%s", sys)
			}
			if !strings.Contains(sys, "\n\n"+projctx.DocsStyleSentence+"\n") || strings.Count(sys, projctx.DocsStyleSentence) != 1 {
				t.Errorf("the system prompt lacks the fixed sentence once")
			}
			if !strings.Contains(usr, projctx.DocsStyleHeader+"\n```\n") {
				t.Errorf("the user prompt lacks the style block:\n%s", usr)
			}
			if !strings.Contains(usr, fileStyleBase) && !strings.Contains(usr, textStyleQX4) {
				t.Errorf("the style text is not in the user prompt")
			}
			// The system prompt differs from the same run without a style
			// by the sentence alone.
			if strings.Replace(sys, "\n\n"+projctx.DocsStyleSentence, "", 1) == sys {
				t.Error("the sentence is not a paragraph of its own")
			}
		})
	}

	// No style given: no sentence, no block.
	h := newHarness(goodAnswer)
	pl := prepareWith(t, h, Args{Focus: "docs"})
	if strings.Contains(pl.Prompts.System, projctx.DocsStyleSentence) || strings.Contains(pl.Prompts.User, projctx.DocsStyleHeader) {
		t.Error("a style block or sentence without a style")
	}
	if c := pl.Result.Coverage.DocsStyle; c == nil || c.Source != "none" || c.Bytes != 0 || c.Cut {
		t.Errorf("coverage.docs_style = %+v, want source none", c)
	}
}

func textStyleHarness() *harness {
	h := newHarness(goodAnswer)
	h.deps.Config.Review.DocsStyle = textStyleQX4
	return h
}

// TestDocsStylePromptsDifferByTheBlockAndTheSentence: the golden of the
// placement. With a style the system prompt is the plain docs prompt plus
// the sentence, and the user prompt the plain one plus the block after the
// focus block.
func TestDocsStylePromptsDifferByTheBlockAndTheSentence(t *testing.T) {
	in := sampleInput()
	in.Focus = FocusDocs
	plain, err := RenderPrompts(in)
	if err != nil {
		t.Fatal(err)
	}
	block := projctx.DocsStyle("Use the present tense.")
	in.DocsStyle = block
	styled, err := RenderPrompts(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Replace(styled.System, "\n\n"+projctx.DocsStyleSentence, "", 1) != plain.System {
		t.Errorf("the system prompts differ beyond the sentence")
	}
	if strings.Replace(styled.User, "\n\n"+block, "", 1) != plain.User {
		t.Errorf("the user prompts differ beyond the block")
	}
	if strings.Contains(styled.System, "Use the present tense.") {
		t.Error("the style is in the system prompt")
	}
}

// TestDocsStyleFileFallsBack [canary j]: a missing or unreadable file falls
// back to review.docs_style (or none) with the fixed note and a coverage
// entry; the run does not fail.
func TestDocsStyleFileFallsBack(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*harness)
		text   string
		note   string
		source string
	}{
		"missing, text set": {func(h *harness) { delete(h.prov.revs["base0"], styleFilePath) }, textStyleQX4,
			projctx.NoteDocsStyleFileNotFoundText, "text"},
		"missing, none": {func(h *harness) { delete(h.prov.revs["base0"], styleFilePath) }, "",
			projctx.NoteDocsStyleFileNotFoundNone, "none"},
		"unreadable, text set": {func(h *harness) { h.prov.readErr = &provider.Error{Class: provider.ClassTransport} }, textStyleQX4,
			projctx.NoteDocsStyleFileUnreadText, "text"},
		"unreadable, none": {func(h *harness) { h.prov.readErr = &provider.Error{Class: provider.ClassTransport} }, "",
			projctx.NoteDocsStyleFileUnreadNone, "none"},
		"empty file": {func(h *harness) { h.prov.revs["base0"][styleFilePath] = " \n\n" }, "",
			projctx.NoteDocsStyleFileEmptyNone, "none"},
		"binary file": {func(h *harness) { h.prov.revs["base0"][styleFilePath] = "abc\x00def" }, textStyleQX4,
			projctx.NoteDocsStyleFileUnreadText, "text"},
	} {
		t.Run(name, func(t *testing.T) {
			h := styleHarness()
			h.deps.Config.Review.DocsStyle = tc.text
			tc.mutate(h)
			pl := prepareWith(t, h, Args{Focus: "docs"})
			if !slices.Contains(pl.Result.Notes, tc.note) {
				t.Errorf("notes %q, want %q", pl.Result.Notes, tc.note)
			}
			if c := pl.Result.Coverage.DocsStyle; c == nil || c.Source != tc.source || c.Cut {
				t.Errorf("coverage.docs_style = %+v, want source %s", c, tc.source)
			}
			if got := strings.Contains(pl.Prompts.User, textStyleQX4); got != (tc.source == "text") {
				t.Errorf("the fallback text in the prompt = %v", got)
			}
			if strings.Contains(pl.Prompts.User, fileStyleBase) {
				t.Error("the unreadable file's text is in the prompt")
			}
		})
	}
}

// TestDocsStyleFileReplacesTheText: when both are set and the file is read,
// the file wins and a note says so.
func TestDocsStyleFileReplacesTheText(t *testing.T) {
	h := styleHarness()
	h.deps.Config.Review.DocsStyle = textStyleQX4
	pl := prepareWith(t, h, Args{Focus: "docs"})
	if strings.Contains(pl.Prompts.User, textStyleQX4) || !strings.Contains(pl.Prompts.User, fileStyleBase) {
		t.Error("the file did not replace the text")
	}
	if !slices.Contains(pl.Result.Notes, projctx.NoteDocsStyleFileReplaced) {
		t.Errorf("notes %q", pl.Result.Notes)
	}
	// Only the file: no such note.
	pl = prepareWith(t, styleHarness(), Args{Focus: "docs"})
	if slices.Contains(pl.Result.Notes, projctx.NoteDocsStyleFileReplaced) {
		t.Errorf("notes %q", pl.Result.Notes)
	}
}

// TestDocsStyleFileIsCutAtALineBoundary: a file over 8 KiB is cut at a line
// boundary under 8 KiB, with the note and coverage.
func TestDocsStyleFileIsCutAtALineBoundary(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() < 20*1024; i++ {
		b.WriteString("Rule " + strings.Repeat("x", 60) + " end-of-line\n")
	}
	h := styleHarness()
	h.prov.revs["base0"][styleFilePath] = b.String()
	pl := prepareWith(t, h, Args{Focus: "docs"})
	c := pl.Result.Coverage.DocsStyle
	if c == nil || c.Source != "file" || !c.Cut || c.Bytes > projctx.MaxDocsStyleFileBytes || c.Bytes < projctx.MaxDocsStyleFileBytes-100 {
		t.Fatalf("coverage.docs_style = %+v", c)
	}
	if !slices.Contains(pl.Result.Notes, projctx.NoteDocsStyleFileCut) {
		t.Errorf("notes %q", pl.Result.Notes)
	}
	_, rest, _ := strings.Cut(pl.Prompts.User, projctx.DocsStyleHeader+"\n```\n")
	body, _, _ := strings.Cut(rest, "\n```")
	if len(body) != c.Bytes || !strings.HasSuffix(body, "end-of-line") {
		t.Errorf("the cut text is %d bytes and ends %q", len(body), body[max(0, len(body)-20):])
	}
}

// TestDocsStyleCutEdges: the cut on its edges.
func TestDocsStyleCutEdges(t *testing.T) {
	long := strings.Repeat("\u00e9", projctx.MaxDocsStyleFileBytes) // one line of 2-byte runes
	h := styleHarness()
	h.prov.revs["base0"][styleFilePath] = long
	pl := prepareWith(t, h, Args{Focus: "docs"})
	c := pl.Result.Coverage.DocsStyle
	if c == nil || !c.Cut || c.Bytes > projctx.MaxDocsStyleFileBytes || c.Bytes%2 != 0 {
		t.Errorf("a single long line: %+v", c)
	}
	h.prov.revs["base0"][styleFilePath] = strings.Repeat("a", projctx.MaxDocsStyleFileBytes)
	pl = prepareWith(t, h, Args{Focus: "docs"})
	if c := pl.Result.Coverage.DocsStyle; c == nil || c.Cut || c.Bytes != projctx.MaxDocsStyleFileBytes {
		t.Errorf("exactly 8 KiB: %+v", c)
	}
}

// TestDocsStyleAndFocusTextNeverLeak [canary k]: the style text and the
// custom instructions are in the user prompt only; they never reach the logs
// or the notes, whether the style file was read, missing or unreadable.
func TestDocsStyleAndFocusTextNeverLeak(t *testing.T) {
	for name, mutate := range map[string]func(*harness){
		"read":       func(*harness) {},
		"missing":    func(h *harness) { delete(h.prov.revs["base0"], styleFilePath) },
		"unreadable": func(h *harness) { h.prov.readErr = &provider.Error{Class: provider.ClassTransport} },
		"cut": func(h *harness) {
			h.prov.revs["base0"][styleFilePath] = strings.Repeat(fileStyleBase+"\n", 2000)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := customDocsHarness(true)
			h.deps.Config.Review.DocsStyle = textStyleQX4
			mutate(h)
			pl := prepareWith(t, h, Args{Focus: "doc-standard"})
			leaks := []string{fileStyleBase, textStyleQX4, docStandardTxt, "Define terms"}
			notes := strings.Join(pl.Result.Notes, "\n")
			for _, l := range leaks {
				if strings.Contains(h.logs.String(), l) {
					t.Errorf("%q reached the logs", l)
				}
				if strings.Contains(notes, l) {
					t.Errorf("%q reached the notes", l)
				}
				if strings.Contains(pl.Prompts.System, l) {
					t.Errorf("%q reached the system prompt", l)
				}
			}
			if !strings.Contains(pl.Prompts.User, docStandardTxt) {
				t.Error("the instructions are not in the user prompt")
			}
		})
	}
}

// TestCustomDocsStyleFlagDecidesTheRead: a custom focus without docs_style
// reads no style file.
func TestCustomDocsStyleFlagDecidesTheRead(t *testing.T) {
	h := newHarness(goodAnswer)
	h.prov.revs["base0"][styleFilePath] = fileStyleBase
	withRepoConfig(h, "[review]\ndocs_style_file = \""+styleFilePath+"\"\n[[review.custom_focuses]]\nname = \"api-contract\"\ninstructions = \"x\"\n")
	pl := prepareWith(t, h, Args{Focus: "api-contract"})
	if len(styleFileReads(h)) != 0 || pl.Result.Coverage.DocsStyle != nil || strings.Contains(pl.Prompts.User, projctx.DocsStyleHeader) {
		t.Error("a custom focus without docs_style got the style")
	}
}
