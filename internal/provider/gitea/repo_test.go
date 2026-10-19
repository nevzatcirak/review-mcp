package gitea_test

import (
	"net/url"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
)

// TestParseRepoPath is Gitea's repository URL table (v2.5 spec V-13b
// §1.1): the repository, a branch below /src/branch/ (every remaining
// segment, so a branch with "/"), the ".git" suffix dropped, and every
// refusal.
func TestParseRepoPath(t *testing.T) {
	cases := []struct {
		in               string
		ns, repo, branch string
		ok               bool
	}{
		{"/owner/repo", "owner", "repo", "", true},
		{"/owner/repo.git", "owner", "repo", "", true},
		{"/owner/repo/src/branch/main", "owner", "repo", "main", true},
		{"/owner/repo/src/branch/feature/x", "owner", "repo", "feature/x", true},
		{"/owner/repo.git/src/branch/a_b-c.d", "owner", "repo", "a_b-c.d", true},
		{"/own%65r/repo", "owner", "repo", "", true},
		{"/owner/repo/src/branch", "", "", "", false},          // no branch
		{"/owner/repo/src/branch/", "", "", "", false},         // an empty branch
		{"/owner/repo/src/branch/a..b", "", "", "", false},     // a refused branch
		{"/owner/repo/src/branch/.hidden", "", "", "", false},  // a refused branch
		{"/owner/repo/src/branch/a%2Fb", "", "", "", false},    // an escaped slash in the branch
		{"/owner/repo/src/commit/0123abcd", "", "", "", false}, // a commit, not a branch
		{"/owner/repo/src/tag/v1", "", "", "", false},          // a tag, not a branch
		{"/owner/repo/tree/main", "", "", "", false},           // another forge's form
		{"/owner/repo/pulls/7", "", "", "", false},             // a pull request
		{"/owner/repo/pulls", "", "", "", false},               // another page
		{"/owner", "", "", "", false},                          // no repository
		{"/owner/.git", "", "", "", false},                     // nothing left of the name
		{"/own%2Fer/repo", "", "", "", false},                  // an escaped slash
		{"/owner//repo", "", "", "", false},                    // an empty segment
		{"/owner/%2E%2E", "", "", "", false},                   // an escaped dot segment
		{"/owner/re%zzpo", "", "", "", false},                  // a bad escape
		{"owner/repo", "", "", "", false},                      // no leading slash
		{"", "", "", "", false},
	}
	for _, c := range cases {
		ns, repo, branch, err := gitea.NewFactory().ParseRepoPath(c.in, nil)
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && (ns != c.ns || repo != c.repo || branch != c.branch) {
			t.Errorf("%q: got %q %q %q", c.in, ns, repo, branch)
		}
	}
	// The query is not read.
	if _, _, branch, err := gitea.NewFactory().ParseRepoPath("/owner/repo", url.Values{"at": {"refs/heads/x"}}); err != nil || branch != "" {
		t.Errorf("a query named the branch %q (%v)", branch, err)
	}
}

// TestResolveItemGitea: through the resolver (web_url included), a pull
// request URL stays a pull request item and a branch URL is a repository
// item.
func TestResolveItemGitea(t *testing.T) {
	cfg := config.Defaults()
	cfg.Gitea.BaseURL, cfg.Gitea.WebURL = "https://your-gitea.example", "https://web.your-gitea.example/gitea"
	r := provider.NewResolver(cfg, nil, gitea.NewFactory())
	if kind, ref, _, _, err := r.ResolveItem("https://your-gitea.example/owner/repo/pulls/7"); err != nil ||
		kind != provider.ItemPullRequest || ref.Number != 7 {
		t.Errorf("pull request: %v %+v %v", kind, ref, err)
	}
	kind, _, repo, _, err := r.ResolveItem("https://web.your-gitea.example/gitea/owner/repo/src/branch/feature/x")
	if err != nil || kind != provider.ItemRepository || repo.Namespace != "owner" || repo.Repo != "repo" || repo.Branch != "feature/x" {
		t.Errorf("repository: %v %+v %v", kind, repo, err)
	}
}
