// Package provider defines the provider-neutral types, the Provider
// interface, the sanitized error classes (X-6) and the PR-URL Resolver (X-2).
//
// Only provider implementations and internal/provider/httpx perform network
// I/O; this package never does.
package provider

import (
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kind identifies a provider implementation. The string values are shared
// with server_info and must not change.
type Kind string

const (
	// KindGitea is the Gitea provider.
	KindGitea Kind = "gitea"
	// KindBitbucketServer is the Bitbucket Server / Data Center provider.
	KindBitbucketServer Kind = "bitbucket_server"
	// KindGitHub is the GitHub provider (github.com and GitHub Enterprise
	// Server).
	KindGitHub Kind = "github"
	// KindGitLab is the GitLab provider (gitlab.com and self-managed GitLab).
	KindGitLab Kind = "gitlab"
)

// PRRef identifies one pull request.
//
// Namespace is the Gitea or GitHub owner, the Bitbucket project key
// ("~user" for personal repositories) or the GitLab namespace. It may
// contain "/" (nested groups, such as "group/sub/team" on GitLab); each
// Factory.ParsePRPath decides how many URL segments are namespace: Gitea,
// Bitbucket Server and GitHub accept exactly one, GitLab one or more. Whatever builds a URL, a cache path or a log field from it escapes it
// per segment (EscapeNamespace), never as one string. URL is the
// user-supplied PR URL; it may only be logged through logging.RedactURL.
type PRRef struct {
	Kind      Kind
	Namespace string
	Repo      string
	Number    int64
	URL       string
}

// EscapeNamespace escapes a namespace for use in a URL path: every segment
// between "/" is escaped with url.PathEscape and the segments are joined
// with "/". A namespace without "/" is escaped exactly as url.PathEscape
// does.
func EscapeNamespace(ns string) string {
	segs := strings.Split(ns, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// PullRequest is the provider-neutral PR metadata. BaseSHA is the revision
// the diff was computed against.
type PullRequest struct {
	Title, Description, Author, SourceBranch, TargetBranch, HeadSHA, BaseSHA, WebURL string
	State                                                                            string
	// BaseStrategy says how BaseSHA was chosen. It is one of BaseGiteaMergeBase,
	// BaseGiteaBaseSHA, BaseBBSMergeBaseEP, BaseBBSAncestorWalk,
	// BaseGitHubMergeBase, BaseGitHubBaseSHA and BaseGitLabDiffRefs. GetDiff
	// uses BaseSHA and BaseStrategy as given.
	BaseStrategy string
	// StartSHA is the revision the host's own diff starts from when the
	// host reports one next to BaseSHA (GitLab's diff_refs.start_sha, the
	// target branch's head when the diff was computed); "" otherwise. It is
	// kept for the positions of inline comments.
	StartSHA string

	// The fields below feed pr_info (X-23). They carry states and identities
	// only, never a comment or review body.

	// Draft is nil when the provider does not report a draft flag.
	Draft *bool
	// Merged is true when the provider says the PR was merged (Gitea reports
	// it next to State; Bitbucket Server uses State "MERGED").
	Merged bool
	// Mergeable is the provider's own verdict carried by the PR itself
	// (Gitea, GitHub); nil when the PR payload has none.
	Mergeable *bool
	// MergeableState is the provider's structured merge state where it has
	// one (GitHub's mergeable_state, such as "clean", "dirty" or
	// "blocked"); "" otherwise. It is an enum value for mapping to fixed
	// texts and is never shown as it is.
	MergeableState string
	// ChangedFiles is the number of changed files the provider reports for
	// the PR itself (GitHub's changed_files); 0 when it reports none. GetDiff
	// compares it with the files the host lists to tell when the listing
	// was cut short.
	ChangedFiles int
	// ChangedFilesAtLeast is true when ChangedFiles is only a lower bound:
	// the host stopped counting there (GitLab's changes_count "1000+", its
	// diff limit), so the files it lists are not all the changed files.
	ChangedFilesAtLeast bool
	// CommitCount is the total number of commits the provider reports for
	// the PR itself (GitHub's commits). 0 means unknown: providers that know
	// the total set it, the others leave it 0.
	CommitCount int

	// Version is the provider's optimistic-locking token for the PR, in
	// decimal; "" when the provider has none (Gitea). Bitbucket Server
	// requires it back in UpdatePR.Version and answers a stale one with a
	// conflict. It is filled by GetPullRequest.
	Version string
}

// UpdatePR is the request of Provider.UpdatePullRequest. A nil Title or
// Description leaves that field as it is; an empty (non-nil) Description
// clears the description.
type UpdatePR struct {
	Title       *string
	Description *string
	// Version is PullRequest.Version of the read the update is based on.
	// Providers that have a version require it (see ValidateUpdatePR).
	Version string
}

// ChangeType is the kind of change applied to a file.
type ChangeType string

// Change types. A rename with content changes is ChangeRenamed and still
// carries hunks.
const (
	ChangeAdded    ChangeType = "added"
	ChangeModified ChangeType = "modified"
	ChangeDeleted  ChangeType = "deleted"
	ChangeRenamed  ChangeType = "renamed"
)

// ContentStatus describes whether a file side's full content was fetched.
type ContentStatus string

// Content statuses. ContentNotApplicable covers a deleted head side and an
// added base side.
const (
	ContentFull              ContentStatus = "full"
	ContentNotFetchedFileCap ContentStatus = "not_fetched_file_limit"
	ContentNotFetchedSizeCap ContentStatus = "not_fetched_size_limit"
	ContentFetchFailed       ContentStatus = "fetch_failed"
	ContentNotApplicable     ContentStatus = "not_applicable"
)

// FilePatch is one changed file.
type FilePatch struct {
	// Path is the new path; for a deletion, the old path.
	Path string
	// OldPath is set only for a rename.
	OldPath string
	Type    ChangeType
	// Patch is a hunk-only unified diff: it starts at the first "@@" line and
	// has no diff --git, index, --- or +++ lines. The contents stay
	// byte-exact. The "\ No newline at end of file" marker is present only
	// when the host provides it (a provider that builds patches from file
	// contents omits it); consumers must not rely on it.
	Patch     string
	Additions int
	Deletions int
	Binary    bool
	// BaseContent and HeadContent are nil when not fetched.
	BaseContent, HeadContent *string
	BaseStatus, HeadStatus   ContentStatus
}

// Skipped-file reasons.
const (
	SkipBinary      = "binary"
	SkipFileLimit   = "file_limit"
	SkipSizeLimit   = "size_limit"
	SkipFetchFailed = "fetch_failed"
	SkipFiltered    = "filtered"
)

// SkippedFile is a file that is deliberately not part of Diff.Files.
type SkippedFile struct {
	Path   string
	Reason string
}

// Base strategies reported in PullRequest.BaseStrategy and Diff.BaseStrategy.
const (
	BaseGiteaMergeBase  = "gitea:merge_base"
	BaseGiteaBaseSHA    = "gitea:base_sha"
	BaseBBSMergeBaseEP  = "bbs:merge_base_endpoint"
	BaseBBSAncestorWalk = "bbs:ancestor_walk"
	// BaseGitHubMergeBase: the merge base GitHub's compare endpoint reports.
	BaseGitHubMergeBase = "github:merge_base"
	// BaseGitHubBaseSHA: the target branch's revision recorded on the pull
	// request, used when the compare endpoint could not be read.
	BaseGitHubBaseSHA = "github:base_sha"
	// BaseGitLabDiffRefs: the base_sha of the merge request's diff_refs,
	// the revision GitLab computed the merge request's diff against.
	BaseGitLabDiffRefs = "gitlab:diff_refs"
)

// Diff is the result of Provider.GetDiff.
type Diff struct {
	Files   []FilePatch
	Skipped []SkippedFile
	// BaseStrategy is copied from PullRequest.BaseStrategy.
	BaseStrategy string
	// Notes are fixed sentences about changed files the host did not list
	// at all, so that they appear neither in Files nor in Skipped (GitHub
	// lists at most 3000 files of a pull request; GitLab stops at its diff
	// limit). Nil for a complete listing.
	Notes []string
	// Incomplete is true when the host stopped listing changed files, so
	// that some files are in neither Files nor Skipped; Notes then says so.
	// Coverage counts such a diff as partial. False for a complete listing.
	Incomplete bool
}

// DiffOptions tunes GetDiff. When Include is non-nil, a file for which it
// returns false goes to Skipped with reason "filtered" before any content is
// fetched.
type DiffOptions struct {
	Include func(path string) bool
}

// Comment is a posted PR comment.
type Comment struct {
	ID  string
	URL string
}

// Capabilities describes what a provider's markup and API support (DQ-16,
// Y-3). Code branches on these flags, never on the provider's Kind.
type Capabilities struct {
	GFM, MarkdownTables, Labels, InlineComments bool
	// SuggestionBlocks: an inline comment can carry a native suggestion block.
	// It is true exactly when SuggestionStyle is not SuggestionStyleNone (the
	// contract suite checks this); code that renders a block reads the
	// syntax through NativeSuggestionStyle.
	SuggestionBlocks bool
	// SuggestionStyle is the syntax of the native suggestion block, and
	// with it which lines the block replaces; SuggestionStyleNone without
	// SuggestionBlocks.
	SuggestionStyle SuggestionStyle
	// QuickActions: a published line that starts with "/" runs a quick
	// action, so every published body is slash-sanitised (SanitizeBody).
	QuickActions bool
	// InlineThreadResolution: the provider reports whether an inline thread
	// is resolved.
	InlineThreadResolution bool
	// GeneralThreadResolution: the provider reports whether a general
	// (PR-level) thread is resolved.
	GeneralThreadResolution bool
	// DescriptionEdit: the PR description can be updated through the API.
	DescriptionEdit bool
}

// NativeSuggestionStyle returns the suggestion style an inline comment may
// use: SuggestionStyle when SuggestionBlocks is set, and SuggestionStyleNone
// otherwise (no native block, whatever SuggestionStyle says).
func (c Capabilities) NativeSuggestionStyle() SuggestionStyle {
	if !c.SuggestionBlocks {
		return SuggestionStyleNone
	}
	return c.SuggestionStyle
}

// SuggestionStyle is the syntax of a provider's native suggestion block
// (design §5, the suggestion-block precondition). The values name the
// syntax, never a provider, so that code branches on the capability.
type SuggestionStyle string

// Suggestion styles.
const (
	// SuggestionStyleNone: the provider has no native suggestion block.
	SuggestionStyleNone SuggestionStyle = ""
	// SuggestionStyleRange: a fence with the info string "suggestion" that
	// replaces every line the comment is attached to, InlineComment.Line to
	// InlineComment.EndLine (GitHub's start_line to line).
	SuggestionStyleRange SuggestionStyle = "range"
	// SuggestionStyleOffset: the comment sits on one line, and the info
	// string "suggestion:-0+N" says the block replaces that line and the N
	// lines below it (GitLab).
	SuggestionStyleOffset SuggestionStyle = "offset"
)

// SanitizeBody returns body ready to be published for a provider with caps.
// When caps.QuickActions is set it puts a space in front of every line that
// starts with "/", so that no published line triggers a quick action
// (upstream's answer sanitization, pr_questions.py _prepare_pr_answer @
// 8e5a929). Lines are split at "\n" and at "\r"; a body that starts with "/"
// is covered too. Without QuickActions body is returned unchanged.
//
// One refinement keeps native suggestion blocks exact (X-35): inside a
// fenced code block whose info string is "suggestion" or
// "suggestion:-N+M" (a fence of three or more backticks or tildes after at
// most three spaces, closed by a fence of the same character that is at
// least as long), a line gets the space only when it could be a quick
// action (QuickActionLine: "/" followed by a letter, a digit or "_"). A
// line such as "// comment" or "/* ... */" is left as it is there. Outside
// such blocks every line that starts with "/" gets the space, exactly as
// SanitizeQuickActions does. The refinement only ever spares a line that
// cannot run a quick action, so it does not depend on how the host parses
// code blocks.
//
// It is the one place where published bodies are sanitised: pr_review (the
// overview and the inline comments), pr_comment_create, pr_comment_reply,
// pr_ask and pr_describe all publish through it.
func SanitizeBody(caps Capabilities, body string) string {
	if !caps.QuickActions {
		return body
	}
	return sanitizeSuggestionAware(body)
}

// QuickActionLine reports whether line could run a quick action: it starts
// with "/" followed by a letter, a digit or "_". A line such as "//", "/*"
// or "/" alone cannot. It is the rule SanitizeBody applies inside a
// suggestion block, and the test a suggestion's code must pass before it is
// published as a native block on a provider with QuickActions.
func QuickActionLine(line string) bool {
	if len(line) < 2 || line[0] != '/' {
		return false
	}
	r, _ := utf8.DecodeRuneInString(line[1:])
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// HasQuickActionLine reports whether any line of s (split at "\r\n", "\r"
// and "\n") is a QuickActionLine.
func HasQuickActionLine(s string) bool {
	for _, l := range splitLines(s) {
		if QuickActionLine(l.text) {
			return true
		}
	}
	return false
}

// mdLine is one line of a body and the line ending that follows it ("" for
// the last line).
type mdLine struct{ text, end string }

// splitLines splits s into lines at "\r\n", "\r" and "\n", the line
// endings of CommonMark. Joining text and end of every line gives s back.
func splitLines(s string) []mdLine {
	var out []mdLine
	for {
		i := strings.IndexAny(s, "\r\n")
		if i < 0 {
			return append(out, mdLine{text: s})
		}
		end := s[i : i+1]
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			end = "\r\n"
		}
		out = append(out, mdLine{text: s[:i], end: end})
		s = s[i+len(end):]
	}
}

// codeFence is an open fenced code block: its fence character and length,
// and whether its info string makes it a suggestion block.
type codeFence struct {
	char       byte
	n          int
	suggestion bool
}

// fenceRun returns the fence character and the length of the run of it
// that starts line after at most three spaces, and the rest of the line;
// n is 0 when the line does not start with a run of three or more
// backticks or tildes.
func fenceRun(line string) (char byte, n int, rest string) {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0, ""
	}
	char = line[i]
	j := i
	for j < len(line) && line[j] == char {
		j++
	}
	if j-i < 3 {
		return 0, 0, ""
	}
	return char, j - i, line[j:]
}

// openFence reports the fenced code block line opens, if any. A backtick
// fence's info string must not contain a backtick (CommonMark).
func openFence(line string) (codeFence, bool) {
	char, n, rest := fenceRun(line)
	if n == 0 || (char == '`' && strings.IndexByte(rest, '`') >= 0) {
		return codeFence{}, false
	}
	return codeFence{char: char, n: n, suggestion: suggestionInfo(strings.Trim(rest, " \t"))}, true
}

// closes reports whether line closes f: a run of f's character at least as
// long, after at most three spaces, followed by spaces or tabs only.
func (f codeFence) closes(line string) bool {
	char, n, rest := fenceRun(line)
	return char == f.char && n >= f.n && strings.Trim(rest, " \t") == ""
}

// suggestionInfo reports whether info is "suggestion" or
// "suggestion:-N+M" with N and M made of ASCII digits.
func suggestionInfo(info string) bool {
	if info == "suggestion" {
		return true
	}
	rest, ok := strings.CutPrefix(info, "suggestion:-")
	if !ok {
		return false
	}
	n, m, ok := strings.Cut(rest, "+")
	return ok && asciiDigits(n) && asciiDigits(m)
}

func asciiDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// sanitizeSuggestionAware is SanitizeBody's rule with QuickActions set: a
// space in front of every line that starts with "/", except inside a
// suggestion block, where only a QuickActionLine gets it. Fenced blocks of
// any other info string are tracked too, so that a fence-like line inside
// one is content, as in CommonMark; their lines are sanitised as every
// line outside a suggestion block is.
func sanitizeSuggestionAware(body string) string {
	var b strings.Builder
	b.Grow(len(body) + 8)
	var open *codeFence
	for _, l := range splitLines(body) {
		text := l.text
		switch {
		case open != nil && open.closes(text):
			open = nil
		case open != nil && open.suggestion:
			if QuickActionLine(text) {
				text = " " + text
			}
		default:
			if open == nil {
				if f, ok := openFence(text); ok {
					open = &f
				}
			}
			if strings.HasPrefix(text, "/") {
				text = " " + text
			}
		}
		b.WriteString(text)
		b.WriteString(l.end)
	}
	return b.String()
}

// SanitizeQuickActions puts a space in front of every line of s that starts
// with "/", unconditionally. SanitizeBody applies it by capability; pr_ask
// also applies it to the question and the answer on every provider (the v1
// behaviour), so that its published output does not depend on QuickActions.
func SanitizeQuickActions(s string) string {
	s = strings.ReplaceAll(s, "\n/", "\n /")
	s = strings.ReplaceAll(s, "\r/", "\r /")
	if strings.HasPrefix(s, "/") {
		s = " " + s
	}
	return s
}

// ThreadKind says whether a comment thread is PR-level or anchored to code.
type ThreadKind string

// Thread kinds.
const (
	// ThreadGeneral is a PR-level conversation.
	ThreadGeneral ThreadKind = "general"
	// ThreadInline is a thread anchored to a file and line.
	ThreadInline ThreadKind = "inline"
)

// CommentItem is one comment inside a Thread. Author and Body are untrusted
// third-party content.
type CommentItem struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// AuthorID and AuthorLogin identify the author exactly as the ownership
	// check of EditComment does, for IsUser(CurrentUser(), AuthorID,
	// AuthorLogin): the numeric user id in decimal ("" when the server sent
	// none) and the login or user name, without the display-name fallback
	// that Author may use. A comment that passes IsUser here therefore
	// passes EditComment's check too.
	AuthorID    string `json:"-"`
	AuthorLogin string `json:"-"`
	// URL is the comment's web URL when the provider knows it; set for the
	// comments a PR-level comment can be edited through (general threads)
	// and for the comments of inline threads, which pr_review links when it
	// skips a finding that is already on the PR (X-29).
	URL string `json:"-"`
}

// Thread is a comment thread of a pull request.
type Thread struct {
	// ID is the ID of the thread's root comment.
	ID   string     `json:"id"`
	Kind ThreadKind `json:"kind"`
	// Path is set for inline threads only.
	Path string `json:"path"`
	// Line is the new-side line of an inline thread; 0 when unknown. For a
	// comment anchored on a range of lines (GitHub) it is the range's last
	// line.
	Line int `json:"line"`
	// StartLine is the first new-side line of an inline thread on a range of
	// lines, below Line, for a provider that reports it (GitHub start_line);
	// 0 for a single line, an unknown start and every other provider.
	StartLine int `json:"start_line"`
	// Outdated is true when the provider says the anchor no longer matches
	// the current diff.
	Outdated bool `json:"outdated"`
	// Resolved is nil when the provider does not expose the state.
	Resolved *bool `json:"resolved"`
	// Comments holds the root first, then the replies, oldest first. It is
	// never nil.
	Comments []CommentItem `json:"comments"`
	// ReplyInThread says whether ReplyToComment can post inside this thread.
	ReplyInThread bool `json:"reply_in_thread"`
}

// ReplyResult is the result of Provider.ReplyToComment. InThread is false
// when the reply was posted as a PR-level comment instead.
type ReplyResult struct {
	Comment  Comment
	InThread bool
}

// User is the identity of a provider account. ID is the provider's numeric
// user id in decimal, or "" when the provider did not report one; Name is
// the login (Gitea, GitHub) or user name (Bitbucket Server).
type User struct {
	ID, Name string
}

// LineType says which kind of diff line an inline comment is anchored to.
type LineType string

// Line types. Both are new-side lines.
const (
	// LineAdded is a "+" line of a hunk.
	LineAdded LineType = "added"
	// LineContext is an unchanged line inside a hunk.
	LineContext LineType = "context"
)

// InlineComment is one comment to post on a changed file's line.
type InlineComment struct {
	// Path is the file's new path.
	Path string
	// OldPath is the file's old path for a rename, and "" otherwise.
	// Bitbucket Server sends it as the anchor's srcPath; Gitea ignores it.
	OldPath string
	// Line is the absolute new-side line number: the comment's line, or the
	// first line of its range.
	Line int
	// EndLine is the last new-side line of a multi-line range, which runs
	// from Line to EndLine inside one hunk; 0 (or Line) for a comment on one
	// line. A provider that can anchor a comment on a range (GitHub's
	// start_line and line) uses it; the others ignore it and post on Line
	// (Gitea, Bitbucket Server).
	EndLine int
	// LineType is the type of Line.
	LineType LineType
	// OldLine is the old-side line number of Line when Line is a context
	// line (the unchanged line's number in the base file), and 0 for an
	// added line. It is filled where LineType is computed, from the same
	// hunk. A provider that places a comment on an unchanged line by both
	// of its numbers uses it (GitLab's old_line and new_line); the others
	// ignore it.
	OldLine int
	Body    string
}

// InlineReason says why an inline comment was or was not posted.
type InlineReason string

// Inline reasons.
const (
	// InlineReasonPosted: the comment was posted (InlineResult.Posted).
	InlineReasonPosted InlineReason = "posted"
	// InlineReasonUnanchorable: the server definitely refused the comment's
	// position (a line that is not on the head side of its diff); nothing
	// was posted for the item.
	InlineReasonUnanchorable InlineReason = "unanchorable"
	// InlineReasonFailed: the comment was not posted for any other reason,
	// or its outcome is unknown.
	InlineReasonFailed InlineReason = "failed"
)

// InlineResult is the outcome of one InlineComment. When Posted is false,
// Error is a fixed sentence (X-6) and ID and URL are empty. When Posted is
// true, ID or URL may still be empty if the server did not report them.
// Reason is InlineReasonPosted exactly when Posted is true, and otherwise
// InlineReasonUnanchorable or InlineReasonFailed.
//
// Class is the class text of the provider error behind Error (ItemClass:
// the error class and the HTTP status, for example "rate_limited 403"), or
// empty when the item failed without one (a stray note that was left) or was
// posted. It is a fixed string, never a provider message.
type InlineResult struct {
	Posted  bool
	ID, URL string
	Error   string
	Reason  InlineReason
	Class   string
}
