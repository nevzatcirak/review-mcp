package render

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/review"
)

func ciOf(counts review.CICounts, state string, checks ...review.CICheck) *review.CI {
	if checks == nil {
		checks = []review.CICheck{}
	}
	return &review.CI{State: state, Counts: counts, Checks: checks}
}

func TestCILine(t *testing.T) {
	for name, tc := range map[string]struct {
		ci   *review.CI
		want string
	}{
		"nil":  {nil, ""},
		"none": {ciOf(review.CICounts{}, review.CINone), "CI: no checks reported"},
		"unreadable": {&review.CI{State: review.CIUnreadable, Note: review.NoteCIUnreadable, Checks: []review.CICheck{}},
			"CI status could not be read"},
		"passing counts neutral and skipped": {
			ciOf(review.CICounts{Success: 2, Neutral: 1, Skipped: 1}, review.CIPassing),
			"CI: 0 failing, 0 pending, 4 passing"},
		"pending": {
			ciOf(review.CICounts{Success: 1, Pending: 2}, review.CIPending),
			"CI: 0 failing, 2 pending, 1 passing"},
		"failing names the failing checks with links": {
			ciOf(review.CICounts{Success: 1, Failure: 1, Error: 1, Pending: 1}, review.CIFailing,
				review.CICheck{Name: "unit tests", State: "failure", URL: "https://ci.example.com/t/1"},
				review.CICheck{Name: "integration", State: "error"},
				review.CICheck{Name: "lint", State: "pending", URL: "https://ci.example.com/l/1"}),
			"CI: 2 failing, 1 pending, 1 passing (failing: [unit tests](https://ci.example.com/t/1), integration)"},
		"incomplete read": {
			&review.CI{State: review.CIPassing, Counts: review.CICounts{Success: 3}, Checks: []review.CICheck{}, Note: review.NoteCITruncated},
			"CI: 0 failing, 0 pending, 3 passing; more checks exist than were read"},
		"partial read": {
			&review.CI{State: review.CIFailing, Counts: review.CICounts{Failure: 1}, Note: review.NoteCIPartial,
				Checks: []review.CICheck{{Name: "build", State: "failure"}}},
			"CI: 1 failing, 0 pending, 0 passing (failing: build); only part of the CI results could be read with this token"},
		"partial and incomplete": {
			&review.CI{State: review.CIPassing, Counts: review.CICounts{Success: 1}, Checks: []review.CICheck{},
				Note: review.NoteCIPartial + " " + review.NoteCITruncated},
			"CI: 0 failing, 0 pending, 1 passing; only part of the CI results could be read with this token; more checks exist than were read"},
		"partial without checks": {
			&review.CI{State: review.CINone, Checks: []review.CICheck{}, Note: review.NoteCIPartial},
			"CI: no checks reported; only part of the CI results could be read with this token"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := CILine(tc.ci); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestCILineNamesFailingOnly(t *testing.T) {
	var checks []review.CICheck
	for i := range 8 {
		checks = append(checks, review.CICheck{Name: "job" + string(rune('a'+i)), State: "failure"})
	}
	got := CILine(ciOf(review.CICounts{Failure: 8}, review.CIFailing, checks...))
	if !strings.HasSuffix(got, "(failing: joba, jobb, jobc, jobd, jobe and 3 more)") {
		t.Errorf("line = %q", got)
	}
	if strings.Contains(got, "jobf") {
		t.Errorf("more than 5 names: %q", got)
	}
}

// hostileCI has a failing check whose name tries markdown, HTML and a line
// break, and a link that is not http(s).
func hostileCI() *review.CI {
	return ciOf(review.CICounts{Failure: 2}, review.CIFailing,
		review.CICheck{Name: "x](https://example.com/evil) <img src=x onerror=1> **bold**\n# heading\n- [y](z)", State: "failure",
			URL: "https://ci.example.com/h/1"},
		review.CICheck{Name: "<script>alert(1)</script> | a | b", State: "failure", URL: "javascript:alert(1)"})
}

// TestCILineHostileNames: check names are untrusted; the overview line stays
// one line with no live markup or HTML, whatever the provider.
func TestCILineHostileNames(t *testing.T) {
	line := CILine(hostileCI())
	if strings.Contains(line, "\n") {
		t.Fatalf("the line breaks:\n%s", line)
	}
	for _, bad := range []string{"<img", "<script", "](https://example.com/evil)", "**bold**", "[y](z)", "javascript:"} {
		for from := 0; ; {
			i := strings.Index(line[from:], bad)
			if i < 0 {
				break
			}
			if i += from; i == 0 || line[i-1] != '\\' {
				t.Errorf("%q is live in %q", bad, line)
			}
			from = i + 1
		}
	}
	if !strings.Contains(line, "](https://ci.example.com/h/1)") {
		t.Errorf("the check's own link is missing: %q", line)
	}
	// Through every renderer: the same line, at the same place.
	res := &review.Result{PR: basePR(), EnabledFields: allKeys, Coverage: baseCoverage(), CI: hostileCI(),
		Review: &review.Review{KeyIssuesToReview: []review.KeyIssue{}}}
	for name, out := range map[string]string{"gitea": Provider(res, capsGitea), "bitbucket": Provider(res, capsBB), "client": Client(res)} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("%s output lacks the CI line:\n%s", name, out)
		}
		if strings.Contains(out, "<img") && !strings.Contains(out, `\<img`) {
			t.Errorf("%s: raw HTML from a check name", name)
		}
	}
}

// TestCILinePlacement: the line follows the run line in the provider
// overview and the pull request line in the client view; without ci nothing
// is added.
func TestCILinePlacement(t *testing.T) {
	res := &review.Result{PR: basePR(), EnabledFields: allKeys, Coverage: baseCoverage(),
		Review:   &review.Review{KeyIssuesToReview: []review.KeyIssue{}},
		Metadata: review.Metadata{ReviewedAt: "2026-10-06T12:00:00Z"},
		CI:       ciOf(review.CICounts{Success: 2, Pending: 1}, review.CIPending)}
	want := "CI: 0 failing, 1 pending, 2 passing"
	for name, out := range map[string]string{"gitea": Provider(res, capsGitea), "bitbucket": Provider(res, capsBB)} {
		i, j, k := strings.Index(out, "Reviewed on"), strings.Index(out, want), strings.Index(out, "- Included")
		if i < 0 || i >= j || j >= k || strings.Contains(out[i:j], "\n\n") {
			t.Errorf("%s: run line %d, CI line %d, coverage %d:\n%s", name, i, j, k, out)
		}
	}
	c := Client(res)
	if i, j := strings.Index(c, "Pull request:"), strings.Index(c, want); i < 0 || i >= j {
		t.Errorf("client:\n%s", c)
	}
	res.CI = nil
	for name, out := range map[string]string{"gitea": Provider(res, capsGitea), "bitbucket": Provider(res, capsBB), "client": Client(res)} {
		if strings.Contains(out, "CI") {
			t.Errorf("%s mentions CI without ci:\n%s", name, out)
		}
	}
}
