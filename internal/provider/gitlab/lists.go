package gitlab

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// listCaps: GitLab lists the token user's merge requests as a reviewer,
// as an assignee and as the author.
var listCaps = provider.ListCaps{Reviewer: true, Assignee: true, Author: true}

// apiListedMR is the part of a listed merge request a list reads.
type apiListedMR struct {
	IID          int64      `json:"iid"`
	Title        string     `json:"title"`
	Draft        *bool      `json:"draft"`
	UpdatedAt    *time.Time `json:"updated_at"`
	TargetBranch string     `json:"target_branch"`
	References   struct {
		Full string `json:"full"`
	} `json:"references"`
	Author apiUser `json:"author"`
}

// ListMyPullRequests implements provider.PRLister (X-48, E2): one GET of
// /api/v4/merge_requests?state=opened&order_by=updated_at&sort=desc
// &per_page={limit}&page=1 with, per role, scope=all&reviewer_id={the
// CurrentUser ID} (reviewer; reviewer_id since 13.8), scope=assigned_to_me
// (assignee) or scope=created_by_me (author). Only the reviewer query
// needs the token user's identity, so only it calls CurrentUser first.
// HasMore is a non-empty X-Next-Page header (X-Total may be absent).
//
// scope=reviews_for_me is not used: GitLab added it in 18.4, and 16.0 (the
// minimum) refuses it (E2).
//
// DESIGN-QUESTION (WP-19a DQ-4): the spec says GitLab calls CurrentUser
// first; the assignee and author queries do not need it, so they skip it
// (one request per query, §0 item 5). Recommendation: keep.
func (p *Provider) ListMyPullRequests(ctx context.Context, q provider.ListQuery) (*provider.PRList, error) {
	if err := provider.ValidateListQuery(listCaps, q); err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("state", "opened")
	v.Set("order_by", "updated_at")
	v.Set("sort", "desc")
	v.Set("per_page", strconv.Itoa(q.Limit))
	v.Set("page", "1")
	switch q.Role {
	case provider.ListReviewer:
		me, err := p.CurrentUser(ctx)
		if err != nil {
			return nil, err
		}
		if me.ID == "" {
			return nil, protocolErr("the user response has no id")
		}
		v.Set("scope", "all")
		v.Set("reviewer_id", me.ID)
	case provider.ListAssignee:
		v.Set("scope", "assigned_to_me")
	case provider.ListAuthor:
		v.Set("scope", "created_by_me")
	}
	resp, err := p.do(ctx, request{path: "/merge_requests?" + v.Encode()})
	if err != nil {
		return nil, err
	}
	var in []apiListedMR
	if err := json.Unmarshal(resp.Data, &in); err != nil || in == nil {
		return nil, protocolErr("response is not valid JSON of the expected shape")
	}
	items := make([]provider.ListedPR, 0, len(in))
	dropped := 0
	for i := range in {
		mr := &in[i]
		ref, ok := p.listedRef(mr.References.Full, mr.IID)
		if !ok || mr.UpdatedAt == nil {
			dropped++
			continue
		}
		items = append(items, provider.ListedPR{
			Ref:          ref,
			Title:        provider.CleanListText(mr.Title, provider.MaxListTitleRunes),
			Author:       provider.CleanListText(mr.Author.Username, provider.MaxListAuthorRunes),
			TargetBranch: provider.ListBranch(mr.TargetBranch),
			Draft:        mr.Draft,
			UpdatedAt:    mr.UpdatedAt.UTC(),
		})
	}
	more := strings.TrimSpace(resp.Header.Get("X-Next-Page")) != ""
	l := provider.FinishList(items, q.Limit, more, dropped)
	p.logger.Debug("gitlab merge requests listed", "role", string(q.Role), "items", len(l.Items),
		"dropped", l.Dropped, "has_more", l.HasMore)
	return l, nil
}

// listedRef builds the reference of a listed merge request from its
// references.full ("group/sub/project!42"): the part before the last "!"
// is the project's full path, whose last segment is the project and the
// rest the namespace; the number after it must be iid. The web path
// (mrWebPath) is parsed back with the resolver's ParsePRPath.
func (p *Provider) listedRef(full string, iid int64) (provider.PRRef, bool) {
	path, num, ok := cutLast(full, "!")
	if !ok || num != strconv.FormatInt(iid, 10) {
		return provider.PRRef{}, false
	}
	ns, repo, ok := cutLast(path, "/")
	if !ok {
		return provider.PRRef{}, false
	}
	return provider.ListedRef(provider.KindGitLab, p.webURL,
		mrWebPath(provider.PRRef{Namespace: ns, Repo: repo, Number: iid}), ns, repo, iid, Factory{}.ParsePRPath)
}

// cutLast cuts s around the last sep.
func cutLast(s, sep string) (before, after string, ok bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+len(sep):], true
}

// mrWebPath is the escaped web path of ref's merge request below the base
// URL: /{namespace...}/{project}/-/merge_requests/{iid}, every segment
// escaped; ParsePRPath gives ref back from it. It is the one builder of
// merge request web URLs.
func mrWebPath(ref provider.PRRef) string {
	return "/" + provider.EscapeNamespace(ref.Namespace) + "/" + url.PathEscape(ref.Repo) +
		"/-/merge_requests/" + strconv.FormatInt(ref.Number, 10)
}
