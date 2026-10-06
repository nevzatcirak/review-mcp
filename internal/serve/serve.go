// Package serve is the shared HTTP transport of review-mcp (P6 spec §1.4,
// X-10). It serves the MCP streamable HTTP endpoint in stateless mode and
// puts a fixed middleware chain in front of it:
//
//  1. body limit (1 MiB, http.MaxBytesReader);
//  2. Host check (loopback listeners only; DNS-rebinding defence);
//  3. Origin check (exact allowlist; requests without Origin pass);
//  4. access token (when configured; constant-time comparison);
//  5. credential header checks (P6 §1.3); the credentials themselves are
//     read by the tool handler from the SDK's request header view
//     (mcpserver.Deps.ConfigFor), never copied anywhere by this package;
//  6. the concurrency gate, applied to tool calls only, inside the MCP server
//     (mcpserver.concurrencyGate).
//
// Every rejection has a fixed body and no tool code runs before all checks
// pass. Credentials are never stored: not in a struct field, a cache, a map
// or a package variable. This package only reads request headers to decide
// whether a request may continue.
package serve

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/credentials"
	"github.com/nevzatcirak/review-mcp/internal/mcpserver"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// Endpoints.
const (
	PathMCP    = "/mcp"
	PathHealth = "/healthz"
)

// Limits and timeouts (P6 spec §1.4).
const (
	MaxBodyBytes           = 1 << 20
	ReadHeaderTimeout      = 10 * time.Second
	IdleTimeout            = 120 * time.Second
	MaxHeaderBytes         = 64 << 10
	DefaultShutdownTimeout = 30 * time.Second
)

// Fixed response bodies. None echoes anything from the request.
const (
	BodyHealth          = "ok\n"
	BodyNotFound        = "not found\n"
	BodyTooLarge        = "request body too large\n"
	BodyForbiddenHost   = "forbidden: invalid Host header\n"
	BodyForbiddenOrigin = "forbidden: origin not allowed\n"
	BodyUnauthorized    = "unauthorized\n"
)

// ErrTLSLoad is returned by New when the configured TLS files do not form a
// usable certificate and key. The underlying error is not included.
var ErrTLSLoad = errors.New("cannot load the TLS certificate and key named by serve.tls_cert and serve.tls_key")

// Options configure a Server.
type Options struct {
	// Config is the startup configuration, validated in serve mode. It holds
	// no provider token; the access token and (with llm_key_source =
	// server) the LLM API key are the server's own secrets.
	Config *config.Config
	Report *config.Report
	// Logger receives the access lines and the SDK's filtered records. Nil
	// discards them.
	Logger *slog.Logger
	// NewResolver and NewLLM are the per-call factories (the wiring
	// package in production).
	NewResolver func(cfg *config.Config, logger *slog.Logger) *provider.Resolver
	NewLLM      func(cfg *config.Config, logger *slog.Logger) (review.Completer, error)
	// ShutdownTimeout bounds the drain on shutdown; zero means 30 s.
	ShutdownTimeout time.Duration
}

// Server is the serve-mode HTTP server. Its fields are fixed at New and hold
// no request data.
type Server struct {
	cfg             *config.Config
	log             *slog.Logger
	handler         http.Handler
	cert            *tls.Certificate
	loopback        bool
	port            string
	origins         map[string]bool
	shutdownTimeout time.Duration
}

// New builds the server. It loads the TLS key pair when TLS is configured
// and opens no listener.
func New(opts Options) (*Server, error) {
	cfg := opts.Config
	if cfg == nil {
		return nil, errors.New("serve: no configuration")
	}
	host, port, ok := config.SplitListen(cfg.Serve.Listen)
	if !ok {
		return nil, errors.New("serve: serve.listen is not host:port")
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		cfg:             cfg,
		log:             log,
		loopback:        config.IsLoopbackHost(host),
		port:            strconv.Itoa(port),
		origins:         make(map[string]bool, len(cfg.Serve.AllowedOrigins)),
		shutdownTimeout: opts.ShutdownTimeout,
	}
	if s.shutdownTimeout <= 0 {
		s.shutdownTimeout = DefaultShutdownTimeout
	}
	for _, o := range cfg.Serve.AllowedOrigins {
		s.origins[o] = true
	}
	if cfg.Serve.TLSEnabled() {
		cert, err := tls.LoadX509KeyPair(cfg.Serve.TLSCert, cfg.Serve.TLSKey)
		if err != nil {
			return nil, ErrTLSLoad
		}
		s.cert = &cert
	}

	mcpHandler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Config:      cfg,
		Report:      opts.Report,
		Logger:      opts.Logger,
		NewResolver: opts.NewResolver,
		NewLLM:      opts.NewLLM,
	})
	// The order is P6 §1.4's: body limit, Host, Origin, access token,
	// credential headers, then the MCP handler (whose server applies the
	// concurrency gate to tool calls).
	chain := s.limitBody(s.checkHost(s.checkOrigin(s.checkAccessToken(s.checkCredentials(mcpHandler)))))
	s.handler = s.accessLog(s.routes(chain))
	return s, nil
}

// Handler returns the complete HTTP handler (routing, middleware and access
// log).
func (s *Server) Handler() http.Handler { return s.handler }

// TLSEnabled reports whether the server serves TLS.
func (s *Server) TLSEnabled() bool { return s.cert != nil }

// URL is the MCP endpoint URL for the startup log line.
func (s *Server) URL() string {
	scheme := "http"
	if s.TLSEnabled() {
		scheme = "https"
	}
	return scheme + "://" + s.cfg.Serve.Listen + PathMCP
}

// Serve serves on ln until ctx is done, then stops accepting connections and
// drains in-flight requests for up to the shutdown timeout (30 s), closing
// whatever remains. It returns nil after a shutdown and the server's error
// if serving fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
		// No WriteTimeout: long LLM calls are bounded by the per-call
		// deadlines (llm.timeout_seconds and the provider timeouts).
		ErrorLog: log.New(serverErrorWriter{s.log}, "", 0),
	}
	errc := make(chan error, 1)
	if s.cert != nil {
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*s.cert}}
		go func() { errc <- srv.ServeTLS(ln, "", "") }()
	} else {
		go func() { errc <- srv.Serve(ln) }()
	}

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	s.log.Info("shutting down: draining in-flight requests", "timeout_seconds", int(s.shutdownTimeout/time.Second))
	sctx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		s.log.Warn("drain timeout reached; closing the remaining connections")
		_ = srv.Close()
	}
	<-errc
	return nil
}

// serverErrorWriter receives net/http's own error log (TLS handshake
// failures, recovered handler panics). Those lines can carry request-derived
// text, so only a fixed debug line is logged.
type serverErrorWriter struct{ log *slog.Logger }

func (w serverErrorWriter) Write(p []byte) (int, error) {
	w.log.Debug("http server error (details withheld)")
	return len(p), nil
}

// routes serves POST /mcp through chain, GET /healthz, and 404 for anything
// else.
func (s *Server) routes(chain http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case PathMCP:
			chain.ServeHTTP(w, r)
		case PathHealth:
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				fixed(w, http.StatusNotFound, BodyNotFound)
				return
			}
			// Unauthenticated and carries no other information.
			fixed(w, http.StatusOK, BodyHealth)
		default:
			fixed(w, http.StatusNotFound, BodyNotFound)
		}
	})
}

// fixed writes a fixed plain-text body.
func fixed(w http.ResponseWriter, status int, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// limitBody is step 1: a declared length over the limit is rejected at once;
// otherwise the body is wrapped with http.MaxBytesReader, and a body that
// turns out longer fails when the MCP handler reads it (413 from the SDK).
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > MaxBodyBytes {
			fixed(w, http.StatusRequestEntityTooLarge, BodyTooLarge)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// checkHost is step 2: on a loopback listener the Host must be a loopback
// name or address with the configured port.
func (s *Server) checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.loopback && !s.hostAllowed(r.Host) {
			fixed(w, http.StatusForbidden, BodyForbiddenHost)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(hostport string) bool {
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	return p == s.port && config.IsLoopbackHost(h)
}

// checkOrigin is step 3: a request carrying Origin passes only when that
// origin is listed exactly in serve.allowed_origins.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if vals := r.Header.Values("Origin"); len(vals) > 0 {
			if len(vals) != 1 || !s.origins[vals[0]] {
				fixed(w, http.StatusForbidden, BodyForbiddenOrigin)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// checkAccessToken is step 4: when an access token is configured, the
// request must carry "Authorization: Bearer <token>". The comparison is
// constant-time over SHA-256 digests, so neither the content nor the length
// of the token leaks through timing.
func (s *Server) checkAccessToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := s.cfg.Secrets.ServeAccessToken; want.IsSet() {
			got, ok := bearer(r.Header)
			if !ok || !tokenEqual(got, want.Reveal()) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				fixed(w, http.StatusUnauthorized, BodyUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// bearer returns the token of a single "Authorization: Bearer <token>"
// header.
func bearer(h http.Header) (string, bool) {
	vals := h.Values(credentials.HeaderAuthorization)
	if len(vals) != 1 {
		return "", false
	}
	scheme, tok, ok := strings.Cut(strings.Trim(vals[0], " \t"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.Trim(tok, " \t")
	return tok, tok != ""
}

func tokenEqual(got, want string) bool {
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

// checkCredentials is step 5: every credential header must pass the value
// checks of P6 §1.3. A malformed one is rejected with
// "malformed credential header: <name>", never echoing the value. Nothing is
// extracted or kept here; the tool handler reads the headers through the SDK
// (mcpserver.Deps.ConfigFor).
func (s *Server) checkCredentials(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := credentials.Check(r.Header); err != nil {
			var me *credentials.MalformedError
			if errors.As(err, &me) {
				fixed(w, http.StatusBadRequest, me.Error()+"\n")
				return
			}
			fixed(w, http.StatusBadRequest, "malformed credential header\n")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// accessLog writes one info line per request: method, path, status,
// duration and response bytes. It never logs headers, query strings or
// bodies; the remote address goes to a separate debug line.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := int(rec.status.Load())
		if status == 0 {
			status = http.StatusOK
		}
		s.log.Debug("http request remote address", "remote", r.RemoteAddr)
		s.log.Info("http request", "method", r.Method, "path", logPath(r.URL.Path), "status", status,
			"duration_ms", time.Since(start).Milliseconds(), "bytes", rec.bytes.Load())
	})
}

// logPath returns the path for the access line. Only the two endpoint paths
// are logged verbatim; any other path is client-chosen text.
// DESIGN-QUESTION: P6 §1.4 says the access line carries the path; should an
// arbitrary 404 path be logged as sent? — chose "(other)" because a path is
// client-controlled and could carry anything; the status still shows the
// 404.
func logPath(p string) string {
	switch p {
	case PathMCP, PathHealth:
		return p
	}
	return "(other)"
}

// recorder captures the status and the number of body bytes. The SDK may
// write from more than one goroutine, so the counters are atomic.
type recorder struct {
	http.ResponseWriter
	status atomic.Int32
	bytes  atomic.Int64
}

func (r *recorder) WriteHeader(code int) {
	r.status.CompareAndSwap(0, int32(code)) //nolint:gosec // G115: HTTP status codes fit in int32
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.status.CompareAndSwap(0, http.StatusOK)
	n, err := r.ResponseWriter.Write(b)
	r.bytes.Add(int64(n))
	return n, err
}

// Flush supports streaming responses.
func (r *recorder) Flush() { _ = http.NewResponseController(r.ResponseWriter).Flush() }

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
