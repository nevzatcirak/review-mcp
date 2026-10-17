package filter

import "testing"

func TestIsDocumentation(t *testing.T) {
	for p, want := range map[string]bool{
		"README.md": true, "docs/setup.md": true, "docs/guide.MDX": true, "docs/index.rst": true,
		"manual/book.adoc": true, "notes.txt": true, "README": true, "readme.de": true, "pkg/Readme": true,
		"CHANGELOG": true, "changelog.yaml": true, "CONTRIBUTING": true, `docs\win.md`: true,
		"main.go": false, "docs/conf.py": false, "README.go.tmpl.bak/x.go": false, "src/readme_test.go": true,
		"internal/llm/client.go": false, "markdown/x.go": false, "doc.go": false, "md": false,
	} {
		if got := IsDocumentation(p); got != want {
			t.Errorf("IsDocumentation(%q) = %v, want %v", p, got, want)
		}
	}
}
