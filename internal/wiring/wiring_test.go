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
