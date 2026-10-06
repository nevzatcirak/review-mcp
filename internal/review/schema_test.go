package review

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestResultSchemaMatchesStructs: every struct in Result has exactly the
// properties the schema declares, recursively.
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
		if got, want := sortedKeys(props), jsonKeys(t, typ); !slices.Equal(got, want) {
			t.Errorf("%s: schema %v, struct %v", path, got, want)
		}
		for i := range typ.NumField() {
			f := typ.Field(i)
			name := jsonName(f)
			if sub, ok := props[name].(map[string]any); ok {
				walk(path+"."+name, sub, f.Type)
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

// TestResultSchemaAcceptedBySDK registers a tool with ResultSchema as its
// output schema and returns real results through the MCP SDK, which
// resolves the schema and validates every structured result against it.
func TestResultSchemaAcceptedBySDK(t *testing.T) {
	results := map[string]*Result{}
	h := newHarness(goodAnswer)
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	results["full"] = res
	h = newHarness(goodAnswer)
	h.deps.Config.Ignore.Glob = []string{"**"}
	h.deps.Config.Review.RequireEffortEstimate = false
	if results["empty"], err = Run(context.Background(), h.deps, Args{PRURL: testPRURL}); err != nil {
		t.Fatal(err)
	}

	bad := *results["full"]
	badReview := *bad.Review
	nine := 9
	badReview.EstimatedEffortToReview = &nine
	bad.Review = &badReview
	results["invalid"] = &bad

	type in struct {
		Name string `json:"name"`
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "schema-test", Version: "0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "review", OutputSchema: ResultSchema()},
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
		r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "review", Arguments: map[string]any{"name": name}})
		if name == "invalid" {
			// Proves the SDK validates results against the schema.
			if err == nil && !r.IsError {
				t.Errorf("an effort of 9 passed the output schema")
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
