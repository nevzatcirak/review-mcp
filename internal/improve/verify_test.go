package improve

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// headFile is a file whose complete head content was fetched.
func headFile(lines ...string) *provider.FilePatch {
	c := strings.Join(lines, "\n") + "\n"
	return &provider.FilePatch{Path: "a.go", Type: provider.ChangeModified, HeadStatus: provider.ContentFull, HeadContent: &c}
}

// rng is a self-review range; rng(0, 0) is no range.
func rng(start, end int) (*int, *int) {
	if start == 0 {
		return nil, nil
	}
	return &start, &end
}

// verdictString is "ok start-end [corrected]" or the reason.
func verdictString(v verdict) string {
	if !v.ok {
		return v.reason
	}
	s := rangeString(v.start, v.end)
	if v.corrected {
		s += " corrected"
	}
	return s
}

// rangeString is "ok SS-EE" with two-digit line numbers.
func rangeString(start, end int) string { return fmt.Sprintf("ok %02d-%02d", start, end) }

// goFile is a small Go file; "return nil" is at lines 4 and 9.
var goFile = []string{
	"package a",           // 1
	"",                    // 2
	"func f() error {",    // 3
	"\treturn nil",        // 4
	"}",                   // 5
	"",                    // 6
	"func g() error {",    // 7
	"\tx := compute()",    // 8
	"\treturn nil",        // 9
	"}",                   // 10
	"",                    // 11
	"func h(v int) int {", // 12
	"\tif v > 0 {",        // 13
	"\t\treturn v * 2",    // 14
	"\t}",                 // 15
	"\treturn 0",          // 16
	"}",                   // 17
}

// TestVerifyRangeMismatch [canary target]: a given range inside the file
// whose lines are not the quoted code is not accepted: the code is
// searched for, and when the head file does not have it the suggestion is
// not verified.
func TestVerifyRangeMismatch(t *testing.T) {
	fp := headFile(goFile...)
	for name, tc := range map[string]struct {
		existing   string
		start, end int
		want       string
	}{
		"code not in the file":     {"x := computeAll()", 8, 8, UnverifiedNotFound},
		"range on other code":      {"\tx := compute()\n\treturn nil", 3, 4, "ok 08-09 corrected"},
		"one line short of a span": {"if v > 0 {\n\treturn v * 2\n}", 13, 14, "ok 13-15 corrected"},
	} {
		s, e := rng(tc.start, tc.end)
		if got := verdictString(verify(fp, tc.existing, s, e)); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

// TestVerifyAmbiguous [canary target]: code that is not at the given lines
// (or has no range) and is at two places in the head file is not
// verified; the first match is never taken. At the given lines it is
// verified even though it occurs twice.
func TestVerifyAmbiguous(t *testing.T) {
	fp := headFile(goFile...)
	for name, tc := range map[string]struct {
		start, end int
		want       string
	}{
		"no range":         {0, 0, UnverifiedAmbiguous},
		"wrong range":      {16, 16, UnverifiedAmbiguous},
		"at the 2nd place": {9, 9, "ok 09-09"},
		"at the 1st place": {4, 4, "ok 04-04"},
	} {
		s, e := rng(tc.start, tc.end)
		if got := verdictString(verify(fp, "return nil", s, e)); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

// TestVerify: an exact match; a range off by N corrected; no range located
// by the search; CRLF and indentation differences; a range outside the
// file; the head content unavailable.
func TestVerify(t *testing.T) {
	fp := headFile(goFile...)
	for name, tc := range map[string]struct {
		existing   string
		start, end int
		want       string
	}{
		"exact":                      {"\tx := compute()\n\treturn nil", 8, 9, "ok 08-09"},
		"exact, one line":            {"func h(v int) int {", 12, 12, "ok 12-12"},
		"off by 2 corrected":         {"func h(v int) int {\n\tif v > 0 {", 14, 15, "ok 12-13 corrected"},
		"off by -3 corrected":        {"\treturn v * 2", 11, 11, "ok 14-14 corrected"},
		"no range, located":          {"if v > 0 {\n\treturn v * 2\n}", 0, 0, "ok 13-15"},
		"no range, not found":        {"return v * 3", 0, 0, UnverifiedNotFound},
		"indentation removed":        {"if v > 0 {\n\treturn v * 2\n}\nreturn 0", 13, 16, "ok 13-16"},
		"indentation as spaces":      {"    if v > 0 {\n    \treturn v * 2\n    }", 13, 15, "ok 13-15"},
		"inner indentation differs":  {"if v > 0 {\nreturn v * 2\n}", 13, 15, UnverifiedNotFound},
		"trailing white space":       {"func h(v int) int {   \n\tif v > 0 {\t", 12, 13, "ok 12-13"},
		"CRLF in the quote":          {"func g() error {\r\n\tx := compute()", 7, 8, "ok 07-08"},
		"blank line inside":          {"}\n\nfunc g() error {", 5, 7, "ok 05-07"},
		"range past the end":         {"func h(v int) int {", 30, 31, "ok 12-12 corrected"},
		"range past the end, absent": {"func k() {}", 17, 18, UnverifiedNotFound},
		"range ends past the end":    {"\treturn 0\n}", 16, 18, "ok 16-17 corrected"},
		"start after end":            {"func h(v int) int {", 12, 11, "ok 12-12 corrected"},
		"start zero":                 {"package a", 0, 0, "ok 01-01 corrected"},
	} {
		s, e := rng(tc.start, tc.end)
		if name == "start after end" || name == "start zero" {
			// Ranges the self-review conversion never yields; verify still
			// treats them as not matching.
			a, b := tc.start, tc.end
			s, e = &a, &b
		}
		if got := verdictString(verify(fp, tc.existing, s, e)); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}

	// CRLF in the head file.
	crlf := headFile(strings.Join(goFile, "\r\n"))
	if got := verdictString(verify(crlf, "func g() error {\n\tx := compute()", rngPtr(7), rngPtr(8))); got != "ok 07-08" {
		t.Errorf("CRLF head file: %s", got)
	}

	// The head content unavailable: never compared, whatever the range.
	content := strings.Join(goFile, "\n")
	for name, f := range map[string]*provider.FilePatch{
		"size limit":   {HeadStatus: provider.ContentNotFetchedSizeCap},
		"file limit":   {HeadStatus: provider.ContentNotFetchedFileCap},
		"fetch failed": {HeadStatus: provider.ContentFetchFailed},
		"binary":       {HeadStatus: provider.ContentFull, HeadContent: &content, Binary: true},
		"no content":   {HeadStatus: provider.ContentFull},
		"no file":      nil,
	} {
		if got := verdictString(verify(f, "func h(v int) int {", rngPtr(12), rngPtr(12))); got != UnverifiedHeadUnavailable {
			t.Errorf("%s: %s, want %s", name, got, UnverifiedHeadUnavailable)
		}
	}
}

func rngPtr(n int) *int { return &n }

// patchOnlyFile is a size-limited file: no head content, a patch of two
// hunks. New-side lines: hunk 1 is 3-7 (a context, b added, c context, d
// added, e context; "gone" removed between c and d, so it has no new line),
// hunk 2 is 20-22. Lines 8-19 are in no hunk. Hunk 3 only removes lines, so
// it has no new-side line.
func patchOnlyFile() *provider.FilePatch {
	p := "@@ -3,5 +3,5 @@\n" +
		" \tfirst := 1\n" + // 3
		"+\tsecond := 2\n" + // 4
		" \tthird := 3\n" + // 5
		"-\tgone := 0\n" +
		"+\tfourth := 4\n" + // 6
		" \tfifth := 5\n" + // 7
		"@@ -20,2 +20,3 @@\n" +
		" \tlate := 1\n" + // 20
		"+\tlater := 2\n" + // 21
		" }\n" + // 22
		"@@ -40,2 +39,0 @@\n" +
		"-\tremoved := 1\n" +
		"-\tremoved := 2\n"
	return &provider.FilePatch{Path: "a.go", Type: provider.ChangeModified, HeadStatus: provider.ContentNotFetchedSizeCap, Patch: p}
}

// TestVerifyPatchWalk: without head content a given range is verified only
// when the patch shows every line of it and they equal the existing code;
// the range stays as given and is never corrected.
func TestVerifyPatchWalk(t *testing.T) {
	for name, tc := range map[string]struct {
		existing   string
		start, end int
		want       string
	}{
		"added and context lines":    {"\tsecond := 2\n\tthird := 3", 4, 5, "ok 04-05"},
		"one added line":             {"second := 2", 4, 4, "ok 04-04"},
		"across a removed line":      {"\tthird := 3\n\tfourth := 4", 5, 6, "ok 05-06"},
		"second hunk":                {"\tlater := 2\n}", 21, 22, "ok 21-22"},
		"CRLF and indentation":       {"    second := 2\r\n    third := 3", 4, 5, "ok 04-05"},
		"trailing white space":       {"\tfirst := 1   \n\tsecond := 2\t", 3, 4, "ok 03-04"},
		"wrong range, code in patch": {"\tthird := 3", 4, 4, UnverifiedHeadUnavailable},
		"wrong text":                 {"\tsecond := 22", 4, 4, UnverifiedHeadUnavailable},
		"range one line short":       {"\tsecond := 2\n\tthird := 3", 4, 4, UnverifiedHeadUnavailable},
		"crosses a hunk gap":         {"\tfifth := 5\n\tx\n\tlate := 1", 7, 20, UnverifiedHeadUnavailable},
		"removed-only position":      {"\tremoved := 1", 39, 39, UnverifiedHeadUnavailable},
		"gap lines only":             {"\tx", 10, 10, UnverifiedHeadUnavailable},
		"starts before the patch":    {"\tx\n\tfirst := 1", 2, 3, UnverifiedHeadUnavailable},
		"start zero":                 {"\tfirst := 1", 0, 3, UnverifiedHeadUnavailable},
		"start after end":            {"\tsecond := 2", 5, 4, UnverifiedHeadUnavailable},
		"no range":                   {"\tsecond := 2", 0, 0, UnverifiedHeadUnavailable},
	} {
		s, e := rng(tc.start, tc.end)
		if name == "start zero" || name == "start after end" {
			a, b := tc.start, tc.end
			s, e = &a, &b
		}
		if got := verdictString(verify(patchOnlyFile(), tc.existing, s, e)); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}

	// The removed text is not on the new side, wherever it is asked for.
	if got := verdictString(verify(patchOnlyFile(), "\tgone := 0", rngPtr(6), rngPtr(6))); got != UnverifiedHeadUnavailable {
		t.Errorf("removed text: %s", got)
	}
	// Only one end given.
	if got := verdictString(verify(patchOnlyFile(), "\tsecond := 2", rngPtr(4), nil)); got != UnverifiedHeadUnavailable {
		t.Errorf("start only: %s", got)
	}

	// No patch, a binary file, and a malformed patch.
	noPatch := patchOnlyFile()
	noPatch.Patch = ""
	bin := patchOnlyFile()
	bin.Binary = true
	bad := patchOnlyFile()
	bad.Patch = "not a patch"
	for name, f := range map[string]*provider.FilePatch{"no patch": noPatch, "binary": bin, "malformed patch": bad} {
		if got := verdictString(verify(f, "\tsecond := 2", rngPtr(4), rngPtr(4))); got != UnverifiedHeadUnavailable {
			t.Errorf("%s: %s", name, got)
		}
	}

	// A fetch failure walks the patch like a limit does.
	failed := patchOnlyFile()
	failed.HeadStatus = provider.ContentFetchFailed
	if got := verdictString(verify(failed, "\tsecond := 2", rngPtr(4), rngPtr(4))); got != "ok 04-04" {
		t.Errorf("fetch failed: %s", got)
	}

	// Full head content keeps the WP-2g rule: a wrong range is searched and
	// corrected, even when a patch is present.
	full := headFile(goFile...)
	full.Patch = patchOnlyFile().Patch
	if got := verdictString(verify(full, "\treturn 0", rngPtr(1), rngPtr(1))); got != "ok 16-16 corrected" {
		t.Errorf("full content: %s", got)
	}
}

// TestNormalizeSnippet: trailing white space and a final "\r" go, blank
// lines become empty and do not count for the common indentation, which
// is removed; tabs and spaces are not converted.
func TestNormalizeSnippet(t *testing.T) {
	in := []string{"\t\tif x {  \r", "\t\t\ty()", "  ", "\t\t}"}
	if got, want := normalizeSnippet(in), []string{"if x {", "\ty()", "", "}"}; !slices.Equal(got, want) {
		t.Errorf("normalizeSnippet = %q, want %q", got, want)
	}
	if in[0] != "\t\tif x {  \r" {
		t.Errorf("the input was changed")
	}
	if got := normalizeSnippet([]string{"\tx", "    y"}); !slices.Equal(got, []string{"\tx", "    y"}) {
		t.Errorf("mixed indentation = %q", got)
	}
}

// headSampleFiles are sampleFiles with the complete head content of the two
// reviewed files: src/app.go has the patch's new side at lines 10 to 13.
func headSampleFiles() []provider.FilePatch {
	files := sampleFiles()
	var app []string
	for i := 1; i <= 9; i++ {
		app = append(app, "line "+strconv.Itoa(i))
	}
	app = append(app, "line 10", "retries := 3 // "+diffMarker, "delay := retries * 100", "line 12", "")
	appContent := strings.Join(app, "\n")
	testContent := "package app\r\nfunc TestRetries(t *testing.T) {}\r\n"
	files[0].HeadStatus, files[0].HeadContent = provider.ContentFull, &appContent
	files[1].HeadStatus, files[1].HeadContent = provider.ContentFull, &testContent
	return files
}

// TestVerifiedRun: in a run, the kept suggestions are verified against the
// head file: a correct range stays, a wrong one is corrected with the
// note, a missing one is located; an unverified suggestion stays in the
// result with its reason and its given range.
func TestVerifiedRun(t *testing.T) {
	h := newHarness(map[int][]string{
		0: {suggestionsAnswer(sugDelay, sugName, sugTest)},
		reflectKind(0): {reflectionAnswer(
			fb{number: 1, summary: sugDelay.summary, file: sugDelay.file, start: 12, end: 12, score: 8, why: "w"},
			fb{number: 2, summary: sugName.summary, file: sugName.file, start: 3, end: 3, score: 7, why: "w"},
		)},
	})
	h.prov.files = headSampleFiles()
	h.deps.Config.Improve.MinScore = 0
	res := h.run(t, Args{})
	got := map[string]string{}
	for _, s := range res.Suggestions {
		v := s.UnverifiedReason
		if s.Verified {
			v = rangeString(*s.StartLine, *s.EndLine)
		}
		got[s.Summary] = v
	}
	want := map[string]string{
		"Cap the retry delay":    "ok 12-12", // the self-review's range
		"Name the retry count":   "ok 11-11", // corrected from line 3
		"Assert the retry count": "ok 02-02", // unscored: located (CRLF head)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q (all %v)", k, got[k], v, got)
		}
	}
	if !slices.Contains(res.Notes, "1 suggestion line range was corrected.") {
		t.Errorf("notes = %q", res.Notes)
	}
	for _, s := range res.Suggestions {
		if s.Anchor != nil {
			t.Errorf("%s: anchor %v before WP-2h", s.Summary, s.Anchor)
		}
	}

	// The quoted code is not in the head file: kept, unverified, the given
	// range as it was.
	missing := sugDelay
	missing.existing = "delay := retries * 200"
	h = newHarness(map[int][]string{
		0: {suggestionsAnswer(missing)},
		reflectKind(0): {reflectionAnswer(fb{number: 1, summary: missing.summary, file: missing.file, start: 12, end: 12,
			score: 8, why: "w"})},
	})
	h.prov.files = headSampleFiles()
	res = h.run(t, Args{})
	if len(res.Suggestions) != 1 {
		t.Fatalf("suggestions = %v", summaries(res))
	}
	s := res.Suggestions[0]
	if s.Verified || s.UnverifiedReason != UnverifiedNotFound || s.StartLine == nil || *s.StartLine != 12 {
		t.Errorf("unverified suggestion = %+v", s)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, "corrected") {
			t.Errorf("note %q", n)
		}
	}
}
