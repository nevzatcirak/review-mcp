package config

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Mode is the transport a configuration is validated for.
type Mode int

// Modes.
const (
	// ModeStdio is the default: one user, credentials from the environment.
	ModeStdio Mode = iota
	// ModeServe is the shared HTTP transport: credentials arrive with each
	// request (X-10, P6 spec §1.2).
	ModeServe
)

// Values of serve.llm_key_source.
const (
	// LLMKeySourceHeader takes the LLM API key from the
	// X-Review-MCP-LLM-API-Key header of each request.
	LLMKeySourceHeader = "header"
	// LLMKeySourceServer takes the LLM API key from REVIEW_MCP_LLM_API_KEY;
	// an access token is then mandatory.
	LLMKeySourceServer = "server"
)

// DefaultServeListen is the default of serve.listen.
const DefaultServeListen = "127.0.0.1:8787"

// Bounds of serve.max_concurrent_calls.
const (
	MinServeConcurrentCalls = 1
	MaxServeConcurrentCalls = 64
)

// Serve configures the serve transport (P6 spec §1.2). Only serve mode reads
// or validates it.
type Serve struct {
	Listen             string   `toml:"listen" json:"listen"`
	TLSCert            string   `toml:"tls_cert" json:"tls_cert"`
	TLSKey             string   `toml:"tls_key" json:"tls_key"`
	AllowInsecureHTTP  bool     `toml:"allow_insecure_http" json:"allow_insecure_http"`
	LLMKeySource       string   `toml:"llm_key_source" json:"llm_key_source"`
	AllowedOrigins     []string `toml:"allowed_origins" json:"allowed_origins"`
	MaxConcurrentCalls int      `toml:"max_concurrent_calls" json:"max_concurrent_calls"`
}

// TLSEnabled reports whether both TLS files are configured.
func (s Serve) TLSEnabled() bool { return s.TLSCert != "" && s.TLSKey != "" }

// WithSecrets returns a shallow copy of c whose Secrets are s; c itself is
// not modified. Every other field of the copy is shared with c (slices and
// optional-value pointers included) and is read-only after loading.
//
// This is the per-request credential overlay of serve mode (X-10): the copy
// lives only as long as the tool call that asked for it, so a credential is
// never kept after the call returns.
func (c *Config) WithSecrets(s Secrets) *Config {
	cp := *c
	cp.Secrets = s
	return &cp
}

// IsLoopbackHost reports whether host (without a port) is a loopback name or
// address: "localhost" (any case), 127.0.0.0/8 or ::1. An empty host and
// unspecified addresses such as 0.0.0.0 are not loopback.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SplitListen splits a serve.listen value into host and port. The port must
// be a decimal number from 1 to 65535.
func SplitListen(listen string) (host string, port int, ok bool) {
	h, p, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
		return "", 0, false
	}
	return h, n, true
}

func isServeKey(key string) bool { return strings.HasPrefix(key, "serve.") }

// Fixed sentence prefix of the serve-mode refusal of environment provider
// tokens (P6 spec §1.2).
const serveEnvTokenSentence = "serve mode takes provider tokens from request headers; unset "

func (l *loader) refuseServeEnvToken(s Secret, env string) {
	if s.IsSet() {
		l.problem("%s%s", serveEnvTokenSentence, env)
	}
}

// ServeRepoContextSentence is the fixed sentence of the serve-mode refusal
// of repository context (RC-1, v1.1 spec §3.0 item 4): a shared server would
// hold code fetched with one user's token, and another user could receive it
// as context.
const ServeRepoContextSentence = "context.repo.enabled: repository context is not available in serve mode " +
	"(cached code fetched with one user's token must not reach another user); unset REVIEW_MCP_CONTEXT_REPO_ENABLED"

// validateServe applies the serve-mode rules of P6 spec §1.2. The provider
// token rules live in validateProviders.
func (l *loader) validateServe() {
	s := &l.cfg.Serve
	sec := &l.cfg.Secrets

	if l.cfg.Context.Repo.Enabled {
		l.problem("%s", ServeRepoContextSentence)
	}

	// Listen address and the non-loopback rule.
	host, _, listenOK := SplitListen(s.Listen)
	if !l.bad["serve.listen"] && !listenOK {
		l.problem("serve.listen: %q must be host:port with a port from 1 to 65535 (%s)", s.Listen, keyToEnv["serve.listen"])
	}

	certSet, keySet := s.TLSCert != "", s.TLSKey != ""
	if certSet != keySet {
		l.problem("serve.tls_cert and serve.tls_key must be set together")
	}
	if certSet {
		l.checkReadable("serve.tls_cert", s.TLSCert, "TLS certificate")
	}
	if keySet {
		l.checkReadable("serve.tls_key", s.TLSKey, "TLS key")
	}
	if tlsConfigured := certSet && keySet; listenOK && !IsLoopbackHost(host) && !tlsConfigured && !s.AllowInsecureHTTP {
		l.problem("serve.listen: %q is not a loopback address; set serve.tls_cert and serve.tls_key, "+
			"or set serve.allow_insecure_http = true behind a TLS-terminating proxy", s.Listen)
	}
	if s.AllowInsecureHTTP {
		l.warn("serve.allow_insecure_http is true: requests are served over plain HTTP; use it only behind a TLS-terminating proxy")
	}

	// LLM key source and the secrets it requires or forbids.
	switch s.LLMKeySource {
	case LLMKeySourceHeader:
		if sec.LLMAPIKey.IsSet() {
			l.problem("serve mode with serve.llm_key_source = header takes the LLM API key from request headers; unset %s", envLLMAPIKey)
		}
	case LLMKeySourceServer:
		if !sec.LLMAPIKey.IsSet() {
			l.problem("%s is required because serve.llm_key_source is server", envLLMAPIKey)
		}
		if !sec.ServeAccessToken.IsSet() {
			l.problem("%s is required because serve.llm_key_source is server", envServeAccessToken)
		}
	default:
		l.problem("serve.llm_key_source: %q must be %q or %q", s.LLMKeySource, LLMKeySourceHeader, LLMKeySourceServer)
	}

	// The index is reported instead of the value: an origin with embedded
	// credentials must not be echoed.
	for i, o := range s.AllowedOrigins {
		if !ValidOrigin(o) {
			l.problem("serve.allowed_origins[%d]: not an exact origin such as https://app.example.com (scheme, host and optional port only)", i)
		}
	}

	if n := s.MaxConcurrentCalls; !l.bad["serve.max_concurrent_calls"] && (n < MinServeConcurrentCalls || n > MaxServeConcurrentCalls) {
		l.problem("serve.max_concurrent_calls: %d is out of range (%d-%d)", n, MinServeConcurrentCalls, MaxServeConcurrentCalls)
	}
}

// ValidOrigin reports whether o is a serialized web origin: http or https,
// a host, an optional port, and nothing else (no path, query, fragment or
// userinfo, no trailing slash). The Origin check compares strings exactly,
// so the scheme and host must be lower case as browsers send them.
func ValidOrigin(o string) bool {
	u, err := url.Parse(o)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Opaque != "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(o, "?#") {
		return false
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return o == strings.ToLower(o)
}

// checkReadable reports a problem unless path names a readable regular file.
func (l *loader) checkReadable(key, path, what string) {
	info, err := l.src.Stat(path)
	if err != nil {
		l.problem("%s: cannot read %s file %q: %s", key, what, path, reason(err))
		return
	}
	if !info.Mode().IsRegular() {
		l.problem("%s: %q is not a regular file", key, path)
		return
	}
	if _, err := l.src.ReadFile(path); err != nil {
		l.problem("%s: cannot read %s file %q: %s", key, what, path, reason(err))
	}
}
