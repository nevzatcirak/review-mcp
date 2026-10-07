package serve

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestPRInfoUsesThePerCallToken: pr_info takes the provider token of its own
// request, needs no LLM key, sends read requests only, and answers with the
// target branch; an optional part that the fake does not serve (the branch
// protection) is null with the fixed note, not a failure.
func TestPRInfoUsesThePerCallToken(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})

	for i, tok := range []string{"gitea-token-A", "gitea-token-B"} {
		pr := 5 + i
		_, r := ts.callTool(t, creds(tok, ""), "pr_info", map[string]any{"pr_url": ts.prURL(pr)})
		if r.IsError {
			t.Fatalf("call %d: %q", i, r.text())
		}
		if !strings.Contains(r.text(), "`feature` → `main`") || !strings.Contains(r.text(), "Reviewers: none") {
			t.Errorf("call %d text:\n%s", i, r.text())
		}
		var got struct {
			TargetBranch      string          `json:"target_branch"`
			RequiredApprovals json.RawMessage `json:"required_approvals"`
			Note              string          `json:"required_approvals_note"`
			Reviewers         []any           `json:"reviewers"`
			Activity          struct {
				Overview       bool `json:"overview"`
				InlineFindings int  `json:"inline_findings"`
			} `json:"review_mcp_activity"`
		}
		if err := json.Unmarshal(r.Structured, &got); err != nil {
			t.Fatal(err)
		}
		if got.TargetBranch != "main" || string(got.RequiredApprovals) != "null" || got.Note != "not readable with this token" ||
			got.Reviewers == nil || got.Activity.Overview {
			t.Errorf("call %d structured: %s", i, r.Structured)
		}
	}
	for _, o := range ts.gitea.observations() {
		if want := map[int]string{5: "gitea-token-A", 6: "gitea-token-B"}[o.pr]; o.cred != want {
			t.Errorf("PR %d was requested with %q, want %q", o.pr, o.cred, want)
		}
	}
	if w, _ := ts.gitea.written(); len(w) != 0 {
		t.Errorf("pr_info sent write requests: %v", w)
	}
	if h := ts.llm.hits.Load(); h != 0 {
		t.Errorf("the LLM saw %d requests for pr_info", h)
	}

	// The PR itself failing is the tool's failure, with the fixed sentence.
	ts.gitea.failStatus.Store(404)
	ts.gitea.leakBody = "LEAK-TEXT-pr-info"
	_, r := ts.callTool(t, creds("gitea-token-A", ""), "pr_info", map[string]any{"pr_url": ts.prURL(5)})
	if !r.IsError || r.text() != "the requested resource was not found (HTTP 404)" {
		t.Errorf("failing PR: isError=%v %q", r.IsError, r.text())
	}
}
