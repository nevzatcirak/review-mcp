package github

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// listCaps: the issue search lists the token user's pull requests as a
// requested reviewer, an assignee and the author.
var listCaps = provider.ListCaps{Reviewer: true, Assignee: true, Author: true}

// searchQualifiers maps a list role to the search qualifier that takes the
// login.
var searchQualifiers = map[provider.ListRole]string{
	provider.ListReviewer: "review-requested:",
	provider.ListAssignee: "assignee:",
	provider.ListAuthor:   "author:",
}

// apiSearchItem is the part of an issue search result item a list reads.
type apiSearchItem struct {
	Number        int64      `json:"number"`
	Title         string     `json:"title"`
	Draft         *bool      `json:"draft"`
	UpdatedAt     *time.Time `json:"updated_at"`
	User          *apiUser   `json:"user"`
	RepositoryURL string     `json:"repository_url"`
	PullRequest   *struct{}  `json:"pull_request"`
}

// ListMyPullRequests implements provider.PRLister (X-48, E3): CurrentUser
// (github.self_login without GET /user), then one GET of /search/issues
// with q="is:pr is:open archived:false {review-requested|assignee|author}:
// {login}", sort=updated, order=desc and per_page={limit}.
// review-requested: includes requests to the user's teams, and a request
// ends once the user has submitted a review (documented).
//
// An item is listed only when it is a pull request (pull_request is set)
// whose repository_url is {the configured API base}/repos/{owner}/{repo};
// owner and repository are taken from that path only, and the URL is
// built from the configured web base. Any other item is dropped and
// counted. HasMore is total_count above the page's items, or
// incomplete_results. The target branch is not in the search answer: nil.
//
// The search is sent once, without the bounded rate-limit wait of the
// other requests: a search rate limit (30 requests a minute, 403 or 429)
// is the fixed rate_limited error.
func (p *Provider) ListMyPullRequests(ctx context.Context, q provider.ListQuery) (*provider.PRList, error) {
	if err := provider.ValidateListQuery(listCaps, q); err != nil {
		return nil, err
	}
	me, err := p.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if !validLogin(me.Name) {
		return nil, protocolErr("the login cannot be searched for")
	}
	v := url.Values{}
	v.Set("q", "is:pr is:open archived:false "+searchQualifiers[q.Role]+me.Name)
	v.Set("sort", "updated")
	v.Set("order", "desc")
	v.Set("per_page", strconv.Itoa(q.Limit))
	resp, err := p.send(ctx, request{path: "/search/issues?" + v.Encode()})
	if err != nil {
		return nil, err
	}
	var in struct {
		TotalCount        *int            `json:"total_count"`
		IncompleteResults bool            `json:"incomplete_results"`
		Items             []apiSearchItem `json:"items"`
	}
	if err := json.Unmarshal(resp.Data, &in); err != nil || in.TotalCount == nil || in.Items == nil {
		return nil, protocolErr("response is not valid JSON of the expected shape")
	}
	items := make([]provider.ListedPR, 0, len(in.Items))
	dropped := 0
	for i := range in.Items {
		it := &in.Items[i]
		ref, ok := p.listedRef(it.RepositoryURL, it.Number)
		if !ok || it.PullRequest == nil || it.UpdatedAt == nil {
			dropped++
			continue
		}
		author := ""
		if it.User != nil {
			author = it.User.Login
		}
		items = append(items, provider.ListedPR{
			Ref:       ref,
			Title:     provider.CleanListText(it.Title, provider.MaxListTitleRunes),
			Author:    provider.CleanListText(author, provider.MaxListAuthorRunes),
			Draft:     it.Draft,
			UpdatedAt: it.UpdatedAt.UTC(),
		})
	}
	more := in.IncompleteResults || *in.TotalCount > len(in.Items)
	l := provider.FinishList(items, q.Limit, more, dropped)
	p.logger.Debug("github pull requests listed", "role", string(q.Role), "items", len(l.Items),
		"dropped", l.Dropped, "has_more", l.HasMore)
	return l, nil
}

// listedRef builds the reference of a listed pull request from its
// repository_url, which must be {apiURL}/repos/{owner}/{repo} with exactly
// these two escaped segments; the web path (prWebPath) is parsed back with
// the resolver's ParsePRPath.
func (p *Provider) listedRef(repositoryURL string, number int64) (provider.PRRef, bool) {
	prefix := p.apiURL + "/repos/"
	if len(repositoryURL) <= len(prefix) || !strings.EqualFold(repositoryURL[:len(prefix)], prefix) {
		return provider.PRRef{}, false
	}
	parts := strings.Split(repositoryURL[len(prefix):], "/")
	if len(parts) != 2 {
		return provider.PRRef{}, false
	}
	owner, err1 := url.PathUnescape(parts[0])
	repo, err2 := url.PathUnescape(parts[1])
	if err1 != nil || err2 != nil {
		return provider.PRRef{}, false
	}
	return provider.ListedRef(provider.KindGitHub, p.webURL,
		prWebPath(provider.PRRef{Namespace: owner, Repo: repo, Number: number}), owner, repo, number, Factory{}.ParsePRPath)
}

// prWebPath is the escaped web path of ref's pull request below the web
// base: /{owner}/{repo}/pull/{n}; ParsePRPath gives ref back from it. It
// is the one builder of pull request web URLs.
func prWebPath(ref provider.PRRef) string {
	return "/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Repo) + "/pull/" + strconv.FormatInt(ref.Number, 10)
}

// validLogin reports whether login can stand in a search qualifier
// without changing the query: ASCII letters, digits and "-", 1 to 39 of
// them, optionally followed by "[bot]" (an app's login). A space or a
// quote would add qualifiers of its own.
func validLogin(login string) bool {
	name := strings.TrimSuffix(login, "[bot]")
	if name == "" || len(name) > 39 || name[0] == '-' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
