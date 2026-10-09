// Package render turns an improve.Result into markdown for the MCP client
// (portable markdown, no raw HTML). The published renderings (the overview
// comment and the inline suggestions) are WP-2h.
//
// It lives apart from internal/improve so that the pipeline package has no
// rendering code, as internal/describe/render does. The fixed headings stay
// English. The summary, the label and the file are model-authored
// one-liners and are escaped; the suggestion text and the score reason are
// model-authored markdown (the prompts ask for backticks around code) and
// are shown as they are, as pr_describe shows its summaries, the reason
// folded to one line; the code is shown inside fences that it cannot close,
// and the language only as a fence info string of safe characters.
package render

import (
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/improve"
	llmrender "github.com/nevzatcirak/review-mcp/internal/llmrun/render"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
)

// Fixed English headings and texts.
const (
	TextSuggestions = "Suggestions"
	TextExisting    = "Existing code:"
	TextImproved    = "Improved code:"
	// TextUnscored marks a suggestion without a self-review score (v2 spec
	// §1.4).
	TextUnscored = "unscored"
	// TextLinesVerified follows the line range of a verified suggestion:
	// the quoted code is at those lines of the head file (v2 spec §2).
	TextLinesVerified = "checked against the head file"
	// TextNotAnchored marks a suggestion whose quoted code was not found at
	// the given lines, nor uniquely elsewhere in the head file (Y-10,
	// verbatim).
	TextNotAnchored = "not anchored: the quoted code was not found at the given lines"
	// TextNotAnchoredNoHead marks a suggestion that could not be checked:
	// the head file's content was not fetched.
	TextNotAnchoredNoHead = "not anchored: the head file was not available to check the quoted code"
	textNoSuggestions     = "No suggestions."
)

// Client renders res for the MCP client:
//
//   - the X-18 banner of a partial result, first;
//   - "## Suggestions": per suggestion, in the result's order, a numbered
//     heading with its summary, a list with the file (with the line range
//     and TextLinesVerified for a verified suggestion; with the range as
//     given, if any, and TextNotAnchored or TextNotAnchoredNoHead for an
//     unverified one), the label, the score ("N of 10", or TextUnscored)
//     and the reason, then the suggestion text and the existing and
//     improved code in fences;
//   - the coverage section (always) and the notes section (when there are
//     notes).
func Client(res *improve.Result) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	if banner := llmrender.PartialBanner(&res.Coverage, false); banner != "" {
		b.WriteString(banner + "\n\n")
	}
	b.WriteString("## " + TextSuggestions + "\n")
	if len(res.Suggestions) == 0 {
		b.WriteString("\n" + textNoSuggestions + "\n")
	}
	for i := range res.Suggestions {
		writeSuggestion(&b, i+1, &res.Suggestions[i])
	}
	llmrender.Coverage(&b, "## "+llmrender.TextCoverage, &res.Coverage)
	llmrender.Notes(&b, "## "+llmrender.TextNotes, res.Notes)
	return b.String()
}

func writeSuggestion(b *strings.Builder, n int, s *improve.Suggestion) {
	b.WriteString("\n### " + strconv.Itoa(n) + ". " + mdutil.Inline(s.Summary) + "\n\n")
	var where []string
	if s.StartLine != nil && s.EndLine != nil {
		lines := "line " + strconv.Itoa(*s.StartLine)
		if *s.EndLine != *s.StartLine {
			lines = "lines " + strconv.Itoa(*s.StartLine) + "-" + strconv.Itoa(*s.EndLine)
		}
		where = append(where, lines)
	}
	switch {
	case s.Verified:
		where = append(where, TextLinesVerified)
	case s.UnverifiedReason == improve.UnverifiedHeadUnavailable:
		where = append(where, TextNotAnchoredNoHead)
	default:
		where = append(where, TextNotAnchored)
	}
	b.WriteString("- File: " + mdutil.Literal(s.File) + " (" + strings.Join(where, "; ") + ")\n")
	if l := strings.TrimSpace(s.Label); l != "" {
		b.WriteString("- Label: " + mdutil.Inline(l) + "\n")
	}
	if s.Score != nil {
		b.WriteString("- Score: " + strconv.Itoa(*s.Score) + " of " + strconv.Itoa(improve.MaxScore) + "\n")
		if w := strings.Join(strings.Fields(s.Why), " "); w != "" {
			b.WriteString("- Why: " + w + "\n")
		}
	} else {
		b.WriteString("- Score: " + TextUnscored + "\n")
	}
	if c := strings.TrimSpace(s.Content); c != "" {
		b.WriteString("\n" + c + "\n")
	}
	info := fenceInfo(s.Language)
	b.WriteString("\n" + TextExisting + "\n\n")
	mdutil.WriteFenced(b, s.ExistingCode, info, "")
	b.WriteString("\n" + TextImproved + "\n\n")
	mdutil.WriteFenced(b, s.ImprovedCode, info, "")
}

// fenceInfo is the fence info string for a model-named language: the name
// lower-cased when it is one word of letters, digits and "+#._-", else
// nothing, so the info string cannot carry markup.
func fenceInfo(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == "" || len(l) > 30 {
		return ""
	}
	for _, r := range l {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("+#._-", r)
		if !ok {
			return ""
		}
	}
	return l
}
