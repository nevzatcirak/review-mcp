package gitea

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// apiPRReview is an entry of GET .../pulls/{n}/reviews. Body is read only to
// look for review-mcp's markers; it is never stored in a result or logged.
type apiPRReview struct {
	ID          int64       `json:"id"`
	User        *apiUser    `json:"user"`
	State       string      `json:"state"`
	Body        string      `json:"body"`
	Stale       bool        `json:"stale"`
	Official    bool        `json:"official"`
	Dismissed   bool        `json:"dismissed"`
	SubmittedAt lenientTime `json:"submitted_at"`
	UpdatedAt   lenientTime `json:"updated_at"`
}

func (r *apiPRReview) at() time.Time {
	if !r.SubmittedAt.IsZero() {
		return r.SubmittedAt.Time
	}
	return r.UpdatedAt.Time
}

// userKey identifies a user across the PR payload and the reviews: the
// numeric id when the server sent one, else the lower-cased login.
func userKey(id, login string) string {
	if id != "" {
		return "id:" + id
	}
	return "login:" + strings.ToLower(login)
}

// userState is the verdict of one user while the reviews are folded.
type userState struct {
	user      provider.User
	display   string
	decisive  *apiPRReview
	comment   *apiPRReview
	requested bool
}

// later reports whether a is after b (time, then id).
func later(a, b *apiPRReview) bool {
	if !a.at().Equal(b.at()) {
		return a.at().After(b.at())
	}
	return a.ID > b.ID
}

// GetReviewStatus implements provider.Provider.
//
// Reviews: per user, the latest non-dismissed APPROVED or REQUEST_CHANGES
// review decides; COMMENT counts only when there is no decisive one. PENDING
// drafts (not visible for other users anyway) and REQUEST_REVIEW markers are
// ignored. A review of the token's user that carries review-mcp's markers
// (in its body or in one of its comments; the inline batch has an empty
// body) is review-mcp's own activity and is not a reviewer. The
// official flag is only counted in the debug log.
//
// Required approvals come from GET .../branch_protections/{target}; any
// failure (403 and 404 in practice) leaves them nil. Gitea gives no
// structured merge blockers: only Mergeable (from the PR) is reported.
func (p *Provider) GetReviewStatus(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, opts provider.ReviewStatusOptions) *provider.ReviewStatus {
	st := &provider.ReviewStatus{MergeBlockers: []string{}}
	if pr == nil {
		return st
	}
	if pr.State == "" || strings.EqualFold(pr.State, "open") {
		st.Mergeable = pr.Mergeable
	}
	pp, err := prPath(ref)
	if err != nil {
		st.Notes = append(st.Notes, provider.NoteReviewsUnreadable)
		return st
	}
	rp, _ := repoPath(ref) // pp built, so rp is valid

	// The requested reviewers come from the PR payload, read again so that
	// GetPullRequest keeps its shape.
	var payload apiPR
	err = p.client.GetJSON(ctx, pp, &payload)
	var reviews []apiPRReview
	if err == nil {
		reviews, err = httpx.PagesUntilEmpty[apiPRReview](ctx, p.client, pp+"/reviews", pageLimit)
	}
	if err != nil {
		p.logger.Debug("gitea reviews not read", "class", errClass(err))
		st.Notes = append(st.Notes, provider.NoteReviewsUnreadable)
	} else {
		st.Reviewers = p.foldReviews(ctx, pp, payload.RequestedReviewers, reviews, opts, st)
	}

	if pr.TargetBranch != "" {
		var bp struct {
			RequiredApprovals *int `json:"required_approvals"`
		}
		if err := p.client.GetJSON(ctx, rp+"/branch_protections/"+url.PathEscape(pr.TargetBranch), &bp); err != nil {
			p.logger.Debug("gitea branch protection not read", "class", errClass(err))
		} else if bp.RequiredApprovals != nil && *bp.RequiredApprovals >= 0 {
			st.RequiredApprovals = bp.RequiredApprovals
		}
	}
	return st
}

func (p *Provider) foldReviews(ctx context.Context, pp string, requested []apiUser, reviews []apiPRReview, opts provider.ReviewStatusOptions, st *provider.ReviewStatus) []provider.Reviewer {
	users := map[string]*userState{}
	var order []string
	get := func(id, login, display string) *userState {
		k := userKey(id, login)
		u, ok := users[k]
		if !ok {
			u = &userState{user: provider.User{ID: id, Name: login}, display: display}
			users[k] = u
			order = append(order, k)
		}
		if u.display == "" {
			u.display = display
		}
		return u
	}
	for i := range requested {
		if requested[i].Login != "" {
			get(userID(&requested[i]), requested[i].Login, requested[i].FullName).requested = true
		}
	}

	official, own, unclassified := 0, 0, false
	for i := range reviews {
		r := &reviews[i]
		if r.User == nil || r.User.Login == "" || r.Dismissed {
			continue
		}
		var kind string
		switch strings.ToUpper(r.State) {
		case "APPROVED", "REQUEST_CHANGES":
			kind = "decisive"
		case "COMMENT":
			kind = "comment"
		default: // PENDING, REQUEST_REVIEW, unknown
			continue
		}
		if kind == "comment" && opts.Me != nil && opts.IsOwn != nil &&
			provider.IsUser(*opts.Me, userID(r.User), r.User.Login) {
			marked, ok := p.ownReview(ctx, pp, r, opts.IsOwn)
			if !ok {
				unclassified = true
				continue
			}
			if marked {
				own++
				continue
			}
		}
		if r.Official {
			official++
		}
		u := get(userID(r.User), r.User.Login, r.User.FullName)
		if kind == "decisive" {
			if u.decisive == nil || later(r, u.decisive) {
				u.decisive = r
			}
		} else if u.comment == nil || later(r, u.comment) {
			u.comment = r
		}
	}
	if unclassified {
		st.Notes = append(st.Notes, provider.NoteActivityUnclassified)
	}

	out := make([]provider.Reviewer, 0, len(order))
	for _, k := range order {
		u := users[k]
		rv := provider.Reviewer{User: u.user, DisplayName: u.display, Requested: u.requested}
		switch {
		case u.decisive != nil:
			rv.State = provider.ReviewApproved
			if strings.EqualFold(u.decisive.State, "REQUEST_CHANGES") {
				rv.State = provider.ReviewChangesRequested
			}
			rv.Stale, rv.At = u.decisive.Stale, u.decisive.at()
		case u.comment != nil:
			rv.State = provider.ReviewCommented
			rv.Stale, rv.At = u.comment.Stale, u.comment.at()
		case u.requested:
			rv.State = provider.ReviewPending
		default:
			continue // only dismissed or ignored reviews
		}
		out = append(out, rv)
	}
	// Reviewers who have given a verdict first, oldest first; the requested
	// but silent ones after them, in the order of the request.
	sort.SliceStable(out, func(i, j int) bool {
		zi, zj := out[i].At.IsZero(), out[j].At.IsZero()
		switch {
		case zi != zj:
			return !zi
		case !zi && !out[i].At.Equal(out[j].At):
			return out[i].At.Before(out[j].At)
		}
		return false
	})
	p.logger.Debug("gitea reviews folded", "reviews", len(reviews), "reviewers", len(out),
		"official", official, "own_excluded", own)
	return out
}

// ownReview reports whether review r of the token's user is review-mcp's own:
// a marker in its body or in one of its comments. ok is false when its
// comments could not be read. The bodies are inspected, never kept.
func (p *Provider) ownReview(ctx context.Context, pp string, r *apiPRReview, isOwn func(string) bool) (marked, ok bool) {
	if isOwn(r.Body) {
		return true, true
	}
	cs, err := pagesDistinct(ctx, p.client, pp+"/reviews/"+strconv.FormatInt(r.ID, 10)+"/comments", pageLimit,
		func(c *apiReviewComment) int64 { return c.ID })
	if err != nil {
		p.logger.Debug("gitea review comments not read", "class", errClass(err))
		return false, false
	}
	for i := range cs {
		if isOwn(cs[i].Body) {
			return true, true
		}
	}
	return false, true
}
