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
	"sort"
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
	// InlineRanges is true for a provider that anchors an inline comment on
	// its whole range, InlineComment.Line to EndLine, and lists the thread
	// at the range's last line (GitHub's line). Without it the provider
	// ignores EndLine: the comment is posted on Line and listed there.
	InlineRanges bool
	// Pending maps a case, or one call of the errors case, to the work
	// package that will implement it, while a provider is built up over
	// several packages. Run skips each with "pending: <package>" instead of
	// running it. A key is a case name of Run ("threads") or "errors/" plus
	// a call name of the errors case ("errors/ListThreads"); an unknown key
	// fails the suite, so that a stale entry cannot hide a case. A finished
	// provider has no Pending entries.
	Pending map[string]string
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
	var names []string
	for _, c := range cases {
		names = append(names, c.name)
	}
	for _, k := range unknownPending(tr.Pending, names) {
		t.Errorf("Traits.Pending names %q, which is neither a case nor a call of the errors case", k)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s.skipPending(t, c.name)
			c.run(t)
		})
	}
}

// unknownPending returns the keys of pending, sorted, that name neither one
// of the cases nor "errors/" plus a call of the errors case.
func unknownPending(pending map[string]string, cases []string) []string {
	known := map[string]bool{}
	for _, c := range cases {
		known[c] = true
	}
	for _, n := range failureCallNames {
		known["errors/"+n] = true
	}
	var out []string
	for k := range pending {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// skipPending skips t when the fixture declares key pending.
func (s *suite) skipPending(t *testing.T, key string) {
	t.Helper()
	if wp, ok := s.tr.Pending[key]; ok {
		t.Skip("pending: " + wp)
	}
}
