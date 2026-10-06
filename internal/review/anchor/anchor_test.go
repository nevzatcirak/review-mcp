package anchor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// numbered returns "line from" to "line to", one per line, skipping skip.
func numbered(from, to, skip int) string {
	var b strings.Builder
	for n := from; n <= to; n++ {
		if n != skip {
			fmt.Fprintf(&b, "line %d\n", n)
		}
	}
	return b.String()
}

// modified is a one-hunk change whose full contents are known, so the
// review prompt extends its hunk (lines 5 to 9 before, 14 after with the
// default config). The server's hunk is lines 10 to 13 only.
func modified() *provider.FilePatch {
	head, base := numbered(1, 20, 0), numbered(1, 20, 11)
	return &provider.FilePatch{
		Path: "src/app.go", Type: provider.ChangeModified, Additions: 1,
		Patch:       "@@ -10,3 +10,4 @@\n line 10\n+line 11\n line 12\n line 13\n",
		BaseContent: &base, HeadContent: &head, BaseStatus: provider.ContentFull, HeadStatus: provider.ContentFull,
	}
}

func twoHunks() *provider.FilePatch {
	return &provider.FilePatch{
		Path: "src/two.go", Type: provider.ChangeModified,
		Patch: "@@ -2,3 +2,3 @@ func a()\n a\n-b\n+B\n c\n@@ -20,3 +20,4 @@ func z()\n x\n+y\n z\n w\n",
	}
}

// TestResolveGoldens pins every case of spec P7 §3.1.
func TestResolveGoldens(t *testing.T) {
	cases := []struct {
		name       string
		file       *provider.FilePatch
		start, end int
		want       Anchor
		ok         bool
	}{
		{"added line", modified(), 11, 11, Anchor{Path: "src/app.go", Line: 11, LineType: provider.LineAdded}, true},
		{"context line", modified(), 10, 10, Anchor{Path: "src/app.go", Line: 10, LineType: provider.LineContext}, true},
		{"last context line", modified(), 13, 13, Anchor{Path: "src/app.go", Line: 13, LineType: provider.LineContext}, true},
		{"range starting before a hunk", modified(), 5, 12, Anchor{Path: "src/app.go", Line: 10, LineType: provider.LineContext}, true},
		{"range spanning two hunks", twoHunks(), 5, 22, Anchor{Path: "src/two.go", Line: 20, LineType: provider.LineContext}, true},
		{"range ending in the second hunk's added line", twoHunks(), 5, 21,
			Anchor{Path: "src/two.go", Line: 20, LineType: provider.LineContext}, true},
		{"range inside the first of two hunks", twoHunks(), 3, 22, Anchor{Path: "src/two.go", Line: 3, LineType: provider.LineAdded}, true},
		{"range entirely outside the hunks", modified(), 15, 18, Anchor{}, false},
		{"range between two hunks", twoHunks(), 5, 19, Anchor{}, false},
		{"context line outside the server's hunk", modified(), 8, 8, Anchor{}, false},
		{"after-context outside the server's hunk", modified(), 14, 14, Anchor{}, false},
		{"end before start means start", modified(), 11, 3, Anchor{Path: "src/app.go", Line: 11, LineType: provider.LineAdded}, true},
		{"unknown start line", modified(), 0, 12, Anchor{}, false},
		{"deleted file", &provider.FilePatch{Path: "old/gone.go", Type: provider.ChangeDeleted,
			Patch: "@@ -1,2 +0,0 @@\n-a\n-b\n"}, 1, 1, Anchor{}, false},
		{"binary file", &provider.FilePatch{Path: "logo.png", Type: provider.ChangeModified, Binary: true,
			Patch: "@@ -1 +1 @@\n-a\n+b\n"}, 1, 1, Anchor{}, false},
		{"added file", &provider.FilePatch{Path: "src/new.go", Type: provider.ChangeAdded,
			Patch: "@@ -0,0 +1,2 @@\n+a\n+b\n"}, 2, 5, Anchor{Path: "src/new.go", Line: 2, LineType: provider.LineAdded}, true},
		{"renamed file", &provider.FilePatch{Path: "src/renamed.go", OldPath: "src/old_name.go", Type: provider.ChangeRenamed,
			Patch: "@@ -1,2 +1,2 @@\n a\n-b\n+c\n"}, 2, 2,
			Anchor{Path: "src/renamed.go", OldPath: "src/old_name.go", Line: 2, LineType: provider.LineAdded}, true},
		{"renamed file, context line", &provider.FilePatch{Path: "src/renamed.go", OldPath: "src/old_name.go", Type: provider.ChangeRenamed,
			Patch: "@@ -1,2 +1,2 @@\n a\n-b\n+c\n"}, 1, 1,
			Anchor{Path: "src/renamed.go", OldPath: "src/old_name.go", Line: 1, LineType: provider.LineContext}, true},
		{"no newline at end of file", &provider.FilePatch{Path: "src/eof.go", Type: provider.ChangeModified,
			Patch: "@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n"}, 2, 2,
			Anchor{Path: "src/eof.go", Line: 2, LineType: provider.LineAdded}, true},
		{"no newline at end of file, line after the end", &provider.FilePatch{Path: "src/eof.go", Type: provider.ChangeModified,
			Patch: "@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n"}, 3, 3, Anchor{}, false},
		{"malformed pseudo-hunk is skipped", &provider.FilePatch{Path: "src/m.go", Type: provider.ChangeModified,
			Patch: "@@ -1 +1 @@\n-a\n+b\n@@@ -5,1 -5,1 +5,1 @@@\n+c\n"}, 1, 5, Anchor{Path: "src/m.go", Line: 1, LineType: provider.LineAdded}, true},
		{"nil file", nil, 1, 1, Anchor{}, false},
		{"unparseable patch", &provider.FilePatch{Path: "src/x.go", Type: provider.ChangeModified, Patch: "not a patch"}, 1, 1, Anchor{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Resolve(c.file, c.start, c.end)
			if got != c.want || ok != c.ok {
				t.Errorf("Resolve(%d, %d) = %+v, %v; want %+v, %v", c.start, c.end, got, ok, c.want, c.ok)
			}
		})
	}
}

// TestExtendedContextIsVisibleToTheModel guards the canary cases above: the
// lines the server does not show (8 and 14, and 5 to 9 of the range
// starting before the hunk) are in the extended context the model sees, so
// a resolver that used it would anchor them.
func TestExtendedContextIsVisibleToTheModel(t *testing.T) {
	fp := modified()
	hunks, err := patch.ParseHunks(fp.Patch)
	if err != nil {
		t.Fatal(err)
	}
	ext := patch.ExtendFile(*fp, hunks, config.Defaults().Diff)
	if len(ext) != 1 || ext[0].NewStart != 5 || ext[0].NewLen != 10 {
		t.Fatalf("extended hunk = %+v", ext)
	}
	if fp.Patch != modified().Patch {
		t.Errorf("the extension changed the provider's patch")
	}
}

func TestAnchorComment(t *testing.T) {
	a := Anchor{Path: "src/renamed.go", OldPath: "src/old_name.go", Line: 2, LineType: provider.LineContext}
	want := provider.InlineComment{Path: "src/renamed.go", OldPath: "src/old_name.go", Line: 2,
		LineType: provider.LineContext, Body: "body"}
	if got := a.Comment("body"); got != want {
		t.Errorf("Comment = %+v", got)
	}
}
