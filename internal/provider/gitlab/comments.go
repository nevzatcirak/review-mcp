package gitlab

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

// discussionsPageCap bounds the discussions read (perPage items per page).
const discussionsPageCap = 40

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

// apiPosition is the position of a DiffNote. A line that is not on the new
// side (a removed line) has no new_line.
type apiPosition struct {
	NewPath string `json:"new_path"`
	OldPath string `json:"old_path"`
	NewLine *int   `json:"new_line"`
	HeadSHA string `json:"head_sha"`
}

// apiNote is a note of a discussion, and the body of GET .../notes/{id}.
type apiNote struct {
	ID         int64        `json:"id"`
	Body       string       `json:"body"`
	Author     *apiUser     `json:"author"`
	CreatedAt  lenientTime  `json:"created_at"`
	UpdatedAt  lenientTime  `json:"updated_at"`
	System     bool         `json:"system"`
	Resolvable bool         `json:"resolvable"`
	Resolved   bool         `json:"resolved"`
	Position   *apiPosition `json:"position"`
}

type apiDiscussion struct {
	ID    string    `json:"id"`
	Notes []apiNote `json:"notes"`
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

func (n *apiNote) authorLogin() string {
	if n.Author == nil {
		return ""
	}
	return n.Author.Username
}

func (n *apiNote) authorID() string {
	if n.Author == nil || n.Author.ID <= 0 {
		return ""
	}
	return idStr(n.Author.ID)
}

// noteURL is the link of a note: {web_url}#note_{id}.
func noteURL(webURL string, id int64) string {
	return webURL + "#note_" + idStr(id)
}

func (n *apiNote) item(webURL string) provider.CommentItem {
	return provider.CommentItem{
		ID: idStr(n.ID), Author: n.authorLogin(), Body: n.Body,
		CreatedAt: n.CreatedAt.Time, UpdatedAt: n.UpdatedAt.Time,
		AuthorID: n.authorID(), AuthorLogin: n.authorLogin(), URL: noteURL(webURL, n.ID),
	}
}

// userNotes returns the notes of d that people wrote (system notes, such
// as "added 1 commit", are dropped).
func (d *apiDiscussion) userNotes() []apiNote {
	var out []apiNote
	for _, n := range d.Notes {
		if !n.System {
			out = append(out, n)
		}
	}
	return out
}

// discussions reads the discussions of the merge request, Link-paged and
// bounded by discussionsPageCap.
func (p *Provider) discussions(ctx context.Context, mr string) ([]apiDiscussion, error) {
	ds, truncated, err := httpx.PagesByLinkCapped(ctx, p.client, p.fetchPage,
		mr+"/discussions?per_page="+strconv.Itoa(perPage), httpx.Cap{Pages: discussionsPageCap},
		func(pg *[]apiDiscussion) []apiDiscussion { return *pg })
	if err != nil {
		return nil, err
	}
	if truncated {
		p.logger.Debug("gitlab discussion list cut at the page cap", "pages", discussionsPageCap)
	}
	return ds, nil
}

// resolution reports the resolved state of a discussion: nil when none of
// its notes is resolvable (an individual note), else true when every
// resolvable note is resolved.
func resolution(notes []apiNote) *bool {
	resolvable, all := false, true
	for _, n := range notes {
		if n.Resolvable {
			resolvable = true
			all = all && n.Resolved
		}
	}
	if !resolvable {
		return nil
	}
	return &all
}

// ListThreads implements provider.Provider: GET
// /projects/{id}/merge_requests/{iid}/discussions, Link-paged. Each
// discussion with at least one note people wrote is a thread; its ID is the
// first note's id (the discussion id stays internal). A discussion whose
// first note has a position is inline, anchored to the new side (Line 0 for
// a removed line), else general. A DiffNote is outdated when its position's
// head_sha is not the merge request's current head (diff_refs.head_sha, as
// GetPullRequest reports it). Resolved is nil for a discussion that is not
// resolvable. A reply always joins its discussion.
func (p *Provider) ListThreads(ctx context.Context, ref provider.PRRef) ([]provider.Thread, error) {
	mr, err := mrPath(ref)
	if err != nil {
		return nil, err
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return nil, err
	}
	ds, err := p.discussions(ctx, mr)
	if err != nil {
		return nil, err
	}
	threads := make([]provider.Thread, 0, len(ds))
	for i := range ds {
		notes := ds[i].userNotes()
		if len(notes) == 0 {
			continue
		}
		root := &notes[0]
		th := provider.Thread{
			ID: idStr(root.ID), Kind: provider.ThreadGeneral, Resolved: resolution(notes), ReplyInThread: true,
			Comments: make([]provider.CommentItem, 0, len(notes)),
		}
		if pos := root.Position; pos != nil {
			th.Kind = provider.ThreadInline
			th.Path = pos.NewPath
			if th.Path == "" {
				th.Path = pos.OldPath
			}
			if pos.NewLine != nil {
				th.Line = *pos.NewLine
			}
			th.Outdated = pos.HeadSHA != pr.HeadSHA
		}
		for j := range notes {
			th.Comments = append(th.Comments, notes[j].item(pr.WebURL))
		}
		threads = append(threads, th)
	}
	provider.SortThreads(threads)
	p.logger.Debug("gitlab discussions listed", "discussions", len(ds), "threads", len(threads))
	return threads, nil
}

// PostComment implements provider.Provider: POST
// /projects/{id}/merge_requests/{iid}/notes. The comment's URL is
// {web_url}#note_{id} with the merge request's own web_url, as ListThreads
// reports it (mrURL); the response carries none.
func (p *Provider) PostComment(ctx context.Context, ref provider.PRRef, body string) (*provider.Comment, error) {
	mr, err := mrPath(ref)
	if err != nil {
		return nil, err
	}
	web, err := p.mrURL(ctx, ref, mr)
	if err != nil {
		return nil, err
	}
	var out apiNote
	if err := p.sendJSON(ctx, http.MethodPost, mr+"/notes", map[string]string{"body": body}, &out); err != nil {
		return nil, err
	}
	return postedComment(web, out.ID), nil
}

// mrURL reads the merge request's web_url with a plain GET of the merge
// request, before any write, so that a failed read leaves nothing posted.
// GetPullRequest is not used: it waits and fails while diff_refs are not
// ready, and a note needs no diff, so a merge request whose diff is not
// ready yet can still be commented on. A merge request without a web_url
// gets the URL built from the web base (mrWebURL).
func (p *Provider) mrURL(ctx context.Context, ref provider.PRRef, mr string) (string, error) {
	var m struct {
		WebURL string `json:"web_url"`
	}
	if err := p.getJSON(ctx, mr, &m); err != nil {
		return "", err
	}
	if m.WebURL == "" {
		return p.mrWebURL(ref), nil
	}
	return m.WebURL, nil
}

// postedComment builds the result of a write. The note exists already: a
// response without an id is not an error (a retry would post a duplicate).
func postedComment(web string, id int64) *provider.Comment {
	if id == 0 {
		return &provider.Comment{}
	}
	return &provider.Comment{ID: idStr(id), URL: noteURL(web, id)}
}

// mrWebURL is {base}/{full_path}/-/merge_requests/{iid}, the web_url
// fallback for a merge request that reports none.
func (p *Provider) mrWebURL(ref provider.PRRef) string {
	return p.webURL + "/" + provider.EscapeNamespace(ref.Namespace) + "/" + url.PathEscape(ref.Repo) +
		"/-/merge_requests/" + idStr(ref.Number)
}

// ReplyToComment implements provider.Provider. It finds the discussion that
// holds the note commentID (a note that is on no listed discussion is
// not_found) and posts the reply to it: POST .../discussions/{id}/notes.
// The reply is in thread for general and inline discussions alike. Its URL
// is built from the merge request's web_url, read (mrURL) before the post.
func (p *Provider) ReplyToComment(ctx context.Context, ref provider.PRRef, commentID, body string) (*provider.ReplyResult, error) {
	if err := provider.ValidateReply(commentID, body); err != nil {
		return nil, err
	}
	mr, err := mrPath(ref)
	if err != nil {
		return nil, err
	}
	ds, err := p.discussions(ctx, mr)
	if err != nil {
		return nil, err
	}
	did := ""
find:
	for i := range ds {
		for _, n := range ds[i].userNotes() {
			if idStr(n.ID) == commentID {
				did = ds[i].ID
				break find
			}
		}
	}
	if did == "" || !validDiscussionID(did) {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	web, err := p.mrURL(ctx, ref, mr)
	if err != nil {
		return nil, err
	}
	var out apiNote
	if err := p.sendJSON(ctx, http.MethodPost, mr+"/discussions/"+did+"/notes", map[string]string{"body": body}, &out); err != nil {
		return nil, err
	}
	return &provider.ReplyResult{Comment: *postedComment(web, out.ID), InThread: true}, nil
}

// validDiscussionID reports whether id is safe as a path segment: GitLab's
// discussion ids are hexadecimal.
func validDiscussionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// EditComment implements provider.Provider. It reads the note (GET
// .../notes/{id}; a system note or a missing one is not_found), refuses
// with not_owner when its author is not CurrentUser, and only then sends
// PUT .../notes/{id} with the body.
func (p *Provider) EditComment(ctx context.Context, ref provider.PRRef, commentID, body string) error {
	if err := provider.ValidateEdit(commentID, body); err != nil {
		return err
	}
	mr, err := mrPath(ref)
	if err != nil {
		return err
	}
	path := mr + "/notes/" + commentID
	var n apiNote
	if err := p.getJSON(ctx, path, &n); err != nil {
		return err
	}
	if n.System {
		return &provider.Error{Class: provider.ClassNotFound}
	}
	me, err := p.CurrentUser(ctx)
	if err != nil {
		return err
	}
	if !provider.IsUser(me, n.authorID(), n.authorLogin()) {
		return &provider.Error{Class: provider.ClassNotOwner}
	}
	return p.sendJSON(ctx, http.MethodPut, path, map[string]string{"body": body}, nil)
}
