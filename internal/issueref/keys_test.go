package issueref

import (
	"slices"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestDetectKeys(t *testing.T) {
	cases := []struct {
		name                string
		title, branch, desc string
		want                []string
	}{
		{name: "title", title: "PROJ-123: add login", want: []string{"PROJ-123"}},
		{name: "branch", branch: "feature/PROJ-7-login", want: []string{"PROJ-7"}},
		{name: "description", desc: "Implements AB2-45.", want: []string{"AB2-45"}},
		{name: "order and repeats", title: "PROJ-2", branch: "PROJ-1-x", desc: "PROJ-3, PROJ-2 and PROJ-1",
			want: []string{"PROJ-2", "PROJ-1", "PROJ-3"}},
		{name: "at most five", desc: "A1-1 B1-2 C1-3 D1-4 E1-5 F1-6", want: []string{"A1-1", "B1-2", "C1-3", "D1-4", "E1-5"}},
		{name: "inside a URL", desc: "See https://tracker.example/browse/PROJ-9 and http://tracker.example/PROJ-10?x=1",
			want: nil},
		{name: "inside a code span", desc: "Run `make PROJ-11` and ``PROJ-12``", want: nil},
		{name: "inside a fenced block", desc: "```\nPROJ-13\n```", want: nil},
		{name: "lower case", desc: "proj-14 Proj-15", want: nil},
		{name: "one letter", desc: "A-1", want: nil},
		{name: "prefix too long", desc: "ABCDEFGHIJK-1", want: nil},
		{name: "number too long", desc: "PROJ-12345678", want: nil},
		{name: "inside a word", desc: "xPROJ-16 PROJ-17x PROJ_18-1", want: nil},
		{name: "starting with a digit", desc: "1PROJ-19", want: nil},
		{name: "a key next to a URL", desc: "PROJ-20 https://tracker.example/browse/PROJ-21", want: []string{"PROJ-20"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DetectKeys(&provider.PullRequest{Title: c.title, SourceBranch: c.branch, Description: c.desc})
			if !slices.Equal(got, c.want) {
				t.Errorf("DetectKeys = %v, want %v", got, c.want)
			}
		})
	}
	if got := DetectKeys(nil); got != nil {
		t.Errorf("DetectKeys(nil) = %v, want nil", got)
	}
}
