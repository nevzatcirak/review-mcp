package improve

import (
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	modulePath = "github.com/nevzatcirak/review-mcp"
	repoRoot   = "../.."
)

// forbidden are the packages that must not be in the dependency closure of
// internal/improve (and of internal/improve/render). pr_improve shares with
// pr_review only through internal/llmrun, internal/repoctx (repository
// context, X-22, as pr_review uses it) and the YAML loader, and knows
// nothing of the other tools or of the tool and transport layers.
var forbidden = []string{
	modulePath + "/internal/review",
	modulePath + "/internal/ask",
	modulePath + "/internal/describe",
	modulePath + "/internal/tools",
	modulePath + "/internal/mcpserver",
	modulePath + "/internal/wiring",
}

// closureByParsing walks the import declarations of the non-test files of
// pkg and of every in-repo package they import, and returns every import
// path found. External modules are recorded but not walked.
func closureByParsing(t *testing.T, pkg string) []string {
	t.Helper()
	seen := map[string]bool{}
	var walk func(path string)
	walk = func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		if !strings.HasPrefix(path, modulePath+"/") {
			return
		}
		dir := filepath.Join(repoRoot, strings.TrimPrefix(path, modulePath+"/"))
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range af.Imports {
				walk(strings.Trim(imp.Path.Value, `"`))
			}
		}
	}
	walk(pkg)
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// TestDependencyClosure enforces the dependency rule for the whole closure
// of internal/improve and internal/improve/render, by
// parsing imports (always) and by `go list -deps` (when the go tool is
// available).
func TestDependencyClosure(t *testing.T) {
	for _, pkg := range []string{modulePath + "/internal/improve", modulePath + "/internal/improve/render"} {
		deps := closureByParsing(t, pkg)
		// Sanity: the walk reached packages improve depends on directly
		// and indirectly.
		for _, want := range []string{modulePath + "/internal/llmrun", modulePath + "/internal/yamlrepair", modulePath + "/internal/tokens"} {
			if !slices.Contains(deps, want) {
				t.Fatalf("closure parsing of %s did not reach %s: %v", pkg, want, deps)
			}
		}
		for _, f := range forbidden {
			if slices.Contains(deps, f) {
				t.Errorf("%s depends on %s (by import parsing)", pkg, f)
			}
		}
	}

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Log("go tool not found; only the import-parsing check ran")
		return
	}
	cmd := exec.Command(goBin, "list", "-deps", "-test=false", ".", "./render") //nolint:gosec // fixed arguments
	out, err := cmd.Output()
	if err != nil {
		t.Logf("go list failed (%v); only the import-parsing check ran", err)
		return
	}
	listed := strings.Fields(string(out))
	if !slices.Contains(listed, modulePath+"/internal/llmrun") {
		t.Fatalf("go list -deps did not reach internal/llmrun: %v", listed)
	}
	for _, f := range forbidden {
		if slices.Contains(listed, f) {
			t.Errorf("internal/improve depends on %s (by go list -deps)", f)
		}
	}
}
