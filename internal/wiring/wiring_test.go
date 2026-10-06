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
