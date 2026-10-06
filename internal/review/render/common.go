package render

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// MaxListedFiles is the most files the coverage section lists (X-3); the
// rest are summarized as "and N more".
const MaxListedFiles = 50

// Fixed English texts. The review text itself (headers, finding contents,
// security text, notes) is model-authored in the requested output language;
// these headings and labels stay English.
const (
	textEffort        = "Estimated effort to review"
	textTests         = "PR contains tests"
	textNoTests       = "No relevant tests"
	textNoSecurity    = "No security concerns identified"
	textSecurity      = "Security concerns"
	textKeyIssues     = "Key issues to review"
	textNoIssues      = "No major issues detected"
	textFocusAreas    = "Recommended focus areas for review"
	textCoverage      = "Coverage"
	textNotes         = "Notes"
	textSnippetNote   = "Snippet note: "
	reasonBudgetAdded = "Left out to fit the context window (added files)"
	reasonBudgetMod   = "Left out to fit the context window (modified files)"
	reasonBudgetDel   = "Left out to fit the context window (deleted files)"
	possibleBug       = "possible bug"
	possibleIssue     = "Possible Issue"
	defaultHeader     = "Issue"
	noLangTag         = "other"
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

// literal renders a path or URL as a code span, or as escaped plain text
// when it holds characters that a code span cannot make inert in every
// context (HTML angle brackets, ampersands, pipes).
func literal(s string) string {
	if strings.ContainsAny(s, "<>&|") {
		return mdutil.Inline(s)
	}
	return mdutil.CodeSpan(s)
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

// coverageGroup is one list of files in the coverage section.
type coverageGroup struct {
	title string // already markdown-safe
	files []string
}

// writeCoverage writes the coverage section (X-3), which is always present:
// the included and omitted counts, then the files that are clipped or
// omitted, grouped by reason. At most MaxListedFiles files are listed
// across all groups; the rest are counted ("and N more"). Everything is
// plain markdown (no HTML).
func writeCoverage(b *strings.Builder, heading string, c *review.Coverage) {
	included := len(c.Included) + len(c.Clipped)
	budget := len(c.Omitted.Added) + len(c.Omitted.Modified) + len(c.Omitted.Deleted)
	omitted := budget + len(c.Skipped) + len(c.Filtered)

	b.WriteString("\n" + heading + "\n\n")
	b.WriteString("- Included: " + strconv.Itoa(included) + " " + plural(included))
	if n := len(c.Clipped); n > 0 {
		b.WriteString(" (" + strconv.Itoa(n) + " in part only, clipped to fit the context window)")
	}
	b.WriteString("\n- Omitted: " + strconv.Itoa(omitted) + " " + plural(omitted) + "\n")

	var groups []coverageGroup
	if len(c.Clipped) > 0 {
		groups = append(groups, coverageGroup{"Included in part only (clipped to fit the context window)", c.Clipped})
	}
	groups = appendGroup(groups, reasonBudgetAdded, c.Omitted.Added)
	groups = appendGroup(groups, reasonBudgetMod, c.Omitted.Modified)
	groups = appendGroup(groups, reasonBudgetDel, c.Omitted.Deleted)
	groups = append(groups, byReason("Skipped", c.Skipped)...)
	groups = append(groups, byReason("Filtered", c.Filtered)...)

	left, unlisted := MaxListedFiles, 0
	for _, g := range groups {
		b.WriteString("\n" + g.title + " (" + strconv.Itoa(len(g.files)) + "):\n")
		shown := min(left, len(g.files))
		if shown > 0 {
			b.WriteString("\n")
		}
		for _, f := range g.files[:shown] {
			b.WriteString("- " + literal(f) + "\n")
		}
		left -= shown
		unlisted += len(g.files) - shown
	}
	if unlisted > 0 {
		b.WriteString("\nand " + strconv.Itoa(unlisted) + " more (not listed; at most " + strconv.Itoa(MaxListedFiles) + " files are listed)\n")
	}
}

func plural(n int) string {
	if n == 1 {
		return "file"
	}
	return "files"
}

func appendGroup(gs []coverageGroup, title string, files []string) []coverageGroup {
	if len(files) == 0 {
		return gs
	}
	return append(gs, coverageGroup{title, files})
}

// byReason groups skipped files by their reason, in order of first
// appearance. The machine reason token is shown in a code span.
func byReason(kind string, files []review.SkippedFile) []coverageGroup {
	var order []string
	byR := map[string][]string{}
	for _, f := range files {
		if _, ok := byR[f.Reason]; !ok {
			order = append(order, f.Reason)
		}
		byR[f.Reason] = append(byR[f.Reason], f.Path)
	}
	gs := make([]coverageGroup, 0, len(order))
	for _, r := range order {
		gs = append(gs, coverageGroup{kind + ": " + literal(r), byR[r]})
	}
	return gs
}

// writeNotes writes the notes section when there are notes.
func writeNotes(b *strings.Builder, heading string, notes []string) {
	if len(notes) == 0 {
		return
	}
	b.WriteString("\n" + heading + "\n\n")
	for _, n := range notes {
		b.WriteString("- " + mdutil.Inline(n) + "\n")
	}
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
