package provider

import (
	"log/slog"
	"net/url"
	"strings"
	"sync"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/logging"
)

// Factory builds one provider kind and knows how to parse that provider's PR
// paths. Provider packages implement it and hand it to NewResolver; this
// keeps internal/provider free of imports of its implementations.
type Factory interface {
	Kind() Kind
	// ParsePRPath parses the path remainder after the matched base path. The
	// remainder starts with "/" and is handed over in its ESCAPED form
	// (url.URL.EscapedPath style): a "%2F" inside a segment is still one
	// segment, so it cannot fabricate extra segments. ParsePRPath must split
	// on "/" first and path-unescape each segment itself.
	ParsePRPath(remainder string) (namespace, repo string, number int64, err error)
	// New builds a request-scoped Provider from the configuration.
	New(cfg *config.Config, logger *slog.Logger) (Provider, error)
}

type candidate struct {
	kind    Kind
	scheme  string
	host    string // lower-case hostname
	port    string // effective port
	segs    []string
	factory Factory // nil when enabled but not registered
}

// Resolver maps a PR URL to a PRRef and a Provider (X-2). It performs no
// network I/O.
//
// A Resolver is request-scoped: it remembers the providers its Resolve calls
// built so that CloseIdleConnections can release their connections when the
// call ends.
type Resolver struct {
	cfg        *config.Config
	logger     *slog.Logger
	candidates []candidate
	configured []string // redacted base URLs for hints

	mu    sync.Mutex
	built []Provider
}

// IdleCloser is implemented by a Provider (or any per-call client) that owns
// an HTTP transport whose idle connections can be released.
type IdleCloser interface {
	CloseIdleConnections()
}

// CloseIdleConnections closes the idle connections of every provider this
// resolver built (those implementing IdleCloser). The caller runs it, with
// defer, when the tool call that owns the resolver ends. It is safe on a nil
// or unused resolver and may be called more than once.
func (r *Resolver) CloseIdleConnections() {
	if r == nil {
		return
	}
	r.mu.Lock()
	built := r.built
	r.built = nil
	r.mu.Unlock()
	for _, p := range built {
		if c, ok := p.(IdleCloser); ok {
			c.CloseIdleConnections()
		}
	}
}

// NewResolver builds a Resolver from the enabled providers' base URLs (plus
// Gitea's web_url when set). For GitHub the base URL is the web base;
// github.api_url only receives API calls and is never matched. Factories whose Kind is not enabled in cfg are
// ignored.
//
// Factories are injected (instead of the spec's NewResolver(cfg)) because the
// provider packages import this package, so it cannot construct them; this
// avoids both an import cycle and a registry global.
//
// A provider that is enabled in cfg but has no registered factory is a wiring
// bug: Resolve returns a url_not_configured error (never a panic) when a URL
// matches its base, and nothing is sent over the network.
func NewResolver(cfg *config.Config, logger *slog.Logger, factories ...Factory) *Resolver {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	byKind := map[Kind]Factory{}
	for _, f := range factories {
		if f != nil {
			byKind[f.Kind()] = f
		}
	}
	r := &Resolver{cfg: cfg, logger: logger}
	add := func(kind Kind, raw string) {
		if raw == "" {
			return
		}
		r.configured = append(r.configured, logging.RedactURL(raw))
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return
		}
		r.candidates = append(r.candidates, candidate{
			kind:    kind,
			scheme:  strings.ToLower(u.Scheme),
			host:    strings.ToLower(u.Hostname()),
			port:    effectivePort(u),
			segs:    splitSegments(u.EscapedPath()),
			factory: byKind[kind],
		})
	}
	if cfg != nil {
		add(KindGitea, cfg.Gitea.BaseURL)
		if cfg.Gitea.BaseURL != "" {
			add(KindGitea, cfg.Gitea.WebURL)
		}
		add(KindBitbucketServer, cfg.BitbucketServer.BaseURL)
		add(KindGitHub, cfg.GitHub.BaseURL)
	}
	return r
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// splitSegments splits an escaped path into escaped segments, dropping the
// leading slash and any trailing empty segment.
func splitSegments(p string) []string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return nil
	}
	segs := strings.Split(p, "/")
	if segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	return segs
}

// prefixLen reports whether base is a segment-boundary prefix of path.
// Segments are compared after unescaping, so "%62b" equals "bb" but a
// "%2F" never splits a segment.
func prefixLen(base, path []string) bool {
	if len(base) > len(path) {
		return false
	}
	for i, b := range base {
		bu, err1 := url.PathUnescape(b)
		pu, err2 := url.PathUnescape(path[i])
		if err1 != nil || err2 != nil || bu != pu {
			return false
		}
	}
	return true
}

func (r *Resolver) notConfigured(hint string) error {
	return &Error{Class: ClassURLNotConfigured, Hint: hint}
}

// Resolve matches rawURL against the configured providers and returns the
// PR reference and a request-scoped Provider. No network I/O happens here.
//
// Matching: same scheme, host and effective port (case-insensitive), and the
// candidate path is a prefix of the URL path at a segment boundary; the
// longest prefix wins. Query and fragment are ignored. Userinfo in the PR
// URL is rejected as url_malformed without being echoed.
func (r *Resolver) Resolve(rawURL string) (PRRef, Provider, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return PRRef{}, nil, &Error{Class: ClassURLMalformed, Hint: "the URL could not be parsed"}
	}
	if u.User != nil {
		return PRRef{}, nil, &Error{Class: ClassURLMalformed, Hint: "the URL must not contain credentials"}
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Hostname() == "" {
		return PRRef{}, nil, r.notConfigured(r.baseHint())
	}
	host, port := strings.ToLower(u.Hostname()), effectivePort(u)
	segs := splitSegments(u.EscapedPath())

	var best *candidate
	for i := range r.candidates {
		c := &r.candidates[i]
		if c.scheme != scheme || c.host != host || c.port != port || !prefixLen(c.segs, segs) {
			continue
		}
		if best == nil || len(c.segs) > len(best.segs) {
			best = c
		}
	}
	if best == nil {
		return PRRef{}, nil, r.notConfigured(r.baseHint())
	}
	if best.factory == nil {
		return PRRef{}, nil, r.notConfigured("no provider implementation is registered for " + string(best.kind))
	}

	rest := segs[len(best.segs):]
	for _, s := range rest {
		if d, err := url.PathUnescape(s); err != nil || d == "." || d == ".." {
			return PRRef{}, nil, &Error{Class: ClassURLMalformed, Hint: "invalid path segment"}
		}
	}
	remainder := "/" + strings.Join(rest, "/")
	ns, repo, num, err := best.factory.ParsePRPath(remainder)
	if err != nil || ns == "" || repo == "" || num <= 0 {
		return PRRef{}, nil, &Error{Class: ClassURLMalformed, Hint: "expected a " + string(best.kind) + " pull request URL"}
	}
	p, err := best.factory.New(r.cfg, r.logger)
	if err != nil {
		return PRRef{}, nil, err
	}
	r.mu.Lock()
	r.built = append(r.built, p)
	r.mu.Unlock()
	r.logger.Debug("resolved pull request URL", "kind", string(best.kind), "url", logging.RedactURL(rawURL))
	return PRRef{Kind: best.kind, Namespace: ns, Repo: repo, Number: num, URL: rawURL}, p, nil
}

func (r *Resolver) baseHint() string {
	if len(r.configured) == 0 {
		return "no provider is configured"
	}
	return "configured base URLs: " + strings.Join(r.configured, ", ")
}
