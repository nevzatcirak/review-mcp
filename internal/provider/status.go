package provider

import "time"

// ReviewState is the state of one reviewer's verdict on a pull request.
type ReviewState string

// Review states of pr_info (X-23).
const (
	ReviewApproved         ReviewState = "approved"
	ReviewChangesRequested ReviewState = "changes_requested"
	ReviewCommented        ReviewState = "commented"
	ReviewPending          ReviewState = "pending"
)

// Reviewer is one human reviewer of a pull request. DisplayName is untrusted
// third-party text.
type Reviewer struct {
	User        User
	DisplayName string
	// Requested is true when the reviewer was asked to review.
	Requested bool
	State     ReviewState
	// Stale is true when the state was given on an older commit than the
	// head, where the provider reports it.
	Stale bool
	// At is when the state was given; the zero time when unknown.
	At time.Time
}

// NoteResolutionUnavailable is the fixed note of pr_comments for a provider
// that reports no resolved state for any thread (neither
// InlineThreadResolution nor GeneralThreadResolution): all threads are shown.
const NoteResolutionUnavailable = "Resolved state is not available on GitHub without GraphQL; all threads are shown."

// Fixed notes of a ReviewStatus. They are the only text a provider adds.
const (
	NoteReviewsUnreadable = "The reviews could not be read, so the reviewers are not listed."
	NoteMergeUnreadable   = "The merge status could not be read."
	// NoteActivityUnclassified: a review of the token's user could not be
	// told from review-mcp's own, so it is left out of the reviewers.
	NoteActivityUnclassified = "A review by the token's user could not be checked for review-mcp's markers and is not listed."
	// NoteCommitsTruncated: GitHub lists at most 250 commits of a pull
	// request, so the commit messages read for it (GetCommitMessages) stop
	// there.
	NoteCommitsTruncated = "GitHub lists at most 250 commits of a pull request; the commits past them are not available."
	// NoteChecksNotRequired: GitHub's "unstable" merge state, a pull
	// request that can be merged while some non-required checks fail or are
	// pending. It is not a blocker.
	NoteChecksNotRequired = "some checks that are not required are failing or pending"
)

// Fixed merge blockers. A provider maps structured server reasons to these
// and never passes server text through.
const (
	BlockerApprovals  = "required approvals missing"
	BlockerNeedsWork  = "a reviewer marked the pull request as needs work"
	BlockerBuilds     = "required builds are missing or failing"
	BlockerConflict   = "merge conflict"
	BlockerOtherCheck = "other merge check"
	// BlockerRequirements: the host blocks the merge for required reviews or
	// required checks and does not say which (GitHub's "blocked").
	BlockerRequirements = "required reviews or checks are not satisfied"
	// BlockerBehind: the head branch is behind the target branch and the
	// host requires it to be up to date.
	BlockerBehind = "the branch is behind the target branch"
	BlockerDraft  = "the pull request is a draft"
)

// Fixed notes for RequiredApprovals (RequiredApprovalsNote).
const (
	// NoteApprovalsUnreadable explains a nil RequiredApprovals: the
	// protection could not be read. It is also what tools reports when a
	// provider leaves RequiredApprovals nil without a note.
	NoteApprovalsUnreadable = "not readable with this token"
	// NoteNoProtectionRule accompanies RequiredApprovals 0 when the
	// protection rules were read and none applies to the target branch.
	NoteNoProtectionRule = "no branch protection rule applies to the target branch"
	// NoteProtectionPatternUnevaluable accompanies a nil RequiredApprovals
	// when no rule matched but a rule pattern could not be evaluated.
	NoteProtectionPatternUnevaluable = "a protection pattern could not be evaluated"
	// NoteClassicProtectionUnreadable accompanies a RequiredApprovals taken
	// from the rulesets alone, when the classic branch protection (which
	// may ask for more) could not be read.
	NoteClassicProtectionUnreadable = "classic branch protection is not readable with this token; the required count may be higher"
)

// ReviewStatusOptions tunes GetReviewStatus.
type ReviewStatusOptions struct {
	// Me is the token's own user. When set, a review of Me that IsOwn
	// recognises as review-mcp's is left out of the reviewers (X-23); when
	// nil, nothing is excluded.
	Me *User
	// IsOwn reports whether a review or review-comment body carries one of
	// review-mcp's markers. The body is only inspected, never kept.
	IsOwn func(body string) bool
}

// ReviewStatus is the reviewer and merge status of a pull request. Every
// part that could not be read is nil (Reviewers, RequiredApprovals,
// Mergeable) and explained by a fixed entry of Notes.
type ReviewStatus struct {
	// Reviewers is nil when the reviews could not be read.
	Reviewers []Reviewer
	// RequiredApprovals is nil when the provider does not expose it to this
	// token; it is never a guess.
	RequiredApprovals *int
	// RequiredApprovalsNote is one of the NoteApprovals*/NoteNoProtection*/
	// NoteProtection* texts, or empty. Empty with a nil RequiredApprovals
	// means NoteApprovalsUnreadable.
	RequiredApprovalsNote string
	// Mergeable is nil when unknown.
	Mergeable *bool
	// MergeBlockers holds Blocker* texts, only where the provider gives
	// structured reasons.
	MergeBlockers []string
	Notes         []string
}
