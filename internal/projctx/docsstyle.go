package projctx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/prompt"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// The documentation style of a run (X-51, v2.9 spec V-19 §2.2): the file
// review.docs_style_file of the repository config, read at the base
// revision, else review.docs_style. Its text goes to the user prompt, never
// to a log or a note.

// MaxDocsStyleFileBytes is the most bytes of a documentation style file the
// prompt carries (8 KiB); a longer file is cut at a line boundary.
const MaxDocsStyleFileBytes = 8 * 1024

// DocsStyleSentence is the only sentence of the system prompt about the
// documentation style (architect review of WP-23a, DQ-6, verbatim): the
// style itself is in the user message.
const DocsStyleSentence = "The project's documentation style, when given, is in the user message; follow it where it " +
	"does not conflict with this task."

// Fixed notes of the documentation style file (§2.2). They never hold the
// file's text.
const (
	NoteDocsStyleFileNotFoundText = "The documentation style file was not found at the base revision; review.docs_style was used."
	NoteDocsStyleFileNotFoundNone = "The documentation style file was not found at the base revision; no documentation style was used."
	NoteDocsStyleFileUnreadText   = "The documentation style file could not be read at the base revision; review.docs_style was used."
	NoteDocsStyleFileUnreadNone   = "The documentation style file could not be read at the base revision; no documentation style was used."
	NoteDocsStyleFileEmptyText    = "The documentation style file at the base revision holds no text; review.docs_style was used."
	NoteDocsStyleFileEmptyNone    = "The documentation style file at the base revision holds no text; no documentation style was used."
	NoteDocsStyleFileCut          = "The documentation style file is larger than 8 KiB and was cut at a line boundary."
	NoteDocsStyleFileReplaced     = "The documentation style file replaced review.docs_style, which was also set."
)

// The values of coverage.docs_style.source.
const (
	DocsStyleFromFile = "file"
	DocsStyleFromText = "text"
	DocsStyleNone     = "none"
)

// DocsStyleText is the documentation style a run uses.
type DocsStyleText struct {
	// Text is the cleaned style, "" when there is none.
	Text string
	// Coverage is coverage.docs_style.
	Coverage llmrun.DocsStyle
	// Notes are the fixed notes of reading the file.
	Notes []string
}

// ReadDocsStyle resolves the documentation style of a run: file, a
// repository-relative path ("" for none), is read once from src (the base
// revision); a missing, unreadable or empty file falls back to text
// (review.docs_style), each with a fixed note. A file larger than
// MaxDocsStyleFileBytes is cut at a line boundary. It never fails and never
// logs or quotes the text. src may be nil, which reads nothing.
func ReadDocsStyle(ctx context.Context, src Source, file, text string, log *slog.Logger) DocsStyleText {
	if file == "" || src == nil {
		return docsStyleFromText(text)
	}
	b, err := src.FileAt(ctx, file)
	switch {
	case errors.Is(err, provider.ErrNotFound):
		return docsStyleFromText(text, NoteDocsStyleFileNotFoundText, NoteDocsStyleFileNotFoundNone)
	case err != nil:
		if log != nil {
			log.Debug("docs style: file not read", "error", errorClass(err))
		}
		return docsStyleFromText(text, NoteDocsStyleFileUnreadText, NoteDocsStyleFileUnreadNone)
	case bytes.IndexByte(b, 0) >= 0:
		// A NUL byte: not a text file.
		return docsStyleFromText(text, NoteDocsStyleFileUnreadText, NoteDocsStyleFileUnreadNone)
	}
	body := prompt.CleanText(string(b))
	if body == "" {
		return docsStyleFromText(text, NoteDocsStyleFileEmptyText, NoteDocsStyleFileEmptyNone)
	}
	body, cut := cutAtLine(body, MaxDocsStyleFileBytes)
	out := DocsStyleText{Text: body, Coverage: llmrun.DocsStyle{Source: DocsStyleFromFile, Bytes: len(body), Cut: cut}}
	if cut {
		out.Notes = append(out.Notes, NoteDocsStyleFileCut)
	}
	if strings.TrimSpace(text) != "" {
		out.Notes = append(out.Notes, NoteDocsStyleFileReplaced)
	}
	return out
}

// docsStyleFromText is the style of review.docs_style (source text, or none
// when nothing is left of it). With two notes, the file was set but not
// used: the first is the note when text is set, the second when it is not.
func docsStyleFromText(text string, notes ...string) DocsStyleText {
	body := prompt.CleanText(text)
	out := DocsStyleText{Text: body}
	if body == "" {
		out.Coverage = llmrun.DocsStyle{Source: DocsStyleNone}
	} else {
		out.Coverage = llmrun.DocsStyle{Source: DocsStyleFromText, Bytes: len(body)}
	}
	if len(notes) == 2 {
		if body != "" {
			out.Notes = []string{notes[0]}
		} else {
			out.Notes = []string{notes[1]}
		}
	}
	return out
}

// cutAtLine returns s whole when it has at most limit bytes; otherwise its
// longest prefix of whole lines of at most limit bytes (without the final
// line break), and true. When even the first line is longer than limit, the
// prefix is cut at a rune boundary.
func cutAtLine(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	end := strings.LastIndexByte(s[:limit+1], '\n')
	if end <= 0 {
		end = limit
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
	}
	return strings.TrimRight(s[:end], "\n"), true
}
