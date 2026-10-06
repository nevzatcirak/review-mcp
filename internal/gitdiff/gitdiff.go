// Package gitdiff parses the output of `git diff` (unified format with git
// extended headers), for example the body served by a Gitea ".diff" endpoint.
//
// The parser is hunk-aware: once a "@@ -a,b +c,d @@" header is seen, exactly
// the announced number of old/new lines is consumed as hunk content, so a
// content line such as "+diff --git a/x b/x" is never mistaken for a file
// header.
package gitdiff

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ChangeType is the kind of change applied to a file.
type ChangeType string

// Supported change types.
const (
	Added    ChangeType = "added"
	Modified ChangeType = "modified"
	Deleted  ChangeType = "deleted"
	Renamed  ChangeType = "renamed"
)

// File is the parsed change of one file in a diff.
type File struct {
	// Path is the new path. For deletions it is the old path.
	Path string
	// OldPath is set only for renames.
	OldPath string
	Type    ChangeType
	// Binary is true for binary files; they carry no hunks.
	Binary bool
	// Patch is the byte-exact hunk-only text, from the first "@@" line to the
	// end of the file's section. It is empty for binary and mode-only changes.
	Patch     string
	Additions int
	Deletions int
}

const (
	diffPrefix = "diff --git "
	hunkPrefix = "@@ "
	devNull    = "/dev/null"
)

// Parse reads the whole of r and parses it as git diff output.
//
// Empty input yields no files and no error. Malformed hunks produce an error
// that names the file path and never echoes diff content.
//
// Parse does not bound the input size: the caller (the provider HTTP client)
// already caps the response body (diff.max_diff_bytes) and reports overflow.
func Parse(r io.Reader) ([]File, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("gitdiff: read input: %w", err)
	}
	p := &parser{data: data}
	return p.parse()
}

type parser struct {
	data []byte
	pos  int
	line int // 1-based number of the line most recently returned by next
}

// next consumes the next line and returns it without its terminating "\n" (a
// trailing "\r" is kept) plus the offset where the line starts.
func (p *parser) next() (line []byte, start int, ok bool) {
	if p.pos >= len(p.data) {
		return nil, 0, false
	}
	start = p.pos
	rest := p.data[p.pos:]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		p.pos += i + 1
		line = rest[:i]
	} else {
		p.pos = len(p.data)
		line = rest
	}
	p.line++
	return line, start, true
}

// peek returns the next line and its start offset without consuming it.
func (p *parser) peek() (line []byte, start int, ok bool) {
	pos, ln := p.pos, p.line
	line, start, ok = p.next()
	p.pos, p.line = pos, ln
	return line, start, ok
}

func (p *parser) parse() ([]File, error) {
	var files []File
	for {
		line, _, ok := p.next()
		if !ok {
			return files, nil
		}
		// Anything before the first file header (mail preamble, commit
		// message) is skipped.
		if !bytes.HasPrefix(line, []byte(diffPrefix)) {
			continue
		}
		f, err := p.parseFile(string(line[len(diffPrefix):]))
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
}

// header collects the extended header fields of one file.
type header struct {
	headerLine int // line number of the "diff --git" line

	fbOld, fbNew string // from the diff --git line; valid when fbOK
	fbOK         bool

	newFile, deleted     bool
	renameFrom, renameTo string
	copyTo               string
	hasMinus, hasPlus    bool
	minusPath, plusPath  string
	minusNull, plusNull  bool
	binary               bool
}

func (p *parser) parseFile(rest string) (File, error) {
	h := &header{headerLine: p.line}
	var err error
	h.fbOld, h.fbNew, h.fbOK, err = splitHeaderPaths(rest)
	if err != nil {
		return File{}, fmt.Errorf("gitdiff: invalid quoted path in diff header at line %d: %w", h.headerLine, err)
	}

	patchStart := -1
	// Header phase: read extended headers until the first hunk or next file.
	for {
		line, start, ok := p.peek()
		if !ok || bytes.HasPrefix(line, []byte(diffPrefix)) {
			break
		}
		if bytes.HasPrefix(line, []byte(hunkPrefix)) {
			patchStart = start
			break
		}
		p.next()
		if h.binary {
			continue // binary patch payload; skipped until the next file
		}
		if err := h.apply(string(line), p.line); err != nil {
			return File{}, err
		}
	}

	f, err := h.resolve()
	if err != nil {
		return File{}, err
	}
	if h.binary {
		f.Binary = true
		return f, nil
	}
	if patchStart < 0 {
		return f, nil
	}

	for {
		line, _, ok := p.peek()
		if !ok || bytes.HasPrefix(line, []byte(diffPrefix)) {
			break
		}
		if !bytes.HasPrefix(line, []byte(hunkPrefix)) {
			return File{}, fmt.Errorf("gitdiff: %s: unexpected line %d after hunk", f.Path, p.line+1)
		}
		p.next()
		add, del, err := p.parseHunk(line, f.Path)
		if err != nil {
			return File{}, err
		}
		f.Additions += add
		f.Deletions += del
	}
	f.Patch = string(p.data[patchStart:p.pos])
	return f, nil
}

// apply interprets one extended header line.
func (h *header) apply(line string, lineNo int) error {
	switch {
	case strings.HasPrefix(line, "new file mode "):
		h.newFile = true
	case strings.HasPrefix(line, "deleted file mode "):
		h.deleted = true
	case strings.HasPrefix(line, "rename from "):
		return h.setPath(&h.renameFrom, line[len("rename from "):], lineNo)
	case strings.HasPrefix(line, "rename to "):
		return h.setPath(&h.renameTo, line[len("rename to "):], lineNo)
	case strings.HasPrefix(line, "copy to "):
		return h.setPath(&h.copyTo, line[len("copy to "):], lineNo)
	case strings.HasPrefix(line, "--- ") && !h.hasMinus:
		h.hasMinus = true
		return h.setMarker(&h.minusPath, &h.minusNull, line[4:], "a/", lineNo)
	case strings.HasPrefix(line, "+++ ") && !h.hasPlus:
		h.hasPlus = true
		return h.setMarker(&h.plusPath, &h.plusNull, line[4:], "b/", lineNo)
	case strings.HasPrefix(line, "Binary files "), line == "GIT binary patch":
		h.binary = true
	}
	// index, old/new mode, similarity index, copy from and unknown lines
	// carry nothing we need.
	return nil
}

func (h *header) setPath(dst *string, raw string, lineNo int) error {
	v, err := decodePath(raw)
	if err != nil {
		return fmt.Errorf("gitdiff: invalid quoted path in header at line %d: %w", lineNo, err)
	}
	*dst = v
	return nil
}

// setMarker handles the operand of a "---" or "+++" line.
func (h *header) setMarker(dst *string, null *bool, raw, prefix string, lineNo int) error {
	if !strings.HasPrefix(raw, `"`) {
		// git appends a tab after unquoted names containing a space; a name
		// with a real tab would have been quoted.
		if i := strings.IndexByte(raw, '\t'); i >= 0 {
			raw = raw[:i]
		}
	}
	v, err := decodePath(raw)
	if err != nil {
		return fmt.Errorf("gitdiff: invalid quoted path in header at line %d: %w", lineNo, err)
	}
	if v == devNull {
		*null = true
		return nil
	}
	*dst = strings.TrimPrefix(v, prefix)
	return nil
}

// resolve derives type and path from the collected header.
func (h *header) resolve() (File, error) {
	var f File
	switch {
	case h.renameTo != "":
		f.Type = Renamed
	case h.copyTo != "":
		f.Type = Added // a copy is an addition at the new path
	case h.newFile || (h.hasMinus && h.minusNull):
		f.Type = Added
	case h.deleted || (h.hasPlus && h.plusNull):
		f.Type = Deleted
	default:
		f.Type = Modified
	}

	switch {
	case h.renameTo != "":
		f.Path = h.renameTo
	case h.copyTo != "":
		f.Path = h.copyTo
	case h.hasPlus && !h.plusNull && h.plusPath != "":
		f.Path = h.plusPath
	case h.hasMinus && !h.minusNull && h.minusPath != "":
		f.Path = h.minusPath
	case h.fbOK && f.Type == Deleted:
		f.Path = h.fbOld
	case h.fbOK:
		f.Path = h.fbNew
	}
	if f.Path == "" {
		return File{}, fmt.Errorf("gitdiff: cannot determine file path for diff header at line %d", h.headerLine)
	}
	if f.Type == Renamed {
		f.OldPath = h.renameFrom
	}
	return f, nil
}

// parseHunk consumes the body of the hunk announced by header line hdr,
// returning its addition and deletion counts. Errors name the file but never
// echo content lines.
func (p *parser) parseHunk(hdr []byte, path string) (add, del int, err error) {
	oldN, newN, ok := parseHunkHeader(hdr)
	if !ok {
		return 0, 0, fmt.Errorf("gitdiff: %s: malformed hunk header at line %d", path, p.line)
	}
	for oldN > 0 || newN > 0 {
		line, _, ok := p.next()
		if !ok {
			return 0, 0, fmt.Errorf("gitdiff: %s: hunk ends before its announced line counts", path)
		}
		c := byte(0)
		if len(line) > 0 {
			c = line[0]
		}
		switch {
		case c == ' ' && oldN > 0 && newN > 0:
			oldN--
			newN--
		case c == '-' && oldN > 0:
			oldN--
			del++
		case c == '+' && newN > 0:
			newN--
			add++
		case c == '\\':
			// "\ No newline at end of file" does not count.
		default:
			return 0, 0, fmt.Errorf("gitdiff: %s: hunk line counts do not match at line %d", path, p.line)
		}
	}
	// Trailing "\ No newline at end of file" markers belong to this hunk.
	for {
		line, _, ok := p.peek()
		if !ok || len(line) == 0 || line[0] != '\\' {
			break
		}
		p.next()
	}
	return add, del, nil
}

// parseHunkHeader parses "@@ -a[,b] +c[,d] @@..." and returns b and d (the old
// and new line counts, each defaulting to 1).
func parseHunkHeader(line []byte) (oldN, newN int, ok bool) {
	s, found := strings.CutPrefix(string(line), "@@ -")
	if !found {
		return 0, 0, false
	}
	oldSpec, s, found := strings.Cut(s, " +")
	if !found {
		return 0, 0, false
	}
	newSpec, _, found := strings.Cut(s, " @@")
	if !found {
		return 0, 0, false
	}
	if oldN, ok = rangeCount(oldSpec); !ok {
		return 0, 0, false
	}
	if newN, ok = rangeCount(newSpec); !ok {
		return 0, 0, false
	}
	return oldN, newN, true
}

// rangeCount parses "start[,count]" and returns count (default 1).
func rangeCount(spec string) (int, bool) {
	start, count, hasCount := strings.Cut(spec, ",")
	if !allDigits(start) {
		return 0, false
	}
	if !hasCount {
		return 1, true
	}
	if !allDigits(count) {
		return 0, false
	}
	n, err := strconv.Atoi(count)
	if err != nil {
		return 0, false
	}
	return n, true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// decodePath unquotes a git C-style quoted path (octal and C escapes, with
// strconv.Unquote semantics); unquoted input is returned unchanged. An invalid
// quoted string is an error.
func decodePath(raw string) (string, error) {
	if !strings.HasPrefix(raw, `"`) {
		return raw, nil
	}
	v, err := strconv.Unquote(raw)
	if err != nil {
		return "", errors.New("unquote failed")
	}
	return v, nil
}

// quotedEnd returns the index just past the closing quote of the quoted token
// at the start of s, or -1.
func quotedEnd(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return -1
}

// splitHeaderPaths extracts the old and new path from the operand of a
// "diff --git" line. It is a fallback only: with unquoted paths the line is
// ambiguous, so ok is false unless the two halves can be told apart reliably.
// Git prints identical paths on both sides unless the file is renamed or
// copied, and then the rename/copy lines are used instead.
func splitHeaderPaths(rest string) (oldP, newP string, ok bool, err error) {
	var a, b string
	switch {
	case strings.HasPrefix(rest, `"`):
		end := quotedEnd(rest)
		if end < 0 {
			return "", "", false, errors.New("unterminated quote")
		}
		if a, err = decodePath(rest[:end]); err != nil {
			return "", "", false, err
		}
		if end >= len(rest) || rest[end] != ' ' {
			return "", "", false, nil
		}
		if b, err = decodePath(rest[end+1:]); err != nil {
			return "", "", false, err
		}
	case strings.Contains(rest, ` "`):
		i := strings.Index(rest, ` "`)
		a = rest[:i]
		if b, err = decodePath(rest[i+1:]); err != nil {
			return "", "", false, err
		}
	default:
		// "a/X b/X": 5 fixed bytes plus the path twice.
		n := len(rest)
		if n < 5 || (n-5)%2 != 0 {
			return "", "", false, nil
		}
		l := (n - 5) / 2
		if rest[2+l] != ' ' || rest[2:2+l] != rest[5+l:] {
			return "", "", false, nil
		}
		a, b = rest[:2+l], rest[3+l:]
	}
	if !strings.HasPrefix(a, "a/") || !strings.HasPrefix(b, "b/") {
		return "", "", false, nil
	}
	return a[2:], b[2:], true, nil
}
