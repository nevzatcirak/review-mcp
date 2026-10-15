package llm

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// fakeClock is a clock whose sleeps advance it at once and are recorded.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *fakeClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.sleeps)
}

var discard = slog.New(slog.DiscardHandler)

func newLimiter(l limits) *limiter {
	return &limiter{endpoint: "https://llm.example/v1", limits: l}
}

// TestLimiterWindow: with requests_per_minute N, N requests start at once
// and request N+1 waits until the first one leaves the 60-second window;
// a later start waits only as long as the window needs.
func TestLimiterWindow(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(limits{rpm: 3, maxWait: time.Minute})
	for i := range 3 {
		if st, err := l.acquire(t.Context(), clk.now, clk.sleep, discard); err != nil || st.waits != 0 {
			t.Fatalf("request %d: %+v %v", i+1, st, err)
		}
		l.release()
		clk.advance(10 * time.Second)
	}
	// Starts at 0 s, 10 s and 20 s; now 30 s: the first leaves at 60 s.
	st, err := l.acquire(t.Context(), clk.now, clk.sleep, discard)
	if err != nil {
		t.Fatal(err)
	}
	l.release()
	if got := clk.recorded(); !slices.Equal(got, []time.Duration{30 * time.Second}) || st.waits != 1 || st.waited != 30*time.Second {
		t.Errorf("sleeps %v, stat %+v; want one wait of 30s", got, st)
	}
}

// TestLimiterWindowCountsRetries [canary target]: every attempt takes a
// place in the window, retries included. A call whose first attempt fails
// and is retried uses two places, so with requests_per_minute 2 the next
// call waits for the window.
func TestLimiterWindowCountsRetries(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			writeJSON(w, 500, `{}`)
			return
		}
		writeJSON(w, 200, okJSON("ok"))
	})
	cfg := baseCfg(f.srv.URL)
	cfg.RequestsPerMinute, cfg.MaxRateWaitSeconds = 2, 60
	clk := newFakeClock()
	c, err := New(cfg, config.NewSecret(testKey), nil, WithSleeper(clk.sleep), WithClock(clk.now), withRegistry(&registry{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	// The retry's backoff (1 s) is the only sleep so far.
	if got := clk.recorded(); !slices.Equal(got, []time.Duration{time.Second}) {
		t.Fatalf("sleeps after the first call %v", got)
	}
	if _, err := c.Complete(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	// The first attempt started at 0 s; the second call waits until 60 s.
	if got := clk.recorded(); !slices.Equal(got, []time.Duration{time.Second, 59 * time.Second}) {
		t.Errorf("sleeps %v, want the backoff and a window wait of 59s", got)
	}
	if f.calls() != 3 {
		t.Errorf("%d requests, want 3", f.calls())
	}
}

// TestLimiterWaitTooLong [canary target]: a wait longer than
// max_rate_wait_seconds is not waited for: the request fails at once, the
// sleeper records no sleep, and the place in the queue is given up.
func TestLimiterWaitTooLong(t *testing.T) {
	clk := newFakeClock()
	l := newLimiter(limits{rpm: 1, maxWait: 10 * time.Second})
	if _, err := l.acquire(t.Context(), clk.now, clk.sleep, discard); err != nil {
		t.Fatal(err)
	}
	l.release()
	_, err := l.acquire(t.Context(), clk.now, clk.sleep, discard)
	var long *errWaitTooLong
	if !errors.As(err, &long) || long.need != time.Minute {
		t.Fatalf("err = %v, want a wait of 1m refused", err)
	}
	if got := clk.recorded(); len(got) != 0 {
		t.Errorf("slept %v", got)
	}
	if len(l.queue) != 0 {
		t.Errorf("the refused request kept its place: %d waiting", len(l.queue))
	}

	// Through the client: rate limited at once, no request, no retry.
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("ok")) })
	cfg := baseCfg(f.srv.URL)
	cfg.RequestsPerMinute, cfg.MaxRateWaitSeconds, cfg.MaxRetries = 1, 10, 3
	clk = newFakeClock()
	c, err := New(cfg, config.NewSecret(testKey), nil, WithSleeper(clk.sleep), WithClock(clk.now), withRegistry(&registry{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(t.Context(), "s", "u")
	var le *Error
	if !errors.As(err, &le) || le.Class != ClassRateLimited || le.reason != reasonWaitTooLong {
		t.Fatalf("err = %v, want rate limited, wait too long", err)
	}
	if got := clk.recorded(); len(got) != 0 || f.calls() != 1 {
		t.Errorf("sleeps %v, %d requests; want none and 1", got, f.calls())
	}
}

// waitQueued waits until n requests are queued on l.
func waitQueued(t *testing.T, l *limiter, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		got := len(l.queue)
		l.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d queued, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestLimiterConcurrencyFIFO: with max_concurrent_requests 1 a second
// request waits until the first is released, and three waiters start in
// the order they arrived.
func TestLimiterConcurrencyFIFO(t *testing.T) {
	l := newLimiter(limits{concurrency: 1, maxWait: time.Minute})
	if _, err := l.acquire(t.Context(), time.Now, sleepCtx, discard); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 3)
	for i, name := range []string{"B", "C", "D"} {
		go func() {
			if _, err := l.acquire(context.Background(), time.Now, sleepCtx, discard); err != nil {
				started <- "error " + err.Error()
				return
			}
			started <- name
		}()
		waitQueued(t, l, i+1)
	}
	select {
	case s := <-started:
		t.Fatalf("%s started while the first request is in flight", s)
	case <-time.After(20 * time.Millisecond):
	}
	var order []string
	for range 3 {
		l.release()
		select {
		case s := <-started:
			order = append(order, s)
		case <-time.After(5 * time.Second):
			t.Fatalf("no request started; order so far %v", order)
		}
	}
	l.release()
	if !slices.Equal(order, []string{"B", "C", "D"}) {
		t.Errorf("order %v, want B C D", order)
	}
}

// TestLimiterCancelGivesUpThePlace: a waiter whose ctx is canceled stops
// waiting at once and the next waiter takes its place.
func TestLimiterCancelGivesUpThePlace(t *testing.T) {
	l := newLimiter(limits{concurrency: 1, maxWait: time.Minute})
	if _, err := l.acquire(t.Context(), time.Now, sleepCtx, discard); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errB := make(chan error, 1)
	go func() {
		_, err := l.acquire(ctx, time.Now, sleepCtx, discard)
		errB <- err
	}()
	waitQueued(t, l, 1)
	startedC := make(chan error, 1)
	go func() {
		_, err := l.acquire(context.Background(), time.Now, sleepCtx, discard)
		startedC <- err
	}()
	waitQueued(t, l, 2)
	cancel()
	if err := <-errB; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	waitQueued(t, l, 1)
	l.release()
	select {
	case err := <-startedC:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the next waiter did not start")
	}
	l.release()
	if l.inFlight != 0 || len(l.queue) != 0 {
		t.Errorf("in flight %d, queued %d", l.inFlight, len(l.queue))
	}
}

// TestRegistry [canary target]: two clients with the same base URL and key
// share a limiter (also with another model, a trailing slash or upper-case
// host); another key or base URL gets another limiter. Neither the key nor
// its HMAC appears in the registry's printed form or in the logs.
func TestRegistry(t *testing.T) {
	r := &registry{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	mk := func(base, key, model string) *Client {
		t.Helper()
		cfg := config.LLM{BaseURL: base, Model: model, TimeoutSeconds: 30, RequestsPerMinute: 1, MaxRateWaitSeconds: 0}
		c, err := New(cfg, config.NewSecret(key), logger, withRegistry(r))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	const otherKey = "sk-other-0123456789"
	a := mk("https://llm.example/v1", testKey, "model-a")
	if b := mk("https://LLM.example:443/v1/", testKey, "model-b"); b.limiter != a.limiter {
		t.Error("the same base URL and key got two limiters")
	}
	if c := mk("https://llm.example/v1", otherKey, "model-a"); c.limiter == a.limiter {
		t.Error("another key shares the limiter")
	}
	if c := mk("https://fallback.example/v1", testKey, "model-a"); c.limiter == a.limiter {
		t.Error("another base URL shares the limiter")
	}

	// A refused wait logs the endpoint, never the key or its HMAC.
	if _, err := a.limiter.acquire(t.Context(), time.Now, sleepCtx, logger); err != nil {
		t.Fatal(err)
	}
	a.limiter.release()
	if _, err := a.limiter.acquire(t.Context(), time.Now, sleepCtx, logger); err == nil {
		t.Fatal("the second request in the minute was not refused")
	}
	u, _ := url.Parse("https://llm.example/v1")
	printed := fmt.Sprintf("%v %+v %#v %s %v %#v", r, r, r, r.String(), a.limiter, a.limiter) + logs.String()
	for _, key := range []string{testKey, otherKey} {
		m := hmac.New(sha256.New, r.salt)
		m.Write([]byte(key))
		mac := hex.EncodeToString(m.Sum(nil))
		if strings.Contains(printed, key) || strings.Contains(printed, mac) || strings.Contains(printed, mac[:16]) {
			t.Errorf("the key or its HMAC is printed:\n%s", printed)
		}
	}
	if !strings.Contains(logs.String(), "endpoint=https://llm.example/v1") || !strings.Contains(logs.String(), "reason=window") {
		t.Errorf("log lacks the endpoint or reason:\n%s", logs.String())
	}
	if normalBase(u) != "https://llm.example/v1" {
		t.Errorf("normalBase = %q", normalBase(u))
	}
}

// TestRegistryUpdatesLimits: a later run with other values updates the
// shared limiter's limits in place.
func TestRegistryUpdatesLimits(t *testing.T) {
	r := &registry{}
	u, _ := url.Parse("https://llm.example/v1")
	key := config.NewSecret(testKey)
	l := r.lookup(u, key, config.LLM{RequestsPerMinute: 3, MaxRateWaitSeconds: 60})
	if again := r.lookup(u, key, config.LLM{RequestsPerMinute: 5, MaxConcurrentRequests: 2, MaxRateWaitSeconds: 30}); again != l {
		t.Fatal("another limiter")
	}
	if want := (limits{rpm: 5, concurrency: 2, maxWait: 30 * time.Second}); l.limits != want {
		t.Errorf("limits %+v, want %+v", l.limits, want)
	}
}

// rateFake answers with the given status and headers per request number
// (1-based) and records the fake clock's time of each request.
type rateFake struct {
	*fake
	mu    sync.Mutex
	times []time.Time
}

func newRateFake(t *testing.T, clk *fakeClock, answer func(n int, h http.Header) int) *rateFake {
	rf := &rateFake{}
	rf.fake = newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		rf.mu.Lock()
		rf.times = append(rf.times, clk.now())
		rf.mu.Unlock()
		st := answer(n, w.Header())
		if st == 200 {
			writeJSON(w, 200, okJSON("ok"))
			return
		}
		writeJSON(w, st, `{}`)
	})
	return rf
}

// rateClient is a client of rf's server with the given rate retries and
// longest wait, on clk, with its own registry r (nil: a new one).
func rateClient(t *testing.T, rf *rateFake, clk *fakeClock, r *registry, rateRetries, maxWait int) *Client {
	t.Helper()
	if r == nil {
		r = &registry{}
	}
	cfg := baseCfg(rf.srv.URL)
	cfg.MaxRateRetries, cfg.MaxRateWaitSeconds = rateRetries, maxWait
	c, err := New(cfg, config.NewSecret(testKey), nil, WithSleeper(clk.sleep), WithClock(clk.now), withRegistry(r))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestRateLimitCoolDown: a 429's delay comes from Retry-After (seconds or an
// HTTP date), else from x-ratelimit-reset-requests, else 10 s; the call is
// retried after it, without the 30-second cap of other classes.
func TestRateLimitCoolDown(t *testing.T) {
	base := newFakeClock().now()
	for _, tc := range []struct {
		name   string
		header map[string]string
		want   time.Duration
	}{
		{"retry-after seconds", map[string]string{"Retry-After": "45"}, 45 * time.Second},
		{"retry-after date", map[string]string{"Retry-After": base.Add(50 * time.Second).Format(http.TimeFormat)}, 50 * time.Second},
		{"retry-after wins", map[string]string{"Retry-After": "7", "X-Ratelimit-Reset-Requests": "20s"}, 7 * time.Second},
		{"reset seconds", map[string]string{"X-Ratelimit-Reset-Requests": "6s"}, 6 * time.Second},
		{"reset minutes", map[string]string{"X-Ratelimit-Reset-Requests": "1m30s"}, 90 * time.Second},
		{"reset milliseconds", map[string]string{"X-Ratelimit-Reset-Requests": "250ms"}, 250 * time.Millisecond},
		{"unparsable reset", map[string]string{"X-Ratelimit-Reset-Requests": "soon"}, 10 * time.Second},
		{"no header", nil, 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := newFakeClock()
			rf := newRateFake(t, clk, func(n int, h http.Header) int {
				if n == 1 {
					for k, v := range tc.header {
						h.Set(k, v)
					}
					return 429
				}
				return 200
			})
			c := rateClient(t, rf, clk, nil, 4, 600)
			if _, err := c.Complete(t.Context(), "s", "u"); err != nil {
				t.Fatal(err)
			}
			if got := clk.recorded(); !slices.Equal(got, []time.Duration{tc.want}) {
				t.Errorf("sleeps %v, want %v", got, tc.want)
			}
			if rf.calls() != 2 {
				t.Errorf("%d requests, want 2", rf.calls())
			}
		})
	}
}

// TestRateLimitDoubling: without headers the cool-down doubles with every
// consecutive 429 of the endpoint and starts again after a success.
func TestRateLimitDoubling(t *testing.T) {
	clk := newFakeClock()
	rf := newRateFake(t, clk, func(n int, _ http.Header) int {
		if n <= 3 || n == 5 {
			return 429
		}
		return 200
	})
	c := rateClient(t, rf, clk, nil, 4, 600)
	for range 2 {
		if _, err := c.Complete(t.Context(), "s", "u"); err != nil {
			t.Fatal(err)
		}
	}
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 10 * time.Second}
	if got := clk.recorded(); !slices.Equal(got, want) {
		t.Errorf("sleeps %v, want %v", got, want)
	}
}

// TestRateLimitSecondCaller [canary target]: a 429 cools the endpoint down
// for every caller: a second caller of the same endpoint waits for the
// cool-down before it sends anything, and both calls succeed after it.
func TestRateLimitSecondCaller(t *testing.T) {
	clk := newFakeClock()
	start := clk.now()
	rf := newRateFake(t, clk, func(n int, h http.Header) int {
		if n == 1 {
			h.Set("Retry-After", "30")
			return 429
		}
		return 200
	})
	r := &registry{}
	first := rateClient(t, rf, clk, r, 4, 60)
	second := rateClient(t, rf, clk, r, 4, 60)
	// The first caller's request gets the 429; its retry is still to come.
	first.cfg.MaxRateRetries = 0
	if _, err := first.Complete(t.Context(), "s", "u"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("first caller: %v", err)
	}
	if _, err := second.Complete(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	rf.mu.Lock()
	times := slices.Clone(rf.times)
	rf.mu.Unlock()
	if len(times) != 2 || times[1].Sub(start) < 30*time.Second {
		t.Errorf("request times %v after %v: the second caller did not wait for the cool-down", times, start)
	}
	if got := clk.recorded(); !slices.Equal(got, []time.Duration{30 * time.Second}) {
		t.Errorf("sleeps %v, want the second caller's 30s cool-down wait", got)
	}
}

// TestRateLimitRetries: a 429 is retried up to max_rate_retries times,
// apart from max_retries; then the rate-limited error is returned.
func TestRateLimitRetries(t *testing.T) {
	clk := newFakeClock()
	rf := newRateFake(t, clk, func(_ int, h http.Header) int {
		h.Set("Retry-After", "1")
		return 429
	})
	c := rateClient(t, rf, clk, nil, 2, 60)
	c.cfg.MaxRetries = 0
	_, err := c.Complete(t.Context(), "s", "u")
	var le *Error
	if !errors.As(err, &le) || le.Class != ClassRateLimited || le.Status != 429 {
		t.Fatalf("err = %v", err)
	}
	if rf.calls() != 3 {
		t.Errorf("%d requests, want 3 (two rate retries)", rf.calls())
	}
	if got := clk.recorded(); !slices.Equal(got, []time.Duration{time.Second, time.Second}) {
		t.Errorf("sleeps %v", got)
	}
}

// TestRateLimitWaitTooLong: a 429 whose delay exceeds max_rate_wait_seconds
// fails at once: no sleep, no retry; a delay above 600 s is capped there.
func TestRateLimitWaitTooLong(t *testing.T) {
	for _, tc := range []struct {
		header string
		need   time.Duration
	}{{"120", 120 * time.Second}, {"3600", 600 * time.Second}} {
		clk := newFakeClock()
		rf := newRateFake(t, clk, func(_ int, h http.Header) int {
			h.Set("Retry-After", tc.header)
			return 429
		})
		c := rateClient(t, rf, clk, nil, 4, 60)
		_, err := c.Complete(t.Context(), "s", "u")
		var le *Error
		if !errors.As(err, &le) || le.Class != ClassRateLimited || le.reason != reasonWaitTooLong || le.wait != tc.need {
			t.Fatalf("Retry-After %s: err = %v (%+v)", tc.header, err, le)
		}
		if got := clk.recorded(); len(got) != 0 || rf.calls() != 1 {
			t.Errorf("Retry-After %s: sleeps %v, %d requests", tc.header, got, rf.calls())
		}
	}
}

// TestRateLimitHeadersOnSuccess: a successful answer with
// x-ratelimit-remaining-requests 0 and a reset cools the endpoint down; the
// remaining count alone does not.
func TestRateLimitHeadersOnSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header map[string]string
		want   []time.Duration
	}{
		{"exhausted", map[string]string{"X-Ratelimit-Remaining-Requests": "0", "X-Ratelimit-Reset-Requests": "12s"}, []time.Duration{12 * time.Second}},
		{"requests left", map[string]string{"X-Ratelimit-Remaining-Requests": "3", "X-Ratelimit-Reset-Requests": "12s"}, nil},
		{"no reset", map[string]string{"X-Ratelimit-Remaining-Requests": "0"}, nil},
		{"other family", map[string]string{"X-Ratelimit-Remaining-Tokens": "0", "X-Ratelimit-Reset-Tokens": "12s"}, nil},
	} {
		clk := newFakeClock()
		rf := newRateFake(t, clk, func(n int, h http.Header) int {
			if n == 1 {
				for k, v := range tc.header {
					h.Set(k, v)
				}
			}
			return 200
		})
		c := rateClient(t, rf, clk, nil, 4, 60)
		for range 2 {
			if _, err := c.Complete(t.Context(), "s", "u"); err != nil {
				t.Fatal(err)
			}
		}
		if got := clk.recorded(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: sleeps %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRateLimitWaitsReported: the client counts its requests that waited and
// their total wait; a refused wait says which limit it exceeded, with no
// header value and no URL.
func TestRateLimitWaitsReported(t *testing.T) {
	clk := newFakeClock()
	rf := newRateFake(t, clk, func(n int, h http.Header) int {
		switch n {
		case 1:
			h.Set("Retry-After", "5")
			return 429
		case 3:
			h.Set("Retry-After", "90")
			return 429
		}
		return 200
	})
	c := rateClient(t, rf, clk, nil, 4, 60)
	if _, err := c.Complete(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	if n, d := c.RateLimitWaits(); n != 1 || d != 5*time.Second {
		t.Errorf("after one wait: %d waits, %v", n, d)
	}
	_, err := c.Complete(t.Context(), "s", "u")
	want := "the LLM endpoint rate-limited the request. The endpoint asked for a wait longer than llm.max_rate_wait_seconds (60 s)."
	if err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
	if n, d := c.RateLimitWaits(); n != 1 || d != 5*time.Second {
		t.Errorf("a refused wait was counted: %d waits, %v", n, d)
	}
	if strings.Contains(err.Error(), rf.srv.URL) || strings.Contains(err.Error(), "90") {
		t.Errorf("the sentence carries a URL or header value: %q", err)
	}
}

// TestWaitTooLongSentence [canary target]: the sentence of a refused wait
// names what needed it: the endpoint's cool-down, or the client's own
// llm.requests_per_minute window. Both name the configured longest wait.
func TestWaitTooLongSentence(t *testing.T) {
	// The endpoint: a 429 asking for 90 s.
	clk := newFakeClock()
	rf := newRateFake(t, clk, func(_ int, h http.Header) int {
		h.Set("Retry-After", "90")
		return 429
	})
	c := rateClient(t, rf, clk, nil, 4, 60)
	_, err := c.Complete(t.Context(), "s", "u")
	if want := "the LLM endpoint rate-limited the request. The endpoint asked for a wait longer than " +
		"llm.max_rate_wait_seconds (60 s)."; err == nil || err.Error() != want {
		t.Errorf("cool-down: err = %v, want %q", err, want)
	}

	// The window: requests_per_minute 1, the second request would wait 60 s.
	clk = newFakeClock()
	rf = newRateFake(t, clk, func(int, http.Header) int { return 200 })
	cfg := baseCfg(rf.srv.URL)
	cfg.RequestsPerMinute, cfg.MaxRateWaitSeconds = 1, 10
	c, err = New(cfg, config.NewSecret(testKey), nil, WithSleeper(clk.sleep), WithClock(clk.now), withRegistry(&registry{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(t.Context(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(t.Context(), "s", "u")
	if want := "the LLM endpoint rate-limited the request. Staying within llm.requests_per_minute would need a wait " +
		"longer than llm.max_rate_wait_seconds (10 s)."; err == nil || err.Error() != want {
		t.Errorf("window: err = %v, want %q", err, want)
	}
}
