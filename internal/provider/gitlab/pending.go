package gitlab

import (
	"context"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// NotImplementedHint is the Hint of the error every method that WP-6c and
// WP-6d will implement returns until then.
const NotImplementedHint = "not implemented for GitLab yet"

// errNotImplemented is returned before any request is sent.
func errNotImplemented() error { return protocolErr(NotImplementedHint) }

// PostInlineComments implements provider.Provider. Not implemented yet
// (WP-6c); Capabilities().InlineComments is false until then.
func (*Provider) PostInlineComments(context.Context, provider.PRRef, *provider.PullRequest, []provider.InlineComment) ([]provider.InlineResult, error) {
	return nil, errNotImplemented()
}

// GetReviewStatus implements provider.Provider. Not implemented yet (WP-6d):
// nothing is read, and the status says so with the fixed notes.
func (*Provider) GetReviewStatus(context.Context, provider.PRRef, *provider.PullRequest, provider.ReviewStatusOptions) *provider.ReviewStatus {
	return &provider.ReviewStatus{Notes: []string{provider.NoteReviewsUnreadable, provider.NoteMergeUnreadable}}
}

// UpdatePullRequest implements provider.Provider. Not implemented yet
// (WP-6d); Capabilities().DescriptionEdit is false until then.
func (*Provider) UpdatePullRequest(context.Context, provider.PRRef, provider.UpdatePR) error {
	return errNotImplemented()
}

// GetChecks implements provider.Provider. Not implemented yet (WP-6d).
func (*Provider) GetChecks(context.Context, provider.PRRef, string) (*provider.Checks, error) {
	return nil, errNotImplemented()
}
