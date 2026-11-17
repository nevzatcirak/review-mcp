package render

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

func callerResult(link string) *review.Result {
	res := localResult("Change the unit")
	res.EnabledFields = []string{review.KeyKeyIssues}
	res.Review = &review.Review{KeyIssuesToReview: []review.KeyIssue{{
		RelevantFile: "pkg/billing/total.go", IssueHeader: "Possible Bug", IssueContent: "Adds cents to euros.",
		StartLine: 42, EndLine: 42, Caller: true, Link: link,
	}}}
	return res
}

// TestCallerFindingLocalText is canary (j2): in a local run a finding in a
// caller is overview text with its path and line and the caller suffix,
// and no URL.
func TestCallerFindingLocalText(t *testing.T) {
	out := Client(callerResult(""))
	if !strings.Contains(out, "`pkg/billing/total.go` L42"+textInCaller) {
		t.Fatalf("caller finding without path, line or suffix:\n%s", out)
	}
	if strings.Contains(out, "](") || strings.Contains(out, "http") {
		t.Fatalf("a local caller finding carries a link:\n%s", out)
	}
	for _, gfm := range []bool{true, false} {
		ov := Provider(callerResult(""), provider.Capabilities{GFM: gfm})
		if !strings.Contains(ov, "pkg/billing/total.go") || !strings.Contains(ov, "L42") || !strings.Contains(ov, textCallerListed) {
			t.Fatalf("gfm=%v overview:\n%s", gfm, ov)
		}
	}
}

func TestCallerFindingLinked(t *testing.T) {
	out := Client(callerResult("https://your-gitea.example/o/r/src/commit/abc/pkg/billing/total.go#L42"))
	if !strings.Contains(out, "(https://your-gitea.example/o/r/src/commit/abc/pkg/billing/total.go#L42)"+textInCaller) {
		t.Fatalf("linked caller finding:\n%s", out)
	}
}

func TestImpactCoverageLine(t *testing.T) {
	res := localResult("x")
	res.Coverage.Impact = &llmrun.Impact{Status: llmrun.ImpactOn, Seeds: 6, Links: 23, DepthReached: 2,
		NotFollowed: llmrun.ImpactNotFollowed{WidelyUsed: 3, Depth: 9}}
	out := Client(res)
	if !strings.Contains(out, "Impact: 6 symbols traced, 23 links to depth 2 (3 not followed: widely used); found by name matching.") {
		t.Fatalf("impact line:\n%s", out)
	}
	res.Coverage.Impact = &llmrun.Impact{Status: llmrun.ImpactOff}
	if strings.Contains(Client(res), "Impact:") {
		t.Fatal("an impact line for a trace that is off")
	}
}
