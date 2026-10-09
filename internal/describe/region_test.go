package describe

import (
	"errors"
	"strings"
	"testing"
)

const regionBody = "## PR Description\n\n- Raise the retry count"

func region(eol string) string {
	return strings.ReplaceAll(RegionStart+"\n"+regionBody+"\n"+RegionEnd, "\n", eol)
}

// TestApplyRegionAppend: the first run appends the region after the author's
// text, one blank line apart, and never changes the author's bytes.
func TestApplyRegionAppend(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"empty", "", region("\n")},
		{"no newline", "Author text.", "Author text.\n\n" + region("\n")},
		{"one newline", "Author text.\n", "Author text.\n\n" + region("\n")},
		{"blank line already", "Author text.\n\n", "Author text.\n\n" + region("\n")},
		{"two blank lines stay", "Author text.\n\n\n", "Author text.\n\n\n" + region("\n")},
		{"crlf", "Line one.\r\nLine two.", "Line one.\r\nLine two.\r\n\r\n" + region("\r\n")},
		{"crlf one newline", "Line one.\r\n", "Line one.\r\n\r\n" + region("\r\n")},
		{"trailing spaces kept", "Text with trailing spaces   \n", "Text with trailing spaces   \n\n" + region("\n")},
	} {
		got, replaced, err := ApplyRegion(c.text, regionBody)
		if err != nil || replaced || got != c.want {
			t.Errorf("%s: got %q replaced=%v err=%v\nwant %q", c.name, got, replaced, err, c.want)
		}
		if !strings.HasPrefix(got, c.text) {
			t.Errorf("%s: the author's text is not a byte-identical prefix", c.name)
		}
	}
}

// authorText exercises the bytes that must survive a replacement: CRLF line
// endings, trailing spaces, an emoji, a fenced block and a lone CR.
const authorBefore = "# Title \U0001F680  \r\n\r\nSome text with trailing spaces   \r\n\r\n```go\r\nfunc main() {\r\n\tprintln(\"hi\")   \r\n}\r\n```\r\n\r\nlone\rcarriage\r\n\r\n"
const authorAfter = "\r\n\r\nFooter \U0001F44D\t \r\n"

// TestApplyRegionReplaceKeepsOuterBytes [canary]: only the region changes;
// the text before and after it is byte-identical, with CRLF line endings,
// trailing spaces, an emoji and a fenced block. Rewriting the whole
// description makes this test fail.
func TestApplyRegionReplaceKeepsOuterBytes(t *testing.T) {
	old := RegionStart + "\r\nold body\r\n- old bullet   \r\n" + RegionEnd + "  "
	text := authorBefore + old + authorAfter
	got, replaced, err := ApplyRegion(text, regionBody)
	if err != nil || !replaced {
		t.Fatalf("replaced=%v err=%v", replaced, err)
	}
	if !strings.HasPrefix(got, authorBefore) {
		t.Errorf("text before the region changed:\n%q", got)
	}
	if !strings.HasSuffix(got, authorAfter) {
		t.Errorf("text after the region changed:\n%q", got)
	}
	want := authorBefore + region("\r\n") + authorAfter
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "old body") {
		t.Errorf("the old region is still there: %q", got)
	}
}

// TestApplyRegionIdempotent: running again on the result with the same body
// returns the same bytes, from the first run and from every replacement.
func TestApplyRegionIdempotent(t *testing.T) {
	for _, text := range []string{"", "Plain.", authorBefore, "A\r\nB\r\n", "x\n\n\n"} {
		once, _, err := ApplyRegion(text, regionBody)
		if err != nil {
			t.Fatal(err)
		}
		twice, replaced, err := ApplyRegion(once, regionBody)
		if err != nil || !replaced || twice != once {
			t.Errorf("%q: second run changed the text (replaced=%v err=%v):\n%q\n%q", text, replaced, err, once, twice)
		}
	}
}

// TestApplyRegionMarkerLines: what counts as a marker line. CRLF and
// surrounding spaces or tabs do not hide it; anything else on the line does.
func TestApplyRegionMarkerLines(t *testing.T) {
	for _, c := range []struct {
		name, start, end string
		replaced         bool
	}{
		{"exact", RegionStart, RegionEnd, true},
		{"padded", " \t" + RegionStart + "  ", "\t" + RegionEnd + " \t", true},
	} {
		for _, eol := range []string{"\n", "\r\n"} {
			text := "before" + eol + c.start + eol + "old" + eol + c.end + eol + "after"
			got, replaced, err := ApplyRegion(text, regionBody)
			if err != nil || replaced != c.replaced || !strings.HasPrefix(got, "before"+eol) || !strings.HasSuffix(got, eol+"after") ||
				strings.Contains(got, "old") {
				t.Errorf("%s %q: got %q replaced=%v err=%v", c.name, eol, got, replaced, err)
			}
		}
	}
	// Not markers: another case, text after the marker, an extra word. The
	// text is treated as having no region and the region is appended.
	for _, line := range []string{
		strings.ToUpper(RegionStart), RegionStart + " x", "x " + RegionEnd, "[//]: # (review-mcp:describe:begin)",
		"<!-- " + RegionStart + " -->", "[//]: # (review-mcp:overview:v1)",
	} {
		text := "before\n" + line + "\nafter"
		got, replaced, err := ApplyRegion(text, regionBody)
		if err != nil || replaced || !strings.HasPrefix(got, text+"\n\n"+RegionStart) {
			t.Errorf("%q: got %q replaced=%v err=%v", line, got, replaced, err)
		}
	}
}

// TestApplyRegionDamaged: two or more start markers, an end before a start,
// a missing end (or start) and a second end are refused, with no text.
func TestApplyRegionDamaged(t *testing.T) {
	s, e := RegionStart, RegionEnd
	for name, text := range map[string]string{
		"two starts":              "a\n" + s + "\nx\n" + s + "\ny\n" + e + "\nb",
		"two starts two ends":     s + "\n" + e + "\n" + s + "\n" + e,
		"end before start":        e + "\nx\n" + s,
		"end before start, crlf":  "a\r\n" + e + "\r\nx\r\n" + s + "\r\nb",
		"start without end":       "a\n" + s + "\nx",
		"end without start":       "a\n" + e + "\nx",
		"second end":              s + "\nx\n" + e + "\ny\n" + e,
		"start in text, end gone": "x\n" + s + "\n",
	} {
		got, replaced, err := ApplyRegion(text, regionBody)
		if !errors.Is(err, errDamagedRegion) || got != "" || replaced {
			t.Errorf("%s: got %q replaced=%v err=%v, want errDamagedRegion", name, got, replaced, err)
		}
	}
	if errDamagedRegion.Error() != "The pull request description contains a damaged review-mcp region; fix or remove it and run again." {
		t.Errorf("sentence = %q", errDamagedRegion.Error())
	}
}

// Two markers glued on one line are neither a marker line, so it is no
// region at all and the text gets a region appended.
func TestApplyRegionGluedMarkersAreText(t *testing.T) {
	text := RegionStart + RegionEnd
	got, replaced, err := ApplyRegion(text, regionBody)
	if err != nil || replaced || !strings.HasPrefix(got, text+"\n\n") {
		t.Errorf("got %q replaced=%v err=%v", got, replaced, err)
	}
}
