package github

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// apiIssue is GET /repos/{o}/{r}/issues/{n}. GitHub serves pull requests
// through the same endpoint, with pull_request set.
type apiIssue struct {
	Title   string  `json:"title"`
	Body    *string `json:"body"`
	State   string  `json:"state"`
	HTMLURL string  `json:"html_url"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
}

// GetIssue implements provider.IssueReader: GET /repos/{o}/{r}/issues/{n}
// on the provider's API base with the run's token. A number that names a
// pull request (pull_request set) is not_an_issue.
func (p *Provider) GetIssue(ctx context.Context, ref provider.IssueRef) (*provider.Issue, error) {
	if !provider.ValidIssueRef(ref) || strings.Contains(ref.Namespace, "/") || strings.Contains(ref.Repo, "/") {
		return nil, protocolErr("invalid issue reference")
	}
	path := "/repos/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Repo) + "/issues/" +
		strconv.FormatInt(ref.Number, 10)
	var in apiIssue
	if err := p.getJSON(ctx, path, &in); err != nil {
		p.logger.Debug("github issue not read", "error", errClass(err))
		return nil, err
	}
	if provider.JSONSet(in.PullRequest) {
		p.logger.Debug("github issue not read", "error", string(provider.ClassNotAnIssue))
		return nil, provider.NotAnIssueError()
	}
	labels := make([]string, 0, len(in.Labels))
	for _, l := range in.Labels {
		labels = append(labels, l.Name)
	}
	var body string
	if in.Body != nil {
		body = *in.Body
	}
	is := provider.NewIssue(ref.Number, in.Title, body, in.State, in.HTMLURL, labels)
	p.logger.Debug("github issue read", "labels", len(is.Labels))
	return is, nil
}
