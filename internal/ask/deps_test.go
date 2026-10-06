package ask

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
// internal/ask (spec P5 §3: no YAML or repair code in pr_ask).
// internal/review imports internal/yamlrepair, so it is forbidden as well.
var forbidden = []string{
	modulePath + "/internal/yamlrepair",
	modulePath + "/internal/review",
	"gopkg.in/yaml.v3",
	"gopkg.in/yaml.v2",
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

// TestNoYAMLInDependencyClosure [canary] enforces the hard rule of spec P5
// §3 for the whole dependency closure, by parsing imports (always) and by
// `go list -deps` (when the go tool is available).
func TestNoYAMLInDependencyClosure(t *testing.T) {
	deps := closureByParsing(t, modulePath+"/internal/ask")
	// Sanity: the walk reached packages ask depends on directly and
	// indirectly.
	for _, want := range []string{modulePath + "/internal/prompt", modulePath + "/internal/tokens"} {
		if !slices.Contains(deps, want) {
			t.Fatalf("closure parsing did not reach %s: %v", want, deps)
		}
	}
	for _, f := range forbidden {
		if slices.Contains(deps, f) {
			t.Errorf("internal/ask depends on %s (by import parsing)", f)
		}
	}

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Log("go tool not found; only the import-parsing check ran")
		return
	}
	cmd := exec.Command(goBin, "list", "-deps", "-test=false", ".") //nolint:gosec // fixed arguments
	out, err := cmd.Output()
	if err != nil {
		t.Logf("go list failed (%v); only the import-parsing check ran", err)
		return
	}
	listed := strings.Fields(string(out))
	if !slices.Contains(listed, modulePath+"/internal/prompt") {
		t.Fatalf("go list -deps did not reach internal/prompt: %v", listed)
	}
	for _, f := range forbidden {
		if slices.Contains(listed, f) {
			t.Errorf("internal/ask depends on %s (by go list -deps)", f)
		}
	}
}
