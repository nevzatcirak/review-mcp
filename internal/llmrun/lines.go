package llmrun

import (
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/patch"
)

// FileLines splits complete file content into its lines, as the numbered
// diff counts them (architect decision D5): at "\n" only, a final newline
// starting no further line, and a "\r" before the "\n" dropped. It is the
// head-content side of the DQ-12 snippet resolver, which pr_review's
// snippets and pr_improve's verification (Y-10) share; it moved here from
// internal/review.
func FileLines(content string) []string {
	all := strings.Split(content, "\n")
	if len(all) > 0 && all[len(all)-1] == "" {
		all = all[:len(all)-1]
	}
	for i, l := range all {
		all[i] = strings.TrimSuffix(l, "\r")
	}
	return all
}

// HeadLines takes the lines start to end (1-based, inclusive) from complete
// file content (FileLines). ok is false when the range is not inside the
// file: start below 1, end before start, or end past the last line.
func HeadLines(content string, start, end int) (lines []string, ok bool) {
	all := FileLines(content)
	if start < 1 || end < start || end > len(all) {
		return nil, false
	}
	return all[start-1 : end], true
}

// patchLinesCapHint bounds the capacity PatchLines reserves for a range, so
// a huge range does not allocate before the first missing line fails it. It
// is review.MaxSnippetLines+1: pr_review cuts a snippet at 30 lines.
const patchLinesCapHint = 31

// PatchLines resolves the range start to end (1-based, inclusive) from the
// new side of the patch's hunks (context and added lines), without line
// endings. Every line of the range must be present: a removed-only
// position, a gap between hunks or an unparsable patch gives ok false;
// malformed hunks are skipped. It is the patch side of the DQ-12 snippet
// resolver, which pr_review's snippets and pr_improve's verification share;
// it moved here from internal/review.
func PatchLines(p string, start, end int) ([]string, bool) {
	hunks, err := patch.ParseHunks(p)
	if err != nil {
		return nil, false
	}
	byLine := map[int]string{}
	for _, h := range hunks {
		if h.Malformed() {
			continue
		}
		n := h.NewStart
		for _, l := range h.Lines {
			switch l.Op {
			case ' ', '+':
				byLine[n] = strings.TrimSuffix(strings.TrimSuffix(l.Text, "\n"), "\r")
				n++
			}
		}
	}
	out := make([]string, 0, min(end-start+1, patchLinesCapHint))
	for n := start; n <= end; n++ {
		l, ok := byLine[n]
		if !ok {
			return nil, false
		}
		out = append(out, l)
	}
	return out, true
}
