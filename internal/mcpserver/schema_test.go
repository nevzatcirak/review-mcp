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
