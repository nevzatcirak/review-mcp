package gitlab

import (
	"net/url"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// TestParseRepoPath is GitLab's repository URL table (v2.5 spec V-13b
// §1.1): nested groups, a branch below /-/tree/ (every remaining segment,
// so a branch with "/"), the ".git" suffix dropped, and every refusal.
func TestParseRepoPath(t *testing.T) {
	cases := []struct {
		in               string
		ns, repo, branch string
		ok               bool
	}{
		{"/group/project", "group", "project", "", true},
		{"/group/sub/sub2/project", "group/sub/sub2", "project", "", true},
		{"/group/sub/project.git", "group/sub", "project", "", true},
		{"/group/project/-/tree/main", "group", "project", "main", true},
		{"/group/sub/project/-/tree/feature/x", "group/sub", "project", "feature/x", true},
		{"/group/sub/project.git/-/tree/feature/x/y", "group/sub", "project", "feature/x/y", true},
		{"/gr%6Fup/project", "group", "project", "", true},
		{"/project", "", "", "", false},                          // no namespace
		{"/group/project/-/tree", "", "", "", false},             // no branch
		{"/group/project/-/tree/", "", "", "", false},            // an empty branch
		{"/group/project/-/tree/a..b", "", "", "", false},        // a refused branch
		{"/group/project/-/tree/a.lock", "", "", "", false},      // a refused branch
		{"/group/project/-/tree/a%2Fb", "", "", "", false},       // an escaped slash in the branch
		{"/group/project/-/blob/main/x.go", "", "", "", false},   // a file
		{"/group/project/-/merge_requests/7", "", "", "", false}, // a merge request
		{"/group/project/-/merge_requests", "", "", "", false},   // another page
		{"/group/project/-", "", "", "", false},                  // nothing after /-/
		{"/-/tree/main", "", "", "", false},                      // nothing before /-/
		{"/project/-/tree/main", "", "", "", false},              // no namespace before /-/
		{"/group/.git", "", "", "", false},                       // nothing left of the name
		{"/group%2Fsub/project", "", "", "", false},              // an escaped slash
		{"/group//project", "", "", "", false},                   // an empty segment
		{"/group/../project", "", "", "", false},                 // a dot segment
		{"/group/%2E%2E/project", "", "", "", false},             // an escaped dot segment
		{"/group/pro%zzject", "", "", "", false},                 // a bad escape
		{"/group/project/", "", "", "", false},                   // an empty segment
		{"group/project", "", "", "", false},                     // no leading slash
		{"", "", "", "", false},
	}
	for _, c := range cases {
		ns, repo, branch, err := NewFactory().ParseRepoPath(c.in, url.Values{"ref": {"ignored"}})
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && (ns != c.ns || repo != c.repo || branch != c.branch) {
			t.Errorf("%q: got %q %q %q", c.in, ns, repo, branch)
		}
	}
}

// TestResolveItemGitLab: under a context path, a merge request URL stays a
// pull request item (ParsePRPath first) and a nested group's tree URL is a
// repository item.
func TestResolveItemGitLab(t *testing.T) {
	cfg := config.Defaults()
	cfg.GitLab.BaseURL = "https://gitlab.example/gitlab"
	r := provider.NewResolver(cfg, nil, NewFactory())
	if kind, ref, _, _, err := r.ResolveItem("https://gitlab.example/gitlab/group/sub/project/-/merge_requests/3/diffs"); err != nil ||
		kind != provider.ItemPullRequest || ref.Namespace != "group/sub" || ref.Number != 3 {
		t.Errorf("merge request: %v %+v %v", kind, ref, err)
	}
	kind, _, repo, _, err := r.ResolveItem("https://gitlab.example/gitlab/group/sub/project/-/tree/feature/x")
	if err != nil || kind != provider.ItemRepository || repo.Namespace != "group/sub" || repo.Repo != "project" ||
		repo.Branch != "feature/x" || repo.Kind != provider.KindGitLab {
		t.Errorf("repository: %v %+v %v", kind, repo, err)
	}
}
