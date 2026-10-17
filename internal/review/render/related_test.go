package render

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/review"
)

// TestRelatedInCoverage: the coverage section lists the related pull
// requests that were read, as links with their status; the others are left
// to the notes, and nothing is listed without one read.
func TestRelatedInCoverage(t *testing.T) {
	res := fixtures()["all_fields"]
	if res == nil {
		for _, r := range fixtures() {
			res = r
			break
		}
	}
	res.Coverage.Related = []review.RelatedItem{
		{URL: "https://gitlab.example/grp/svc/-/merge_requests/3", Status: "partial", HeadSHA: "abc", FilesIncluded: 1, FilesListed: 2},
		{URL: "https://git.example/o/r/pulls/9", Status: "not_placed", Reason: "did_not_fit", HeadSHA: "def"},
		{URL: "https://unknown.example/a/b/pulls/1", Status: "failed", Reason: "url_not_configured"},
	}
	for name, out := range map[string]string{"gfm": Provider(res, capsGitea), "plain": Provider(res, capsBB), "client": Client(res)} {
		for _, want := range []string{
			"Related pull requests (context only; nothing was published to them):",
			"- [gitlab.example/grp/svc/-/merge\\_requests/3](https://gitlab.example/grp/svc/-/merge_requests/3): `partial`",
			"- [git.example/o/r/pulls/9](https://git.example/o/r/pulls/9): `not_placed (did_not_fit)`",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, out)
			}
		}
		if strings.Contains(out, "unknown.example") {
			t.Errorf("%s lists a related pull request that was not read", name)
		}
	}
	res.Coverage.Related = []review.RelatedItem{{URL: "https://unknown.example/a/b/pulls/1", Status: "failed", Reason: "url_not_configured"}}
	if strings.Contains(Client(res), "Related pull requests") {
		t.Error("a related section without a read item")
	}
}

// TestRelatedFindingSuffix: a finding in a related pull request is marked
// in every overview as listed only, with a link to that pull request.
func TestRelatedFindingSuffix(t *testing.T) {
	res := &review.Result{PR: basePR(), EnabledFields: allKeys, Review: &review.Review{KeyIssuesToReview: []review.KeyIssue{{
		RelevantFile: "proto/user.proto", IssueHeader: "Field renamed", IssueContent: "c", StartLine: 1, EndLine: 2,
		Link: "https://gitlab.example/grp/svc/-/blob/abc/proto/user.proto#L1", Related: "https://gitlab.example/grp/svc/-/merge_requests/3",
		RelatedName: "svc#3"}}}}
	gfm := Provider(res, capsGitea)
	if !strings.Contains(gfm, "(in related pull request <a href='https://gitlab.example/grp/svc/-/merge_requests/3'>svc#3</a>, listed here only)") {
		t.Errorf("GFM overview:\n%s", gfm)
	}
	plain := Provider(res, capsBB)
	if !strings.Contains(plain, "(in related pull request [svc#3](https://gitlab.example/grp/svc/-/merge_requests/3), listed here only)") {
		t.Errorf("plain overview:\n%s", plain)
	}
	if strings.Contains(gfm, "(listed here only)") {
		t.Error("the plain listed-here suffix next to the related one")
	}
}
