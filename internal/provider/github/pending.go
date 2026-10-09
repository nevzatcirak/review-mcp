package github

import (
	"context"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// NotImplementedHint is the Hint of the error every method that WP-2k,
// WP-2l and WP-2m will implement returns until then.
const NotImplementedHint = "not implemented for GitHub yet"

// errNotImplemented is returned before any request is sent.
func errNotImplemented() error { return protocolErr(NotImplementedHint) }

// PostComment implements provider.Provider. Not implemented yet (WP-2k).
func (*Provider) PostComment(context.Context, provider.PRRef, string) (*provider.Comment, error) {
	return nil, errNotImplemented()
}

// ListThreads implements provider.Provider. Not implemented yet (WP-2k).
func (*Provider) ListThreads(context.Context, provider.PRRef) ([]provider.Thread, error) {
	return nil, errNotImplemented()
}

// ReplyToComment implements provider.Provider. Not implemented yet (WP-2k).
func (*Provider) ReplyToComment(context.Context, provider.PRRef, string, string) (*provider.ReplyResult, error) {
	return nil, errNotImplemented()
}

// EditComment implements provider.Provider. Not implemented yet (WP-2k).
func (*Provider) EditComment(context.Context, provider.PRRef, string, string) error {
	return errNotImplemented()
}

// PostInlineComments implements provider.Provider. Not implemented yet
// (WP-2l); Capabilities().InlineComments is false until then.
func (*Provider) PostInlineComments(context.Context, provider.PRRef, *provider.PullRequest, []provider.InlineComment) ([]provider.InlineResult, error) {
	return nil, errNotImplemented()
}

// GetReviewStatus implements provider.Provider. Not implemented yet (WP-2m):
// nothing is read, and the status says so with the fixed notes.
func (*Provider) GetReviewStatus(context.Context, provider.PRRef, *provider.PullRequest, provider.ReviewStatusOptions) *provider.ReviewStatus {
	return &provider.ReviewStatus{Notes: []string{provider.NoteReviewsUnreadable, provider.NoteMergeUnreadable}}
}

// UpdatePullRequest implements provider.Provider. Not implemented yet
// (WP-2m); Capabilities().DescriptionEdit is false until then.
func (*Provider) UpdatePullRequest(context.Context, provider.PRRef, provider.UpdatePR) error {
	return errNotImplemented()
}
