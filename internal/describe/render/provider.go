package render

import (
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/describe"
	llmrender "github.com/nevzatcirak/review-mcp/internal/llmrun/render"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Emojis of the GFM provider profile (the same as pr_review's and pr_ask's).
const (
	emojiTitle    = "📝"
	emojiCoverage = "📂"
	emojiNotes    = "📝"
	emojiWarning  = "⚠️"
)

// Provider renders res as published markdown for a provider with the given
// capabilities (DQ-16); it has the describe.ProviderRenderer signature. The
// pipeline wraps the result in the description comment (and adds its marker)
// or in the managed region of the PR description (ApplyRegion).
//
// The layout is the client view's, with level-three headings under one
// "PR Description" heading, the banner right under it (as the overview does,
// so every edit in place carries it), and the walkthrough entry of a
// clipped file marked partial (MarkPartial). With caps.GFM (Gitea) the
// headings carry the emojis of pr_review's overview and the banner is a
// warning blockquote; without it (Bitbucket Server) there is no emoji and no
// HTML anywhere. The coverage and notes sections are plain markdown on both.
//
// Escaping follows pr_review's provider rendering of model markdown. The
// title, the types and the labels are model-authored one-liners and are
// escaped as the client view escapes them (mdutil.Inline). The summary and
// the per-file summaries are model-authored bullet lists: their bullet
// markers are kept, and the text after them is escaped as pr_review escapes
// a finding's text, per caps.GFM: markdown control characters are
// backslash-escaped and, with GFM, HTML metacharacters become entities;
// without GFM, '<', '>' and '&' are backslash-escaped, so no raw HTML, tag,
// autolink or entity can come out of a summary. The same escaping keeps the
// text from forming a marker line of ours. Published bodies are then
// slash-sanitised for quick actions by the caller (provider.SanitizeBody).
//
// DESIGN-QUESTION: are the bullets of the summary kept as list syntax, or is
// the whole summary escaped like a finding's text? — chose to keep the
// bullet markers and escape the rest: the prompt asks for '- ' bullets, and
// escaping the marker would publish a column of "- " text where a list
// belongs; the cost is that the backticks the prompt asks for show as
// literal backticks, as they do in pr_review's findings.
func Provider(res *describe.Result, caps provider.Capabilities) string {
	if res == nil {
		return ""
	}
	esc := mdutil.Escape
	head, covHead, notesHead := "## PR Description", "### "+llmrender.TextCoverage, "### "+llmrender.TextNotes
	if caps.GFM {
		esc = mdutil.EscapeGFM
		head += " " + emojiTitle
		covHead = "### " + emojiCoverage + " " + llmrender.TextCoverage
		notesHead = "### " + emojiNotes + " " + llmrender.TextNotes
	}
	var b strings.Builder
	b.WriteString(head + "\n\n")
	if banner := llmrender.PartialDescribeBanner(&res.Coverage); banner != "" {
		if caps.GFM {
			banner = "> " + emojiWarning + " " + banner
		}
		b.WriteString(banner + "\n\n")
	}

	b.WriteString("### " + TextTitle + "\n\n")
	if res.Title != nil {
		b.WriteString(mdutil.Inline(*res.Title) + "\n")
	} else {
		b.WriteString(textNotGenerated + "\n")
	}
	b.WriteString("\n### " + TextType + "\n\n")
	if len(res.Type) > 0 {
		types := make([]string, len(res.Type))
		for i, t := range res.Type {
			types[i] = mdutil.Inline(t)
		}
		b.WriteString(strings.Join(types, ", ") + "\n")
	} else {
		b.WriteString(textNotGenerated + "\n")
	}
	b.WriteString("\n### " + TextSummary + "\n\n")
	if res.Description != nil && strings.TrimSpace(*res.Description) != "" {
		b.WriteString(mdutil.EscapeBullets(*res.Description, esc) + "\n")
	} else {
		b.WriteString(textNotGenerated + "\n")
	}

	b.WriteString("\n### " + TextWalkthrough + "\n\n")
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
			b.WriteString(indent(mdutil.EscapeBullets(s, esc), "  ") + "\n")
		}
	}

	writeNotDescribedAt(&b, &res.Coverage, "### ")
	llmrender.DescribeCoverage(&b, covHead, &res.Coverage)
	llmrender.Notes(&b, notesHead, res.Notes)
	return b.String()
}
