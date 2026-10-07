package render

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

const (
	bannerPartial = "**Partial review: 2 of 5 changed files were reviewed. 3 files were not reviewed (see Coverage); nothing is concluded about them.**"
	heading       = "## PR Review 🔍"
)

// completeCoverage has every reviewable file in full; the files excluded on
// purpose or with nothing to review are listed but do not count (X-18).
func completeCoverage() review.Coverage {
	return review.Coverage{
		Included: []string{"cmd/app/main.go", "internal/util/strings_util.go"},
		Clipped:  []string{},
		Omitted:  review.OmittedFiles{Added: []string{}, Modified: []string{}, Deleted: []string{}},
		Skipped:  []review.SkippedFile{{Path: "assets/logo.png", Reason: provider.SkipBinary}},
		Filtered: []review.SkippedFile{{Path: "go.sum", Reason: "lockfile_or_minified"}},
	}
}

// noFindingsResult is a run that found nothing, with the given coverage.
func noFindingsResult(cov review.Coverage) *review.Result {
	return &review.Result{
		PR: basePR(), EnabledFields: allKeys, Coverage: cov, Notes: []string{},
		Review: &review.Review{
			EstimatedEffortToReview: ip(1), RelevantTests: bp(false), SecurityConcerns: sp(review.SecurityNo),
			PerformanceConcerns: sp(review.PerformanceNo), KeyIssuesToReview: []review.KeyIssue{},
		},
	}
}

func profiles() map[string]func(*review.Result) string {
	return map[string]func(*review.Result) string{
		"client":    Client,
		"gitea":     func(r *review.Result) string { return Provider(r, capsGitea) },
		"bitbucket": func(r *review.Result) string { return Provider(r, capsBB) },
	}
}

// TestClientPartialBannerIsFirstLine: the first line of the client text is
// the banner, before the heading.
func TestClientPartialBannerIsFirstLine(t *testing.T) {
	got := Client(noFindingsResult(baseCoverage()))
	lines := strings.Split(got, "\n")
	if lines[0] != bannerPartial || lines[1] != "" || lines[2] != "## PR Review" {
		t.Errorf("head of the client text:\n%s", strings.Join(lines[:4], "\n"))
	}
}

// TestProviderPartialBannerFollowsHeading: the banner is right under the
// heading, a warning blockquote on Gitea and a bold line on Bitbucket.
func TestProviderPartialBannerFollowsHeading(t *testing.T) {
	res := noFindingsResult(baseCoverage())
	for profile, want := range map[string]string{
		"gitea":     "> ⚠️ " + bannerPartial,
		"bitbucket": bannerPartial,
	} {
		got := profiles()[profile](res)
		lines := strings.Split(got, "\n")
		if lines[0] != heading || lines[1] != "" || lines[2] != want || lines[3] != "" {
			t.Errorf("%s head:\n%s", profile, strings.Join(lines[:5], "\n"))
		}
	}
}

// TestCompleteHasNoBanner: a complete run, also one with filtered, binary
// and empty-diff files, has no banner and keeps the unscoped wording.
func TestCompleteHasNoBanner(t *testing.T) {
	res := noFindingsResult(completeCoverage())
	for profile, render := range profiles() {
		got := render(res)
		for _, bad := range []string{"Partial", "in the reviewed files", "⚠️"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s: complete run contains %q:\n%s", profile, bad, got)
			}
		}
		for _, want := range []string{textNoSecurity, textNoPerf, textNoIssues} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: %q missing:\n%s", profile, want, got)
			}
		}
	}
}

// TestPartialScopesNoConcerns: in every profile a partial run claims
// nothing about the files that were not reviewed.
func TestPartialScopesNoConcerns(t *testing.T) {
	res := noFindingsResult(baseCoverage())
	for profile, render := range profiles() {
		got := render(res)
		for _, want := range []string{textNoSecurityScoped, textNoPerfScoped, textNoIssuesScoped} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: %q missing:\n%s", profile, want, got)
			}
		}
		// The unscoped sentences appear only as the start of the scoped ones.
		for _, bare := range []string{textNoSecurity, textNoPerf} {
			if strings.Count(got, bare) != strings.Count(got, bare+" in the reviewed files") {
				t.Errorf("%s: unscoped %q in a partial run:\n%s", profile, bare, got)
			}
		}
		if strings.Contains(got, textNoIssues) {
			t.Errorf("%s: %q in a partial run:\n%s", profile, textNoIssues, got)
		}
	}
}

// TestPartialStillShowsFindingsAndConcerns: scoping changes only the "No"
// statements; findings and concerns are shown as before.
func TestPartialStillShowsFindingsAndConcerns(t *testing.T) {
	res := fixtures()["all_fields"]
	for profile, render := range profiles() {
		got := render(res)
		for _, want := range []string{"SQL injection", "N+1 access", "Possible Issue"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: %q missing", profile, want)
			}
		}
		if strings.Contains(got, "in the reviewed files") {
			t.Errorf("%s: scoped wording although concerns were found", profile)
		}
	}
}

// TestFilteredOnlyIsNotPartial and TestClippedAloneIsPartial pin the
// classification at the renderer.
func TestFilteredOnlyIsNotPartial(t *testing.T) {
	cov := completeCoverage()
	cov.Filtered = append(cov.Filtered, review.SkippedFile{Path: "dist/app.min.js", Reason: "lockfile_or_minified"})
	for profile, render := range profiles() {
		if got := render(noFindingsResult(cov)); strings.Contains(got, "Partial") {
			t.Errorf("%s: filtered-only run is partial:\n%s", profile, got)
		}
	}
}

func TestClippedAloneIsPartial(t *testing.T) {
	cov := completeCoverage()
	cov.Clipped = []string{"internal/big/large.go"}
	want := "**Partial review: 2 of 3 changed files were reviewed. 1 file was not reviewed (see Coverage); nothing is concluded about it.**"
	for profile, render := range profiles() {
		if got := render(noFindingsResult(cov)); !strings.Contains(got, want) {
			t.Errorf("%s: banner missing for a clipped file:\n%s", profile, got)
		}
	}
}

// TestPartialBannerIsDeterministic pins the exact sentences, including the
// singular forms.
func TestPartialBannerIsDeterministic(t *testing.T) {
	for _, tc := range []struct {
		name          string
		included, not int
		want          string
	}{
		{"many", 4, 3, "**Partial review: 4 of 7 changed files were reviewed. 3 files were not reviewed (see Coverage); nothing is concluded about them.**"},
		{"one reviewed", 1, 4, "**Partial review: 1 of 5 changed files was reviewed. 4 files were not reviewed (see Coverage); nothing is concluded about them.**"},
		{"one not reviewed", 9, 1, "**Partial review: 9 of 10 changed files were reviewed. 1 file was not reviewed (see Coverage); nothing is concluded about it.**"},
		{"none reviewed", 0, 3, "**Partial review: 0 of 3 changed files were reviewed. 3 files were not reviewed (see Coverage); nothing is concluded about them.**"},
		{"single file", 0, 1, "**Partial review: 0 of 1 changed file was reviewed. 1 file was not reviewed (see Coverage); nothing is concluded about it.**"},
	} {
		cov := review.Coverage{Included: []string{}, Clipped: []string{}}
		for i := 0; i < tc.included; i++ {
			cov.Included = append(cov.Included, "ok.go")
		}
		for i := 0; i < tc.not; i++ {
			cov.Clipped = append(cov.Clipped, "part.go")
		}
		got := Client(noFindingsResult(cov))
		if first, _, _ := strings.Cut(got, "\n"); first != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, first, tc.want)
		}
	}
}

// TestCoverageDeletedListed: X-20. Deleted files listed by name are counted
// as reviewed (no banner) and listed under their own heading, apart from
// the files left out.
func TestCoverageDeletedListed(t *testing.T) {
	cov := completeCoverage()
	cov.DeletedListed = []string{"old/gone.go", "old/removed.go"}
	cov.Finalize()
	for name, render := range profiles() {
		out := render(noFindingsResult(cov))
		if strings.Contains(out, "Partial review") {
			t.Errorf("%s: listed deletions made the review partial\n%s", name, out)
		}
		for _, want := range []string{
			"- Included: 2 files\n- Deleted (listed by name): 2 files\n- Omitted: 2 files\n",
			"Deleted (listed by name) (2):\n\n- `old/gone.go`\n- `old/removed.go`\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: want %q in\n%s", name, want, out)
			}
		}
		if strings.Contains(out, "deleted files)") {
			t.Errorf("%s: listed deletions shown as left out\n%s", name, out)
		}
	}
}
