// Package llm is the OpenAI-compatible chat-completions client used by the
// review pipeline. It owns request building (the only place the API key is
// revealed), the redirect policy, the response cap, error classification
// (X-6) and the DQ-9 transport retries. It never logs prompts, responses or
// headers (X-8). The client uses the system TLS roots only; v1 has no CA or
// insecure-skip-verify setting for llm.*.
package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	// MaxResponseBytes caps the response body.
	MaxResponseBytes int64 = 8 << 20
	// MaxRetryAfter caps a server-supplied Retry-After delay.
	MaxRetryAfter = 30 * time.Second

	maxRedirects = 10
	// errBodyBytes bounds how much of a 400/413 body is inspected in memory
	// for context-length wording; other error bodies are drained, not read.
	errBodyBytes = 64 << 10
	drainBytes   = 4 << 10
	chatPath     = "/chat/completions"
)

// backoff is the default delay before retry n (0-based); the last value
// repeats.
var backoff = [...]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// Usage is the token accounting reported by the endpoint (zero when absent).
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Response is a successful completion.
type Response struct {
	Content string
	// Truncated is true when finish_reason was "length".
	Truncated bool
	Usage     Usage
}

// Option customizes a Client (used by tests).
type Option func(*Client)

// WithSleeper replaces the retry sleep. It must return ctx.Err() when ctx is
// done first.
func WithSleeper(s func(ctx context.Context, d time.Duration) error) Option {
	return func(c *Client) { c.sleep = s }
}

// WithClock replaces the wall clock (used for HTTP-date Retry-After values
// and request durations).
func WithClock(now func() time.Time) Option {
	return func(c *Client) { c.now = now }
}

// WithProbeTimeout replaces the 10 s timeout of ResolveContextWindow.
func WithProbeTimeout(d time.Duration) Option {
	return func(c *Client) { c.probeTimeout = d }
}

// Client calls one OpenAI-compatible endpoint. It holds no per-request state;
// Complete is safe for concurrent use.
type Client struct {
	cfg      config.LLM
	key      config.Secret
	base     *url.URL
	baseStr  string
	baseSegs []string
	logger   *slog.Logger
	hc       *http.Client
	timeout  time.Duration
	// probeTimeout bounds ResolveContextWindow.
	probeTimeout time.Duration
	maxBytes     int64
	sleep        func(context.Context, time.Duration) error
	now          func() time.Time
}

// redirectError marks a refused redirect.
type redirectError struct{ reason string }

func (e *redirectError) Error() string { return "redirect refused: " + e.reason }

// New builds a Client from the validated LLM config. The returned errors
// never contain the key.
func New(cfg config.LLM, key config.Secret, logger *slog.Logger, opts ...Option) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, errors.New("llm: invalid base URL")
	}
	if !key.IsSet() {
		return nil, errors.New("llm: API key is not set")
	}
	if cfg.Model == "" {
		return nil, errors.New("llm: model is not set")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	c := &Client{
		cfg:          cfg,
		key:          key,
		base:         u,
		baseStr:      strings.TrimRight(cfg.BaseURL, "/"),
		baseSegs:     splitEscaped(u.EscapedPath()),
		logger:       logger,
		timeout:      timeout,
		probeTimeout: probeTimeout,
		maxBytes:     MaxResponseBytes,
		sleep:        sleepCtx,
		now:          time.Now,
	}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("llm: unexpected default transport")
	}
	tr = tr.Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	c.hc = &http.Client{Transport: tr, CheckRedirect: c.checkRedirect}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// CloseIdleConnections closes the idle keep-alive connections of the
// client's own transport, and any connection that becomes idle later. The
// transport is per client (one per tool call), so this never touches
// another call's connections. It is safe on a nil or unused client and may
// be called more than once.
func (c *Client) CloseIdleConnections() {
	if c == nil || c.hc == nil {
		return
	}
	c.hc.CloseIdleConnections()
}

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

func splitEscaped(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// checkRedirect follows a redirect only when the target keeps scheme, host,
// effective port and sits under the base path at a segment boundary.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return &redirectError{"too many redirects"}
	}
	t := req.URL
	if !strings.EqualFold(t.Scheme, c.base.Scheme) || !strings.EqualFold(t.Hostname(), c.base.Hostname()) || port(t) != port(c.base) {
		return &redirectError{"target is outside the configured origin"}
	}
	segs := splitEscaped(t.EscapedPath())
	if len(segs) < len(c.baseSegs) {
		return &redirectError{"target is outside the base path"}
	}
	for i, b := range c.baseSegs {
		bu, e1 := url.PathUnescape(b)
		su, e2 := url.PathUnescape(segs[i])
		if e1 != nil || e2 != nil || bu != su {
			return &redirectError{"target is outside the base path"}
		}
	}
	for _, s := range segs {
		if d, err := url.PathUnescape(s); err != nil || d == ".." {
			return &redirectError{"target is outside the base path"}
		}
	}
	c.setAuth(req)
	return nil
}

// setAuth is the only place the API key is revealed.
func (c *Client) setAuth(req *http.Request) {
	req.Header.Del("Authorization")
	req.Header.Set("Authorization", "Bearer "+c.key.Reveal())
}

// --- request / response wire types --------------------------------------

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest carries sampling parameters as pointers with omitempty so that
// an unset value is absent from the JSON (DQ-26), while an explicit zero is
// still sent.
type chatRequest struct {
	Model           string    `json:"model"`
	Messages        []message `json:"messages"`
	Temperature     *float64  `json:"temperature,omitempty"`
	Seed            *int64    `json:"seed,omitempty"`
	ReasoningEffort *string   `json:"reasoning_effort,omitempty"`
	MaxTokens       *int      `json:"max_tokens,omitempty"`
}

// buildRequest builds a fresh, request-scoped body (DQ-9: nothing is shared
// between attempts or calls). Config values are copied, not aliased.
func (c *Client) buildRequest(system, user string) ([]byte, error) {
	r := chatRequest{
		Model: c.cfg.Model,
		Messages: []message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	if v := c.cfg.Temperature; v != nil {
		x := *v
		r.Temperature = &x
	}
	if v := c.cfg.Seed; v != nil {
		x := *v
		r.Seed = &x
	}
	if v := c.cfg.ReasoningEffort; v != nil {
		x := *v
		r.ReasoningEffort = &x
	}
	if v := c.cfg.MaxOutputTokens; v != nil {
		x := *v
		r.MaxTokens = &x
	}
	return json.Marshal(r)
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// contentText reads a string content, or concatenates the "text" of an array
// of content parts. Anything else yields "".
func contentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		return s
	case '[':
		var parts []struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) != nil {
			return ""
		}
		var sb strings.Builder
		for _, p := range parts {
			if p.Text != nil && (p.Type == "" || p.Type == "text") {
				sb.WriteString(*p.Text)
			}
		}
		return sb.String()
	}
	return ""
}

func parseResponse(data []byte) (*Response, *Error) {
	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		// The decoder error is not propagated: syntax errors quote bytes.
		return nil, &Error{Class: ClassProtocol, Detail: "response is not valid JSON of the expected shape"}
	}
	if len(cr.Choices) == 0 {
		return nil, &Error{Class: ClassProtocol, Detail: "response has no choices"}
	}
	content := contentText(cr.Choices[0].Message.Content)
	if strings.TrimSpace(content) == "" {
		return nil, &Error{Class: ClassProtocol, Detail: "response content is empty"}
	}
	return &Response{
		Content:   content,
		Truncated: cr.Choices[0].FinishReason == "length",
		Usage: Usage{
			PromptTokens:     cr.Usage.PromptTokens,
			CompletionTokens: cr.Usage.CompletionTokens,
			TotalTokens:      cr.Usage.TotalTokens,
		},
	}, nil
}

// --- Complete ------------------------------------------------------------

// Complete sends one system and one user message and returns the first
// choice. Retryable classes are retried up to cfg.MaxRetries times. A
// canceled or expired ctx stops everything and is returned as ctx.Err().
func (c *Client) Complete(ctx context.Context, system, user string) (*Response, error) {
	maxRetries := max(c.cfg.MaxRetries, 0)
	for attempt := 0; ; attempt++ {
		resp, retryAfter, le := c.attempt(ctx, attempt, system, user)
		if le == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !le.Class.Retryable() || attempt >= maxRetries {
			return nil, le
		}
		delay := backoff[min(attempt, len(backoff)-1)]
		if retryAfter > 0 {
			delay = min(retryAfter, MaxRetryAfter)
		}
		c.logger.Debug("llm retry", "class", string(le.Class), "attempt", attempt+1, "delay_ms", delay.Milliseconds())
		if serr := c.sleep(ctx, delay); serr != nil {
			return nil, serr
		}
	}
}

// attempt performs one request. The body is rebuilt here, so no state is
// shared between attempts.
func (c *Client) attempt(ctx context.Context, attempt int, system, user string) (*Response, time.Duration, *Error) {
	body, err := c.buildRequest(system, user)
	if err != nil {
		return nil, 0, &Error{Class: ClassProtocol, Detail: "request could not be encoded"}
	}
	full := c.baseStr + chatPath
	rctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, full, bytes.NewReader(body))
	if err != nil {
		return nil, 0, &Error{Class: ClassProtocol, Detail: "invalid request"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.setAuth(req)

	start := c.now()
	dur := func() int64 { return c.now().Sub(start).Milliseconds() }
	logFail := func(status int, class ErrorClass) {
		c.logger.Debug("llm request", "method", http.MethodPost, "url", logging.RedactURL(full),
			"status", status, "class", string(class), "attempt", attempt+1, "duration_ms", dur())
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		le := c.transportError(ctx, rctx, err)
		logFail(0, le.Class)
		return nil, 0, le
	}
	defer func() { _ = resp.Body.Close() }()
	status := resp.StatusCode

	if status < 200 || status >= 300 {
		var errBody []byte
		if status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge {
			errBody, _ = io.ReadAll(io.LimitReader(resp.Body, errBodyBytes))
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainBytes))
		}
		retryAfter := c.retryAfter(resp.Header.Get("Retry-After"))
		le := c.statusError(status, errBody)
		logFail(status, le.Class)
		return nil, retryAfter, le
	}

	if resp.ContentLength > c.maxBytes {
		le := &Error{Class: ClassProtocol, Status: status, Detail: "response exceeded the size limit"}
		logFail(status, le.Class)
		return nil, 0, le
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		le := c.transportError(ctx, rctx, err)
		logFail(status, le.Class)
		return nil, 0, le
	}
	if int64(len(data)) > c.maxBytes {
		le := &Error{Class: ClassProtocol, Status: status, Detail: "response exceeded the size limit"}
		logFail(status, le.Class)
		return nil, 0, le
	}
	out, le := parseResponse(data)
	if le != nil {
		le.Status = status
		logFail(status, le.Class)
		return nil, 0, le
	}
	c.logger.Debug("llm request", "method", http.MethodPost, "url", logging.RedactURL(full),
		"status", status, "class", "ok", "attempt", attempt+1, "duration_ms", dur(),
		"prompt_tokens", out.Usage.PromptTokens, "completion_tokens", out.Usage.CompletionTokens,
		"total_tokens", out.Usage.TotalTokens, "truncated", out.Truncated)
	return out, 0, nil
}

// transportError classifies a failure from Do or a body read. err.Error() is
// never included: *url.Error embeds the request URL.
func (c *Client) transportError(parent, reqCtx context.Context, err error) *Error {
	var re *redirectError
	if errors.As(err, &re) {
		return &Error{Class: ClassProtocol, Detail: re.reason}
	}
	if errors.Is(reqCtx.Err(), context.DeadlineExceeded) && parent.Err() == nil {
		return &Error{Class: ClassTimeout}
	}
	pe := provider.TransportError(err)
	if pe.Hint == "timeout" {
		return &Error{Class: ClassTimeout}
	}
	return &Error{Class: ClassTransport, Detail: pe.Hint}
}

// statusError maps a non-2xx status to a sanitized error. errBody (400/413
// only) is inspected in memory and never returned or logged.
func (c *Client) statusError(status int, errBody []byte) *Error {
	e := &Error{Status: status}
	switch {
	case status == 401 || status == 403:
		e.Class, e.Hint = ClassAuth, hintAPIKey
	case status == 404:
		e.Class, e.Hint = ClassNotFound, hintBaseURLModel
	case status == 429:
		e.Class = ClassRateLimited
	case status >= 500:
		e.Class = ClassUpstream
	case (status == 400 || status == 413) && mentionsContextLength(errBody):
		e.Class, e.Hint = ClassContextTooLong, hintContextWindow
	case status >= 400:
		// DESIGN-QUESTION: which class for 4xx statuses the spec does not
		// list (413 without context wording, 405, 408, 409, ...)? — chose
		// llm_bad_request because the request was refused and the sampling
		// hint is the only actionable advice.
		e.Class, e.Hint = ClassBadRequest, c.samplingHint()
	default:
		// 1xx/3xx that was not followed.
		e.Class = ClassProtocol
	}
	return e
}

// samplingHint names the explicitly configured sampling keys (DQ-26).
func (c *Client) samplingHint() string {
	var keys []string
	if c.cfg.MaxOutputTokens != nil {
		keys = append(keys, "llm.max_output_tokens")
	}
	if c.cfg.Temperature != nil {
		keys = append(keys, "llm.temperature")
	}
	if c.cfg.Seed != nil {
		keys = append(keys, "llm.seed")
	}
	if c.cfg.ReasoningEffort != nil {
		keys = append(keys, "llm.reasoning_effort")
	}
	return strings.Join(keys, ", ")
}

// mentionsContextLength is a case-insensitive in-memory match on "context"
// together with "length", "window" or "maximum".
func mentionsContextLength(body []byte) bool {
	s := strings.ToLower(string(body))
	if !strings.Contains(s, "context") {
		return false
	}
	return strings.Contains(s, "length") || strings.Contains(s, "window") || strings.Contains(s, "maximum")
}

// retryAfter parses a Retry-After header (delay-seconds or HTTP-date). It
// returns 0 when absent, unparseable or not positive; the caller caps it.
func (c *Client) retryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if n, err := strconv.ParseInt(h, 10, 64); err == nil {
		if n <= 0 {
			return 0
		}
		if n > int64(MaxRetryAfter/time.Second) {
			return MaxRetryAfter
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := t.Sub(c.now()); d > 0 {
			return d
		}
	}
	return 0
}
