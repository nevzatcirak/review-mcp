package render

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/filter"
	llmrender "github.com/nevzatcirak/review-mcp/internal/llmrun/render"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// MaxListedFiles is the most files the coverage section lists (X-3); the
// rest are summarized as "and N more".
const MaxListedFiles = llmrender.MaxListedFiles

// literal is mdutil.Literal.
func literal(s string) string { return mdutil.Literal(s) }

// Fixed English texts. The review text itself (headers, finding contents,
// security text, notes) is model-authored in the requested output language;
// these headings and labels stay English.
const (
	textEffort      = "Estimated effort to review"
	textTests       = "PR contains tests"
	textNoTests     = "No relevant tests"
	textNoSecurity  = "No security concerns identified"
	textSecurity    = "Security concerns"
	textKeyIssues   = "Key issues to review"
	textNoIssues    = "No major issues detected"
	textFocusAreas  = "Recommended focus areas for review"
	textCoverage    = llmrender.TextCoverage
	textNotes       = llmrender.TextNotes
	textSnippetNote = "Snippet note: "
	possibleBug     = "possible bug"
	possibleIssue   = "Possible Issue"
	defaultHeader   = "Issue"
	noLangTag       = "other"
)

const (
	filledBar = "🔵"
	emptyBar  = "⚪"
)

// view is the review of a Result, resolved to what the renderers show.
type view struct {
	effort      *int
	tests       *bool
	security    *string
	showEffort  bool
	showTests   bool
	showSec     bool
	showIssues  bool
	issues      []review.KeyIssue
	hasReview   bool
	hasConcerns bool
}

func newView(res *review.Result) view {
	var v view
	for _, k := range res.EnabledFields {
		switch k {
		case review.KeyEffort:
			v.showEffort = true
		case review.KeyRelevantTests:
			v.showTests = true
		case review.KeySecurityConcerns:
			v.showSec = true
		case review.KeyKeyIssues:
			v.showIssues = true
		}
	}
	if r := res.Review; r != nil {
		v.hasReview = true
		v.effort, v.tests, v.security, v.issues = r.EstimatedEffortToReview, r.RelevantTests, r.SecurityConcerns, r.KeyIssuesToReview
		if v.security != nil && strings.TrimSpace(*v.security) == "" {
			v.security = nil // an empty answer says nothing; do not claim "no concerns"
		}
		v.hasConcerns = v.security != nil && r.HasSecurityConcerns()
	}
	return v
}

// effortValue clamps the effort to 1..5 and renders "N/5" with the bars.
func effortValue(n int) string {
	n = max(review.MinEffort, min(review.MaxEffort, n))
	return strconv.Itoa(n) + "/" + strconv.Itoa(review.MaxEffort) + " " +
		strings.Repeat(filledBar, n) + strings.Repeat(emptyBar, review.MaxEffort-n)
}

// providerName is the display name of a provider kind.
func providerName(kind string) string {
	switch kind {
	case string(provider.KindGitea):
		return "Gitea"
	case string(provider.KindBitbucketServer):
		return "Bitbucket Server"
	}
	return kind
}

// issueHeader is the finding's title: upstream softens "Possible Bug" to
// "Possible Issue"; an empty title gets a neutral one.
func issueHeader(h string) string {
	h = strings.TrimSpace(h)
	switch {
	case strings.EqualFold(h, possibleBug):
		return possibleIssue
	case h == "":
		return defaultHeader
	}
	return h
}

// lineRange formats a finding's lines ("L10-12", "L10"), or "" when the
// range is not usable.
func lineRange(i *review.KeyIssue) string {
	if i.StartLine <= 0 || i.EndLine < i.StartLine {
		return ""
	}
	if i.StartLine == i.EndLine {
		return "L" + strconv.Itoa(i.StartLine)
	}
	return "L" + strconv.Itoa(i.StartLine) + "-" + strconv.Itoa(i.EndLine)
}

var linkEscaper = strings.NewReplacer(
	"(", "%28", ")", "%29", "<", "%3C", ">", "%3E", "'", "%27", `"`, "%22", "`", "%60", `\`, "%5C", "|", "%7C",
)

// safeLink returns link as a URL that can sit in a markdown link
// destination or an HTML attribute, or "" when it is not an absolute
// http(s) URL.
func safeLink(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return linkEscaper.Replace(u.String())
}

// langTag is the info-string word of a snippet's fence: the language of the
// file, lower-cased and reduced to characters that are safe in an info
// string; "" for an unknown language.
func langTag(file string) string {
	lang := strings.ToLower(filter.Language(file))
	if lang == noLangTag || lang == "" {
		return ""
	}
	var b strings.Builder
	for _, c := range lang {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '#', c == '.', c == '-':
			b.WriteRune(c)
		case c == ' ' || c == '_':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// indentLines prefixes every non-empty line of s with indent.
func indentLines(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = indent + l
		}
	}
	return strings.Join(lines, "\n")
}
