package gitlab

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// reviewersPageCap bounds the reviewers read (perPage items per page); a
// merge request has a handful of reviewers.
const reviewersPageCap = 5

// apiReviewer is an entry of GET .../merge_requests/{iid}/reviewers.
type apiReviewer struct {
	User  apiUser `json:"user"`
	State string  `json:"state"`
}

// apiApprovals is the body of GET .../merge_requests/{iid}/approvals.
// ApprovalsRequired is nil when the field is absent.
type apiApprovals struct {
	ApprovalsRequired *int `json:"approvals_required"`
	ApprovedBy        []struct {
		User apiUser `json:"user"`
	} `json:"approved_by"`
}

// userKey identifies a user across the payloads: the numeric id when the
// server sent one, else the lower-cased username.
func userKey(u *apiUser) string {
	if u.ID > 0 {
		return "id:" + idStr(u.ID)
	}
	return "login:" + strings.ToLower(u.Username)
}

// reviewerState maps a reviewer's state: approved is approved,
// requested_changes is changes_requested, reviewed is commented, and
// unreviewed, review_started, unapproved and anything unknown are pending.
func reviewerState(s string) provider.ReviewState {
	switch s {
	case "approved":
		return provider.ReviewApproved
	case "requested_changes":
		return provider.ReviewChangesRequested
	case "reviewed":
		return provider.ReviewCommented
	}
	return provider.ReviewPending
}

func reviewerOf(u *apiUser, state provider.ReviewState, requested bool) provider.Reviewer {
	out := provider.User{Name: u.Username}
	if u.ID > 0 {
		out.ID = idStr(u.ID)
	}
	return provider.Reviewer{User: out, DisplayName: u.Name, Requested: requested, State: state}
}

// GetReviewStatus implements provider.Provider (spec 2C §4 item 1).
//
// Reviewers: GET .../reviewers, every entry requested, with the state
// mapped by reviewerState; the users of approved_by (GET .../approvals)
// who are not reviewers are added as approved and not requested. GitLab
// reports no commit per approval, so Stale is always false. A 404 on
// /reviewers (an instance without the endpoint) falls back to the merge
// request's reviewers[], all pending. Any other failure leaves Reviewers
// nil with NoteReviewsUnreadable. Approvals carry no body and review-mcp
// never approves, so there is no review of its own to leave out and
// opts.Me and opts.IsOwn are not used.
//
// Required approvals: approvals_required when the response has it (0 comes
// with NoteNoApprovalRule); absent or unreadable is nil with
// NoteApprovalsUnreadable.
//
// Merge status: detailed_merge_status of pr, see mergeStatus. The merge
// state of a merge request that is not open is not reported.
func (p *Provider) GetReviewStatus(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, _ provider.ReviewStatusOptions) *provider.ReviewStatus {
	st := &provider.ReviewStatus{MergeBlockers: []string{}}
	if pr == nil {
		return st
	}
	if !pr.Merged && (pr.State == "" || strings.EqualFold(pr.State, "open")) {
		setMergeStatus(st, pr.MergeableState)
	}
	mr, err := mrPath(ref)
	if err != nil {
		st.Notes = append(st.Notes, provider.NoteReviewsUnreadable)
		st.RequiredApprovalsNote = provider.NoteApprovalsUnreadable
		return st
	}

	// DESIGN-QUESTION WP-6d DQ-d1: when the approvals cannot be read, the
	// reviewers are still listed from /reviewers, without the approvers who
	// are not reviewers, and no note says so (only RequiredApprovals gets
	// NoteApprovalsUnreadable). Option: drop the reviewers too. Kept: a
	// partial list beats none, as every approver who was asked to review is
	// in /reviewers with the state approved.
	var appr apiApprovals
	apprErr := p.getJSON(ctx, mr+"/approvals", &appr)
	if apprErr != nil {
		p.logger.Debug("gitlab approvals not read", "class", errClass(apprErr))
	}
	switch n := appr.ApprovalsRequired; {
	case apprErr != nil || n == nil || *n < 0:
		st.RequiredApprovalsNote = provider.NoteApprovalsUnreadable
	case *n == 0:
		zero := 0
		st.RequiredApprovals, st.RequiredApprovalsNote = &zero, provider.NoteNoApprovalRule
	default:
		st.RequiredApprovals = n
	}

	reviewers, err := p.reviewers(ctx, mr)
	if err != nil {
		p.logger.Debug("gitlab reviewers not read", "class", errClass(err))
		st.Notes = append(st.Notes, provider.NoteReviewsUnreadable)
		return st
	}
	seen := map[string]bool{}
	for i := range reviewers {
		seen[userKey(&reviewers[i].User)] = true
	}
	out := make([]provider.Reviewer, 0, len(reviewers)+len(appr.ApprovedBy))
	// DESIGN-QUESTION WP-6d DQ-d2: a requested reviewer keeps the state
	// /reviewers reports even when approved_by lists them (the spec maps
	// approved_by only for users who are not reviewers). Option: show them
	// approved unless they requested changes. Kept the spec's rule.
	for i := range reviewers {
		if reviewers[i].User.Username == "" {
			continue
		}
		out = append(out, reviewerOf(&reviewers[i].User, reviewerState(reviewers[i].State), true))
	}
	for i := range appr.ApprovedBy {
		u := &appr.ApprovedBy[i].User
		if u.Username == "" || seen[userKey(u)] {
			continue
		}
		seen[userKey(u)] = true
		out = append(out, reviewerOf(u, provider.ReviewApproved, false))
	}
	st.Reviewers = out
	return st
}

// reviewers reads the requested reviewers: GET .../reviewers, or, when the
// instance answers 404 for it, the merge request's own reviewers[] as
// pending.
func (p *Provider) reviewers(ctx context.Context, mr string) ([]apiReviewer, error) {
	rs, _, err := httpx.PagesByLinkCapped(ctx, p.client, p.fetchPage,
		mr+"/reviewers?per_page="+strconv.Itoa(perPage), httpx.Cap{Pages: reviewersPageCap},
		func(pg *[]apiReviewer) []apiReviewer { return *pg })
	if err == nil || !errors.Is(err, provider.ErrNotFound) {
		return rs, err
	}
	var m struct {
		Reviewers []apiUser `json:"reviewers"`
	}
	if err := p.getJSON(ctx, mr, &m); err != nil {
		return nil, err
	}
	out := make([]apiReviewer, len(m.Reviewers))
	for i, u := range m.Reviewers {
		out[i] = apiReviewer{User: u, State: "unreviewed"}
	}
	return out, nil
}

// setMergeStatus sets Mergeable and MergeBlockers from detailed_merge_status
// (an enum, never shown as it is):
//
//	mergeable                                    mergeable, no blocker
//	unchecked, checking, approvals_syncing,
//	preparing                                    not known yet (Mergeable nil)
//	conflict, broken_status                      merge conflict
//	ci_must_pass, ci_still_running,
//	external_status_checks,
//	security_policy_pipeline_check               builds
//	discussions_not_resolved                     unresolved discussions
//	draft_status                                 draft
//	need_rebase                                  behind
//	not_approved                                 approvals
//	requested_changes                            needs work
//	anything else                                other merge check
//
// A merge request whose status was not read (empty) is not known, with
// NoteMergeUnreadable.
func setMergeStatus(st *provider.ReviewStatus, detailed string) {
	status := strings.ToLower(strings.TrimSpace(detailed))
	no, yes := false, true
	switch status {
	case "":
		st.Notes = append(st.Notes, provider.NoteMergeUnreadable)
		return
	case "mergeable":
		st.Mergeable = &yes
		return
	case "unchecked", "checking", "approvals_syncing", "preparing":
		return
	}
	st.Mergeable = &no
	st.MergeBlockers = []string{mergeBlocker(status)}
}

// mergeBlocker is the fixed blocker text of a detailed_merge_status that
// blocks the merge.
func mergeBlocker(status string) string {
	switch status {
	case "conflict", "broken_status":
		return provider.BlockerConflict
	case "ci_must_pass", "ci_still_running", "external_status_checks", "security_policy_pipeline_check":
		return provider.BlockerBuilds
	case "discussions_not_resolved":
		return provider.BlockerDiscussions
	case "draft_status":
		return provider.BlockerDraft
	case "need_rebase":
		return provider.BlockerBehind
	case "not_approved":
		return provider.BlockerApprovals
	case "requested_changes":
		return provider.BlockerNeedsWork
	}
	return provider.BlockerOtherCheck
}
