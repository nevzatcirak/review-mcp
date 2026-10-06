package render

// The emoji map and the table / <details> presentation of the provider
// profile are adapted from PR-Agent's convert_to_markdown_v2
// (pr_agent/algo/utils.py @ 8e5a929, MIT); see NOTICE.

import (
	"html"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Emojis of the provider profile (upstream's map, for our fields).
const (
	emojiEffort   = "⏱️"
	emojiTests    = "🧪"
	emojiSecurity = "🔒"
	emojiIssues   = "⚡"
	emojiCoverage = "📂"
	emojiNotes    = "📝"
	titleSuffix   = " 🔍"
)

// Provider renders res as a published PR comment for a provider with the
// given capabilities (DQ-16); it has the review.ProviderRenderer signature.
//
// With caps.GFM (Gitea) the output is upstream-style: emojis, a <table>
// layout and one <details> block per key issue; dynamic text inside HTML is
// HTML-escaped. Without GFM (Bitbucket Server) it is headings and pipe
// tables with no HTML (a plain list instead of tables when
// caps.MarkdownTables is false). The coverage and notes sections are plain
// markdown in both.
func Provider(res *review.Result, caps provider.Capabilities) string {
	if res == nil {
		return ""
	}
	v := newView(res)
	var b strings.Builder
	b.WriteString("## PR Review" + titleSuffix + "\n\n")
	b.WriteString(clientPRLine(&res.PR) + "\n\n")
	if caps.GFM {
		writeGFM(&b, res, &v)
	} else {
		writePlain(&b, res, &v, caps.MarkdownTables)
	}
	trimmed := strings.TrimRight(b.String(), "\n") + "\n"
	b.Reset()
	b.WriteString(trimmed)
	writeCoverage(&b, "### "+emojiCoverage+" "+textCoverage, &res.Coverage)
	writeNotes(&b, "### "+emojiNotes+" "+textNotes, res.Notes)
	return b.String()
}

// gfmText makes untrusted text safe inside HTML and markdown at once:
// markdown control characters are escaped, then HTML metacharacters become
// entities, which render as the literal characters in both contexts.
func gfmText(s string) string {
	return html.EscapeString(mdutil.EscapeControl(strings.TrimSpace(s)))
}

// ---- GFM (Gitea) ----

func writeGFM(b *strings.Builder, res *review.Result, v *view) {
	b.WriteString("<table>\n")
	for _, k := range res.EnabledFields {
		switch {
		case k == review.KeyEffort && v.effort != nil:
			gfmRow(b, emojiEffort, textEffort, ": "+effortValue(*v.effort))
		case k == review.KeyRelevantTests && v.tests != nil:
			gfmRow(b, emojiTests, testsText(*v.tests), "")
		case k == review.KeySecurityConcerns && v.security != nil:
			if !v.hasConcerns {
				gfmRow(b, emojiSecurity, textNoSecurity, "")
				continue
			}
			b.WriteString("<tr><td>" + emojiSecurity + "&nbsp;<strong>" + textSecurity + "</strong><br><br>\n\n" +
				gfmText(*v.security) + "\n</td></tr>\n")
		}
	}
	if v.showIssues && v.hasReview {
		b.WriteString("<tr><td>")
		if len(v.issues) == 0 {
			b.WriteString(emojiIssues + "&nbsp;<strong>" + textNoIssues + "</strong>")
		} else {
			b.WriteString(emojiIssues + "&nbsp;<strong>" + textFocusAreas + "</strong><br><br>\n\n")
			for n := range v.issues {
				b.WriteString(gfmIssue(&v.issues[n]) + "\n\n")
			}
		}
		b.WriteString("</td></tr>\n")
	}
	b.WriteString("</table>\n")
}

// gfmRow writes one single-line table row; label is fixed English text and
// value is built by the caller from fixed text only.
func gfmRow(b *strings.Builder, emoji, label, value string) {
	b.WriteString("<tr><td>" + emoji + "&nbsp;<strong>" + label + "</strong>" + value + "</td></tr>\n")
}

// gfmIssue is one key issue: a <details> block with the snippet when there
// is one, a plain block otherwise.
func gfmIssue(i *review.KeyIssue) string {
	head := "<strong>" + html.EscapeString(issueHeader(i.IssueHeader)) + "</strong>"
	if l := safeLink(i.Link); l != "" {
		head = "<a href='" + html.EscapeString(l) + "'>" + head + "</a>"
	}
	if file := strings.TrimSpace(i.RelevantFile); file != "" {
		loc := file
		if lr := lineRange(i); lr != "" {
			loc += " " + lr
		}
		head += " <code>" + html.EscapeString(loc) + "</code>"
	}
	var b strings.Builder
	content := gfmText(i.IssueContent)
	if i.Snippet != "" {
		b.WriteString("<details><summary>" + head + "\n\n" + content + "\n</summary>\n\n")
		mdutil.WriteFenced(&b, i.Snippet, langTag(i.RelevantFile), "")
		if i.SnippetNote != "" {
			b.WriteString("\n" + textSnippetNote + gfmText(i.SnippetNote) + "\n")
		}
		b.WriteString("\n</details>")
		return b.String()
	}
	b.WriteString(head + "<br>\n\n" + content + "\n")
	if i.SnippetNote != "" {
		b.WriteString("\n" + textSnippetNote + gfmText(i.SnippetNote) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- Plain markdown (Bitbucket Server) ----

func writePlain(b *strings.Builder, res *review.Result, v *view, tables bool) {
	type row struct{ label, value string }
	var rows []row
	for _, k := range res.EnabledFields {
		switch {
		case k == review.KeyEffort && v.effort != nil:
			rows = append(rows, row{emojiEffort + " " + textEffort, effortValue(*v.effort)})
		case k == review.KeyRelevantTests && v.tests != nil:
			rows = append(rows, row{emojiTests + " Tests", testsText(*v.tests)})
		case k == review.KeySecurityConcerns && v.security != nil && !v.hasConcerns:
			rows = append(rows, row{emojiSecurity + " Security", textNoSecurity})
		}
	}
	if len(rows) > 0 {
		if tables {
			b.WriteString("| Check | Result |\n|---|---|\n")
		}
		for _, r := range rows {
			if tables {
				b.WriteString("| " + r.label + " | " + r.value + " |\n")
			} else {
				b.WriteString("- " + r.label + ": " + r.value + "\n")
			}
		}
		b.WriteString("\n")
	}
	if v.showSec && v.hasConcerns {
		b.WriteString("### " + emojiSecurity + " " + textSecurity + "\n\n" + mdutil.Escape(strings.TrimSpace(*v.security)) + "\n\n")
	}
	if !v.showIssues || !v.hasReview {
		return
	}
	if len(v.issues) == 0 {
		b.WriteString("### " + emojiIssues + " " + textNoIssues + "\n")
		return
	}
	b.WriteString("### " + emojiIssues + " " + textFocusAreas + "\n\n")
	if tables {
		b.WriteString("| # | Issue | Location |\n|---|---|---|\n")
		for n := range v.issues {
			i := &v.issues[n]
			b.WriteString("| " + strconv.Itoa(n+1) + " | " + mdutil.Inline(issueHeader(i.IssueHeader)) + " | " + clientLocation(i) + " |\n")
		}
		b.WriteString("\n")
	}
	for n := range v.issues {
		i := &v.issues[n]
		b.WriteString("#### " + strconv.Itoa(n+1) + ". " + mdutil.Inline(issueHeader(i.IssueHeader)) + "\n\n")
		if !tables {
			if loc := clientLocation(i); loc != "" {
				b.WriteString(loc + "\n\n")
			}
		}
		if c := strings.TrimSpace(i.IssueContent); c != "" {
			b.WriteString(mdutil.Escape(c) + "\n\n")
		}
		if i.Snippet != "" {
			mdutil.WriteFenced(b, i.Snippet, langTag(i.RelevantFile), "")
			b.WriteString("\n")
		}
		if i.SnippetNote != "" {
			b.WriteString(textSnippetNote + mdutil.Inline(i.SnippetNote) + "\n\n")
		}
	}
}
