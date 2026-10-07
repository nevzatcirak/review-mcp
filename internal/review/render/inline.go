package render

import (
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Inline renders the body of a finding's inline comment (spec P7 §3.2); it
// has the review.InlineRenderer signature. The body is the bold header, a
// blank line, the content and, when the finding spans more than one line,
// a line naming the range ("Lines 40–52"). There is no snippet: the code is
// next to the comment. The pipeline appends the fingerprint marker.
//
// The header and content are escaped as in the overview (Provider): with
// caps.GFM (Gitea) markdown control characters are backslash-escaped and
// HTML metacharacters become entities; without it (Bitbucket Server) they
// are backslash-escaped. Neither form contains HTML.
func Inline(i *review.KeyIssue, caps provider.Capabilities) string {
	if i == nil {
		return ""
	}
	header := strings.Join(strings.Fields(issueHeader(i.IssueHeader)), " ")
	content := strings.TrimSpace(i.IssueContent)
	var b strings.Builder
	if caps.GFM {
		b.WriteString("**" + gfmText(header) + "**\n")
		if content != "" {
			b.WriteString("\n" + gfmText(content) + "\n")
		}
	} else {
		b.WriteString("**" + mdutil.Inline(header) + "**\n")
		if content != "" {
			b.WriteString("\n" + mdutil.Escape(content) + "\n")
		}
	}
	if i.StartLine > 0 && i.EndLine > i.StartLine {
		b.WriteString("\nLines " + strconv.Itoa(i.StartLine) + "–" + strconv.Itoa(i.EndLine) + "\n")
	}
	return b.String()
}
