package llmrun

import (
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Rendered is one rendered prompt pair.
type Rendered struct {
	System string
	User   string
	// GuardUser is the user prompt the guard measures when it differs from
	// User (pr_review checks its re-ask request, a few tokens longer). Empty
	// means User.
	GuardUser string
}

// RenderFunc renders the prompts for a diff.
type RenderFunc func(diff string) (Rendered, error)

// Fitted is the outcome of the request-size guard.
type Fitted struct {
	Rendered Rendered
	// RequestTokens is tokens.RequestTokens of System and User.
	RequestTokens int
	// Diff is the diff in the prompts; KeptLines is its line count when the
	// guard trimmed it, -1 otherwise.
	Diff      string
	KeptLines int
}

// Fit renders the final prompts and applies the guard against estimator
// drift (P4 §4.3 step 6, P5 §1.2 step 6): RequestTokens + HardReserve must
// not exceed ContextWindow. When it does, the diff is trimmed to the
// longest line prefix that fits, found with diffpipe's verified-prefix
// search, and tokens.TruncationMarker is appended. An error wrapping
// tokens.ErrDoesNotFit (class ClassDoesNotFit) means not even one line fits.
//
// DESIGN-QUESTION: which request does the guard check, and at what
// granularity does it trim? — chose to check Rendered.GuardUser when set
// (for pr_review the re-ask request, so a re-ask can never overrun the
// window the first request fitted) and to trim at line granularity with
// diffpipe.VerifiedPrefix (as spec §4.3 step 6 says "trim through the
// verified prefix"); the caller records the cut files in the coverage
// (TrimCoverage) and a note; re-running diffpipe.Prepare with a smaller
// budget was the alternative, but it is not a verified-prefix trim and can
// loop on the fast path. The guard only trips on estimator drift larger
// than the 500-token soft reserve margin, so this path is rare.
func Fit(diff string, b tokens.Budget, render RenderFunc) (*Fitted, error) {
	limit := b.ContextWindow - b.HardReserve()
	try := func(d string) (*Fitted, bool, error) {
		r, err := render(d)
		if err != nil {
			return nil, false, err
		}
		guard := r.GuardUser
		if guard == "" {
			guard = r.User
		}
		f := &Fitted{Rendered: r, RequestTokens: tokens.RequestTokens(r.System, r.User, b.Factor),
			Diff: d, KeptLines: -1}
		return f, tokens.RequestTokens(r.System, guard, b.Factor) <= limit, nil
	}
	f, ok, err := try(diff)
	if err != nil || ok {
		return f, err
	}

	lines := strings.Split(diff, "\n")
	trimmed := func(n int) string { return strings.Join(lines[:n], "\n") + tokens.TruncationMarker }
	var renderErr error
	fits := func(n int) bool {
		if n <= 0 || strings.TrimSpace(strings.Join(lines[:n], "\n")) == "" {
			return false
		}
		_, ok, err := try(trimmed(n))
		if err != nil {
			renderErr = err
		}
		return ok
	}
	n := diffpipe.VerifiedPrefix(len(lines)-1, fits)
	if renderErr != nil {
		return nil, renderErr
	}
	if n == 0 {
		return nil, DoesNotFit(tokens.ErrDoesNotFit)
	}
	f, _, err = try(trimmed(n))
	if err != nil {
		return nil, err
	}
	f.KeptLines = n
	return f, nil
}
