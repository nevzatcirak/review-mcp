package gitea

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

// maxCommentPages bounds pagesDistinct, like httpx's page ceiling.
const maxCommentPages = 1000

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

type apiUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// apiIssueComment is an entry of GET .../issues/{n}/comments and the body of
// GET .../issues/comments/{id}. Field names come from Gitea's swagger and are
// live-verification items.
type apiIssueComment struct {
	ID             int64       `json:"id"`
	User           *apiUser    `json:"user"`
	Body           string      `json:"body"`
	CreatedAt      lenientTime `json:"created_at"`
	UpdatedAt      lenientTime `json:"updated_at"`
	HTMLURL        string      `json:"html_url"`
	IssueURL       string      `json:"issue_url"`
	PullRequestURL string      `json:"pull_request_url"`
	Type           string      `json:"type"`
}

type apiReview struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
}

type apiReviewComment struct {
	ID               int64       `json:"id"`
	User             *apiUser    `json:"user"`
	Body             string      `json:"body"`
	Path             string      `json:"path"`
	Position         int         `json:"position"`
	OriginalPosition int         `json:"original_position"`
	Line             int         `json:"line"`
	OriginalLine     int         `json:"original_line"`
	HTMLURL          string      `json:"html_url"`
	CreatedAt        lenientTime `json:"created_at"`
	UpdatedAt        lenientTime `json:"updated_at"`
	Resolver         *struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"resolver"`
}

func login(u *apiUser) string {
	if u == nil {
		return ""
	}
	return u.Login
}

// isSystemEntry reports an entry whose type is present and not "comment".
func (c *apiIssueComment) isSystemEntry() bool {
	return c.Type != "" && c.Type != "comment"
}

// line is the new-side line of a review comment when known: the line field if
// the server sends one, else position (which Gitea fills with the line
// number), else 0.
//
// Decision: position is used as the line when line is absent, because Gitea's
// position carries the line number; it is 0 whenever position is 0 (an
// outdated comment). This stays a live-verification item.
func (c *apiReviewComment) line() int {
	switch {
	case c.Line > 0:
		return c.Line
	case c.Position > 0:
		return c.Position
	}
	return 0
}

func (c *apiReviewComment) outdated() bool { return c.Position == 0 && c.OriginalPosition != 0 }

func (c *apiReviewComment) resolved() bool {
	return c.Resolver != nil && (c.Resolver.Login != "" || c.Resolver.ID != 0)
}

// groupKey groups review comments into threads: path plus the new-side line
// when the API gives one, else position, else the original line/position of
// an outdated comment.
func (c *apiReviewComment) groupKey() string {
	var k string
	switch {
	case c.Line > 0:
		k = "L" + strconv.Itoa(c.Line)
	case c.Position > 0:
		k = "P" + strconv.Itoa(c.Position)
	case c.OriginalLine > 0:
		k = "OL" + strconv.Itoa(c.OriginalLine)
	default:
		k = "OP" + strconv.Itoa(c.OriginalPosition)
	}
	return c.Path + "\x00" + k
}

func (c *apiReviewComment) item() provider.CommentItem {
	return provider.CommentItem{
		ID: strconv.FormatInt(c.ID, 10), Author: login(c.User), Body: c.Body,
		CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time,
		AuthorID: userID(c.User), AuthorLogin: login(c.User),
	}
}

// pagesDistinct is PagesUntilEmpty that also stops when a page adds no
// unseen id. It is used for the review-comments endpoint, which Gitea may
// serve unpaginated: a server that ignores page= would otherwise repeat the
// same list until the page ceiling.
func pagesDistinct[T any](ctx context.Context, c *httpx.Client, path string, limit int, id func(*T) int64) ([]T, error) {
	var all []T
	seen := map[int64]struct{}{}
	for page := 1; page <= maxCommentPages; page++ {
		var items []T
		q := path + "?page=" + strconv.Itoa(page) + "&limit=" + strconv.Itoa(limit)
		if err := c.GetJSON(ctx, q, &items); err != nil {
			return nil, err
		}
		added := 0
		for i := range items {
			if _, dup := seen[id(&items[i])]; dup {
				continue
			}
			seen[id(&items[i])] = struct{}{}
			all = append(all, items[i])
			added++
		}
		if added == 0 {
			return all, nil
		}
	}
	return nil, protocolErr("pagination did not terminate within the page limit")
}

// reviewComments returns the PR's review comments over all non-pending
// reviews, plus the number of reviews read. PENDING reviews are drafts that
// other users cannot see, so they are skipped.
func (p *Provider) reviewComments(ctx context.Context, pp string) ([]apiReviewComment, int, error) {
	reviews, err := httpx.PagesUntilEmpty[apiReview](ctx, p.client, pp+"/reviews", pageLimit)
	if err != nil {
		return nil, 0, err
	}
	var out []apiReviewComment
	seen := map[int64]struct{}{}
	for _, r := range reviews {
		if strings.EqualFold(r.State, "PENDING") {
			continue
		}
		cs, err := pagesDistinct(ctx, p.client, pp+"/reviews/"+strconv.FormatInt(r.ID, 10)+"/comments", pageLimit,
			func(c *apiReviewComment) int64 { return c.ID })
		if err != nil {
			return nil, 0, err
		}
		for _, c := range cs {
			if _, dup := seen[c.ID]; dup {
				continue
			}
			seen[c.ID] = struct{}{}
			out = append(out, c)
		}
	}
	return out, len(reviews), nil
}

// ListThreads implements provider.Provider.
//
// Every issue comment is its own general thread (Gitea has no PR-level
// threading). Review comments become inline threads grouped by path and
// line; the earliest comment of a group is the root and the thread ID is its
// ID. Resolved comes from the root's resolver, Outdated from
// position == 0 && original_position != 0, and both are live-verification
// items.
func (p *Provider) ListThreads(ctx context.Context, ref provider.PRRef) ([]provider.Thread, error) {
	pp, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	issueComments, err := httpx.PagesUntilEmpty[apiIssueComment](ctx, p.client,
		rp+"/issues/"+strconv.FormatInt(ref.Number, 10)+"/comments", pageLimit)
	if err != nil {
		return nil, err
	}
	rcs, nReviews, err := p.reviewComments(ctx, pp)
	if err != nil {
		return nil, err
	}

	threads := make([]provider.Thread, 0, len(issueComments))
	for i := range issueComments {
		c := &issueComments[i]
		if c.isSystemEntry() {
			continue
		}
		threads = append(threads, provider.Thread{
			ID:   strconv.FormatInt(c.ID, 10),
			Kind: provider.ThreadGeneral,
			Comments: []provider.CommentItem{{
				ID: strconv.FormatInt(c.ID, 10), Author: login(c.User), Body: c.Body,
				CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time,
				// The same id and login EditComment compares (userID, login).
				AuthorID: userID(c.User), AuthorLogin: login(c.User), URL: c.HTMLURL,
			}},
		})
	}
	nGeneral := len(threads)

	sort.SliceStable(rcs, func(i, j int) bool {
		if !rcs[i].CreatedAt.Equal(rcs[j].CreatedAt.Time) {
			return rcs[i].CreatedAt.Before(rcs[j].CreatedAt.Time)
		}
		return rcs[i].ID < rcs[j].ID
	})
	idx := map[string]int{}
	for i := range rcs {
		c := &rcs[i]
		key := c.groupKey()
		if at, ok := idx[key]; ok {
			threads[at].Comments = append(threads[at].Comments, c.item())
			continue
		}
		resolved := c.resolved()
		idx[key] = len(threads)
		threads = append(threads, provider.Thread{
			ID:       strconv.FormatInt(c.ID, 10),
			Kind:     provider.ThreadInline,
			Path:     c.Path,
			Line:     c.line(),
			Outdated: c.outdated(),
			Resolved: &resolved,
			Comments: []provider.CommentItem{c.item()},
		})
	}
	provider.SortThreads(threads)
	p.logger.Debug("gitea comment threads listed", "threads", len(threads), "general", nGeneral,
		"issue_comments", len(issueComments), "reviews", nReviews, "review_comments", len(rcs))
	return threads, nil
}

// issueCommentBelongsToPR checks that a comment fetched by id belongs to this
// PR. Comment IDs are global, so every URL field that is present (issue_url,
// pull_request_url, html_url; fragments and queries are ignored) must end with
// the segments {owner}/{repo}/{issues|pulls}/{n}. Owner and repo are compared
// case-insensitively after unescaping each segment. At least one field must be
// present.
//
// Decision: a comment with none of those fields is treated as not found,
// because it could belong to another repository, issue or PR and a misdirected
// quote would leak its author and path.
func issueCommentBelongsToPR(c *apiIssueComment, ref provider.PRRef) bool {
	checked := 0
	for _, raw := range []string{c.IssueURL, c.PullRequestURL, c.HTMLURL} {
		if raw == "" {
			continue
		}
		if !urlNamesPR(raw, ref) {
			return false
		}
		checked++
	}
	return checked > 0
}

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
		(tail[2] == "issues" || tail[2] == "pulls") && tail[3] == strconv.FormatInt(ref.Number, 10)
}

// oneLine keeps header fragments on a single line.
func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func quoteHeader(author, path string, line int) string {
	h := "> Replying to a comment"
	if author != "" {
		h = "> Replying to @" + oneLine(author)
	}
	switch {
	case path != "" && line > 0:
		h += " on " + oneLine(path) + ":" + strconv.Itoa(line)
	case path != "":
		h += " on " + oneLine(path)
	}
	return h
}

// ReplyToComment implements provider.Provider. Gitea has no reply-to-thread
// endpoint that v1 relies on, so the reply is a new PR-level comment that
// starts with a quote header naming the referenced comment's author and
// anchor. The result has InThread false.
func (p *Provider) ReplyToComment(ctx context.Context, ref provider.PRRef, commentID, body string) (*provider.ReplyResult, error) {
	if err := provider.ValidateReply(commentID, body); err != nil {
		return nil, err
	}
	pp, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	rp, err := repoPath(ref)
	if err != nil {
		return nil, err
	}
	id, _ := strconv.ParseInt(commentID, 10, 64) // validated above

	var author, path string
	var line int
	var ic apiIssueComment
	err = p.client.GetJSON(ctx, rp+"/issues/comments/"+strconv.FormatInt(id, 10), &ic)
	switch {
	case err == nil:
		if !issueCommentBelongsToPR(&ic, ref) {
			return nil, &provider.Error{Class: provider.ClassNotFound, Status: http.StatusNotFound}
		}
		author = login(ic.User)
	case errors.Is(err, provider.ErrNotFound):
		rcs, _, serr := p.reviewComments(ctx, pp)
		if serr != nil {
			return nil, serr
		}
		found := false
		for i := range rcs {
			if rcs[i].ID == id {
				author, path, line, found = login(rcs[i].User), rcs[i].Path, rcs[i].line(), true
				break
			}
		}
		if !found {
			return nil, &provider.Error{Class: provider.ClassNotFound, Status: http.StatusNotFound}
		}
	default:
		return nil, err
	}

	c, err := p.PostComment(ctx, ref, quoteHeader(author, path, line)+"\n\n"+body)
	if err != nil {
		return nil, err
	}
	return &provider.ReplyResult{Comment: *c, InThread: false}, nil
}
