package render

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/issuectx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// complianceResult is a review with a verdict on two issues: #12 with a
// known URL, PROJ-1 without one. Its texts carry HTML and markdown.
func complianceResult() *review.Result {
	return &review.Result{
		PR: basePR(), EnabledFields: allKeys, Coverage: baseCoverage(),
		Review: &review.Review{KeyIssuesToReview: []review.KeyIssue{}},
		Issue: &review.IssueResult{Source: issuectx.SourceProvider, Refs: []issuectx.Ref{
			{Ref: "#12", Source: "keyword", Outcome: "used", URL: "https://your-gitea.example/org/repo/issues/12"},
			{Ref: "PROJ-1", Source: "argument", Outcome: "used"},
		}, DetectedKeys: []string{}, Compliance: &review.Compliance{Verdict: review.VerdictPartlyCompliant,
			Issues: []review.IssueVerdict{
				{Ref: "#12", Met: []string{"Retries once"}, NotMet: []string{"Shows <b>no</b> error | ever"},
					NeedsVerification: []string{}},
				{Ref: "PROJ-1", Met: []string{}, NotMet: []string{}, NeedsVerification: []string{"The `log` line"}},
			}}},
	}
}

// TestComplianceInTheOverview: a row with the verdict, then per issue the
// lists; the reference links to the issue when its URL is known; the text
// is escaped.
func TestComplianceInTheOverview(t *testing.T) {
	res := complianceResult()
	gfm := Provider(res, capsGitea)
	for _, want := range []string{
		"<tr><td>🎫&nbsp;<strong>Issue compliance</strong>: Partly compliant<br><br>",
		"<details><summary><a href='https://your-gitea.example/org/repo/issues/12'><strong>#12</strong></a></summary>",
		"<details><summary><strong>PROJ-1</strong></summary>",
		"<strong>Met</strong>\n\n- Retries once\n",
		"<strong>Needs human verification</strong>\n\n- The `log` line\n",
	} {
		if !strings.Contains(gfm, want) {
			t.Errorf("GFM overview lacks %q:\n%s", want, gfm)
		}
	}
	if strings.Contains(gfm, "<b>no</b>") || strings.Contains(gfm, "<strong>Not met</strong>\n\n</details>") {
		t.Errorf("unescaped text or an empty list in the GFM overview:\n%s", gfm)
	}

	plain := Provider(res, capsBB)
	for _, want := range []string{
		"| 🎫 Issue compliance | Partly compliant |",
		"### 🎫 Issue compliance",
		"**[\\#12](https://your-gitea.example/org/repo/issues/12)**",
		"- Not met:\n  - Shows \\<b\\>no\\</b\\> error \\| ever",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain overview lacks %q:\n%s", want, plain)
		}
	}
	if tagStart.MatchString(plain) {
		t.Errorf("HTML in the plain overview:\n%s", plain)
	}

	client := Client(res)
	if !strings.Contains(client, "- Issue compliance: Partly compliant\n") || !strings.Contains(client, "### Issue compliance\n") ||
		tagStart.MatchString(client) {
		t.Errorf("client view:\n%s", client)
	}
}

// TestNoComplianceRow: without a verdict there is no row and no section.
func TestNoComplianceRow(t *testing.T) {
	for name, res := range map[string]*review.Result{
		"no issue object": {PR: basePR(), EnabledFields: allKeys, Review: &review.Review{}},
		"no verdict": {PR: basePR(), EnabledFields: allKeys, Review: &review.Review{},
			Issue: &review.IssueResult{Source: issuectx.SourceNone}},
	} {
		for profile, out := range map[string]string{
			"gfm": Provider(res, capsGitea), "plain": Provider(res, capsBB), "client": Client(res),
			"list": Provider(res, provider.Capabilities{}),
		} {
			if strings.Contains(out, "Issue compliance") {
				t.Errorf("%s, %s: a compliance row without a verdict:\n%s", name, profile, out)
			}
		}
	}
}

func TestVerdictText(t *testing.T) {
	for v, want := range map[string]string{review.VerdictCompliant: "Compliant", review.VerdictPartlyCompliant: "Partly compliant",
		review.VerdictNotCompliant: "Not compliant", review.VerdictUnclear: "Unclear", "other": "Unclear"} {
		if got := verdictText(v); got != want {
			t.Errorf("verdictText(%q) = %q, want %q", v, got, want)
		}
	}
}
