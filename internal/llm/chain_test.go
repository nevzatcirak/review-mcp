package llm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// chainFake is one model endpoint of a chain test: it answers with
// answer(n) (the status, 200 for an ok answer naming the model it served).
type chainFake struct {
	*fake
}

func newChainFake(t *testing.T, answer func(n int) int) *chainFake {
	cf := &chainFake{}
	cf.fake = newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		st := answer(n)
		if st == 200 {
			writeJSON(w, 200, okJSON("served by "+cf.lastModel()))
			return
		}
		writeJSON(w, st, `{}`)
	})
	return cf
}

func (cf *chainFake) lastModel() string {
	b := cf.body(cf.calls() - 1)
	i := strings.Index(string(b), `"model":"`)
	if i < 0 {
		return ""
	}
	rest := string(b)[i+len(`"model":"`):]
	return rest[:strings.IndexByte(rest, '"')]
}

func always(st int) func(int) int { return func(int) int { return st } }

// chainClient builds a client of model on base with cfg's options; no key
// when key is "".
func chainClient(t *testing.T, base, model, key string, mutate func(*config.LLM)) *Client {
	t.Helper()
	cfg := baseCfg(base)
	cfg.Model, cfg.MaxRetries, cfg.MaxRateRetries, cfg.MaxRateWaitSeconds = model, 0, 0, 60
	if mutate != nil {
		mutate(&cfg)
	}
	opts := []Option{WithSleeper((&fakeSleep{}).sleep), withRegistry(&registry{})}
	if key == "" {
		opts = append(opts, WithoutKey())
	}
	c, err := New(cfg, config.NewSecret(key), nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestChainMovesOn: each failure the spec names moves the call to the next
// model, which serves it; the run's use records the fallback and the
// reason of the first move.
func TestChainMovesOn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(int) int
		mutate func(*config.LLM)
		closed bool
		reason string
	}{
		{"server error", always(503), nil, false, ReasonServerError},
		{"rate limited, retries used up", always(429), nil, false, ReasonRateLimited},
		{"rate limited, wait too long", func(int) int { return 429 }, func(c *config.LLM) { c.MaxRateRetries = 2; c.MaxRateWaitSeconds = 0 }, false, ReasonRateLimited},
		{"timeout", always(200), func(c *config.LLM) { c.TimeoutSeconds = 1 }, false, ReasonTimedOut},
		{"unreachable", always(200), nil, true, ReasonUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := newChainFake(t, tc.answer)
			fallback := newChainFake(t, always(200))
			p := chainClient(t, primary.srv.URL, "primary-model", testKey, tc.mutate)
			if tc.name == "timeout" {
				p.timeout = 20 * time.Millisecond
				primary.handler = func(_ int, _ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
			}
			if tc.closed {
				primary.srv.Close()
			}
			ch := NewChain(p, []*Client{chainClient(t, fallback.srv.URL, "second-model", "", nil)}, 0.3)
			resp, err := ch.Complete(t.Context(), "s", "u")
			if err != nil || resp.Content != "served by second-model" {
				t.Fatalf("resp %v err %v", resp, err)
			}
			u := ch.Use()
			if u.Model != "second-model" || u.FallbackCalls != 1 || u.FallbackReason != tc.reason ||
				len(u.ModelsUsed) != 1 || u.ModelsUsed[0] != "second-model" {
				t.Errorf("use %+v", u)
			}
		})
	}
}

// TestChainDoesNotMoveOn [canary target]: an auth error, a refused request
// and a canceled context end the call at the model that had them; the
// fallback is never asked.
func TestChainDoesNotMoveOn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		cancel bool
		want   error
	}{
		{"auth 401", 401, false, ErrAuth},
		{"auth 403", 403, false, ErrAuth},
		{"bad request", 400, false, ErrBadRequest},
		{"canceled", 503, true, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := newChainFake(t, always(tc.status))
			fallback := newChainFake(t, always(200))
			ch := NewChain(chainClient(t, primary.srv.URL, "primary-model", testKey, nil),
				[]*Client{chainClient(t, fallback.srv.URL, "second-model", "", nil)}, 0.3)
			ctx := t.Context()
			if tc.cancel {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			_, err := ch.Complete(ctx, "s", "u")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if fallback.calls() != 0 {
				t.Errorf("the fallback was asked %d times", fallback.calls())
			}
			if strings.Contains(err.Error(), "fallback") {
				t.Errorf("err %q names fallbacks that were not tried", err)
			}
		})
	}
}

// TestChainSticky [canary target]: once a call fell back, the rest of the
// run starts at the fallback: the primary sees no second request. A new run
// (a new chain) starts at the primary again.
func TestChainSticky(t *testing.T) {
	primary := newChainFake(t, always(503))
	fallback := newChainFake(t, always(200))
	mk := func() *Chain {
		return NewChain(chainClient(t, primary.srv.URL, "primary-model", testKey, nil),
			[]*Client{chainClient(t, fallback.srv.URL, "second-model", "", nil)}, 0.3)
	}
	run := mk()
	for range 3 {
		if _, err := run.Complete(t.Context(), "s", "u"); err != nil {
			t.Fatal(err)
		}
	}
	if primary.calls() != 1 || fallback.calls() != 3 {
		t.Errorf("primary %d requests, fallback %d; want 1 and 3", primary.calls(), fallback.calls())
	}
	if u := run.Use(); u.FallbackCalls != 3 {
		t.Errorf("fallback calls %d", u.FallbackCalls)
	}
	if _, err := mk().Complete(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	if primary.calls() != 2 {
		t.Errorf("a new run did not start at the primary: %d primary requests", primary.calls())
	}
}

// TestChainSizeSkip [canary target]: a fallback whose context window cannot
// hold the prompt plus its maximum output is skipped for that call and
// counted; the next one serves it.
func TestChainSizeSkip(t *testing.T) {
	primary := newChainFake(t, always(503))
	small := newChainFake(t, always(200))
	large := newChainFake(t, always(200))
	out := 1000
	ch := NewChain(chainClient(t, primary.srv.URL, "primary-model", testKey, nil), []*Client{
		chainClient(t, small.srv.URL, "small-model", "", func(c *config.LLM) { c.ContextWindow, c.MaxOutputTokens = 4096, &out }),
		chainClient(t, large.srv.URL, "large-model", "", func(c *config.LLM) { c.ContextWindow = 128000 }),
	}, 0.3)
	prompt := strings.Repeat("token ", 4000)
	for range 2 {
		resp, err := ch.Complete(t.Context(), "s", prompt)
		if err != nil || resp.Content != "served by large-model" {
			t.Fatalf("resp %v err %v", resp, err)
		}
	}
	if small.calls() != 0 {
		t.Errorf("the small fallback got %d requests", small.calls())
	}
	u := ch.Use()
	if len(u.SizeSkips) != 1 || u.SizeSkips[0] != (SizeSkip{Model: "small-model", Calls: 1}) {
		t.Errorf("size skips %+v (the second call starts at the large model)", u.SizeSkips)
	}
	// A short prompt fits the small one.
	ch2 := NewChain(chainClient(t, primary.srv.URL, "primary-model", testKey, nil), []*Client{
		chainClient(t, small.srv.URL, "small-model", "", func(c *config.LLM) { c.ContextWindow, c.MaxOutputTokens = 4096, &out }),
	}, 0.3)
	if resp, err := ch2.Complete(t.Context(), "s", "short"); err != nil || resp.Content != "served by small-model" {
		t.Errorf("short prompt: resp %v err %v", resp, err)
	}
}

// TestChainAllFail: when every model fails, the error of the primary is
// returned, with its class and the note about the fallbacks.
func TestChainAllFail(t *testing.T) {
	primary := newChainFake(t, always(503))
	f1 := newChainFake(t, always(502))
	f2 := newChainFake(t, always(401))
	ch := NewChain(chainClient(t, primary.srv.URL, "primary-model", testKey, nil), []*Client{
		chainClient(t, f1.srv.URL, "second-model", "", nil),
		chainClient(t, f2.srv.URL, "third-model", "", nil),
	}, 0.3)
	_, err := ch.Complete(t.Context(), "s", "u")
	want := "the LLM endpoint reported an internal error (HTTP 503). All 2 fallback models failed as well."
	if err == nil || err.Error() != want || !errors.Is(err, ErrUpstream) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	// One fallback, refused for its missing key (acceptance Q4).
	ch = NewChain(chainClient(t, primary.srv.URL, "primary-model", testKey, nil),
		[]*Client{chainClient(t, f2.srv.URL, "third-model", "", nil)}, 0.3)
	_, err = ch.Complete(t.Context(), "s", "u")
	if want := "the LLM endpoint reported an internal error (HTTP 503). All 1 fallback models failed as well."; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

// TestChainCompleteNext: an answer that stays unusable after the re-ask
// sends the original prompt once to the next model, which the run then
// stays on; with no next model, nothing is sent.
func TestChainCompleteNext(t *testing.T) {
	primary := newChainFake(t, always(200))
	fallback := newChainFake(t, always(200))
	ch := NewChain(chainClient(t, primary.srv.URL, "primary-model", testKey, nil),
		[]*Client{chainClient(t, fallback.srv.URL, "second-model", "", nil)}, 0.3)
	if _, err := ch.Complete(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	resp, ok, err := ch.CompleteNext(t.Context(), "s", "u")
	if err != nil || !ok || resp.Content != "served by second-model" {
		t.Fatalf("CompleteNext: %v %v %v", resp, ok, err)
	}
	if u := ch.Use(); u.FallbackReason != ReasonUnusable || u.FallbackCalls != 1 || len(u.ModelsUsed) != 2 {
		t.Errorf("use %+v", u)
	}
	if _, ok, err := ch.CompleteNext(t.Context(), "s", "u"); ok || err != nil {
		t.Errorf("past the last model: ok %v err %v", ok, err)
	}
	if fallback.calls() != 1 {
		t.Errorf("fallback requests %d", fallback.calls())
	}
	// Without fallbacks there is no next model.
	single := NewChain(chainClient(t, primary.srv.URL, "primary-model", testKey, nil), nil, 0.3)
	if _, ok, _ := single.CompleteNext(t.Context(), "s", "u"); ok {
		t.Error("a chain without fallbacks served a next call")
	}
}

// TestWithoutKeySendsNoAuthorization: a client built without a key sends no
// Authorization header, also not an empty one.
func TestWithoutKeySendsNoAuthorization(t *testing.T) {
	f := newChainFake(t, always(200))
	c := chainClient(t, f.srv.URL, "m", "", nil)
	if _, err := c.Complete(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auths[0] != "" {
		t.Errorf("Authorization %q", f.auths[0])
	}
	if _, err := New(baseCfg(f.srv.URL), config.NewSecret(""), nil); err == nil {
		t.Error("a client without a key was built without WithoutKey")
	}
}
