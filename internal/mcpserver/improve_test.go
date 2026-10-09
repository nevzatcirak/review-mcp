package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/improve"
	improverender "github.com/nevzatcirak/review-mcp/internal/improve/render"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// improveSummaryMarker is in the fake model's suggestion: it reaches the
// tool result, never the logs.
const improveSummaryMarker = "IMPROVE-SUMMARY-MARKER-6d0a2c"

// improveAnswer is a pr_improve suggestion answer for the fake Gitea PR
// (one file, src/app.go).
const improveAnswer = "```yaml\ncode_suggestions:\n- relevant_file: |\n    src/app.go\n  language: |\n    go\n" +
	"  existing_code: |\n    var a = 2\n  suggestion_content: |\n    Name the constant.\n" +
	"  improved_code: |\n    const answer = 2\n  one_sentence_summary: |\n    Name the constant " + improveSummaryMarker + "\n" +
	"  label: |\n    general\n```\n"

// improveReflection scores improveAnswer's suggestion.
const improveReflection = "```yaml\ncode_suggestions:\n- suggestion_number: 1\n  relevant_file: src/app.go\n" +
	"  relevant_lines_start: 2\n  relevant_lines_end: 2\n  suggestion_score: 8\n  why: |\n    Clearer.\n```\n"

// newImproveLLMHost answers a pr_improve self-review call (its prompt opens
// with the diff sentence of the self-review) with reflection, and any other
// call with suggestions, or, when suggestions is empty, with one suggestion
// per "src/partN.go" file of the prompt's diff.
func newImproveLLMHost(t *testing.T, suggestions, reflection string) *fakeLLMHost {
	t.Helper()
	f := &fakeLLMHost{status: http.StatusOK}
	fileRE := regexp.MustCompile(`(?m)^## File: '(src/part\d\.go)'$`)
	f.srv, f.conns = startCounted(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := f.record(r)
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal([]byte(body), &req)
		user := ""
		if n := len(req.Messages); n > 0 {
			user = req.Messages[n-1].Content
		}
		answer := suggestions
		switch {
		case strings.HasPrefix(user, "You are given a Pull Request (PR) code diff:"):
			answer = reflection
		case answer == "":
			var b strings.Builder
			b.WriteString("```yaml\ncode_suggestions:\n")
			for _, m := range fileRE.FindAllStringSubmatch(user, -1) {
				b.WriteString("- relevant_file: " + m[1] + "\n  language: go\n  existing_code: some added line of code number 1\n" +
					"  suggestion_content: Explain it.\n  improved_code: some added line of code number 1 // why\n" +
					"  one_sentence_summary: Explain " + m[1] + "\n  label: general\n")
			}
			b.WriteString("```\n")
			answer = b.String()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestPRImproveToolDefinition(t *testing.T) {
	cs := connect(t, realDeps(validEnv(), nil))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var tl *mcp.Tool
	for _, x := range list.Tools {
		if x.Name == "pr_improve" {
			tl = x
		}
	}
	if tl == nil {
		t.Fatal("pr_improve is not registered")
	}
	if tl.Description != prImproveDescription || strings.Contains(tl.Description, "not available yet") ||
		!strings.Contains(tl.Description, "Publishing (publish=true) writes to the pull request: one overview comment") ||
		!strings.Contains(tl.Description, "inline comment") {
		t.Errorf("description = %q", tl.Description)
	}
	// As pr_review: publish=true writes, so the tool is not read-only and not
	// idempotent, and it deletes nothing.
	a := tl.Annotations
	if a == nil || a.ReadOnlyHint || a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint ||
		a.OpenWorldHint == nil || !*a.OpenWorldHint {
		t.Errorf("annotations = %+v", a)
	}
	raw, _ := json.Marshal(tl.InputSchema)
	var in struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	var props []string
	for k, v := range in.Properties {
		props = append(props, k)
		if v["description"] == nil || v["description"] == "" {
			t.Errorf("property %s has no description", k)
		}
	}
	sort.Strings(props)
	if got := strings.Join(props, ","); got != "output_language,pr_url,publish,wait_seconds" {
		t.Errorf("properties = %s", got)
	}
	if strings.Join(in.Required, ",") != "pr_url" {
		t.Errorf("required = %v, want pr_url", in.Required)
	}
	rawOut, _ := json.Marshal(tl.OutputSchema)
	var out struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(rawOut, &out); err != nil {
		t.Fatal(err)
	}
	var outProps []string
	for k := range out.Properties {
		outProps = append(outProps, k)
	}
	sort.Strings(outProps)
	if got := strings.Join(outProps, ","); got != "coverage,metadata,notes,publish,suggestions" {
		t.Errorf("output schema properties = %s", got)
	}
}

// TestPRImproveCall: through the real wiring, pr_improve reads the PR, its
// diff and its discussion, makes the suggestion call and the self-review
// call, writes nothing to the PR, and returns the client rendering of its
// structured result.
func TestPRImproveCall(t *testing.T) {
	g, l := newFakeGiteaHost(t), newImproveLLMHost(t, improveAnswer, improveReflection)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))

	res := callTool(t, cs, "pr_improve", map[string]any{"pr_url": reviewPRURL(g), "output_language": "tr-TR"})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	text := textOf(t, res)
	var got improve.Result
	decodeStructured(t, res, &got)
	if want := improverender.Client(&got); want != text {
		t.Errorf("text is not the client rendering of the structured result:\n%s\n---\n%s", text, want)
	}
	if len(got.Suggestions) != 1 || !strings.Contains(got.Suggestions[0].Summary, improveSummaryMarker) ||
		got.Suggestions[0].Score == nil || *got.Suggestions[0].Score != 8 || got.Metadata.LLMCalls != 2 ||
		got.Metadata.SelfReviewCalls != 1 {
		t.Errorf("structured = %+v", got)
	}
	if w := g.writeLog(); len(w) != 0 {
		t.Errorf("pr_improve wrote to the provider: %v", w)
	}
	reqs := l.recorded()
	if len(reqs) != 2 {
		t.Fatalf("LLM requests = %d, want 2", len(reqs))
	}
	for _, m := range []string{descMarker, titleMarker, branchMarker, diffMarker, "locale code: 'tr-TR'"} {
		if !strings.Contains(reqs[0], m) {
			t.Errorf("the suggestion request lacks %q", m)
		}
	}
	if !strings.Contains(reqs[1], improveSummaryMarker) || !strings.Contains(reqs[1], "locale code: 'tr-TR'") {
		t.Errorf("the self-review request lacks the suggestion or the language")
	}
}

// TestPRImprovePublish runs the whole publish flow end to end with the real
// Gitea provider: the first call posts the overview with its marker, posts
// the verified suggestion as an inline comment (a review with one comment
// carrying its fingerprint marker) and edits the overview with the link; the
// second call edits the same overview in place and posts nothing else, the
// suggestion being already on the PR. A foreign comment carrying the
// overview marker is never touched.
func TestPRImprovePublish(t *testing.T) {
	answer := strings.Replace(improveAnswer, "existing_code: |\n    var a = 2\n", "existing_code: |\n    var a = 2 // "+diffMarker+"\n", 1)
	g, l := newFakeGiteaHost(t), newImproveLLMHost(t, answer, improveReflection)
	foreignBody := "Not ours.\n\n" + improve.OverviewMarker
	g.plantComment("mallory", 77, foreignBody)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	call := func() improve.Result {
		t.Helper()
		res := callTool(t, cs, "pr_improve", map[string]any{"pr_url": reviewPRURL(g), "publish": true})
		if res.IsError {
			t.Fatalf("tool error: %s", textOf(t, res))
		}
		var got improve.Result
		decodeStructured(t, res, &got)
		return got
	}
	const api = "/api/v1/repos/octo/demo"
	first := call()
	if p := first.Publish; p == nil || !p.Published || p.Updated || p.CommentID != "56" || p.Error != "" ||
		p.Inline == nil || *p.Inline != (improve.InlineSummary{Posted: 1}) {
		t.Fatalf("first publish = %+v", first.Publish)
	}
	if len(first.Suggestions) != 1 || !first.Suggestions[0].Verified || first.Suggestions[0].Anchor == nil ||
		first.Suggestions[0].Anchor.Status != improve.AnchorPosted || first.Suggestions[0].Anchor.Line != 2 ||
		first.Suggestions[0].Anchor.URL == "" {
		t.Fatalf("suggestion = %+v", first.Suggestions)
	}
	wantFirst := []string{"POST " + api + "/issues/7/comments", "POST " + api + "/pulls/7/reviews", "PATCH " + api + "/issues/comments/56"}
	if got := g.writeLog(); !slices.Equal(got, wantFirst) {
		t.Errorf("first writes %v, want %v", got, wantFirst)
	}
	var overviews []string
	for _, b := range g.commentBodies() {
		if llmrun.HasMarkerLastLine(b, improve.OverviewMarker) {
			overviews = append(overviews, b)
		}
	}
	if len(overviews) != 2 || overviews[0] != foreignBody || !strings.Contains(overviews[1], "## Code Suggestions 💡") ||
		!strings.Contains(overviews[1], first.Suggestions[0].Anchor.URL) || !strings.Contains(overviews[1], improveSummaryMarker) {
		t.Fatalf("overviews after the first call: %q", overviews)
	}
	if reviews := g.reviewBodies(); len(reviews) != 1 || !strings.Contains(reviews[0], "```diff\n-var a = 2 // "+diffMarker) ||
		!strings.Contains(reviews[0], "(review-mcp:suggestion:") {
		t.Fatalf("inline comments = %q", reviews)
	}

	second := call()
	if p := second.Publish; p == nil || !p.Published || !p.Updated || p.CommentID != "56" ||
		p.Inline == nil || *p.Inline != (improve.InlineSummary{SkippedDuplicate: 1}) {
		t.Errorf("second publish = %+v", second.Publish)
	}
	if a := second.Suggestions[0].Anchor; a == nil || a.Status != improve.AnchorSkippedDuplicate {
		t.Errorf("second anchor = %+v", a)
	}
	if got, want := g.writeLog(), append(wantFirst, "PATCH "+api+"/issues/comments/56"); !slices.Equal(got, want) {
		t.Errorf("writes %v, want %v", got, want)
	}
}

// TestPRImproveArgumentErrors: invalid arguments fail with fixed sentences
// before any request to the provider or the LLM.
func TestPRImproveArgumentErrors(t *testing.T) {
	g, l := newFakeGiteaHost(t), newImproveLLMHost(t, improveAnswer, improveReflection)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"output_language": "Turkish"}, tools.InvalidOutputLanguageMessage},
		{map[string]any{"output_language": "Turkish", "publish": true}, tools.InvalidOutputLanguageMessage},
	} {
		tc.args["pr_url"] = reviewPRURL(g)
		res := callTool(t, cs, "pr_improve", tc.args)
		if !res.IsError || textOf(t, res) != tc.want {
			t.Errorf("%v: isError=%v text=%q, want %q", tc.args, res.IsError, textOf(t, res), tc.want)
		}
	}
	if g.hits.Load() != 0 || l.hits.Load() != 0 {
		t.Errorf("requests were sent: provider %d, LLM %d", g.hits.Load(), l.hits.Load())
	}
}

// TestPRImproveJobs (X-16): a fast pr_improve answers exactly as the
// synchronous path does; a slow one answers with a job id, and job_result
// returns the same bytes as the synchronous call.
func TestPRImproveJobs(t *testing.T) {
	args := func(pr string, wait int) map[string]any {
		return map[string]any{"pr_url": pr, "wait_seconds": wait}
	}
	t.Run("fast", func(t *testing.T) {
		g, l := newFakeGiteaHost(t), newImproveLLMHost(t, improveAnswer, improveReflection)
		deps := realDeps(reviewEnv(g, l), nil)
		want := startRaw(t, deps).tool("pr_improve", args(reviewPRURL(g), 30), nil)
		if r := decodeRaw(t, want); r.IsError || len(r.Structured) == 0 {
			t.Fatalf("synchronous call failed: %s", want)
		}
		got := startRaw(t, withJobs(t, deps, nil)).tool("pr_improve", args(reviewPRURL(g), 30), nil)
		assertSameBytes(t, "fast result", got, want)
	})
	t.Run("slow", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		// The gated host gives the same answer to both calls: the
		// self-review then matches nothing and the suggestion is unscored.
		l, gate := newGatedLLMHost(t, 200, improveAnswer)
		deps := realDeps(reviewEnv(g, l), nil)
		c := startRaw(t, withJobs(t, deps, nil))
		st := mustRunning(t, c.tool("pr_improve", args(reviewPRURL(g), 0), nil), "suggestion job")
		gate.waitArrived(t, 1)
		again := mustRunning(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 0}, nil), "suggestion job")
		if again.Stage != "calling model" {
			t.Errorf("stage = %q", again.Stage)
		}
		gate.release()
		got := c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, nil)
		want := startRaw(t, deps).tool("pr_improve", args(reviewPRURL(g), 30), nil)
		if r := decodeRaw(t, want); r.IsError {
			t.Fatalf("synchronous call failed: %s", want)
		}
		assertSameBytes(t, "job_result", got, want)
		var res improve.Result
		if err := json.Unmarshal(decodeRaw(t, got).Structured, &res); err != nil || len(res.Suggestions) != 1 {
			t.Errorf("job_result structured content is not a suggestion result: %v", err)
		}
	})
}

// TestImproveInPartsAsBackgroundJob: through the stdio server, a run in
// three parts runs as a background job; job_result reports a model stage
// and a scoring stage per part, and returns a result whose coverage counts
// every part. Nothing is written to the PR.
func TestImproveInPartsAsBackgroundJob(t *testing.T) {
	g, l := newFakeGiteaHost(t), newImproveLLMHost(t, "", "```yaml\ncode_suggestions:\n- suggestion_number: 1\n  suggestion_score: 9\n  why: Fine.\n```\n")
	g.large = largeFiles()
	env := reviewEnv(g, l)
	env["REVIEW_MCP_DIFF_MAX_TOKENS"] = "1000"
	c := startRaw(t, withJobs(t, realDeps(env, nil), nil))

	st := mustRunning(t, c.tool("pr_improve", map[string]any{"pr_url": reviewPRURL(g), "wait_seconds": 0}, nil), "suggestion job")
	r := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 60}, "tok-poll"))
	if r.IsError {
		t.Fatalf("job_result: %s", r.text())
	}
	want := "fetching|preparing diff|calling model (part 1 of 3)|scoring suggestions (part 1 of 3)|" +
		"calling model (part 2 of 3)|scoring suggestions (part 2 of 3)|calling model (part 3 of 3)|scoring suggestions (part 3 of 3)|rendering"
	if got := strings.Join(c.progressOf("tok-poll"), "|"); got != want {
		t.Errorf("stages = %q, want %q", got, want)
	}
	var res improve.Result
	if err := json.Unmarshal(r.Structured, &res); err != nil {
		t.Fatal(err)
	}
	if cov := res.Coverage; cov.ModelCalls != 3 || cov.Partial || cov.ReviewedFiles != 3 || len(res.Suggestions) != 3 {
		t.Errorf("coverage = %+v, suggestions %d", cov, len(res.Suggestions))
	}
	if res.Metadata.LLMCalls != 6 || res.Metadata.SelfReviewCalls != 3 || len(l.recorded()) != 6 {
		t.Errorf("llm_calls %d, self_review_calls %d, requests %d", res.Metadata.LLMCalls, res.Metadata.SelfReviewCalls, len(l.recorded()))
	}
	if !strings.Contains(r.text(), "- Reviewed in 3 model calls.") {
		t.Errorf("the client text lacks the model-calls line:\n%s", r.text())
	}
	if w := g.writeLog(); len(w) != 0 {
		t.Errorf("pr_improve wrote to the provider: %v", w)
	}
}

// TestImproveStagesShareTheReviewFormat: the part stages of pr_improve are
// pr_review's text, so the progress reporter sizes its total from them; the
// scoring stages are not part stages.
func TestImproveStagesShareTheReviewFormat(t *testing.T) {
	if improve.CallingModelPart(2, 3) != review.CallingModelPart(2, 3) {
		t.Errorf("part stage %q differs from pr_review's %q", improve.CallingModelPart(2, 3), review.CallingModelPart(2, 3))
	}
	if _, ok := review.StageParts(improve.ScoringPart(2, 3)); ok {
		t.Errorf("a scoring stage parses as a part stage")
	}
	if !improve.IsScoringStage(improve.ScoringPart(2, 3)) || !improve.IsScoringStage(improve.StageScoring) ||
		improve.IsScoringStage(improve.StageCallingModel) {
		t.Errorf("IsScoringStage")
	}
}
