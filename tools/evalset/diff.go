package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nevzatcirak/review-mcp/tools/evalset/cases"
)

// maxDiffBytes bounds the diff the judge sees.
const maxDiffBytes = 120_000

// caseDiff renders the pull request of a case as a unified diff (three
// lines of context), for the judge. It reads the corpus files only.
func caseDiff(c *cases.Case) string {
	paths := map[string]bool{}
	for p := range c.Head {
		paths[p] = true
	}
	for _, p := range c.Deleted {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	var b strings.Builder
	for _, p := range sorted {
		before, inBase := c.Base[p]
		after, inHead := c.AtHead(p)
		from, to := "a/"+p, "b/"+p
		if !inBase {
			from = "/dev/null"
		}
		if !inHead {
			to = "/dev/null"
		}
		b.WriteString(unifiedDiff(from, to, splitLines(string(before)), splitLines(string(after))))
		if b.Len() > maxDiffBytes {
			return b.String()[:maxDiffBytes] + "\n[diff cut]\n"
		}
	}
	return b.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	return strings.Split(s, "\n")
}

// unifiedDiff diffs two line lists by their longest common subsequence.
// The corpus files are small (at most a few hundred lines), so the
// quadratic table is cheap.
func unifiedDiff(from, to string, a, b []string) string {
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		kind byte // ' ', '-', '+'
		text string
		ai   int // line numbers (1-based) in a and b before this op
		bi   int
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, op{' ', a[i], i, j})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, op{'-', a[i], i, j})
			i++
		default:
			ops = append(ops, op{'+', b[j], i, j})
			j++
		}
	}
	const ctx = 3
	var out strings.Builder
	header := false
	for k := 0; k < len(ops); {
		if ops[k].kind == ' ' {
			k++
			continue
		}
		start := max(k-ctx, 0)
		// Extend the hunk while changes are within 2*ctx lines of each other.
		end, last := k, k
		for ; end < len(ops); end++ {
			if ops[end].kind != ' ' {
				last = end
			} else if end-last > 2*ctx {
				break
			}
		}
		end = min(last+ctx+1, len(ops))
		if !header {
			fmt.Fprintf(&out, "--- %s\n+++ %s\n", from, to)
			header = true
		}
		aStart, bStart, aLen, bLen := ops[start].ai+1, ops[start].bi+1, 0, 0
		for _, o := range ops[start:end] {
			if o.kind != '+' {
				aLen++
			}
			if o.kind != '-' {
				bLen++
			}
		}
		if aLen == 0 {
			aStart--
		}
		if bLen == 0 {
			bStart--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart, aLen, bStart, bLen)
		for _, o := range ops[start:end] {
			out.WriteByte(o.kind)
			out.WriteString(o.text)
			out.WriteByte('\n')
		}
		k = end
	}
	return out.String()
}
