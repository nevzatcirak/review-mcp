package repoctx

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func docPatch(path, patch string) provider.FilePatch {
	return provider.FilePatch{Path: path, Type: provider.ChangeModified, Patch: patch}
}

// TestDocNamesChangedLinesOnly: only added lines of documentation files
// give names; context and removed lines never do. [canary]
func TestDocNamesChangedLinesOnly(t *testing.T) {
	files := []provider.FilePatch{docPatch("docs/setup.md",
		"@@ -1,4 +1,4 @@\n Unchanged `ContextName` here.\n-Old `RemovedName` text.\n+Set `llm.max_rate_wait_seconds` and pass ``--publish``.\n+See `internal/llm`.\n")}
	if got, want := DocNames(files), []string{"llm.max_rate_wait_seconds", "--publish", "internal/llm"}; !slices.Equal(got, want) {
		t.Errorf("DocNames = %q, want %q", got, want)
	}
}

func TestDocNamesFilters(t *testing.T) {
	long := strings.Repeat("a", MaxDocNameBytes+1)
	ok100 := strings.Repeat("b", MaxDocNameBytes)
	files := []provider.FilePatch{
		docPatch("README.md", "@@ -1 +1,4 @@\n+`x` `ab` `two words` `"+long+"` `"+ok100+"`\n+`a.b` `-v` `---x` `a..b` `a/` `9lives` `fmt.Println()`\n+an unmatched ` backtick and `ab` again\n+``code with `tick` inside`` `` spaced `` `MaxDocNames`\n"),
		// A code file: names in its comments are not documentation names.
		docPatch("main.go", "@@ -1 +1,2 @@\n+// see `GoComment`\n+x := 1\n"),
		// A reStructuredText file.
		docPatch("docs/index.rst", "@@ -1 +1,2 @@\n+Use ``review.docs_style`` here.\n"),
	}
	want := []string{"ab", ok100, "a.b", "-v", "spaced", "MaxDocNames", "review.docs_style"}
	if got := DocNames(files); !slices.Equal(got, want) {
		t.Errorf("DocNames =\n%q\nwant\n%q", got, want)
	}
}

func TestDocNamesCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@ -0,0 +1,40 @@\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "+`name%02d` and `name00`\n", i)
	}
	got := DocNames([]provider.FilePatch{docPatch("CHANGELOG.md", b.String())})
	if len(got) != MaxDocNames || got[0] != "name00" || got[MaxDocNames-1] != "name29" {
		t.Errorf("DocNames = %d names, first %q, last %q", len(got), got[0], got[len(got)-1])
	}
}

// TestSessionSymbolsWithDocNames: the names of the documentation come first,
// then the code's symbols, capped together by context.repo.max_symbols;
// without LookUpDocNames nothing changes.
func TestSessionSymbolsWithDocNames(t *testing.T) {
	files := []provider.FilePatch{
		docPatch("docs/a.md", "@@ -1 +1,2 @@\n x\n+`Alpha` `doc.name` `other-name`\n"),
		docPatch("pkg/a.go", "@@ -1,2 +1,3 @@\n a\n+func Alpha() {}\n+func Gamma() {}\n b\n"),
	}
	s := &Session{settings: Settings{MaxSymbols: 4}}
	names := func() []string {
		var out []string
		for _, sym := range s.Symbols(files) {
			out = append(out, sym.Name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"Alpha", "Gamma"}) {
		t.Errorf("without doc names: %q", got)
	}
	s.LookUpDocNames()
	if got := names(); !slices.Equal(got, []string{"Alpha", "doc.name", "other-name", "Gamma"}) {
		t.Errorf("with doc names: %q", got)
	}
	s.settings.MaxSymbols = 2
	if got := names(); !slices.Equal(got, []string{"Alpha", "doc.name"}) {
		t.Errorf("capped: %q", got)
	}
	for _, sym := range docSymbols(files) {
		if sym.Path != "" || sym.Rank != gitctx.RankChanged {
			t.Errorf("doc symbol %+v", sym)
		}
	}
}
