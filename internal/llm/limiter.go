package llm

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/logging"
)

// Client-side rate limiting (X-37, v2.1 spec V-6 §2.2). One process-wide
// registry holds one limiter per endpoint; every Client of the process (one
// per tool call) looks its endpoint's limiter up, so parallel tool calls
// share it.

// rateWindow is the sliding window of llm.requests_per_minute.
const rateWindow = time.Minute

// Wait reasons, as the debug log names them (§2.3 item 4).
const (
	waitWindow      = "window"
	waitConcurrency = "concurrency"
	waitCoolDown    = "cool_down"
)

// Cool-down after HTTP 429 (§2.2 item 5).
const (
	// coolDownFirst is the cool-down of a 429 without a usable header; it
	// doubles with every consecutive 429 of the endpoint.
	coolDownFirst = 10 * time.Second
	// maxCoolDown caps every cool-down. It is above the largest
	// llm.max_rate_wait_seconds, so a long reset is reported, not waited
	// for.
	maxCoolDown = 600 * time.Second
)

// Rate-limit headers read by the limiter. Other header families are
// ignored (§2.2 item 6).
const (
	headerRetryAfter        = "Retry-After"
	headerResetRequests     = "X-Ratelimit-Reset-Requests"
	headerRemainingRequests = "X-Ratelimit-Remaining-Requests"
)

// reasonWaitTooLong is the internal reason of a rate-limited failure whose
// needed cool-down wait exceeds llm.max_rate_wait_seconds (§7.1). It is not
// shown to clients as such.
const reasonWaitTooLong = "wait_too_long"

// limits are the configured limits of one endpoint.
type limits struct {
	// rpm is llm.requests_per_minute; 0 is no limit.
	rpm int
	// concurrency is llm.max_concurrent_requests; 0 is no limit.
	concurrency int
	// maxWait is llm.max_rate_wait_seconds: the longest wait for the
	// endpoint's own cool-down (§7.1); the client's own window and
	// concurrency waits are not compared with it.
	maxWait time.Duration
}

func limitsOf(cfg config.LLM) limits {
	return limits{
		rpm:         max(cfg.RequestsPerMinute, 0),
		concurrency: max(cfg.MaxConcurrentRequests, 0),
		maxWait:     time.Duration(max(cfg.MaxRateWaitSeconds, 0)) * time.Second,
	}
}

// limiter keeps the requests of one endpoint within its limits. Waiters are
// served in arrival order: only the first of the queue may start, and every
// change of the limiter's state wakes all waiters so that each computes its
// wait again.
type limiter struct {
	// endpoint is the redacted base URL, for logs only.
	endpoint string

	mu     sync.Mutex
	limits limits
	// starts are the start times of the requests of the last rateWindow,
	// oldest first.
	starts []time.Time
	// inFlight is the number of requests started and not yet released.
	inFlight int
	queue    []*waiter
	// coolUntil is the end of the endpoint's cool-down: no request starts
	// before it. consecutive429 counts the 429s since the last success.
	coolUntil      time.Time
	consecutive429 int
	// tools is what the process knows of each model's tool calling on
	// the endpoint (toolsupport.go).
	tools map[string]ToolSupport
}

// waiter is one request waiting for its turn. wake, set while it waits,
// ends its wait early so that it computes its wait again.
type waiter struct {
	wake context.CancelFunc
}

// String names the endpoint only; the registry key is never printed.
func (l *limiter) String() string { return "llm limiter " + l.endpoint }

// GoString is String, so that %#v prints no field either.
func (l *limiter) GoString() string { return l.String() }

// setLimits applies the configuration of a run (§2.2 item 2): the first run
// creates the limiter, a later run with other values updates the limits in
// place. A change wakes the waiters.
func (l *limiter) setLimits(lim limits) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.limits != lim {
		l.limits = lim
		l.broadcast()
	}
}

// waitStat is what acquire reports of its waits.
type waitStat struct {
	// waited is the total time acquire waited; waits the number of waits.
	waited time.Duration
	waits  int
}

// errWaitTooLong is acquire's failure when the endpoint's cool-down is
// longer than the limit; need is the time until the cool-down ends.
type errWaitTooLong struct {
	need time.Duration
}

func (e *errWaitTooLong) Error() string { return "llm: rate-limit wait too long" }

// acquire waits until a request may start on the endpoint (§2.2 items 2 to
// 4, and v2.1 spec V-6 §7.1) and records its start; the caller must call
// release once the request is done, and only when acquire returned no
// error. Every wait is ctx-aware: a canceled ctx stops waiting at once and
// gives up the place in the queue. Before each wait the endpoint's
// cool-down (the time until it ends, nothing else) is compared with the
// configured maximum; when it is longer, acquire returns *errWaitTooLong
// without waiting.
//
// DESIGN-QUESTION (WP-8b DQ-1): a wait for llm.max_concurrent_requests has
// no computable length (it lasts until a request in flight ends), so it is
// not checked against llm.max_rate_wait_seconds; it is bounded by the
// request timeout of the requests in flight and by ctx. Recommendation:
// keep.
//
// DESIGN-QUESTION (WP-8f DQ-1): a wait for the window
// (llm.requests_per_minute) is predictable and our own, so it is bounded by
// ctx only, as a concurrency wait is; a request that waits for both is
// compared on its cool-down part alone, which is not shortened by the
// queue ahead of it. A request in the queue therefore never fails because
// of how many requests are ahead of it. Recommendation: keep.
func (l *limiter) acquire(ctx context.Context, now func() time.Time, sleep func(context.Context, time.Duration) error,
	log *slog.Logger) (waitStat, error) {
	var st waitStat
	l.mu.Lock()
	w := &waiter{}
	l.queue = append(l.queue, w)
	for {
		t := now()
		l.prune(t)
		k := slices.Index(l.queue, w)
		d, reason := l.waitFor(k, t)
		if cool := l.coolDownLeft(t); cool > l.limits.maxWait {
			l.leave(w)
			l.mu.Unlock()
			log.Debug("llm rate limit wait too long", "endpoint", l.endpoint, "reason", waitCoolDown, "wait_ms", cool.Milliseconds())
			return st, &errWaitTooLong{need: cool}
		}
		if d == 0 && k == 0 && (l.limits.concurrency == 0 || l.inFlight < l.limits.concurrency) {
			l.starts = append(l.starts, t)
			l.inFlight++
			l.leave(w)
			l.mu.Unlock()
			return st, nil
		}
		if d == 0 {
			reason = waitConcurrency
		}
		wctx, cancel := context.WithCancel(ctx)
		w.wake = cancel
		l.mu.Unlock()

		start := now()
		if d > 0 {
			_ = sleep(wctx, d)
		} else {
			<-wctx.Done()
		}
		cancel()
		waited := now().Sub(start)
		st.waited += waited
		st.waits++
		log.Debug("llm rate limit wait", "endpoint", l.endpoint, "reason", reason, "wait_ms", waited.Milliseconds())

		l.mu.Lock()
		w.wake = nil
		if err := ctx.Err(); err != nil {
			l.leave(w)
			l.mu.Unlock()
			return st, err
		}
	}
}

// release ends a request started by acquire.
func (l *limiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight--
	l.broadcast()
}

// coolDownLeft is the time from t until the endpoint's cool-down ends, 0
// when it is not cooling down. Call with mu held.
func (l *limiter) coolDownLeft(t time.Time) time.Duration {
	return max(l.coolUntil.Sub(t), 0)
}

// waitFor is how long the waiter at queue position k must wait from t for
// the cool-down and the window, and why; 0 when it may start now. Every
// waiter ahead of it is assumed to start as early as the cool-down and the
// window let it, so a waiter's wait accounts for the queue ahead (FIFO).
// Call with mu held, after prune.
func (l *limiter) waitFor(k int, t time.Time) (time.Duration, string) {
	reason := ""
	at := t
	if l.coolUntil.After(t) {
		at, reason = l.coolUntil, waitCoolDown
	}
	if l.limits.rpm > 0 {
		earliest := at
		planned := make([]time.Time, 0, len(l.starts)+k+1)
		planned = append(planned, l.starts...)
		for range k + 1 {
			at = earliest
			if len(planned) >= l.limits.rpm {
				if free := planned[len(planned)-l.limits.rpm].Add(rateWindow); free.After(at) {
					at, reason = free, waitWindow
				}
			}
			planned = append(planned, at)
		}
	}
	if d := at.Sub(t); d > 0 {
		return d, reason
	}
	return 0, ""
}

// rateLimited records an HTTP 429 of the endpoint at t (§2.2 item 5): the
// endpoint cools down for D, from Retry-After (seconds or an HTTP date),
// else from x-ratelimit-reset-requests, else 10 s doubled for every
// consecutive 429 since the last success; D is capped at maxCoolDown.
// Every request of the endpoint waits for the cool-down. It returns D.
//
// DESIGN-QUESTION (WP-8b DQ-2): two requests in flight may both get a 429,
// the second with a shorter delay. The cool-down is never shortened: it
// ends at the later of the current end and t + D, so a reset the gateway
// announced is not cut short by a later, shorter answer. Recommendation:
// keep.
func (l *limiter) rateLimited(t time.Time, h http.Header) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.consecutive429++
	d, ok := retryAfterDelay(h.Get(headerRetryAfter), t)
	if !ok {
		d, ok = resetDelay(h.Get(headerResetRequests))
	}
	if !ok {
		d = coolDownFirst << min(l.consecutive429-1, 10)
	}
	d = min(d, maxCoolDown)
	if end := t.Add(d); end.After(l.coolUntil) {
		l.coolUntil = end
	}
	l.broadcast()
	return d
}

// succeeded records a successful response at t (§2.2 item 6): the doubling
// starts again, and a response that says no request is left
// (x-ratelimit-remaining-requests: 0) with a parsable
// x-ratelimit-reset-requests cools the endpoint down until that reset.
func (l *limiter) succeeded(t time.Time, h http.Header) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.consecutive429 = 0
	if strings.TrimSpace(h.Get(headerRemainingRequests)) != "0" {
		return
	}
	if d, ok := resetDelay(h.Get(headerResetRequests)); ok {
		if end := t.Add(min(d, maxCoolDown)); end.After(l.coolUntil) {
			l.coolUntil = end
			l.broadcast()
		}
	}
}

// retryAfterDelay parses a Retry-After value at t: delay-seconds or an HTTP
// date in the future. It is not capped here.
func retryAfterDelay(h string, t time.Time) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(h, 10, 64); err == nil {
		if n <= 0 {
			return 0, false
		}
		return time.Duration(min(n, int64(maxCoolDown/time.Second))) * time.Second, true
	}
	if at, err := http.ParseTime(h); err == nil {
		if d := at.Sub(t); d > 0 {
			return d, true
		}
	}
	return 0, false
}

// resetDelay parses an OpenAI-style x-ratelimit-reset-requests value, a
// duration such as "6s", "1m30s" or "250ms".
func resetDelay(h string) (time.Duration, bool) {
	d, err := time.ParseDuration(strings.TrimSpace(h))
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// prune drops the starts that left the window at t. Call with mu held.
func (l *limiter) prune(t time.Time) {
	i := 0
	for i < len(l.starts) && !l.starts[i].After(t.Add(-rateWindow)) {
		i++
	}
	l.starts = l.starts[i:]
}

// leave removes w from the queue and wakes the others. Call with mu held.
func (l *limiter) leave(w *waiter) {
	if i := slices.Index(l.queue, w); i >= 0 {
		l.queue = slices.Delete(l.queue, i, i+1)
	}
	l.broadcast()
}

// broadcast wakes every waiter. Call with mu held.
func (l *limiter) broadcast() {
	for _, w := range l.queue {
		if w.wake != nil {
			w.wake()
		}
	}
}

// registry is the process-wide set of limiters (§2.2 item 1). Its key is the
// normalised base URL plus an HMAC-SHA256 of the API key under a random
// per-process salt, so that two models on the same base URL and key share a
// limiter and two keys do not. The key itself is never stored, and the key
// and its HMAC are never printed or logged.
type registry struct {
	once sync.Once
	salt []byte

	mu       sync.Mutex
	limiters map[string]*limiter
}

// limiters is the registry of the process.
var limiters = &registry{}

// String counts the endpoints only.
func (r *registry) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return "llm limiter registry (" + strconv.Itoa(len(r.limiters)) + " endpoints)"
}

// GoString is String, so that %#v prints no key.
func (r *registry) GoString() string { return r.String() }

// lookup returns the limiter of base and key, created on first use, with
// the limits of cfg applied.
func (r *registry) lookup(base *url.URL, key config.Secret, cfg config.LLM) *limiter {
	r.once.Do(func() {
		r.salt = make([]byte, 32)
		_, _ = rand.Read(r.salt)
	})
	k := config.NormalBaseURL(base)
	if key.IsSet() {
		m := hmac.New(sha256.New, r.salt)
		m.Write([]byte(key.Reveal()))
		k += "\x00" + hex.EncodeToString(m.Sum(nil))
	}
	r.mu.Lock()
	l, ok := r.limiters[k]
	if !ok {
		if r.limiters == nil {
			r.limiters = map[string]*limiter{}
		}
		l = &limiter{endpoint: logging.RedactURL(base.String())}
		r.limiters[k] = l
	}
	r.mu.Unlock()
	l.setLimits(limitsOf(cfg))
	return l
}
