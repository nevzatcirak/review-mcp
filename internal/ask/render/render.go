// Package render turns an ask.Result into markdown for its two audiences
// (DQ-16): the MCP client (portable markdown, no raw HTML) and a published
// provider comment (Gitea: GFM headings with emojis; Bitbucket Server: plain
// headings).
//
// It lives apart from internal/ask so that the pipeline package has no
// rendering code, and it imports only YAML-free packages (the shared
// coverage and notes sections come from internal/llmrun/render). The fixed
// headings stay English; the answer is model-authored, in the requested
// output language, and is shown as is.
package render

import (
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	llmrender "github.com/nevzatcirak/review-mcp/internal/llmrun/render"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Emojis of the GFM provider profile (the same as pr_review's).
const (
	emojiCoverage = "📂"
	emojiNotes    = "📝"
	emojiWarning  = "⚠️"
)

// Client renders res for the MCP client: "## Question" with the question in
// a fenced block (the fence is longer than any backtick run in the
// question), "## Answer" with the model's answer as it is (it is
// model-authored markdown; the section is omitted when no model call was
// made), the coverage section (always) and the notes section (when there
// are notes).
func Client(res *ask.Result) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	// X-18: a partial result leads with the banner, before anything else.
	if banner := llmrender.PartialBanner(&res.Coverage, true); banner != "" {
		b.WriteString(banner + "\n\n")
	}
	b.WriteString("## Question\n\n")
	mdutil.WriteFenced(&b, res.Question, "", "")
	if a := strings.TrimSpace(res.Answer); a != "" {
		b.WriteString("\n## Answer\n\n" + a + "\n")
	}
	llmrender.Coverage(&b, "## "+llmrender.TextCoverage, &res.Coverage)
	llmrender.Notes(&b, "## "+llmrender.TextNotes, res.Notes)
	return b.String()
}

// Provider renders res as a published PR comment for a provider with the
// given capabilities; it has the ask.ProviderRenderer signature.
//
// The layout follows upstream's _prepare_pr_answer: a heading for the
// question, the question, a heading for the answer, the answer. With
// caps.GFM (Gitea) the headings are upstream's ("Ask" with the question
// emoji, and "Answer:"); without it (Bitbucket Server) they are plain. The
// coverage section follows in both. Quick-action sanitization
// (provider.SanitizeQuickActions) applies to the question and the answer on
// every provider, whatever caps.QuickActions says (the v1 behaviour, so the
// published bytes do not change): no line of either starts with "/".
// Publishing adds provider.SanitizeBody on top for the whole body when the
// provider has QuickActions.
//
// DESIGN-QUESTION: is the question shown raw, as upstream does, or in a
// fenced block? — chose a fenced block, as the client profile does, because
// the question is caller-supplied text and raw markdown could spoof the
// comment's own headings.
func Provider(res *ask.Result, caps provider.Capabilities) string {
	if res == nil {
		return ""
	}
	askHead, answerHead := "### Question", "### Answer"
	covHead, notesHead := "### "+llmrender.TextCoverage, "### "+llmrender.TextNotes
	if caps.GFM {
		askHead, answerHead = "### **Ask** ❓", "### **Answer:**"
		covHead = "### " + emojiCoverage + " " + llmrender.TextCoverage
		notesHead = "### " + emojiNotes + " " + llmrender.TextNotes
	}
	var b strings.Builder
	// X-18: the comment has no title of its own, so the banner is its first
	// line (a warning blockquote on Gitea, a bold line on Bitbucket Server).
	if banner := llmrender.PartialBanner(&res.Coverage, true); banner != "" {
		if caps.GFM {
			banner = "> " + emojiWarning + " " + banner
		}
		b.WriteString(banner + "\n\n")
	}
	b.WriteString(askHead + "\n")
	mdutil.WriteFenced(&b, provider.SanitizeQuickActions(res.Question), "", "")
	if a := strings.TrimSpace(res.Answer); a != "" {
		b.WriteString("\n" + answerHead + "\n" + provider.SanitizeQuickActions(a) + "\n")
	}
	llmrender.Coverage(&b, covHead, &res.Coverage)
	llmrender.Notes(&b, notesHead, res.Notes)
	return b.String()
}
