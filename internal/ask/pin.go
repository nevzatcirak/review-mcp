package ask

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// MinPinnedBaseName is the shortest base name (in characters) that pins a
// file by itself (X-21): shorter ones ("a.go", "util") are too likely to
// occur in a question by accident.
const MinPinnedBaseName = 5

// PinnedFiles returns the paths of the changed files the question names
// (X-21, v1.1 spec WP-11e2 item 6), in the order of files. Prepare admits
// them first; pr_ask still makes one call.
//
// A file is named when its full path, or its base name of at least
// MinPinnedBaseName characters, occurs in the question as a whole token.
// The match is case-sensitive. "Whole token" means:
//
//   - The character before the occurrence is not a path character, or the
//     occurrence starts the question.
//   - After the occurrence come zero or more '.' (the full stop of a
//     sentence, "an ellipsis..."), and then a character that is not a path
//     character, or the end of the question.
//
// Path characters are letters and digits (any script), '.', '/', '-' and
// '_': the characters a path segment is usually made of. Everything else
// bounds a token: white space, backticks, quotes, brackets, and punctuation
// such as '?', ',', ':', ';', '!'. So "src/a.go?", "`src/a.go`" and "see
// src/a.go." name src/a.go, while "xmain.go", "main.go_old", "main.go-v2",
// "pkg/main.go" (for cmd/main.go by base name; "main.go" alone would name
// it) and "./src/a.go" do not name the file through that token.
//
// Only files is searched: the reviewable files after filtering, so a
// filtered or provider-skipped file is never pinned. Several files with the
// same named base name are all pinned.
func PinnedFiles(question string, files []provider.FilePatch) []string {
	var out []string
	for _, f := range files {
		if f.Path == "" {
			continue
		}
		base := path.Base(f.Path)
		if containsToken(question, f.Path) ||
			(utf8.RuneCountInString(base) >= MinPinnedBaseName && containsToken(question, base)) {
			out = append(out, f.Path)
		}
	}
	return out
}

// containsToken reports whether s occurs in text as a whole token (see
// PinnedFiles).
func containsToken(text, s string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], s)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(s)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		if start == 0 || !isPathRune(before) {
			rest := strings.TrimLeft(text[end:], ".")
			after, _ := utf8.DecodeRuneInString(rest)
			if rest == "" || !isPathRune(after) {
				return true
			}
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		from = start + size
	}
}

// isPathRune reports whether r can be part of a path segment for the
// token rule of PinnedFiles.
func isPathRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '/' || r == '-' || r == '_'
}
