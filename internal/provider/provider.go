package provider

import "context"

// Provider is a cheap, request-scoped view of one code host. Implementations
// are built from config.Config plus the provider's config.Secret; the secret
// is revealed only when a request header is set and is never stored in its
// revealed form.
type Provider interface {
	Kind() Kind
	Capabilities() Capabilities
	GetPullRequest(ctx context.Context, ref PRRef) (*PullRequest, error)
	// GetCommitMessages returns the PR's commit messages, oldest first.
	GetCommitMessages(ctx context.Context, ref PRRef) ([]string, error)
	GetDiff(ctx context.Context, ref PRRef, pr *PullRequest, opts DiffOptions) (*Diff, error)
	PostComment(ctx context.Context, ref PRRef, body string) (*Comment, error)
	// ListThreads returns the PR's comment threads: general threads first,
	// then inline threads (see SortThreads). System events are excluded.
	ListThreads(ctx context.Context, ref PRRef) ([]Thread, error)
	// ReplyToComment replies to the comment commentID. It validates its
	// input with ValidateReply before any request is sent.
	ReplyToComment(ctx context.Context, ref PRRef, commentID string, body string) (*ReplyResult, error)
	// CurrentUser returns the identity of the token's own user.
	CurrentUser(ctx context.Context) (User, error)
	// EditComment replaces the body of the comment commentID entirely. It
	// validates its input with ValidateEdit before any request is sent, and
	// it re-reads the comment and refuses with a not_owner error, without
	// sending the edit, when the comment was not written by CurrentUser.
	EditComment(ctx context.Context, ref PRRef, commentID string, body string) error
	// PostInlineComments posts items as inline comments on the head side of
	// the PR's diff. The items are validated with ValidateInlineComments
	// before any request is sent; an invalid item fails the whole call. The
	// result has one entry per item, in order. An error is returned only
	// when nothing could be attempted.
	PostInlineComments(ctx context.Context, ref PRRef, pr *PullRequest, items []InlineComment) ([]InlineResult, error)
	// FileLineURL is pure: no I/O.
	FileLineURL(ref PRRef, pr *PullRequest, path string, line int) string
}
