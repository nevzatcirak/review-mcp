package issueref

import (
	"regexp"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// MaxKeys is the most tracker keys DetectKeys returns.
const MaxKeys = 5

var (
	// DESIGN-QUESTION (WP-10a DQ-2): the key pattern of the spec also
	// matches names of standards such as UTF-8, SHA-256 or ISO-8601, which
	// then reach issue.detected_keys and its note. The pattern is applied
	// as the spec gives it. Recommendation: a short fixed stop list of
	// such prefixes (UTF, SHA, ISO, RFC, CVE, ...) in WP-10b.
	keyRE = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d{1,7}\b`)
	// inlineCodeRE is a Markdown code span on one line.
	inlineCodeRE = regexp.MustCompile("`+[^`\n]*`+")
)

// DetectKeys returns the tracker keys (such as PROJ-123) that pr's title,
// source branch and description name, in that order, at most MaxKeys
// distinct ones. Keys inside URLs, code spans and fenced code blocks are
// ignored. review-mcp never reads them: they only tell the client which
// issue it could pass as issue_context.
func DetectKeys(pr *provider.PullRequest) []string {
	if pr == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, text := range []string{cleanLine(pr.Title), cleanLine(pr.SourceBranch), cleanDescription(pr.Description)} {
		text = urlRE.ReplaceAllString(inlineCodeRE.ReplaceAllString(text, " "), " ")
		for _, k := range keyRE.FindAllString(text, -1) {
			if len(out) >= MaxKeys {
				return out
			}
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}
