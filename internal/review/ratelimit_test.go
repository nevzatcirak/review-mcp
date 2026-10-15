package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// tickingClock is a fake clock that a ticker moves forward 100 ms every
// 100 µs of real time; a sleep waits until the clock reaches its end, or
// until ctx is done. Concurrent runs share it.
type tickingClock struct {
	mu   sync.Mutex
	t    time.Time
	stop chan struct{}
}

func newTickingClock(t *testing.T) *tickingClock {
	c := &tickingClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), stop: make(chan struct{})}
	go func() {
		for {
			select {
			case <-c.stop:
				return
			case <-time.After(100 * time.Microsecond):
				c.mu.Lock()
				c.t = c.t.Add(100 * time.Millisecond)
				c.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() { close(c.stop) })
	return c
}

func (c *tickingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *tickingClock) sleep(ctx context.Context, d time.Duration) error {
	end := c.now().Add(d)
	for c.now().Before(end) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Microsecond):
		}
	}
	return nil
}

// chatAnswer is an OpenAI-compatible completion whose content is content.
func chatAnswer(content string) []byte {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
	})
	return b
}

// TestFocusedRunsShareTheRateLimit (X-37, v2.1 spec V-6 §2.5): four
// focused pr_review runs at once against an endpoint that answers 429 to a
// fourth request in the same (fake) minute, with requests_per_minute 3:
// every run succeeds, no request is answered 429, and the metadata counts
// the waits.
func TestFocusedRunsShareTheRateLimit(t *testing.T) {
	clk := newTickingClock(t)
	start := clk.now()
	var mu sync.Mutex
	perMinute := map[int64]int{}
	refused := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		minute := int64(clk.now().Sub(start) / time.Minute)
		mu.Lock()
		perMinute[minute]++
		over := perMinute[minute] > 3
		if over {
			refused++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if over {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write(chatAnswer(answerWith()))
	}))
	t.Cleanup(srv.Close)

	results := make([]*Result, 4)
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i, focus := range []string{"security", "architecture", "performance", "tests"} {
		h := newHarness()
		h.deps.Config.LLM.BaseURL = srv.URL
		h.deps.Config.LLM.RequestsPerMinute = 3
		h.deps.Config.LLM.MaxRateWaitSeconds = 120
		c, err := llm.New(h.deps.Config.LLM, config.NewSecret("sk-test-ratelimit"), nil,
			llm.WithClock(clk.now), llm.WithSleeper(clk.sleep))
		if err != nil {
			t.Fatal(err)
		}
		h.deps.LLM = c
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = Run(context.Background(), h.deps, Args{PRURL: testPRURL, Focus: focus})
		}()
	}
	wg.Wait()

	waits := 0
	for i, res := range results {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if res.Metadata.LLMCalls != 1 {
			t.Errorf("run %d: %d model calls", i, res.Metadata.LLMCalls)
		}
		waits += res.Metadata.RateLimitWaits
		if res.Metadata.RateLimitWaits > 0 {
			if note := llmrun.RateLimitNote(res.Metadata.RateLimitWaitMS); note == "" || !slices.Contains(res.Notes, note) {
				t.Errorf("run %d: waited %d ms, notes %q", i, res.Metadata.RateLimitWaitMS, res.Notes)
			}
		}
	}
	if refused != 0 {
		t.Errorf("%d requests were answered 429", refused)
	}
	if waits < 1 {
		t.Errorf("no run waited: the fourth request did not wait for the window")
	}
}
