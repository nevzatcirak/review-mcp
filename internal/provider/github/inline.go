package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// sideRight is the head side of a diff in GitHub's comment positions.
const sideRight = "RIGHT"

// reviewCommentsPageCap bounds the listing of a posted review's comments
// (100 per page), read only to report each comment's id and URL.
const reviewCommentsPageCap = commentsPageCap

// apiInlineIn is the position and body of one review comment: an entry of
// comments[] of POST /pulls/{n}/reviews, and (with CommitID) the body of
// POST /pulls/{n}/comments. A range sets start_line and start_side; line is
// then its last line.
type apiInlineIn struct {
	CommitID  string `json:"commit_id,omitempty"`
	Path      string `json:"path"`
	Body      string `json:"body"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
}

// apiReviewIn is the body of POST /pulls/{n}/reviews. It has no body of its
// own: the review is only the carrier of its comments, and a review with a
// body would also be listed as a general entry (ListThreads).
type apiReviewIn struct {
	CommitID string        `json:"commit_id"`
	Event    string        `json:"event"`
	Comments []apiInlineIn `json:"comments"`
}

// apiPosted is the part of a created review or review comment that is read.
type apiPosted struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
}

// inlineIn is the position of it on the head side: line alone for one
// line, start_line to line for a range (InlineComment.EndLine). GitHub
// accepts any head-side line of a hunk, added or unchanged, so the line
// type is not sent.
func inlineIn(it *provider.InlineComment) apiInlineIn {
	in := apiInlineIn{Path: it.Path, Body: it.Body, Line: it.Line, Side: sideRight}
	if it.EndLine > it.Line {
		in.StartLine, in.StartSide, in.Line = it.Line, sideRight, it.EndLine
	}
	return in
}

// batchRefused reports whether err is GitHub's refusal of a review as a
// whole: 422 Unprocessable Entity, which it answers when any comment of the
// review cannot be placed on the diff (and creates nothing).
func batchRefused(err error) bool {
	var pe *provider.Error
	return errors.As(err, &pe) && pe.Status == http.StatusUnprocessableEntity
}

// PostInlineComments implements provider.Provider (X-11, Y-14).
//
// All items go into one review: POST /pulls/{n}/reviews with event COMMENT,
// commit_id the head commit and one entry per item (path, line, side RIGHT,
// and start_line and start_side RIGHT for a range). GitHub creates such a
// review, and every comment in it, or nothing.
//
//   - Created: every item is posted. Their ids and URLs are read from GET
//     /pulls/{n}/reviews/{id}/comments, matched by path, body and line;
//     when that read fails, or an item is not found, its result keeps the
//     review's URL and an empty id.
//   - 422: GitHub refuses the whole review when any item is outside the
//     diff, so each item is posted alone through POST /pulls/{n}/comments
//     (commit_id, path, line, side, start_line, start_side) to find the
//     ones it refuses; a refused item's reason comes from
//     provider.RejectedReason (a 422 is unanchorable). After an auth or
//     rate-limit failure, or a canceled context (provider.StopsBatch), the
//     remaining items are reported with the same error and not sent.
//   - Any other failure (an auth or rate-limit failure, another status, no
//     response, a response that cannot be read): every item is reported
//     failed with that error and nothing is posted again, since only a 422
//     says for certain that nothing was created.
//
// Requests: with n items, a created review costs one POST and ceil(n/100)
// GETs (at most reviewCommentsPageCap); a 422 costs that POST plus at most
// one POST per item; any other failure costs the one POST. The rate-limit
// wait of do may repeat a request once. Bodies are never logged.
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
	results := make([]provider.InlineResult, len(items))
	in := apiReviewIn{CommitID: pr.HeadSHA, Event: "COMMENT", Comments: make([]apiInlineIn, len(items))}
	for i := range items {
		in.Comments[i] = inlineIn(&items[i])
	}
	var rev apiPosted
	err = p.sendJSON(ctx, http.MethodPost, pp+"/reviews", in, &rev)
	if err == nil {
		p.fillResults(ctx, pp, &rev, items, results)
		p.logger.Debug("github inline comments posted", "items", len(items), "mode", "review")
		return results, nil
	}
	if !batchRefused(err) {
		for i := range results {
			results[i] = provider.InlineResult{Error: provider.ItemError(err), Reason: provider.InlineReasonFailed}
		}
		p.logger.Debug("github inline review failed", "items", len(items), "error", errClass(err))
		return results, nil
	}
	p.logger.Debug("github inline review refused, posting one by one", "items", len(items))
	posted, stopMsg := 0, ""
	for i := range items {
		if stopMsg != "" {
			results[i] = provider.InlineResult{Error: stopMsg, Reason: provider.InlineReasonFailed}
			continue
		}
		c := inlineIn(&items[i])
		c.CommitID = pr.HeadSHA
		var out apiPosted
		if err := p.sendJSON(ctx, http.MethodPost, pp+"/comments", c, &out); err != nil {
			results[i] = provider.InlineResult{Error: provider.ItemError(err), Reason: provider.RejectedReason(err, false)}
			if provider.StopsBatch(err) {
				stopMsg = results[i].Error
			}
			continue
		}
		results[i] = provider.InlineResult{Posted: true, URL: out.HTMLURL, Reason: provider.InlineReasonPosted}
		if out.ID != 0 {
			results[i].ID = idStr(out.ID)
		}
		posted++
	}
	p.logger.Debug("github inline comments posted", "items", len(items), "mode", "per_item", "posted", posted)
	return results, nil
}

// fillResults marks every item posted in the review rev and looks up each
// comment's id and URL among the review's comments: the first unused one
// with the item's path and body, preferring one on the item's line (the
// last line of a range). Without a review id, or when the listing fails,
// the results keep the review's URL and an empty id.
func (p *Provider) fillResults(ctx context.Context, pp string, rev *apiPosted, items []provider.InlineComment, results []provider.InlineResult) {
	for i := range results {
		results[i] = provider.InlineResult{Posted: true, URL: rev.HTMLURL, Reason: provider.InlineReasonPosted}
	}
	if rev.ID == 0 {
		return
	}
	cs, err := httpx.PagesByLink[apiReviewComment](ctx, p.client, p.fetchPage,
		listPath(pp+"/reviews/"+strconv.FormatInt(rev.ID, 10)+"/comments"), reviewCommentsPageCap)
	if err != nil {
		p.logger.Debug("github review comments lookup failed", "error", errClass(err))
		return
	}
	used := make([]bool, len(cs))
	for i := range items {
		want := inlineIn(&items[i]).Line
		at := -1
		for j := range cs {
			if used[j] || cs[j].Path != items[i].Path || cs[j].Body != items[i].Body {
				continue
			}
			onLine := cs[j].Line != nil && *cs[j].Line == want
			if at < 0 || onLine {
				at = j
			}
			if onLine {
				break
			}
		}
		if at < 0 {
			continue
		}
		used[at] = true
		results[i].ID = idStr(cs[at].ID)
		if cs[at].HTMLURL != "" {
			results[i].URL = cs[at].HTMLURL
		}
	}
}
