package review

import (
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review/anchor"
)

func TestOwnFindingsOf(t *testing.T) {
	a, b, c := FingerprintMarker("aaaaaaaaaaaa"), FingerprintMarker("bbbbbbbbbbbb"), FingerprintMarker("cccccccccccc")
	withURL := func(it provider.CommentItem, url string) provider.CommentItem {
		it.URL = url
		return it
	}
	ranged := inlineThread("a.go", 19, false, withURL(citem(botUser, 0, "t\n\n"+a), "https://github.example.com/c/1"))
	ranged.StartLine = 14
	outdated := inlineThread("b.go", 7, true, withURL(citem(botUser, 1, "t\n\n"+c), "https://github.example.com/c/4"))
	outdated.Outdated = true
	threads := []provider.Thread{
		ranged,
		// Gitea groups the review comments of one line: ours may be a reply.
		inlineThread("a.go", 2, false, citem(alice, 0, "human"), withURL(citem(botUser, 1, "t\n\n"+b), "https://github.example.com/c/3")),
		// Not ours, and not an inline thread.
		inlineThread("a.go", 3, false, citem(mallory, 0, "t\n\n"+c)),
		general(citem(botUser, 0, "t\n\n"+c)),
		// Ours without a finding marker.
		inlineThread("a.go", 4, false, citem(botUser, 0, "no marker")),
		outdated,
	}
	got := ownFindingsOf(threads, botUser)
	type row struct {
		fp, path, url string
		start, line   int
		outdated      bool
	}
	want := []row{
		{"aaaaaaaaaaaa", "a.go", "https://github.example.com/c/1", 14, 19, false},
		{"bbbbbbbbbbbb", "a.go", "https://github.example.com/c/3", 2, 2, false},
		{"cccccccccccc", "b.go", "https://github.example.com/c/4", 7, 7, true},
	}
	if len(got) != len(want) {
		t.Fatalf("own findings = %+v", got)
	}
	for i := range want {
		g := got[i]
		if (row{g.fp, g.path, g.url, g.start, g.line, g.outdated}) != want[i] || g.c == nil {
			t.Errorf("own finding %d = %+v, want %+v", i, g, want[i])
		}
	}
}

// TestOverlaps is the X-29 place rule: same path, inclusive ranges sharing
// a line, outdated never, resolved still, a line 0 (base side) never.
func TestOverlaps(t *testing.T) {
	at := func(line, end int) anchor.Anchor { return anchor.Anchor{Path: "src/app.go", Line: line, EndLine: end} }
	own := func(start, line int) ownFinding { return ownFinding{path: "src/app.go", start: start, line: line} }
	for name, tc := range map[string]struct {
		a    anchor.Anchor
		o    ownFinding
		want bool
	}{
		"same line":                {at(14, 0), own(14, 14), true},
		"next line":                {at(15, 0), own(14, 14), false},
		"line before":              {at(13, 0), own(14, 14), false},
		"range over first line":    {at(14, 19), own(14, 14), true},
		"range over last line":     {at(14, 19), own(19, 19), true},
		"range inside":             {at(14, 19), own(16, 16), true},
		"range just after":         {at(14, 19), own(20, 20), false},
		"range just before":        {at(14, 19), own(13, 13), false},
		"end line before line":     {at(14, 3), own(14, 14), true},
		"in a comment range":       {at(16, 0), own(14, 19), true},
		"at a comment range start": {at(14, 0), own(14, 19), true},
		"before a comment range":   {at(13, 0), own(14, 19), false},
		"ranges touching":          {at(10, 14), own(14, 19), true},
		"ranges apart":             {at(10, 13), own(14, 19), false},
		"other path":               {anchor.Anchor{Path: "src/other.go", Line: 14}, own(14, 14), false},
		"renamed file, old path": {anchor.Anchor{Path: "src/new.go", OldPath: "src/app.go", Line: 14},
			own(14, 14), false},
		"base side (line 0)": {at(1, 0), own(0, 0), false},
		"outdated":           {at(14, 0), ownFinding{path: "src/app.go", start: 14, line: 14, outdated: true}, false},
	} {
		if got := tc.o.overlaps(tc.a); got != tc.want {
			t.Errorf("%s: overlaps = %v, want %v", name, got, tc.want)
		}
	}
}

// TestDuplicateOf: the place wins over the fingerprint; among several own
// comments on the place, the closest first line, then the oldest; a
// fingerprint match counts anywhere, outdated too, and picks the oldest.
func TestDuplicateOf(t *testing.T) {
	item := func(id string, minutes int) *provider.CommentItem {
		it := citem(botUser, minutes, "x")
		it.ID = id
		return &it
	}
	own := []ownFinding{
		{fp: "aaaaaaaaaaaa", path: "f.go", start: 19, line: 19, url: "u19", c: item("1", 0)},
		{fp: "bbbbbbbbbbbb", path: "f.go", start: 14, line: 14, url: "u14", c: item("2", 0)},
		{fp: "cccccccccccc", path: "f.go", start: 30, line: 30, url: "u30-new", c: item("4", 5)},
		{fp: "cccccccccccc", path: "f.go", start: 30, line: 30, url: "u30-old", c: item("3", 1)},
		{fp: "dddddddddddd", path: "f.go", start: 50, line: 50, outdated: true, url: "u50", c: item("5", 0)},
		{fp: "dddddddddddd", path: "g.go", start: 1, line: 1, url: "g1", c: item("6", 9)},
	}
	for name, tc := range map[string]struct {
		a    anchor.Anchor
		fp   string
		want string
	}{
		"range over 14 and 19":       {anchor.Anchor{Path: "f.go", Line: 14, EndLine: 19}, "ffffffffffff", "u14"},
		"single line 19":             {anchor.Anchor{Path: "f.go", Line: 19}, "ffffffffffff", "u19"},
		"place wins over fp":         {anchor.Anchor{Path: "f.go", Line: 19}, "bbbbbbbbbbbb", "u19"},
		"equal distance, the oldest": {anchor.Anchor{Path: "f.go", Line: 30}, "ffffffffffff", "u30-old"},
		"outdated: fp still counts":  {anchor.Anchor{Path: "f.go", Line: 50}, "dddddddddddd", "u50"},
		"outdated: no place match":   {anchor.Anchor{Path: "f.go", Line: 50}, "ffffffffffff", ""},
		"nothing at the place":       {anchor.Anchor{Path: "f.go", Line: 16}, "ffffffffffff", ""},
	} {
		got := ""
		if d := duplicateOf(own, tc.a, tc.fp); d != nil {
			got = d.url
		}
		if got != tc.want {
			t.Errorf("%s: duplicate %q, want %q", name, got, tc.want)
		}
	}
}
