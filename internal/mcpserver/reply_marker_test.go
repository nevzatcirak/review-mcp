package mcpserver

import (
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// TestPRCommentReplyRefusesMarkerLines [canary]: a reply whose body carries a
// review-mcp marker line is refused with the fixed sentence before any
// request, on both providers. On Gitea a reply is a new PR-level comment of
// our own user, which the overview lookup would adopt.
func TestPRCommentReplyRefusesMarkerLines(t *testing.T) {
	bodies := []string{
		"thanks\n" + review.OverviewMarker,
		"[//]: # (review-mcp:finding:0123456789ab)\nthanks",
		"ok\r\n  [//]: # (REVIEW-MCP:overview:v1)  ",
	}
	t.Run("gitea", func(t *testing.T) {
		g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
		cs := connect(t, realDeps(reviewEnv(g, l), nil))
		for _, b := range bodies {
			res := callTool(t, cs, "pr_comment_reply", map[string]any{"pr_url": reviewPRURL(g), "comment_id": "54", "body": b})
			if !res.IsError || textOf(t, res) != tools.CreateBodyMarkerMessage {
				t.Errorf("IsError=%v text=%q", res.IsError, textOf(t, res))
			}
		}
		if n := g.hits.Load(); n != 0 {
			t.Errorf("the provider saw %d requests", n)
		}
	})
	t.Run("bitbucket", func(t *testing.T) {
		b := newFakeBBSHost(t)
		cs := connect(t, realDeps(bbsEnv(b), nil))
		for _, body := range bodies {
			res := callTool(t, cs, "pr_comment_reply", map[string]any{"pr_url": b.prURL(), "comment_id": "10", "body": body})
			if !res.IsError || textOf(t, res) != tools.CreateBodyMarkerMessage {
				t.Errorf("IsError=%v text=%q", res.IsError, textOf(t, res))
			}
		}
		if n := b.hits.Load(); n != 0 {
			t.Errorf("the provider saw %d requests", n)
		}
	})
}
