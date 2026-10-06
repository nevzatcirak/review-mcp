package review

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func headFile(content, patch string) *provider.FilePatch {
	return &provider.FilePatch{Path: "f.go", Type: provider.ChangeModified, Patch: patch,
		HeadContent: &content, HeadStatus: provider.ContentFull}
}

func patchOnlyFile(patch string, status provider.ContentStatus) *provider.FilePatch {
	return &provider.FilePatch{Path: "f.go", Type: provider.ChangeModified, Patch: patch, HeadStatus: status}
}

// twoHunks has new-side lines 3-5 and 20-22.
const twoHunks = "@@ -3,2 +3,3 @@\n c3\n+n4\n c5\n@@ -18,3 +20,3 @@\n c20\n-old\n+n21\n c22\n"

func TestSnippet(t *testing.T) {
	head := numberedLines(1, 40, nil)
	for _, tc := range []struct {
		name       string
		fp         *provider.FilePatch
		start, end int
		want, note string
	}{
		// From the head content (complete): any line of the file.
		{"head range", headFile(head, twoHunks), 7, 9, "line 7\nline 8\nline 9", ""},
		{"head single line, end 0", headFile(head, twoHunks), 12, 0, "line 12", ""},
		{"head last line", headFile(head, twoHunks), 40, 40, "line 40", ""},
		{"head beyond end of file", headFile(head, twoHunks), 39, 41, "", SnippetNoteUnverified},
		{"head CRLF", headFile("a\r\nb\r\n", ""), 1, 2, "a\nb", ""},
		{"head without final newline", headFile("a\nb", ""), 2, 2, "b", ""},
		{"head cap", headFile(head, twoHunks), 1, 40, strings.TrimSuffix(numberedLines(1, 30, nil), "\n"), snippetNoteCut},
		{"head exactly the cap", headFile(head, twoHunks), 1, 30, strings.TrimSuffix(numberedLines(1, 30, nil), "\n"), ""},

		// From the patch walk: every line of the range must be on the new side.
		{"patch within a hunk", patchOnlyFile(twoHunks, provider.ContentNotFetchedFileCap), 3, 5, "c3\nn4\nc5", ""},
		{"patch second hunk", patchOnlyFile(twoHunks, provider.ContentFetchFailed), 20, 22, "c20\nn21\nc22", ""},
		{"patch range partly outside the diff", patchOnlyFile(twoHunks, provider.ContentNotFetchedSizeCap), 4, 7, "", SnippetNoteUnverified},
		{"patch range across the gap", patchOnlyFile(twoHunks, provider.ContentNotFetchedSizeCap), 5, 20, "", SnippetNoteUnverified},
		{"patch range before the diff", patchOnlyFile(twoHunks, provider.ContentNotFetchedSizeCap), 1, 2, "", SnippetNoteUnverified},
		{"patch huge range", patchOnlyFile(twoHunks, provider.ContentNotFetchedSizeCap), 3, 1 << 40, "", SnippetNoteUnverified},
		{"patch CRLF", patchOnlyFile("@@ -1 +1,2 @@\n a\r\n+b\r\n", provider.ContentNotFetchedSizeCap), 1, 2, "a\nb", ""},
		{"patch no-newline marker", patchOnlyFile("@@ -1 +1 @@\n-a\n+b\n\\ No newline at end of file\n", provider.ContentNotFetchedSizeCap), 1, 1, "b", ""},
		{"patch unparseable", patchOnlyFile("not a patch", provider.ContentNotFetchedSizeCap), 1, 1, "", SnippetNoteUnverified},
		{"patch cap", patchOnlyFile("@@ -0,0 +1,35 @@\n"+strings.Repeat("+x\n", 35), provider.ContentNotApplicable), 1, 35,
			strings.TrimSuffix(strings.Repeat("x\n", 30), "\n"), snippetNoteCut},
		// A deleted file has no new side.
		{"deleted file", &provider.FilePatch{Path: "f.go", Type: provider.ChangeDeleted, Patch: "@@ -1,2 +0,0 @@\n-a\n-b\n",
			HeadStatus: provider.ContentNotApplicable}, 1, 1, "", SnippetNoteUnverified},
		// A head that was not fetched completely is not used.
		{"head present but not full", &provider.FilePatch{Path: "f.go", Patch: twoHunks, HeadContent: &head,
			HeadStatus: provider.ContentFetchFailed}, 7, 7, "", SnippetNoteUnverified},

		// Ranges that cannot be resolved at all.
		{"unknown file", nil, 1, 2, "", SnippetNoteUnverified},
		{"no start line", headFile(head, twoHunks), 0, 3, "", SnippetNoteUnverified},
		{"end before start", headFile(head, twoHunks), 5, 4, "", SnippetNoteUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, note := snippet(tc.fp, tc.start, tc.end)
			if got != tc.want || note != tc.note {
				t.Errorf("snippet = %q (%q), want %q (%q)", got, note, tc.want, tc.note)
			}
		})
	}
}
