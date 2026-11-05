package tools

import (
	"slices"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// enabledFake is a resolver that enables a fixed set of kinds and records
// the kinds it was asked to build.
type enabledFake struct {
	kinds []provider.Kind
	built []provider.Kind
}

func (f *enabledFake) Resolve(string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{}, nil, provider.ErrURLNotConfigured
}

func (f *enabledFake) EnabledKinds() []provider.Kind { return f.kinds }

func (f *enabledFake) EnabledOf(kinds ...provider.Kind) []provider.Provider {
	var out []provider.Provider
	for _, k := range f.kinds {
		if slices.Contains(kinds, k) {
			f.built = append(f.built, k)
			out = append(out, kindProvider{kind: k})
		}
	}
	return out
}

type kindProvider struct {
	provider.Provider
	kind provider.Kind
}

func (p kindProvider) Kind() provider.Kind { return p.kind }

func kindsOfProviders(ps []provider.Provider) []provider.Kind {
	var out []provider.Kind
	for _, p := range ps {
		out = append(out, p.Kind())
	}
	return out
}

var allKinds = []provider.Kind{provider.KindGitea, provider.KindBitbucketServer, provider.KindGitHub, provider.KindGitLab}

// TestListSourceServeBuildsOnlyKindsWithTokens: the serve wrapper reports a
// kind without its token as no_token and builds nothing for it.
func TestListSourceServeBuildsOnlyKindsWithTokens(t *testing.T) {
	inner := &enabledFake{kinds: allKinds}
	cfg := config.Defaults().WithSecrets(config.Secrets{GiteaToken: config.NewSecret("g"), GitLabToken: config.NewSecret("l")})
	src := ListSourceOf(RequireCredentials(inner, cfg, false))
	if src == nil {
		t.Fatal("the credential wrapper is a ListSource")
	}
	if got := src.EnabledKinds(); !slices.Equal(got, allKinds) {
		t.Fatalf("EnabledKinds = %v", got)
	}
	ps, noToken := src.ListProviders(allKinds)
	if got := kindsOfProviders(ps); !slices.Equal(got, []provider.Kind{provider.KindGitea, provider.KindGitLab}) {
		t.Errorf("providers = %v", got)
	}
	if !slices.Equal(noToken, []provider.Kind{provider.KindBitbucketServer, provider.KindGitHub}) {
		t.Errorf("noToken = %v", noToken)
	}
	if !slices.Equal(inner.built, []provider.Kind{provider.KindGitea, provider.KindGitLab}) {
		t.Errorf("the inner resolver built %v, want only the kinds with a token", inner.built)
	}

	// Only the named kinds are considered.
	inner.built = nil
	ps, noToken = src.ListProviders([]provider.Kind{provider.KindGitHub})
	if len(ps) != 0 || !slices.Equal(noToken, []provider.Kind{provider.KindGitHub}) || len(inner.built) != 0 {
		t.Errorf("GitHub only: providers %v, noToken %v, built %v", kindsOfProviders(ps), noToken, inner.built)
	}
}

// TestListSourcePlain: in stdio mode every named kind is built and none is
// reported as no_token; a resolver that enables nothing is not a source.
func TestListSourcePlain(t *testing.T) {
	inner := &enabledFake{kinds: allKinds[:2]}
	src := ListSourceOf(inner)
	if src == nil {
		t.Fatal("a resolver that enables providers is a ListSource")
	}
	ps, noToken := src.ListProviders(allKinds)
	if got := kindsOfProviders(ps); !slices.Equal(got, allKinds[:2]) || len(noToken) != 0 {
		t.Errorf("providers = %v, noToken = %v", got, noToken)
	}
	if ListSourceOf(&kindResolver{}) != nil {
		t.Error("a resolver that does not enable providers has no ListSource")
	}
	if ps, nt := RequireCredentials(&kindResolver{}, nil, false).(ListSource).ListProviders(allKinds); len(ps) != 0 || len(nt) != 0 {
		t.Error("a wrapped resolver that enables nothing yields nothing")
	}
}
