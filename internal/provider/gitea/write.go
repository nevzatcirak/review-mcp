package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// Fixed sentences of InlineResult.Error that are not provider error classes.
const (
	errPendingDeleted = "the server kept the review as a pending draft; it was deleted and the comment was not posted"
	errPendingLeft    = "a pending draft review could not be removed; the comment was not posted"
	errOutcomeUnknown = "the outcome of the review request is unknown; the comment may have been posted and was not posted again"
)

// rejected reports whether err is a definite rejection by the server: an
// HTTP response with a non-2xx status (provider.Error.Status is set only
// then). Any other error (no response, a failed body read or an
// undecodable body after a 2xx, a refused redirect) leaves the outcome
// unknown: the review may exist and be submitted, so it must not be posted
// again.
func rejected(err error) bool {
	var pe *provider.Error
	return errors.As(err, &pe) && pe.Status != 0
}

// apiPostedReview is the response of POST .../pulls/{n}/reviews.
type apiPostedReview struct {
	ID      int64    `json:"id"`
	State   string   `json:"state"`
	HTMLURL string   `json:"html_url"`
	User    *apiUser `json:"user"`
}

func isPending(state string) bool { return strings.EqualFold(state, "PENDING") }

func userID(u *apiUser) string {
	if u == nil || u.ID == 0 {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

// CurrentUser implements provider.Provider with GET /api/v1/user.
func (p *Provider) CurrentUser(ctx context.Context) (provider.User, error) {
	var u apiUser
	if err := p.client.GetJSON(ctx, apiPrefix+"/user", &u); err != nil {
		return provider.User{}, err
	}
	if u.Login == "" {
		return provider.User{}, protocolErr("the server did not identify the token's user")
	}
	return provider.User{ID: userID(&u), Name: u.Login}, nil
}

// EditComment implements provider.Provider.
//
// Only PR-level (issue) comments can be edited: Gitea answers GET
// /issues/comments/{id} with 204 and no body for any other comment type,
// which is reported as not_found. The comment is re-read and must pass the
// same repository and PR check as the ReplyToComment lookup (else
// not_found) and be authored by CurrentUser (else not_owner) before the
// PATCH is sent. The author check is ours: Gitea itself lets a user with
// write access to the repository's pull requests edit anyone's comment.
func (p *Provider) EditComment(ctx context.Context, ref provider.PRRef, commentID, body string) error {
	if err := provider.ValidateEdit(commentID, body); err != nil {
		return err
	}
	rp, err := repoPath(ref)
	if err != nil {
		return err
	}
	id, _ := strconv.ParseInt(commentID, 10, 64) // validated above
	path := rp + "/issues/comments/" + strconv.FormatInt(id, 10)

	data, status, err := p.client.Get(ctx, path, httpx.MaxJSONBytes, httpx.JSONCapKey)
	if err != nil {
		return err
	}
	if status == http.StatusNoContent || len(bytes.TrimSpace(data)) == 0 {
		return &provider.Error{Class: provider.ClassNotFound, Hint: "only pull request comments can be edited"}
	}
	var c apiIssueComment
	if json.Unmarshal(data, &c) != nil {
		return protocolErr("response is not valid JSON of the expected shape")
	}
	if !issueCommentBelongsToPR(&c, ref) {
		return &provider.Error{Class: provider.ClassNotFound, Status: http.StatusNotFound}
	}
	me, err := p.CurrentUser(ctx)
	if err != nil {
		return err
	}
	if !provider.IsUser(me, userID(c.User), login(c.User)) {
		return &provider.Error{Class: provider.ClassNotOwner}
	}
	return p.client.SendJSON(ctx, http.MethodPatch, path, map[string]string{"body": body}, nil)
}

// UpdatePullRequest implements provider.Provider with PATCH
// /repos/{owner}/{repo}/pulls/{index}, which sends only the fields that are
// set: Gitea's EditPullRequestOption leaves an absent field as it is, so the
// reviewers, labels, assignees and the base branch are never named, let
// alone changed. Gitea has no version: up.Version is ignored, and the
// caller detects a concurrent edit by comparing the text it read.
func (p *Provider) UpdatePullRequest(ctx context.Context, ref provider.PRRef, up provider.UpdatePR) error {
	if err := provider.ValidateUpdatePR(up); err != nil {
		return err
	}
	path, err := prPath(ref)
	if err != nil {
		return err
	}
	in := map[string]string{}
	if up.Title != nil {
		in["title"] = *up.Title
	}
	if up.Description != nil {
		in["body"] = *up.Description
	}
	return p.client.SendJSON(ctx, http.MethodPatch, path, in, nil)
}

type apiReviewCommentIn struct {
	Path        string `json:"path"`
	Body        string `json:"body"`
	NewPosition int    `json:"new_position"`
	OldPosition int    `json:"old_position"`
}

type apiReviewIn struct {
	Event    string               `json:"event"`
	CommitID string               `json:"commit_id"`
	Body     string               `json:"body"`
	Comments []apiReviewCommentIn `json:"comments"`
}

// PostInlineComments implements provider.Provider.
//
// All items go into one review (POST .../pulls/{n}/reviews, event COMMENT,
// pinned to the head commit, empty body). If that fails, each item is
// posted as its own review and reports its own outcome. Line types are not
// sent: Gitea anchors every new_position on the head side.
//
// Gitea API behaviour this relies on (read from the Gitea 1.24 sources,
// routers/api/v1/repo/pull_review.go and services/pull/review.go; a
// live-verification item):
//   - CreatePullReview first adds every comment to the user's current
//     PENDING review, creating one if none exists, and submits it with the
//     event only after all comments were added. A failure part-way through
//     therefore leaves a PENDING review with the earlier comments.
//   - An existing PENDING review of the same user is reused, so it would be
//     submitted together with our comments. The call is refused when the
//     token's user already has one (a draft started in the web UI).
//   - An event the server does not recognise yields a PENDING review; the
//     response state then is PENDING.
//   - GET .../reviews lists PENDING reviews only to their author (and to
//     admins), and DELETE .../reviews/{id} deletes one of the user's own
//     reviews, PENDING included.
//
// After any failed or PENDING review post, every PENDING review of the
// token's user on the PR is deleted, so none is left behind. If that
// cleanup fails, no further review is posted (it would reuse the draft).
//
// Items are posted again one by one only after a definite outcome: a
// non-2xx response, or a review that came back PENDING (and was deleted).
// Gitea answers 500 for a position outside the diff, so 500 counts. When
// the outcome is unknown (see rejected), the server may already have
// created and submitted the review; reposting would duplicate every
// comment, so the items are reported with errOutcomeUnknown instead. The
// same rule holds for each item of the fallback, which is never retried.
func (p *Provider) PostInlineComments(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, items []provider.InlineComment) ([]provider.InlineResult, error) {
	if err := provider.ValidateInlineComments(items); err != nil {
		return nil, err
	}
	if pr == nil || pr.HeadSHA == "" {
		return nil, protocolErr("the pull request head commit is unknown")
	}
	pp, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []provider.InlineResult{}, nil
	}
	me, err := p.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := p.pendingReviews(ctx, pp, me)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return nil, &provider.Error{Class: provider.ClassConflict,
			Hint: "the token's user has a pending review on this pull request; submit or delete it first"}
	}

	results := make([]provider.InlineResult, len(items))
	rev, err := p.postReview(ctx, pp, pr.HeadSHA, items)
	if err == nil && !isPending(rev.State) {
		p.fillResults(ctx, pp, rev, items, results)
		p.logger.Debug("gitea inline comments posted", "items", len(items), "mode", "batch")
		return results, nil
	}
	// A PENDING review (err == nil) is a definite outcome too.
	definite := err == nil || rejected(err)
	if err == nil {
		err = &provider.Error{Class: provider.ClassProtocol, Hint: "the review was left pending"}
	}
	p.logger.Debug("gitea inline review batch failed",
		"items", len(items), "class", errClass(err), "outcome_known", definite)
	// The cleanup runs on an unknown outcome as well; the outcome sentence
	// then wins, since the review may have been posted.
	cerr := p.deletePending(ctx, pp, me)
	if !definite || cerr != nil {
		msg := errOutcomeUnknown
		if definite {
			msg = errPendingLeft
		}
		for i := range results {
			results[i] = provider.InlineResult{Error: msg, Reason: provider.InlineReasonFailed}
		}
		return results, nil
	}
	if provider.StopsBatch(err) {
		for i := range results {
			results[i] = provider.InlineResult{Error: provider.ItemError(err), Reason: provider.InlineReasonFailed}
		}
		return results, nil
	}

	posted, stopMsg := 0, ""
	for i := range items {
		if stopMsg != "" {
			results[i] = provider.InlineResult{Error: stopMsg, Reason: provider.InlineReasonFailed}
			continue
		}
		rev, err := p.postReview(ctx, pp, pr.HeadSHA, items[i:i+1])
		if err == nil && !isPending(rev.State) {
			p.fillResults(ctx, pp, rev, items[i:i+1], results[i:i+1])
			posted++
			continue
		}
		msg, reason := errPendingDeleted, provider.InlineReasonFailed
		switch {
		case err != nil && rejected(err):
			msg, reason = provider.ItemError(err), provider.RejectedReason(err, true)
		case err != nil:
			msg = errOutcomeUnknown
		}
		switch {
		case p.deletePending(ctx, pp, me) != nil:
			// Any later review would reuse the leftover draft.
			stopMsg = errPendingLeft
			if msg != errOutcomeUnknown {
				msg = errPendingLeft
			}
			reason = provider.InlineReasonFailed
		case err != nil && provider.StopsBatch(err):
			stopMsg = provider.ItemError(err)
		}
		results[i] = provider.InlineResult{Error: msg, Reason: reason}
	}
	p.logger.Debug("gitea inline comments posted", "items", len(items), "mode", "per_item", "posted", posted)
	return results, nil
}

func (p *Provider) postReview(ctx context.Context, pp, headSHA string, items []provider.InlineComment) (*apiPostedReview, error) {
	in := apiReviewIn{Event: "COMMENT", CommitID: headSHA, Body: "", Comments: make([]apiReviewCommentIn, len(items))}
	for i, it := range items {
		in.Comments[i] = apiReviewCommentIn{Path: it.Path, Body: it.Body, NewPosition: it.Line, OldPosition: 0}
	}
	var out apiPostedReview
	if err := p.client.SendJSON(ctx, http.MethodPost, pp+"/reviews", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// pendingReviews returns the ids of the PENDING reviews of me on the PR.
func (p *Provider) pendingReviews(ctx context.Context, pp string, me provider.User) ([]int64, error) {
	reviews, err := httpx.PagesUntilEmpty[apiPostedReview](ctx, p.client, pp+"/reviews", pageLimit)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, r := range reviews {
		if isPending(r.State) && provider.IsUser(me, userID(r.User), login(r.User)) {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

// deletePending deletes every PENDING review of me on the PR. The listing
// is used instead of the id of a failed post because a failed post returns
// no id. A review that is already gone counts as deleted.
func (p *Provider) deletePending(ctx context.Context, pp string, me provider.User) error {
	ids, err := p.pendingReviews(ctx, pp, me)
	if err != nil {
		return err
	}
	for _, id := range ids {
		_, _, err := p.client.Do(ctx, http.MethodDelete, pp+"/reviews/"+strconv.FormatInt(id, 10), nil, "", httpx.MaxJSONBytes, httpx.JSONCapKey)
		if err != nil && !errors.Is(err, provider.ErrNotFound) {
			return err
		}
	}
	p.logger.Debug("gitea pending reviews deleted", "count", len(ids))
	return nil
}

// fillResults marks items as posted in rev and looks up each comment's id
// and URL in the review's comments, matching path, body and line (the first
// unused match wins). When the lookup fails, or a comment is not found, the
// result keeps the review's URL and an empty id.
func (p *Provider) fillResults(ctx context.Context, pp string, rev *apiPostedReview, items []provider.InlineComment, results []provider.InlineResult) {
	for i := range results {
		results[i] = provider.InlineResult{Posted: true, URL: rev.HTMLURL, Reason: provider.InlineReasonPosted}
	}
	if rev.ID == 0 {
		return
	}
	cs, err := pagesDistinct(ctx, p.client, pp+"/reviews/"+strconv.FormatInt(rev.ID, 10)+"/comments", pageLimit,
		func(c *apiReviewComment) int64 { return c.ID })
	if err != nil {
		p.logger.Debug("gitea review comments lookup failed", "class", errClass(err))
		return
	}
	used := make([]bool, len(cs))
	for i, it := range items {
		at := -1
		for j := range cs {
			if used[j] || cs[j].Path != it.Path || cs[j].Body != it.Body {
				continue
			}
			if at < 0 || cs[j].line() == it.Line {
				at = j
			}
			if cs[j].line() == it.Line {
				break
			}
		}
		if at < 0 {
			continue
		}
		used[at] = true
		results[i].ID = strconv.FormatInt(cs[at].ID, 10)
		if cs[at].HTMLURL != "" {
			results[i].URL = cs[at].HTMLURL
		}
	}
}
