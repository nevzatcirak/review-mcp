package bitbucketserver

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// prReviewers maps reviewers[] of the PR payload. Participants that are not
// reviewers are not part of the payload's reviewers[] and are not listed. A
// status other than the three known ones is treated as pending. Stale is
// true when a decided reviewer's lastReviewedCommit is present and differs
// from head.
func prReviewers(in *apiPR, head string) []provider.Reviewer {
	out := make([]provider.Reviewer, 0, len(in.Reviewers))
	for _, r := range in.Reviewers {
		if r.User.Name == "" {
			continue
		}
		u := provider.User{Name: r.User.Name}
		if r.User.ID > 0 {
			u.ID = strconv.FormatInt(r.User.ID, 10)
		}
		state := provider.ReviewPending
		switch strings.ToUpper(r.Status) {
		case "APPROVED":
			state = provider.ReviewApproved
		case "NEEDS_WORK":
			state = provider.ReviewChangesRequested
		}
		out = append(out, provider.Reviewer{
			User: u, DisplayName: r.User.DisplayName, Requested: true, State: state,
			Stale: state != provider.ReviewPending && r.LastReviewedCommit != "" && r.LastReviewedCommit != head,
		})
	}
	return out
}

type apiMergeStatus struct {
	CanMerge   bool `json:"canMerge"`
	Conflicted bool `json:"conflicted"`
	Vetoes     []struct {
		SummaryMessage  string `json:"summaryMessage"`
		DetailedMessage string `json:"detailedMessage"`
	} `json:"vetoes"`
}

// GetReviewStatus implements provider.Provider.
//
// Reviewers come from reviewers[] of the PR payload. Every listed reviewer
// is a requested reviewer (Bitbucket Server's reviewers are asked to review),
// so Requested is always true. Stale is true when a decided reviewer's
// lastReviewedCommit is present and differs from the head commit. The
// payload has no review time, so At is zero. Review-mcp never casts a
// Bitbucket review, so there is nothing to exclude and opts is not used.
//
// Merge status comes from GET .../merge (only for an open PR): canMerge is
// Mergeable and the vetoes become fixed blockers (see blockerFor); the
// server's veto text is never passed on. Required approvals are read from a
// veto only when it states a number.
func (p *Provider) GetReviewStatus(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, _ provider.ReviewStatusOptions) *provider.ReviewStatus {
	st := &provider.ReviewStatus{MergeBlockers: []string{}}
	if pr == nil {
		return st
	}
	path, err := prPath(ref)
	if err != nil {
		st.Notes = append(st.Notes, provider.NoteReviewsUnreadable)
		return st
	}
	// The reviewers are read from the PR payload again so that
	// GetPullRequest keeps its shape.
	var payload apiPR
	if err := p.client.GetJSON(ctx, path, &payload); err != nil {
		p.logger.Debug("bitbucket server reviewers not read", "class", errClass(err))
		st.Notes = append(st.Notes, provider.NoteReviewsUnreadable)
	} else {
		st.Reviewers = prReviewers(&payload, pr.HeadSHA)
	}
	if !strings.EqualFold(pr.State, "OPEN") {
		return st
	}
	var ms apiMergeStatus
	if err := p.client.GetJSON(ctx, path+"/merge", &ms); err != nil {
		p.logger.Debug("bitbucket server merge status not read", "class", errClass(err))
		st.Notes = append(st.Notes, provider.NoteMergeUnreadable)
		return st
	}
	can := ms.CanMerge
	st.Mergeable = &can
	seen := map[string]bool{}
	add := func(b string) {
		if !seen[b] {
			seen[b] = true
			st.MergeBlockers = append(st.MergeBlockers, b)
		}
	}
	if ms.Conflicted {
		add(provider.BlockerConflict)
	}
	for _, v := range ms.Vetoes {
		b := blockerFor(v.SummaryMessage)
		add(b)
		if b == provider.BlockerApprovals && st.RequiredApprovals == nil {
			if n, ok := approvalsIn(v.SummaryMessage, v.DetailedMessage); ok {
				st.RequiredApprovals = &n
			}
		}
	}
	p.logger.Debug("bitbucket server merge status read", "can_merge", can, "vetoes", len(ms.Vetoes), "blockers", len(st.MergeBlockers))
	return st
}

// blockerFor maps a veto summary to one of the fixed blocker texts. Anything
// unknown is "other merge check"; the summary itself is never returned.
func blockerFor(summary string) string {
	s := strings.ToLower(summary)
	switch {
	case strings.Contains(s, "needs work"):
		return provider.BlockerNeedsWork
	case strings.Contains(s, "conflict"):
		return provider.BlockerConflict
	case strings.Contains(s, "build"):
		return provider.BlockerBuilds
	case strings.Contains(s, "approv") || strings.Contains(s, "reviewer"):
		return provider.BlockerApprovals
	}
	return provider.BlockerOtherCheck
}

// approvalsRE finds a stated total such as "requires 2 approvals", "at least
// 2 approvals" or "2 approvals required". A count of what is still missing
// ("2 more approvals", "needs 1 additional approval") is not the required
// total and must not be read as one.
var (
	approvalsBefore = regexp.MustCompile(`(?i)\b(?:requires?|required|at least|minimum(?: of)?)\s+(\d{1,3})\s+approvals?\b`)
	approvalsAfter  = regexp.MustCompile(`(?i)\b(\d{1,3})\s+approvals?\s+(?:is |are )?required\b`)
	approvalsGap    = regexp.MustCompile(`(?i)\b(?:more|additional|remaining|further|still)\b`)
)

func approvalsIn(texts ...string) (int, bool) {
	for _, t := range texts {
		if approvalsGap.MatchString(t) {
			continue
		}
		for _, re := range []*regexp.Regexp{approvalsBefore, approvalsAfter} {
			if m := re.FindStringSubmatch(t); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					return n, true
				}
			}
		}
	}
	return 0, false
}
