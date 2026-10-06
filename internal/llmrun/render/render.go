// Package render holds the YAML-free markdown sections that both LLM tools
// (pr_review and pr_ask) show: the coverage section (X-3) and the notes
// section. It imports neither the review nor the ask package, so the ask
// package family stays free of YAML and repair code (spec P5 §3).
package render

import (
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
)

// MaxListedFiles is the most files the coverage section lists (X-3); the
// rest are summarized as "and N more".
const MaxListedFiles = 50

// Fixed English section titles and reasons.
const (
	TextCoverage      = "Coverage"
	TextNotes         = "Notes"
	reasonBudgetAdded = "Left out to fit the context window (added files)"
	reasonBudgetMod   = "Left out to fit the context window (modified files)"
	reasonBudgetDel   = "Left out to fit the context window (deleted files)"
)

// coverageGroup is one list of files in the coverage section.
type coverageGroup struct {
	title string // already markdown-safe
	files []string
}

// Coverage writes the coverage section (X-3), which is always present:
// the included and omitted counts, then the files that are clipped or
// omitted, grouped by reason. At most MaxListedFiles files are listed
// across all groups; the rest are counted ("and N more"). Everything is
// plain markdown (no HTML).
func Coverage(b *strings.Builder, heading string, c *llmrun.Coverage) {
	included := len(c.Included) + len(c.Clipped)
	budget := len(c.Omitted.Added) + len(c.Omitted.Modified) + len(c.Omitted.Deleted)
	omitted := budget + len(c.Skipped) + len(c.Filtered)

	b.WriteString("\n" + heading + "\n\n")
	b.WriteString("- Included: " + strconv.Itoa(included) + " " + plural(included))
	if n := len(c.Clipped); n > 0 {
		b.WriteString(" (" + strconv.Itoa(n) + " in part only, clipped to fit the context window)")
	}
	b.WriteString("\n- Omitted: " + strconv.Itoa(omitted) + " " + plural(omitted) + "\n")

	var groups []coverageGroup
	if len(c.Clipped) > 0 {
		groups = append(groups, coverageGroup{"Included in part only (clipped to fit the context window)", c.Clipped})
	}
	groups = appendGroup(groups, reasonBudgetAdded, c.Omitted.Added)
	groups = appendGroup(groups, reasonBudgetMod, c.Omitted.Modified)
	groups = appendGroup(groups, reasonBudgetDel, c.Omitted.Deleted)
	groups = append(groups, byReason("Skipped", c.Skipped)...)
	groups = append(groups, byReason("Filtered", c.Filtered)...)

	left, unlisted := MaxListedFiles, 0
	for _, g := range groups {
		b.WriteString("\n" + g.title + " (" + strconv.Itoa(len(g.files)) + "):\n")
		shown := min(left, len(g.files))
		if shown > 0 {
			b.WriteString("\n")
		}
		for _, f := range g.files[:shown] {
			b.WriteString("- " + mdutil.Literal(f) + "\n")
		}
		left -= shown
		unlisted += len(g.files) - shown
	}
	if unlisted > 0 {
		b.WriteString("\nand " + strconv.Itoa(unlisted) + " more (not listed; at most " + strconv.Itoa(MaxListedFiles) + " files are listed)\n")
	}
}

func plural(n int) string {
	if n == 1 {
		return "file"
	}
	return "files"
}

func appendGroup(gs []coverageGroup, title string, files []string) []coverageGroup {
	if len(files) == 0 {
		return gs
	}
	return append(gs, coverageGroup{title, files})
}

// byReason groups skipped files by their reason, in order of first
// appearance. The machine reason token is shown in a code span.
func byReason(kind string, files []llmrun.SkippedFile) []coverageGroup {
	var order []string
	byR := map[string][]string{}
	for _, f := range files {
		if _, ok := byR[f.Reason]; !ok {
			order = append(order, f.Reason)
		}
		byR[f.Reason] = append(byR[f.Reason], f.Path)
	}
	gs := make([]coverageGroup, 0, len(order))
	for _, r := range order {
		gs = append(gs, coverageGroup{kind + ": " + mdutil.Literal(r), byR[r]})
	}
	return gs
}

// Notes writes the notes section when there are notes.
func Notes(b *strings.Builder, heading string, notes []string) {
	if len(notes) == 0 {
		return
	}
	b.WriteString("\n" + heading + "\n\n")
	for _, n := range notes {
		b.WriteString("- " + mdutil.Inline(n) + "\n")
	}
}
