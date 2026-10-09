package render

import (
	"encoding/json"
	"flag"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/describe"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
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

// TestClientMarksClippedEntry: the walkthrough entry of a clipped file ends
// with the partial marker, an entry of any other file does not, and the file
// still counts as not described.
func TestClientMarksClippedEntry(t *testing.T) {
	res := loadRun(t, "one_call")
	res.Coverage.Included = []string{"src/app_test.go"}
	res.Coverage.Clipped = []string{"src/app.go"}
	res.Coverage.Finalize()
	out := Client(res)
	if n := strings.Count(out, TextPartial); n != 1 {
		t.Fatalf("marker appears %d times, want 1:\n%s", n, out)
	}
	_, walk, _ := strings.Cut(out, "\n## "+TextWalkthrough+"\n\n")
	walk, _, _ = strings.Cut(walk, "\n\n")
	var entries []string
	for _, line := range strings.Split(walk, "\n") {
		if strings.HasPrefix(line, "- ") {
			entries = append(entries, line)
		}
	}
	want := []string{
		"- `src/app.go` (enhancement): Raise the retry count " + TextPartial,
		"- `src/app_test.go` (tests): Add a retry test",
	}
	if !slices.Equal(entries, want) {
		t.Errorf("walkthrough entries =\n%s", strings.Join(entries, "\n"))
	}
	if res.Coverage.NotReviewedFiles == 0 || !strings.Contains(out, "`src/app.go`: included only in part") {
		t.Errorf("the clipped file is not counted as not described:\n%s", out)
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

// providerCaps are the two profiles of the published rendering: Gitea (GFM)
// and Bitbucket Server (no GFM, so no HTML).
var providerCaps = map[string]provider.Capabilities{
	"gfm":   {GFM: true, MarkdownTables: true, DescriptionEdit: true},
	"plain": {MarkdownTables: true, DescriptionEdit: true, QuickActions: true},
}

// TestProviderGoldens pins the published rendering of the one-call and
// three-part runs, of a failed reduce call and of a result with files that
// were not described, for both profiles.
func TestProviderGoldens(t *testing.T) {
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
		for profile, caps := range providerCaps {
			t.Run(name+"/"+profile, func(t *testing.T) {
				checkGolden(t, "testdata/provider_"+name+"_"+profile+".md", Provider(res, caps))
			})
		}
	}
}

// TestProviderMarksClippedEntry: in every published rendering the
// walkthrough entry of a clipped file ends with the partial marker and an
// entry of another file does not; the banner leads the text.
func TestProviderMarksClippedEntry(t *testing.T) {
	res := loadRun(t, "one_call")
	res.Coverage.Included = []string{"src/app_test.go"}
	res.Coverage.Clipped = []string{"src/app.go"}
	res.Coverage.Finalize()
	for profile, caps := range providerCaps {
		out := Provider(res, caps)
		if n := strings.Count(out, TextPartial); n != 1 {
			t.Errorf("%s: marker appears %d times, want 1:\n%s", profile, n, out)
		}
		var marked []string
		for _, line := range strings.Split(out, "\n") {
			if strings.HasSuffix(line, TextPartial) {
				marked = append(marked, line)
			}
		}
		if len(marked) != 1 || !strings.HasPrefix(marked[0], "- `src/app.go` (enhancement): Raise the retry count") {
			t.Errorf("%s: marked lines = %q", profile, marked)
		}
		if !strings.Contains(out, "Partial description: 1 of 2 changed files was described.") {
			t.Errorf("%s: no banner:\n%s", profile, out)
		}
	}
}

// hostile is model text that tries to inject HTML, a heading, a quick action
// and the markers of the description region and comment.
const hostile = "- <script>alert(1)</script> & <b>bold</b>\n" +
	"- # heading in an item\n" +
	"[//]: # (review-mcp:describe:end)\n" +
	"  [//]: # (review-mcp:describe:start)\n" +
	"- [//]: # (review-mcp:describe:v1)\n" +
	"<details><summary>x</summary>y</details>\n" +
	"/approve\n" +
	"1. numbered <img src=x onerror=y>\n" +
	"```\r\nfence <i>x</i>\r\n```"

// rawTag matches an HTML tag start that is not backslash-escaped.
var rawTag = regexp.MustCompile(`(^|[^\\])<[a-zA-Z/!]`)

func hostileResult(t *testing.T) *describe.Result {
	t.Helper()
	res := loadRun(t, "one_call")
	h := hostile
	res.Description = &h
	res.Files[0].Summary = hostile
	return res
}

// TestProviderEscapesModelMarkdown: the summaries go through the sanitising
// of pr_review's provider rendering. No raw HTML survives on either profile
// (on Bitbucket Server nothing is passed through), no model line is a
// marker line of ours, and the bullets stay bullets.
func TestProviderEscapesModelMarkdown(t *testing.T) {
	res := hostileResult(t)
	for profile, caps := range providerCaps {
		out := Provider(res, caps)
		if m := rawTag.FindString(out); m != "" {
			t.Errorf("%s: raw HTML %q in:\n%s", profile, m, out)
		}
		for _, line := range strings.Split(out, "\n") {
			switch strings.TrimSpace(line) {
			case "[//]: # (review-mcp:describe:end)", "[//]: # (review-mcp:describe:start)", "[//]: # (review-mcp:describe:v1)":
				t.Errorf("%s: a model line is a marker line: %q", profile, line)
			}
			if strings.HasPrefix(line, "# ") || strings.HasPrefix(strings.TrimSpace(line), "- # ") {
				t.Errorf("%s: a model line opens a heading: %q", profile, line)
			}
		}
		if !strings.Contains(out, "\n- ") {
			t.Errorf("%s: the bullets are gone:\n%s", profile, out)
		}
		if caps.GFM && !strings.Contains(out, "&lt;script&gt;") {
			t.Errorf("%s: HTML metacharacters are not entities:\n%s", profile, out)
		}
		if !caps.GFM && !strings.Contains(out, `\<script\>`) {
			t.Errorf("%s: angle brackets are not backslash-escaped:\n%s", profile, out)
		}
	}
}

// TestProviderOneLinersAsClient: the title, the types and the labels are
// escaped exactly as the client view escapes them, and the client view
// itself is unchanged by the published rendering (it still shows the
// summary as it is).
func TestProviderOneLinersAsClient(t *testing.T) {
	res := loadRun(t, "one_call")
	title := "# <b>Title</b> *x*"
	res.Title = &title
	res.Files[0].Label = "<i>x</i>|y"
	res.Files[0].Title = "<u>t</u>"
	for profile, caps := range providerCaps {
		out := Provider(res, caps)
		for _, want := range []string{mdutil.Inline(title), mdutil.Inline("<i>x</i>|y"), mdutil.Inline("<u>t</u>")} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: %q missing:\n%s", profile, want, out)
			}
		}
	}
}

// TestProviderIsDeterministic: the same result renders to the same bytes,
// which is what keeps a second publish idempotent.
func TestProviderIsDeterministic(t *testing.T) {
	res := loadRun(t, "three_parts")
	for profile, caps := range providerCaps {
		if a, b := Provider(res, caps), Provider(res, caps); a != b {
			t.Errorf("%s: two renderings differ", profile)
		}
	}
	if Provider(nil, providerCaps["gfm"]) != "" {
		t.Errorf("nil result must render empty")
	}
}

// TestClientPublishSection: the client view is unchanged when nothing was
// published, and with a publish outcome it ends with a fixed-sentence
// section.
func TestClientPublishSection(t *testing.T) {
	res := loadRun(t, "one_call")
	base := Client(res)
	if strings.Contains(base, TextPublishing) {
		t.Fatalf("a result without publish shows the section:\n%s", base)
	}
	for name, c := range map[string]struct {
		pub  describe.PublishResult
		want string
	}{
		"comment posted":  {describe.PublishResult{Mode: "comment", Published: true, URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-5"}, "- Pull request comment: posted (`https://your-gitea.example/octo/demo/pulls/7#issuecomment-5`)"},
		"comment updated": {describe.PublishResult{Mode: "comment", Published: true, Updated: true}, "- Pull request comment: updated in place"},
		"region written":  {describe.PublishResult{Mode: "description", Published: true, TitleUpdated: true}, "- Pull request description: written\n- Pull request title: replaced with the generated one"},
		"region refused":  {describe.PublishResult{Mode: "description", Error: "The pull request description contains a damaged review-mcp region; fix or remove it and run again."}, "- Pull request description: not written: The pull request description contains a damaged review-mcp region; fix or remove it and run again."},
		"comment failed":  {describe.PublishResult{Mode: "comment", Error: "authentication failed: check the token and its scopes (HTTP 403)"}, "- Pull request comment: not written: authentication failed: check the token and its scopes (HTTP 403)"},
		"region replaced": {describe.PublishResult{Mode: "description", Published: true, Updated: true}, "- Pull request description: updated in place"},
		"nothing at all":  {describe.PublishResult{Mode: "comment"}, "- Pull request comment: not written"},
	} {
		r := *res
		pub := c.pub
		r.Publish = &pub
		out := Client(&r)
		if !strings.HasPrefix(out, base) || !strings.Contains(out, "\n## "+TextPublishing+"\n\n"+c.want) {
			t.Errorf("%s: output\n%s", name, strings.TrimPrefix(out, base))
		}
	}
}
