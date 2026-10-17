package gitea

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// apiIssue is GET /repos/{o}/{r}/issues/{n}. Gitea serves pull requests
// through the same endpoint, with pull_request set (null for an issue).
type apiIssue struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
}

// GetIssue implements provider.IssueReader: GET /repos/{o}/{r}/issues/{n}
// with the run's token. A number that names a pull request (pull_request
// set) is not_an_issue.
func (p *Provider) GetIssue(ctx context.Context, ref provider.IssueRef) (*provider.Issue, error) {
	if !provider.ValidIssueRef(ref) || strings.Contains(ref.Namespace, "/") || strings.Contains(ref.Repo, "/") {
		return nil, protocolErr("invalid issue reference")
	}
	path := apiPrefix + "/repos/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Repo) + "/issues/" +
		strconv.FormatInt(ref.Number, 10)
	var in apiIssue
	if err := p.client.GetJSON(ctx, path, &in); err != nil {
		p.logger.Debug("gitea issue not read", "error", errClass(err))
		return nil, err
	}
	if provider.JSONSet(in.PullRequest) {
		p.logger.Debug("gitea issue not read", "error", string(provider.ClassNotAnIssue))
		return nil, provider.NotAnIssueError()
	}
	labels := make([]string, 0, len(in.Labels))
	for _, l := range in.Labels {
		labels = append(labels, l.Name)
	}
	is := provider.NewIssue(ref.Number, in.Title, in.Body, in.State, in.HTMLURL, labels)
	p.logger.Debug("gitea issue read", "labels", len(is.Labels))
	return is, nil
}
