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
