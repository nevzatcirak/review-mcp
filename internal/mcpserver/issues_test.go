package mcpserver

import (
	"log/slog"
	"maps"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/review"
)

// TestServerInfoReportsIssueReads: server_info says, per provider, whether
// review-mcp reads the issues of its tracker (X-42): Gitea, GitHub and
// GitLab do, Bitbucket Server has no tracker. A degraded start reports none,
// since no provider can be built.
func TestServerInfoReportsIssueReads(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	res := callServerInfo(t, connect(t, realDeps(validEnv(), logger)))
	got := map[string]bool{}
	for _, p := range structuredOf(t, res).Providers {
		got[p.Kind] = p.Issues
	}
	want := map[string]bool{"gitea": true, "bitbucket_server": false, "github": true, "gitlab": true}
	if !maps.Equal(got, want) {
		t.Errorf("issues per provider = %v, want %v", got, want)
	}
	text := textOf(t, res)
	if n := strings.Count(text, ", reads issues\n"); n != 3 {
		t.Errorf("the markdown names %d providers that read issues, want 3:\n%s", n, text)
	}
	if strings.Contains(text, "bitbucket.example.com/bb`, reads issues") {
		t.Error("the markdown says Bitbucket Server reads issues")
	}

	d := realDeps(invalidEnv(), logger)
	for _, p := range structuredOf(t, callServerInfo(t, connect(t, d))).Providers {
		if p.Issues {
			t.Errorf("degraded start: %s reports issue reads", p.Kind)
		}
	}
}

// TestPRReviewIssueContext: issue_context reaches the run through MCP with
// acceptance criteria as a string or a list, and an invalid item is refused
// with the fixed sentence before anything is fetched.
func TestPRReviewIssueContext(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	for name, crit := range map[string]any{"string": "retries once", "list": []any{"retries once", "shows an error"}} {
		res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "issue_context": []any{
			map[string]any{"key": "PROJ-123", "title": "Retry sign-in", "acceptance_criteria": crit,
				"url": "https://tracker.example/browse/PROJ-123"},
		}})
		if res.IsError {
			t.Fatalf("%s: tool error: %s", name, textOf(t, res))
		}
		var got review.Result
		decodeStructured(t, res, &got)
		if got.Issue == nil || got.Issue.Source != "argument" || len(got.Issue.Refs) != 1 || got.Issue.Refs[0].Ref != "PROJ-123" {
			t.Errorf("%s: issue = %+v", name, got.Issue)
		}
	}
	before := l.hits.Load()
	res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "issue_context": []any{
		map[string]any{"key": "PROJ-123", "title": strings.Repeat("t", 301)},
	}})
	if !res.IsError || textOf(t, res) != "issue_context[1].title must be at most 300 characters" || l.hits.Load() != before {
		t.Errorf("error %v %q; model calls %d -> %d", res.IsError, textOf(t, res), before, l.hits.Load())
	}
}
