package mcpserver

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/repotools"
)

// TestOutputSchemasDeclareRepoTools: every tool's coverage schema declares
// coverage.repo_context.tools (X-43) as an object without additional
// properties, and its calls likewise; pr_review requires it.
func TestOutputSchemasDeclareRepoTools(t *testing.T) {
	check := func(name string, schema any, required bool) {
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
		if len(covs) == 0 {
			t.Fatalf("%s: no coverage schema", name)
		}
		for _, c := range covs {
			props, _ := c["properties"].(map[string]any)
			rc, _ := props["repo_context"].(map[string]any)
			rcProps, _ := rc["properties"].(map[string]any)
			tl, _ := rcProps["tools"].(map[string]any)
			if tl == nil {
				t.Fatalf("%s: coverage.repo_context.tools is not declared", name)
			}
			if tl["additionalProperties"] != false {
				t.Errorf("%s: coverage.repo_context.tools allows additional properties: %v", name, tl["additionalProperties"])
			}
			tp, _ := tl["properties"].(map[string]any)
			for _, f := range []string{"status", "reason", "rounds", "calls", "tokens"} {
				if tp[f] == nil {
					t.Errorf("%s: coverage.repo_context.tools.%s is not declared", name, f)
				}
			}
			calls, _ := tp["calls"].(map[string]any)
			items, _ := calls["items"].(map[string]any)
			if items["additionalProperties"] != false {
				t.Errorf("%s: a tool call allows additional properties: %v", name, items)
			}
			rcReq, _ := rc["required"].([]any)
			if required && !slices.Contains(rcReq, any("tools")) {
				t.Errorf("%s: coverage.repo_context.tools is not required", name)
			}
		}
	}
	for _, deps := range []Deps{{Serve: true}, withJobs(t, Deps{}, nil)} {
		check("pr_review", reviewOutputSchema(deps), true)
		check("pr_ask", askOutputSchema(deps), true)
		check("pr_describe", describeOutputSchema(deps), false)
		check("pr_improve", improveOutputSchema(deps), false)
	}
}

// TestPRAskToolsResultsValidate [WP-10d]: a real pr_ask result (repository
// tools off, validated by the server itself) carries coverage.repo_context
// .tools, and every other case of the tools (X-43) validates against the
// declared schema of pr_ask and of pr_review once put into that result:
// used with every call status, not_used, off for both reasons, skipped for
// every reason.
func TestPRAskToolsResultsValidate(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, askAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	schemas := outputSchemas(t, cs)
	out := callValid(t, cs, schemas, "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion})
	cov, _ := out["coverage"].(map[string]any)
	rc, _ := cov["repo_context"].(map[string]any)
	tl, _ := rc["tools"].(map[string]any)
	if tl["status"] != "off" || tl["reason"] != "off" || tl["rounds"] != 0.0 || tl["tokens"] != 0.0 {
		t.Fatalf("tools of a run with the rounds off: %v", tl)
	}
	if calls, ok := tl["calls"].([]any); !ok || len(calls) != 0 {
		t.Fatalf("calls %v, want []", tl["calls"])
	}
	md, _ := out["metadata"].(map[string]any)
	if md["tool_rounds"] != 0.0 {
		t.Fatalf("tool_rounds %v", md["tool_rounds"])
	}

	allCalls := []repotools.Call{
		{Tool: repotools.ReadFile, Target: "a.go", Status: repotools.StatusOK, Tokens: 80},
		{Tool: repotools.ReadFile, Target: "../../etc/passwd", Status: repotools.StatusNotAllowed},
		{Tool: repotools.ReadFile, Target: "gone.go", Status: repotools.StatusNotAvailable, Tokens: 5},
		{Tool: repotools.ReadFile, Target: "huge.go", Status: repotools.StatusTooLarge, Tokens: 5},
		{Tool: repotools.ListDir, Target: "", Status: repotools.StatusInvalidArguments, Tokens: 5},
		{Tool: repotools.Search, Target: "needle", Status: repotools.StatusTooManyCalls},
		{Tool: "run_shell", Target: "", Status: repotools.StatusBudgetExhausted},
	}
	uses := []repotools.Use{
		{Status: repotools.StatusUsed, Rounds: 2, Tokens: 95, Calls: allCalls, Exhausted: true},
		{Status: repotools.StatusNotUsed},
		{Status: repotools.StatusOff, Reason: repotools.ReasonOff},
		{Status: repotools.StatusOff, Reason: repotools.ReasonRepoContextOff},
	}
	for _, r := range []string{repotools.ReasonRepoContextUnavailable, repotools.ReasonReviewInParts, repotools.ReasonEndpointNoTools,
		repotools.ReasonNoRoom, repotools.ReasonNoModelCall, repotools.ReasonConversationFailed} {
		uses = append(uses, repotools.Use{Status: repotools.StatusSkipped, Reason: r})
	}
	var base ask.Result
	raw, _ := json.Marshal(out)
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	for _, u := range uses {
		res := base
		res.Coverage.RepoContext.Tools = u.Report()
		res.Metadata.ToolRounds = u.Rounds
		if err := validateOutput(t, schemas["pr_ask"], res); err != nil {
			t.Errorf("pr_ask %s/%s: %v", u.Status, u.Reason, err)
		}
	}
	// A call status or a reason outside the declared enums is refused.
	bad := base
	bad.Coverage.RepoContext.Tools = &llmrun.RepoTools{Status: "used", Reason: "made_up", Calls: []llmrun.RepoToolCall{}}
	if err := validateOutput(t, schemas["pr_ask"], bad); err == nil {
		t.Error("a reason outside the enum passed the pr_ask schema")
	}
	bad.Coverage.RepoContext.Tools = &llmrun.RepoTools{Status: "used", Calls: []llmrun.RepoToolCall{{Tool: "read_file", Status: "weird"}}}
	if err := validateOutput(t, schemas["pr_ask"], bad); err == nil {
		t.Error("a call status outside the enum passed the pr_ask schema")
	}
}
