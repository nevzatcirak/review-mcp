package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/httpx"
)

// MaxRateLimitWait is the longest wait for a rate limit to reset before the
// one retry of a request (spec 2B §1 item 4).
const MaxRateLimitWait = 60 * time.Second

// Rate-limit headers.
const (
	headerRemaining  = "X-RateLimit-Remaining"
	headerReset      = "X-RateLimit-Reset"
	headerRetryAfter = "Retry-After"
)

// resetWindow bounds a reset time that is shown or waited for: a value
// further from now than this is treated as unknown.
const resetWindow = 24 * time.Hour

// rateLimited reports whether a 403 or 429 response is a rate limit: the
// primary limit says so with X-RateLimit-Remaining: 0, a secondary limit
// with Retry-After.
func rateLimited(status int, h http.Header) bool {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return false
	}
	return strings.TrimSpace(h.Get(headerRemaining)) == "0" || strings.TrimSpace(h.Get(headerRetryAfter)) != ""
}

// resetAt returns when a rate limit ends: Retry-After (seconds, or an HTTP
// date) when present, else X-RateLimit-Reset (epoch seconds). ok is false
// when neither header gives a usable time within resetWindow of now.
func resetAt(h http.Header, now time.Time) (time.Time, bool) {
	var at time.Time
	if ra := strings.TrimSpace(h.Get(headerRetryAfter)); ra != "" {
		if secs, ok := digits(ra); ok {
			at = now.Add(time.Duration(secs) * time.Second)
		} else if t, err := http.ParseTime(ra); err == nil {
			at = t
		}
	} else if secs, ok := digits(strings.TrimSpace(h.Get(headerReset))); ok {
		at = time.Unix(secs, 0)
	}
	if at.IsZero() || at.Before(now.Add(-resetWindow)) || at.After(now.Add(resetWindow)) {
		return time.Time{}, false
	}
	return at, true
}

// digits parses a non-negative integer of at most 12 ASCII digits.
func digits(s string) (int64, bool) {
	if s == "" || len(s) > 12 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// RateLimitHint is the Hint of a rate_limited error whose reset time is
// known: "retry after 2006-01-02 15:04:05 UTC".
func RateLimitHint(at time.Time) string {
	return "retry after " + at.UTC().Format("2006-01-02 15:04:05") + " UTC"
}

// classify maps a non-2xx GitHub response to its error from the status and
// the headers only. A 403 or 429 that carries a rate-limit signal is
// rate_limited, with the reset time in the hint when it is known; every
// other status maps as for every provider (provider.StatusError), so a 403
// without a rate-limit signal stays auth.
func classify(status int, h http.Header, now time.Time) *provider.Error {
	if !rateLimited(status, h) {
		return provider.StatusError(status)
	}
	e := &provider.Error{Class: provider.ClassRateLimited, Status: status}
	if at, ok := resetAt(h, now); ok {
		e.Hint = RateLimitHint(at)
	}
	return e
}

// sleepCtx waits for d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// request is one API request; body is resent unchanged on the retry.
type request struct {
	method, path, accept string
	body                 []byte
	maxBytes             int64
	capKey               string
}

// do performs r. When the answer is a rate limit whose reset is known, at
// most MaxRateLimitWait away and before ctx's deadline (if it has one), it
// waits once and repeats the request once; the second answer is final,
// whatever it is. There is no retry loop.
func (p *Provider) do(ctx context.Context, r request) (*httpx.Response, error) {
	resp, err := p.send(ctx, r)
	if err == nil || !errors.Is(err, provider.ErrRateLimited) || resp.Header == nil {
		return resp, err
	}
	now := p.now()
	at, ok := resetAt(resp.Header, now)
	if !ok {
		return resp, err
	}
	wait := max(at.Sub(now), 0)
	if wait > MaxRateLimitWait {
		return resp, err
	}
	if dl, has := ctx.Deadline(); has && !now.Add(wait).Before(dl) {
		return resp, err
	}
	p.logger.Debug("github rate limit: waiting once before the retry", "wait_ms", wait.Milliseconds())
	if serr := p.sleep(ctx, wait); serr != nil {
		return resp, provider.TransportError(serr)
	}
	return p.send(ctx, r)
}

func (p *Provider) send(ctx context.Context, r request) (*httpx.Response, error) {
	hr := httpx.Request{Method: r.method, PathAndQuery: r.path, Accept: r.accept, MaxBytes: r.maxBytes, CapKey: r.capKey}
	if r.method == "" {
		hr.Method = http.MethodGet
	}
	if hr.MaxBytes <= 0 {
		hr.MaxBytes, hr.CapKey = httpx.MaxJSONBytes, httpx.JSONCapKey
	}
	if r.body != nil {
		hr.Body, hr.ContentType = strings.NewReader(string(r.body)), "application/json"
	}
	return p.client.DoRequest(ctx, hr)
}

// getJSON GETs path (10 MiB cap) and decodes the JSON body into out.
func (p *Provider) getJSON(ctx context.Context, path string, out any) error {
	resp, err := p.do(ctx, request{path: path})
	if err != nil {
		return err
	}
	// The decoder error is deliberately not propagated: syntax errors quote
	// bytes of the (untrusted) response.
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return protocolErr("response is not valid JSON of the expected shape")
	}
	return nil
}

// fetchPage is the httpx.PageFetch of every list: a GET through do, so that
// each page gets the bounded rate-limit wait.
func (p *Provider) fetchPage(ctx context.Context, path string) ([]byte, http.Header, error) {
	resp, err := p.do(ctx, request{path: path})
	if err != nil {
		return nil, nil, err
	}
	return resp.Data, resp.Header, nil
}
