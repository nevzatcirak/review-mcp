package llmrun

import (
	"slices"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// TestPathSpans: the path pattern of V-13b §6 item 1. Only a code span whose
// whole content looks like a repository path is taken: at least one '/', a
// last segment with an extension of 1 to 8 letters or digits, only
// A–Z a–z 0–9 / . _ -; an optional :line or :line-line suffix is ignored, a
// leading R1: to R4: label is kept apart; a span in a fenced code block is
// not one.
func TestPathSpans(t *testing.T) {
	cases := []struct {
		name, text string
		want       []PathSpan
	}{
		{"a path", "see `src/api/header.ts`", []PathSpan{{Path: "src/api/header.ts"}}},
		{"two paths in order", "`a/b.go` and `c/d.py`", []PathSpan{{Path: "a/b.go"}, {Path: "c/d.py"}}},
		{"a line suffix", "`src/a.go:12`", []PathSpan{{Path: "src/a.go"}}},
		{"a line range suffix", "`src/a.go:12-20`", []PathSpan{{Path: "src/a.go"}}},
		{"a label", "`R1:web/src/ui/header.ts:7`", []PathSpan{{Label: "R1", Path: "web/src/ui/header.ts"}}},
		{"the last label", "`R4:x/y.proto`", []PathSpan{{Label: "R4", Path: "x/y.proto"}}},
		{"a dotted directory and a dash", "`.github/work-flows/ci.yml`", []PathSpan{{Path: ".github/work-flows/ci.yml"}}},
		{"an eight-character extension", "`a/b.abcdefgh`", []PathSpan{{Path: "a/b.abcdefgh"}}},
		{"a hidden file", "`config/.env`", []PathSpan{{Path: "config/.env"}}},
		{"a double-backtick span", "``src/a.go``", []PathSpan{{Path: "src/a.go"}}},
		{"spaces stripped as CommonMark does", "`` src/a.go ``", []PathSpan{{Path: "src/a.go"}}},
		{"after a fence closes", "```\n`x/in.go`\n```\nthen `x/out.go`", []PathSpan{{Path: "x/out.go"}}},
		{"no slash", "`header.ts`", nil},
		{"no extension", "`src/Makefile`", nil},
		{"a directory", "`src/api/`", nil},
		{"a nine-character extension", "`a/b.abcdefghi`", nil},
		{"an extension with a dash", "`a/b.t-s`", nil},
		{"a space", "`src/a b.go`", nil},
		{"a URL", "`https://git.example/a/b.go`", nil},
		{"a call", "`pkg.Find(a/b.go)`", nil},
		{"a label out of range", "`R5:a/b.go`", nil},
		{"a label of another form", "`r1:a/b.go`", nil},
		{"a bad line suffix", "`a/b.go:x`", nil},
		{"a line suffix without a number", "`a/b.go:`", nil},
		{"not a code span", "src/a.go and 'src/b.go'", nil},
		{"an unclosed span", "`src/a.go", nil},
		{"an escaped backtick", "\\`src/a.go`", nil},
		{"a backtick fence", "```go\n// `src/a.go`\n```", nil},
		{"a tilde fence", "~~~\n`src/a.go`\n~~~", nil},
		{"an indented fence", "   ```\n`src/a.go`\n   ```", nil},
		{"a longer closing fence", "```\n`src/a.go`\n`````", nil},
		{"an unclosed fence", "text\n```\n`src/a.go`", nil},
		{"a shorter line does not close", "````\n```\n`src/a.go`\n````", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PathSpans(c.text); !slices.Equal(got, c.want) {
				t.Errorf("PathSpans(%q) = %+v, want %+v", c.text, got, c.want)
			}
		})
	}
}

// TestSeenUnseen: a path without a label is checked against every set, a
// labelled one against its related item only; each unseen path is given
// once, in order, without its label and line suffix.
func TestSeenUnseen(t *testing.T) {
	var s Seen
	s.AddDiff(&provider.Diff{
		Files:   []provider.FilePatch{{Path: "api/openapi.yaml"}, {Path: "src/new.go", OldPath: "src/old.go"}},
		Skipped: []provider.SkippedFile{{Path: "vendor/x/lib.go"}},
	})
	s.Add("cmd/main.go")
	s.AddRelated("R1", "web/src/ui/header.ts")
	s.AddRelated("R2", "proto/user.proto")
	cases := []struct {
		text string
		want []string
	}{
		{"`api/openapi.yaml`, `src/new.go`, `src/old.go:3`, `vendor/x/lib.go` and `cmd/main.go`", nil},
		{"`web/src/ui/header.ts` and `R1:web/src/ui/header.ts:7`", nil},
		{"`R2:proto/user.proto`", nil},
		{"`web/src/api/header.ts`", []string{"web/src/api/header.ts"}},
		{"`R2:web/src/ui/header.ts`", []string{"web/src/ui/header.ts"}},
		{"`R1:api/openapi.yaml`", []string{"api/openapi.yaml"}},
		{"`R3:proto/user.proto`", []string{"proto/user.proto"}},
		{"`b/x.go:1`, `a/y.go`, `R1:b/x.go:9`", []string{"b/x.go", "a/y.go"}},
		{"nothing here", nil},
	}
	for _, c := range cases {
		if got := s.Unseen(c.text); !slices.Equal(got, c.want) {
			t.Errorf("Unseen(%q) = %q, want %q", c.text, got, c.want)
		}
	}
	if got := s.Unseen("`a/x.go`", "`a/x.go` `b/y.go`"); !slices.Equal(got, []string{"a/x.go", "b/y.go"}) {
		t.Errorf("Unseen over two texts = %q", got)
	}
	var empty Seen
	if got := empty.Unseen("`a/b.go` and `R1:c/d.go`"); !slices.Equal(got, []string{"a/b.go", "c/d.go"}) {
		t.Errorf("Unseen of an empty set = %q", got)
	}
}

// TestUnseenLine: the mark line's grammar for one path and for many, at
// most five named, then "and N more".
func TestUnseenLine(t *testing.T) {
	cases := []struct {
		paths []string
		want  string
	}{
		{nil, ""},
		{[]string{"a/b.go"}, "Path not seen in this review: `a/b.go`"},
		{[]string{"a/b.go", "c/d.ts"}, "Paths not seen in this review: `a/b.go`, `c/d.ts`"},
		{[]string{"a/1.go", "a/2.go", "a/3.go", "a/4.go", "a/5.go"},
			"Paths not seen in this review: `a/1.go`, `a/2.go`, `a/3.go`, `a/4.go`, `a/5.go`"},
		{[]string{"a/1.go", "a/2.go", "a/3.go", "a/4.go", "a/5.go", "a/6.go"},
			"Paths not seen in this review: `a/1.go`, `a/2.go`, `a/3.go`, `a/4.go`, `a/5.go` and 1 more"},
		{[]string{"a/1.go", "a/2.go", "a/3.go", "a/4.go", "a/5.go", "a/6.go", "a/7.go"},
			"Paths not seen in this review: `a/1.go`, `a/2.go`, `a/3.go`, `a/4.go`, `a/5.go` and 2 more"},
	}
	for _, c := range cases {
		if got := UnseenLine(c.paths); got != c.want {
			t.Errorf("UnseenLine(%q) = %q, want %q", c.paths, got, c.want)
		}
	}
}

// TestWithUnseen: the mark is one last line after a blank line; a text
// without unseen paths is returned byte for byte.
func TestWithUnseen(t *testing.T) {
	text := "It reads `a/b.go`.\n"
	if got := WithUnseen(text, nil); got != text {
		t.Errorf("WithUnseen without paths changed the text: %q", got)
	}
	if got, want := WithUnseen(text, []string{"a/b.go"}), "It reads `a/b.go`.\n\nPath not seen in this review: `a/b.go`"; got != want {
		t.Errorf("WithUnseen = %q, want %q", got, want)
	}
}
