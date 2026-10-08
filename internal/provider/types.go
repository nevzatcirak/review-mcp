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
)

// Kind identifies a provider implementation. The string values are shared
// with server_info and must not change.
type Kind string

const (
	// KindGitea is the Gitea provider.
	KindGitea Kind = "gitea"
	// KindBitbucketServer is the Bitbucket Server / Data Center provider.
	KindBitbucketServer Kind = "bitbucket_server"
)

// PRRef identifies one pull request.
//
// Namespace is the Gitea owner or the Bitbucket project key ("~user" for
// personal repositories). It may contain "/" (nested groups, such as
// "group/sub/team" on GitLab); each Factory.ParsePRPath decides how many
// URL segments are namespace, and Gitea and Bitbucket Server accept exactly
// one. Whatever builds a URL, a cache path or a log field from it escapes it
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
	// BaseGiteaBaseSHA, BaseBBSMergeBaseEP and BaseBBSAncestorWalk. GetDiff
	// uses BaseSHA and BaseStrategy as given.
	BaseStrategy string

	// The fields below feed pr_info (X-23). They carry states and identities
	// only, never a comment or review body.

	// Draft is nil when the provider does not report a draft flag.
	Draft *bool
	// Merged is true when the provider says the PR was merged (Gitea reports
	// it next to State; Bitbucket Server uses State "MERGED").
	Merged bool
	// Mergeable is the provider's own verdict carried by the PR itself
	// (Gitea); nil when the PR payload has none.
	Mergeable *bool
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
	// has no diff --git, index, --- or +++ lines. "\ No newline at end of
	// file" lines are kept verbatim.
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
)

// Diff is the result of Provider.GetDiff.
type Diff struct {
	Files   []FilePatch
	Skipped []SkippedFile
	// BaseStrategy is copied from PullRequest.BaseStrategy.
	BaseStrategy string
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
	SuggestionBlocks bool
	// QuickActions: a published line that starts with "/" runs a quick
	// action, so every published body is slash-sanitised (SanitizeBody).
	QuickActions bool
	// ThreadResolution: the provider reports whether a thread is resolved.
	ThreadResolution bool
	// DescriptionEdit: the PR description can be updated through the API.
	DescriptionEdit bool
}

// SanitizeBody returns body ready to be published for a provider with caps.
// When caps.QuickActions is set it puts a space in front of every line that
// starts with "/", so that no published line triggers a quick action
// (upstream's answer sanitization, pr_questions.py _prepare_pr_answer @
// 8e5a929). Lines are split at "\n" and at "\r"; a body that starts with "/"
// is covered too. Without QuickActions body is returned unchanged.
//
// It is the one place where published bodies are sanitised: pr_review (the
// overview and the inline comments), pr_comment_create, pr_comment_reply,
// pr_ask and pr_describe all publish through it.
func SanitizeBody(caps Capabilities, body string) string {
	if !caps.QuickActions {
		return body
	}
	return SanitizeQuickActions(body)
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
	// comments a PR-level comment can be edited through (general threads).
	URL string `json:"-"`
}

// Thread is a comment thread of a pull request.
type Thread struct {
	// ID is the ID of the thread's root comment.
	ID   string     `json:"id"`
	Kind ThreadKind `json:"kind"`
	// Path is set for inline threads only.
	Path string `json:"path"`
	// Line is the new-side line of an inline thread; 0 when unknown.
	Line int `json:"line"`
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
// the login (Gitea) or user name (Bitbucket Server).
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
	// Line is the absolute new-side line number.
	Line     int
	LineType LineType
	Body     string
}

// InlineResult is the outcome of one InlineComment. When Posted is false,
// Error is a fixed sentence (X-6) and ID and URL are empty. When Posted is
// true, ID or URL may still be empty if the server did not report them.
type InlineResult struct {
	Posted  bool
	ID, URL string
	Error   string
}
