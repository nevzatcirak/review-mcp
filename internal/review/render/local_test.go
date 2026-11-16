package render

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

func localResult(title string) *review.Result {
	return &review.Result{
		PR:       review.PRInfo{Kind: string(provider.KindLocal), Title: title},
		Coverage: completeCoverage(),
	}
}

// TestClientLocalHeading: [canary] (q). A local result heads with
// "## Local review" and the line "Local work: <subject>"; no number, no URL,
// no pull request words. Mutation: the old "## PR Review" -> fails.
func TestClientLocalHeading(t *testing.T) {
	out := Client(localResult("Add retry to the fetcher"))
	if !strings.HasPrefix(out, "## Local review\n\nLocal work: Add retry to the fetcher\n") {
		t.Errorf("head:\n%s", out)
	}
	if strings.Contains(out, "PR Review") || strings.Contains(out, "Pull request") {
		t.Errorf("pull request words in a local review:\n%s", out)
	}
}

// TestClientLocalEmptySubject: no subject, no colon.
func TestClientLocalEmptySubject(t *testing.T) {
	for _, title := range []string{"", "  "} {
		out := Client(localResult(title))
		if !strings.HasPrefix(out, "## Local review\n\nLocal work\n") {
			t.Errorf("title %q:\n%s", title, out)
		}
	}
}

// TestClientLocalSubjectEscaped: the subject goes through mdutil.Inline.
func TestClientLocalSubjectEscaped(t *testing.T) {
	out := Client(localResult("# fix <b>x</b>\nmore"))
	_, rest, ok := strings.Cut(out, "## Local review\n\n")
	line, _, _ := strings.Cut(rest, "\n")
	if !ok || strings.Contains(line, "<b>") || !strings.HasPrefix(line, "Local work: ") {
		t.Errorf("line = %q in:\n%s", line, out)
	}
}
