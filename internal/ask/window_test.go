package ask

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llm"
)

// windowLLM adds the context-window probe to the scripted model. It records
// how many provider requests had been made when the probe ran.
type windowLLM struct {
	*fakeLLM
	prov          *fakeProvider
	resolver      *fakeResolver
	n             int
	err           error
	probes        int
	provAtProbe   int
	resolveAtProb int
}

func (w *windowLLM) ResolveContextWindow(context.Context) (int, string, error) {
	w.probes++
	w.provAtProbe = w.prov.calls
	w.resolveAtProb = w.resolver.calls
	return w.n, "endpoint, 90% of 40000", w.err
}

func TestRunResolvesAnUnsetContextWindow(t *testing.T) {
	h := newHarness("It is bounded.")
	h.deps.Config.LLM.ContextWindow = 0
	w := &windowLLM{fakeLLM: h.llm, prov: h.prov, resolver: h.resolver, n: 36000}
	h.deps.LLM = w
	res, err := Run(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	if res.Metadata.ContextWindow != 36000 {
		t.Errorf("metadata context window = %d, want the resolved 36000", res.Metadata.ContextWindow)
	}
	if w.probes != 1 {
		t.Errorf("probes = %d, want 1", w.probes)
	}
	// After URL resolution, before any provider request.
	if w.resolveAtProb != 1 || w.provAtProbe != 0 {
		t.Errorf("at probe time: %d URL resolutions, %d provider requests; want 1 and 0", w.resolveAtProb, w.provAtProbe)
	}
}

// A failing probe sends nothing to the provider and nothing to the model.
func TestRunFailedProbeMakesNoProviderRequest(t *testing.T) {
	h := newHarness("It is bounded.")
	h.deps.Config.LLM.ContextWindow = 0
	h.deps.LLM = &windowLLM{fakeLLM: h.llm, prov: h.prov, resolver: h.resolver,
		err: &llm.Error{Class: llm.ClassAuth, Hint: "REVIEW_MCP_LLM_API_KEY"}}
	_, err := Run(context.Background(), h.deps, h.args())
	if !errors.Is(err, llm.ErrAuth) {
		t.Fatalf("err = %v, want the classified auth error", err)
	}
	if h.prov.calls != 0 || len(h.llm.calls) != 0 {
		t.Errorf("provider requests %d, model calls %d; want 0 and 0", h.prov.calls, len(h.llm.calls))
	}
	h.checkNoLeaks(t, err)
}

func TestRunConfiguredWindowIsNeverProbed(t *testing.T) {
	h := newHarness("It is bounded.")
	w := &windowLLM{fakeLLM: h.llm, prov: h.prov, resolver: h.resolver, n: 5000}
	h.deps.LLM = w
	res, err := Run(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	if w.probes != 0 || res.Metadata.ContextWindow != 32000 {
		t.Errorf("probes %d, window %d; want 0 and the configured 32000", w.probes, res.Metadata.ContextWindow)
	}
}

func TestRunUnsetWindowWithoutResolverFails(t *testing.T) {
	h := newHarness("It is bounded.")
	h.deps.Config.LLM.ContextWindow = 0
	_, err := Run(context.Background(), h.deps, h.args())
	if err == nil || !strings.Contains(err.Error(), "set llm.context_window") {
		t.Fatalf("err = %v", err)
	}
	if h.prov.calls != 0 {
		t.Errorf("provider requests = %d, want 0", h.prov.calls)
	}
}
