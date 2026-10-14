package gitlab

import (
	"context"
	"net/http"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// UpdatePullRequest implements provider.Provider with PUT
// /projects/{id}/merge_requests/{iid}, which sends only the fields that are
// set: GitLab leaves an absent field as it is, so the draft state (derived
// from the title), the reviewers, labels, assignees and the target branch
// are never named. GitLab has no version: up.Version is ignored, and the
// caller detects a concurrent edit by comparing the text it read.
func (p *Provider) UpdatePullRequest(ctx context.Context, ref provider.PRRef, up provider.UpdatePR) error {
	if err := provider.ValidateUpdatePR(up); err != nil {
		return err
	}
	path, err := mrPath(ref)
	if err != nil {
		return err
	}
	in := map[string]string{}
	if up.Title != nil {
		in["title"] = *up.Title
	}
	if up.Description != nil {
		in["description"] = *up.Description
	}
	return p.sendJSON(ctx, http.MethodPut, path, in, nil)
}
