// Package render turns a review.Result into markdown for its two audiences
// (DQ-16): the MCP client (portable markdown, no raw HTML) and a published
// provider comment (Gitea: GFM with HTML; Bitbucket Server: headings and
// pipe tables).
//
// Language: the review text (finding headers and contents, security text,
// notes) is model-authored in the requested output language. The fixed
// headings and labels of the renderers stay English; translating them is out
// of scope.
package render

import (
	"strconv"
	"strings"

	llmrender "github.com/nevzatcirak/review-mcp/internal/llmrun/render"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Client renders res for the MCP client (DQ-16): portable markdown with no
// raw HTML. Every dynamic string outside a fenced block is escaped for
// markdown control characters and HTML angle brackets; snippets go in
// backtick fences longer than any backtick run inside them.
//
// Layout: header and PR reference; the enabled fields in descriptor order
// with the key issues last; the coverage section (always); the notes section
// when there are notes.
func Client(res *review.Result) string {
	if res == nil {
		return ""
	}
	v := newView(res)
	var b strings.Builder
	b.WriteString("## PR Review\n\n")
	b.WriteString(clientPRLine(&res.PR) + "\n")

	// Short facts as a list; the security text is a section of its own.
	var facts []string
	for _, k := range res.EnabledFields {
		switch {
		case k == review.KeyEffort && v.effort != nil:
			facts = append(facts, textEffort+": "+effortValue(*v.effort))
		case k == review.KeyRelevantTests && v.tests != nil:
			facts = append(facts, testsText(*v.tests))
		case k == review.KeySecurityConcerns && v.security != nil && !v.hasConcerns:
			facts = append(facts, textNoSecurity)
		}
	}
	if len(facts) > 0 {
		b.WriteString("\n")
		for _, f := range facts {
			b.WriteString("- " + f + "\n")
		}
	}
	if v.showSec && v.hasConcerns {
		b.WriteString("\n### " + textSecurity + "\n\n" + mdutil.Escape(strings.TrimSpace(*v.security)) + "\n")
	}
	if v.showIssues && v.hasReview {
		writeClientIssues(&b, v.issues)
	}
	llmrender.Coverage(&b, "### "+textCoverage, &res.Coverage)
	llmrender.Notes(&b, "### "+textNotes, res.Notes)
	return b.String()
}

func testsText(has bool) string {
	if has {
		return textTests
	}
	return textNoTests
}

func clientPRLine(pr *review.PRInfo) string {
	line := "Pull request: " + mdutil.Inline(providerName(pr.Kind))
	if pr.Number > 0 {
		line += " #" + strconv.FormatInt(pr.Number, 10)
	}
	if t := strings.TrimSpace(pr.Title); t != "" {
		line += " — " + mdutil.Inline(t)
	}
	if pr.URL != "" {
		line += " (" + literal(pr.URL) + ")"
	}
	return line
}

func writeClientIssues(b *strings.Builder, issues []review.KeyIssue) {
	b.WriteString("\n### " + textKeyIssues + "\n\n")
	if len(issues) == 0 {
		b.WriteString(textNoIssues + "\n")
		return
	}
	for n := range issues {
		i := &issues[n]
		if n > 0 {
			b.WriteString("\n")
		}
		prefix := strconv.Itoa(n+1) + ". "
		indent := strings.Repeat(" ", len(prefix))
		b.WriteString(prefix + "**" + mdutil.Inline(issueHeader(i.IssueHeader)) + "**")
		if loc := clientLocation(i); loc != "" {
			b.WriteString(" — " + loc)
		}
		b.WriteString("\n")
		if c := strings.TrimSpace(i.IssueContent); c != "" {
			b.WriteString("\n" + indentLines(mdutil.Escape(c), indent) + "\n")
		}
		if i.Snippet != "" {
			b.WriteString("\n")
			mdutil.WriteFenced(b, i.Snippet, langTag(i.RelevantFile), indent)
		}
		if i.SnippetNote != "" {
			b.WriteString("\n" + indent + textSnippetNote + mdutil.Inline(i.SnippetNote) + "\n")
		}
	}
}

// clientLocation is "file L10-12" as a markdown link when the finding has a
// usable link, else the same text unlinked.
func clientLocation(i *review.KeyIssue) string {
	file := strings.TrimSpace(i.RelevantFile)
	if file == "" {
		return ""
	}
	text := literal(file)
	if lr := lineRange(i); lr != "" {
		text += " " + lr
	}
	if l := findingLink(i); l != "" {
		return "[" + text + "](" + l + ")"
	}
	return text
}
