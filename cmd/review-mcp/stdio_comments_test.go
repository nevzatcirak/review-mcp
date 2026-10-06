package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/tools"
)

func toolCall(id int, name string, args map[string]any) string {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	return string(raw)
}

type toolResponse struct {
	Result struct {
		Content []struct{ Text string } `json:"content"`
		Struct  json.RawMessage         `json:"structuredContent"`
		IsError bool                    `json:"isError"`
	} `json:"result"`
}

func responseByID(t *testing.T, stdout string, id string) toolResponse {
	t.Helper()
	for _, m := range decodeLines(t, stdout) {
		if string(m["id"]) == id {
			raw, _ := json.Marshal(m)
			var r toolResponse
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatal(err)
			}
			return r
		}
	}
	t.Fatalf("no response with id %s:\n%s", id, stdout)
	return toolResponse{}
}

// TestStdioCommentToolsUseProductionWiring drives the stdio server with the
// real wiring.NewResolver against the fake hosts, through the MCP protocol.
func TestStdioCommentToolsUseProductionWiring(t *testing.T) {
	g, b := newFakeGitea(t), newFakeBBS(t)
	_, stdout, stderr := session(t, nil, diagEnv(g, b),
		handshake[0], handshake[1],
		toolCall(10, "pr_comments", map[string]any{"pr_url": g.giteaPR()}),
		toolCall(11, "pr_comment_reply", map[string]any{"pr_url": b.bbsPR(), "comment_id": "10", "body": "thanks"}),
		toolCall(12, "pr_comments", map[string]any{"pr_url": "https://other.example.net/octo/demo/pulls/7"}),
	)
	checkCommentStreams(t, "", stderr)
	for _, s := range []string{diagGiteaToken, diagBBSToken} {
		if strings.Contains(stdout, s) {
			t.Errorf("token %q on stdout", s)
		}
	}

	r := responseByID(t, stdout, "10")
	var res tools.PRCommentsResult
	if r.Result.IsError || json.Unmarshal(r.Result.Struct, &res) != nil || threadIDs(res) != "101,201" {
		t.Fatalf("pr_comments: %+v (%s)", r.Result, r.Result.Struct)
	}
	if text := r.Result.Content[0].Text; !strings.Contains(text, commentBodyMarker) || !strings.Contains(text, tools.UntrustedNotice) {
		t.Errorf("markdown text:\n%s", text)
	}

	r = responseByID(t, stdout, "11")
	var rep tools.PRCommentReplyResult
	if r.Result.IsError || json.Unmarshal(r.Result.Struct, &rep) != nil || !rep.InThread || rep.ID != "77" {
		t.Errorf("pr_comment_reply: %+v (%s)", r.Result, r.Result.Struct)
	}

	r = responseByID(t, stdout, "12")
	if !r.Result.IsError || !strings.HasPrefix(r.Result.Content[0].Text, "the pull request URL does not match any configured provider") {
		t.Errorf("unconfigured host: %+v", r.Result)
	}
}

func TestStdioCommentToolsDegraded(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12"
	_, stdout, _ := session(t, nil, env,
		handshake[0], handshake[1],
		toolCall(10, "pr_comments", map[string]any{"pr_url": g.giteaPR()}),
	)
	r := responseByID(t, stdout, "10")
	const want = "review-mcp configuration is invalid; call server_info for the list of problems"
	if !r.Result.IsError || r.Result.Content[0].Text != want {
		t.Errorf("result = %+v", r.Result)
	}
	if n := g.requests(); n != 0 {
		t.Errorf("degraded start made %d requests", n)
	}
}
