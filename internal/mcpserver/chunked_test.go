package mcpserver

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/review"
)

// largeFiles are three added files of 30 lines each: with diff.max_tokens
// at its minimum (1000), each takes a part of its own.
func largeFiles() map[string]string {
	out := map[string]string{}
	for i := range 3 {
		var b strings.Builder
		for n := 1; n <= 30; n++ {
			fmt.Fprintf(&b, "some added line of code number %d in file %d\n", n, i)
		}
		out[fmt.Sprintf("src/part%d.go", i)] = b.String()
	}
	return out
}

// largeDiff is the unified diff of added files (path -> content).
func largeDiff(files map[string]string) string {
	var b strings.Builder
	for _, path := range slices.Sorted(maps.Keys(files)) {
		content := files[path]
		n := strings.Count(content, "\n")
		fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n",
			path, path, path, n)
		for _, l := range strings.SplitAfter(strings.TrimSuffix(content, "\n"), "\n") {
			b.WriteString("+" + strings.TrimSuffix(l, "\n") + "\n")
		}
	}
	return b.String()
}

var partLineRE = regexp.MustCompile(`This is part (\d+) of (\d+)\.`)

// newPartLLMHost answers each part of a review in parts with a finding on
// line 3 of the part's file, which it reads from the prompt.
func newPartLLMHost(t *testing.T) *fakeLLMHost {
	t.Helper()
	f := &fakeLLMHost{status: http.StatusOK}
	fileRE := regexp.MustCompile(`## File: '(src/part\d\.go)'`)
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
		part := "1"
		if m := partLineRE.FindStringSubmatch(user); m != nil {
			part = m[1]
		}
		file := "unknown.go"
		if m := fileRE.FindStringSubmatch(user); m != nil {
			file = m[1]
		}
		answer := "```yaml\nreview:\n  estimated_effort_to_review: " + part + "\n  relevant_tests: \"No\"\n" +
			"  key_issues_to_review:\n    - relevant_file: " + file + "\n      issue_header: Issue in part " + part + "\n" +
			"      issue_content: Line 3 of " + file + " is wrong.\n      start_line: 3\n      end_line: 3\n" +
			"  security_concerns: \"No\"\n  performance_concerns: \"No\"\n```\n"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// TestChunkedReviewAsBackgroundJob: through the stdio server, a review in
// three parts runs as a background job; job_result reports one progress
// stage per part and returns the merged review, whose coverage counts every
// part; the publish posts one overview and one Gitea review with the three
// inline findings.
func TestChunkedReviewAsBackgroundJob(t *testing.T) {
	g, l := newFakeGiteaHost(t), newPartLLMHost(t)
	g.large = largeFiles()
	env := reviewEnv(g, l)
	env["REVIEW_MCP_DIFF_MAX_TOKENS"] = "1000"
	c := startRaw(t, withJobs(t, realDeps(env, nil), nil))

	st := mustRunning(t, c.tool("pr_review", map[string]any{"pr_url": reviewPRURL(g), "publish": true, "wait_seconds": 0}, nil), "review")
	r := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 60}, "tok-poll"))
	if r.IsError {
		t.Fatalf("job_result: %s", r.text())
	}
	want := "fetching|preparing diff|calling model (part 1 of 3)|calling model (part 2 of 3)|calling model (part 3 of 3)|rendering"
	if got := strings.Join(c.progressOf("tok-poll"), "|"); got != want {
		t.Errorf("stages = %q, want %q", got, want)
	}

	var res review.Result
	if err := json.Unmarshal(r.Structured, &res); err != nil {
		t.Fatal(err)
	}
	cov := res.Coverage
	if cov.ModelCalls != 3 || cov.FailedParts != 0 || cov.Partial || cov.ReviewedFiles != 3 || cov.TotalFiles != 3 {
		t.Errorf("coverage = %+v", cov)
	}
	if len(l.recorded()) != 3 || res.Metadata.LLMCalls != 3 {
		t.Errorf("LLM requests %d, llm_calls %d; want 3", len(l.recorded()), res.Metadata.LLMCalls)
	}
	var files []string
	for _, ki := range res.Review.KeyIssuesToReview {
		files = append(files, ki.RelevantFile)
		if ki.InlineStatus != review.InlinePosted {
			t.Errorf("finding on %s: inline status %q", ki.RelevantFile, ki.InlineStatus)
		}
	}
	if !slices.Equal(files, []string{"src/part0.go", "src/part1.go", "src/part2.go"}) {
		t.Errorf("findings = %v", files)
	}
	if e := res.Review.EstimatedEffortToReview; e == nil || *e != 3 {
		t.Errorf("effort = %v, want the largest part's (3)", e)
	}
	if !strings.Contains(r.text(), "- Reviewed in 3 model calls.") {
		t.Errorf("the client text lacks the model-calls line:\n%s", r.text())
	}

	ov := g.overviewBodies()
	if len(ov) != 1 || !strings.Contains(ov[0], "Reviewed in 3 model calls.") {
		t.Errorf("overviews = %d, or the overview lacks the model-calls line", len(ov))
	}
	g.mu.Lock()
	reviews := slices.Clone(g.reviews)
	g.mu.Unlock()
	if len(reviews) != 1 {
		t.Fatalf("Gitea reviews = %d, want 1", len(reviews))
	}
	if cs, _ := reviews[0]["comments"].([]any); len(cs) != 3 {
		t.Errorf("inline comments in the review = %d, want 3", len(cs))
	}
	if res.Publish == nil || !res.Publish.Published || res.Publish.Inline == nil || res.Publish.Inline.Posted != 3 {
		t.Errorf("publish = %+v", res.Publish)
	}
}
