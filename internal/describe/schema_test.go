package describe

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
// one-call result, a three-part result, a failed reduce call (null title and
// type) and an empty result. A type outside the enum is rejected.
func TestResultSchemaAcceptedBySDK(t *testing.T) {
	results := map[string]*Result{}
	results["one call"] = newHarness(map[int][]string{0: {oneCallAnswer}}).run(t, Args{})
	results["parts"] = threePartHarness(t).run(t, Args{})
	h := threePartHarness(t)
	h.llm.errs[kindReduce] = &llm.Error{Class: llm.ClassTimeout}
	results["reduce failed"] = h.run(t, Args{})
	h = newHarness(nil)
	h.deps.Config.Ignore.Glob = []string{"**"}
	results["empty"] = h.run(t, Args{})

	bad := *results["one call"]
	bad.Type = []string{"Feature"}
	results["invalid"] = &bad

	type in struct {
		Name string `json:"name"`
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "schema-test", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "describe", OutputSchema: ResultSchema()},
		func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, *Result, error) {
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
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "describe", Arguments: map[string]any{"name": name}})
		if name == "invalid" {
			// Proves the SDK validates results against the schema.
			if err == nil && !r.IsError {
				t.Errorf("a type outside the enum passed the output schema")
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
