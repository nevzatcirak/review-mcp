package bitbucketserver

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

type apiAuthor struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

type apiAnchor struct {
	Path     string `json:"path"`
	Line     int    `json:"line"`
	LineType string `json:"lineType"`
	FileType string `json:"fileType"`
	Orphaned bool   `json:"orphaned"`
}

// apiComment is a comment of an activity. Field names come from the
// Bitbucket Server REST documentation and are live-verification items,
// notably state, threadResolved and deleted.
type apiComment struct {
	ID             int64        `json:"id"`
	Text           string       `json:"text"`
	Author         apiAuthor    `json:"author"`
	CreatedDate    int64        `json:"createdDate"`
	UpdatedDate    int64        `json:"updatedDate"`
	State          string       `json:"state"`
	ThreadResolved *bool        `json:"threadResolved"`
	Deleted        bool         `json:"deleted"`
	Anchor         *apiAnchor   `json:"anchor"`
	Comments       []apiComment `json:"comments"`
}

type apiActivity struct {
	Action         string      `json:"action"`
	CommentAction  string      `json:"commentAction"`
	Comment        *apiComment `json:"comment"`
	CommentAnchor  *apiAnchor  `json:"commentAnchor"`
	ThreadResolved *bool       `json:"threadResolved"`
}

func millis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func (c *apiComment) live() bool { return !c.Deleted && strings.TrimSpace(c.Text) != "" }

func (c *apiComment) item() provider.CommentItem {
	author := c.Author.Name
	if author == "" {
		author = c.Author.DisplayName
	}
	return provider.CommentItem{
		ID: strconv.FormatInt(c.ID, 10), Author: author, Body: c.Text,
		CreatedAt: millis(c.CreatedDate), UpdatedAt: millis(c.UpdatedDate),
	}
}

// flattenReplies appends the descendants of c depth-first, children in
// creation order, skipping deleted ones (their own replies are still kept).
func flattenReplies(c *apiComment, deleted map[int64]struct{}, out *[]provider.CommentItem) {
	kids := append([]apiComment(nil), c.Comments...)
	sort.SliceStable(kids, func(i, j int) bool {
		if kids[i].CreatedDate != kids[j].CreatedDate {
			return kids[i].CreatedDate < kids[j].CreatedDate
		}
		return kids[i].ID < kids[j].ID
	})
	for i := range kids {
		k := &kids[i]
		if _, gone := deleted[k.ID]; !gone && k.live() {
			*out = append(*out, k.item())
		}
		flattenReplies(k, deleted, out)
	}
}

// resolvedState maps the server's thread state: threadResolved (on the
// comment, then on the activity) decides when present; otherwise state
// RESOLVED is true and OPEN is false; anything else is unknown (nil).
func resolvedState(root *apiComment, act *apiActivity) *bool {
	var v bool
	switch {
	case root.ThreadResolved != nil:
		v = *root.ThreadResolved
	case act.ThreadResolved != nil:
		v = *act.ThreadResolved
	case root.State == "RESOLVED":
		v = true
	case root.State == "OPEN":
		v = false
	default:
		return nil
	}
	return &v
}

// ListThreads implements provider.Provider.
//
// Decision: the spec puts the side of a FROM-side anchor "in the path note",
// but Thread has no such field, so Path stays clean (the anchor path only) and
// Line is 0 for any anchor whose fileType is not TO, including a missing one.
// Comment IDs seen in COMMENTED/DELETED activities are excluded as well.
func (p *Provider) ListThreads(ctx context.Context, ref provider.PRRef) ([]provider.Thread, error) {
	path, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	acts, err := httpx.PagesStartLimit[apiActivity](ctx, p.client, path+"/activities", pageLimit)
	if err != nil {
		return nil, err
	}

	// Comments removed later show up as COMMENTED/DELETED activities.
	deleted := map[int64]struct{}{}
	for i := range acts {
		if a := &acts[i]; a.Action == "COMMENTED" && a.CommentAction == "DELETED" && a.Comment != nil {
			deleted[a.Comment.ID] = struct{}{}
		}
	}

	threads := []provider.Thread{}
	seen := map[int64]struct{}{}
	nComments := 0
	for i := range acts {
		a := &acts[i]
		if a.Action != "COMMENTED" || a.CommentAction != "ADDED" || a.Comment == nil {
			continue
		}
		root := a.Comment
		if _, gone := deleted[root.ID]; gone || !root.live() {
			continue
		}
		if _, dup := seen[root.ID]; dup {
			continue
		}
		seen[root.ID] = struct{}{}

		anchor := a.CommentAnchor
		if anchor == nil {
			anchor = root.Anchor
		}
		t := provider.Thread{
			ID:            strconv.FormatInt(root.ID, 10),
			Kind:          provider.ThreadGeneral,
			Resolved:      resolvedState(root, a),
			ReplyInThread: true,
			Comments:      []provider.CommentItem{root.item()},
		}
		if anchor != nil && anchor.Path != "" {
			t.Kind, t.Path, t.Outdated = provider.ThreadInline, anchor.Path, anchor.Orphaned
			if anchor.FileType == "TO" {
				t.Line = anchor.Line
			}
		}
		var replies []provider.CommentItem
		flattenReplies(root, deleted, &replies)
		sort.SliceStable(replies, func(i, j int) bool {
			if !replies[i].CreatedAt.Equal(replies[j].CreatedAt) {
				return replies[i].CreatedAt.Before(replies[j].CreatedAt)
			}
			// IDs are decimal; shorter means smaller.
			a, b := replies[i].ID, replies[j].ID
			return len(a) < len(b) || (len(a) == len(b) && a < b)
		})
		t.Comments = append(t.Comments, replies...)
		nComments += len(t.Comments)
		threads = append(threads, t)
	}
	provider.SortThreads(threads)
	p.logger.Debug("bitbucket server comment threads listed", "activities", len(acts),
		"threads", len(threads), "comments", nComments)
	return threads, nil
}

// ReplyToComment implements provider.Provider. The reply is posted inside
// the thread through the parent id; an unknown comment id is the server's
// 404, mapped to not_found.
func (p *Provider) ReplyToComment(ctx context.Context, ref provider.PRRef, commentID, body string) (*provider.ReplyResult, error) {
	if err := provider.ValidateReply(commentID, body); err != nil {
		return nil, err
	}
	path, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	parent, _ := strconv.ParseInt(commentID, 10, 64) // validated above
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	type parentRef struct {
		ID int64 `json:"id"`
	}
	in := struct {
		Text   string    `json:"text"`
		Parent parentRef `json:"parent"`
	}{Text: body, Parent: parentRef{ID: parent}}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := p.client.SendJSON(ctx, http.MethodPost, path+"/comments", in, &out); err != nil {
		return nil, err
	}
	// As in PostComment, a response without an id is not an error: the reply
	// already exists.
	return &provider.ReplyResult{Comment: *p.newComment(ref, out.ID), InThread: true}, nil
}
