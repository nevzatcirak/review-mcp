package patch

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func ptr(s string) *string { return &s }

// Expected values recorded from CPython 3.12 str.splitlines().
func TestPySplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"\n", []string{""}},
		{"a\r\nb\r", []string{"a", "b"}},
		{"a\f\n", []string{"a", ""}},
		{"a\rb\nc", []string{"a", "b", "c"}},
		{"a\x0bb\x1cc\x1dd\x1ee", []string{"a", "b", "c", "d", "e"}},
		{"a\u0085b\u2028c\u2029d", []string{"a", "b", "c", "d"}},
		{"a\x1fb\tc", []string{"a\x1fb\tc"}},
		{"a\xffb\n", []string{"a\xffb"}},
		{"\r\n\r\n", []string{"", ""}},
	}
	for _, c := range cases {
		if got := pySplitLines(c.in); !slices.Equal(got, c.want) {
			t.Errorf("pySplitLines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	_, keep := pySplitLinesKeep("a\r\nb\fc")
	if want := []string{"a\r\n", "b\f", "c"}; !slices.Equal(keep, want) {
		t.Errorf("pySplitLinesKeep ends = %q, want %q", keep, want)
	}
}

// Expected values recorded from CPython 3.12 str.strip().
func TestPyStrip(t *testing.T) {
	cases := map[string]string{
		"  a b  ":                 "a b",
		"\x1c\x1fa\x1e":           "a",
		"\u3000a\u00a0":           "a",
		"\u200ba\u200b":           "\u200ba\u200b", // ZERO WIDTH SPACE is not whitespace
		"\t\n\x0b\x0c\r a \u0085": "a",
		"a \x85":                  "a \x85", // invalid UTF-8 byte, not U+0085
	}
	for in, want := range cases {
		if got := pyStrip(in); got != want {
			t.Errorf("pyStrip(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPySlice(t *testing.T) {
	s := []string{"a", "b", "c", "d"}
	cases := []struct {
		a, b int
		want []string
	}{
		{0, 2, []string{"a", "b"}},
		{0, -1, []string{"a", "b", "c"}},
		{-1, 0, []string{}},
		{-1, 5, []string{"d"}},
		{3, 1, []string{}},
		{-9, 9, []string{"a", "b", "c", "d"}},
	}
	for _, c := range cases {
		if got := pySlice(s, c.a, c.b); !slices.Equal(got, c.want) {
			t.Errorf("pySlice(%d,%d) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestParseHunks(t *testing.T) {
	p := "@@ -3 +3,2 @@ func main() {\r\n ctx\r\n-old\r\n+new\r\n+more\r\n" +
		"@@@ -1,2 -1,2 +1,2 @@@\n- a\n +b\n" +
		"@@ -0,0 +1 @@x\n+y\n\\ No newline at end of file\n"
	hunks, err := ParseHunks(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 3 {
		t.Fatalf("got %d hunks, want 3", len(hunks))
	}
	h := hunks[0]
	if h.OldStart != 3 || h.OldLen != 1 || h.NewStart != 3 || h.NewLen != 2 || h.Section != "func main() {" || h.Malformed() {
		t.Errorf("hunk 0 = %+v", h)
	}
	if want := []Line{{' ', "ctx\r\n"}, {'-', "old\r\n"}, {'+', "new\r\n"}, {'+', "more\r\n"}}; !slices.Equal(h.Lines, want) {
		t.Errorf("hunk 0 lines = %q", h.Lines)
	}
	if !hunks[1].Malformed() || len(hunks[1].Lines) != 2 {
		t.Errorf("hunk 1 should be a malformed pseudo-hunk with 2 lines: %+v", hunks[1])
	}
	h = hunks[2]
	if h.OldStart != 0 || h.OldLen != 0 || h.NewStart != 1 || h.NewLen != 1 || h.Section != "x" {
		t.Errorf("hunk 2 = %+v", h)
	}
	if last := h.Lines[len(h.Lines)-1]; last.Op != '\\' || last.Text != " No newline at end of file\n" {
		t.Errorf("marker line = %+v", last)
	}
	if got := patchText(hunks); got != p {
		t.Errorf("round trip = %q", got)
	}

	if hs, err := ParseHunks(""); err != nil || hs != nil {
		t.Errorf("empty patch: %v, %v", hs, err)
	}
	for _, bad := range []string{"diff --git a/x b/x\n@@ -1 +1 @@\n", "@@ -1 +1 @@\n\n+x\n", "@@ -1 +1 @@\n*x\n"} {
		if _, err := ParseHunks(bad); err == nil {
			t.Errorf("ParseHunks(%q) succeeded, want error", bad)
		}
	}
}

func loadCase(t *testing.T, name string) (provider.FilePatch, config.Diff, []Hunk) {
	t.Helper()
	fp, d := loadGolden(t, filepath.Join("testdata/upstream", name))
	hunks, err := ParseHunks(fp.Patch)
	if err != nil {
		t.Fatal(err)
	}
	return fp, d, hunks
}

// TestExtendMismatchingHeaderDoesNotExtend: [canary] a hunk whose header
// start line does not match the base file must not be extended (§3.2).
func TestExtendMismatchingHeaderDoesNotExtend(t *testing.T) {
	fp, d, hunks := loadCase(t, "mismatch_header")
	ext := ExtendFile(fp, hunks, d)
	if len(ext) != 1 {
		t.Fatalf("got %d hunks", len(ext))
	}
	h := ext[0]
	if h.OldStart != 14 || h.OldLen != 7 || h.NewStart != 14 || h.NewLen != 7 {
		t.Errorf("mismatching header was extended: -%d,%d +%d,%d", h.OldStart, h.OldLen, h.NewStart, h.NewLen)
	}
	if !slices.Equal(h.Lines, hunks[0].Lines) {
		t.Errorf("mismatching hunk gained lines: %d -> %d", len(hunks[0].Lines), len(h.Lines))
	}

	// Control: the same hunk with its correct header is extended.
	fixed := strings.Replace(fp.Patch, "@@ -14,7 +14,7 @@", "@@ -12,7 +12,7 @@", 1)
	good, err := ParseHunks(fixed)
	if err != nil {
		t.Fatal(err)
	}
	if g := ExtendFile(fp, good, d)[0]; g.OldStart != 7 || len(g.Lines) != len(good[0].Lines)+6 {
		t.Errorf("control hunk not extended as expected: start %d, %d lines", g.OldStart, len(g.Lines))
	}
}

// TestExtendNeverReadsPastEOF: [canary] the extended old range never reaches
// past the end of the base file, and no after-context is invented.
func TestExtendNeverReadsPastEOF(t *testing.T) {
	for _, name := range []string{"eof", "max_context", "no_newline_eof", "ordinary", "multi_hunk", "crlf"} {
		t.Run(name, func(t *testing.T) {
			fp, _, hunks := loadCase(t, name)
			n := len(pySplitLines(*fp.BaseContent))
			for _, after := range []int{1, 3, 10} {
				ext := Extend(hunks, fp.BaseContent, fp.HeadContent, 10, after)
				for i, h := range ext {
					if end := h.OldStart - 1 + h.OldLen; end > n {
						t.Errorf("after=%d hunk %d: old range -%d,%d ends past EOF (%d lines)", after, i, h.OldStart, h.OldLen, n)
					}
					oldSide := 0
					for _, l := range h.Lines {
						if l.Op == ' ' || l.Op == '-' {
							oldSide++
						}
					}
					if oldSide > n {
						t.Errorf("after=%d hunk %d: %d old-side lines in a %d-line file", after, i, oldSide, n)
					}
				}
			}
		})
	}
}

func TestExtendReturnsUnchanged(t *testing.T) {
	fp, d, hunks := loadCase(t, "ordinary")
	cases := map[string][]Hunk{
		"nil head":   Extend(hunks, fp.BaseContent, nil, 5, 1),
		"nil base":   Extend(hunks, nil, fp.HeadContent, 5, 1),
		"empty base": Extend(hunks, ptr(""), fp.HeadContent, 5, 1),
		"zero extra": Extend(hunks, fp.BaseContent, fp.HeadContent, 0, 0),
		"negative":   Extend(hunks, fp.BaseContent, fp.HeadContent, -3, -1),
		"skip ext": ExtendFile(fp, hunks, config.Diff{
			ExtraLinesBefore: 5, ExtraLinesAfter: 1, SkipExtendExtensions: []string{".py"},
		}),
	}
	for name, got := range cases {
		if patchText(got) != fp.Patch {
			t.Errorf("%s: patch changed", name)
		}
	}
	// The result never aliases the input.
	ext := ExtendFile(fp, hunks, d)
	ext[0].Lines[len(ext[0].Lines)-1].Text = "mutated"
	same := Extend(hunks, nil, nil, 5, 1)
	same[0].Lines[0].Text = "mutated"
	if patchText(hunks) != fp.Patch {
		t.Error("mutating a result changed the input hunks")
	}
}

func TestExtendClampsToMaxExtraLines(t *testing.T) {
	fp, _, hunks := loadCase(t, "ordinary")
	a := patchText(Extend(hunks, fp.BaseContent, fp.HeadContent, 50, 99))
	b := patchText(Extend(hunks, fp.BaseContent, fp.HeadContent, 10, 10))
	if a != b {
		t.Error("before/after above 10 are not capped to 10")
	}
}

// A Python line inside a patch line that starts with "@@" would be a hunk
// header upstream; such a patch is returned unextended (documented parity gap).
func TestExtendLeavesAmbiguousPatchUnextended(t *testing.T) {
	base := "a\nb\nc\nd\ne\n"
	p := "@@ -3,2 +3,2 @@\n c\f@@ -1 +1 @@\n-d\n+D\n"
	hunks, err := ParseHunks(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := patchText(Extend(hunks, &base, &base, 5, 1)); got != p {
		t.Errorf("ambiguous patch was extended: %q", got)
	}
	if got := patchText(OmitDeletionHunks(hunks)); got != p {
		t.Errorf("ambiguous patch was compacted: %q", got)
	}
}

func TestHandleDeletionsDeletedFile(t *testing.T) {
	fp, _, hunks := loadCase(t, "deleted")
	kept, deleted := HandleDeletions(fp.Type, nil, hunks)
	if !deleted || kept != nil {
		t.Errorf("deleted file: kept=%v deleted=%v", kept, deleted)
	}
	kept, deleted = HandleDeletions(provider.ChangeModified, nil, hunks)
	if deleted || patchText(kept) != fp.Patch {
		t.Error("a modified file with only deletions must keep its patch")
	}
}

func TestOmitDeletionHunksDropsDeletionOnlyHunks(t *testing.T) {
	_, _, hunks := loadCase(t, "multi_hunk")
	got := OmitDeletionHunks(hunks)
	if len(hunks) != 3 || len(got) != 2 {
		t.Fatalf("got %d of %d hunks, want 2 of 3", len(got), len(hunks))
	}
	for _, h := range got {
		if !slices.ContainsFunc(h.Lines, func(l Line) bool { return l.Op == '+' }) {
			t.Errorf("kept a hunk without additions: %+v", h)
		}
	}
}

var lineNumberPrefix = regexp.MustCompile(`(?m)^\d+ `)

// The unnumbered decoupled view has no upstream golden; it must equal the
// numbered oracle golden with the line-number prefixes removed.
func TestRenderDecoupledUnnumbered(t *testing.T) {
	for _, name := range []string{"ordinary", "multi_hunk", "additions_only", "no_newline_eof"} {
		fp, d, hunks := loadCase(t, name)
		want, _ := readOptional(t, filepath.Join("testdata/upstream", name, "out.numbered.txt"))
		got := RenderDecoupled(NewFile(fp, ExtendFile(fp, hunks, d)), false)
		if got != lineNumberPrefix.ReplaceAllString(*want, "") {
			t.Errorf("%s: unnumbered render differs\n got: %q", name, got)
		}
	}
}

func TestRenderEmptyAndUnreadable(t *testing.T) {
	f := File{Path: "a.go", Type: provider.ChangeModified, HeadStatus: provider.ContentFull}
	if RenderPlain(f) != "" || RenderDecoupled(f, true) != "" || RenderCompressed(f, false) != "" {
		t.Error("a file without hunks must render as empty")
	}
	f.HeadStatus = provider.ContentFetchFailed
	if got := RenderPlain(f); !strings.Contains(got, "flag it for manual review") || !strings.HasPrefix(got, "\n\n## File: 'a.go'\n\n") {
		t.Errorf("unreadable notice missing: %q", got)
	}
}
