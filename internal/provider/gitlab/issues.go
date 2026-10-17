package gitlab

import (
	"context"
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// apiIssue is GET /projects/{id}/issues/{iid}. Labels are names (GitLab
// sends objects only when asked to with with_labels_details).
type apiIssue struct {
	Title       string   `json:"title"`
	Description *string  `json:"description"`
	State       string   `json:"state"`
	WebURL      string   `json:"web_url"`
	Labels      []string `json:"labels"`
}

// GetIssue implements provider.IssueReader: GET /projects/{id}/issues/{iid}
// with the run's token, the project addressed by its URL-encoded full path.
// Issues and merge requests are numbered apart on GitLab, so a merge
// request's number never answers as an issue. State "opened" is open.
func (p *Provider) GetIssue(ctx context.Context, ref provider.IssueRef) (*provider.Issue, error) {
	if !provider.ValidIssueRef(ref) {
		return nil, protocolErr("invalid issue reference")
	}
	pp, err := projectPath(provider.PRRef{Namespace: ref.Namespace, Repo: ref.Repo})
	if err != nil {
		return nil, err
	}
	var in apiIssue
	if err := p.getJSON(ctx, pp+"/issues/"+strconv.FormatInt(ref.Number, 10), &in); err != nil {
		p.logger.Debug("gitlab issue not read", "error", errClass(err))
		return nil, err
	}
	var body string
	if in.Description != nil {
		body = *in.Description
	}
	is := provider.NewIssue(ref.Number, in.Title, body, in.State, in.WebURL, in.Labels)
	p.logger.Debug("gitlab issue read", "labels", len(is.Labels))
	return is, nil
}
