package filter

import (
	"path"
	"strings"
)

// docExtensions are the extensions of documentation files (v2.1 spec V-5
// §3a item 4), lower case.
var docExtensions = map[string]bool{".md": true, ".mdx": true, ".rst": true, ".adoc": true, ".txt": true}

// docPrefixes start the base name of a documentation file that has no
// extension (README, CHANGELOG), compared without regard to case.
var docPrefixes = []string{"readme", "changelog", "contributing"}

// IsDocumentation reports whether p names a documentation file: its
// extension is .md, .mdx, .rst, .adoc or .txt, or it has no extension and
// its base name starts with README, CHANGELOG or CONTRIBUTING (both without
// regard to case). A code file named like one (readme_test.go,
// changelog.go) is code. Doc comments in code are not documentation files:
// they are reviewed where they appear in the diff.
func IsDocumentation(p string) bool {
	base := strings.ToLower(path.Base(strings.ReplaceAll(p, "\\", "/")))
	ext := path.Ext(base)
	if docExtensions[ext] {
		return true
	}
	if ext != "" {
		return false
	}
	for _, pre := range docPrefixes {
		if strings.HasPrefix(base, pre) {
			return true
		}
	}
	return false
}
