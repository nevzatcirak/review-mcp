package render

// The emoji map and the table / <details> presentation of the provider
// profile are adapted from PR-Agent's convert_to_markdown_v2
// (pr_agent/algo/utils.py @ 8e5a929, MIT); see NOTICE.

import (
	"html"
	"strconv"
	"strings"

	llmrender "github.com/nevzatcirak/review-mcp/internal/llmrun/render"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Emojis of the provider profile (upstream's map, for our fields;
// emojiPerf is ours, since upstream has no performance field).
const (
	emojiEffort   = "⏱️"
	emojiTests    = "🧪"
	emojiSecurity = "🔒"
	emojiPerf     = "🐢"
	// emojiDiscussed is ours too (X-13).
	emojiDiscussed = "💬"
	emojiIssues    = "⚡"
	emojiCoverage  = "📂"
	emojiNotes     = "📝"
	emojiWarning   = "⚠️"
	titleSuffix    = " 🔍"
)

// Provider renders res as a published PR comment for a provider with the
// given capabilities (DQ-16); it has the review.ProviderRenderer signature.
// It is the overview of spec P7 §4.3: the header with the run time and the
// head commit, the enabled fields, the "already discussed" count when it is
// greater than 0, the findings index, the coverage and the notes. The
// pipeline appends the overview marker as the last line.
//
// With caps.GFM (Gitea) the output is upstream-style: emojis, a <table>
// layout and one <details> block per key issue; dynamic text inside HTML is
// HTML-escaped. Without GFM (Bitbucket Server) it has no HTML: headings, a
// pipe table of the fields (a plain list when caps.MarkdownTables is false)
// and the findings as a numbered list with a sub-list per finding. The coverage and notes sections are plain
// markdown in both.
func Provider(res *review.Result, caps provider.Capabilities) string {
	if res == nil {
		return ""
	}
	v := newView(res)
	var b strings.Builder
	b.WriteString("## PR Review" + titleSuffix + "\n\n")
	// X-18: right under the heading. Because the overview is edited in place
	// (X-12) by rendering it again, every edit carries the banner too.
	if banner := llmrender.PartialBanner(&res.Coverage, false); banner != "" {
		if caps.GFM {
			banner = "> " + emojiWarning + " " + banner
		}
		b.WriteString(banner + "\n\n")
	}
	b.WriteString(clientPRLine(&res.PR) + "\n")
	if l := runLine(res); l != "" {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")
	if caps.GFM {
		writeGFM(&b, res, &v)
	} else {
		writePlain(&b, res, &v, caps.MarkdownTables)
	}
	trimmed := strings.TrimRight(b.String(), "\n") + "\n"
	b.Reset()
	b.WriteString(trimmed)
	llmrender.Coverage(&b, "### "+emojiCoverage+" "+textCoverage, &res.Coverage)
	llmrender.Notes(&b, "### "+emojiNotes+" "+textNotes, res.Notes)
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
				gfmRow(b, emojiSecurity, v.noSecurity(), "")
				continue
			}
			b.WriteString("<tr><td>" + emojiSecurity + "&nbsp;<strong>" + textSecurity + "</strong><br><br>\n\n" +
				gfmText(*v.security) + "\n</td></tr>\n")
		case k == review.KeyPerformanceConcerns && v.perf != nil:
			if !v.hasPerf {
				gfmRow(b, emojiPerf, v.noPerf(), "")
				continue
			}
			b.WriteString("<tr><td>" + emojiPerf + "&nbsp;<strong>" + textPerf + "</strong><br><br>\n\n" +
				gfmText(*v.perf) + "\n</td></tr>\n")
		}
	}
	if n := res.Metadata.AlreadyDiscussed; n > 0 {
		gfmRow(b, emojiDiscussed, textDiscussed, ": "+strconv.Itoa(n))
	}
	if v.showIssues && v.hasReview {
		b.WriteString("<tr><td>")
		if len(v.issues) == 0 {
			b.WriteString(emojiIssues + "&nbsp;<strong>" + v.noIssues() + "</strong>")
		} else {
			b.WriteString(emojiIssues + "&nbsp;<strong>" + textFocusAreas + "</strong><br><br>\n\n")
			for n := range v.issues {
				b.WriteString(gfmIssue(n+1, &v.issues[n]) + "\n\n")
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

// gfmIssue is entry n of the findings index (spec P7 §4.3): a <details>
// block whose summary is the index line (number, header linked to the
// inline comment or the file line, location, "(listed here only)" when the
// finding has no inline comment) and whose body is the content, the
// snippet and the snippet note.
func gfmIssue(n int, i *review.KeyIssue) string {
	head := "<strong>" + html.EscapeString(issueHeader(i.IssueHeader)) + "</strong>"
	if l := findingLink(i); l != "" {
		head = "<a href='" + html.EscapeString(l) + "'>" + head + "</a>"
	}
	head = strconv.Itoa(n) + ". " + head
	if file := strings.TrimSpace(i.RelevantFile); file != "" {
		loc := file
		if lr := lineRange(i); lr != "" {
			loc += " " + lr
		}
		head += " <code>" + html.EscapeString(loc) + "</code>"
	}
	if listedHereOnly(i) {
		head += " " + textListedHere
	}
	var b strings.Builder
	b.WriteString("<details><summary>" + head + "</summary>\n\n" + gfmText(i.IssueContent) + "\n")
	if i.Snippet != "" {
		b.WriteString("\n")
		mdutil.WriteFenced(&b, i.Snippet, langTag(i.RelevantFile), "")
	}
	if i.SnippetNote != "" {
		b.WriteString("\n" + textSnippetNote + gfmText(i.SnippetNote) + "\n")
	}
	b.WriteString("\n</details>")
	return b.String()
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
			rows = append(rows, row{emojiSecurity + " Security", v.noSecurity()})
		case k == review.KeyPerformanceConcerns && v.perf != nil && !v.hasPerf:
			rows = append(rows, row{emojiPerf + " Performance", v.noPerf()})
		}
	}
	if n := res.Metadata.AlreadyDiscussed; n > 0 {
		rows = append(rows, row{emojiDiscussed + " " + textDiscussed, strconv.Itoa(n)})
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
	if v.showPerf && v.hasPerf {
		b.WriteString("### " + emojiPerf + " " + textPerf + "\n\n" + mdutil.Escape(strings.TrimSpace(*v.perf)) + "\n\n")
	}
	if !v.showIssues || !v.hasReview {
		return
	}
	if len(v.issues) == 0 {
		b.WriteString("### " + emojiIssues + " " + v.noIssues() + "\n")
		return
	}
	b.WriteString("### " + emojiIssues + " " + textFocusAreas + "\n\n")
	for n := range v.issues {
		writePlainIssue(b, n+1, &v.issues[n])
	}
}

// writePlainIssue writes one entry of the findings index without HTML
// (spec P7 §4.3): a numbered line with the header, the location (a link to
// the inline comment or the file line) and "(listed here only)" when the
// finding has no inline comment, then a sub-list with the content, the
// snippet and the snippet note.
func writePlainIssue(b *strings.Builder, n int, i *review.KeyIssue) {
	prefix := strconv.Itoa(n) + ". "
	indent := strings.Repeat(" ", len(prefix))
	sub := indent + "  "
	b.WriteString(prefix + "**" + mdutil.Inline(issueHeader(i.IssueHeader)) + "**")
	if loc := clientLocation(i); loc != "" {
		b.WriteString(" — " + loc)
	}
	if listedHereOnly(i) {
		b.WriteString(" " + textListedHere)
	}
	b.WriteString("\n")
	if c := strings.TrimSpace(i.IssueContent); c != "" {
		b.WriteString(indent + "- " + strings.TrimPrefix(indentLines(mdutil.Escape(c), sub), sub) + "\n")
		if i.Snippet != "" {
			b.WriteString("\n")
			mdutil.WriteFenced(b, i.Snippet, langTag(i.RelevantFile), sub)
		}
	} else if i.Snippet != "" {
		b.WriteString(indent + "- " + textCode + "\n\n")
		mdutil.WriteFenced(b, i.Snippet, langTag(i.RelevantFile), sub)
	}
	if i.SnippetNote != "" {
		b.WriteString(indent + "- " + textSnippetNote + mdutil.Inline(i.SnippetNote) + "\n")
	}
	b.WriteString("\n")
}
