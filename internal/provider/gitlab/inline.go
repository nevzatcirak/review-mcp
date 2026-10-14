package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Fixed sentences of InlineResult.Error.
const (
	// ErrStrayNoteLeft is the error of an item GitLab created as a general
	// note (it could not place the position) that could not be removed: the
	// delete failed, or the note could not be shown to be the token user's.
	ErrStrayNoteLeft = "an inline comment GitLab could not place was posted as a general comment and could not be removed"
	// ErrStrayNoteRemoved is the error of an item GitLab created as a
	// general note that was removed again at once: the comment is
	// unanchorable.
	ErrStrayNoteRemoved = "GitLab could not place the inline comment on its line; the general comment it created instead was removed"
	// errNoNote is the hint of a created discussion whose response names no
	// note: the outcome cannot be checked.
	errNoNote = "the created discussion's note was not reported"
)

// noteTypeDiff is the type of a note GitLab placed on a diff position.
const noteTypeDiff = "DiffNote"

// positionTypeText is GitLab's position type of a comment on a text line.
const positionTypeText = "text"

// apiPositionIn is the position of POST .../discussions. A comment on an
// added line has new_line only; one on an unchanged line both old_line and
// new_line.
type apiPositionIn struct {
	PositionType string `json:"position_type"`
	BaseSHA      string `json:"base_sha"`
	StartSHA     string `json:"start_sha"`
	HeadSHA      string `json:"head_sha"`
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	NewLine      int    `json:"new_line"`
	OldLine      int    `json:"old_line,omitempty"`
}

type apiDiscussionIn struct {
	Body     string        `json:"body"`
	Position apiPositionIn `json:"position"`
}

// apiCreatedNote is the part of a created discussion's note that is read.
type apiCreatedNote struct {
	ID     int64    `json:"id"`
	Type   *string  `json:"type"`
	Author *apiUser `json:"author"`
}

type apiCreatedDiscussion struct {
	Notes []apiCreatedNote `json:"notes"`
}

func (n *apiCreatedNote) diffNote() bool { return n.Type != nil && *n.Type == noteTypeDiff }

// positionIn is the position of it on the revisions of pr. The old path is
// the file's old path for a rename and its path otherwise. A range is
// anchored on its first line (EndLine is ignored, as on Gitea).
func positionIn(it *provider.InlineComment, pr *provider.PullRequest) apiPositionIn {
	pos := apiPositionIn{
		PositionType: positionTypeText, BaseSHA: pr.BaseSHA, StartSHA: pr.StartSHA, HeadSHA: pr.HeadSHA,
		OldPath: it.Path, NewPath: it.Path, NewLine: it.Line,
	}
	if it.OldPath != "" {
		pos.OldPath = it.OldPath
	}
	if it.LineType == provider.LineContext {
		pos.OldLine = it.OldLine
	}
	return pos
}

// positionRefused is the httpx.Request.ErrorProbe of an inline post: it
// reports whether a 400 answer is GitLab's refusal of the note's position,
// a validation error that names the position or the line code. GitLab
// sends it as {"message": {"line_code": [...], ...}}, or as
// {"message": "400 Bad request - Note {:line_code=>[...]}"} (the note's
// errors printed as a Ruby hash, "line_code:" since Ruby 3.4). Only the
// yes-or-no answer is kept; no byte of the message leaves the probe.
// DESIGN-QUESTION WP-6c DQ-c2: both forms are accepted until a live
// GitLab shows which one its discussions endpoint sends.
func positionRefused(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	var e struct {
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(body, &e) != nil || len(e.Message) == 0 {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(e.Message, &fields) == nil {
		_, pos := fields["position"]
		_, code := fields["line_code"]
		return pos || code
	}
	var msg string
	if json.Unmarshal(e.Message, &msg) != nil {
		return false
	}
	hash, ok := strings.CutPrefix(msg, "400 Bad request - Note {")
	if !ok {
		return false
	}
	for _, key := range []string{"position", "line_code"} {
		for _, form := range []string{":" + key + "=>", "\"" + key + "\"=>", key + ": "} {
			if strings.HasPrefix(hash, form) || strings.Contains(hash, ", "+form) {
				return true
			}
		}
	}
	return false
}

// PostInlineComments implements provider.Provider (spec 2C §3 items 1 and
// 3). Each item is one discussion: POST
// /projects/{id}/merge_requests/{iid}/discussions with the body and a text
// position on the merge request's diff_refs (base_sha, start_sha,
// head_sha), new_path and old_path, and new_line for an added line or
// old_line and new_line for an unchanged one (InlineComment.OldLine). A
// range is anchored on its first line.
//
//   - 201 whose note is a DiffNote: posted; the id is the note's and the URL
//     {web_url}#note_{id} (the merge request's web_url, as ListThreads
//     reports it; built from the web base when pr has none).
//   - 201 whose note is not a DiffNote: GitLab could not place the position
//     and created a general note instead. That note, the one just created,
//     is deleted at once (DELETE .../notes/{id}) after its author is shown to
//     be CurrentUser, and the item is unanchorable. When the author check or
//     the delete fails, the item is failed with ErrStrayNoteLeft. No other
//     note is ever deleted.
//   - 400 whose error names the position or the line code
//     (positionRefused): unanchorable. Any other failure: failed. The error
//     is the fixed sentence of its class; no server text is kept.
//
// After an auth or rate-limit failure, or a canceled context
// (provider.StopsBatch), the remaining items are reported with the same
// error and not sent. A pr without start_sha (one GetPullRequest did not
// return) is read again first, and refused when its head moved.
func (p *Provider) PostInlineComments(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, items []provider.InlineComment) ([]provider.InlineResult, error) {
	if err := provider.ValidateInlineComments(items); err != nil {
		return nil, err
	}
	if pr == nil || pr.HeadSHA == "" {
		return nil, protocolErr("the pull request head commit is unknown")
	}
	mr, err := mrPath(ref)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []provider.InlineResult{}, nil
	}
	if pr.StartSHA == "" || pr.BaseSHA == "" {
		// DESIGN-QUESTION WP-6c DQ-c3: a caller's pull request without
		// diff_refs' start_sha (GetPullRequest always sets it) is read again
		// rather than refused or posted with a guessed start.
		fresh, err := p.GetPullRequest(ctx, ref)
		if err != nil {
			return nil, err
		}
		if fresh.HeadSHA != pr.HeadSHA {
			return nil, &provider.Error{Class: provider.ClassConflict, Hint: "the merge request's head changed; read it again"}
		}
		pr = fresh
	}
	results := make([]provider.InlineResult, len(items))
	var me *provider.User
	posted, stopMsg := 0, ""
	for i := range items {
		if stopMsg != "" {
			results[i] = provider.InlineResult{Error: stopMsg, Reason: provider.InlineReasonFailed}
			continue
		}
		var res provider.InlineResult
		res, stopMsg = p.postInline(ctx, ref, mr, pr, &items[i], &me)
		results[i] = res
		if res.Posted {
			posted++
		}
	}
	p.logger.Debug("gitlab inline comments posted", "items", len(items), "posted", posted)
	return results, nil
}

// postInline posts one item and returns its result, and the error sentence
// that stops the batch ("" to go on). me caches CurrentUser for the
// stray-note guard.
func (p *Provider) postInline(ctx context.Context, ref provider.PRRef, mr string, pr *provider.PullRequest,
	it *provider.InlineComment, me **provider.User) (provider.InlineResult, string) {
	fail := func(err error, reason provider.InlineReason) (provider.InlineResult, string) {
		r := provider.InlineResult{Error: provider.ItemError(err), Reason: reason}
		if provider.StopsBatch(err) {
			return r, r.Error
		}
		return r, ""
	}
	b, err := json.Marshal(apiDiscussionIn{Body: it.Body, Position: positionIn(it, pr)})
	if err != nil {
		return fail(protocolErr("request body could not be encoded"), provider.InlineReasonFailed)
	}
	resp, err := p.do(ctx, request{method: http.MethodPost, path: mr + "/discussions", body: b, probe: positionRefused})
	if err != nil {
		if resp != nil && resp.ErrorProbed {
			return fail(err, provider.InlineReasonUnanchorable)
		}
		return fail(err, provider.InlineReasonFailed)
	}
	var d apiCreatedDiscussion
	if json.Unmarshal(resp.Data, &d) != nil {
		return fail(protocolErr("response is not valid JSON of the expected shape"), provider.InlineReasonFailed)
	}
	if len(d.Notes) == 0 || d.Notes[0].ID <= 0 {
		return fail(protocolErr(errNoNote), provider.InlineReasonFailed)
	}
	n := &d.Notes[0]
	if n.diffNote() {
		// The URL is built from the merge request's web_url, as ListThreads
		// builds it, so that an X-29 link of a later run is the same URL.
		web := pr.WebURL
		if web == "" {
			web = p.mrWebURL(ref)
		}
		return provider.InlineResult{Posted: true, ID: idStr(n.ID), URL: noteURL(web, n.ID),
			Reason: provider.InlineReasonPosted}, ""
	}
	return p.removeStray(ctx, mr, n, me)
}

// removeStray deletes the general note n that GitLab created for an inline
// item it could not place, the note just created, and only when its author
// is the token's user. It never touches another note.
func (p *Provider) removeStray(ctx context.Context, mr string, n *apiCreatedNote, me **provider.User) (provider.InlineResult, string) {
	left := provider.InlineResult{Error: ErrStrayNoteLeft, Reason: provider.InlineReasonFailed}
	if *me == nil {
		u, err := p.CurrentUser(ctx)
		if err != nil {
			p.logger.Debug("gitlab stray note kept: the token's user could not be read", "class", errClass(err))
			return left, stopSentence(err)
		}
		*me = &u
	}
	author := apiNote{Author: n.Author}
	if !provider.IsUser(**me, author.authorID(), author.authorLogin()) {
		p.logger.Debug("gitlab stray note kept: it is not the token user's")
		return left, ""
	}
	if _, err := p.do(ctx, request{method: http.MethodDelete, path: mr + "/notes/" + idStr(n.ID)}); err != nil {
		p.logger.Debug("gitlab stray note could not be deleted", "class", errClass(err))
		return left, stopSentence(err)
	}
	p.logger.Debug("gitlab stray note deleted")
	return provider.InlineResult{Error: ErrStrayNoteRemoved, Reason: provider.InlineReasonUnanchorable}, ""
}

// stopSentence is the sentence that stops a batch after err, or "".
func stopSentence(err error) string {
	if provider.StopsBatch(err) {
		return provider.ItemError(err)
	}
	return ""
}
