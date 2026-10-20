package render

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/review"
)

func intp(n int) *int       { return &n }
func strp(s string) *string { return &s }

// consumerResult is the all-fields fixture with a related pull request, two
// searched related repositories and one that failed, and two findings in
// the first repository: one linked at its commit, one on a file the run
// never saw.
func consumerResult() *review.Result {
	res := *fixtures()["all_fields"]
	const commit = "0fedcba9876543210fedcba9876543210fedcba9"
	res.Coverage.Related = []review.RelatedItem{
		{URL: "https://gitlab.example/grp/svc/-/merge_requests/3", Status: "full", HeadSHA: "1234567890abcdef1234567890abcdef12345678",
			FilesIncluded: 2},
		{URL: "https://gitlab.example/grp/web-ui/-/tree/feature/x", Status: "partial", Kind: "repository", Branch: strp("feature/x"),
			Commit: commit, Identifiers: intp(3), Matches: intp(7), MatchesShown: intp(5)},
		{URL: "https://gitlab.example/grp/api", Status: "no_use_found", Kind: "repository", Branch: strp(""), Commit: commit,
			Identifiers: intp(3), Matches: intp(0), MatchesShown: intp(0)},
		{URL: "https://gitlab.example/grp/gone/-/tree/x", Status: "failed", Reason: "branch_not_found", Kind: "repository",
			Branch: strp("x"), Identifiers: intp(0), Matches: intp(0), MatchesShown: intp(0)},
	}
	res.Review = &review.Review{KeyIssuesToReview: []review.KeyIssue{
		{RelevantFile: "src/client/profile.ts", IssueHeader: "Consumer reads the old name", IssueContent: "Needs the new name.",
			StartLine: 42, EndLine: 42, Related: "https://gitlab.example/grp/web-ui/-/tree/feature/x", RelatedName: "grp/web-ui",
			RelatedRepository: true, Link: "https://gitlab.example/grp/web-ui/-/blob/" + commit + "/src/client/profile.ts#L42"},
		{RelevantFile: "src/client/never-seen.ts", IssueHeader: "Maybe here too", IssueContent: "Needs verification.",
			StartLine: 7, EndLine: 7, Related: "https://gitlab.example/grp/web-ui/-/tree/feature/x", RelatedName: "grp/web-ui",
			RelatedRepository: true, LocationUnverified: true},
	}}
	return &res
}

// TestConsumerImpactRendering: the overview lists a consumer finding with
// its link at the commit and "(in related repository …, listed here
// only)", one on a file never seen without a link and with "(location not
// verified)"; the coverage section's related list names repositories and
// shows "searched {i} identifiers at {commit 12}: {m} matches" or "no use
// found at {commit 12}", and leaves the failed one to the notes.
func TestConsumerImpactRendering(t *testing.T) {
	const at = "https://gitlab.example/grp/web-ui/-/blob/0fedcba9876543210fedcba9876543210fedcba9/src/client/profile.ts#L42"
	for name, render := range map[string]func(*review.Result) string{
		"client": Client,
		"gfm":    func(r *review.Result) string { return Provider(r, capsGitea) },
		"plain":  func(r *review.Result) string { return Provider(r, capsBB) },
	} {
		t.Run(name, func(t *testing.T) {
			out := render(consumerResult())
			for _, want := range []string{
				at,
				"Related pull requests and repositories (context only; nothing was published to them):",
				"gitlab.example/grp/web-ui/-/tree/feature/x](https://gitlab.example/grp/web-ui/-/tree/feature/x): `searched 3 identifiers at 0fedcba98765: 7 matches`",
				"gitlab.example/grp/api](https://gitlab.example/grp/api): `no use found at 0fedcba98765`",
				"(https://gitlab.example/grp/svc/-/merge_requests/3): `full`",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("the overview lacks %q:\n%s", want, out)
				}
			}
			// The provider overviews say where the finding is; the client
			// view, like for related pull requests, marks only the
			// unverified location.
			wantSuffix := []string{"(in related repository ", ", listed here only) (location not verified)"}
			if name == "client" {
				wantSuffix = []string{"`src/client/never-seen.ts` L7 (location not verified)"}
			}
			for _, want := range wantSuffix {
				if !strings.Contains(out, want) {
					t.Errorf("the overview lacks %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "grp/gone") && strings.Contains(out, "grp/gone/-/tree/x):") {
				t.Error("a failed repository is listed in the coverage")
			}
			if strings.Count(out, "(location not verified)") != 1 {
				t.Errorf("(location not verified) appears %d times", strings.Count(out, "(location not verified)"))
			}
			// The unverified finding has no link of its own.
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "Maybe here too") && strings.Contains(line, "/blob/") {
					t.Errorf("the unverified finding has a link: %s", line)
				}
			}
		})
	}
}

// TestDeclaredRepositoryRendering: the overview names a related repository
// read because the repository config declares it (V-13b §3.2 item 3); one
// given by the call is listed as before.
func TestDeclaredRepositoryRendering(t *testing.T) {
	res := consumerResult()
	res.Coverage.Related[2].Source = "repo_config"
	for name, out := range map[string]string{"client": Client(res), "gfm": Provider(res, capsGitea), "plain": Provider(res, capsBB)} {
		if want := "(https://gitlab.example/grp/api): `no use found at 0fedcba98765` (declared in the repository config)"; !strings.Contains(out, want) {
			t.Errorf("%s: the overview lacks %q:\n%s", name, want, out)
		}
		if strings.Count(out, "declared in the repository config") != 1 {
			t.Errorf("%s: the mark appears %d times", name, strings.Count(out, "declared in the repository config"))
		}
	}
}

// TestRepositoryCountsInWords: the coverage line of a searched repository
// counts with the count helper (WP-13f): "1 identifier" and "1 match" for
// one, the plural for many, in every view.
func TestRepositoryCountsInWords(t *testing.T) {
	cases := []struct {
		identifiers, matches int
		want                 string
	}{
		{1, 1, "`searched 1 identifier at 0fedcba98765: 1 match`"},
		{1, 4, "`searched 1 identifier at 0fedcba98765: 4 matches`"},
		{2, 1, "`searched 2 identifiers at 0fedcba98765: 1 match`"},
		{3, 7, "`searched 3 identifiers at 0fedcba98765: 7 matches`"},
	}
	for _, c := range cases {
		res := consumerResult()
		res.Coverage.Related[1].Identifiers, res.Coverage.Related[1].Matches = intp(c.identifiers), intp(c.matches)
		for name, out := range map[string]string{"client": Client(res), "gfm": Provider(res, capsGitea), "plain": Provider(res, capsBB)} {
			if !strings.Contains(out, "(https://gitlab.example/grp/web-ui/-/tree/feature/x): "+c.want) {
				t.Errorf("%s, %d identifiers and %d matches: the overview lacks %s:\n%s", name, c.identifiers, c.matches, c.want, out)
			}
		}
	}
}
