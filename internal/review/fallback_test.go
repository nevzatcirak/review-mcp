package review

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
)

// TestReviewServedByFallback (X-38, v2.1 spec V-6 §3.5): a review in three
// parts whose primary serves the first part and then answers 503 is served
// by fallback 1 from there on: models_used names both in order, model is
// the fallback, fallback_calls counts its calls, and the note says why.
func TestReviewServedByFallback(t *testing.T) {
	var mu sync.Mutex
	primaryCalls := 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		primaryCalls++
		n := primaryCalls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n > 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write(chatAnswer(findingsAnswer()))
	}))
	t.Cleanup(primary.Close)
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(chatAnswer(findingsAnswer()))
	}))
	t.Cleanup(fallback.Close)

	h, _ := chunkHarness(t, nil)
	cfg := h.deps.Config
	cfg.LLM.BaseURL, cfg.LLM.MaxRetries = primary.URL, 0
	pc, err := llm.New(cfg.LLM, config.NewSecret("sk-test-primary"), nil)
	if err != nil {
		t.Fatal(err)
	}
	fcfg := cfg.LLM
	fcfg.BaseURL, fcfg.Model = fallback.URL, "second-model"
	fc, err := llm.New(fcfg, config.Secret{}, nil, llm.WithoutKey())
	if err != nil {
		t.Fatal(err)
	}
	h.deps.LLM = llm.NewChain(pc, []*llm.Client{fc}, cfg.LLM.TokenEstimateFactor)
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	m := res.Metadata
	if m.Model != "second-model" || !slices.Equal(m.ModelsUsed, []string{"test-model", "second-model"}) || m.FallbackCalls != 2 {
		t.Errorf("metadata model %q, models_used %v, fallback_calls %d", m.Model, m.ModelsUsed, m.FallbackCalls)
	}
	note := "2 model calls were served by a fallback model (`second-model`) because the primary model was unavailable (server error)."
	if !slices.Contains(res.Notes, note) {
		t.Errorf("notes %q lack %q", res.Notes, note)
	}
	if primaryCalls != 2 {
		t.Errorf("the primary got %d requests, want 2 (the run stays on the fallback)", primaryCalls)
	}
}
