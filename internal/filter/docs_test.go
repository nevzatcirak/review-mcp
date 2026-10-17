package filter

import "testing"

func TestIsDocumentation(t *testing.T) {
	for p, want := range map[string]bool{
		"README.md": true, "docs/setup.md": true, "docs/guide.MDX": true, "docs/index.rst": true,
		"manual/book.adoc": true, "notes.txt": true, "README": true, "pkg/Readme": true, "Readme.RST": true,
		"CHANGELOG": true, "CONTRIBUTING": true, "CONTRIBUTING.adoc": true, "README_FIRST": true, `docs\win.md`: true,
		// Named like documentation, but with a code or other extension (DQ-7).
		"readme_test.go": false, "changelog.go": false, "pkg/README.go": false, "changelog.yaml": false,
		"readme.de": false, "CONTRIBUTING.py": false,
		"main.go": false, "docs/conf.py": false, "README.go.tmpl.bak/x.go": false, "src/readme_test.go": false,
		"internal/llm/client.go": false, "markdown/x.go": false, "doc.go": false, "md": false,
	} {
		if got := IsDocumentation(p); got != want {
			t.Errorf("IsDocumentation(%q) = %v, want %v", p, got, want)
		}
	}
}
