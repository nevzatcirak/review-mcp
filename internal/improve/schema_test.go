package improve

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/llm"
)

// TestResultSchemaMatchesStructs: every struct in Result has exactly the
// properties the schema declares, recursively, and every property is
// required except those the struct marks omitempty (publish and its
// optional fields, as in pr_review's schema).
func TestResultSchemaMatchesStructs(t *testing.T) {
	var walk func(path string, s map[string]any, typ reflect.Type)
	walk = func(path string, s map[string]any, typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			if typ.Kind() == reflect.Slice {
				items, ok := s["items"].(map[string]any)
				if !ok {
					t.Errorf("%s: slice without items schema", path)
					return
				}
				s = items
			}
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		props, ok := s["properties"].(map[string]any)
		if !ok {
			t.Errorf("%s: struct without properties", path)
			return
		}
		got, want := sortedKeys(props), jsonKeys(typ)
		if !slices.Equal(got, want) {
			t.Errorf("%s: schema %v, struct %v", path, got, want)
		}
		var req []string
		for _, r := range s["required"].([]any) {
			req = append(req, r.(string))
		}
		slices.Sort(req)
		if wantReq := requiredKeys(typ); !slices.Equal(req, wantReq) {
			t.Errorf("%s: required %v, want every property without omitempty %v", path, req, wantReq)
		}
		for i := range typ.NumField() {
			f := typ.Field(i)
			if sub, ok := props[jsonName(f)].(map[string]any); ok {
				walk(path+"."+jsonName(f), sub, f.Type)
			}
		}
	}
	walk("result", ResultSchema(), reflect.TypeFor[Result]())
}

func jsonName(f reflect.StructField) string {
	n := f.Tag.Get("json")
	for i := range len(n) {
		if n[i] == ',' {
			return n[:i]
		}
	}
	return n
}

// requiredKeys are the json keys of typ without omitempty.
func requiredKeys(typ reflect.Type) []string {
	var out []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		if n := jsonName(f); n != "" && n != "-" && !strings.Contains(f.Tag.Get("json"), "omitempty") {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func sortedKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func jsonKeys(typ reflect.Type) []string {
	var out []string
	for i := range typ.NumField() {
		if n := jsonName(typ.Field(i)); n != "" && n != "-" {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// TestResultSchemaAcceptedBySDK registers a tool with ResultSchema as its
// output schema and returns real results through the MCP SDK, which
// resolves the schema and validates every structured result against it: a
// one-call result, a three-part result, a result whose self-review failed
// (null scores), an empty result and a verified result. A score outside 0
// to 10, an unknown unverified_reason and an anchor with a field are
// rejected.
func TestResultSchemaAcceptedBySDK(t *testing.T) {
	results := map[string]any{}
	results["one call"] = newHarness(oneCallAnswers()).run(t, Args{})
	results["parts"] = threePartHarness(t).run(t, Args{})
	h := newHarness(oneCallAnswers())
	h.llm.errs[reflectKind(0)] = &llm.Error{Class: llm.ClassTimeout}
	results["unscored"] = h.run(t, Args{})
	h = newHarness(nil)
	h.deps.Config.Ignore.Glob = []string{"**"}
	results["empty"] = h.run(t, Args{})
	h = newHarness(oneCallAnswers())
	h.prov.files = headSampleFiles()
	results["verified"] = h.run(t, Args{})
	if !results["verified"].(*Result).Suggestions[0].Verified {
		t.Fatal("the verified result has no verified suggestion")
	}

	bad := *results["one call"].(*Result)
	bad.Suggestions = append([]Suggestion(nil), bad.Suggestions...)
	eleven := 11
	bad.Suggestions[0].Score = &eleven
	results["invalid score"] = &bad
	badReason := *results["one call"].(*Result)
	badReason.Suggestions = append([]Suggestion(nil), badReason.Suggestions...)
	badReason.Suggestions[0].UnverifiedReason = "unknown"
	results["invalid reason"] = &badReason
	raw := marshal(t, results["one call"])
	var anchored map[string]any
	if err := json.Unmarshal([]byte(strings.Replace(raw, `"anchor": null`, `"anchor": {"line": 3}`, 1)), &anchored); err != nil {
		t.Fatal(err)
	}
	results["invalid anchor"] = anchored

	type in struct {
		Name string `json:"name"`
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "schema-test", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "improve", OutputSchema: ResultSchema()},
		func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, any, error) {
			return nil, results[a.Name], nil
		})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	for name := range results {
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "improve", Arguments: map[string]any{"name": name}})
		if strings.HasPrefix(name, "invalid") {
			// Proves the SDK validates results against the schema.
			if err == nil && !r.IsError {
				t.Errorf("%s passed the output schema", name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := json.Marshal(r.Content)
		if r.IsError {
			t.Fatalf("%s: the SDK rejected the result: %s", name, b)
		}
	}
}

// TestInterimFields: until WP-2h every suggestion has a null anchor; the
// JSON carries it and the verification fields on every suggestion.
func TestInterimFields(t *testing.T) {
	res := newHarness(oneCallAnswers()).run(t, Args{})
	if len(res.Suggestions) == 0 {
		t.Fatal("no suggestion")
	}
	for _, s := range res.Suggestions {
		if s.Anchor != nil {
			t.Errorf("suggestion %q: anchor %v", s.Summary, s.Anchor)
		}
	}
	raw := marshal(t, res)
	if strings.Count(raw, `"anchor": null`) != len(res.Suggestions) || strings.Count(raw, `"verified": `) != len(res.Suggestions) ||
		strings.Count(raw, `"unverified_reason": `) != len(res.Suggestions) {
		t.Errorf("JSON lacks the anchor or the verification fields:\n%s", raw)
	}
}
