package gitctx

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

var update = flag.Bool("update", false, "rewrite the symbol goldens")

// loadDiff reads a testdata/symbols/<name>.diff: "=== <path>" starts a file,
// the lines up to the next one are its hunk-only patch.
func loadDiff(t *testing.T, name string) []provider.FilePatch {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "symbols", name+".diff")) //nolint:gosec // G304: test fixture path
	if err != nil {
		t.Fatal(err)
	}
	var files []provider.FilePatch
	var cur *provider.FilePatch
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "=== "); ok {
			files = append(files, provider.FilePatch{Path: strings.TrimSpace(p), Type: provider.ChangeModified})
			cur = &files[len(files)-1]
			continue
		}
		if cur != nil {
			cur.Patch += line
		}
	}
	return files
}

func dumpSymbols(syms []Symbol) string {
	var b strings.Builder
	for _, s := range syms {
		n := ""
		if s.New {
			n = " new"
		}
		if s.Test {
			n += " test"
		}
		fmt.Fprintf(&b, "%-9s %-16s %s%s\n", s.Rank, s.Name, s.Path, n)
	}
	return b.String()
}

// TestSymbolGoldens: the symbols of one diff per language family. Run with
// -update after checking a change on purpose.
func TestSymbolGoldens(t *testing.T) {
	for _, name := range []string{"go", "java_kotlin", "ts_js", "python", "csharp", "ranking"} {
		t.Run(name, func(t *testing.T) {
			got := dumpSymbols(ExtractSymbols(loadDiff(t, name), 100))
			path := filepath.Join("testdata", "symbols", name+".golden")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path) //nolint:gosec // G304: test fixture path
			if err != nil {
				t.Fatalf("%v (run go test ./internal/gitctx -run TestSymbolGoldens -update to create it)", err)
			}
			if got != string(want) {
				t.Errorf("symbols differ from %s (-update after checking)\n--- got\n%s--- want\n%s", path, got, want)
			}
		})
	}
}

// TestExtractSymbolsFilters: the length, keyword and noise filters, the cap,
// the best rank of a repeated name, and lines that are not definitions.
func TestExtractSymbolsFilters(t *testing.T) {
	patchOf := func(lines ...string) []provider.FilePatch {
		return []provider.FilePatch{{Path: "x.go", Patch: "@@ -1,1 +1,1 @@\n" + strings.Join(lines, "\n") + "\n"}}
	}
	names := func(syms []Symbol) string {
		var n []string
		for _, s := range syms {
			n = append(n, s.Name)
		}
		return strings.Join(n, ",")
	}
	fp := patchOf(
		"+func Run() {}",          // too short
		"+func main() {}",         // ubiquitous
		"+func init() {}",         // ubiquitous
		"+func Keep() {}",         // 4 characters: kept
		"+func Another() {}",      //
		"+func Third() {}",        //
		"-func Third() {}",        // removed and added with the same text: moved
		"+func Fourth() {}",       //
		"+x := call(a, b)",        // not a definition
		"+// func Commented() {}", // a comment
	)
	if got := names(ExtractSymbols(fp, 100)); got != "Third,Keep,Another,Fourth" {
		t.Errorf("symbols = %s", got)
	}
	if got := names(ExtractSymbols(fp, 2)); got != "Third,Keep" {
		t.Errorf("capped symbols = %s", got)
	}
	if got := ExtractSymbols(fp, 0); len(got) != 4 {
		t.Errorf("max 0 gave %d symbols, want the default cap to leave all 4", len(got))
	}
	if got := ExtractSymbols([]provider.FilePatch{{Path: "x.go", Patch: "not a patch"}, {Path: "b.bin", Binary: true, Patch: "@@ -1 +1 @@\n+func Never() {}\n"}}, 5); len(got) != 0 {
		t.Errorf("unparsable and binary patches gave %v", got)
	}
}

// TestChangedPaths: the new path and the old path of a rename.
func TestChangedPaths(t *testing.T) {
	got := ChangedPaths([]provider.FilePatch{{Path: "b.go", OldPath: "a.go"}, {Path: "c.go"}, {Path: "a.go"}})
	if strings.Join(got, ",") != "b.go,a.go,c.go" {
		t.Errorf("ChangedPaths = %v", got)
	}
}

// TestIsTestPath: each name rule matches, and look-alikes do not.
func TestIsTestPath(t *testing.T) {
	yes := []string{
		"a_test.go", "pkg/a_test.go", "test_a.py", "a/test_a.py", "a_test.py", "FooTest.java", "FooTests.java",
		"FooTest.kt", "a.test.ts", "a.test.tsx", "a.test.js", "a.test.jsx", "a.spec.ts", "a.spec.tsx",
		"a.spec.js", "a.spec.jsx", "FooTests.cs", "FooTest.cs", "test/x.go", "a/tests/x.go", "src/__tests__/x.js",
		"src/test/java/Foo.java",
	}
	no := []string{
		"contest.go", "latest/x.go", "testing.go", "a/testing/x.go", "attests/x.go", "test.go", "_test.go",
		"a_test.txt", "test_a.go", "mytest.py", "Test.java", "FooTester.java", "footest.java", "FooTest.cs.bak",
		"a.test.go", "a.spec.py", "a.tests.ts", "a.test.json", "Tests/x.go", "__test__/x.go", "x/test", "src/Test/x.go",
	}
	for _, p := range yes {
		if !IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsTestPath(p) {
			t.Errorf("IsTestPath(%q) = true, want false", p)
		}
	}
}

// TestExtractSymbolsCapCutsTestsFirst: the cap applies after the full
// ordering, so test-file symbols are cut before any other.
func TestExtractSymbolsCapCutsTestsFirst(t *testing.T) {
	files := loadDiff(t, "ranking")
	all := ExtractSymbols(files, 100)
	nonTest := 0
	for _, s := range all {
		if !s.Test {
			nonTest++
		}
	}
	for _, s := range ExtractSymbols(files, nonTest) {
		if s.Test {
			t.Errorf("test symbol %s kept while a non-test one was cut", s.Name)
		}
	}
}
