package github

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// rulesPageCap bounds the listing of the rules that apply to a branch.
const rulesPageCap = 5

// maxListedCommits is the most commits GitHub lists for a pull request
// (GET /pulls/{n}/commits stops there).
const maxListedCommits = 250

// apiTeam is an entry of requested_teams.
type apiTeam struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// apiStatusPR is the part of the pull request payload GetReviewStatus
// reads again: the requested reviewers and teams, and the commit count.
type apiStatusPR struct {
	RequestedReviewers []apiUser `json:"requested_reviewers"`
	RequestedTeams     []apiTeam `json:"requested_teams"`
	Commits            int       `json:"commits"`
}

// apiStatusReview is an entry of GET /pulls/{n}/reviews. Body is read only to
// look for review-mcp's markers; it is never stored in a result or logged.
type apiStatusReview struct {
	ID          int64       `json:"id"`
	User        *apiUser    `json:"user"`
	Body        string      `json:"body"`
	State       string      `json:"state"`
	CommitID    string      `json:"commit_id"`
	SubmittedAt lenientTime `json:"submitted_at"`
}

// later reports whether a is after b (time, then id).
func later(a, b *apiStatusReview) bool {
	if !a.SubmittedAt.Equal(b.SubmittedAt.Time) {
		return a.SubmittedAt.After(b.SubmittedAt.Time)
	}
	return a.ID > b.ID
}

// userKey identifies a user across the PR payload and the reviews: the
// numeric id when the server sent one, else the lower-cased login.
func userKey(u *apiUser) string {
	if id := userID(u); id != "" {
		return "id:" + id
	}
	return "login:" + strings.ToLower(login(u))
}

// userState is the verdict of one reviewer while the reviews are folded.
type userState struct {
	user      provider.User
	decisive  *apiStatusReview
	comment   *apiStatusReview
	requested bool
	display   string
}

// GetReviewStatus implements provider.Provider (Y-15).
//
// Reviewers: the requested reviewers of the pull request, requested teams
// (listed as "@{owner}/{slug}", a name no user login can have, with the
// team's name as display name), and the reviews folded per user: the latest
// APPROVED or CHANGES_REQUESTED decides; DISMISSED never counts (GitHub
// turns the dismissed review itself into a DISMISSED one); COMMENTED counts
// only when the user has no decisive review; PENDING drafts are not
// reported. A review is stale when its commit_id is not the head. A
// COMMENTED review of the token's user that carries review-mcp's markers (in
// its body or in one of its comments: the inline batch has no body) is
// review-mcp's own activity and is not a reviewer.
//
// Required approvals: see readRequiredApprovals. Merge status: Mergeable is
// the pull request's own verdict, and the blockers come from its
// mergeable_state (blockersOf); "unstable" gives the note
// NoteChecksNotRequired instead of a blocker. A pull request with more commits than GitHub
// lists gets the NoteCommitsTruncated note.
func (p *Provider) GetReviewStatus(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, opts provider.ReviewStatusOptions) *provider.ReviewStatus {
	st := &provider.ReviewStatus{MergeBlockers: []string{}}
	if pr == nil {
		return st
	}
	open := isOpen(pr)
	if open {
		st.Mergeable = pr.Mergeable
		st.MergeBlockers = blockersOf(pr.MergeableState)
		if strings.EqualFold(strings.TrimSpace(pr.MergeableState), "unstable") {
			st.Notes = append(st.Notes, provider.NoteChecksNotRequired)
		}
	}
	pp, err := prPath(ref)
	if err != nil {
		st.Notes = append(st.Notes, provider.NoteReviewsUnreadable)
		return st
	}
	rp, _ := repoPath(ref) // pp built, so rp is valid

	var payload apiStatusPR
	err = p.getJSON(ctx, pp, &payload)
	var reviews []apiStatusReview
	if err == nil {
		reviews, err = httpx.PagesByLink[apiStatusReview](ctx, p.client, p.fetchPage, listPath(pp+"/reviews"), commentsPageCap)
	}
	if err != nil {
		p.logger.Debug("github reviews not read", "class", errClass(err))
		st.Notes = append(st.Notes, provider.NoteReviewsUnreadable)
	} else {
		st.Reviewers = p.foldReviews(ctx, ref, pp, pr.HeadSHA, &payload, reviews, opts, st)
		if payload.Commits > maxListedCommits {
			st.Notes = append(st.Notes, provider.NoteCommitsTruncated)
		}
	}

	if pr.TargetBranch != "" {
		p.readRequiredApprovals(ctx, rp, pr.TargetBranch, st)
	}
	return st
}

// isOpen reports whether pr is an open, unmerged pull request.
func isOpen(pr *provider.PullRequest) bool {
	return !pr.Merged && (pr.State == "" || strings.EqualFold(pr.State, "open"))
}

// blockersOf maps GitHub's mergeable_state to fixed blocker texts; the state
// is an enum and never shown as it is.
//
//	clean, has_hooks    no blocker
//	unknown, ""         no blocker: not computed yet (Mergeable is nil too)
//	dirty               merge conflict
//	blocked             required reviews or checks not satisfied
//	behind              the branch is behind the base
//	unstable            no blocker: mergeable, only checks that are not
//	                    required fail or are pending (a note, see
//	                    GetReviewStatus)
//	draft               draft
//	anything else       other merge check
func blockersOf(state string) []string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "clean", "has_hooks", "unknown", "", "unstable":
		return []string{}
	case "dirty":
		return []string{provider.BlockerConflict}
	case "blocked":
		return []string{provider.BlockerRequirements}
	case "behind":
		return []string{provider.BlockerBehind}
	case "draft":
		return []string{provider.BlockerDraft}
	}
	return []string{provider.BlockerOtherCheck}
}

func (p *Provider) foldReviews(ctx context.Context, ref provider.PRRef, pp, head string, payload *apiStatusPR, reviews []apiStatusReview, opts provider.ReviewStatusOptions, st *provider.ReviewStatus) []provider.Reviewer {
	users := map[string]*userState{}
	var order []string
	add := func(key string, u *userState) *userState {
		if have, ok := users[key]; ok {
			return have
		}
		users[key] = u
		order = append(order, key)
		return u
	}
	for i := range payload.RequestedReviewers {
		u := &payload.RequestedReviewers[i]
		if u.Login == "" {
			continue
		}
		add(userKey(u), &userState{user: provider.User{ID: userID(u), Name: u.Login}}).requested = true
	}
	for _, t := range payload.RequestedTeams {
		if t.Slug == "" {
			continue
		}
		add("team:"+strings.ToLower(t.Slug), &userState{
			user: provider.User{Name: "@" + ref.Namespace + "/" + t.Slug}, display: t.Name, requested: true,
		})
	}

	own, unclassified := 0, false
	for i := range reviews {
		r := &reviews[i]
		if r.User == nil || r.User.Login == "" {
			continue
		}
		var decisive bool
		switch strings.ToUpper(r.State) {
		case "APPROVED", "CHANGES_REQUESTED":
			decisive = true
		case "COMMENTED":
		default: // DISMISSED, PENDING, unknown
			continue
		}
		if !decisive && opts.Me != nil && opts.IsOwn != nil && provider.IsUser(*opts.Me, userID(r.User), r.User.Login) {
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
		u := add(userKey(r.User), &userState{user: provider.User{ID: userID(r.User), Name: r.User.Login}})
		if decisive {
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
		var from *apiStatusReview
		switch {
		case u.decisive != nil:
			from = u.decisive
			rv.State = provider.ReviewApproved
			if strings.EqualFold(from.State, "CHANGES_REQUESTED") {
				rv.State = provider.ReviewChangesRequested
			}
		case u.comment != nil:
			from = u.comment
			rv.State = provider.ReviewCommented
		case u.requested:
			rv.State = provider.ReviewPending
		default:
			continue
		}
		if from != nil {
			rv.At = from.SubmittedAt.Time
			rv.Stale = from.CommitID != "" && head != "" && !strings.EqualFold(from.CommitID, head)
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
	p.logger.Debug("github reviews folded", "reviews", len(reviews), "reviewers", len(out), "own_excluded", own)
	return out
}

// ownReview reports whether review r of the token's user is review-mcp's own:
// a marker in its body or in one of its comments. ok is false when its
// comments could not be read. The bodies are inspected, never kept.
func (p *Provider) ownReview(ctx context.Context, pp string, r *apiStatusReview, isOwn func(string) bool) (marked, ok bool) {
	if isOwn(r.Body) {
		return true, true
	}
	cs, err := httpx.PagesByLink[apiReviewComment](ctx, p.client, p.fetchPage,
		listPath(pp+"/reviews/"+idStr(r.ID)+"/comments"), reviewCommentsPageCap)
	if err != nil {
		p.logger.Debug("github review comments not read", "class", errClass(err))
		return false, false
	}
	for i := range cs {
		if isOwn(cs[i].Body) {
			return true, true
		}
	}
	return false, true
}

// readRequiredApprovals sets st.RequiredApprovals from the rules that apply
// to the target branch, combining two sources.
//
// Rulesets (GET .../rules/branches/{branch}, readable with read access): the
// pull_request rules give required_approving_review_count, and the largest
// one over all matching rules counts (several rulesets can cover a branch,
// and the strictest applies). Classic branch protection (GET
// .../branches/{branch}/protection, needs admin rights) is always tried as
// well, because a branch can have both; with required_pull_request_reviews
// its count, without it 0. GitHub answers an unprotected branch and a token
// without admin rights alike with 404 or 403 there, so "no protection" cannot
// be told from "not readable".
//
//	rulesets   classic     RequiredApprovals   RequiredApprovalsNote
//	count R    count C     max(R, C)           none
//	count R    unreadable  R                   NoteClassicProtectionUnreadable
//	none       count C     C                   none
//	none       unreadable  nil                 NoteApprovalsUnreadable
//
// "none" means no pull_request rule with a count, or the rules unreadable.
// nil is never turned into 0 on a guess (unlike Gitea, whose rule list can be
// read in full).
func (p *Provider) readRequiredApprovals(ctx context.Context, rp, target string, st *provider.ReviewStatus) {
	type rule struct {
		Type       string `json:"type"`
		Parameters struct {
			Required *int `json:"required_approving_review_count"`
		} `json:"parameters"`
	}
	branch := url.PathEscape(target)
	best := -1
	rules, err := httpx.PagesByLink[rule](ctx, p.client, p.fetchPage, listPath(rp+"/rules/branches/"+branch), rulesPageCap)
	if err != nil {
		p.logger.Debug("github branch rules not read", "class", errClass(err))
	} else {
		for _, r := range rules {
			if r.Type == "pull_request" && r.Parameters.Required != nil && *r.Parameters.Required > best {
				best = *r.Parameters.Required
			}
		}
	}

	var prot struct {
		RequiredPullRequestReviews *struct {
			Required *int `json:"required_approving_review_count"`
		} `json:"required_pull_request_reviews"`
	}
	if err := p.getJSON(ctx, rp+"/branches/"+branch+"/protection", &prot); err != nil {
		p.logger.Debug("github branch protection not read", "class", errClass(err))
		if best >= 0 {
			st.RequiredApprovals = &best
			st.RequiredApprovalsNote = provider.NoteClassicProtectionUnreadable
			return
		}
		st.RequiredApprovalsNote = provider.NoteApprovalsUnreadable
		return
	}
	n := 0
	if rv := prot.RequiredPullRequestReviews; rv != nil && rv.Required != nil && *rv.Required > 0 {
		n = *rv.Required
	}
	if best > n {
		n = best
	}
	st.RequiredApprovals = &n
}

// UpdatePullRequest implements provider.Provider: PATCH /pulls/{n} with the
// title and/or the body and nothing else, so the draft flag, the base, the
// reviewers and their verdicts are never part of the request. GitHub has no
// version to send; UpdatePR.Version is ignored (describe compares the text
// it read right before the write instead).
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
	return p.sendJSON(ctx, http.MethodPatch, path, in, nil)
}
