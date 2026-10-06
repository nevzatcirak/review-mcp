// Package patch holds the parsed hunk model of one file's patch (DQ-10), the
// static context extension (DQ-1), deletion handling and the renderers whose
// bytes the prompts depend on (spec §3).
//
// Behaviour mirrors PR-Agent at commit
// 8e5a9295973b24af4b70cafd0b660a230811ef9e byte for byte; the goldens under
// testdata/upstream are produced by running the upstream functions (see the
// README there). The upstream algorithms work on Python strings, so this
// package reproduces Python's splitlines/strip/slice semantics where the
// output depends on them (pystr.go).
//
// Typical use by the assembly step (WP-PR-3d):
//
//	hunks, err := patch.ParseHunks(fp.Patch)
//	// fast path
//	ext := patch.ExtendFile(fp, hunks, cfg.Diff)
//	text := patch.RenderPlain(patch.NewFile(fp, ext))          // pr_ask
//	text := patch.RenderDecoupled(patch.NewFile(fp, ext), true) // pr_review
//	// compressed path
//	kept, deleted := patch.HandleDeletions(fp.Type, fp.HeadContent, hunks)
//	text := patch.RenderCompressed(patch.NewFile(fp, kept), numbered)
package patch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeaderRE = regexp.MustCompile(hunkHeaderPattern)

// Line is one line of a hunk. Op is ' ' (context), '+', '-' or '\\' (a
// "\ No newline at end of file" marker). Text is the rest of the line and
// keeps its original line ending, if any.
type Line struct {
	Op   byte
	Text string
}

// Hunk is one hunk of a file's patch. Omitted counts in the header mean 1, as
// in git. Section is the text after the closing "@@" (without the single
// separating space).
//
// A line that starts with "@@" but is not a valid hunk header (for example a
// combined-diff header) is kept as a malformed pseudo-hunk: its counts are
// zero, Malformed reports true, and the lines up to the next header belong to
// it. Every view treats it the way upstream treats such a line.
type Hunk struct {
	OldStart, OldLen, NewStart, NewLen int
	Section                            string
	Lines                              []Line

	// header is the header line exactly as it is serialized: the parsed line
	// with its ending for formRaw, the rewritten header without an ending
	// for an extended hunk. Empty means "derive it from the fields".
	header    string
	malformed bool
	form      form
	// tail and nDelta are used by extended hunks only. tail holds the
	// Python lines that followed the first one in the original header line
	// (a header whose section text contains \f, \v, ...); upstream emits
	// them after the added pre-context, which is Lines[:nDelta].
	tail   []string
	nDelta int
}

// form says how a hunk is serialized into the patch text upstream's renderers
// would see (see patchText).
type form uint8

const (
	// formRaw: the original patch bytes (ParseHunks output).
	formRaw form = iota
	// formExtended: upstream's process_patch_lines output — lines joined by
	// "\n", line endings dropped, an empty line before every valid header.
	formExtended
	// formCompact: upstream's omit_deletion_hunks output — lines joined by
	// "\n", line endings dropped, malformed header lines dropped.
	formCompact
)

// Malformed reports whether the hunk is a pseudo-hunk whose "@@" line is not
// a valid unified hunk header.
func (h Hunk) Malformed() bool { return h.malformed }

// ParseHunks parses a hunk-only patch as produced by the providers: it starts
// at the first "@@" line and has no file header lines. An empty patch yields
// no hunks. Every non-header line must start with ' ', '+', '-' or '\\';
// anything else is an error (the providers never produce such lines).
func ParseHunks(patch string) ([]Hunk, error) {
	if patch == "" {
		return nil, nil
	}
	if !strings.HasPrefix(patch, "@@") {
		return nil, fmt.Errorf("patch: patch does not start with a hunk header")
	}
	var hunks []Hunk
	for n, raw := range splitAfterNewline(patch) {
		if strings.HasPrefix(raw, "@@") {
			hunks = append(hunks, parseHeader(raw))
			continue
		}
		switch raw[0] {
		case ' ', '+', '-', '\\':
			h := &hunks[len(hunks)-1]
			h.Lines = append(h.Lines, Line{Op: raw[0], Text: raw[1:]})
		default:
			return nil, fmt.Errorf("patch: line %d has no diff line prefix", n+1)
		}
	}
	return hunks, nil
}

// splitAfterNewline splits s after every "\n"; a final line without "\n" is
// kept.
func splitAfterNewline(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// parseHeader parses one "@@" line. Validity is decided on the line as
// upstream sees it: the first Python line of it, matched by RE_HUNK_HEADER.
func parseHeader(raw string) Hunk {
	h := Hunk{header: raw, form: formRaw}
	first := ""
	if pieces := pySplitLines(raw); len(pieces) > 0 {
		first = pieces[0]
	}
	m := hunkHeaderRE.FindStringSubmatch(first)
	if m == nil {
		h.malformed = true
		return h
	}
	num := func(s string, def int) (int, bool) {
		if s == "" {
			return def, true
		}
		v, err := strconv.Atoi(s)
		return v, err == nil
	}
	var ok [4]bool
	h.OldStart, ok[0] = num(m[1], 0)
	h.OldLen, ok[1] = num(m[2], 1)
	h.NewStart, ok[2] = num(m[3], 0)
	h.NewLen, ok[3] = num(m[4], 1)
	if !ok[0] || !ok[1] || !ok[2] || !ok[3] {
		// Python's int() has no overflow; a number Go cannot hold is
		// treated as a malformed header.
		return Hunk{header: raw, form: formRaw, malformed: true}
	}
	h.Section = m[5]
	return h
}

// rawHeader returns the header line of a raw hunk, with its line ending.
func (h Hunk) rawHeader() string {
	if h.header != "" {
		return h.header
	}
	s := fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLen, h.NewStart, h.NewLen)
	if h.Section != "" {
		s += " " + h.Section
	}
	return s + "\n"
}

// patchText serializes hunks into the patch string the upstream renderer
// operating at the same pipeline stage would receive: the original bytes for
// raw hunks, upstream's "\n"-joined form for extended or compacted hunks.
// Every renderer starts from this text.
func patchText(hunks []Hunk) string {
	allRaw := true
	for _, h := range hunks {
		if h.form != formRaw {
			allRaw = false
			break
		}
	}
	var b strings.Builder
	if allRaw {
		for _, h := range hunks {
			b.WriteString(h.rawHeader())
			for _, l := range h.Lines {
				b.WriteByte(l.Op)
				b.WriteString(l.Text)
			}
		}
		return b.String()
	}
	var lines []string
	pieces := func(ls []Line) {
		for _, l := range ls {
			lines = append(lines, linePieces(l)...)
		}
	}
	for _, h := range hunks {
		body := h.Lines
		switch {
		case h.form == formExtended && !h.malformed:
			n := min(max(h.nDelta, 0), len(h.Lines))
			lines = append(lines, "", h.header)
			pieces(h.Lines[:n])
			lines = append(lines, h.tail...)
			body = h.Lines[n:]
		case h.form == formCompact && h.malformed:
			// omit_deletion_hunks drops a malformed "@@" line (its first
			// Python line only).
			lines = append(lines, headerPieces(h)[1:]...)
		default:
			lines = append(lines, headerPieces(h)...)
		}
		pieces(body)
	}
	return strings.Join(lines, "\n")
}

// linePieces returns the Python lines of one patch line.
func linePieces(l Line) []string {
	return pySplitLines(string(l.Op) + l.Text)
}

// headerPieces returns the Python lines of a hunk's header line; the first
// one is the header upstream matches, any others are ordinary lines.
func headerPieces(h Hunk) []string {
	return pySplitLines(h.rawHeader())
}

// pyLineSafe reports whether the hunks can be processed structurally with
// the same result as upstream's line-based processing: no Python line other
// than the first line of a header line starts with "@@". Such a line (after
// a \v, \f, a lone \r, ... inside a patch line) would be taken for a hunk
// header by upstream.
func pyLineSafe(hunks []Hunk) bool {
	for _, h := range hunks {
		for _, p := range headerPieces(h)[1:] {
			if strings.HasPrefix(p, "@@") {
				return false
			}
		}
		for _, l := range h.Lines {
			for _, p := range linePieces(l)[1:] {
				if strings.HasPrefix(p, "@@") {
					return false
				}
			}
		}
	}
	return true
}

// cloneHunks returns a deep copy, so results never alias the input.
func cloneHunks(hunks []Hunk) []Hunk {
	if hunks == nil {
		return nil
	}
	out := make([]Hunk, len(hunks))
	for i, h := range hunks {
		out[i] = h
		out[i].Lines = append([]Line(nil), h.Lines...)
		out[i].tail = append([]string(nil), h.tail...)
	}
	return out
}
