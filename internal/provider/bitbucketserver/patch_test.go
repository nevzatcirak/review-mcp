package bitbucketserver

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func numberedLines(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		sb.WriteString("line" + strconv.Itoa(i) + "\n")
	}
	return sb.String()
}

// The golden patches below were cross-checked against Python's
// difflib.unified_diff (3 context lines, lines split after "\n", headers
// removed) for the same inputs.
func TestMakePatchGolden(t *testing.T) {
	twoHunksBase, twoHunksHead := "", ""
	for i := 1; i <= 30; i++ {
		l := "l" + strconv.Itoa(i) + "\n"
		twoHunksBase += l
		if i == 2 || i == 29 {
			l = "L" + strconv.Itoa(i) + "\n"
		}
		twoHunksHead += l
	}
	mid := numberedLines(20)
	cases := []struct {
		name       string
		base, head string
		want       string
		ok         bool
		add, del   int
	}{
		{"added file (empty base)", "", "alpha\nbeta\n", "@@ -0,0 +1,2 @@\n+alpha\n+beta\n", true, 2, 0},
		{"deleted file (empty head)", "alpha\nbeta\n", "", "@@ -1,2 +0,0 @@\n-alpha\n-beta\n", true, 0, 2},
		{"modification in the middle", mid, strings.Replace(mid, "line10\n", "line10 changed\n", 1),
			"@@ -7,7 +7,7 @@\n line7\n line8\n line9\n-line10\n+line10 changed\n line11\n line12\n line13\n", true, 1, 1},
		{"CRLF file", "a\r\nb\r\nc\r\n", "a\r\nB\r\nc\r\n", "@@ -1,3 +1,3 @@\n a\r\n-b\r\n+B\r\n c\r\n", true, 1, 1},
		{"no trailing newline on the base", "a\nb", "a\nb\nc\n", "@@ -1,2 +1,3 @@\n a\n b\n+c\n", true, 1, 0},
		{"no trailing newline on the head", "a\nb\n", "a\nc", "@@ -1,2 +1,2 @@\n a\n-b\n+c\n", true, 1, 1},
		{"single-line ranges", "x\n", "y\n", "@@ -1 +1 @@\n-x\n+y\n", true, 1, 1},
		{"two hunks", twoHunksBase, twoHunksHead,
			"@@ -1,5 +1,5 @@\n l1\n-l2\n+L2\n l3\n l4\n l5\n@@ -26,5 +26,5 @@\n l26\n l27\n l28\n-l29\n+L29\n l30\n", true, 2, 2},
		{"content that looks like headers", "-- c\nkeep\n", "++ c\nkeep\n", "@@ -1,2 +1,2 @@\n--- c\n+++ c\n keep\n", true, 1, 1},
		{"identical", "a\nb\n", "a\nb\n", "", false, 0, 0},
		{"identical after newline normalization", "a\nb", "a\nb\n", "", false, 0, 0},
		{"identical after normalization, other side", "a\nb\n", "a\nb", "", false, 0, 0},
		{"both empty", "", "", "", false, 0, 0},
		{"only a newline vs empty", "", "\n", "@@ -0,0 +1 @@\n+\n", true, 1, 0},
	}
	for _, c := range cases {
		got, ok, err := makePatch(c.base, c.head)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if ok != c.ok || got != c.want {
			t.Errorf("%s:\n got ok=%v %q\nwant ok=%v %q", c.name, ok, got, c.ok, c.want)
			continue
		}
		if !ok {
			continue
		}
		if !strings.HasPrefix(got, "@@") || strings.Contains(got, "\n--- a") || strings.Contains(got, "No newline") {
			t.Errorf("%s: patch is not hunk-only: %q", c.name, got)
		}
		if a, d := countChanges(got); a != c.add || d != c.del {
			t.Errorf("%s: counts = +%d -%d, want +%d -%d", c.name, a, d, c.add, c.del)
		}
	}
}

// difflib.SplitLines appends "\n" to its last element, which for text that
// already ends in "\n" invents an extra empty line. splitLines must not.
func TestSplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a\n", []string{"a\n"}},
		{"a", []string{"a"}},
		{"a\nb\n", []string{"a\n", "b\n"}},
		{"a\n\n", []string{"a\n", "\n"}},
		{"\n", []string{"\n"}},
		{"a\r\nb\r\n", []string{"a\r\n", "b\r\n"}},
		{"a\rb\n", []string{"a\rb\n"}}, // a lone CR is not a line break
	}
	for _, c := range cases {
		if got := splitLines(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitLines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMakePatchLargeFileAutoJunkParity(t *testing.T) {
	// 300 lines (above go-difflib's 200-line autojunk threshold, as in
	// Python) with a repeated line: the patch must still apply cleanly, i.e.
	// the counts must match the real difference.
	var b, h strings.Builder
	for i := 0; i < 300; i++ {
		l := "common\n"
		if i%7 == 0 {
			l = "row" + strconv.Itoa(i) + "\n"
		}
		b.WriteString(l)
		if i == 150 {
			l = "changed\n"
		}
		h.WriteString(l)
	}
	p, ok, err := makePatch(b.String(), h.String())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if a, d := countChanges(p); a != 1 || d != 1 {
		t.Fatalf("counts = +%d -%d, want +1 -1\n%s", a, d, p)
	}
}
