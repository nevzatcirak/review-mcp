package review

import (
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/projctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// docsSentence is the system-prompt sentence of the docs focus (v2.1 spec
// V-5 §3a item 1, verbatim).
const docsSentence = "This review covers documentation only: statements in changed documentation (README, guides, CHANGELOG, configuration and API references, doc comments in code) that contradict the code in this pull request or in the repository context; behaviour the code changes without the matching documentation update; names of options, flags, keys, functions, paths, defaults and messages that do not match the code; broken links or anchors; steps that are missing or out of order; and wording, terminology and tone that differ from the rest of the documentation or from the documentation style given below. When the code a statement describes is not shown, do not guess: report only contradictions you can see."

const styleMarker = "FORMAL-IMPERSONAL-STYLE-MARKER"

func TestDocsFocusValue(t *testing.T) {
	if !slices.Equal(FocusValues, []string{"security", "architecture", "performance", "ux", "tests", "docs"}) {
		t.Errorf("FocusValues = %q, want docs after tests", FocusValues)
	}
	for _, in := range []string{"docs", "Docs", " DOCS "} {
		if got, err := NormalizeFocus(in); err != nil || got != FocusDocs {
			t.Errorf("NormalizeFocus(%q) = %q, %v", in, got, err)
		}
	}
	h := newHarness(goodAnswer)
	sys := prepareWith(t, h, Args{Focus: "docs"}).Prompts.System
	if !strings.Contains(sys, "- "+docsSentence+" "+focusOnly) {
		t.Error("the system prompt lacks the docs sentence")
	}
}

// TestDocsStyleOnlyForTheDocsFocus: review.docs_style reaches the prompt,
// fenced under its fixed header, only in a review with focus docs and only
// when it is set; there is no default style. [canary]
func TestDocsStyleOnlyForTheDocsFocus(t *testing.T) {
	for _, tc := range []struct {
		name, focus, style string
		want               bool
	}{
		{"docs with a style", "docs", styleMarker, true},
		{"docs without a style", "docs", "", false},
		{"security with a style", "security", styleMarker, false},
		{"free text with a style", "naming of things", styleMarker, false},
		{"no focus with a style", "", styleMarker, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(goodAnswer)
			h.deps.Config.Review.DocsStyle = tc.style
			pl := prepareWith(t, h, Args{Focus: tc.focus})
			all := pl.Prompts.System + pl.Prompts.User
			if got := strings.Contains(all, projctx.DocsStyleHeader); got != tc.want {
				t.Fatalf("style header present = %v, want %v", got, tc.want)
			}
			if !tc.want {
				if strings.Contains(all, styleMarker) {
					t.Error("the style reached the prompt")
				}
				return
			}
			_, rest, _ := strings.Cut(pl.Prompts.System, projctx.DocsStyleHeader+"\n```\n")
			body, _, closed := strings.Cut(rest, "\n```")
			if !closed || body != styleMarker {
				t.Errorf("the style is not fenced under its header: %q", rest)
			}
		})
	}
}

// TestDocsStyleFromTheRepositoryFile: review.docs_style of the repository
// config replaces the operator's.
func TestDocsStyleFromTheRepositoryFile(t *testing.T) {
	h := newHarness(goodAnswer)
	h.deps.Config.Review.DocsStyle = "OPERATOR-STYLE"
	withRepoConfig(h, "[review]\ndocs_style = \""+styleMarker+"\"\n")
	pl := prepareWith(t, h, Args{Focus: "docs"})
	if !strings.Contains(pl.Prompts.System, styleMarker) || strings.Contains(pl.Prompts.System, "OPERATOR-STYLE") {
		t.Error("the repository's style does not replace the operator's")
	}
	if !slices.Contains(pl.Result.RepoConfig.Applied, "review.docs_style") {
		t.Errorf("applied %q", pl.Result.RepoConfig.Applied)
	}
}

func docsFile(path string) provider.FilePatch {
	return provider.FilePatch{Path: path, Type: provider.ChangeModified, Additions: 1,
		Patch: "@@ -1,2 +1,3 @@\n intro\n+Set `llm.model` first.\n end\n", BaseStatus: provider.ContentNotFetchedSizeCap,
		HeadStatus: provider.ContentNotFetchedSizeCap}
}

// TestDocsOnlyNoteWithoutRepositoryContext: a docs review of a diff that
// changes documentation files only, with repository context off, says the
// documentation was checked against the pull request only. Any code file,
// another focus or repository context on: no note.
func TestDocsOnlyNoteWithoutRepositoryContext(t *testing.T) {
	run := func(focus string, files ...provider.FilePatch) []string {
		h := newHarness(goodAnswer)
		h.prov.files = files
		return prepareWith(t, h, Args{Focus: focus}).Result.Notes
	}
	if notes := run("docs", docsFile("README.md"), docsFile("docs/setup.rst")); !slices.Contains(notes, NoteDocsWithoutRepoContext) {
		t.Errorf("docs-only diff: notes %q", notes)
	}
	if notes := run("docs", docsFile("README.md"), sampleFiles()[0]); slices.Contains(notes, NoteDocsWithoutRepoContext) {
		t.Errorf("a diff with a code file: notes %q", notes)
	}
	if notes := run("security", docsFile("README.md")); slices.Contains(notes, NoteDocsWithoutRepoContext) {
		t.Errorf("another focus: notes %q", notes)
	}
	if notes := run("", docsFile("README.md")); slices.Contains(notes, NoteDocsWithoutRepoContext) {
		t.Errorf("no focus: notes %q", notes)
	}
	files := []provider.FilePatch{docsFile("README.md")}
	if docsOnlyWithoutContext(FocusDocs, true, files) || docsOnlyWithoutContext(FocusDocs, false, nil) ||
		!docsOnlyWithoutContext(FocusDocs, false, files) {
		t.Error("docsOnlyWithoutContext: repository context on, or an empty diff, makes no note")
	}
}

// TestDocsFocusLooksUpDocumentationNames: with repository context on, a docs
// review searches the names the changed documentation mentions; another
// focus does not.
func TestDocsFocusLooksUpDocumentationNames(t *testing.T) {
	files := []provider.FilePatch{docsFile("docs/setup.md")}
	h, fb := repoHarness(files, map[string][]gitctx.Hit{"llm.model": {useHit("llm.model", "internal/config/config.go", 40)}})
	pl := prepareWith(t, h, Args{Focus: "docs"})
	if len(fb.queries) != 1 || !slices.Equal(fb.symbolsOf(0), []string{"llm.model"}) {
		t.Fatalf("queries %+v", fb.queries)
	}
	if !strings.Contains(pl.Prompts.User, "internal/config/config.go") || slices.Contains(pl.Result.Notes, NoteDocsWithoutRepoContext) {
		t.Errorf("the use is not in the prompt, or the note is there: notes %q", pl.Result.Notes)
	}

	h, fb = repoHarness(files, nil)
	prepareWith(t, h, Args{Focus: "security"})
	if len(fb.queries) != 0 || fb.ensures != 0 {
		t.Errorf("another focus searched documentation names: %d queries, %d fetches", len(fb.queries), fb.ensures)
	}
}
