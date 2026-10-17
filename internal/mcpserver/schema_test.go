package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// validateOutput validates structured against a tool's output schema the way
// the MCP SDK validates tool output (go-sdk mcp.applySchema): the schema is
// resolved with ValidateDefaults, the result is the JSON form unmarshaled
// into any, defaults are applied to an object, then it is validated.
func validateOutput(t *testing.T, schema, structured any) error {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("output schema does not decode: %v", err)
	}
	resolved, err := s.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		t.Fatalf("output schema does not resolve: %v", err)
	}
	out, err := json.Marshal(structured)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(map[string]any); ok {
		if err := resolved.ApplyDefaults(&v); err != nil {
			return err
		}
	}
	return resolved.Validate(&v)
}

// outputSchemas returns each tool's declared output schema by name.
func outputSchemas(t *testing.T, cs *mcp.ClientSession) map[string]any {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for _, tl := range res.Tools {
		out[tl.Name] = tl.OutputSchema
	}
	return out
}

// callValid calls tool through MCP (whose server validates the output
// against the declared schema, and fails the call when it does not hold),
// then validates the structured result once more as the SDK does, and
// returns it decoded.
func callValid(t *testing.T, cs *mcp.ClientSession, schemas map[string]any, tool string, args map[string]any) map[string]any {
	t.Helper()
	res := callTool(t, cs, tool, args)
	if res.IsError {
		t.Fatalf("%s: tool error: %s", tool, textOf(t, res))
	}
	if err := validateOutput(t, schemas[tool], res.StructuredContent); err != nil {
		t.Fatalf("%s: the result does not validate against the output schema: %v", tool, err)
	}
	var out map[string]any
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestRepoConfigWithNothingIgnoredValidates: a repository config whose every
// key is applied (the 2.2.0-rc.2 acceptance Q6 file) gives repo_config with
// "ignored": [], and the results of pr_review, pr_describe and pr_improve
// validate against their output schemas. [canary]
func TestRepoConfigWithNothingIgnoredValidates(t *testing.T) {
	cases := []struct {
		tool string
		llm  func(t *testing.T) *fakeLLMHost
	}{
		{"pr_review", func(t *testing.T) *fakeLLMHost { return newFakeLLMHost(t, 200, goodAnswer) }},
		{"pr_describe", func(t *testing.T) *fakeLLMHost { return newFakeLLMHost(t, 200, describeAnswer) }},
		{"pr_improve", func(t *testing.T) *fakeLLMHost { return newImproveLLMHost(t, improveAnswer, improveReflection) }},
	}
	for _, c := range cases {
		t.Run(c.tool, func(t *testing.T) {
			g := newFakeGiteaHost(t)
			g.repoConfig = "[review]\nissue_compliance = false\n"
			cs := connect(t, realDeps(reviewEnv(g, c.llm(t)), nil))
			out := callValid(t, cs, outputSchemas(t, cs), c.tool, map[string]any{"pr_url": reviewPRURL(g)})
			rc, _ := out["repo_config"].(map[string]any)
			ignored, isList := rc["ignored"].([]any)
			if rc["status"] != "applied" || !isList || len(ignored) != 0 {
				t.Errorf("repo_config = %v, want applied with ignored []", rc)
			}
		})
	}
}

// Answers of the edge cases: a review and suggestions that find nothing.
const (
	emptyReviewAnswer = "```yaml\nreview:\n  estimated_effort_to_review: 1\n  relevant_tests: No\n" +
		"  key_issues_to_review: []\n  security_concerns: No\n  performance_concerns: No\n```\n"
	emptyImproveAnswer = "```yaml\ncode_suggestions: []\n```\n"
)

// list returns the JSON array at key of m, failing when it is absent or not
// an array (null included).
func list(t *testing.T, m map[string]any, key string) []any {
	t.Helper()
	v, ok := m[key].([]any)
	if !ok {
		t.Fatalf("%s is %#v, want an array", key, m[key])
	}
	return v
}

// TestEdgeCaseResultsValidate: the results of the empty edge cases validate
// against the tools' output schemas, as the MCP SDK checks them, and carry
// their lists as [] rather than null: no findings, no notes, no changed
// files, no suggestions, no issue references or detected keys, compliance
// null, a repository config that is absent or ignores nothing, a merged
// publish of no findings, and the same results through job_result.
func TestEdgeCaseResultsValidate(t *testing.T) {
	t.Run("pr_review with no findings and no notes", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		cs := connect(t, realDeps(reviewEnv(g, newFakeLLMHost(t, 200, emptyReviewAnswer)), nil))
		out := callValid(t, cs, outputSchemas(t, cs), "pr_review", map[string]any{"pr_url": reviewPRURL(g)})
		rev, _ := out["review"].(map[string]any)
		if n := len(list(t, rev, "key_issues_to_review")); n != 0 {
			t.Errorf("%d findings", n)
		}
		if n := len(list(t, out, "notes")); n != 0 {
			t.Errorf("notes %v, want none for this edge case", out["notes"])
		}
		rc, _ := out["repo_config"].(map[string]any)
		if rc["status"] != "absent" || len(list(t, rc, "applied")) != 0 || len(list(t, rc, "ignored")) != 0 {
			t.Errorf("repo_config %v", rc)
		}
		is, _ := out["issue"].(map[string]any)
		if is["source"] != "none" || len(list(t, is, "refs")) != 0 || len(list(t, is, "detected_keys")) != 0 ||
			is["compliance"] != nil {
			t.Errorf("issue %v", is)
		}
		md, _ := out["metadata"].(map[string]any)
		list(t, md, "models_used")
	})
	t.Run("pr_review with no changed files", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		g.noFiles = true
		cs := connect(t, realDeps(reviewEnv(g, newFakeLLMHost(t, 200, emptyReviewAnswer)), nil))
		out := callValid(t, cs, outputSchemas(t, cs), "pr_review", map[string]any{"pr_url": reviewPRURL(g)})
		cov, _ := out["coverage"].(map[string]any)
		for _, k := range []string{"included", "clipped", "deleted_listed", "skipped", "filtered"} {
			if n := len(list(t, cov, k)); n != 0 {
				t.Errorf("coverage.%s has %d entries", k, n)
			}
		}
		om, _ := cov["omitted"].(map[string]any)
		for _, k := range []string{"added", "modified", "deleted"} {
			list(t, om, k)
		}
	})
	t.Run("pr_review merged publish of no findings", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		cs := connect(t, realDeps(reviewEnv(g, newFakeLLMHost(t, 200, emptyReviewAnswer)), nil))
		out := callValid(t, cs, outputSchemas(t, cs), "pr_review", map[string]any{"pr_url": reviewPRURL(g),
			"publish": true, "findings": []any{}})
		rev, _ := out["review"].(map[string]any)
		list(t, rev, "key_issues_to_review")
		list(t, out, "enabled_fields")
	})
	t.Run("pr_review with a dropped compliance answer", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		g.repoConfig = "[review]\nissue_compliance = true\n"
		cs := connect(t, realDeps(reviewEnv(g, newFakeLLMHost(t, 200, emptyReviewAnswer)), nil))
		out := callValid(t, cs, outputSchemas(t, cs), "pr_review", map[string]any{"pr_url": reviewPRURL(g),
			"issue_context": []any{map[string]any{"key": "PROJ-1", "title": "Retry"}}})
		is, _ := out["issue"].(map[string]any)
		if is["source"] != "argument" || is["compliance"] != nil || len(list(t, is, "detected_keys")) != 0 {
			t.Errorf("issue %v", is)
		}
	})
	t.Run("pr_describe with no changed files", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		g.noFiles = true
		cs := connect(t, realDeps(reviewEnv(g, newFakeLLMHost(t, 200, describeAnswer)), nil))
		out := callValid(t, cs, outputSchemas(t, cs), "pr_describe", map[string]any{"pr_url": reviewPRURL(g)})
		list(t, out, "files")
		list(t, out, "notes")
		is, _ := out["issue"].(map[string]any)
		list(t, is, "refs")
		list(t, is, "detected_keys")
	})
	t.Run("pr_improve with no suggestions", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		cs := connect(t, realDeps(reviewEnv(g, newImproveLLMHost(t, emptyImproveAnswer, improveReflection)), nil))
		out := callValid(t, cs, outputSchemas(t, cs), "pr_improve", map[string]any{"pr_url": reviewPRURL(g)})
		if n := len(list(t, out, "suggestions")); n != 0 {
			t.Errorf("%d suggestions", n)
		}
		list(t, out, "notes")
	})
	t.Run("pr_improve with no changed files", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		g.noFiles = true
		cs := connect(t, realDeps(reviewEnv(g, newImproveLLMHost(t, emptyImproveAnswer, improveReflection)), nil))
		out := callValid(t, cs, outputSchemas(t, cs), "pr_improve", map[string]any{"pr_url": reviewPRURL(g)})
		list(t, out, "suggestions")
	})
	t.Run("job_result of an empty review", func(t *testing.T) {
		g := newFakeGiteaHost(t)
		g.repoConfig = "[review]\nissue_compliance = false\n"
		d := withJobs(t, realDeps(reviewEnv(g, newFakeLLMHost(t, 200, emptyReviewAnswer)), nil), nil)
		cs := connect(t, d)
		schemas := outputSchemas(t, cs)
		first := callValid(t, cs, schemas, "pr_review", map[string]any{"pr_url": reviewPRURL(g), "wait_seconds": 0})
		id, _ := first["job_id"].(string)
		if id == "" {
			// The run finished within the wait: the result itself was checked.
			return
		}
		out := callValid(t, cs, schemas, "job_result", map[string]any{"job_id": id, "wait_seconds": 30})
		rc, _ := out["repo_config"].(map[string]any)
		if rc["status"] != "applied" || len(list(t, rc, "ignored")) != 0 {
			t.Errorf("job_result %v", out)
		}
	})
}
