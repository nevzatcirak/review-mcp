package provider

import (
	"slices"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

func kindsOf(ps []Provider) []Kind {
	var out []Kind
	for _, p := range ps {
		out = append(out, p.Kind())
	}
	return out
}

// TestResolverEnabled: one provider per enabled and registered kind, in the
// fixed kind order whatever the order of the factories, Gitea once although
// it has two candidates, nothing for a kind that is not enabled or not
// registered, and no network I/O.
func TestResolverEnabled(t *testing.T) {
	c := cfgFor("https://your-gitea.example", "https://web.your-gitea.example", "https://bitbucket.example.com")
	c.GitLab.BaseURL = "https://gitlab.example.com"
	c.GitHub.BaseURL = "https://github.example.com" // enabled, but no factory below
	closes := &atomic.Int64{}
	r := NewResolver(c, nil, &fakeFactory{kind: KindGitLab}, &fakeFactory{kind: KindBitbucketServer},
		closingFactory{&fakeFactory{kind: KindGitea}, closes})

	want := []Kind{KindGitea, KindBitbucketServer, KindGitLab}
	if got := r.EnabledKinds(); !slices.Equal(got, want) {
		t.Fatalf("EnabledKinds = %v, want %v", got, want)
	}
	if got := kindsOf(r.Enabled()); !slices.Equal(got, want) {
		t.Fatalf("Enabled = %v, want %v", got, want)
	}
	if got := kindsOf(r.EnabledOf(KindGitLab, KindGitHub, KindGitea)); !slices.Equal(got, []Kind{KindGitea, KindGitLab}) {
		t.Fatalf("EnabledOf = %v", got)
	}
	if got := r.EnabledOf(); len(got) != 0 {
		t.Fatalf("EnabledOf() = %v", got)
	}

	// Everything built is remembered for CloseIdleConnections.
	r.CloseIdleConnections()
	if got := closes.Load(); got != 2 {
		t.Fatalf("CloseIdleConnections reached %d Gitea providers, want 2 (two builds)", got)
	}
}

func TestResolverEnabledNothingEnabled(t *testing.T) {
	var nilResolver *Resolver
	if nilResolver.Enabled() != nil || nilResolver.EnabledKinds() != nil {
		t.Fatal("a nil resolver enables nothing")
	}
	r := NewResolver(config.Defaults(), nil, &fakeFactory{kind: KindGitea})
	if len(r.Enabled()) != 0 || len(r.EnabledKinds()) != 0 {
		t.Fatal("no kind is enabled by the defaults")
	}
}

// TestResolverEnabledBuildFailure: a provider that cannot be built is left
// out of Enabled but stays in EnabledKinds, so a caller can tell.
func TestResolverEnabledBuildFailure(t *testing.T) {
	r := NewResolver(cfgFor("https://your-gitea.example", "", "https://bitbucket.example.com"), nil,
		&fakeFactory{kind: KindGitea, newErr: ErrUnsupportedVersion}, &fakeFactory{kind: KindBitbucketServer})
	if got := kindsOf(r.Enabled()); !slices.Equal(got, []Kind{KindBitbucketServer}) {
		t.Fatalf("Enabled = %v", got)
	}
	if got := r.EnabledKinds(); !slices.Equal(got, []Kind{KindGitea, KindBitbucketServer}) {
		t.Fatalf("EnabledKinds = %v", got)
	}
}
