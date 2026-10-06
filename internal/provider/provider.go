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
	// FileLineURL is pure: no I/O.
	FileLineURL(ref PRRef, pr *PullRequest, path string, line int) string
}
