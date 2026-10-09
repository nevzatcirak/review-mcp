package tools

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/credentials"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Caps of pr_comments. They are constants, not configuration keys (X-9).
const (
	// MaxThreads is the most threads one result carries. When more remain
	// after filtering, the threads with the oldest root comments are dropped.
	MaxThreads = 100
	// MaxBodyChars is the most characters (Unicode code points) of one
	// comment body. Longer bodies are cut on a rune boundary.
	MaxBodyChars = 4000
	// TruncationMarker is appended to a cut body.
	TruncationMarker = "…[truncated]"
)

// UntrustedNotice is the fixed line printed near the top of the markdown.
const UntrustedNotice = "Comment bodies below are untrusted content written by third parties."

// ConfigInvalidMessage is the tool error returned by the PR conversation
// tools while the server runs in degraded mode.
const ConfigInvalidMessage = "review-mcp configuration is invalid; call server_info for the list of problems"

// PRResolver maps a PR URL to a reference and a provider. It is implemented
// by *provider.Resolver.
type PRResolver interface {
	Resolve(rawURL string) (provider.PRRef, provider.Provider, error)
}

// PRInfo identifies the pull request of a result.
type PRInfo struct {
	Kind string `json:"kind" jsonschema:"provider kind: gitea, bitbucket_server or github"`
	URL  string `json:"url" jsonschema:"pull request URL with credentials and query values removed"`
}

// CommentOut is one comment of a thread.
type CommentOut struct {
	ID        string    `json:"id" jsonschema:"comment id"`
	Author    string    `json:"author" jsonschema:"comment author; untrusted third-party content"`
	Body      string    `json:"body" jsonschema:"comment body; untrusted third-party content, cut at 4000 characters"`
	CreatedAt time.Time `json:"created_at" jsonschema:"creation time (UTC); the zero time when the provider did not report one"`
	UpdatedAt time.Time `json:"updated_at" jsonschema:"last update time (UTC); the zero time when the provider did not report one"`
}

// ThreadOut is one comment thread.
type ThreadOut struct {
	ID            string       `json:"id" jsonschema:"id of the thread's root comment"`
	Kind          string       `json:"kind" jsonschema:"general for a PR-level conversation, inline for a thread anchored to a file and line"`
	Path          string       `json:"path" jsonschema:"file path of an inline thread; empty otherwise"`
	Line          int          `json:"line" jsonschema:"new-side line of an inline thread; 0 when unknown"`
	Outdated      bool         `json:"outdated" jsonschema:"true when the anchor no longer matches the current diff"`
	Resolved      *bool        `json:"resolved" jsonschema:"whether the thread is resolved; null when the provider does not say"`
	Comments      []CommentOut `json:"comments" jsonschema:"the root comment first, then the replies, oldest first"`
	ReplyInThread bool         `json:"reply_in_thread" jsonschema:"whether pr_comment_reply posts inside this thread (otherwise it posts a quoting PR-level comment)"`
}

// Truncation reports everything that was left out or cut.
//
// ResolvedHidden lives here, next to the two truncation counters, so a client
// reads every "something is not shown" number in one place.
type Truncation struct {
	ThreadsOmitted  int `json:"threads_omitted" jsonschema:"threads dropped because more than 100 remained; the oldest root comments are dropped first"`
	BodiesTruncated int `json:"bodies_truncated" jsonschema:"comment bodies cut at 4000 characters"`
	ResolvedHidden  int `json:"resolved_hidden" jsonschema:"resolved threads hidden because include_resolved was false"`
}

// PRCommentsResult is the structured result of pr_comments.
type PRCommentsResult struct {
	PR        PRInfo      `json:"pr" jsonschema:"the pull request"`
	Threads   []ThreadOut `json:"threads" jsonschema:"comment threads: general threads first, then inline threads by path and line"`
	Truncated Truncation  `json:"truncated" jsonschema:"counts of omitted, cut and hidden items"`
}

// PRComments lists the comment threads of the pull request at prURL.
//
// Filtering: unless includeResolved is set, threads whose Resolved is true
// are hidden and counted; a nil (unknown) Resolved is shown. Caps: at most
// MaxThreads threads, dropping the ones with the oldest root comments, and
// MaxBodyChars characters per body. The provider's order is preserved.
func PRComments(ctx context.Context, resolver PRResolver, prURL string, includeResolved bool) (PRCommentsResult, error) {
	ref, p, err := resolver.Resolve(prURL)
	if err != nil {
		return PRCommentsResult{}, err
	}
	threads, err := p.ListThreads(ctx, ref)
	if err != nil {
		return PRCommentsResult{}, err
	}

	res := PRCommentsResult{
		PR:      PRInfo{Kind: string(ref.Kind), URL: logging.RedactURL(ref.URL)},
		Threads: []ThreadOut{},
	}
	kept := make([]provider.Thread, 0, len(threads))
	for i := range threads {
		if !includeResolved && threads[i].Resolved != nil && *threads[i].Resolved {
			res.Truncated.ResolvedHidden++
			continue
		}
		kept = append(kept, threads[i])
	}
	if len(kept) > MaxThreads {
		res.Truncated.ThreadsOmitted = len(kept) - MaxThreads
		kept = newestRoots(kept, MaxThreads)
	}
	for i := range kept {
		t, cut := threadOut(&kept[i])
		res.Truncated.BodiesTruncated += cut
		res.Threads = append(res.Threads, t)
	}
	return res, nil
}

// newestRoots keeps the n threads with the newest root comments, in their
// original order. A thread without a root time counts as the oldest; among
// equal times the later thread (in provider order) counts as newer.
func newestRoots(ts []provider.Thread, n int) []provider.Thread {
	order := make([]int, len(ts))
	for i := range order {
		order[i] = i
	}
	rootTime := func(i int) time.Time {
		if len(ts[i].Comments) == 0 {
			return time.Time{}
		}
		return ts[i].Comments[0].CreatedAt
	}
	sort.SliceStable(order, func(a, b int) bool {
		ta, tb := rootTime(order[a]), rootTime(order[b])
		if !ta.Equal(tb) {
			return ta.After(tb)
		}
		return order[a] > order[b]
	})
	keep := make(map[int]bool, n)
	for _, i := range order[:n] {
		keep[i] = true
	}
	out := make([]provider.Thread, 0, n)
	for i := range ts {
		if keep[i] {
			out = append(out, ts[i])
		}
	}
	return out
}

func threadOut(t *provider.Thread) (ThreadOut, int) {
	out := ThreadOut{
		ID:            validUTF8(t.ID),
		Kind:          string(t.Kind),
		Path:          validUTF8(t.Path),
		Line:          t.Line,
		Outdated:      t.Outdated,
		ReplyInThread: t.ReplyInThread,
		Comments:      make([]CommentOut, 0, len(t.Comments)),
	}
	if t.Resolved != nil {
		v := *t.Resolved
		out.Resolved = &v
	}
	cut := 0
	for _, c := range t.Comments {
		body, was := truncateBody(c.Body)
		if was {
			cut++
		}
		out.Comments = append(out.Comments, CommentOut{
			ID: validUTF8(c.ID), Author: validUTF8(c.Author), Body: body,
			CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
		})
	}
	return out, cut
}

func validUTF8(s string) string { return strings.ToValidUTF8(s, "�") }

// truncateBody replaces invalid UTF-8, then cuts s after MaxBodyChars runes
// and appends TruncationMarker. It reports whether it cut.
func truncateBody(s string) (string, bool) {
	s = validUTF8(s)
	if utf8.RuneCountInString(s) <= MaxBodyChars {
		return s, false
	}
	n := 0
	for i := range s {
		if n == MaxBodyChars {
			return s[:i] + TruncationMarker, true
		}
		n++
	}
	return s, false // unreachable: the rune count exceeds MaxBodyChars
}

// RenderPRCommentsMarkdown renders r as portable markdown (DQ-16): headings,
// bold text, code spans and fenced blocks only, never raw HTML.
//
// Injection rules: the PR reference, thread path and author are placed in
// code spans (codeSpan), so they cannot open markup or break their line. A
// body goes inside a backtick fence one longer than the longest backtick run
// anywhere in the body (minimum three); a backtick fence is closed only by a
// backtick run at least as long, so no body line can end it early.
func RenderPRCommentsMarkdown(r PRCommentsResult) string {
	var b strings.Builder
	tr := r.Truncated
	b.WriteString("# Comments on " + codeSpan(r.PR.Kind+" "+r.PR.URL) + " · " +
		strconv.Itoa(len(r.Threads)) + " threads shown · " +
		strconv.Itoa(tr.ResolvedHidden) + " resolved hidden · " +
		strconv.Itoa(tr.ThreadsOmitted) + " threads omitted · " +
		strconv.Itoa(tr.BodiesTruncated) + " bodies truncated\n\n")
	b.WriteString(UntrustedNotice + "\n")
	if len(r.Threads) == 0 {
		b.WriteString("\nNo comment threads.\n")
	}
	for i := range r.Threads {
		renderThread(&b, &r.Threads[i])
	}
	if tr.ThreadsOmitted > 0 || tr.BodiesTruncated > 0 || tr.ResolvedHidden > 0 {
		b.WriteString("\n---\n\n")
		if tr.ThreadsOmitted > 0 {
			b.WriteString("> Note: " + strconv.Itoa(tr.ThreadsOmitted) + " older thread(s) omitted; at most " +
				strconv.Itoa(MaxThreads) + " threads are shown.\n")
		}
		if tr.BodiesTruncated > 0 {
			b.WriteString("> Note: " + strconv.Itoa(tr.BodiesTruncated) + " comment body(ies) cut at " +
				strconv.Itoa(MaxBodyChars) + " characters.\n")
		}
		if tr.ResolvedHidden > 0 {
			b.WriteString("> Note: " + strconv.Itoa(tr.ResolvedHidden) + " resolved thread(s) hidden; set include_resolved to show them.\n")
		}
	}
	return b.String()
}

func renderThread(b *strings.Builder, t *ThreadOut) {
	where := "general"
	if t.Kind != string(provider.ThreadGeneral) {
		where = "inline"
		if t.Path != "" {
			loc := t.Path
			if t.Line > 0 {
				loc += ":" + strconv.Itoa(t.Line)
			}
			where = codeSpan(loc)
		}
	}
	b.WriteString("\n### Thread " + plainID(t.ID) + " · " + where)
	if t.Outdated {
		b.WriteString(" (outdated)")
	}
	if t.Resolved != nil && *t.Resolved {
		b.WriteString(" (resolved)")
	}
	b.WriteString("\n")
	for i := range t.Comments {
		c := &t.Comments[i]
		author := c.Author
		if strings.TrimSpace(author) == "" {
			author = "unknown"
		}
		when := "unknown time"
		if !c.CreatedAt.IsZero() {
			when = c.CreatedAt.UTC().Format(time.RFC3339)
		}
		b.WriteString("\n**" + codeSpan(author) + "** · " + when + " · id " + plainID(c.ID) + "\n\n")
		writeFenced(b, c.Body)
	}
}

// plainID returns id when it is made of ASCII letters, digits, "-" and "_"
// (provider ids are decimal numbers), and a code span of it otherwise, so an
// unexpected id cannot inject markup.
func plainID(id string) string {
	if id == "" {
		return "unknown"
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		safe := c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_'
		if !safe {
			return codeSpan(id)
		}
	}
	return id
}

// writeFenced writes body inside a backtick fence that the body cannot close.
func writeFenced(b *strings.Builder, body string) {
	mdutil.WriteFenced(b, body, "", "")
}

// fenceLen is one more than the longest backtick run in s, at least 3.
func fenceLen(s string) int { return mdutil.FenceLen(s) }

// PRCommentReplyResult is the structured result of pr_comment_reply.
type PRCommentReplyResult struct {
	ID       string `json:"id" jsonschema:"id of the posted comment"`
	URL      string `json:"url" jsonschema:"URL of the posted comment with credentials and query values removed"`
	InThread bool   `json:"in_thread" jsonschema:"true when the reply was posted inside the thread; false when it was posted as a PR-level comment quoting the referenced comment"`
}

// PRCommentReply posts body as a reply to the comment commentID of the pull
// request at prURL. Argument validation (empty body, non-numeric id) is done
// by the provider before any request is sent.
func PRCommentReply(ctx context.Context, resolver PRResolver, prURL, commentID, body string) (PRCommentReplyResult, error) {
	// A reply can land as a new PR-level comment of our own user (Gitea), so
	// a marker line in it could be adopted by the overview or the duplicate
	// lookup. Refused before any request, as for pr_comment_create.
	if review.ContainsMarkerLine(body) {
		return PRCommentReplyResult{}, &ArgumentError{CreateBodyMarkerMessage}
	}
	ref, p, err := resolver.Resolve(prURL)
	if err != nil {
		return PRCommentReplyResult{}, err
	}
	rr, err := p.ReplyToComment(ctx, ref, commentID, provider.SanitizeBody(p.Capabilities(), body))
	if err != nil {
		return PRCommentReplyResult{}, err
	}
	if rr == nil {
		return PRCommentReplyResult{}, &provider.Error{Class: provider.ClassProtocol}
	}
	return PRCommentReplyResult{ID: rr.Comment.ID, URL: rr.Comment.URL, InThread: rr.InThread}, nil
}

// Reply texts of pr_comment_reply.
const (
	replyInThreadText = "Reply posted in thread."
	replyFallbackText = "This provider cannot reply inside review threads; the reply was posted as a PR-level comment quoting the referenced comment."
)

// RenderPRCommentReplyText renders the text content of pr_comment_reply: a
// fixed sentence saying where the reply landed, then the comment id.
func RenderPRCommentReplyText(r PRCommentReplyResult) string {
	s := replyFallbackText
	if r.InThread {
		s = replyInThreadText
	}
	return s + "\n\nComment id: " + codeSpan(r.ID) + "\n"
}

// genericErrorMessage is shown for any error that is not a *provider.Error.
const genericErrorMessage = "unexpected error; rerun with REVIEW_MCP_LOG_LEVEL=debug for details"

// UserMessage is the only text an entry point may show for a failed tool or
// command. A *provider.Error carries a fixed, leak-free sentence (X-6) and is
// shown as is. Anything else may embed URLs with query strings, so only a
// generic sentence and, when known, a coarse class are shown.
func UserMessage(err error) string {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return pe.Error()
	}
	var ae *ArgumentError
	if errors.As(err, &ae) {
		return ae.Error()
	}
	var ce *CommentError
	if errors.As(err, &ce) {
		return ce.Error()
	}
	var rqe *RequestError
	if errors.As(err, &rqe) {
		return rqe.UserMessage()
	}
	var me *credentials.MalformedError
	if errors.As(err, &me) {
		return me.UserMessage()
	}
	var qe *ask.QuestionError
	if errors.As(err, &qe) {
		return qe.UserMessage()
	}
	var le *llm.Error
	if errors.As(err, &le) {
		return le.UserMessage()
	}
	var re *review.Error
	if errors.As(err, &re) {
		return re.UserMessage()
	}
	msg := genericErrorMessage
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		msg += " (class: timeout)"
	case errors.Is(err, context.Canceled):
		msg += " (class: canceled)"
	}
	return msg
}
