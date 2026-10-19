package bitbucketserver_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
)

// TestParseRepoPath is Bitbucket Server's repository URL table (v2.5 spec
// V-13b §1.1): the repository with or without /browse, a branch only as
// "at=refs/heads/{branch}" on /browse, the ".git" suffix dropped, and every
// refusal.
func TestParseRepoPath(t *testing.T) {
	at := func(v ...string) url.Values { return url.Values{"at": v} }
	cases := []struct {
		in               string
		q                url.Values
		ns, repo, branch string
		ok               bool
	}{
		{"/projects/PROJ/repos/repo", nil, "PROJ", "repo", "", true},
		{"/projects/PROJ/repos/repo/browse", nil, "PROJ", "repo", "", true},
		{"/projects/PROJ/repos/repo.git", nil, "PROJ", "repo", "", true},
		{"/users/alice/repos/repo", nil, "~alice", "repo", "", true},
		{"/projects/PROJ/repos/repo/browse", at("refs/heads/main"), "PROJ", "repo", "main", true},
		{"/projects/PROJ/repos/repo/browse", at("refs/heads/feature/x"), "PROJ", "repo", "feature/x", true},
		{"/projects/PROJ/repos/repo/browse", url.Values{"at": {"refs/heads/main"}, "until": {"x"}}, "PROJ", "repo", "main", true},
		{"/projects/PROJ/repos/repo/browse", url.Values{"other": {"refs/heads/main"}}, "PROJ", "repo", "", true}, // only "at" is read
		{"/projects/PROJ/repos/repo/browse", at("main"), "", "", "", false},                                      // not refs/heads/
		{"/projects/PROJ/repos/repo/browse", at("refs/tags/v1"), "", "", "", false},                              // a tag
		{"/projects/PROJ/repos/repo/browse", at("0123456789abcdef"), "", "", "", false},                          // a commit
		{"/projects/PROJ/repos/repo/browse", at("refs/heads/"), "", "", "", false},                               // an empty branch
		{"/projects/PROJ/repos/repo/browse", at("refs/heads/a..b"), "", "", "", false},                           // a refused branch
		{"/projects/PROJ/repos/repo/browse", at("refs/heads/HEAD"), "", "", "", false},                           // a refused branch
		{"/projects/PROJ/repos/repo/browse", at("refs/heads/a", "refs/heads/b"), "", "", "", false},              // two values
		{"/projects/PROJ/repos/repo", at("refs/heads/main"), "", "", "", false},                                  // a branch only on /browse
		{"/projects/PROJ/repos/repo/browse/src/x.go", nil, "", "", "", false},                                    // a file
		{"/projects/PROJ/repos/repo/commits", nil, "", "", "", false},                                            // another page
		{"/projects/PROJ/repos/repo/pull-requests/7", nil, "", "", "", false},                                    // a pull request
		{"/projects/PROJ/repos", nil, "", "", "", false},                                                         // no slug
		{"/projects/PROJ/repos/.git", nil, "", "", "", false},                                                    // nothing left of the name
		{"/groups/PROJ/repos/repo", nil, "", "", "", false},                                                      // an unknown owner kind
		{"/projects/PROJ/repo/repo", nil, "", "", "", false},                                                     // not /repos/
		{"/projects/PR%2FOJ/repos/repo", nil, "", "", "", false},                                                 // an escaped slash
		{"/projects/PROJ/repos/%2E%2E", nil, "", "", "", false},                                                  // an escaped dot segment
		{"/projects//repos/repo", nil, "", "", "", false},                                                        // an empty segment
		{"/projects/PROJ/repos/re%zzpo", nil, "", "", "", false},                                                 // a bad escape
		{"projects/PROJ/repos/repo", nil, "", "", "", false},                                                     // no leading slash
		{"", nil, "", "", "", false},
	}
	for _, c := range cases {
		ns, repo, branch, err := bitbucketserver.NewFactory().ParseRepoPath(c.in, c.q)
		if (err == nil) != c.ok {
			t.Errorf("%q %v: err = %v, want ok=%v", c.in, c.q, err, c.ok)
			continue
		}
		if c.ok && (ns != c.ns || repo != c.repo || branch != c.branch) {
			t.Errorf("%q %v: got %q %q %q", c.in, c.q, ns, repo, branch)
		}
	}
}

// TestResolveItemBitbucket: through the resolver, the "at" query of a
// /browse URL names the branch (escaped or not), a pull request URL stays a
// pull request item, and nothing is sent to the server.
func TestResolveItemBitbucket(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	cfg := config.Defaults()
	cfg.BitbucketServer.BaseURL = srv.URL + "/bb"
	r := provider.NewResolver(cfg, nil, bitbucketserver.NewFactory())
	for _, u := range []string{
		srv.URL + "/bb/projects/PROJ/repos/repo/browse?at=refs/heads/feature/x",
		srv.URL + "/bb/projects/PROJ/repos/repo/browse?at=refs%2Fheads%2Ffeature%2Fx",
	} {
		kind, _, repo, _, err := r.ResolveItem(u)
		if err != nil || kind != provider.ItemRepository || repo.Namespace != "PROJ" || repo.Repo != "repo" || repo.Branch != "feature/x" {
			t.Errorf("%s: %v %+v %v", u, kind, repo, err)
		}
	}
	if kind, ref, _, _, err := r.ResolveItem(srv.URL + "/bb/projects/PROJ/repos/repo/pull-requests/7/overview?at=refs/heads/x"); err != nil ||
		kind != provider.ItemPullRequest || ref.Number != 7 {
		t.Errorf("pull request: %v %+v %v", kind, ref, err)
	}
	if _, _, _, _, err := r.ResolveItem(srv.URL + "/bb/projects/PROJ/repos/repo/browse?at=refs/tags/v1"); !errors.Is(err, provider.ErrURLMalformed) {
		t.Errorf("a tag: %v, want url_malformed", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("the server saw %d requests", hits.Load())
	}
}
