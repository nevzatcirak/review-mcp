package serve

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"testing"
)

// TestServeToolListHasNoJobs: serve mode has no background jobs (X-16,
// X-10): no job_result tool, and the pr_review and pr_ask definitions do
// not mention jobs. [canary target: registering job_result in serve must
// fail this test.]
func TestServeToolListHasNoJobs(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})

	resp := ts.post(t, PathMCP, nil, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Tools []struct {
			Name         string          `json:"name"`
			Description  string          `json:"description"`
			OutputSchema json.RawMessage `json:"outputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(rpcMessage(t, resp.body)["result"], &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
		if strings.Contains(tl.Description, "job") || strings.Contains(string(tl.OutputSchema), "job_id") {
			t.Errorf("serve tool %s mentions background jobs", tl.Name)
		}
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "pr_ask,pr_comment_create,pr_comment_reply,pr_comments,pr_describe,pr_improve,pr_info,pr_review,server_info" {
		t.Errorf("serve tools = %s", got)
	}
}

// TestServeCallsStaySynchronous: serve runs pr_review and pr_ask in the
// request and ignores wait_seconds, even 0 or out of range; job_result is
// not a tool there.
func TestServeCallsStaySynchronous(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})
	for _, wait := range []int{0, 601} {
		_, r := ts.callTool(t, creds("gitea-token", "llm-key"), "pr_review", map[string]any{"pr_url": ts.prURL(1), "wait_seconds": wait})
		if r.IsError || strings.Contains(string(r.Structured), "job_id") || !strings.Contains(string(r.Structured), `"review"`) {
			t.Errorf("serve pr_review with wait_seconds %d: isError=%v %s", wait, r.IsError, r.text())
		}
		_, r = ts.callTool(t, creds("gitea-token", "llm-key"), "pr_ask", map[string]any{"pr_url": ts.prURL(1), "question": "Why?", "wait_seconds": wait})
		if r.IsError || strings.Contains(string(r.Structured), "job_id") || !strings.Contains(string(r.Structured), `"answer"`) {
			t.Errorf("serve pr_ask with wait_seconds %d: isError=%v %s", wait, r.IsError, r.text())
		}
	}
	resp := ts.post(t, PathMCP, creds("gitea-token", "llm-key"), callBody(9, "job_result", map[string]any{"job_id": "job_aaaaaaaaaaaaaaaaaaaaaaaaaa"}))
	m := rpcMessage(t, resp.body)
	var r toolResult
	_ = json.Unmarshal(m["result"], &r)
	if m["error"] == nil && !r.IsError {
		t.Errorf("serve answered job_result: %s", resp.body)
	}
}
