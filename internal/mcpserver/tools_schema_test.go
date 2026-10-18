package mcpserver

import (
	"encoding/json"
	"slices"
	"testing"
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
		check("pr_ask", askOutputSchema(deps), false)
		check("pr_describe", describeOutputSchema(deps), false)
		check("pr_improve", improveOutputSchema(deps), false)
	}
}
