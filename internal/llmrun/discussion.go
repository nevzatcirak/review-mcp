package llmrun

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// The existing-discussion block of a prompt (spec P7 §5.2, X-13), shared by
// pr_review and pr_improve: the PR's threads without the comments a tool of
// ours wrote, rendered newest activity first within a token budget, inside
// a fence that no comment text can close. Each tool supplies the header
// above the fence (its only instruction) and the test of what is its own.
// It moved here from internal/review unchanged.

// Caps of the discussion block. They are constants, not configuration keys.
const (
	// discussionCommentRunes caps one comment body, in runes (spec P7 §5.2).
	discussionCommentRunes = 600
	// discussionReplies is the most replies shown after a thread's first
	// comment.
	discussionReplies = 2
	// discussionAuthorRunes and discussionPathRunes cap the one-line fields
	// of an entry.
	discussionAuthorRunes = 64
	discussionPathRunes   = 200
	// discussionCut is appended to a comment cut at discussionCommentRunes.
	discussionCut = "…"
)

// HumanThreads returns the threads that make up the discussion: every
// thread with the comments for which own reports false. A thread whose
// root is ours but which has other people's replies stays, without the root
// (the replies discuss our comment and may say it was fixed or wrong); a
// thread with nothing left is dropped. The input is not modified.
func HumanThreads(threads []provider.Thread, own func(c *provider.CommentItem) bool) []provider.Thread {
	var out []provider.Thread
	for i := range threads {
		t := threads[i]
		kept := make([]provider.CommentItem, 0, len(t.Comments))
		for j := range t.Comments {
			if !own(&t.Comments[j]) {
				kept = append(kept, t.Comments[j])
			}
		}
		if len(kept) == 0 {
			continue
		}
		t.Comments = kept
		out = append(out, t)
	}
	return out
}

// Discussion is the rendered prompt block and what it covers.
type Discussion struct {
	// Block is the header and the fenced threads; "" when no thread is shown.
	Block string
	// Included and Omitted count the threads shown and the ones left out by
	// the budget.
	Included, Omitted int
}

// RenderDiscussion renders the discussion block for the threads of
// HumanThreads within maxTokens (tokens.Estimate of the whole block, header
// and fences included, the estimator that clips the description). header is
// the tool's line above the fence. Threads are taken whole, newest activity
// first, until the next one does not fit; the ones taken keep their
// original order (general threads first, then inline by path and line).
// maxTokens <= 0 (the block is off) or no thread yields an empty
// discussion; when not even the newest thread fits, the block is empty and
// every thread counts as omitted.
func RenderDiscussion(header string, threads []provider.Thread, maxTokens int, factor float64) Discussion {
	if maxTokens <= 0 || len(threads) == 0 {
		return Discussion{}
	}
	order := make([]int, len(threads))
	for i := range order {
		order[i] = i
	}
	at := make([]time.Time, len(threads))
	for i := range threads {
		for _, c := range threads[i].Comments {
			if c.CreatedAt.After(at[i]) {
				at[i] = c.CreatedAt
			}
		}
	}
	// Newest activity first; among equal times the later thread counts as
	// newer, as in pr_comments.
	sort.SliceStable(order, func(a, b int) bool {
		ta, tb := at[order[a]], at[order[b]]
		if !ta.Equal(tb) {
			return ta.After(tb)
		}
		return order[a] > order[b]
	})
	entries := make([]string, len(threads))
	for i := range threads {
		entries[i] = threadEntry(&threads[i])
	}
	taken := make([]bool, len(threads))
	var block string
	n := 0
	for _, i := range order {
		taken[i] = true
		cand := discussionBlock(header, entries, taken)
		if tokens.Estimate(cand, factor) > maxTokens {
			break
		}
		block = cand
		n++
	}
	return Discussion{Block: block, Included: n, Omitted: len(threads) - n}
}

// discussionBlock joins the taken entries under the header, inside a fence
// that no comment text can close: mdutil.WriteFenced makes it one backtick
// longer than the longest backtick run anywhere in the entries.
func discussionBlock(header string, entries []string, taken []bool) string {
	var parts []string
	for i, e := range entries {
		if taken[i] {
			parts = append(parts, e)
		}
	}
	var b strings.Builder
	b.WriteString(header + "\n")
	mdutil.WriteFenced(&b, strings.Join(parts, "\n\n"), "", "")
	return strings.TrimRight(b.String(), "\n")
}

// threadEntry renders one thread: its location line, the first comment and
// up to discussionReplies replies as "author: text".
func threadEntry(t *provider.Thread) string {
	var b strings.Builder
	if t.Kind == provider.ThreadInline {
		b.WriteString("[inline")
		if p := CleanCommentLine(t.Path, discussionPathRunes); p != "" {
			b.WriteString(" " + p)
			if t.Line > 0 {
				b.WriteString(":" + strconv.Itoa(t.Line))
			}
		}
		b.WriteString("]")
	} else {
		b.WriteString("[general]")
	}
	if t.Resolved != nil && *t.Resolved {
		b.WriteString(" (resolved)")
	}
	b.WriteString("\n" + CleanCommentBody(t.Comments[0].Body))
	replies := t.Comments[1:]
	for i := 0; i < len(replies) && i < discussionReplies; i++ {
		author := CleanCommentLine(replies[i].Author, discussionAuthorRunes)
		if author == "" {
			author = "unknown"
		}
		b.WriteString("\n" + author + ": " + CleanCommentBody(replies[i].Body))
	}
	return b.String()
}

// CleanCommentBody sanitizes a comment body for a prompt, with the rules of
// pr_comments (P2e): invalid UTF-8 is replaced, carriage returns become
// line feeds and the text is cut after 600 runes with a "…". It also drops
// control characters other than line feed and tab, which carry no meaning
// for the model and could disturb a terminal or a log.
func CleanCommentBody(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	s = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > discussionCommentRunes {
		s = string(r[:discussionCommentRunes]) + discussionCut
	}
	return s
}

// CleanCommentLine is CleanCommentBody for a value that must stay on one
// line (an author or a path), cut at limit runes.
func CleanCommentLine(s string, limit int) string {
	s = strings.Join(strings.Fields(CleanCommentBody(s)), " ")
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit])
	}
	return s
}

// markerLastLinePrefix opens every marker line a tool of ours writes as the
// last line of a comment ("[//]: # (review-mcp:...)").
const markerLastLinePrefix = "[//]: # (review-mcp:"

// HasToolMarkerLastLine reports whether body's last line, ignoring trailing
// whitespace, is a marker line of any review-mcp tool: "[//]: # (review-mcp:"
// followed by anything and a closing ")". It recognises the comments of
// every tool, now and in later marker versions (pr_review's overview and
// finding markers, pr_describe's comment marker). It says nothing about the
// author: anyone can type a marker, so callers combine it with an author
// check.
func HasToolMarkerLastLine(body string) bool {
	body = strings.TrimRight(body, " \t\r\n")
	last := strings.TrimSpace(body[strings.LastIndexByte(body, '\n')+1:])
	return strings.HasPrefix(last, markerLastLinePrefix) && strings.HasSuffix(last, ")")
}
