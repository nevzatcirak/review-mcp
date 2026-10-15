package llm

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
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
)

// reasonWaitTooLong is the internal reason of a rate-limited failure whose
// needed wait exceeds llm.max_rate_wait_seconds (§2.2 item 4). It is not
// shown to clients as such.
const reasonWaitTooLong = "wait_too_long"

// limits are the configured limits of one endpoint.
type limits struct {
	// rpm is llm.requests_per_minute; 0 is no limit.
	rpm int
	// concurrency is llm.max_concurrent_requests; 0 is no limit.
	concurrency int
	// maxWait is llm.max_rate_wait_seconds.
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

// errWaitTooLong is acquire's failure when the needed wait exceeds the
// limit; need is that wait.
type errWaitTooLong struct{ need time.Duration }

func (e *errWaitTooLong) Error() string { return "llm: rate-limit wait too long" }

// acquire waits until a request may start on the endpoint (§2.2 items 2 to
// 4) and records its start; the caller must call release once the request
// is done, and only when acquire returned no error. Every wait is
// ctx-aware: a canceled ctx stops waiting at once and gives up the place in
// the queue. Before each wait the wait is computed; when it is longer than
// the configured maximum, acquire returns *errWaitTooLong without waiting.
//
// DESIGN-QUESTION (WP-8b DQ-1): a wait for llm.max_concurrent_requests has
// no computable length (it lasts until a request in flight ends), so it is
// not checked against llm.max_rate_wait_seconds; it is bounded by the
// request timeout of the requests in flight and by ctx. Recommendation:
// keep.
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
		if d > l.limits.maxWait {
			l.leave(w)
			l.mu.Unlock()
			log.Debug("llm rate limit wait too long", "endpoint", l.endpoint, "reason", reason, "wait_ms", d.Milliseconds())
			return st, &errWaitTooLong{need: d}
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

// waitFor is how long the waiter at queue position k must wait from t for
// the window, and why; 0 when the window leaves room now. Every waiter
// ahead of it is assumed to start as early as the window lets it, so a
// waiter's wait accounts for the queue ahead (FIFO). Call with mu held,
// after prune.
func (l *limiter) waitFor(k int, t time.Time) (time.Duration, string) {
	if l.limits.rpm == 0 {
		return 0, ""
	}
	n := len(l.starts)
	planned := make([]time.Time, 0, n+k+1)
	planned = append(planned, l.starts...)
	at := t
	for range k + 1 {
		at = t
		if len(planned) >= l.limits.rpm {
			if free := planned[len(planned)-l.limits.rpm].Add(rateWindow); free.After(at) {
				at = free
			}
		}
		planned = append(planned, at)
	}
	if d := at.Sub(t); d > 0 {
		return d, waitWindow
	}
	return 0, ""
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
	k := normalBase(base)
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

// normalBase is the base URL as the registry compares it: the scheme and
// host in lower case, without a default port, without credentials, query or
// fragment, and the path without a trailing slash.
func normalBase(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && p != defaultPort(u.Scheme) {
		host += ":" + p
	}
	return strings.ToLower(u.Scheme) + "://" + host + strings.TrimRight(u.EscapedPath(), "/")
}

func defaultPort(scheme string) string {
	if strings.EqualFold(scheme, "https") {
		return "443"
	}
	return "80"
}
