package data

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

var countRE = regexp.MustCompile(`(\w+)=(\d+)`)

// headerCounts reads the "Entry counts:" line of a data file.
func headerCounts(t *testing.T, file string) map[string]int {
	t.Helper()
	src, err := os.ReadFile(file) //nolint:gosec // test reads fixed sibling file names
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^// Entry counts: (.*)$`).FindSubmatch(src)
	if line == nil {
		t.Fatalf("%s: no Entry counts header", file)
	}
	out := map[string]int{}
	for _, m := range countRE.FindAllStringSubmatch(string(line[1]), -1) {
		n, _ := strconv.Atoi(m[2])
		out[m[1]] = n
	}
	return out
}

func TestHeaderCountsMatchData(t *testing.T) {
	globs := 0
	for _, v := range GeneratedCode {
		globs += len(v)
	}
	exts := 0
	for _, e := range LanguageExtensions {
		exts += len(e.Extensions)
	}
	tests := []struct {
		file string
		want map[string]int
	}{
		{"generated_code.go", map[string]int{"frameworks": len(GeneratedCode), "globs": globs}},
		{"bad_extensions.go", map[string]int{"bad_extensions": len(BadExtensions)}},
		{"languages.go", map[string]int{"languages": len(LanguageExtensions), "extensions": exts}},
		{"auto_generated.go", map[string]int{"lockfiles": len(LockfileNames), "minified_suffixes": len(MinifiedSuffixes)}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got := headerCounts(t, tt.file)
			for k, w := range tt.want {
				if got[k] != w {
					t.Errorf("%s: header %s=%d, data has %d", tt.file, k, got[k], w)
				}
			}
		})
	}
}

func TestFrameworkNamesSorted(t *testing.T) {
	names := FrameworkNames()
	if len(names) != len(GeneratedCode) {
		t.Fatalf("got %d names, want %d", len(names), len(GeneratedCode))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("names not sorted: %v", names)
		}
	}
}

func TestBadExtensionsLowercase(t *testing.T) {
	for _, e := range BadExtensions {
		if e == "" || e != lower(e) {
			t.Errorf("bad extension %q must be non-empty lowercase", e)
		}
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
