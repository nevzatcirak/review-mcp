package bitbucketserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// listCaps: the dashboard lists the token user's pull requests as a
// reviewer and as the author. Bitbucket Server has no assignees (X-48 §0
// item 2).
var listCaps = provider.ListCaps{Reviewer: true, Author: true}

// listOrder is the order the dashboard is asked for.
//
// DESIGN-QUESTION (WP-19a DQ-2, E1): the dashboard documents the orders
// OLDEST, NEWEST, PARTICIPANT_STATUS and CLOSED_DATE (7.0; DRAFT_STATUS
// from 8.17), with NEWEST the default, but no documentation says whether
// NEWEST sorts by creation or by last update. NEWEST is sent; the provider
// sorts the page by updatedDate itself (provider.FinishList), so the
// order of the page is right either way, and only which pull requests a
// full page holds depends on it. The fixture assumes NEWEST is by update.
// Recommendation: keep NEWEST; acceptance on the Data Center instance
// confirms it.
const listOrder = "NEWEST"

// dashboardRoles maps a list role to the dashboard's role parameter.
var dashboardRoles = map[provider.ListRole]string{
	provider.ListReviewer: "REVIEWER",
	provider.ListAuthor:   "AUTHOR",
}

// apiListedPR is the part of a dashboard pull request a list reads.
type apiListedPR struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Draft       *bool  `json:"draft"`
	UpdatedDate int64  `json:"updatedDate"`
	ToRef       struct {
		DisplayID  string `json:"displayId"`
		Repository struct {
			Slug    string `json:"slug"`
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
		} `json:"repository"`
	} `json:"toRef"`
	Author struct {
		User apiPerson `json:"user"`
	} `json:"author"`
	Reviewers []struct {
		User   apiPerson `json:"user"`
		Status string    `json:"status"`
	} `json:"reviewers"`
}

// ListMyPullRequests implements provider.PRLister (X-48, E1): one GET of
// /rest/api/1.0/dashboard/pull-requests?state=OPEN&role={REVIEWER|AUTHOR}
// &order=NEWEST&start=0&limit={limit}. The dashboard lists the pull
// requests of the authenticated user; no user name is sent. HasMore is the
// page's isLastPage false.
//
// The token user's own reviewer status (MyStatus) is read from the
// reviewers[] entry whose user is the one the response's X-AUSERID (or,
// without it, X-AUSERNAME) header names, the headers every authenticated
// response carries (see CurrentUser). Without them MyStatus stays "":
// never another reviewer's status.
//
// DESIGN-QUESTION (WP-19a DQ-3): the spec reads "the token user's entry in
// reviewers[]" but sends no user name and allows one request per query;
// the identity headers of the same response give the entry without a
// second request. Recommendation: keep; it is a live-verification item.
// The version probe (ensureSupported) is not sent either: the dashboard is
// documented since 4.10.
func (p *Provider) ListMyPullRequests(ctx context.Context, q provider.ListQuery) (*provider.PRList, error) {
	if err := provider.ValidateListQuery(listCaps, q); err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("state", "OPEN")
	v.Set("role", dashboardRoles[q.Role])
	v.Set("order", listOrder)
	v.Set("start", "0")
	v.Set("limit", strconv.Itoa(q.Limit))
	resp, err := p.client.DoRequest(ctx, httpx.Request{Method: http.MethodGet,
		PathAndQuery: apiV1 + "/dashboard/pull-requests?" + v.Encode(),
		MaxBytes:     httpx.MaxJSONBytes, CapKey: httpx.JSONCapKey})
	if err != nil {
		return nil, err
	}
	var page struct {
		IsLastPage *bool         `json:"isLastPage"`
		Values     []apiListedPR `json:"values"`
	}
	if err := json.Unmarshal(resp.Data, &page); err != nil || page.IsLastPage == nil {
		return nil, protocolErr("response is not valid JSON of the expected shape")
	}
	me := provider.User{Name: resp.Header.Get(headerUserName)}
	if id, ok := parsePositive(resp.Header.Get(headerUserID)); ok {
		me.ID = strconv.FormatInt(id, 10)
	}
	items := make([]provider.ListedPR, 0, len(page.Values))
	dropped := 0
	for i := range page.Values {
		in := &page.Values[i]
		ref, ok := p.listedRef(in.ToRef.Repository.Project.Key, in.ToRef.Repository.Slug, in.ID)
		if !ok {
			dropped++
			continue
		}
		it := provider.ListedPR{
			Ref:          ref,
			Title:        provider.CleanListText(in.Title, provider.MaxListTitleRunes),
			Author:       provider.CleanListText(in.Author.User.Name, provider.MaxListAuthorRunes),
			TargetBranch: provider.ListBranch(in.ToRef.DisplayID),
			Draft:        in.Draft,
			UpdatedAt:    time.UnixMilli(in.UpdatedDate).UTC(),
		}
		if q.Role == provider.ListReviewer {
			for _, r := range in.Reviewers {
				if provider.IsUser(me, positiveID(r.User.ID), r.User.Name) {
					it.MyStatus = myStatus(r.Status)
					break
				}
			}
		}
		items = append(items, it)
	}
	l := provider.FinishList(items, q.Limit, !*page.IsLastPage, dropped)
	p.logger.Debug("bitbucket server pull requests listed", "role", string(q.Role), "items", len(l.Items),
		"dropped", l.Dropped, "has_more", l.HasMore)
	return l, nil
}

// listedRef builds the reference of a listed pull request: its web path
// (prWebPath) parsed back with the resolver's ParsePRPath.
func (p *Provider) listedRef(key, slug string, id int64) (provider.PRRef, bool) {
	return provider.ListedRef(provider.KindBitbucketServer, p.baseURL,
		prWebPath(provider.PRRef{Namespace: key, Repo: slug, Number: id}), key, slug, id, Factory{}.ParsePRPath)
}

// myStatus maps a participant status to ListedPR.MyStatus; "" for a value
// the host does not document.
func myStatus(s string) string {
	switch s {
	case "APPROVED":
		return provider.MyStatusApproved
	case "NEEDS_WORK":
		return provider.MyStatusNeedsWork
	case "UNAPPROVED":
		return provider.MyStatusUnapproved
	}
	return ""
}

// positiveID is id in decimal, or "" when the host sent none.
func positiveID(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
