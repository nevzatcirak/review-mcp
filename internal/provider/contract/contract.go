// Package contract is the provider contract suite (X-24, design note Y-1):
// one set of behavioural cases, written against provider.Provider only, that
// every provider runs against its own fake server.
//
// A provider package implements Fixture in its _test.go files and calls Run
// from a test. Provider-specific expectations come from the provider itself
// (Kind, Capabilities, what it reports) or from Traits the fixture declares;
// Run never branches on a provider kind, so a new provider only adds a
// Fixture.
//
// RunReviewDuplicates (review.go) is the one scenario above the provider: it
// drives pr_review's publish through each Fixture, so that the duplicate
// rule of X-29 is tested end to end on every provider's fake.
//
// The package is test support. It imports testing and net/http/httptest and
// must only be imported from _test.go files; no production package imports
// it. Run, the Spec types and the server helpers live in non-test files only
// because Go cannot import another package's _test.go files.
package contract

import (
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Fixture serves one synthetic pull request through a provider's fake
// server and returns a Provider pointed at it.
type Fixture interface {
	Kind() provider.Kind
	Serve(t *testing.T, pr Spec) (provider.Provider, provider.PRRef)
}

// Traits are provider facts the Provider interface does not expose yet. A
// Fixture declares them by also implementing Declarer.
type Traits struct {
	// BaseStrategies lists the PullRequest.BaseStrategy values the provider
	// documents (the provider.Base* constants). Required.
	BaseStrategies []string
	// CheckStrategies lists the Checks.Strategy values the provider
	// documents (the provider.Checks* constants). Required.
	CheckStrategies []string
	// CheckStates lists the check states the provider's host can report;
	// the checks cases serve only checks in these states. Nil means all six.
	CheckStates []provider.CheckState
	// OmitsNoNewlineMarker is true for a provider that builds patches from
	// file contents and never emits "\ No newline at end of file" (Bitbucket
	// Server, upstream parity). Consumers must not rely on the marker
	// (FilePatch.Patch); the expected hunks here lack those marker lines and
	// everything else stays byte for byte.
	OmitsNoNewlineMarker bool
	// InlineRanges is true for a provider that anchors an inline comment on
	// its whole range, InlineComment.Line to EndLine, and lists the thread
	// at the range's last line (GitHub's line) with Thread.StartLine the
	// range's first line (GitHub's start_line). Without it the provider
	// ignores EndLine: the comment is posted on Line and listed there, with
	// StartLine 0.
	InlineRanges bool
	// NoStaleReviews is true for a provider whose host reports no commit
	// per verdict (GitLab): a Reviewer with Stale in the Spec is served as
	// a plain verdict, and the provider must report it not stale.
	//
	// DESIGN-QUESTION WP-6d DQ-d3: the stale case of review_status cannot
	// hold on such a host; a trait (not a skip) keeps the rest of the case
	// running.
	NoStaleReviews bool
	// NoListingLimit is true for a provider whose host lists every changed
	// file however many there are (Gitea, Bitbucket Server): Env.ListLimit
	// has no effect, and the listing_limit case checks only that a complete
	// listing is not marked Incomplete.
	NoListingLimit bool
	// SeparateIssueNumbers is true for a provider whose host numbers issues
	// and pull requests apart (GitLab): its issue endpoint never answers
	// with a pull request, so a pull request's number reads as not_found
	// there instead of not_an_issue.
	SeparateIssueNumbers bool
}

// Declarer is implemented by a Fixture that declares Traits.
type Declarer interface {
	Traits() Traits
}

// Run executes the contract cases against f. Each case is a named subtest
// that serves its own pull request, so a failure names the case.
func Run(t *testing.T, f Fixture) {
	t.Helper()
	var tr Traits
	if d, ok := f.(Declarer); ok {
		tr = d.Traits()
	}
	if len(tr.BaseStrategies) == 0 {
		t.Fatal("the fixture declares no base strategies (Traits.BaseStrategies)")
	}
	if len(tr.CheckStrategies) == 0 {
		t.Fatal("the fixture declares no check strategies (Traits.CheckStrategies)")
	}
	s := &suite{f: f, tr: tr}
	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"capabilities", s.capabilities},
		{"metadata", s.metadata},
		{"file_list", s.fileList},
		{"commit_messages", s.commitMessages},
		{"file_limit", s.fileLimit},
		{"listing_limit", s.listingLimit},
		{"hunks_and_content", s.hunksAndContent},
		{"threads", s.threads},
		{"reply_in_thread", s.reply},
		{"general_reply", s.generalReply},
		{"edit_ownership", s.editOwnership},
		{"inline_anchoring", s.inline},
		{"review_status", s.reviewStatus},
		{"file_line_url", s.fileLineURL},
		{"update_pull_request", s.updatePullRequest},
		{"checks", s.checks},
		{"file_at", s.fileAt},
		{"list_dir", s.listDir},
		{"issues", s.issues},
		{"decision", s.decision},
		{"errors", s.errorCases},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t)
		})
	}
}
