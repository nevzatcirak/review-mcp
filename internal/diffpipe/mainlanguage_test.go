package diffpipe

import (
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestMainLanguage(t *testing.T) {
	big := func(path string) provider.FilePatch { return added(path, 40) }
	small := func(path string) provider.FilePatch { return added(path, 2) }
	tests := []struct {
		name  string
		files []provider.FilePatch
		want  string
	}{
		{"empty", nil, ""},
		{"heaviest group wins over order", []provider.FilePatch{small("a.py"), big("b.go"), small("c.py")}, "Go"},
		{"other comes last even when heavy", []provider.FilePatch{big("notes.unknownext"), small("a.go")}, "Go"},
		{"only other", []provider.FilePatch{small("notes.unknownext")}, "Other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := MainLanguage(tc.files); got != tc.want {
				t.Errorf("MainLanguage = %q, want %q", got, tc.want)
			}
		})
	}
}
