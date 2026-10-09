// Package render turns a describe.Result into markdown for the MCP client
// (portable markdown, no raw HTML). The published renderings (a comment, or
// the marked region of the PR description) are WP-2d.
//
// It lives apart from internal/describe so that the pipeline package has no
// rendering code, as internal/ask/render does. The fixed headings stay
// English. The title and the labels are model-authored one-liners and are
// escaped; the summary and the per-file summaries are model-authored
// markdown bullet lists (the prompt asks for '- ' bullets and backticks) and
// are shown as they are, as pr_ask shows its answer.
package render

import (
	"slices"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/describe"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	llmrender "github.com/nevzatcirak/review-mcp/internal/llmrun/render"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Fixed English headings and texts.
const (
	TextTitle        = "Title"
	TextType         = "Type"
	TextSummary      = "Summary"
	TextWalkthrough  = "Walkthrough"
	TextNotDescribed = "Not described"
	textNotGenerated = "(not generated)"
	textNoFiles      = "No file was described."

	// TextPartial marks a walkthrough entry of a clipped file: the model
	// saw only part of it, and coverage counts it as not described.
	TextPartial = "(partial: only part of this file was shown)"
)

// Client renders res for the MCP client:
//
//   - the X-18 banner of a partial result, first;
//   - "## Title", "## Type" and "## Summary", each "(not generated)" when
//     its value is null;
//   - "## Walkthrough": per described file its path, label and title, then
//     its summary indented below; the line of a clipped file ends with
//     TextPartial (see MarkPartial);
//   - "## Not described" (Y-7), when files were not described: every such
//     file with the reason of its coverage category, at most
//     llmrender.MaxListedFiles listed;
//   - the coverage section (always) and the notes section (when there are
//     notes).
func Client(res *describe.Result) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	if banner := llmrender.PartialDescribeBanner(&res.Coverage); banner != "" {
		b.WriteString(banner + "\n\n")
	}
	b.WriteString("## " + TextTitle + "\n\n")
	if res.Title != nil {
		b.WriteString(mdutil.Inline(*res.Title) + "\n")
	} else {
		b.WriteString(textNotGenerated + "\n")
	}
	b.WriteString("\n## " + TextType + "\n\n")
	if res.Type != nil {
		types := make([]string, len(res.Type))
		for i, t := range res.Type {
			types[i] = mdutil.Inline(t)
		}
		if len(types) == 0 {
			b.WriteString(textNotGenerated + "\n")
		} else {
			b.WriteString(strings.Join(types, ", ") + "\n")
		}
	} else {
		b.WriteString(textNotGenerated + "\n")
	}
	b.WriteString("\n## " + TextSummary + "\n\n")
	if res.Description != nil && strings.TrimSpace(*res.Description) != "" {
		b.WriteString(strings.TrimSpace(*res.Description) + "\n")
	} else {
		b.WriteString(textNotGenerated + "\n")
	}

	b.WriteString("\n## " + TextWalkthrough + "\n\n")
	if len(res.Files) == 0 {
		b.WriteString(textNoFiles + "\n")
	}
	for _, f := range res.Files {
		line := "- " + mdutil.Literal(f.Path)
		if l := strings.TrimSpace(f.Label); l != "" {
			line += " (" + mdutil.Inline(l) + ")"
		}
		if t := strings.TrimSpace(f.Title); t != "" {
			line += ": " + mdutil.Inline(t)
		}
		b.WriteString(MarkPartial(line, f.Path, &res.Coverage) + "\n")
		if s := strings.TrimSpace(f.Summary); s != "" {
			b.WriteString(indent(s, "  ") + "\n")
		}
	}

	writeNotDescribed(&b, &res.Coverage)
	llmrender.DescribeCoverage(&b, "## "+llmrender.TextCoverage, &res.Coverage)
	llmrender.Notes(&b, "## "+llmrender.TextNotes, res.Notes)
	return b.String()
}

// MarkPartial returns the walkthrough line of the entry for path with
// TextPartial appended when path is a clipped file of c, and line unchanged
// otherwise. Every rendering of the walkthrough uses it, so an entry never
// reads as complete next to a "not described" count that includes it.
func MarkPartial(line, path string, c *describe.Coverage) string {
	if slices.Contains(c.Clipped, path) {
		return line + " " + TextPartial
	}
	return line
}

// notDescribed is one file that was not described and the reason.
type notDescribed struct{ path, reason string }

// notDescribedOf lists the files of c that were not described (Y-7), in
// the coverage section's order, each with the reason of its coverage
// category. Its length is c's NotReviewedFiles: filtered files and binary or
// empty-diff skips are left out on purpose, as llmrun.Tally leaves them out.
func notDescribedOf(c *describe.Coverage) []notDescribed {
	var out []notDescribed
	add := func(paths []string, reason string) {
		for _, p := range paths {
			out = append(out, notDescribed{p, reason})
		}
	}
	add(c.Clipped, "included only in part (clipped to fit the context window)")
	add(c.Omitted.Added, "left out to fit the context window")
	add(c.Omitted.Modified, "left out to fit the context window")
	add(c.Omitted.Deleted, "left out to fit the context window")
	for _, s := range c.Skipped {
		if r := skipReason(s.Reason); r != "" {
			out = append(out, notDescribed{s.Path, r})
		}
	}
	return out
}

// skipReason is the reason text of a skip that loses a file, or "" for a
// skip outside the count (binary, empty diff), as llmrun.Tally decides.
func skipReason(reason string) string {
	switch reason {
	case provider.SkipBinary, diffpipe.SkipEmptyDiff:
		return ""
	case describe.SkipNotReturned:
		return "shown to the model, but it returned no walkthrough entry"
	case llmrun.SkipModelCallFailed:
		return "its part's model call failed"
	case diffpipe.SkipTooLarge:
		return "too large for a part of its own"
	}
	return "skipped: " + mdutil.Literal(reason)
}

// writeNotDescribed writes the "Not described" section when files were not
// described.
func writeNotDescribed(b *strings.Builder, c *describe.Coverage) {
	list := notDescribedOf(c)
	if len(list) == 0 {
		return
	}
	b.WriteString("\n## " + TextNotDescribed + "\n\n")
	shown := min(len(list), llmrender.MaxListedFiles)
	for _, nd := range list[:shown] {
		b.WriteString("- " + mdutil.Literal(nd.path) + ": " + nd.reason + "\n")
	}
	if rest := len(list) - shown; rest > 0 {
		b.WriteString("\nand " + strconv.Itoa(rest) + " more (not listed; at most " +
			strconv.Itoa(llmrender.MaxListedFiles) + " files are listed)\n")
	}
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
