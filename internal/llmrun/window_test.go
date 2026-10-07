package llmrun

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
)

type fakeResolver struct {
	n      int
	err    error
	probes int
}

func (f *fakeResolver) ResolveContextWindow(context.Context) (int, string, error) {
	f.probes++
	return f.n, "endpoint, 90% of 20000", f.err
}

func cfgWith(window int) *config.Config {
	cfg := config.Defaults()
	cfg.LLM.ContextWindow = window
	return cfg
}

// A set llm.context_window always wins, and nothing is asked of the endpoint.
func TestContextWindowConfigWins(t *testing.T) {
	r := &fakeResolver{n: 18000}
	n, src, err := ContextWindow(context.Background(), cfgWith(32000), r)
	if err != nil || n != 32000 || src != SourceConfig {
		t.Fatalf("got %d %q %v, want 32000 config", n, src, err)
	}
	if r.probes != 0 {
		t.Errorf("probes = %d, want 0 when llm.context_window is set", r.probes)
	}
}

func TestContextWindowFromEndpointWhenUnset(t *testing.T) {
	r := &fakeResolver{n: 18000}
	n, src, err := ContextWindow(context.Background(), cfgWith(0), r)
	if err != nil || n != 18000 || src != "endpoint, 90% of 20000" {
		t.Fatalf("got %d %q %v", n, src, err)
	}
	if r.probes != 1 {
		t.Errorf("probes = %d, want 1", r.probes)
	}
}

func TestContextWindowProbeErrorIsReturned(t *testing.T) {
	want := &llm.Error{Class: llm.ClassAuth}
	_, _, err := ContextWindow(context.Background(), cfgWith(0), &fakeResolver{err: want})
	if !errors.Is(err, llm.ErrAuth) {
		t.Fatalf("err = %v, want the auth error", err)
	}
}

// A client that cannot ask the endpoint gives the fixed sentence, never a
// guessed number.
func TestContextWindowNoResolverFailsWithFixedSentence(t *testing.T) {
	for name, c := range map[string]any{"nil": nil, "not a resolver": struct{}{}} {
		t.Run(name, func(t *testing.T) {
			n, _, err := ContextWindow(context.Background(), cfgWith(0), c)
			if err == nil || n != 0 || !strings.Contains(err.Error(), "set llm.context_window") {
				t.Fatalf("got %d %v", n, err)
			}
		})
	}
}
