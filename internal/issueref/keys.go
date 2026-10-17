package issueref

import (
	"regexp"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// MaxKeys is the most tracker keys DetectKeys returns.
const MaxKeys = 5

// notTrackerPrefixes are key prefixes that name standards, encodings,
// algorithms and specifications, not trackers (WP-10a DQ-2, decided in the
// architect review): UTF-8, SHA-256, ISO-8601, RFC-9110, CVE-2024, CWE-79,
// TLS-1, SSL-3, HTTP-2, UTC-5, GMT-3, AES-256, RSA-2048, MD-5, ECMA-262,
// ES-2022, IEEE-754, ANSI-1, PEP-8, JSR-310. A key whose prefix is one of
// them is never detected. The list is fixed and kept here only.
var notTrackerPrefixes = map[string]bool{
	"UTF": true, "SHA": true, "ISO": true, "RFC": true, "CVE": true, "CWE": true, "TLS": true, "SSL": true,
	"HTTP": true, "UTC": true, "GMT": true, "AES": true, "RSA": true, "MD": true, "ECMA": true, "ES": true,
	"IEEE": true, "ANSI": true, "PEP": true, "JSR": true,
}

var (
	keyRE = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d{1,7}\b`)
	// inlineCodeRE is a Markdown code span on one line.
	inlineCodeRE = regexp.MustCompile("`+[^`\n]*`+")
)

// DetectKeys returns the tracker keys (such as PROJ-123) that pr's title,
// source branch and description name, in that order, at most MaxKeys
// distinct ones. Keys inside URLs, code spans and fenced code blocks are
// ignored, and so are keys whose prefix names a standard or an encoding
// rather than a tracker (notTrackerPrefixes: UTF-8, SHA-256, ...).
// review-mcp never reads them: they only tell the client which issue it
// could pass as issue_context.
func DetectKeys(pr *provider.PullRequest) []string {
	if pr == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, text := range []string{cleanLine(pr.Title), cleanLine(pr.SourceBranch), cleanDescription(pr.Description)} {
		text = urlRE.ReplaceAllString(inlineCodeRE.ReplaceAllString(text, " "), " ")
		for _, k := range keyRE.FindAllString(text, -1) {
			if prefix, _, _ := strings.Cut(k, "-"); notTrackerPrefixes[prefix] {
				continue
			}
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
