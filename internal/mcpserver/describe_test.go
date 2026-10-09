package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/describe"
	describerender "github.com/nevzatcirak/review-mcp/internal/describe/render"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// describeTitleMarker is in the fake model's answer: it reaches the tool
// result, never the logs.
const describeTitleMarker = "DESCRIBE-TITLE-MARKER-3e9b1f"

// describeAnswer is a pr_describe answer for the fake Gitea PR (one file,
// src/app.go).
const describeAnswer = "```yaml\ntype:\n- Enhancement\ndescription: |\n  - Change the constant\n" +
	"title: |\n  Change the constant " + describeTitleMarker + "\n" +
	"pr_files:\n- filename: |\n    src/app.go\n  changes_summary: |\n    - Set `a` to 2\n" +
	"  changes_title: |\n    Change the constant\n  label: |\n    enhancement\n```\n"

func TestPRDescribeToolDefinition(t *testing.T) {
	cs := connect(t, realDeps(validEnv(), nil))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var tl *mcp.Tool
	for _, x := range list.Tools {
		if x.Name == "pr_describe" {
			tl = x
		}
	}
	if tl == nil {
		t.Fatal("pr_describe is not registered")
	}
	const want = "Describes a pull request with the configured LLM: a title, the change types, a short summary and a walkthrough of the changed files. By default it only reads: nothing is written to the pull request. Publishing (publish=true) writes to the pull request: publish_mode=comment (default) posts one comment and edits it in place on later runs; publish_mode=description writes a marked region at the end of the pull request description, leaves the rest of the description unchanged and, with update_title=true, also replaces the title. The PR's title, description, branch names, commit messages and diff are sent to the configured LLM endpoint. If the result says the description is partial, tell the user how many files were not described and never present the walkthrough as covering those files."
	if tl.Description != want {
		t.Errorf("description = %q", tl.Description)
	}
	// As pr_review: publishing writes to the pull request, so the tool is
	// not read-only, and it is not destructive (it edits its own comment or
	// region only).
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
	if got := strings.Join(props, ","); got != "output_language,pr_url,publish,publish_mode,update_title,wait_seconds" {
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
	if got := strings.Join(outProps, ","); got != "coverage,description,files,metadata,notes,publish,title,type" {
		t.Errorf("output schema properties = %s", got)
	}
}

// TestPRDescribeCall: through the real wiring, pr_describe reads the PR, its
// diff and its commits, makes one model call, writes nothing to the PR, and
// returns the client rendering of its structured result.
func TestPRDescribeCall(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, describeAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))

	res := callTool(t, cs, "pr_describe", map[string]any{"pr_url": reviewPRURL(g), "output_language": "tr-TR"})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	text := textOf(t, res)
	var got describe.Result
	decodeStructured(t, res, &got)
	if want := describerender.Client(&got); want != text {
		t.Errorf("text is not the client rendering of the structured result:\n%s\n---\n%s", text, want)
	}
	if got.Title == nil || !strings.Contains(*got.Title, describeTitleMarker) || len(got.Files) != 1 ||
		got.Files[0].Path != "src/app.go" || got.Metadata.LLMCalls != 1 || got.Metadata.CommitMessages != 1 {
		t.Errorf("structured = %+v", got)
	}
	if w := g.writeLog(); len(w) != 0 {
		t.Errorf("pr_describe wrote to the provider: %v", w)
	}
	found := false
	for _, r := range g.requestLog() {
		found = found || strings.HasSuffix(r, "/pulls/7/commits")
	}
	if !found {
		t.Errorf("the commit messages were not read: %v", g.requestLog())
	}
	body := strings.Join(l.recorded(), "\n")
	for _, m := range []string{descMarker, titleMarker, branchMarker, diffMarker, "Change the constant", "locale code: 'tr-TR'"} {
		if !strings.Contains(body, m) {
			t.Errorf("the LLM request lacks %q", m)
		}
	}
}

// TestPRDescribeArgumentErrors: invalid arguments fail with fixed sentences
// before any request to the provider or the LLM. (publish=true is no longer
// refused: WP-2d.)
func TestPRDescribeArgumentErrors(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, describeAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"publish_mode": "wiki"}, tools.InvalidPublishModeMessage},
		{map[string]any{"update_title": true}, tools.InvalidUpdateTitleMessage},
		{map[string]any{"publish": true, "update_title": true}, tools.InvalidUpdateTitleMessage},
		{map[string]any{"output_language": "Turkish"}, tools.InvalidOutputLanguageMessage},
	} {
		tc.args["pr_url"] = reviewPRURL(g)
		res := callTool(t, cs, "pr_describe", tc.args)
		if !res.IsError || textOf(t, res) != tc.want {
			t.Errorf("%v: isError=%v text=%q, want %q", tc.args, res.IsError, textOf(t, res), tc.want)
		}
	}
	if g.hits.Load() != 0 || l.hits.Load() != 0 {
		t.Errorf("requests were sent: provider %d, LLM %d", g.hits.Load(), l.hits.Load())
	}
}

// TestPRDescribeJobs (X-16): a fast pr_describe answers exactly as the
// synchronous path does; a slow one answers with a job id ("The description
// is still running ..."), and job_result returns the same bytes as the
// synchronous call.
func TestPRDescribeJobs(t *testing.T) {
	args := func(pr string, wait int) map[string]any {
		return map[string]any{"pr_url": pr, "wait_seconds": wait}
	}
	t.Run("fast", func(t *testing.T) {
		g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, describeAnswer)
		deps := realDeps(reviewEnv(g, l), nil)
		want := startRaw(t, deps).tool("pr_describe", args(reviewPRURL(g), 30), nil)
		if r := decodeRaw(t, want); r.IsError || len(r.Structured) == 0 {
			t.Fatalf("synchronous call failed: %s", want)
		}
		got := startRaw(t, withJobs(t, deps, nil)).tool("pr_describe", args(reviewPRURL(g), 30), nil)
		assertSameBytes(t, "fast result", got, want)
	})
	t.Run("slow", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		l, gate := newGatedLLMHost(t, 200, describeAnswer)
		deps := realDeps(reviewEnv(g, l), nil)
		c := startRaw(t, withJobs(t, deps, nil))
		st := mustRunning(t, c.tool("pr_describe", args(reviewPRURL(g), 0), nil), "description")
		gate.waitArrived(t, 1)
		again := mustRunning(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 0}, nil), "description")
		if again.Stage != "calling model" {
			t.Errorf("stage = %q", again.Stage)
		}
		gate.release()
		got := c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, nil)
		want := startRaw(t, deps).tool("pr_describe", args(reviewPRURL(g), 30), nil)
		if r := decodeRaw(t, want); r.IsError {
			t.Fatalf("synchronous call failed: %s", want)
		}
		assertSameBytes(t, "job_result", got, want)
		var res describe.Result
		if err := json.Unmarshal(decodeRaw(t, got).Structured, &res); err != nil || res.Title == nil {
			t.Errorf("job_result structured content is not a description: %v", err)
		}
	})
}

// newDescribePartLLMHost answers a pr_describe part with the walkthrough of
// the files of its diff, and the reduce call (its prompt has the files
// walkthrough) with the title, type and summary.
func newDescribePartLLMHost(t *testing.T) *fakeLLMHost {
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
		answer := "```yaml\ntype:\n- Enhancement\ndescription: |\n  - Add three files\ntitle: |\n  Add three files\n```\n"
		if !strings.Contains(user, "\nFiles walkthrough:\n") {
			var b strings.Builder
			b.WriteString("```yaml\npr_files:\n")
			for _, m := range fileRE.FindAllStringSubmatch(user, -1) {
				b.WriteString("- filename: " + m[1] + "\n  changes_title: Add " + m[1] + "\n  changes_summary: \"- New file\"\n  label: enhancement\n")
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

// TestDescribeInPartsAsBackgroundJob: through the stdio server, a
// description in three parts runs as a background job; job_result reports a
// stage per part and one for the reduce call, and returns a description
// whose coverage counts every part and whose title comes from the reduce
// call. Nothing is written to the PR.
func TestDescribeInPartsAsBackgroundJob(t *testing.T) {
	g, l := newFakeGiteaHost(t), newDescribePartLLMHost(t)
	g.large = largeFiles()
	env := reviewEnv(g, l)
	env["REVIEW_MCP_DIFF_MAX_TOKENS"] = "1000"
	c := startRaw(t, withJobs(t, realDeps(env, nil), nil))

	st := mustRunning(t, c.tool("pr_describe", map[string]any{"pr_url": reviewPRURL(g), "wait_seconds": 0}, nil), "description")
	r := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 60}, "tok-poll"))
	if r.IsError {
		t.Fatalf("job_result: %s", r.text())
	}
	want := "fetching|preparing diff|calling model (part 1 of 3)|calling model (part 2 of 3)|calling model (part 3 of 3)|calling model (summary)|rendering"
	if got := strings.Join(c.progressOf("tok-poll"), "|"); got != want {
		t.Errorf("stages = %q, want %q", got, want)
	}
	var res describe.Result
	if err := json.Unmarshal(r.Structured, &res); err != nil {
		t.Fatal(err)
	}
	if cov := res.Coverage; cov.ModelCalls != 3 || cov.Partial || cov.ReviewedFiles != 3 || len(res.Files) != 3 {
		t.Errorf("coverage = %+v, files %d", cov, len(res.Files))
	}
	if res.Title == nil || *res.Title != "Add three files" || res.Metadata.LLMCalls != 4 || len(l.recorded()) != 4 {
		t.Errorf("title %v, llm_calls %d, requests %d", res.Title, res.Metadata.LLMCalls, len(l.recorded()))
	}
	if !strings.Contains(r.text(), "- Described in 3 model calls.") {
		t.Errorf("the client text lacks the model-calls line:\n%s", r.text())
	}
	if w := g.writeLog(); len(w) != 0 {
		t.Errorf("pr_describe wrote to the provider: %v", w)
	}
}

// TestDescribeStagesShareTheReviewFormat: the part stages of pr_describe are
// pr_review's text, so the progress reporter sizes its total from them.
func TestDescribeStagesShareTheReviewFormat(t *testing.T) {
	if describe.CallingModelPart(2, 3) != review.CallingModelPart(2, 3) {
		t.Errorf("part stage %q differs from pr_review's %q", describe.CallingModelPart(2, 3), review.CallingModelPart(2, 3))
	}
	if n, ok := review.StageParts(describe.CallingModelPart(2, 5)); !ok || n != 5 {
		t.Errorf("StageParts = %d, %v", n, ok)
	}
	if _, ok := review.StageParts(describe.StageSummarizing); ok {
		t.Errorf("the summary stage parses as a part stage")
	}
}

// describeComments returns the bodies of the PR-level comments that carry
// the description comment marker, by id order.
func (f *fakeServer) describeComments() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := int64(1); id <= f.nextComment; id++ {
		if c, ok := f.comments[id]; ok && strings.HasSuffix(strings.TrimSpace(c.body), describe.CommentMarker) {
			out = append(out, c.body)
		}
	}
	return out
}

// TestPRDescribePublishComment: publish=true posts one comment with the
// marker, rendered for Gitea, and the second call edits it in place with the
// same bytes; the PR itself is not written.
func TestPRDescribePublishComment(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, describeAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	args := map[string]any{"pr_url": reviewPRURL(g), "publish": true}

	res := callTool(t, cs, "pr_describe", args)
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	var got describe.Result
	decodeStructured(t, res, &got)
	if got.Publish == nil || !got.Publish.Published || got.Publish.Updated || got.Publish.Mode != "comment" || got.Publish.CommentID == "" {
		t.Fatalf("publish = %+v", got.Publish)
	}
	if want := describerender.Client(&got); textOf(t, res) != want || !strings.Contains(want, "- Pull request comment: posted") {
		t.Errorf("text is not the client rendering with the publish section:\n%s", textOf(t, res))
	}
	first := g.describeComments()
	if len(first) != 1 || !strings.Contains(first[0], "## PR Description 📝") || !strings.Contains(first[0], "Change the constant") {
		t.Fatalf("comments = %q", first)
	}

	res = callTool(t, cs, "pr_describe", args)
	decodeStructured(t, res, &got)
	if got.Publish == nil || !got.Publish.Published || !got.Publish.Updated {
		t.Fatalf("second publish = %+v", got.Publish)
	}
	if second := g.describeComments(); len(second) != 1 || second[0] != first[0] {
		t.Errorf("the second run did not edit the comment in place with the same bytes: %q", second)
	}
	for _, w := range g.writeLog() {
		if strings.Contains(w, "/pulls/7") && !strings.Contains(w, "/reviews") {
			t.Errorf("comment mode wrote to the PR: %s", w)
		}
	}
}

// TestPRDescribePublishDescription: publish_mode=description appends the
// region below the author's text, replaces the title with update_title, and
// the second call writes nothing new.
func TestPRDescribePublishDescription(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, describeAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	args := map[string]any{"pr_url": reviewPRURL(g), "publish": true, "publish_mode": "description", "update_title": true}

	res := callTool(t, cs, "pr_describe", args)
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	var got describe.Result
	decodeStructured(t, res, &got)
	if got.Publish == nil || !got.Publish.Published || got.Publish.Updated || !got.Publish.TitleUpdated || got.Publish.Mode != "description" {
		t.Fatalf("publish = %+v", got.Publish)
	}
	g.mu.Lock()
	body, title, patches := g.prBody, g.prTitle, len(g.prPatchBody)
	g.mu.Unlock()
	if !strings.HasPrefix(body, "Please look. "+descMarker+"\n\n"+describe.RegionStart+"\n") || !strings.HasSuffix(body, "\n"+describe.RegionEnd) {
		t.Errorf("body = %q", body)
	}
	if title != "Change the constant "+describeTitleMarker || patches != 1 {
		t.Errorf("title %q, %d PATCH requests", title, patches)
	}

	res = callTool(t, cs, "pr_describe", args)
	decodeStructured(t, res, &got)
	g.mu.Lock()
	again, patches2 := g.prBody, len(g.prPatchBody)
	g.mu.Unlock()
	if !got.Publish.Published || !got.Publish.Updated || again != body || patches2 != 1 {
		t.Errorf("second run: %+v, same body %v, %d PATCH requests", got.Publish, again == body, patches2)
	}
	if len(g.describeComments()) != 0 {
		t.Errorf("description mode posted a comment")
	}
}
