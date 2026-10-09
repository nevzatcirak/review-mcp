package render

import (
	"encoding/json"
	"flag"
	"os"
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
	res.Notes = []string{"Part 2 of 3 failed (llm_timeout); its files were not reviewed."}
	return res
}

// TestClientGoldens pins the client rendering of the one-call and
// three-part runs, of a failed self-review and of a partial result.
func TestClientGoldens(t *testing.T) {
	for name, res := range map[string]*improve.Result{
		"one_call":    loadRun(t, "one_call"),
		"three_parts": loadRun(t, "three_parts"),
		"unscored":    unscoredResult(t),
		"partial":     partialResult(t),
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
