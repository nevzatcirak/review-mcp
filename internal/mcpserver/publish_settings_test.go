package mcpserver

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/review"
)

func countWrites(g *fakeServer, suffix string) int {
	n := 0
	for _, w := range g.writeLog() {
		if strings.HasPrefix(w, "POST ") && strings.HasSuffix(w, suffix) {
			n++
		}
	}
	return n
}

// TestPRReviewPublishSettings: review.inline_findings, review.persistent_
// overview and review.max_discussion_tokens reach the pipeline as the
// fallback of the per-call options, and the inline_findings argument wins
// over the configuration in both directions.
func TestPRReviewPublishSettings(t *testing.T) {
	t.Run("inline_findings", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			env     string // "" leaves the default (true)
			arg     any    // nil leaves the argument out
			reviews int
		}{
			{"default", "", nil, 1},
			{"config off", "false", nil, 0},
			{"argument on beats config off", "false", true, 1},
			{"argument off beats the default", "", false, 0},
			{"argument on", "true", true, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
				env := reviewEnv(g, l)
				if tc.env != "" {
					env["REVIEW_MCP_REVIEW_INLINE_FINDINGS"] = tc.env
				}
				args := map[string]any{"pr_url": reviewPRURL(g), "publish": true}
				if tc.arg != nil {
					args["inline_findings"] = tc.arg
				}
				res := callTool(t, connect(t, realDeps(env, nil)), "pr_review", args)
				if res.IsError {
					t.Fatalf("tool error: %s", textOf(t, res))
				}
				if got := countWrites(g, "/pulls/7/reviews"); got != tc.reviews {
					t.Errorf("reviews posted = %d, want %d", got, tc.reviews)
				}
				if got := len(g.overviewBodies()); got != 1 {
					t.Errorf("overviews = %d, want 1", got)
				}
			})
		}
	})

	t.Run("persistent_overview", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			env       string
			overviews int
			edits     int
			// edits: the first run edits its new overview once to add the
			// inline links; later runs edit only in place.
		}{{"default edits in place", "", 1, 2}, {"off posts a new overview each run", "false", 2, 1}} {
			t.Run(tc.name, func(t *testing.T) {
				g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
				env := reviewEnv(g, l)
				if tc.env != "" {
					env["REVIEW_MCP_REVIEW_PERSISTENT_OVERVIEW"] = tc.env
				}
				cs := connect(t, realDeps(env, nil))
				for range 2 {
					if res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "publish": true}); res.IsError {
						t.Fatalf("tool error: %s", textOf(t, res))
					}
				}
				if got := len(g.overviewBodies()); got != tc.overviews {
					t.Errorf("overviews = %d, want %d", got, tc.overviews)
				}
				if got := countWrites(g, "/issues/7/comments"); got != tc.overviews {
					t.Errorf("overview posts = %d, want %d", got, tc.overviews)
				}
				patches := 0
				for _, w := range g.writeLog() {
					if strings.HasPrefix(w, "PATCH ") {
						patches++
					}
				}
				if patches != tc.edits {
					t.Errorf("edits = %d, want %d", patches, tc.edits)
				}
			})
		}
	})

	t.Run("max_discussion_tokens", func(t *testing.T) {
		for _, tc := range []struct {
			name, env string
			inPrompt  bool
		}{{"default", "", true}, {"zero turns the discussion off", "0", false}, {"explicit budget", "300", true}} {
			t.Run(tc.name, func(t *testing.T) {
				g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
				g.plantComment("alice", 7, "Please double check the constant. "+threadMarker)
				env := reviewEnv(g, l)
				if tc.env != "" {
					env["REVIEW_MCP_REVIEW_MAX_DISCUSSION_TOKENS"] = tc.env
				}
				if res := callTool(t, connect(t, realDeps(env, nil)), "pr_review", map[string]any{"pr_url": reviewPRURL(g)}); res.IsError {
					t.Fatalf("tool error: %s", textOf(t, res))
				}
				bodies := l.recorded()
				if len(bodies) == 0 {
					t.Fatal("the LLM received no request")
				}
				if got := strings.Contains(bodies[0], threadMarker); got != tc.inPrompt {
					t.Errorf("thread text in the prompt = %v, want %v", got, tc.inPrompt)
				}
			})
		}
	})
}

func TestArgsWithConfigDefaults(t *testing.T) {
	// Kept here with the end-to-end cases it backs: a call's own value wins.
	on, off := true, false
	env := validEnv()
	env["REVIEW_MCP_REVIEW_INLINE_FINDINGS"] = "false"
	cfg := depsFor(env, nil).Config
	got := review.Args{}.WithConfigDefaults(cfg)
	if *got.InlineFindings || !*got.PersistentOverview || *got.MaxDiscussionTokens != 1500 {
		t.Errorf("config fallback = %v %v %v", *got.InlineFindings, *got.PersistentOverview, *got.MaxDiscussionTokens)
	}
	got = review.Args{InlineFindings: &on, PersistentOverview: &off}.WithConfigDefaults(cfg)
	if !*got.InlineFindings || *got.PersistentOverview {
		t.Errorf("call values lost: %v %v", *got.InlineFindings, *got.PersistentOverview)
	}
	if (review.Args{}).WithConfigDefaults(nil).InlineFindings != nil {
		t.Error("a nil config must leave the options unset")
	}
}
