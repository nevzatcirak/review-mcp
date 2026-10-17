package render

import (
	"html"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Issue compliance in the overview (X-42, v2.2 spec V-2 §2.3 item 2): a row
// "Issue compliance" with the verdict, then per issue what is met, not met
// and needs verification. The reference links to the issue when its URL is
// known. Nothing is shown without a verdict.

// emojiCompliance is the issue compliance row's emoji (upstream's ticket
// compliance uses the same one).
const emojiCompliance = "🎫"

// Fixed texts of issue compliance.
const (
	textCompliance        = "Issue compliance"
	textMet               = "Met"
	textNotMet            = "Not met"
	textNeedsVerification = "Needs human verification"
)

// verdictText is the fixed text of a verdict.
func verdictText(v string) string {
	switch v {
	case review.VerdictCompliant:
		return "Compliant"
	case review.VerdictPartlyCompliant:
		return "Partly compliant"
	case review.VerdictNotCompliant:
		return "Not compliant"
	default:
		return "Unclear"
	}
}

// compliance returns the result's verdict, or nil when there is none to
// show.
func compliance(res *review.Result) *review.Compliance {
	if res == nil || res.Issue == nil || res.Issue.Compliance == nil {
		return nil
	}
	return res.Issue.Compliance
}

// issueURL is the web page of the issue the model named ref: the URL of the
// run's reference of the same name (without regard to case or space), when
// it is a clean http(s) URL.
func issueURL(res *review.Result, ref string) string {
	ref = strings.Join(strings.Fields(ref), " ")
	for _, r := range res.Issue.Refs {
		if strings.EqualFold(r.Ref, ref) {
			return provider.CleanCheckURL(r.URL)
		}
	}
	return ""
}

// complianceLists are the three lists of one issue's verdict with their
// labels, in order.
func complianceLists(iv *review.IssueVerdict) []struct {
	label string
	items []string
} {
	return []struct {
		label string
		items []string
	}{{textMet, iv.Met}, {textNotMet, iv.NotMet}, {textNeedsVerification, iv.NeedsVerification}}
}

// writeGFMCompliance writes the table row of the GFM overview: the verdict,
// then one <details> block per issue.
func writeGFMCompliance(b *strings.Builder, res *review.Result) {
	c := compliance(res)
	if c == nil {
		return
	}
	b.WriteString("<tr><td>" + emojiCompliance + "&nbsp;<strong>" + textCompliance + "</strong>: " +
		verdictText(c.Verdict))
	if len(c.Issues) > 0 {
		b.WriteString("<br><br>\n\n")
		for i := range c.Issues {
			iv := &c.Issues[i]
			head := "<strong>" + html.EscapeString(iv.Ref) + "</strong>"
			if u := issueURL(res, iv.Ref); u != "" {
				head = "<a href='" + html.EscapeString(u) + "'>" + head + "</a>"
			}
			b.WriteString("<details><summary>" + head + "</summary>\n\n")
			for _, l := range complianceLists(iv) {
				if len(l.items) == 0 {
					continue
				}
				b.WriteString("<strong>" + l.label + "</strong>\n\n")
				for _, it := range l.items {
					b.WriteString("- " + gfmText(it) + "\n")
				}
				b.WriteString("\n")
			}
			b.WriteString("</details>\n\n")
		}
	}
	b.WriteString("</td></tr>\n")
}

// complianceRow is the verdict row of the plain overview's table; ok is
// false without a verdict.
func complianceRow(res *review.Result) (label, value string, ok bool) {
	c := compliance(res)
	if c == nil {
		return "", "", false
	}
	return emojiCompliance + " " + textCompliance, verdictText(c.Verdict), true
}

// writePlainCompliance writes the per-issue section of the plain overview
// and of the client view: a heading, then per issue its reference (a link
// when known) and the three lists. heading is the section's heading line.
func writePlainCompliance(b *strings.Builder, res *review.Result, heading string) {
	c := compliance(res)
	if c == nil || len(c.Issues) == 0 {
		return
	}
	b.WriteString(heading + "\n\n")
	for i := range c.Issues {
		iv := &c.Issues[i]
		ref := mdutil.Inline(iv.Ref)
		if u := issueURL(res, iv.Ref); u != "" {
			ref = "[" + ref + "](" + u + ")"
		}
		b.WriteString("**" + ref + "**\n\n")
		for _, l := range complianceLists(iv) {
			if len(l.items) == 0 {
				continue
			}
			b.WriteString("- " + l.label + ":\n")
			for _, it := range l.items {
				b.WriteString("  - " + mdutil.InlineKeepingCodeSpans(it) + "\n")
			}
		}
		b.WriteString("\n")
	}
}
