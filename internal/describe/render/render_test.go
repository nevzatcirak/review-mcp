package render

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/describe"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

var update = flag.Bool("update", false, "rewrite the rendering goldens")

// loadRun reads a run golden of the pipeline tests (internal/describe
// testdata/runs/<name>/result.json), so the renderings below follow the
// pipeline's goldens.
func loadRun(t *testing.T, name string) *describe.Result {
	t.Helper()
	b, err := os.ReadFile("../testdata/runs/" + name + "/result.json") //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	var res describe.Result
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	return &res
}

func checkGolden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the rendering (run go test -run TestClientGoldens -update after checking the change):\n%s", path, got)
	}
}

// notDescribedResult is the one-call run with one file not returned, one
// left out for budget, one clipped, one in a failed part and one binary
// (outside the count).
func notDescribedResult(t *testing.T) *describe.Result {
	t.Helper()
	res := loadRun(t, "one_call")
	c := &res.Coverage
	c.Included = []string{"src/app.go"}
	c.Clipped = []string{"src/big.go"}
	c.Omitted.Modified = []string{"src/later.go"}
	c.Skipped = []llmrun.SkippedFile{
		{Path: "assets/logo.png", Reason: "binary"},
		{Path: "src/part2.go", Reason: llmrun.SkipModelCallFailed},
		{Path: "src/app_test.go", Reason: describe.SkipNotReturned},
		{Path: "src/huge.go", Reason: "size_limit"},
	}
	c.Finalize()
	res.Files = res.Files[:1]
	res.Notes = []string{"1 file was shown to the model but got no walkthrough entry; listed as not described (see Coverage)."}
	return res
}

// TestClientGoldens pins the client rendering of the one-call and
// three-part runs, of a failed reduce call and of a result with files that
// were not described.
func TestClientGoldens(t *testing.T) {
	failed := loadRun(t, "three_parts")
	failed.Title, failed.Type = nil, nil
	var lines []string
	for _, f := range failed.Files {
		lines = append(lines, "- "+f.Title)
	}
	d := strings.Join(lines, "\n")
	failed.Description = &d
	failed.Notes = append(failed.Notes, describe.NoteReduceFailed)

	for name, res := range map[string]*describe.Result{
		"one_call":      loadRun(t, "one_call"),
		"three_parts":   loadRun(t, "three_parts"),
		"reduce_failed": failed,
		"not_described": notDescribedResult(t),
	} {
		t.Run(name, func(t *testing.T) { checkGolden(t, "testdata/"+name+".md", Client(res)) })
	}
}

// TestNotDescribedSection (Y-7): every changed file that was not described is
// listed with its reason, and the banner leads the text; the binary file is
// outside the count and not listed; the number of listed files equals
// not_reviewed_files.
func TestNotDescribedSection(t *testing.T) {
	res := notDescribedResult(t)
	out := Client(res)
	if first, _, _ := strings.Cut(out, "\n"); first != "**Partial description: 1 of 6 changed files was described. 5 files were not described (see Coverage).**" {
		t.Errorf("first line = %q", first)
	}
	_, section, _ := strings.Cut(out, "\n## "+TextNotDescribed+"\n\n")
	section, _, _ = strings.Cut(section, "\n\n")
	want := []string{
		"- `src/big.go`: included only in part (clipped to fit the context window)",
		"- `src/later.go`: left out to fit the context window",
		"- `src/part2.go`: its part's model call failed",
		"- `src/app_test.go`: shown to the model, but it returned no walkthrough entry",
		"- `src/huge.go`: skipped: `size_limit`",
	}
	if section != strings.Join(want, "\n") {
		t.Errorf("Not described section =\n%s", section)
	}
	if strings.Count(section, "\n")+1 != res.Coverage.NotReviewedFiles {
		t.Errorf("listed %d, not_reviewed_files %d", strings.Count(section, "\n")+1, res.Coverage.NotReviewedFiles)
	}
	if strings.Contains(section, "logo.png") {
		t.Errorf("the binary file is listed as not described")
	}

	// A complete result has no banner and no section.
	full := Client(loadRun(t, "one_call"))
	if strings.Contains(full, "Partial description") || strings.Contains(full, TextNotDescribed) {
		t.Errorf("complete result renders a banner or the section:\n%s", full)
	}
}

// TestClientEscapesOneLiners: the title, the types and the labels are
// escaped; a title cannot open a heading or inject HTML.
func TestClientEscapesOneLiners(t *testing.T) {
	res := loadRun(t, "one_call")
	title := "# <b>Title</b>"
	res.Title = &title
	res.Files[0].Label = "<i>x</i>"
	out := Client(res)
	if strings.Contains(out, "<b>") || strings.Contains(out, "<i>") || strings.Contains(out, "\n# ") {
		t.Errorf("unescaped one-liner:\n%s", out)
	}
}
