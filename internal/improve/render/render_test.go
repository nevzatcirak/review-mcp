package render

import (
	"encoding/json"
	"flag"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/improve"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

var update = flag.Bool("update", false, "rewrite the rendering goldens")

// loadRun reads a run golden of the pipeline tests (internal/improve
// testdata/runs/<name>/result.json), so the renderings below follow the
// pipeline's goldens.
func loadRun(t *testing.T, name string) *improve.Result {
	t.Helper()
	b, err := os.ReadFile("../testdata/runs/" + name + "/result.json") //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	var res improve.Result
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	return &res
}

func checkGolden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
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

// unscoredResult is the one-call run with its self-review failed: no
// score, no reason, no line range, and the fixed note.
func unscoredResult(t *testing.T) *improve.Result {
	t.Helper()
	res := loadRun(t, "one_call")
	for i := range res.Suggestions {
		s := &res.Suggestions[i]
		s.Score, s.Why, s.StartLine, s.EndLine = nil, "", nil, nil
	}
	res.Notes = []string{improve.NoteNotScoredOneCall}
	return res
}

// partialResult is the three-part run with part 2 failed: the banner leads.
func partialResult(t *testing.T) *improve.Result {
	t.Helper()
	res := loadRun(t, "three_parts")
	c := &res.Coverage
	c.Included = []string{"src/f00.go", "src/f01.go", "src/f04.go", "src/f05.go"}
	c.Skipped = []llmrun.SkippedFile{{Path: "src/f02.go", Reason: llmrun.SkipModelCallFailed},
		{Path: "src/f03.go", Reason: llmrun.SkipModelCallFailed}}
	c.FailedParts = 1
	c.Finalize()
	// A failed part's files were not reviewed, so no suggestion is left for
	// them (the pipeline never produces one).
	res.Suggestions = slices.DeleteFunc(res.Suggestions, func(s improve.Suggestion) bool {
		return slices.ContainsFunc(c.Skipped, func(f llmrun.SkippedFile) bool { return f.Path == s.File })
	})
	res.Notes = []string{"Part 2 of 3 failed (llm_timeout); its files were not reviewed."}
	return res
}

// withPublish is the one-call run with publishing's outcome p.
func withPublish(t *testing.T, p *improve.PublishResult) *improve.Result {
	t.Helper()
	res := loadRun(t, "one_call")
	res.Publish = p
	return res
}

// TestClientPublishingSection: the Publishing section shows the overview's
// outcome and the inline counts for each outcome, with singular and plural
// forms, and is absent when publishing did not run, so that output without
// publishing is unchanged.
func TestClientPublishingSection(t *testing.T) {
	const head = "\n## " + TextPublishing + "\n\n"
	for _, tc := range []struct {
		name string
		p    *improve.PublishResult
		want string
	}{
		{"posted", &improve.PublishResult{Published: true, URL: "https://h/c/1"},
			"- Overview comment: posted (`https://h/c/1`)\n"},
		{"posted without a link", &improve.PublishResult{Published: true},
			"- Overview comment: posted\n"},
		{"updated", &improve.PublishResult{Published: true, Updated: true, URL: "https://h/c/1"},
			"- Overview comment: updated in place (`https://h/c/1`)\n"},
		{"failed", &improve.PublishResult{Error: "the token is not allowed to write"},
			"- Overview comment: not posted: the token is not allowed to write\n"},
		{"failed without a sentence", &improve.PublishResult{},
			"- Overview comment: not posted\n"},
		{"counts", &improve.PublishResult{Published: true,
			Inline: &improve.InlineSummary{Posted: 3, SkippedDuplicate: 2, Unanchorable: 1, Failed: 4}},
			"- Overview comment: posted\n- Inline suggestions: 3 posted, 2 skipped as duplicates, 1 unanchorable, 4 failed\n"},
		{"singular", &improve.PublishResult{Published: true,
			Inline: &improve.InlineSummary{SkippedDuplicate: 1}},
			"- Overview comment: posted\n- Inline suggestions: 0 posted, 1 skipped as a duplicate, 0 unanchorable, 0 failed\n"},
	} {
		out := Client(withPublish(t, tc.p))
		if !strings.HasSuffix(out, head+tc.want) {
			t.Errorf("%s: want the section %q in:\n%s", tc.name, head+tc.want, out)
		}
		if strings.Count(out, "## "+TextPublishing) != 1 || !strings.Contains(out, "## Coverage") ||
			strings.Index(out, "## "+TextPublishing) < strings.Index(out, "## Coverage") {
			t.Errorf("%s: the section is not once, after the coverage:\n%s", tc.name, out)
		}
	}
	// A failed overview has no inline counts line, and no URL.
	if out := Client(withPublish(t, &improve.PublishResult{Error: "x", URL: "https://h"})); strings.Contains(out, "Inline suggestions") || strings.Contains(out, "https://h") {
		t.Errorf("failed overview:\n%s", out)
	}
	if out := Client(loadRun(t, "one_call")); strings.Contains(out, TextPublishing) {
		t.Errorf("section without publishing:\n%s", out)
	}
}

// TestClientGoldens pins the client rendering of the one-call and
// three-part runs, of a failed self-review and of a partial result.
func TestClientGoldens(t *testing.T) {
	for name, res := range map[string]*improve.Result{
		"one_call":    loadRun(t, "one_call"),
		"three_parts": loadRun(t, "three_parts"),
		"unscored":    unscoredResult(t),
		"partial":     partialResult(t),
		"published": withPublish(t, &improve.PublishResult{Published: true, URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-3",
			Inline: &improve.InlineSummary{Posted: 1, SkippedDuplicate: 1, Failed: 1}}),
		"empty": {Suggestions: []improve.Suggestion{}, Notes: []string{improve.NoteNoReviewableChanges},
			Coverage: llmrun.Coverage{}},
	} {
		checkGolden(t, "testdata/"+name+".md", Client(res))
	}
}

// TestClientMarksUnscoredAndVerification: an unscored suggestion says so;
// a verified suggestion's range says it was checked against the head file;
// an unverified one is marked not anchored (Y-10), with the range as given
// or without one, and a distinct mark when the head file was not available.
func TestClientMarksUnscoredAndVerification(t *testing.T) {
	out := Client(unscoredResult(t))
	if strings.Count(out, "- Score: "+TextUnscored+"\n") != 2 || strings.Contains(out, "- Why:") {
		t.Errorf("unscored rendering:\n%s", out)
	}
	res := loadRun(t, "one_call")
	// The run's suggestions are patch-verified; make them head-unavailable.
	for i := range res.Suggestions {
		res.Suggestions[i].Verified, res.Suggestions[i].UnverifiedReason = false, improve.UnverifiedHeadUnavailable
	}
	if out = Client(res); !strings.Contains(out, "(line 12; "+TextNotAnchoredNoHead+")") || !strings.Contains(out, "- Score: 9 of 10\n") {
		t.Errorf("head unavailable rendering:\n%s", out)
	}
	res.Suggestions[0].Verified, res.Suggestions[0].UnverifiedReason = true, ""
	res.Suggestions[1].UnverifiedReason = improve.UnverifiedNotFound
	out = Client(res)
	if !strings.Contains(out, "- File: `src/app_test.go` (line 2; "+TextLinesVerified+")\n") ||
		!strings.Contains(out, "- File: `src/app.go` (line 12; "+TextNotAnchored+")\n") {
		t.Errorf("verified and not-found rendering:\n%s", out)
	}
	res.Suggestions[1].StartLine, res.Suggestions[1].EndLine = nil, nil
	res.Suggestions[1].UnverifiedReason = improve.UnverifiedAmbiguous
	if out = Client(res); !strings.Contains(out, "- File: `src/app.go` ("+TextNotAnchored+")\n") {
		t.Errorf("ambiguous rendering without a range:\n%s", out)
	}
	if TextNotAnchored != "not anchored: the quoted code was not found at the given lines" {
		t.Errorf("TextNotAnchored is not the Y-10 sentence: %q", TextNotAnchored)
	}
	if !strings.HasPrefix(Client(partialResult(t)), "**Partial review: 4 of 6 changed files were reviewed.") {
		t.Errorf("the partial banner does not lead")
	}
}

// TestClientFencesHoldTheCode: model-written code cannot close its fence,
// and a language name cannot carry markup into the info string.
func TestClientFencesHoldTheCode(t *testing.T) {
	res := loadRun(t, "one_call")
	s := &res.Suggestions[0]
	s.ExistingCode = "x := \"```\"\n```\n# not a heading"
	s.Language = "go\n<script>"
	out := Client(res)
	if !strings.Contains(out, "````\nx := \"```\"\n```\n# not a heading\n````\n") {
		t.Errorf("the fence does not hold the code:\n%s", out)
	}
	if strings.Contains(out, "<script>") {
		t.Errorf("markup in the info string")
	}
	if fenceInfo("C++") != "c++" || fenceInfo("go lang") != "" {
		t.Errorf("fenceInfo = %q %q", fenceInfo("C++"), fenceInfo("go lang"))
	}
}
