package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// commentsPageCap bounds each comment list (100 per page).
const commentsPageCap = 40

// NoteAmbiguousID is the Hint of the error for a comment id that names a
// comment in more than one of GitHub's id spaces (issue comments, review
// comments and review bodies are numbered separately).
const NoteAmbiguousID = "the comment id is ambiguous on GitHub"

// lenientTime decodes an RFC 3339 timestamp; a null, empty or unparsable
// value becomes the zero time instead of failing the whole response.
type lenientTime struct{ time.Time }

func (t *lenientTime) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		t.Time = ts
	}
	return nil
}

// apiIssueComment is an entry of GET /issues/{n}/comments and the body of
// GET /issues/comments/{id}.
type apiIssueComment struct {
	ID        int64       `json:"id"`
	User      *apiUser    `json:"user"`
	Body      string      `json:"body"`
	CreatedAt lenientTime `json:"created_at"`
	UpdatedAt lenientTime `json:"updated_at"`
	HTMLURL   string      `json:"html_url"`
	IssueURL  string      `json:"issue_url"`
}

// apiReviewComment is an entry of GET /pulls/{n}/comments and the body of
// GET /pulls/comments/{id}. Line is null for an outdated comment.
type apiReviewComment struct {
	ID             int64       `json:"id"`
	User           *apiUser    `json:"user"`
	Body           string      `json:"body"`
	Path           string      `json:"path"`
	Line           *int        `json:"line"`
	OriginalLine   *int        `json:"original_line"`
	Side           string      `json:"side"`
	InReplyToID    *int64      `json:"in_reply_to_id"`
	CreatedAt      lenientTime `json:"created_at"`
	UpdatedAt      lenientTime `json:"updated_at"`
	HTMLURL        string      `json:"html_url"`
	PullRequestURL string      `json:"pull_request_url"`
}

// apiReview is an entry of GET /pulls/{n}/reviews.
type apiReview struct {
	ID          int64       `json:"id"`
	User        *apiUser    `json:"user"`
	Body        string      `json:"body"`
	State       string      `json:"state"`
	SubmittedAt lenientTime `json:"submitted_at"`
	HTMLURL     string      `json:"html_url"`
}

func login(u *apiUser) string {
	if u == nil {
		return ""
	}
	return u.Login
}

func userID(u *apiUser) string {
	if u == nil || u.ID == 0 {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

func (c *apiIssueComment) item() provider.CommentItem {
	return provider.CommentItem{
		ID: idStr(c.ID), Author: login(c.User), Body: c.Body,
		CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time,
		AuthorID: userID(c.User), AuthorLogin: login(c.User), URL: c.HTMLURL,
	}
}

func (c *apiReviewComment) item() provider.CommentItem {
	return provider.CommentItem{
		ID: idStr(c.ID), Author: login(c.User), Body: c.Body,
		CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time,
		AuthorID: userID(c.User), AuthorLogin: login(c.User), URL: c.HTMLURL,
	}
}

func (r *apiReview) item() provider.CommentItem {
	return provider.CommentItem{
		ID: idStr(r.ID), Author: login(r.User), Body: r.Body,
		CreatedAt: r.SubmittedAt.Time, UpdatedAt: r.SubmittedAt.Time,
		AuthorID: userID(r.User), AuthorLogin: login(r.User), URL: r.HTMLURL,
	}
}

// anchor returns the line of a review comment and whether it is outdated.
// GitHub sends line null when the comment no longer maps onto the current
// diff; original_line is then the line it was made on. A comment on the
// base (LEFT) side has no new-side line: Thread.Line is the new side, so it
// is 0 (unknown).
func (c *apiReviewComment) anchor() (line int, outdated bool) {
	switch {
	case c.Line != nil:
		line = *c.Line
	case c.OriginalLine != nil:
		line, outdated = *c.OriginalLine, true
	}
	if strings.EqualFold(c.Side, "LEFT") {
		line = 0
	}
	return line, outdated
}

func listPath(base string) string { return base + "?per_page=" + strconv.Itoa(perPage) }

// ListThreads implements provider.Provider.
//
// Every issue comment is a general thread of its own, and so is every
// review with a body (authored by the reviewer; a pending draft is not
// visible to others and is skipped). Review comments become inline threads:
// replies point at their root through in_reply_to_id. GitHub's REST API does
// not expose whether a thread is resolved, so Resolved is always nil.
func (p *Provider) ListThreads(ctx context.Context, ref provider.PRRef) ([]provider.Thread, error) {
	pp, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	issueComments, err := httpx.PagesByLink[apiIssueComment](ctx, p.client, p.fetchPage,
		listPath(rp+"/issues/"+idStr(ref.Number)+"/comments"), commentsPageCap)
	if err != nil {
		return nil, err
	}
	rcs, err := httpx.PagesByLink[apiReviewComment](ctx, p.client, p.fetchPage, listPath(pp+"/comments"), commentsPageCap)
	if err != nil {
		return nil, err
	}
	reviews, err := httpx.PagesByLink[apiReview](ctx, p.client, p.fetchPage, listPath(pp+"/reviews"), commentsPageCap)
	if err != nil {
		return nil, err
	}

	threads := make([]provider.Thread, 0, len(issueComments)+len(reviews))
	for i := range issueComments {
		c := &issueComments[i]
		threads = append(threads, provider.Thread{ID: idStr(c.ID), Kind: provider.ThreadGeneral,
			Comments: []provider.CommentItem{c.item()}})
	}
	for i := range reviews {
		r := &reviews[i]
		if strings.EqualFold(r.State, "PENDING") || strings.TrimSpace(r.Body) == "" {
			continue
		}
		threads = append(threads, provider.Thread{ID: idStr(r.ID), Kind: provider.ThreadGeneral,
			Comments: []provider.CommentItem{r.item()}})
	}
	nGeneral := len(threads)
	threads = append(threads, inlineThreads(rcs)...)
	provider.SortThreads(threads)
	p.logger.Debug("github comment threads listed", "threads", len(threads), "general", nGeneral,
		"issue_comments", len(issueComments), "reviews", len(reviews), "review_comments", len(rcs))
	return threads, nil
}

// inlineThreads groups review comments into threads by in_reply_to_id. The
// top of a chain of replies is the root; a reply whose parent is not listed
// (deleted) is grouped under that parent's id, and its earliest comment is
// the root.
func inlineThreads(rcs []apiReviewComment) []provider.Thread {
	sort.SliceStable(rcs, func(i, j int) bool {
		if !rcs[i].CreatedAt.Equal(rcs[j].CreatedAt.Time) {
			return rcs[i].CreatedAt.Before(rcs[j].CreatedAt.Time)
		}
		return rcs[i].ID < rcs[j].ID
	})
	byID := make(map[int64]*apiReviewComment, len(rcs))
	for i := range rcs {
		byID[rcs[i].ID] = &rcs[i]
	}
	top := func(c *apiReviewComment) int64 {
		id := c.ID
		for range len(rcs) { // bounded: a cycle cannot loop forever
			cur := byID[id]
			if cur == nil || cur.InReplyToID == nil {
				break
			}
			id = *cur.InReplyToID
		}
		return id
	}
	idx := map[int64]int{}
	var out []provider.Thread
	for i := range rcs {
		c := &rcs[i]
		key := top(c)
		if at, ok := idx[key]; ok {
			out[at].Comments = append(out[at].Comments, c.item())
			continue
		}
		line, outdated := c.anchor()
		idx[key] = len(out)
		out = append(out, provider.Thread{
			ID: idStr(c.ID), Kind: provider.ThreadInline, Path: c.Path, Line: line, Outdated: outdated,
			Comments: []provider.CommentItem{c.item()}, ReplyInThread: true,
		})
	}
	return out
}

// PostComment implements provider.Provider with POST /issues/{n}/comments.
func (p *Provider) PostComment(ctx context.Context, ref provider.PRRef, body string) (*provider.Comment, error) {
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := p.sendJSON(ctx, http.MethodPost, rp+"/issues/"+idStr(ref.Number)+"/comments", map[string]string{"body": body}, &out); err != nil {
		return nil, err
	}
	c := &provider.Comment{URL: out.HTMLURL}
	// The comment exists already: a response without an id is not an error
	// (a retry would post a duplicate).
	if out.ID != 0 {
		c.ID = idStr(out.ID)
	}
	return c, nil
}

// sendJSON sends a JSON body and decodes the JSON response into out (nil:
// the response is ignored).
func (p *Provider) sendJSON(ctx context.Context, method, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return protocolErr("request body could not be encoded")
	}
	resp, err := p.do(ctx, request{method: method, path: path, body: b})
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return protocolErr("response is not valid JSON of the expected shape")
	}
	return nil
}

// urlNamesPR reports whether raw ends with {owner}/{repo}/{issues|pulls}/{n}
// for ref (fragments and queries ignored; owner and repo case-insensitive).
func urlNamesPR(raw string, ref provider.PRRef) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	segs := strings.Split(strings.TrimRight(u.EscapedPath(), "/"), "/")
	if len(segs) < 4 {
		return false
	}
	tail := segs[len(segs)-4:]
	for i := range tail {
		if tail[i], err = url.PathUnescape(tail[i]); err != nil {
			return false
		}
	}
	return strings.EqualFold(tail[0], ref.Namespace) && strings.EqualFold(tail[1], ref.Repo) &&
		(tail[2] == "issues" || tail[2] == "pulls") && tail[3] == idStr(ref.Number)
}

// found is what a comment id names on this pull request. GitHub numbers
// issue comments, review comments and review bodies separately, so one id
// can name up to three things.
type found struct {
	issue  *apiIssueComment
	rc     *apiReviewComment
	review *apiReview
}

func (f found) count() int {
	n := 0
	if f.issue != nil {
		n++
	}
	if f.rc != nil {
		n++
	}
	if f.review != nil {
		n++
	}
	return n
}

// getOptional GETs path into out; a 404 is (false, nil).
func (p *Provider) getOptional(ctx context.Context, path string, out any) (bool, error) {
	if err := p.getJSON(ctx, path, out); err != nil {
		if errors.Is(err, provider.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// locate reads the comment id from the issue comments, the review comments
// and (withReview) the review bodies of the pull request. Anything that does
// not belong to this pull request counts as absent.
func (p *Provider) locate(ctx context.Context, ref provider.PRRef, id string, withReview bool) (found, error) {
	var f found
	rp, err := repoPath(ref)
	if err != nil {
		return f, err
	}
	var ic apiIssueComment
	ok, err := p.getOptional(ctx, rp+"/issues/comments/"+id, &ic)
	if err != nil {
		return f, err
	}
	if ok && ic.IssueURL != "" && urlNamesPR(ic.IssueURL, ref) {
		f.issue = &ic
	}
	var rc apiReviewComment
	if ok, err = p.getOptional(ctx, rp+"/pulls/comments/"+id, &rc); err != nil {
		return f, err
	} else if ok && rc.PullRequestURL != "" && urlNamesPR(rc.PullRequestURL, ref) {
		f.rc = &rc
	}
	if withReview {
		var rv apiReview
		if ok, err = p.getOptional(ctx, rp+"/pulls/"+idStr(ref.Number)+"/reviews/"+id, &rv); err != nil {
			return f, err
		} else if ok && !strings.EqualFold(rv.State, "PENDING") && strings.TrimSpace(rv.Body) != "" {
			f.review = &rv
		}
	}
	switch {
	case f.count() == 0:
		return f, &provider.Error{Class: provider.ClassNotFound, Status: http.StatusNotFound}
	case f.count() > 1:
		return f, protocolErr(NoteAmbiguousID)
	}
	return f, nil
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func quoteHeader(author string) string {
	if author == "" {
		return "> Replying to a comment"
	}
	return "> Replying to @" + oneLine(author)
}

// ReplyToComment implements provider.Provider. A reply to an inline thread
// goes into the thread (POST /pulls/{n}/comments/{id}/replies; a reply's id
// is replaced by its root's). A general comment has no thread on GitHub, so
// the reply is a new PR-level comment that starts with a quote header naming
// the author; InThread is false.
func (p *Provider) ReplyToComment(ctx context.Context, ref provider.PRRef, commentID, body string) (*provider.ReplyResult, error) {
	if err := provider.ValidateReply(commentID, body); err != nil {
		return nil, err
	}
	pp, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	f, err := p.locate(ctx, ref, commentID, true)
	if err != nil {
		return nil, err
	}
	if f.rc != nil {
		root := f.rc.ID
		if f.rc.InReplyToID != nil && *f.rc.InReplyToID > 0 {
			root = *f.rc.InReplyToID
		}
		var out struct {
			ID      int64  `json:"id"`
			HTMLURL string `json:"html_url"`
		}
		if err := p.sendJSON(ctx, http.MethodPost, pp+"/comments/"+idStr(root)+"/replies", map[string]string{"body": body}, &out); err != nil {
			return nil, err
		}
		c := provider.Comment{URL: out.HTMLURL}
		if out.ID != 0 {
			c.ID = idStr(out.ID)
		}
		return &provider.ReplyResult{Comment: c, InThread: true}, nil
	}
	author := ""
	if f.issue != nil {
		author = login(f.issue.User)
	} else {
		author = login(f.review.User)
	}
	c, err := p.PostComment(ctx, ref, quoteHeader(author)+"\n\n"+body)
	if err != nil {
		return nil, err
	}
	return &provider.ReplyResult{Comment: *c, InThread: false}, nil
}

// EditComment implements provider.Provider: PATCH /issues/comments/{id} or
// PATCH /pulls/comments/{id}. The comment is read first and must be written
// by CurrentUser (else not_owner) before the PATCH is sent; GitHub itself
// lets anyone with write access edit another user's comment through some
// tokens, so the check is ours. A review body is not a comment and is not
// editable (not_found).
func (p *Provider) EditComment(ctx context.Context, ref provider.PRRef, commentID, body string) error {
	if err := provider.ValidateEdit(commentID, body); err != nil {
		return err
	}
	rp, err := repoPath(ref)
	if err != nil {
		return err
	}
	f, err := p.locate(ctx, ref, commentID, false)
	if err != nil {
		return err
	}
	path, author := rp+"/issues/comments/"+commentID, (*apiUser)(nil)
	if f.issue != nil {
		author = f.issue.User
	} else {
		path, author = rp+"/pulls/comments/"+commentID, f.rc.User
	}
	me, err := p.CurrentUser(ctx)
	if err != nil {
		return err
	}
	if !provider.IsUser(me, userID(author), login(author)) {
		return &provider.Error{Class: provider.ClassNotOwner}
	}
	return p.sendJSON(ctx, http.MethodPatch, path, map[string]string{"body": body}, nil)
}
