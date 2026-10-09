package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
)

const (
	partialSentenceText = "If the result says the review is partial, tell the user how many files were not reviewed and never state that those files have no issues."
	// One of the two changed files cannot be read (setGhost).
	reviewBanner = "**Partial review: 1 of 2 changed files was reviewed. 1 file was not reviewed (see Coverage); nothing is concluded about it.**"
	askBanner    = "**Partial answer: 1 of 2 changed files was used for this answer. 1 file was not reviewed (see Coverage); nothing is concluded about it.**"
)

// toolsByName lists the tools of a server built from deps.
func toolsByName(t *testing.T, deps Deps) map[string]string {
	t.Helper()
	cs := connect(t, deps)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, tl := range list.Tools {
		out[tl.Name] = tl.Description
	}
	return out
}

// TestPartialSentenceInToolDescriptions: pr_review, pr_ask, pr_improve and
// job_result tell the client model what to do with a partial result, in stdio mode (with
// the job sentence after it) and in serve mode.
func TestPartialSentenceInToolDescriptions(t *testing.T) {
	stdio := realDeps(validEnv(), nil)
	stdio = withJobs(t, stdio, nil)
	serve := realDeps(validEnv(), nil)
	serve.Serve = true
	for mode, tools := range map[string]map[string]string{"stdio": toolsByName(t, stdio), "serve": toolsByName(t, serve)} {
		names := []string{"pr_review", "pr_ask", "pr_improve"}
		if mode == "stdio" {
			names = append(names, "job_result")
		}
		for _, name := range names {
			d, ok := tools[name]
			if !ok || !strings.Contains(d, " "+partialSentenceText) {
				t.Errorf("%s %s: description lacks the partial sentence: %q", mode, name, d)
			}
		}
		if _, ok := tools["job_result"]; ok == (mode == "serve") {
			t.Errorf("%s: job_result registered = %v", mode, ok)
		}
		// pr_describe (and job_result, which returns its results) carry the
		// "described" sentence of Y-7.
		describeNames := []string{"pr_describe"}
		if mode == "stdio" {
			describeNames = append(describeNames, "job_result")
		}
		for _, name := range describeNames {
			if d := tools[name]; !strings.Contains(d, describePartialSentence) {
				t.Errorf("%s %s: description lacks the describe partial sentence: %q", mode, name, d)
			}
		}
		// The job sentence of stdio mode follows the partial sentence.
		if mode == "stdio" && !strings.Contains(tools["pr_review"], partialSentenceText+" A review that takes longer") {
			t.Errorf("stdio pr_review description order: %q", tools["pr_review"])
		}
	}
}

// coverageSchemas collects the "coverage" object schemas found anywhere in a
// JSON schema, so the oneOf branches and the plain schemas are all checked.
func coverageSchemas(v any, out *[]map[string]any) {
	switch x := v.(type) {
	case map[string]any:
		if props, ok := x["properties"].(map[string]any); ok {
			if c, ok := props["coverage"].(map[string]any); ok {
				*out = append(*out, c)
			}
		}
		for _, c := range x {
			coverageSchemas(c, out)
		}
	case []any:
		for _, c := range x {
			coverageSchemas(c, out)
		}
	}
}

// TestOutputSchemasCarryThePartialFields: every output schema that has a
// coverage object (pr_review, pr_ask, pr_describe and pr_improve, plain and
// stdio oneOf, and the four result branches of job_result) declares the four X-18 fields, the X-20 list
// deleted_listed and the X-19 counts model_calls and failed_parts as
// required, and the RC-9 object repo_context with its five required fields.
func TestOutputSchemasCarryThePartialFields(t *testing.T) {
	types := map[string]string{"partial": "boolean", "reviewed_files": "integer", "total_files": "integer", "not_reviewed_files": "integer",
		"deleted_listed": "array", "model_calls": "integer", "failed_parts": "integer", "repo_context": "object"}
	check := func(name string, schema any, want int) {
		t.Helper()
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		var covs []map[string]any
		coverageSchemas(v, &covs)
		if len(covs) != want {
			t.Errorf("%s: %d coverage schemas, want %d", name, len(covs), want)
		}
		for _, c := range covs {
			props, _ := c["properties"].(map[string]any)
			required, _ := c["required"].([]any)
			for field, typ := range types {
				p, _ := props[field].(map[string]any)
				// The schema inferred for ask.Result types a slice as
				// ["null", "array"], as it does every other list.
				ts, _ := p["type"].([]any)
				nullableArray := typ == "array" && slices.Contains(ts, any(typ))
				if p["type"] != typ && !nullableArray {
					t.Errorf("%s: coverage.%s = %v, want type %s", name, field, p, typ)
				}
				if items, _ := p["items"].(map[string]any); typ == "array" && items["type"] != "string" {
					t.Errorf("%s: coverage.%s items = %v, want strings", name, field, p["items"])
				}
				found := false
				for _, r := range required {
					found = found || r == field
				}
				if !found {
					t.Errorf("%s: coverage.%s is not required", name, field)
				}
			}
			rc, _ := props["repo_context"].(map[string]any)
			rcProps, _ := rc["properties"].(map[string]any)
			rcReq, _ := rc["required"].([]any)
			for field, typ := range map[string]string{"status": "string", "reason": "string", "symbols": "integer", "references": "integer", "files": "integer"} {
				if p, _ := rcProps[field].(map[string]any); p["type"] != typ {
					t.Errorf("%s: coverage.repo_context.%s = %v, want type %s", name, field, p, typ)
				}
				if !slices.Contains(rcReq, any(field)) {
					t.Errorf("%s: coverage.repo_context.%s is not required", name, field)
				}
			}
		}
	}
	serve := Deps{Serve: true}
	check("serve pr_review", reviewOutputSchema(serve), 1)
	check("serve pr_ask", askOutputSchema(serve), 1)
	stdio := withJobs(t, Deps{}, nil)
	check("stdio pr_review", reviewOutputSchema(stdio), 1)
	check("stdio pr_ask", askOutputSchema(stdio), 1)
	check("serve pr_describe", describeOutputSchema(serve), 1)
	check("stdio pr_describe", describeOutputSchema(stdio), 1)
	check("serve pr_improve", improveOutputSchema(serve), 1)
	check("stdio pr_improve", improveOutputSchema(stdio), 1)

	cs := connect(t, withJobs(t, realDeps(validEnv(), nil), nil))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range list.Tools {
		switch tl.Name {
		case "pr_review", "pr_ask":
			check("listed "+tl.Name, tl.OutputSchema, 1)
		case "pr_describe", "pr_improve":
			check("listed "+tl.Name, tl.OutputSchema, 1)
		case "job_result":
			check("listed job_result", tl.OutputSchema, 4)
		}
	}
}

// TestPartialReviewEndToEnd: a file the provider cannot read makes the
// review partial. The banner is the first line of the tool text, the
// structured coverage carries the counts, and the published overview has the
// banner right under its heading, also after the in-place edit of a second
// run (X-12).
func TestPartialReviewEndToEnd(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	g.setGhost()
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	call := func() (string, review.Result) {
		t.Helper()
		res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "publish": true})
		if res.IsError {
			t.Fatalf("tool error: %s", textOf(t, res))
		}
		var got review.Result
		decodeStructured(t, res, &got)
		return textOf(t, res), got
	}
	text, got := call()
	if first, _, _ := strings.Cut(text, "\n"); first != reviewBanner {
		t.Errorf("first line of the tool text = %q", first)
	}
	c := got.Coverage
	if !c.Partial || c.ReviewedFiles != 1 || c.NotReviewedFiles != 1 || c.TotalFiles != 2 ||
		c.ReviewedFiles+c.NotReviewedFiles != c.TotalFiles {
		t.Errorf("coverage = %+v", c)
	}
	if want := render.Client(&got); want != text {
		t.Errorf("text is not the client rendering of the structured result")
	}
	// The provider skip has its own hint; no budget setting is named.
	notes := strings.Join(got.Notes, "\n")
	if !strings.Contains(notes, "could not be read") || strings.Contains(notes, "diff.max_tokens") {
		t.Errorf("notes = %q", got.Notes)
	}

	wantHead := "## PR Review 🔍\n\n> ⚠️ " + reviewBanner + "\n\n"
	for run := 1; run <= 2; run++ {
		if run == 2 {
			if _, again := call(); again.Publish == nil || !again.Publish.Updated {
				t.Fatalf("the second run did not update the overview: %+v", again.Publish)
			}
		}
		ov := g.overviewBodies()
		if len(ov) != 1 || !strings.HasPrefix(ov[0], wantHead) {
			t.Errorf("run %d: overview head: %q", run, ov)
		}
	}
}

// commentBodies returns the bodies of all PR-level comments, by id order.
func (f *fakeServer) commentBodies() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := int64(1); id <= f.nextComment; id++ {
		if c, ok := f.comments[id]; ok {
			out = append(out, c.body)
		}
	}
	return out
}

// TestCompleteReviewHasNoBanner: the same flow without the unreadable file.
func TestCompleteReviewHasNoBanner(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g)})
	var got review.Result
	decodeStructured(t, res, &got)
	if c := got.Coverage; c.Partial || c.ReviewedFiles != 1 || c.NotReviewedFiles != 0 || c.TotalFiles != 1 {
		t.Errorf("coverage = %+v", c)
	}
	if text := textOf(t, res); !strings.HasPrefix(text, "## PR Review") || strings.Contains(text, "Partial") {
		t.Errorf("unexpected text:\n%s", text)
	}
}

// TestPartialAskEndToEnd: pr_ask uses the "answer" banner in its text and in
// the published comment, and the structured coverage carries the counts.
func TestPartialAskEndToEnd(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, askAnswer)
	g.setGhost()
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	res := callTool(t, cs, "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "publish": true})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	var got ask.Result
	decodeStructured(t, res, &got)
	if first, _, _ := strings.Cut(textOf(t, res), "\n"); first != askBanner {
		t.Errorf("first line of the tool text = %q", first)
	}
	c := got.Coverage
	if !c.Partial || c.ReviewedFiles != 1 || c.NotReviewedFiles != 1 || c.TotalFiles != 2 {
		t.Errorf("coverage = %+v", c)
	}
	if posted := g.commentBodies(); len(posted) != 1 || !strings.HasPrefix(posted[0], "> ⚠️ "+askBanner+"\n\n### **Ask**") {
		t.Errorf("the published answer does not start with the warning banner: %q", posted)
	}
}

// TestJobResultCarriesThePartialBanner: job_result returns the original
// result unchanged (X-16), so the banner and the counts are the same as in
// the synchronous call.
func TestJobResultCarriesThePartialBanner(t *testing.T) {
	for _, tc := range []struct {
		tool, noun, answer, banner string
		args                       func(pr string) map[string]any
	}{
		{"pr_review", "review", goodAnswer, reviewBanner, func(pr string) map[string]any {
			return map[string]any{"pr_url": pr, "wait_seconds": 0}
		}},
		{"pr_ask", "answer", askAnswer, askBanner, func(pr string) map[string]any {
			return map[string]any{"pr_url": pr, "question": askQuestion, "wait_seconds": 0}
		}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			g := newFakeGiteaHost(t)
			g.setGhost()
			l, gate := newGatedLLMHost(t, 200, tc.answer)
			deps := realDeps(reviewEnv(g, l), nil)
			c := startRaw(t, withJobs(t, deps, nil))
			st := mustRunning(t, c.tool(tc.tool, tc.args(reviewPRURL(g)), nil), tc.noun)
			gate.release()
			got := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, nil))
			if first, _, _ := strings.Cut(got.text(), "\n"); first != tc.banner {
				t.Errorf("first line of the job_result text = %q", first)
			}
			var raw map[string]any
			if err := json.Unmarshal(got.Structured, &raw); err != nil {
				t.Fatal(err)
			}
			c2, _ := raw["coverage"].(map[string]any)
			if c2["partial"] != true || c2["reviewed_files"] != 1.0 || c2["not_reviewed_files"] != 1.0 || c2["total_files"] != 2.0 {
				t.Errorf("job_result coverage = %v", c2)
			}
		})
	}
}
