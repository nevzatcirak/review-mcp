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
	// OmitsNoNewlineMarker is true for a provider that builds patches from
	// file contents and never emits "\ No newline at end of file" (Bitbucket
	// Server, upstream parity). Consumers must not rely on the marker
	// (FilePatch.Patch); the expected hunks here lack those marker lines and
	// everything else stays byte for byte.
	OmitsNoNewlineMarker bool
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
	s := &suite{f: f, tr: tr}
	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"capabilities", s.capabilities},
		{"metadata", s.metadata},
		{"file_list", s.fileList},
		{"file_limit", s.fileLimit},
		{"hunks_and_content", s.hunksAndContent},
		{"threads", s.threads},
		{"reply_in_thread", s.reply},
		{"general_reply", s.generalReply},
		{"edit_ownership", s.editOwnership},
		{"inline_anchoring", s.inline},
		{"review_status", s.reviewStatus},
		{"file_line_url", s.fileLineURL},
		{"update_pull_request", s.updatePullRequest},
		{"errors", s.errorCases},
	}
	for _, c := range cases {
		t.Run(c.name, c.run)
	}
}
