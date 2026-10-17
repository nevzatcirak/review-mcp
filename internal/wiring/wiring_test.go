package wiring

import (
	"errors"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestNewResolverResolvesBothProviders(t *testing.T) {
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = "https://your-gitea.example"
	cfg.BitbucketServer.BaseURL = "https://bitbucket.example.com/bb"
	r := NewResolver(cfg, nil)

	ref, p, err := r.Resolve("https://your-gitea.example/octo/demo/pulls/7")
	if err != nil || p == nil || ref.Kind != provider.KindGitea || ref.Number != 7 {
		t.Fatalf("gitea: ref %+v err %v", ref, err)
	}
	ref, p, err = r.Resolve("https://bitbucket.example.com/bb/projects/PROJ/repos/demo/pull-requests/9")
	if err != nil || p == nil || ref.Kind != provider.KindBitbucketServer || ref.Number != 9 {
		t.Fatalf("bitbucket: ref %+v err %v", ref, err)
	}
	if _, _, err = r.Resolve("https://other.example.net/octo/demo/pulls/7"); !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Errorf("foreign host: err = %v", err)
	}
}

func TestNewResolverResolvesGitHub(t *testing.T) {
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = "https://your-gitea.example"
	cfg.GitHub.BaseURL = "https://github.example.com"
	cfg.GitHub.APIURL = "https://api.github.example.com"
	r := NewResolver(cfg, nil)

	ref, p, err := r.Resolve("https://github.example.com/octo/demo/pull/7/files")
	if err != nil || p == nil || p.Kind() != provider.KindGitHub || ref.Kind != provider.KindGitHub || ref.Number != 7 {
		t.Fatalf("github: ref %+v err %v", ref, err)
	}
	// The API base receives API calls only; it is never matched.
	if _, _, err = r.Resolve("https://api.github.example.com/octo/demo/pull/7"); !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Errorf("api host: err = %v", err)
	}
	// No GitHub host is assumed when github.base_url is unset (X-2).
	cfg.GitHub = config.GitHub{}
	if _, _, err = NewResolver(cfg, nil).Resolve("https://github.com/octo/demo/pull/7"); !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Errorf("unconfigured github.com: err = %v", err)
	}
}

func TestNewResolverResolvesGitLab(t *testing.T) {
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = "https://your-gitea.example"
	cfg.GitLab.BaseURL = "https://example.com/gitlab"
	r := NewResolver(cfg, nil)

	ref, p, err := r.Resolve("https://example.com/gitlab/group/sub/project/-/merge_requests/7/diffs")
	if err != nil || p == nil || p.Kind() != provider.KindGitLab || ref.Kind != provider.KindGitLab ||
		ref.Namespace != "group/sub" || ref.Repo != "project" || ref.Number != 7 {
		t.Fatalf("gitlab: ref %+v err %v", ref, err)
	}
	// Outside the context path nothing matches.
	if _, _, err = r.Resolve("https://example.com/group/project/-/merge_requests/7"); !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Errorf("outside the context path: err = %v", err)
	}
	// No GitLab host is assumed when gitlab.base_url is unset (X-2).
	cfg.GitLab = config.GitLab{}
	if _, _, err = NewResolver(cfg, nil).Resolve("https://gitlab.com/group/project/-/merge_requests/7"); !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Errorf("unconfigured gitlab.com: err = %v", err)
	}
}

// TestResolverCapabilities: the resolver reports each enabled provider's
// capabilities from its factory, the same as a provider it builds; issue
// reads are on for Gitea, GitHub and GitLab and off for Bitbucket Server
// (X-42). A kind that is not enabled is not known.
func TestResolverCapabilities(t *testing.T) {
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = "https://your-gitea.example"
	cfg.BitbucketServer.BaseURL = "https://bitbucket.example.com/bb"
	cfg.GitHub.BaseURL = "https://github.example.com"
	r := NewResolver(cfg, nil)
	for _, c := range []struct {
		url    string
		kind   provider.Kind
		issues bool
	}{
		{"https://your-gitea.example/octo/demo/pulls/7", provider.KindGitea, true},
		{"https://bitbucket.example.com/bb/projects/PROJ/repos/demo/pull-requests/9", provider.KindBitbucketServer, false},
		{"https://github.example.com/octo/demo/pull/7", provider.KindGitHub, true},
	} {
		_, p, err := r.Resolve(c.url)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		got, ok := r.Capabilities(c.kind)
		if !ok || got != p.Capabilities() || got.Issues != c.issues {
			t.Errorf("%s: Capabilities = %+v, %v; want the provider's %+v with Issues %v", c.kind, got, ok, p.Capabilities(), c.issues)
		}
		if _, reader := p.(provider.IssueReader); reader != c.issues {
			t.Errorf("%s: implements IssueReader %v, want %v", c.kind, reader, c.issues)
		}
	}
	if _, ok := r.Capabilities(provider.KindGitLab); ok {
		t.Error("Capabilities of GitLab, which is not enabled: ok, want unknown")
	}
	cfg.GitLab.BaseURL = "https://example.com/gitlab"
	if got, ok := NewResolver(cfg, nil).Capabilities(provider.KindGitLab); !ok || !got.Issues {
		t.Errorf("Capabilities of GitLab = %+v, %v; want issue reads", got, ok)
	}
	if _, ok := (*provider.Resolver)(nil).Capabilities(provider.KindGitea); ok {
		t.Error("Capabilities of a nil resolver: ok, want unknown")
	}
}
