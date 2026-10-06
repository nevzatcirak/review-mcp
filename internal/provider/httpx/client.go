// Package httpx is the shared HTTP client for provider implementations. It
// owns auth injection, TLS configuration, the redirect policy, response
// caps, status mapping and request logging. It never sees a secret type:
// providers hand it an Auth func that sets the header.
package httpx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	// RequestTimeout is the per-request timeout (a constant, not a config key).
	RequestTimeout = 60 * time.Second
	// MaxJSONBytes caps JSON response bodies.
	MaxJSONBytes int64 = 10 << 20
	// JSONCapKey is the Hint used when a JSON body exceeds MaxJSONBytes.
	JSONCapKey = "(json response limit)"

	maxRedirects = 10
	// drainBytes bounds how much of an error body is read to let the
	// connection be reused; the content is discarded.
	drainBytes = 4 << 10
)

// Options configures a Client.
type Options struct {
	// BaseURL is the normalized base URL (scheme, host, optional context
	// path; no trailing slash). Requests are made to BaseURL+path only.
	BaseURL string
	// Auth sets the provider-specific credentials on a request. It is called
	// for every request, including followed same-origin redirects. It is the
	// only place a secret is revealed.
	Auth func(*http.Request)
	// CACertPath is an optional PEM bundle added to the system roots.
	CACertPath string
	// InsecureSkipVerify disables TLS verification; honoured only when set.
	InsecureSkipVerify bool
	// UserAgent is sent as User-Agent when non-empty.
	UserAgent string
	Logger    *slog.Logger
	// ReadFile reads CACertPath; nil means os.ReadFile.
	ReadFile func(path string) ([]byte, error)
}

// Client is a base-URL-pinned HTTP client.
type Client struct {
	base     *url.URL
	baseStr  string
	baseSegs []string
	auth     func(*http.Request)
	ua       string
	logger   *slog.Logger
	hc       *http.Client
	timeout  time.Duration
}

// redirectError is returned by CheckRedirect for a refused redirect.
type redirectError struct{ reason string }

func (e *redirectError) Error() string { return "redirect refused: " + e.reason }

// New builds a Client. An unparsable or empty CACertPath PEM is an error
// naming the path.
func New(opts Options) (*Client, error) {
	u, err := url.Parse(opts.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, errors.New("httpx: invalid base URL")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	c := &Client{
		base:     u,
		baseStr:  strings.TrimRight(opts.BaseURL, "/"),
		baseSegs: splitEscaped(u.EscapedPath()),
		auth:     opts.Auth,
		ua:       opts.UserAgent,
		logger:   logger,
		timeout:  RequestTimeout,
	}

	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("httpx: unexpected default transport")
	}
	tr = tr.Clone()
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.InsecureSkipVerify {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec // explicit opt-in via config; the config layer warns about it
	}
	if opts.CACertPath != "" {
		read := opts.ReadFile
		if read == nil {
			read = os.ReadFile
		}
		pem, err := read(opts.CACertPath) //nolint:gosec // path comes from operator configuration
		if err != nil {
			return nil, fmt.Errorf("httpx: reading CA certificate %q failed", opts.CACertPath)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("httpx: no valid PEM certificate found in %q", opts.CACertPath)
		}
		tlsCfg.RootCAs = pool
	}
	tr.TLSClientConfig = tlsCfg
	c.hc = &http.Client{Transport: tr, CheckRedirect: c.checkRedirect}
	return c, nil
}

// CloseIdleConnections closes the idle keep-alive connections of the
// client's own transport, and any connection that becomes idle later (a
// response body closed after this call). The transport is per client, so
// this never touches another call's connections. Callers run it when the
// tool call that built the client ends. It is safe on a nil or unused
// client and may be called more than once.
func (c *Client) CloseIdleConnections() {
	if c == nil || c.hc == nil {
		return
	}
	c.hc.CloseIdleConnections()
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

// checkRedirect follows a redirect only when the target keeps the same
// scheme, host, effective port and sits under the base path at a segment
// boundary. Anything else is refused without a request being sent.
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
	// Credentials are set per request by Auth, never copied by hand.
	req.Header.Del("Authorization")
	if c.auth != nil {
		c.auth(req)
	}
	return nil
}

// Do performs method on BaseURL+pathAndQuery. pathAndQuery must start with
// "/" and is already escaped by the caller; it may include a query. The body
// is read through a maxBytes+1 limit: an overflow yields a too_large error
// whose Hint is capKey. A non-2xx status is mapped through
// provider.StatusError; its body is discarded and never returned. A 404 is a
// *provider.Error of class not_found, which callers may test with
// errors.Is(err, provider.ErrNotFound). The returned status is set whenever
// a response was received.
func (c *Client) Do(ctx context.Context, method, pathAndQuery string, body io.Reader, contentType string, maxBytes int64, capKey string) ([]byte, int, error) {
	if !strings.HasPrefix(pathAndQuery, "/") {
		return nil, 0, &provider.Error{Class: provider.ClassProtocol, Hint: "invalid request path"}
	}
	full := c.baseStr + pathAndQuery
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, 0, &provider.Error{Class: provider.ClassProtocol, Hint: "invalid request"}
	}
	if req.URL.Host != c.base.Host {
		return nil, 0, &provider.Error{Class: provider.ClassProtocol, Hint: "invalid request path"}
	}
	if c.ua != "" {
		req.Header.Set("User-Agent", c.ua)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, */*;q=0.5")
	if c.auth != nil {
		c.auth(req)
	}

	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		var re *redirectError
		var perr *provider.Error
		if errors.As(err, &re) {
			perr = &provider.Error{Class: provider.ClassProtocol, Hint: re.reason}
		} else {
			perr = provider.TransportError(err)
		}
		c.logger.Debug("http request failed", "method", method, "url", logging.RedactURL(full),
			"class", string(perr.Class), "duration_ms", time.Since(start).Milliseconds())
		return nil, 0, perr
	}
	defer func() { _ = resp.Body.Close() }()
	status := resp.StatusCode
	logDone := func(class string) {
		c.logger.Debug("http request", "method", method, "url", logging.RedactURL(full),
			"status", status, "class", class, "duration_ms", time.Since(start).Milliseconds())
	}

	if perr := provider.StatusError(status); perr != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainBytes))
		logDone(string(perr.Class))
		return nil, status, perr
	}
	if resp.ContentLength > maxBytes {
		logDone(string(provider.ClassTooLarge))
		return nil, status, &provider.Error{Class: provider.ClassTooLarge, Hint: capKey}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		perr := provider.TransportError(err)
		logDone(string(perr.Class))
		return nil, status, perr
	}
	if int64(len(data)) > maxBytes {
		logDone(string(provider.ClassTooLarge))
		return nil, status, &provider.Error{Class: provider.ClassTooLarge, Hint: capKey}
	}
	logDone("ok")
	return data, status, nil
}

// Get is Do with GET and no request body.
func (c *Client) Get(ctx context.Context, pathAndQuery string, maxBytes int64, capKey string) ([]byte, int, error) {
	return c.Do(ctx, http.MethodGet, pathAndQuery, nil, "", maxBytes, capKey)
}

// GetJSON GETs pathAndQuery (10 MiB cap) and decodes the JSON body into out.
func (c *Client) GetJSON(ctx context.Context, pathAndQuery string, out any) error {
	data, _, err := c.Get(ctx, pathAndQuery, MaxJSONBytes, JSONCapKey)
	if err != nil {
		return err
	}
	return decodeJSON(data, out)
}

// SendJSON sends in (JSON-encoded, may be nil) with method and decodes the
// JSON response into out (may be nil to ignore the response body).
func (c *Client) SendJSON(ctx context.Context, method, pathAndQuery string, in, out any) error {
	var rd io.Reader
	ct := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return &provider.Error{Class: provider.ClassProtocol, Hint: "request could not be encoded"}
		}
		rd, ct = strings.NewReader(string(b)), "application/json"
	}
	data, _, err := c.Do(ctx, method, pathAndQuery, rd, ct, MaxJSONBytes, JSONCapKey)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return decodeJSON(data, out)
}

func decodeJSON(data []byte, out any) error {
	// The decoder error is deliberately not propagated: syntax errors quote
	// bytes of the (untrusted) response.
	if err := json.Unmarshal(data, out); err != nil {
		return &provider.Error{Class: provider.ClassProtocol, Hint: "response is not valid JSON of the expected shape"}
	}
	return nil
}
