// Package provider defines the provider-neutral types, the Provider
// interface, the sanitized error classes (X-6) and the PR-URL Resolver (X-2).
//
// Only provider implementations and internal/provider/httpx perform network
// I/O; this package never does.
package provider

import "time"

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
// personal repositories). URL is the user-supplied PR URL; it may only be
// logged through logging.RedactURL.
type PRRef struct {
	Kind      Kind
	Namespace string
	Repo      string
	Number    int64
	URL       string
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

// Capabilities describes what a provider's markup supports (DQ-16).
type Capabilities struct {
	GFM, MarkdownTables, Labels, InlineComments bool
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
