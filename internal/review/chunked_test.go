package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// partLLM answers each part of a review in parts from its own script: the
// part is read from the part line of the user prompt (PartHeader); a prompt
// without one is part 1. err[i], when set, fails every call of part i.
type partLLM struct {
	answers map[int]string
	errs    map[int]error
	calls   []llmCall
	parts   []int
}

var partRE = regexp.MustCompile(`This is part (\d+) of (\d+)\.`)

func (f *partLLM) Complete(_ context.Context, system, user string) (*llm.Response, error) {
	f.calls = append(f.calls, llmCall{system, user})
	part := 1
	if m := partRE.FindStringSubmatch(user); m != nil {
		part, _ = strconv.Atoi(m[1])
	}
	f.parts = append(f.parts, part)
	if err := f.errs[part]; err != nil {
		return nil, err
	}
	a, ok := f.answers[part]
	if !ok {
		a = findingsAnswer()
	}
	return &llm.Response{Content: a}, nil
}

// chunkHarness is a harness over six files of about 700 tokens each with
// diff.max_tokens 2000, so the review takes three parts of two files.
func chunkHarness(t *testing.T, answers map[int]string) (*harness, *partLLM) {
	t.Helper()
	h := newHarness(goodAnswer)
	h.prov.files = bigFiles(6)
	capTokens := 2000
	h.deps.Config.Diff.MaxTokens = &capTokens
	f := &partLLM{answers: answers, errs: map[int]error{}}
	h.deps.LLM = f
	return h, f
}

// partFiles are the files of part i (1-based) of chunkHarness.
func partFiles(i int) []string {
	return []string{fmt.Sprintf("src/f%02d.go", 2*(i-1)), fmt.Sprintf("src/f%02d.go", 2*(i-1)+1)}
}

// partAnswerYAML is an answer with the given fields; an empty string leaves
// a field out, and findings are "file|header|content" triples.
func partAnswerYAML(effort, tests, security, performance string, findings ...string) string {
	var b strings.Builder
	b.WriteString("```yaml\nreview:\n")
	if effort != "" {
		b.WriteString("  estimated_effort_to_review: " + effort + "\n")
	}
	if tests != "" {
		b.WriteString("  relevant_tests: " + tests + "\n")
	}
	b.WriteString("  key_issues_to_review:\n")
	for _, f := range findings {
		p := strings.Split(f, "|")
		fmt.Fprintf(&b, "    - relevant_file: %s\n      issue_header: %s\n      issue_content: %s\n      start_line: 3\n      end_line: 3\n",
			p[0], p[1], p[2])
	}
	if security != "" {
		b.WriteString("  security_concerns: |\n    " + security + "\n")
	}
	if performance != "" {
		b.WriteString("  performance_concerns: |\n    " + performance + "\n")
	}
	b.WriteString("```\n")
	return b.String()
}

func runChunked(t *testing.T, h *harness, args Args) *Result {
	t.Helper()
	var stages []string
	h.deps.Progress = func(s string) { stages = append(stages, s) }
	if args.PRURL == "" {
		args.PRURL = testPRURL
	}
	res, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	h.stages = stages
	return res
}

// TestChunkedThreeParts: six files that need three parts are reviewed in
// three sequential calls, each with the part line and only its own files;
// the answers are merged and the coverage is complete.
func TestChunkedThreeParts(t *testing.T) {
	h, f := chunkHarness(t, map[int]string{
		1: partAnswerYAML("2", "No", "No", "No", "src/f00.go|Possible Bug|Off by one in f00."),
		2: partAnswerYAML("4", "Yes", "No", "Unbounded loop in src/f03.go.", "src/f03.go|Leak|The handle is never closed."),
		3: partAnswerYAML("3", "No", "No", "No", "src/f05.go|Race|The map is written concurrently."),
	})
	res := runChunked(t, h, Args{})

	if len(f.calls) != 3 || !slices.Equal(f.parts, []int{1, 2, 3}) {
		t.Fatalf("calls %d, parts %v; want three calls in part order", len(f.calls), f.parts)
	}
	for i, c := range f.calls {
		hdr := PartHeader(i+1, 3)
		if !strings.Contains(c.user, hdr+"\n\n\nThe PR code diff:\n") {
			t.Errorf("part %d: the part line is not right before the diff", i+1)
		}
		for j := 1; j <= 3; j++ {
			for _, file := range partFiles(j) {
				if got := strings.Contains(c.user, "## File: '"+file+"'"); got != (i+1 == j) {
					t.Errorf("part %d: file %s in the prompt = %v", i+1, file, got)
				}
			}
		}
		if c.system != f.calls[0].system {
			t.Errorf("part %d: the system prompt differs from part 1's", i+1)
		}
		if !strings.Contains(c.user, titleMarker) || !strings.Contains(c.user, descMarker) {
			t.Errorf("part %d: the PR title or description is missing", i+1)
		}
	}
	wantStages := []string{StageFetching, StagePreparingDiff, "calling model (part 1 of 3)", "calling model (part 2 of 3)",
		"calling model (part 3 of 3)"}
	if !slices.Equal(h.stages, wantStages) {
		t.Errorf("stages = %q", h.stages)
	}
	for _, s := range wantStages[2:] {
		if n, ok := StageParts(s); !ok || n != 3 {
			t.Errorf("StageParts(%q) = %d, %v", s, n, ok)
		}
	}
	if _, ok := StageParts(StageCallingModel); ok {
		t.Errorf("StageParts accepts the one-call stage")
	}

	r := res.Review
	var files []string
	for _, ki := range r.KeyIssuesToReview {
		files = append(files, ki.RelevantFile)
		if ki.Link == "" {
			t.Errorf("finding on %s has no link", ki.RelevantFile)
		}
	}
	if !slices.Equal(files, []string{"src/f00.go", "src/f03.go", "src/f05.go"}) {
		t.Errorf("findings = %v, want part order", files)
	}
	if r.EstimatedEffortToReview == nil || *r.EstimatedEffortToReview != 4 {
		t.Errorf("effort = %v, want the largest (4)", r.EstimatedEffortToReview)
	}
	if r.RelevantTests == nil || !*r.RelevantTests {
		t.Errorf("tests = %v, want true (one part says yes)", r.RelevantTests)
	}
	if r.SecurityConcerns == nil || *r.SecurityConcerns != SecurityNo {
		t.Errorf("security = %v, want No", r.SecurityConcerns)
	}
	if r.PerformanceConcerns == nil || *r.PerformanceConcerns != "Unbounded loop in src/f03.go." {
		t.Errorf("performance = %q, want the one concern without a part prefix", deref(r.PerformanceConcerns))
	}

	c := res.Coverage
	if c.Partial || c.ReviewedFiles != 6 || c.TotalFiles != 6 || c.ModelCalls != 3 || c.FailedParts != 0 ||
		len(c.Included) != 6 {
		t.Errorf("coverage = %+v", c)
	}
	m := res.Metadata
	if m.LLMCalls != 3 || m.FastPath || m.Reasked || m.RepairTactic != "direct" || m.DiffTokens <= 0 || m.RequestTokens <= 0 {
		t.Errorf("metadata = %+v", m)
	}
	if len(res.Notes) != 0 {
		t.Errorf("notes = %q", res.Notes)
	}
	h.checkNoLeaks(t)
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// TestChunkedDedupAcrossParts: a finding an earlier part returned (same
// file, header and content; other lines) is dropped from a later part;
// duplicates within one answer stay, as in a review in one call.
func TestChunkedDedupAcrossParts(t *testing.T) {
	dup := "src/f00.go|Possible Bug|Off by one in f00."
	h, _ := chunkHarness(t, map[int]string{
		1: partAnswerYAML("", "", "", "", dup, "src/f01.go|Style|Same text twice.", "src/f01.go|Style|Same text twice."),
		2: partAnswerYAML("", "", "", "", "src/f00.go|possible  bug|Off by one in f00.", "src/f02.go|Leak|Never closed."),
		3: partAnswerYAML("", "", "", "", dup),
	})
	res := runChunked(t, h, Args{MaxFindings: 5})
	var got []string
	for _, ki := range res.Review.KeyIssuesToReview {
		got = append(got, ki.RelevantFile+"|"+ki.IssueHeader)
	}
	want := []string{"src/f00.go|Possible Bug", "src/f01.go|Style", "src/f01.go|Style", "src/f02.go|Leak"}
	if !slices.Equal(got, want) {
		t.Errorf("merged findings = %q, want %q", got, want)
	}
}

// TestChunkedTotalCap: the merged list is capped at
// review.max_total_findings, not at max_findings, and the findings beyond
// it are counted in a note.
func TestChunkedTotalCap(t *testing.T) {
	answers := map[int]string{}
	for i := 1; i <= 3; i++ {
		var fs []string
		for j := range 3 {
			fs = append(fs, fmt.Sprintf("%s|Issue %d|Problem %d of part %d.", partFiles(i)[j%2], j, j, i))
		}
		answers[i] = partAnswerYAML("", "", "", "", fs...)
	}
	h, _ := chunkHarness(t, answers)
	h.deps.Config.Review.MaxTotalFindings = 5
	res := runChunked(t, h, Args{})
	if n := len(res.Review.KeyIssuesToReview); n != 5 {
		t.Fatalf("findings = %d, want 5 (review.max_total_findings); notes %q", n, res.Notes)
	}
	if last := res.Review.KeyIssuesToReview[4]; last.IssueContent != "Problem 1 of part 2." {
		t.Errorf("the cap kept %q last, want the merged order", last.IssueContent)
	}
	if !slices.Contains(res.Notes, "4 further findings were not shown because of review.max_total_findings.") {
		t.Errorf("notes = %q, want the total-cap note", res.Notes)
	}

	// Each part still asks for at most max_findings.
	if !strings.Contains(h.deps.LLM.(*partLLM).calls[1].system, "(0-3 issues)") {
		t.Errorf("a part does not ask for at most max_findings")
	}

	// One dropped finding: the singular note.
	h, _ = chunkHarness(t, answers)
	h.deps.Config.Review.MaxTotalFindings = 8
	res = runChunked(t, h, Args{})
	if len(res.Review.KeyIssuesToReview) != 8 ||
		!slices.Contains(res.Notes, "1 further finding was not shown because of review.max_total_findings.") {
		t.Errorf("findings %d, notes %q", len(res.Review.KeyIssuesToReview), res.Notes)
	}
}

// TestChunkedFieldMerge: effort, tests and concerns across parts.
func TestChunkedFieldMerge(t *testing.T) {
	cases := []struct {
		name          string
		answers       map[int]string
		effort        *int
		tests         *bool
		security, per string
	}{
		{
			name: "every part says no",
			answers: map[int]string{1: partAnswerYAML("1", "No", "No", "No"), 2: partAnswerYAML("1", "No", "No", "No"),
				3: partAnswerYAML("2", "No", "No", "No")},
			effort: ptr(2), tests: ptr(false), security: "No", per: "No",
		},
		{
			name: "two concerns get part prefixes",
			answers: map[int]string{1: partAnswerYAML("", "", "SQL injection: the query is built from input.", "No"),
				2: partAnswerYAML("", "", "No", "No"), 3: partAnswerYAML("", "", "XSS: the name is not escaped.", "No")},
			security: "Part 1: SQL injection: the query is built from input.\n\nPart 3: XSS: the name is not escaped.",
			per:      "No",
		},
		{
			name: "fields no part answered stay unset",
			answers: map[int]string{1: partAnswerYAML("", "", "", ""), 2: partAnswerYAML("", "No", "", ""),
				3: partAnswerYAML("", "", "", "")},
			tests: ptr(false), security: "<nil>", per: "<nil>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := chunkHarness(t, tc.answers)
			r := runChunked(t, h, Args{}).Review
			if !samePtr(r.EstimatedEffortToReview, tc.effort) || !samePtr(r.RelevantTests, tc.tests) ||
				deref(r.SecurityConcerns) != tc.security || deref(r.PerformanceConcerns) != tc.per {
				t.Errorf("merged = effort %v tests %v security %q performance %q", r.EstimatedEffortToReview,
					r.RelevantTests, deref(r.SecurityConcerns), deref(r.PerformanceConcerns))
			}
		})
	}
}

func samePtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// TestChunkedFailedPart: a part whose call fails in the middle makes the
// result partial, publishable and honest: its files are not reviewed with
// reason model_call_failed, the note names the part and a fixed class, and
// the error text is never shown.
func TestChunkedFailedPart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *partLLM)
		class string
	}{
		{"llm error", func(f *partLLM) {
			f.errs[2] = &llm.Error{Class: llm.ClassTimeout, Detail: "secret detail " + answerMarker}
		}, "llm_timeout"},
		{"unparseable after the re-ask", func(f *partLLM) { f.answers[2] = "not yaml " + answerMarker }, "review_unparseable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f := chunkHarness(t, map[int]string{
				1: partAnswerYAML("2", "", "", "", "src/f00.go|Bug|One."),
				3: partAnswerYAML("3", "", "", "", "src/f04.go|Bug|Three."),
			})
			tc.setup(f)
			res := runChunked(t, h, Args{Publish: true})

			c := res.Coverage
			var failed []string
			for _, s := range c.Skipped {
				if s.Reason == llmrun.SkipModelCallFailed {
					failed = append(failed, s.Path)
				}
			}
			if !slices.Equal(failed, partFiles(2)) {
				t.Errorf("model_call_failed files = %v, want part 2's %v", failed, partFiles(2))
			}
			for _, p := range partFiles(2) {
				if slices.Contains(c.Included, p) {
					t.Errorf("%s of the failed part counts as included", p)
				}
			}
			if !c.Partial || c.ReviewedFiles != 4 || c.NotReviewedFiles != 2 || c.TotalFiles != 6 ||
				c.ModelCalls != 3 || c.FailedParts != 1 {
				t.Errorf("coverage = partial %v reviewed %d not reviewed %d total %d calls %d failed %d", c.Partial,
					c.ReviewedFiles, c.NotReviewedFiles, c.TotalFiles, c.ModelCalls, c.FailedParts)
			}
			want := "Part 2 of 3 failed (" + tc.class + "); its files were not reviewed."
			if !slices.Contains(res.Notes, want) {
				t.Errorf("notes = %q, want %q", res.Notes, want)
			}
			if raw, _ := json.Marshal(res); strings.Contains(string(raw), answerMarker) || strings.Contains(string(raw), "secret detail") {
				t.Errorf("the error or answer text reached the result")
			}
			if len(res.Review.KeyIssuesToReview) != 2 || res.Publish == nil || !res.Publish.Published {
				t.Errorf("findings %d, publish %+v", len(res.Review.KeyIssuesToReview), res.Publish)
			}
			h.checkNoLeaks(t)
		})
	}
}

// TestChunkedEveryPartFails: the run returns the first part's classified
// error, as a review in one call does, and publishes nothing.
func TestChunkedEveryPartFails(t *testing.T) {
	h, f := chunkHarness(t, nil)
	f.errs[1] = &llm.Error{Class: llm.ClassAuth}
	f.errs[2] = &llm.Error{Class: llm.ClassTimeout}
	f.errs[3] = &llm.Error{Class: llm.ClassTimeout}
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if res != nil || !errors.Is(err, llm.ErrAuth) {
		t.Fatalf("res %v, err %v; want the first part's error", res, err)
	}
	if len(f.calls) != 3 || len(h.prov.posted) != 0 {
		t.Errorf("calls %d, posted %d", len(f.calls), len(h.prov.posted))
	}
}

// TestChunkedMaxChunksReached: files left after review.max_chunks parts are
// not reviewed, and the hint names review.max_chunks (and diff.max_tokens,
// the limit that applied); without the cap it names the window only.
func TestChunkedMaxChunksReached(t *testing.T) {
	h, f := chunkHarness(t, nil)
	h.deps.Config.Review.MaxChunks = 2
	res := runChunked(t, h, Args{})
	c := res.Coverage
	if len(f.calls) != 2 || c.ModelCalls != 2 || !c.Partial || c.ReviewedFiles != 4 || c.NotReviewedFiles != 2 ||
		!slices.Equal(c.Omitted.Modified, partFiles(3)) {
		t.Errorf("calls %d, coverage %+v", len(f.calls), c)
	}
	if !slices.Contains(res.Notes, llmrun.NoteRaiseChunksLimit) || slices.Contains(res.Notes, llmrun.NoteRaiseLimit) {
		t.Errorf("notes = %q", res.Notes)
	}

	h, _ = chunkHarness(t, nil)
	h.deps.Config.Diff.MaxTokens = nil
	h.prov.files = bigFiles(30)
	h.deps.Config.LLM.ContextWindow = 8000
	h.deps.Config.Review.MaxChunks = 2
	res = runChunked(t, h, Args{})
	if !res.Coverage.Partial || !slices.Contains(res.Notes, llmrun.NoteRaiseChunksWindow) {
		t.Errorf("window: partial %v, notes %q", res.Coverage.Partial, res.Notes)
	}

	// The packing did not reach max_chunks: the plain hint.
	notes := llmrun.ChunkedPartialNotes(&Coverage{Omitted: OmittedFiles{Added: []string{"a.go"}}}, tokens.Budget{ContextWindow: 32000, PromptTokens: 1000, Factor: 0.3}, false)
	if !slices.Equal(notes, []string{llmrun.NoteLargerWindow}) {
		t.Errorf("notes without max_chunks reached = %q", notes)
	}
}

// TestChunkedRespectsDiffCap: diff.max_tokens bounds each part's diff.
func TestChunkedRespectsDiffCap(t *testing.T) {
	h, _ := chunkHarness(t, nil)
	h.prov.files = bigFiles(30)
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.parts) != 8 {
		t.Fatalf("%d parts, want 8 (review.max_chunks)", len(pl.parts))
	}
	for i, p := range pl.parts {
		if p.prep.Tokens > 2000+500 {
			t.Errorf("part %d: %d diff tokens exceed the cap", i+1, p.prep.Tokens)
		}
	}
	c := pl.Result.Coverage
	if c.ModelCalls != 8 || c.ReviewedFiles != 16 || c.TotalFiles != 30 {
		t.Errorf("dry-run coverage = %+v", c)
	}
}

// TestChunkedTooLarge: under large_patch_policy skip, a file too large for a
// part of its own is skipped as too_large, counted as not reviewed, and the
// budget hint (not the provider note) applies.
func TestChunkedTooLarge(t *testing.T) {
	h, f := chunkHarness(t, nil)
	h.deps.Config.Diff.LargePatchPolicy = "skip"
	huge := provider.FilePatch{Path: "src/huge.go", Type: provider.ChangeModified,
		Patch:      "@@ -1,2 +1,400 @@\n a\n" + strings.Repeat("+some added line of code here\n", 400),
		BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap}
	h.prov.files = append(bigFiles(4), huge)
	res := runChunked(t, h, Args{})
	c := res.Coverage
	if !slices.Contains(c.Skipped, SkippedFile{Path: "src/huge.go", Reason: diffpipe.SkipTooLarge}) || !c.Partial ||
		c.ReviewedFiles != 4 || c.NotReviewedFiles != 1 || len(f.calls) != 2 {
		t.Errorf("calls %d, coverage %+v", len(f.calls), c)
	}
	if slices.Contains(res.Notes, llmrun.NoteProviderSkips) || !slices.Contains(res.Notes, llmrun.NoteRaiseLimit) {
		t.Errorf("notes = %q", res.Notes)
	}
}

// TestMaxChunksOneIsOneCall: with review.max_chunks 1 a large PR is one
// call without the part line, as in v1.0; a PR that fits one call gives the
// same result and prompts with any review.max_chunks.
func TestMaxChunksOneIsOneCall(t *testing.T) {
	h, f := chunkHarness(t, nil)
	res := runChunked(t, h, Args{MaxChunks: 1})
	if len(f.calls) != 1 || strings.Contains(f.calls[0].user, "This pull request is large") ||
		res.Coverage.ModelCalls != 1 || !res.Coverage.Partial || !slices.Equal(h.stages[2:], []string{StageCallingModel}) {
		t.Errorf("calls %d, coverage %+v, stages %q", len(f.calls), res.Coverage, h.stages)
	}

	run := func(maxChunks int) ([]byte, []llmCall) {
		h := newHarness(goodAnswer)
		res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, MaxChunks: maxChunks, Publish: true})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res)
		return raw, h.llm.calls
	}
	one, oneCalls := run(1)
	eight, eightCalls := run(8)
	if string(one) != string(eight) || !slices.Equal(oneCalls, eightCalls) {
		t.Errorf("max_chunks 1 and 8 differ on a PR that fits one call:\n%s\n%s", one, eight)
	}
}

func ptr[T any](v T) *T { return &v }
