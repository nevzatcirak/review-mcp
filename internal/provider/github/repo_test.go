package github

import (
	"net/url"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// TestParseRepoPath is GitHub's repository URL table (v2.5 spec V-13b
// §1.1): the repository, a branch below /tree/ (every remaining segment, so
// a branch with "/"), the ".git" suffix dropped, and every refusal.
func TestParseRepoPath(t *testing.T) {
	cases := []struct {
		in               string
		ns, repo, branch string
		ok               bool
	}{
		{"/octo/demo", "octo", "demo", "", true},
		{"/octo/demo.git", "octo", "demo", "", true},
		{"/octo/demo.js", "octo", "demo.js", "", true},
		{"/octo/demo/tree/main", "octo", "demo", "main", true},
		{"/octo/demo/tree/feature/x", "octo", "demo", "feature/x", true},
		{"/octo/demo/tree/release/2.5/rc", "octo", "demo", "release/2.5/rc", true},
		{"/octo/demo.git/tree/main", "octo", "demo", "main", true},
		{"/o%20x/de%6Do", "o x", "demo", "", true},
		{"/octo/demo/tree/fe%61ture", "octo", "demo", "feature", true},
		{"/octo/demo/tree", "", "", "", false},                // no branch
		{"/octo/demo/tree/", "", "", "", false},               // an empty branch
		{"/octo/demo/tree/a..b", "", "", "", false},           // a refused branch
		{"/octo/demo/tree/HEAD", "", "", "", false},           // a refused branch
		{"/octo/demo/tree/x.lock", "", "", "", false},         // a refused branch
		{"/octo/demo/tree/-x", "", "", "", false},             // a refused branch
		{"/octo/demo/tree/a%2Fb", "", "", "", false},          // an escaped slash in the branch
		{"/octo/demo/tree/a//b", "", "", "", false},           // an empty segment
		{"/octo/demo/tree/a/%2e%2e/b", "", "", "", false},     // an escaped dot segment
		{"/octo/demo/blob/main/README.md", "", "", "", false}, // a file
		{"/octo/demo/pull/7", "", "", "", false},              // a pull request, not a repository
		{"/octo/demo/pulls", "", "", "", false},               // another page
		{"/octo/demo/issues", "", "", "", false},              // another page
		{"/octo", "", "", "", false},                          // no repository
		{"/octo/.git", "", "", "", false},                     // nothing left of the name
		{"/octo%2Fsub/demo", "", "", "", false},               // an escaped slash
		{"/octo/de%2fmo", "", "", "", false},                  // an escaped slash, lower case
		{"//demo", "", "", "", false},                         // an empty owner
		{"/../demo", "", "", "", false},                       // a dot segment
		{"/octo/de%zzmo", "", "", "", false},                  // a bad escape
		{"/octo/demo/", "", "", "", false},                    // an empty segment
		{"octo/demo", "", "", "", false},                      // no leading slash
		{"", "", "", "", false},
	}
	for _, c := range cases {
		ns, repo, branch, err := NewFactory().ParseRepoPath(c.in, url.Values{"at": {"refs/heads/ignored"}})
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && (ns != c.ns || repo != c.repo || branch != c.branch) {
			t.Errorf("%q: got %q %q %q", c.in, ns, repo, branch)
		}
	}
}

// TestResolveItemGitHub: through the resolver, a pull request URL stays a
// pull request item and a tree URL is a repository item.
func TestResolveItemGitHub(t *testing.T) {
	cfg := config.Defaults()
	cfg.GitHub.BaseURL = "https://github.example.com/ghe"
	r := provider.NewResolver(cfg, nil, NewFactory())
	if kind, ref, _, _, err := r.ResolveItem("https://github.example.com/ghe/octo/demo/pull/7/files"); err != nil ||
		kind != provider.ItemPullRequest || ref.Number != 7 {
		t.Errorf("pull request: %v %+v %v", kind, ref, err)
	}
	kind, _, repo, _, err := r.ResolveItem("https://github.example.com/ghe/octo/demo/tree/feature/x")
	if err != nil || kind != provider.ItemRepository || repo.Namespace != "octo" || repo.Repo != "demo" || repo.Branch != "feature/x" ||
		repo.Kind != provider.KindGitHub {
		t.Errorf("repository: %v %+v %v", kind, repo, err)
	}
}
