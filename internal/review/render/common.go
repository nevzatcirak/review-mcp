package render

import (
	"net/url"
	"strconv"
	"strings"
	"time"

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
// security and performance text, notes) is model-authored in the requested output language;
// these headings and labels stay English.
const (
	textEffort     = "Estimated effort to review"
	textTests      = "PR contains tests"
	textNoTests    = "No relevant tests"
	textNoSecurity = "No security concerns identified"
	textSecurity   = "Security concerns"
	textNoPerf     = "No performance concerns identified"
	// The same statements for a partial result (X-18): they claim nothing
	// about the files that were not reviewed.
	textNoSecurityScoped = "No security concerns identified in the reviewed files"
	textNoPerfScoped     = "No performance concerns identified in the reviewed files"
	textNoIssuesScoped   = "No key issues found in the reviewed files"
	textPerf             = "Performance concerns"
	textKeyIssues        = "Key issues to review"
	textNoIssues         = "No major issues detected"
	textFocusAreas       = "Recommended focus areas for review"
	textCoverage         = llmrender.TextCoverage
	textNotes            = llmrender.TextNotes
	textSnippetNote      = "Snippet note: "
	textListedHere       = "(listed here only)"
	textDiscussed        = "Already discussed"
	textCode             = "Code:"
	textPublish          = "Publishing"
	possibleBug          = "possible bug"
	possibleIssue        = "Possible Issue"
	defaultHeader        = "Issue"
	noLangTag            = "other"
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
	perf        *string
	showEffort  bool
	showTests   bool
	showSec     bool
	showPerf    bool
	showIssues  bool
	issues      []review.KeyIssue
	hasReview   bool
	hasConcerns bool
	hasPerf     bool
	// partial is the X-18 state of the coverage: "no concerns" statements
	// are scoped to the reviewed files.
	partial bool
}

// noSecurity, noPerf and noIssues are the "nothing found" statements, scoped
// to the reviewed files when the result is partial.
func (v *view) noSecurity() string {
	if v.partial {
		return textNoSecurityScoped
	}
	return textNoSecurity
}

func (v *view) noPerf() string {
	if v.partial {
		return textNoPerfScoped
	}
	return textNoPerf
}

func (v *view) noIssues() string {
	if v.partial {
		return textNoIssuesScoped
	}
	return textNoIssues
}

func newView(res *review.Result) view {
	var v view
	v.partial = res.Coverage.Tally().Partial
	for _, k := range res.EnabledFields {
		switch k {
		case review.KeyEffort:
			v.showEffort = true
		case review.KeyRelevantTests:
			v.showTests = true
		case review.KeySecurityConcerns:
			v.showSec = true
		case review.KeyPerformanceConcerns:
			v.showPerf = true
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
		v.perf = r.PerformanceConcerns
		if v.perf != nil && strings.TrimSpace(*v.perf) == "" {
			v.perf = nil // as for security: an empty answer claims nothing
		}
		v.hasPerf = v.perf != nil && r.HasPerformanceConcerns()
	}
	return v
}

// listedHereOnly reports a finding of an inline publish that has no inline
// comment because it is not on a changed line or its comment failed.
func listedHereOnly(i *review.KeyIssue) bool {
	return i.InlineStatus == review.InlineUnanchorable || i.InlineStatus == review.InlineFailed
}

// runLine is the overview's second header line (spec P7 §4.3): the run time
// in UTC and the head commit's short SHA, or "" when neither is known.
func runLine(res *review.Result) string {
	var parts []string
	if t, err := time.Parse(time.RFC3339, res.Metadata.ReviewedAt); err == nil {
		parts = append(parts, "on "+t.UTC().Format("2006-01-02 15:04")+" UTC")
	}
	if sha := shortSHA(res.PR.HeadSHA); sha != "" {
		parts = append(parts, "at commit `"+sha+"`")
	}
	if len(parts) == 0 {
		return ""
	}
	return "Reviewed " + strings.Join(parts, " ") + "."
}

// shortSHA is the first 7 characters of a hexadecimal commit id, or ""
// when sha is not one (it is then not shown).
func shortSHA(sha string) string {
	if len(sha) < 7 {
		return ""
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return ""
		}
	}
	return strings.ToLower(sha[:7])
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

// findingLink is where a finding links to: its inline comment when one was
// posted (spec P7 §3.3), else its file line.
func findingLink(i *review.KeyIssue) string {
	if l := safeLink(i.InlineURL); l != "" {
		return l
	}
	return safeLink(i.Link)
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
