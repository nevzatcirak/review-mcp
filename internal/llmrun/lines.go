package llmrun

import "strings"

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
