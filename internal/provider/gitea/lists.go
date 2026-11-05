package gitea

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// listCaps: the issue search lists the token user's pull requests as a
// requested reviewer, an assignee and the author (Gitea 1.14+).
var listCaps = provider.ListCaps{Reviewer: true, Assignee: true, Author: true}

// HintListMinVersion is the Hint of the unsupported_version error of a list
// on a Gitea before 1.14, whose issue search ignores the role filters.
const HintListMinVersion = "Gitea 1.14 or newer is required to list pull requests"

// The first Gitea version whose issue search has the role filters (E4).
const (
	listMinMajor = 1
	listMinMinor = 14
)

// searchFilters maps a list role to the issue search filter that selects
// the signed-in user's pull requests in it.
var searchFilters = map[provider.ListRole]string{
	provider.ListReviewer: "review_requested",
	provider.ListAssignee: "assigned",
	provider.ListAuthor:   "created",
}

// apiSearchIssue is the part of an issue search result a list reads.
type apiSearchIssue struct {
	Number     int64      `json:"number"`
	Title      string     `json:"title"`
	UpdatedAt  *time.Time `json:"updated_at"`
	User       apiUser    `json:"user"`
	Repository *struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"repository"`
	PullRequest *struct {
		Draft *bool `json:"draft"`
	} `json:"pull_request"`
}

// ListMyPullRequests implements provider.PRLister (X-48, E4): one GET of
// /api/v1/version (checkListVersion: a Gitea before 1.14 is refused, and
// no search is sent), then one GET of /api/v1/repos/issues/search?
// type=pulls&state=open&{review_requested|assigned|created}=true&page=1&
// limit={limit}. The filters apply to the
// signed-in user (the token's); no user name is sent. HasMore is an
// X-Total-Count above the page's items (Gitea before 1.26 caps limit at
// its MaxResponseItems, 50 by default, which this covers). The draft flag
// is pull_request.draft (Gitea 1.22+; nil before). The target branch is
// not in the answer: nil.
//
// DESIGN-QUESTION (WP-19a DQ-5, E4): the issue search has no sort
// parameter and orders by creation, newest first (v1.14 to v1.27.3), not by
// update. The provider sorts the page by updated_at itself
// (provider.FinishList), so the order is right; but when more pull
// requests match than limit, the page holds the most recently created
// ones, which are not always the most recently updated. HasMore and the
// tool's NoteListMore say so. Recommendation: keep, and say it in
// docs/pr-lists.md (WP-19c).
func (p *Provider) ListMyPullRequests(ctx context.Context, q provider.ListQuery) (*provider.PRList, error) {
	if err := provider.ValidateListQuery(listCaps, q); err != nil {
		return nil, err
	}
	if err := p.checkListVersion(ctx); err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("type", "pulls")
	v.Set("state", "open")
	v.Set(searchFilters[q.Role], "true")
	v.Set("page", "1")
	v.Set("limit", strconv.Itoa(q.Limit))
	resp, err := p.client.DoRequest(ctx, httpx.Request{Method: http.MethodGet,
		PathAndQuery: apiPrefix + "/repos/issues/search?" + v.Encode(),
		MaxBytes:     httpx.MaxJSONBytes, CapKey: httpx.JSONCapKey})
	if err != nil {
		return nil, err
	}
	var in []apiSearchIssue
	if err := json.Unmarshal(resp.Data, &in); err != nil || in == nil {
		return nil, protocolErr("response is not valid JSON of the expected shape")
	}
	items := make([]provider.ListedPR, 0, len(in))
	dropped := 0
	for i := range in {
		it := &in[i]
		var ref provider.PRRef
		ok := it.Repository != nil && it.PullRequest != nil && it.UpdatedAt != nil
		if ok {
			ref, ok = p.listedRef(it.Repository.Owner, it.Repository.Name, it.Number)
		}
		if !ok {
			dropped++
			continue
		}
		items = append(items, provider.ListedPR{
			Ref:       ref,
			Title:     provider.CleanListText(it.Title, provider.MaxListTitleRunes),
			Author:    provider.CleanListText(it.User.Login, provider.MaxListAuthorRunes),
			Draft:     it.PullRequest.Draft,
			UpdatedAt: it.UpdatedAt.UTC(),
		})
	}
	more := false
	if total, ok := parsePositive(strings.TrimSpace(resp.Header.Get("X-Total-Count"))); ok {
		more = total > int64(len(in))
	}
	l := provider.FinishList(items, q.Limit, more, dropped)
	p.logger.Debug("gitea pull requests listed", "role", string(q.Role), "items", len(l.Items),
		"dropped", l.Dropped, "has_more", l.HasMore)
	return l, nil
}

// checkListVersion sends one GET /api/v1/version and refuses a list on a
// Gitea before 1.14 (WP-19a DQ-10): its issue search ignores the role
// filters and would list every open pull request the token can see. A
// failed version request or a version that does not parse lets the list go
// ahead: the check must not make a working list fail. The answer is not
// kept across calls, and the version text is never logged.
func (p *Provider) checkListVersion(ctx context.Context) error {
	var out struct {
		Version string `json:"version"`
	}
	if err := p.client.GetJSON(ctx, apiPrefix+"/version", &out); err != nil {
		p.logger.Debug("gitea version unknown")
		return nil
	}
	ok, known := listVersionSupported(out.Version)
	if !known {
		p.logger.Debug("gitea version unknown")
		return nil
	}
	if !ok {
		return &provider.Error{Class: provider.ClassUnsupportedVersion, Hint: HintListMinVersion}
	}
	return nil
}

// listVersionSupported reports whether the Gitea version v has the issue
// search's role filters (1.14+); known is false when v does not parse.
//   - A Forgejo version that carries "+gitea-A.B..." (7.0.1+gitea-1.21.11)
//     is compared by A.B.
//   - Otherwise "X.Y[.Z][-suffix][+meta]" is compared by X.Y, and a major of
//     2 or more is new enough on its own (Forgejo numbering).
func listVersionSupported(v string) (ok, known bool) {
	if _, meta, found := strings.Cut(v, "+gitea-"); found {
		if major, minor, ok := majorMinor(meta); ok && minor >= 0 {
			return versionAtLeast(major, minor), true
		}
	}
	major, minor, parsed := majorMinor(v)
	switch {
	case !parsed:
		return false, false
	case major >= 2:
		return true, true
	case minor < 0:
		return false, false
	}
	return versionAtLeast(major, minor), true
}

func versionAtLeast(major, minor int) bool {
	return major > listMinMajor || major == listMinMajor && minor >= listMinMinor
}

// majorMinor reads the leading "X" or "X.Y" of a version; minor is -1 when
// there is no ".Y". What follows must be the end or one of ".-+".
func majorMinor(v string) (major, minor int, ok bool) {
	major, rest, ok := leadingInt(v)
	if !ok {
		return 0, 0, false
	}
	minor = -1
	if strings.HasPrefix(rest, ".") {
		if m, r, ok := leadingInt(rest[1:]); ok {
			minor, rest = m, r
		}
	}
	if rest != "" && !strings.ContainsRune(".-+", rune(rest[0])) {
		return 0, 0, false
	}
	return major, minor, true
}

// leadingInt reads the leading decimal number of s (at most 6 digits).
func leadingInt(s string) (n int, rest string, ok bool) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 || end > 6 {
		return 0, s, false
	}
	n, err := strconv.Atoi(s[:end])
	return n, s[end:], err == nil
}

// listedRef builds the reference of a listed pull request from its
// repository's owner and name; the web path (prWebPath) is parsed back
// with the resolver's ParsePRPath.
func (p *Provider) listedRef(owner, repo string, number int64) (provider.PRRef, bool) {
	return provider.ListedRef(provider.KindGitea, p.webURL,
		prWebPath(provider.PRRef{Namespace: owner, Repo: repo, Number: number}), owner, repo, number, Factory{}.ParsePRPath)
}

// prWebPath is the escaped web path of ref's pull request below the web
// base: /{owner}/{repo}/pulls/{n}; ParsePRPath gives ref back from it. It
// is the one builder of pull request web URLs.
func prWebPath(ref provider.PRRef) string {
	return "/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Repo) + "/pulls/" + strconv.FormatInt(ref.Number, 10)
}
