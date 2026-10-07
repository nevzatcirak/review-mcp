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
//     occurrence starts the question. A full path may also be written with
//     a leading "./": the "./" is then skipped and the same rule applies to
//     the character before it.
//   - After the occurrence come zero or more '.' (the full stop of a
//     sentence, "an ellipsis..."), and then a character that is not a path
//     character, or the end of the question.
//
// Path characters are letters and digits (any script), '.', '/', '-' and
// '_': the characters a path segment is usually made of. Everything else
// bounds a token: white space, backticks, quotes, brackets, and punctuation
// such as '?', ',', ':', ';', '!'. So "src/a.go?", "`src/a.go`", "see
// src/a.go.", "./src/a.go" and "(./src/a.go)" name src/a.go, while
// "xmain.go", "main.go_old", "main.go-v2", "pkg/main.go" (for cmd/main.go by
// base name; "main.go" alone would name it), "../src/a.go" and "x./src/a.go"
// do not name the file through that token. The "./" is accepted before a
// full path only, not before a base name.
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
		if containsToken(question, f.Path, true) ||
			(utf8.RuneCountInString(base) >= MinPinnedBaseName && containsToken(question, base, false)) {
			out = append(out, f.Path)
		}
	}
	return out
}

// containsToken reports whether s occurs in text as a whole token (see
// PinnedFiles). dotSlash also accepts an occurrence written with a leading
// "./" (full paths only).
func containsToken(text, s string, dotSlash bool) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], s)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(s)
		if boundaryBefore(text, start) || (dotSlash && strings.HasSuffix(text[:start], "./") && boundaryBefore(text, start-2)) {
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

// boundaryBefore reports whether a token may start at byte offset i of text:
// i is the start of text, or the character before it is not a path
// character.
func boundaryBefore(text string, i int) bool {
	if i == 0 {
		return true
	}
	before, _ := utf8.DecodeLastRuneInString(text[:i])
	return !isPathRune(before)
}

// isPathRune reports whether r can be part of a path segment for the
// token rule of PinnedFiles.
func isPathRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '/' || r == '-' || r == '_'
}
