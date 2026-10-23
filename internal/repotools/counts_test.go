package repotools

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// TestResultCountsAreInWords: every count in a tool result reads "1 file was",
// "1 match", "1 entry", "1 line" for one and the plural for two.
func TestResultCountsAreInWords(t *testing.T) {
	hit := func(p string) gitctx.Hit { return gitctx.Hit{Path: p, Line: 1, StartLine: 1, Snippet: "x"} }
	for _, tc := range []struct {
		n        int
		skipped  string
		matches  string
		entries  string
		lines    string
		hits     []gitctx.Hit
		dir      []provider.DirEntry
		fileText string
	}{
		{1, "1 file was not searched: it was not fetched.", "1 match", "1 entry", "the file has 1 line",
			[]gitctx.Hit{hit("a.go")}, []provider.DirEntry{{Name: "a.go"}}, "x\n"},
		{2, "2 files were not searched: they were not fetched.", "2 matches", "2 entries", "the file has 2 lines",
			[]gitctx.Hit{hit("a.go"), hit("b.go")}, []provider.DirEntry{{Name: "a.go"}, {Name: "b.go"}}, "x\ny\n"},
	} {
		f := &fakeHead{
			hits:  gitctx.TextResult{Hits: tc.hits, SkippedBlobs: tc.n},
			dirs:  map[string][]provider.DirEntry{"d": tc.dir},
			files: map[string]string{"f.txt": tc.fileText},
		}
		b := newBox(f, 40000)
		got, _ := one(t, b, call("1", Search, `{"text":"xx"}`))
		if !strings.Contains(got, ": "+tc.matches+"\n") || !strings.Contains(got, "\n\n"+tc.skipped+"\n") {
			t.Errorf("%d: search = %q, want %q and %q", tc.n, got, tc.matches, tc.skipped)
		}
		got, _ = one(t, b, call("2", ListDir, `{"path":"d"}`))
		if !strings.Contains(got, ": "+tc.entries+"\n") {
			t.Errorf("%d: list_dir = %q, want %q", tc.n, got, tc.entries)
		}
		got, _ = one(t, b, call("3", ReadFile, `{"path":"f.txt","start_line":9}`))
		if !strings.Contains(got, tc.lines) || strings.HasSuffix(got, "lines") != (tc.n != 1) {
			t.Errorf("%d: read_file = %q, want %q", tc.n, got, tc.lines)
		}
	}
}

// TestInstructionRounds: the rounds the model may use are in words.
func TestInstructionRounds(t *testing.T) {
	for n, want := range map[int]string{1: "at most 1 round of tool calls", 2: "at most 2 rounds of tool calls"} {
		if got := instruction(n, "p", "f", nil); !strings.Contains(got, want) {
			t.Errorf("rounds %d: %q lacks %q", n, got, want)
		}
	}
}
