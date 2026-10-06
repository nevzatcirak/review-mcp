package filter

import "testing"

func TestLanguage(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"main.go", "Go"},
		{"a/b/main.go", "Go"},
		{"a\\b\\main.go", "Go"},
		{"MAIN.GO", "Go"},            // unique case-folded match
		{"Dockerfile", "Dockerfile"}, // exact basename token
		{"build/Makefile", "Makefile"},
		{"x.bsl", "1C Enterprise"}, // "*.bsl" token has its star stripped
		{"x.sh.in", "Shell"},       // multi-dot token beats the shorter ".in"
		{"x.rs.in", "Rust"},
		{"CMakeLists.cmake.in", "CMake"},
		{"notes.rst.txt", "reStructuredText"},
		{"a.b.c.d.e.go", "Go"},       // long names do not break the bounded suffix scan
		{"dockerfile", "Dockerfile"}, // unique case fold
		{"x.d.ts", "TypeScript"},
		{"x.c", "C"},
		{"x.C", "C++"}, // exact-case token beats the folded ambiguity
		{"x.ML", "Standard ML"},
		{"x.html.hl", "HTML"},
		{"README", "Other"},
		{"file.unknownext", "Other"},
		{"", "Other"},
		{".", "Other"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := Language(tt.path); got != tt.want {
				t.Errorf("Language(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestLanguageAmbiguousFold: ".mL" is not an exact token, and its case-folded
// form is claimed by two languages, so it must not be guessed.
func TestLanguageAmbiguousFold(t *testing.T) {
	m := buildMatcher()
	if _, ok := m.exact[".mL"]; ok {
		t.Fatal("test premise broken: .mL is an exact token")
	}
	if n := len(m.folded[".ml"]); n < 2 {
		t.Fatalf("test premise broken: .ml maps to %d languages", n)
	}
	tests := []struct{ path, want string }{
		{"a.ml", "OCaml"}, // exact match wins; the first table entry owns it
		{"a.mL", OtherLanguage},
	}
	for _, tt := range tests {
		if got := Language(tt.path); got != tt.want {
			t.Errorf("Language(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
