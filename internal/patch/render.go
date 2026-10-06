package patch

import (
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// File is what the renderers need to know about one changed file.
type File struct {
	// Path is the path shown in the header: the new path, or the old path
	// of a deleted file (provider.FilePatch.Path).
	Path string
	Type provider.ChangeType
	// HeadStatus selects the unreadable-file notice when it is
	// provider.ContentFetchFailed and Hunks is empty.
	HeadStatus provider.ContentStatus
	Hunks      []Hunk
}

// NewFile builds the renderer input for fp with the given hunks (raw,
// extended or deletion-handled).
func NewFile(fp provider.FilePatch, hunks []Hunk) File {
	return File{Path: fp.Path, Type: fp.Type, HeadStatus: fp.HeadStatus, Hunks: hunks}
}

// unreadable selects the notice (spec §3.4): HeadStatus == fetch_failed and
// no hunks, that is an empty patch. This is upstream's own trigger
// (content_fetch_failed and an empty patch).
//
// Decision, architect decision D3 (PR #4), an intentional deviation from
// upstream and from the earlier status-only rule: a trustworthy patch is
// never replaced by a notice that claims no diff is available. A Gitea patch comes from the PR's .diff and stays
// trustworthy when only the head-content fetch failed, so a fetch-failed
// file that has a patch renders it normally in every view (plain, decoupled,
// compressed). It stays unextended because its head content is nil (§3.2).
// Upstream would extend such a patch against an empty head; this deviation
// is pinned by testdata/deviations/deviation_head_fetch_failed_with_patch.
func (f File) unreadable() bool {
	return f.HeadStatus == provider.ContentFetchFailed && len(f.Hunks) == 0
}

// UnreadableNotice is upstream's _unreadable_file_notice for path: the file
// stays visible to the model, flagged for manual review, instead of being
// dropped silently.
func UnreadableNotice(path string) string {
	return plainFileHeaderPrefix + pyStrip(path) + plainFileHeaderSuffix + unreadableNoticeBody
}

// RenderPlain renders f the way upstream's fast (extended-diff) path does in
// plain mode (pr_generate_extended_diff): the "## File: '<path>'" header,
// then the patch with an extra newline inserted before every "@@ " line.
//
// Upstream quirk reproduced: an extended patch already has an empty line
// before each hunk header, so extended hunks end up two blank lines apart.
//
// A file whose head content could not be read (HeadStatus fetch_failed)
// and that has no hunks renders as UnreadableNotice; with hunks it renders
// normally (D3). Any other file without hunks renders as "" (upstream skips
// it).
func RenderPlain(f File) string {
	if f.unreadable() {
		return UnreadableNotice(f.Path)
	}
	text := patchText(f.Hunks)
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\n@@ ", "\n\n@@ ")
	return plainFileHeaderPrefix + pyStrip(f.Path) + plainFileHeaderSuffix + stripCRLF(text) + "\n"
}

// RenderDecoupled renders f in upstream's
// decouple_and_convert_to_hunks_with_lines_numbers format: per hunk, the
// "@@" header, a "__new hunk__" block (context and '+' lines, each prefixed
// with its new-file line number when numbered is true) and, only when the
// hunk has '-' lines, an unnumbered "__old hunk__" block (context and '-'
// lines). "\ No newline at end of file" lines are dropped; a malformed "@@"
// pseudo-hunk is skipped; a deleted file renders as one "was deleted" line.
//
// numbered = false (reserved for v2, DQ-10) renders the same text without
// the line-number prefixes; upstream has no such renderer at the pinned
// commit, so this variant has no oracle golden.
//
// A fetch-failed file without hunks renders as UnreadableNotice (with hunks
// it renders normally, D3); any other file without hunks renders as "".
func RenderDecoupled(f File, numbered bool) string {
	if f.unreadable() {
		return UnreadableNotice(f.Path)
	}
	if len(f.Hunks) == 0 {
		return ""
	}
	if f.Type == provider.ChangeDeleted {
		return deletedFilePrefix + pyStrip(f.Path) + deletedFileSuffix
	}
	return decouple(patchText(f.Hunks), f.Path, numbered)
}

// RenderCompressed renders f as one entry of upstream's compressed path
// (pr_generate_compressed_diff + generate_full_patch). Pass the hunks
// returned by HandleDeletions. Unlike RenderPlain, the plain form inserts no
// blank lines between hunks, and the numbered form is the decoupled render
// with surrounding newlines trimmed (so it has no trailing newline).
//
// A fetch-failed file without hunks renders the notice as upstream does on
// this path (the numbered form loses the notice's final newline); with hunks
// it renders normally (D3). An empty result means upstream would skip the
// file.
func RenderCompressed(f File, numbered bool) string {
	if f.unreadable() {
		note := UnreadableNotice(f.Path)
		if numbered {
			return "\n\n" + stripCRLF(note)
		}
		parts := strings.SplitN(note, "\n\n", 3)
		return plainFileHeaderPrefix + pyStrip(f.Path) + plainFileHeaderSuffix + stripCRLF(parts[len(parts)-1]) + "\n"
	}
	if len(f.Hunks) == 0 {
		return ""
	}
	if numbered {
		return "\n\n" + stripCRLF(RenderDecoupled(f, true))
	}
	text := patchText(f.Hunks)
	if text == "" {
		return ""
	}
	return plainFileHeaderPrefix + pyStrip(f.Path) + plainFileHeaderSuffix + stripCRLF(text) + "\n"
}

// decouple mirrors the body of decouple_and_convert_to_hunks_with_lines_
// numbers on the serialized patch text.
//
// Parity decision (lead; architect may override on PR #4): upstream splits the patch with str.splitlines, so a line
// that contains \f, \v, U+2028, ... becomes several numbered lines and every
// later number drifts from the file's real line numbers (see the
// python_line_breaks golden). This looks like an upstream bug; fix it here?
// — chose to reproduce it for now, because byte parity is the acceptance
// criterion and a fix belongs to a deliberate, documented deviation.
func decouple(text, path string, numbered bool) string {
	d := decoupler{numbered: numbered, cur: decoupledFileHeaderPrefix + pyStrip(path) + decoupledFileHeaderSuffix}
	lines := pySplitLines(text)
	matched, skip := false, false
	header, prevHeader := "", ""
	for i, line := range lines {
		if line == noNewlineMarker {
			continue
		}
		switch {
		case strings.HasPrefix(line, "@@"):
			start2, ok := headerNewStart(line)
			if !ok {
				skip = true
				continue
			}
			skip = false
			header = line
			matched = true
			if len(d.newLines) > 0 || len(d.oldLines) > 0 {
				if prevHeader != "" {
					d.cur += "\n" + prevHeader + "\n"
				}
				d.blocks()
				d.done.WriteString(d.cur)
				d.cur = ""
				d.newLines, d.oldLines = nil, nil
			}
			prevHeader = header
			d.start2 = start2
		case skip, !matched:
			// Lines of a skipped pseudo-hunk, or metadata before the first
			// valid hunk.
		case strings.HasPrefix(line, "+"):
			d.newLines = append(d.newLines, line)
		case strings.HasPrefix(line, "-"):
			d.oldLines = append(d.oldLines, line)
		default:
			// An empty line right before a header or at the very end is
			// dropped (never the first line).
			if line == "" && i > 0 {
				if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "@@") {
					continue
				}
				if i+1 == len(lines) {
					continue
				}
			}
			d.newLines = append(d.newLines, line)
			d.oldLines = append(d.oldLines, line)
		}
	}
	if matched && (len(d.newLines) > 0 || len(d.oldLines) > 0) {
		d.cur += "\n" + header + "\n"
		d.blocks()
	}
	d.done.WriteString(d.cur)
	return rstripCRLF(d.done.String())
}

type decoupler struct {
	numbered           bool
	done               strings.Builder
	cur                string
	start2             int
	newLines, oldLines []string
}

// blocks appends the __new hunk__ / __old hunk__ blocks of the current hunk.
// A hunk with neither '+' nor '-' lines gets only its header.
func (d *decoupler) blocks() {
	plus := anyPrefix(d.newLines, "+")
	minus := anyPrefix(d.oldLines, "-")
	if plus || minus {
		d.cur = rstripCRLF(d.cur) + "\n" + newHunkMarker + "\n"
		var b strings.Builder
		for i, l := range d.newLines {
			if d.numbered {
				b.WriteString(strconv.Itoa(d.start2 + i))
				b.WriteByte(' ')
			}
			b.WriteString(l)
			b.WriteByte('\n')
		}
		d.cur += b.String()
	}
	if minus {
		d.cur = rstripCRLF(d.cur) + "\n" + oldHunkMarker + "\n"
		var b strings.Builder
		for _, l := range d.oldLines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
		d.cur += b.String()
	}
}

func anyPrefix(lines []string, p string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// headerNewStart matches line against RE_HUNK_HEADER and returns the new
// start.
func headerNewStart(line string) (int, bool) {
	m := hunkHeaderRE.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	for _, g := range m[1:5] {
		if g == "" {
			continue
		}
		if _, err := strconv.Atoi(g); err != nil {
			return 0, false
		}
	}
	v, _ := strconv.Atoi(m[3])
	return v, true
}
