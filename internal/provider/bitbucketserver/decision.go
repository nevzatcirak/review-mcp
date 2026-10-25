package bitbucketserver

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// decisionCaps: the participant status of the token's user takes all three
// decisions and is checked against lastReviewedCommit by the server; the
// body is a general comment after it.
var decisionCaps = provider.DecisionCaps{Approve: true, RequestChanges: true, Clear: true, AtomicHead: true}

// participantStatus maps a decision to the participant status it sets.
var participantStatus = map[provider.Decision]string{
	provider.DecisionApprove:        "APPROVED",
	provider.DecisionRequestChanges: "NEEDS_WORK",
	provider.DecisionClear:          "UNAPPROVED",
}

// SetReviewDecision implements provider.DecisionWriter with PUT
// .../pull-requests/{id}/participants/{userSlug} and the body
// {"status": S, "lastReviewedCommit": HeadSHA}, S being APPROVED,
// NEEDS_WORK or UNAPPROVED. The endpoint changes the token's own user's
// status ("implicitly adds the user as a participant"), needs only
// REPO_READ, and fails for the author. The server refuses a
// lastReviewedCommit that is not the latest commit (the status code is a
// live-verification item); any 4xx refusal is checked for a moved head
// with a re-read (provider.DecisionRefused). The user's slug is read first
// (userSlug). A 2xx answer whose status field names another status is a
// protocol error; the body, when there is one, is posted as a general
// comment after the decision (provider.PostDecisionBody).
func (p *Provider) SetReviewDecision(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, req provider.DecisionRequest) (*provider.DecisionResult, error) {
	if err := provider.ValidateDecision(decisionCaps, req); err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, protocolErr("the pull request is unknown")
	}
	path, err := prPath(ref)
	if err != nil {
		return nil, err
	}
	if err := p.ensureSupported(ctx); err != nil {
		return nil, err
	}
	me, err := p.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	slug, err := p.userSlug(ctx, me)
	if err != nil {
		return nil, err
	}
	status := participantStatus[req.Decision]
	in := struct {
		Status             string `json:"status"`
		LastReviewedCommit string `json:"lastReviewedCommit"`
	}{Status: status, LastReviewedCommit: req.HeadSHA}
	var out struct {
		Status string `json:"status"`
	}
	if err := p.client.SendJSON(ctx, http.MethodPut, path+"/participants/"+url.PathEscape(slug), in, &out); err != nil {
		return nil, provider.DecisionRefused(ctx, err, req.HeadSHA, func(ctx context.Context) (*provider.PullRequest, error) {
			return p.GetPullRequest(ctx, ref)
		})
	}
	if out.Status != "" && out.Status != status {
		return nil, protocolErr("the server reported another participant status")
	}
	posted := provider.PostDecisionBody(ctx, p, ref, decisionCaps, req.Body)
	p.logger.Debug("bitbucket server participant status set", "status", status, "body_posted", posted)
	return &provider.DecisionResult{State: status, BodyPosted: posted}, nil
}

// apiUserEntry is an entry of GET /rest/api/1.0/users.
type apiUserEntry struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// userSlug returns the slug of me, the token's user, which is not always
// its user name. It reads GET /rest/api/1.0/users?filter={name} (paged;
// the filter matches user names, display names and e-mail addresses that
// contain it) and takes the entry whose name is me.Name exactly and, when
// me has an ID, whose id is me.ID too. No such entry, or one without a
// slug, is a protocol error.
//
// DESIGN-QUESTION (WP-18a DQ-1): Bitbucket Data Center's REST API 1.0 has
// no resource for the current user (CurrentUser reads the X-AUSERNAME and
// X-AUSERID headers for that reason), and GET /users/{userSlug} needs the
// slug it would give. The users list with filter is the documented way on
// 7.0 to 9.x. Recommendation: keep it.
func (p *Provider) userSlug(ctx context.Context, me provider.User) (string, error) {
	users, err := httpx.PagesStartLimit[apiUserEntry](ctx, p.client, apiV1+"/users?filter="+url.QueryEscape(me.Name), pageLimit)
	if err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Name != me.Name || (me.ID != "" && strconv.FormatInt(u.ID, 10) != me.ID) {
			continue
		}
		if u.Slug == "" {
			break
		}
		return u.Slug, nil
	}
	return "", protocolErr("the token's user slug could not be read")
}
