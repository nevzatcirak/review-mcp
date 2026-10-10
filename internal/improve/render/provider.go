package render

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/improve"
	llmrender "github.com/nevzatcirak/review-mcp/internal/llmrun/render"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Emojis of the GFM provider profile (the same as pr_review's, pr_ask's and
// pr_describe's).
const (
	emojiTitle    = "💡"
	emojiCoverage = "📂"
	emojiNotes    = "📝"
	emojiWarning  = "⚠️"
)

// Fixed English texts of the published renderings.
const (
	TextOverview = "Code Suggestions"
	// TextListedHere heads the suggestions that have no inline comment.
	TextListedHere = "Suggestions listed here only"
	// Status texts of the overview table's last column.
	TextStatusPosted    = "inline comment posted"
	TextStatusDuplicate = "already posted on this PR"
	TextStatusHere      = "listed here only"
)

// Overview renders the overview comment of a result for a provider with the
// given capabilities (DQ-16); it has the improve.OverviewRenderer signature.
// The pipeline adds the marker.
//
// Layout: the heading, the X-18 banner of a partial result (a warning
// blockquote with caps.GFM), the table of the suggestions (label, file with
// a link to the line, summary, score or "unscored", and the status: what the
// verification and the inline post made of it, the "not anchored" marks of
// Y-10 included; a list instead of a table where caps.MarkdownTables is
// off), the full text of the suggestions that have no inline comment, the
// coverage section ("Reviewed in N model calls." for a run in parts) and the
// notes.
//
// A verified suggestion links to its inline comment when one was posted and
// otherwise to its first line at the PR's head (link). A link is used only
// when it is an absolute http(s) URL.
//
// Escaping follows pr_describe's provider rendering: the label, the summary
// and the file are model-authored one-liners escaped with mdutil.Inline (and,
// with caps.GFM, HTML metacharacters as entities, which render as the
// literal characters in a table cell too); the suggestion text is escaped
// per caps.GFM like a summary of pr_describe (EscapeBullets); code stays
// inside fences that it cannot close. Nothing here is raw HTML.
func Overview(res *improve.Result, caps provider.Capabilities, link func(path string, line int) string) string {
	if res == nil {
		return ""
	}
	esc := mdutil.Escape
	head, covHead, notesHead := "## "+TextOverview, "### "+llmrender.TextCoverage, "### "+llmrender.TextNotes
	if caps.GFM {
		esc = mdutil.EscapeGFM
		head += " " + emojiTitle
		covHead = "### " + emojiCoverage + " " + llmrender.TextCoverage
		notesHead = "### " + emojiNotes + " " + llmrender.TextNotes
	}
	var b strings.Builder
	b.WriteString(head + "\n\n")
	if banner := llmrender.PartialBanner(&res.Coverage, false); banner != "" {
		if caps.GFM {
			banner = "> " + emojiWarning + " " + banner
		}
		b.WriteString(banner + "\n\n")
	}
	if len(res.Suggestions) == 0 {
		b.WriteString(textNoSuggestions + "\n")
	} else if caps.MarkdownTables {
		b.WriteString("| # | Label | File | Summary | Score | Status |\n|---|---|---|---|---|---|\n")
		for i := range res.Suggestions {
			s := &res.Suggestions[i]
			b.WriteString("| " + strconv.Itoa(i+1) + " | " + labelCell(s) + " | " + fileCell(s, link) + " | " +
				oneLine(s.Summary, esc) + " | " + scoreText(s) + " | " + statusText(s) + " |\n")
		}
	} else {
		for i := range res.Suggestions {
			s := &res.Suggestions[i]
			b.WriteString(strconv.Itoa(i+1) + ". " + oneLine(s.Summary, esc) + " (" + joinNonEmpty(", ", labelCell(s),
				fileCell(s, link), "score: "+scoreText(s), statusText(s)) + ")\n")
		}
	}

	first := true
	for i := range res.Suggestions {
		s := &res.Suggestions[i]
		if !listedHereOnly(s) {
			continue
		}
		if first {
			b.WriteString("\n### " + TextListedHere + "\n")
			first = false
		}
		b.WriteString("\n#### " + strconv.Itoa(i+1) + ". " + oneLine(s.Summary, esc) + "\n\n")
		b.WriteString(suggestionBody(s, caps, provider.SuggestionStyleNone, false))
	}

	llmrender.Coverage(&b, covHead, &res.Coverage)
	llmrender.Notes(&b, notesHead, res.Notes)
	return b.String()
}

// listedHereOnly reports a suggestion whose text the overview must carry:
// it has no inline comment on the PR (not verified, not anchorable, failed),
// or nothing was posted at all.
func listedHereOnly(s *improve.Suggestion) bool {
	return s.Anchor == nil || (s.Anchor.Status != improve.AnchorPosted && s.Anchor.Status != improve.AnchorSkippedDuplicate)
}

// labelCell is the label as a one-line cell, or "-" when there is none.
func labelCell(s *improve.Suggestion) string {
	if l := strings.TrimSpace(s.Label); l != "" {
		return mdutil.Inline(l)
	}
	return "-"
}

// fileCell is the file with its line range, linked when a link is known.
func fileCell(s *improve.Suggestion, link func(string, int) string) string {
	text := mdutil.Literal(s.File)
	if s.StartLine != nil && s.EndLine != nil {
		text += " " + lineRange(*s.StartLine, *s.EndLine)
	}
	if !s.Verified {
		return text
	}
	dest := ""
	if s.Anchor != nil && s.Anchor.Status == improve.AnchorPosted {
		dest = safeLink(s.Anchor.URL)
	}
	if dest == "" && link != nil && s.StartLine != nil {
		dest = safeLink(link(s.File, *s.StartLine))
	}
	if dest == "" {
		return text
	}
	return "[" + text + "](" + dest + ")"
}

func lineRange(start, end int) string {
	if end <= start {
		return "L" + strconv.Itoa(start)
	}
	return "L" + strconv.Itoa(start) + "-" + strconv.Itoa(end)
}

func scoreText(s *improve.Suggestion) string {
	if s.Score == nil {
		return TextUnscored
	}
	return strconv.Itoa(*s.Score) + "/" + strconv.Itoa(improve.MaxScore)
}

// statusText is what the verification and the inline post made of s: the
// "not anchored" marks of Y-10 for an unverified one.
func statusText(s *improve.Suggestion) string {
	if !s.Verified {
		if s.UnverifiedReason == improve.UnverifiedHeadUnavailable {
			return TextNotAnchoredNoHead
		}
		return TextNotAnchored
	}
	if s.Anchor != nil {
		switch s.Anchor.Status {
		case improve.AnchorPosted:
			return TextStatusPosted
		case improve.AnchorSkippedDuplicate:
			return TextStatusDuplicate
		}
		return TextStatusHere
	}
	return TextLinesVerified
}

// oneLine folds s to one line and escapes it with esc.
func oneLine(s string, esc func(string) string) string {
	return esc(strings.Join(strings.Fields(s), " "))
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// SuggestionBlock renders improved as a native suggestion block in style
// (provider.Capabilities.SuggestionStyle) that replaces `lines` lines (at
// least 1):
//
//   - provider.SuggestionStyleRange: a fence with the info string
//     "suggestion"; the lines it replaces are the comment's range, so lines
//     does not appear in it;
//   - provider.SuggestionStyleOffset: "suggestion:-0+N" for a block of
//     several lines (the comment's line and the N = lines-1 lines below
//     it), plain "suggestion" for one line.
//
// The fence is adaptive: longer than any run of backticks in improved. For
// a block of one line both styles are the same.
func SuggestionBlock(style provider.SuggestionStyle, improved string, lines int) string {
	info := "suggestion"
	if style == provider.SuggestionStyleOffset && lines > 1 {
		info += ":-0+" + strconv.Itoa(lines-1)
	}
	var b strings.Builder
	mdutil.WriteFenced(&b, improved, info, "")
	return b.String()
}

// Inline renders the body of a suggestion's inline comment; it has the
// improve.InlineRenderer signature. The pipeline adds the fingerprint marker
// and slash-sanitises the body.
//
// The body is the bold summary, a line with the label and the score, the
// suggestion text and then the change:
//
//   - caps.NativeSuggestionStyle() set: a native suggestion block holding
//     the improved code in that style (SuggestionBlock), which replaces the
//     verified range. The pipeline sets the style only when the block is
//     safe, and then passes the improved code re-indented to the real lines
//     (improve.InlineRenderer);
//   - otherwise: a fenced "diff" block that removes the existing code and
//     adds the improved code.
//
// The summary and the label are escaped like the overview's, the text per
// caps.GFM, the code stays inside adaptive fences.
func Inline(s *improve.Suggestion, caps provider.Capabilities) string {
	if s == nil {
		return ""
	}
	esc := mdutil.Escape
	if caps.GFM {
		esc = mdutil.EscapeGFM
	}
	return "**" + oneLine(s.Summary, esc) + "**\n\n" + suggestionBody(s, caps, caps.NativeSuggestionStyle(), true)
}

// suggestionBody is the label and score line, the text and the change of s.
// inline selects the inline comment's form (the summary is the caller's);
// native is the suggestion style of its block, none for the diff block.
func suggestionBody(s *improve.Suggestion, caps provider.Capabilities, native provider.SuggestionStyle, inline bool) string {
	esc := mdutil.Escape
	if caps.GFM {
		esc = mdutil.EscapeGFM
	}
	var b strings.Builder
	var meta []string
	if l := strings.TrimSpace(s.Label); l != "" {
		meta = append(meta, "Label: "+mdutil.Inline(l))
	}
	if s.Score != nil {
		meta = append(meta, "Score: "+strconv.Itoa(*s.Score)+" of "+strconv.Itoa(improve.MaxScore))
	} else {
		meta = append(meta, "Score: "+TextUnscored)
	}
	if !inline {
		meta = append([]string{"File: " + mdutil.Literal(s.File) + rangeSuffix(s)}, meta...)
	}
	b.WriteString(strings.Join(meta, " · ") + "\n")
	if !inline && s.Anchor != nil && s.Anchor.Status == improve.AnchorFailed {
		b.WriteString("\nThe inline comment could not be posted.\n")
	}
	if c := strings.TrimSpace(s.Content); c != "" {
		b.WriteString("\n" + mdutil.EscapeBullets(c, esc) + "\n")
	}
	b.WriteString("\n")
	lang := fenceInfo(s.Language)
	switch {
	case native != provider.SuggestionStyleNone:
		lines := 1
		if s.StartLine != nil && s.EndLine != nil && *s.EndLine > *s.StartLine {
			lines = *s.EndLine - *s.StartLine + 1
		}
		b.WriteString(SuggestionBlock(native, trimCode(s.ImprovedCode), lines))
	case inline:
		mdutil.WriteFenced(&b, diffBlock(s.ExistingCode, s.ImprovedCode), "diff", "")
	default:
		b.WriteString(TextExisting + "\n\n")
		mdutil.WriteFenced(&b, s.ExistingCode, lang, "")
		b.WriteString("\n" + TextImproved + "\n\n")
		mdutil.WriteFenced(&b, s.ImprovedCode, lang, "")
	}
	return b.String()
}

func rangeSuffix(s *improve.Suggestion) string {
	if s.StartLine == nil || s.EndLine == nil {
		return ""
	}
	return " " + lineRange(*s.StartLine, *s.EndLine)
}

// trimCode drops the line ending of code, so that a fence adds exactly one.
func trimCode(code string) string {
	return strings.TrimSuffix(strings.ReplaceAll(code, "\r\n", "\n"), "\n")
}

// diffBlock is the body of a "diff" fence: every line of the existing code
// with a leading "-", then every line of the improved code with a leading
// "+".
func diffBlock(existing, improved string) string {
	var b strings.Builder
	for _, l := range strings.Split(trimCode(existing), "\n") {
		b.WriteString("-" + l + "\n")
	}
	for _, l := range strings.Split(trimCode(improved), "\n") {
		b.WriteString("+" + l + "\n")
	}
	return b.String()
}

var linkEscaper = strings.NewReplacer(
	"(", "%28", ")", "%29", "<", "%3C", ">", "%3E", "'", "%27", `"`, "%22", "`", "%60", `\`, "%5C", "|", "%7C",
)

// safeLink returns link as a URL that can sit in a markdown link
// destination, or "" when it is not an absolute http(s) URL.
func safeLink(link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return linkEscaper.Replace(u.String())
}
