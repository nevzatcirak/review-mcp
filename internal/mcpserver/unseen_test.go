package mcpserver

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// unseenReviewAnswer is goodAnswer's finding with a path the review never
// saw in its content.
const unseenReviewAnswer = "```yaml\nreview:\n  estimated_effort_to_review: 2\n  relevant_tests: \"No\"\n" +
	"  key_issues_to_review:\n    - relevant_file: src/app.go\n      issue_header: Constant changed\n" +
	"      issue_content: The constant is also read in `src/never.go:4`.\n      start_line: 2\n      end_line: 2\n" +
	"  security_concerns: \"No\"\n  performance_concerns: \"No\"\n```\n"

// TestUnverifiedPathsValidate [WP-10d]: the paths the run never saw (v2.5
// spec V-13b §6 item 1) are declared in the output schemas of pr_review
// (unverified_paths on each finding) and pr_ask (answer_unverified_paths),
// which keep additionalProperties false; real results with them, and
// without them (the empty case: omitted), validate as the MCP SDK checks
// them, and a value of the wrong type is refused.
func TestUnverifiedPathsValidate(t *testing.T) {
	for _, c := range []struct {
		tool, answer, key string
		args              map[string]any
		get               func(out map[string]any) map[string]any
	}{
		{"pr_review", unseenReviewAnswer, "unverified_paths", nil, func(out map[string]any) map[string]any {
			rev, _ := out["review"].(map[string]any)
			f, _ := rev["key_issues_to_review"].([]any)
			if len(f) == 0 {
				t.Fatal("no finding")
			}
			ki, _ := f[0].(map[string]any)
			return ki
		}},
		{"pr_ask", "Callers in `src/never.go` break.", "answer_unverified_paths", map[string]any{"question": askQuestion},
			func(out map[string]any) map[string]any { return out }},
	} {
		t.Run(c.tool, func(t *testing.T) {
			g := newFakeGiteaHost(t)
			cs := connect(t, realDeps(reviewEnv(g, newFakeLLMHost(t, 200, c.answer)), nil))
			schemas := outputSchemas(t, cs)
			args := map[string]any{"pr_url": reviewPRURL(g)}
			for k, v := range c.args {
				args[k] = v
			}
			out := callValid(t, cs, schemas, c.tool, args)
			obj := c.get(out)
			if paths, _ := obj[c.key].([]any); !slices.Equal(paths, []any{"src/never.go"}) {
				t.Fatalf("%s = %v", c.key, obj[c.key])
			}
			raw, _ := json.Marshal(schemas[c.tool])
			if !strings.Contains(string(raw), `"`+c.key+`"`) {
				t.Errorf("the output schema does not declare %s", c.key)
			}
			// The empty case: the field is omitted.
			delete(obj, c.key)
			if err := validateOutput(t, schemas[c.tool], out); err != nil {
				t.Errorf("without %s: %v", c.key, err)
			}
			obj[c.key] = []any{}
			if err := validateOutput(t, schemas[c.tool], out); err != nil {
				t.Errorf("with %s []: %v", c.key, err)
			}
			obj[c.key] = []any{1}
			if err := validateOutput(t, schemas[c.tool], out); err == nil {
				t.Errorf("%s with a number passed the schema", c.key)
			}
			delete(obj, c.key)
			obj[c.key+"_x"] = []any{"a/b.go"}
			if err := validateOutput(t, schemas[c.tool], out); err == nil {
				t.Errorf("an undeclared property next to %s passed the schema", c.key)
			}
		})
	}
}
