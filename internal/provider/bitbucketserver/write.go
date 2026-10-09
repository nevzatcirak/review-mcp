package bitbucketserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Response headers that name the authenticated user of a request.
const (
	headerUserName = "X-AUSERNAME"
	headerUserID   = "X-AUSERID"
)

// CurrentUser implements provider.Provider.
//
// Bitbucket Server has no "current user" REST resource. Every response to
// an authenticated request carries the X-AUSERNAME header (the user name,
// the "name" field of user and comment-author payloads) and X-AUSERID (the
// numeric "id" field). They are read from GET
// /rest/api/1.0/application-properties, which every authenticated user may
// read and which never fails for a valid token. Endpoints outside /rest (such
// as /plugins/servlet/applinks/whoami) are not used: since Bitbucket Data
// Center 8.9.22, 8.19.12 and 9.3.2, access tokens are honoured only on /rest
// and /scm paths. A response without X-AUSERNAME (an anonymous request) is a
// protocol error. The mechanism is a live-verification item (acceptance I1).
func (p *Provider) CurrentUser(ctx context.Context) (provider.User, error) {
	h, err := p.client.GetHeaders(ctx, apiV1+"/application-properties", headerUserName, headerUserID)
	if err != nil {
		return provider.User{}, err
	}
	name := h[headerUserName]
	if name == "" {
		return provider.User{}, protocolErr("the server did not identify the token's user")
	}
	u := provider.User{Name: name}
	if id, ok := parsePositive(h[headerUserID]); ok {
		u.ID = strconv.FormatInt(id, 10)
	}
	return u, nil
}

// apiEditable is the part of GET .../comments/{id} that an edit needs.
type apiEditable struct {
	ID      int64 `json:"id"`
	Version *int  `json:"version"`
	Author  struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"author"`
}

// EditComment implements provider.Provider.
//
// The comment is read (GET .../pull-requests/{n}/comments/{id}) for its
// version and author; an author other than CurrentUser is refused with
// not_owner before any PUT. The PUT sends {text, version}. A 409 (the
// version changed in between) is retried once after a fresh read, which
// repeats the author check; a second 409 is reported as conflict.
func (p *Provider) EditComment(ctx context.Context, ref provider.PRRef, commentID, body string) error {
	if err := provider.ValidateEdit(commentID, body); err != nil {
		return err
	}
	pp, err := prPath(ref)
	if err != nil {
		return err
	}
	id, _ := strconv.ParseInt(commentID, 10, 64) // validated above
	if err := p.ensureSupported(ctx); err != nil {
		return err
	}
	me, err := p.CurrentUser(ctx)
	if err != nil {
		return err
	}
	path := pp + "/comments/" + strconv.FormatInt(id, 10)
	for attempt := 0; ; attempt++ {
		var c apiEditable
		if err := p.client.GetJSON(ctx, path, &c); err != nil {
			return err
		}
		if c.Version == nil {
			return protocolErr("the comment has no version")
		}
		author := ""
		if c.Author.ID != 0 {
			author = strconv.FormatInt(c.Author.ID, 10)
		}
		if !provider.IsUser(me, author, c.Author.Name) {
			return &provider.Error{Class: provider.ClassNotOwner}
		}
		in := struct {
			Text    string `json:"text"`
			Version int    `json:"version"`
		}{Text: body, Version: *c.Version}
		err := p.client.SendJSON(ctx, http.MethodPut, path, in, nil)
		var pe *provider.Error
		if err == nil || !errors.As(err, &pe) || pe.Status != http.StatusConflict {
			return err
		}
		if attempt > 0 {
			return &provider.Error{Class: provider.ClassConflict, Status: http.StatusConflict,
				Hint: "the comment was changed by someone else at the same time"}
		}
		p.logger.Debug("bitbucket server comment version conflict; retrying once")
	}
}

// UpdatePullRequest implements provider.Provider with PUT
// .../pull-requests/{id}.
//
// Request shape and why (DESIGN-QUESTION in the WP-2d report). The PUT is a
// full update, not a patch: the porting map records that upstream's PR
// update "needs version, title and, critically, the existing reviewers list,
// otherwise reviewers get wiped" and that it re-reads the title when it has
// none, to avoid clobbering it. So the request names every field the
// endpoint may change, each taken from one fresh GET of the PR, whose version
// the caller's up.Version must equal (else a conflict, before any write):
//
//   - version: up.Version, the optimistic lock; a PUT with a stale one is
//     answered 409 and changes nothing.
//   - title and description: the caller's value, or the fresh one when the
//     caller left the field nil, so the PUT never blanks a field it was not
//     asked to change.
//   - reviewers: the fresh list as [{"user": {"name": ...}}], the documented
//     input shape. Retained reviewers keep their status; the fields of the
//     read model (status, approved, lastReviewedCommit) are not sent back, as
//     the endpoint takes the reviewer set and not the verdicts. A reviewer
//     without a user name cannot be named, so the update is refused instead
//     of sending a list that would drop them.
//   - draft: echoed when the fresh PR reports it, so that a server that
//     treats an absent flag as false cannot turn a draft into a ready PR.
//
// The target branch (toRef) is not named, so it is not changed. A 409 is
// reported as a conflict (no retry here: the caller owns the text, so it
// re-reads and recomputes).
func (p *Provider) UpdatePullRequest(ctx context.Context, ref provider.PRRef, up provider.UpdatePR) error {
	if err := provider.ValidateUpdatePR(up); err != nil {
		return err
	}
	if up.Version == "" {
		return protocolErr("missing pull request version")
	}
	version, _ := strconv.Atoi(up.Version) // validated above
	path, err := prPath(ref)
	if err != nil {
		return err
	}
	if err := p.ensureSupported(ctx); err != nil {
		return err
	}
	var cur apiPR
	if err := p.client.GetJSON(ctx, path, &cur); err != nil {
		return err
	}
	if cur.Version == nil {
		return protocolErr("the pull request has no version")
	}
	if *cur.Version != version {
		return &provider.Error{Class: provider.ClassConflict, Hint: "the pull request was changed by someone else"}
	}
	type reviewerIn struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	reviewers := make([]reviewerIn, 0, len(cur.Reviewers))
	for _, r := range cur.Reviewers {
		if r.User.Name == "" {
			return protocolErr("a reviewer cannot be named, so the update was not sent")
		}
		var ri reviewerIn
		ri.User.Name = r.User.Name
		reviewers = append(reviewers, ri)
	}
	in := struct {
		Version     int          `json:"version"`
		Title       string       `json:"title"`
		Description string       `json:"description"`
		Reviewers   []reviewerIn `json:"reviewers"`
		Draft       *bool        `json:"draft,omitempty"`
	}{Version: version, Title: cur.Title, Description: cur.Description, Reviewers: reviewers, Draft: cur.Draft}
	if up.Title != nil {
		in.Title = *up.Title
	}
	if up.Description != nil {
		in.Description = *up.Description
	}
	err = p.client.SendJSON(ctx, http.MethodPut, path, in, nil)
	var pe *provider.Error
	if errors.As(err, &pe) && pe.Status == http.StatusConflict {
		return &provider.Error{Class: provider.ClassConflict, Status: http.StatusConflict,
			Hint: "the pull request was changed by someone else at the same time"}
	}
	return err
}

type apiInlineAnchor struct {
	DiffType string `json:"diffType"`
	Path     string `json:"path"`
	SrcPath  string `json:"srcPath,omitempty"`
	Line     int    `json:"line"`
	LineType string `json:"lineType"`
	FileType string `json:"fileType"`
}

func lineType(t provider.LineType) string {
	if t == provider.LineAdded {
		return "ADDED"
	}
	return "CONTEXT"
}

// PostInlineComments implements provider.Provider: one POST
// .../pull-requests/{n}/comments per item, anchored on the effective diff's
// head side (diffType EFFECTIVE, fileType TO) with the item's line type.
// A renamed file's old path is sent as srcPath. After an auth or rate-limit
// failure the remaining items are reported with the same error and not
// sent. Comment URLs are built as in PostComment.
func (p *Provider) PostInlineComments(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, items []provider.InlineComment) ([]provider.InlineResult, error) {
	if err := provider.ValidateInlineComments(items); err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, protocolErr("the pull request is unknown")
	}
	pp, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []provider.InlineResult{}, nil
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	results := make([]provider.InlineResult, len(items))
	posted, stopMsg := 0, ""
	for i, it := range items {
		if stopMsg != "" {
			results[i] = provider.InlineResult{Error: stopMsg}
			continue
		}
		in := struct {
			Text   string          `json:"text"`
			Anchor apiInlineAnchor `json:"anchor"`
		}{Text: it.Body, Anchor: apiInlineAnchor{
			DiffType: "EFFECTIVE", Path: it.Path, SrcPath: it.OldPath, Line: it.Line,
			LineType: lineType(it.LineType), FileType: "TO",
		}}
		var out struct {
			ID int64 `json:"id"`
		}
		if err := p.client.SendJSON(ctx, http.MethodPost, pp+"/comments", in, &out); err != nil {
			results[i] = provider.InlineResult{Error: provider.ItemError(err)}
			if provider.StopsBatch(err) {
				stopMsg = results[i].Error
			}
			continue
		}
		c := p.newComment(ref, out.ID)
		results[i] = provider.InlineResult{Posted: true, ID: c.ID, URL: c.URL}
		posted++
	}
	p.logger.Debug("bitbucket server inline comments posted", "items", len(items), "posted", posted)
	return results, nil
}
