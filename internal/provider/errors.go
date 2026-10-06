package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strconv"
)

// ErrorClass is the X-6 allowlist class of a provider error.
type ErrorClass string

// Error classes.
const (
	ClassURLNotConfigured   ErrorClass = "url_not_configured"
	ClassURLMalformed       ErrorClass = "url_malformed"
	ClassAuth               ErrorClass = "auth"
	ClassNotFound           ErrorClass = "not_found"
	ClassRateLimited        ErrorClass = "rate_limited"
	ClassUpstream           ErrorClass = "upstream"
	ClassTooLarge           ErrorClass = "too_large"
	ClassUnsupportedVersion ErrorClass = "unsupported_version"
	ClassTransport          ErrorClass = "transport"
	ClassProtocol           ErrorClass = "protocol"
)

// Class sentinels for errors.Is.
var (
	ErrURLNotConfigured   = &Error{Class: ClassURLNotConfigured}
	ErrURLMalformed       = &Error{Class: ClassURLMalformed}
	ErrAuth               = &Error{Class: ClassAuth}
	ErrNotFound           = &Error{Class: ClassNotFound}
	ErrRateLimited        = &Error{Class: ClassRateLimited}
	ErrUpstream           = &Error{Class: ClassUpstream}
	ErrTooLarge           = &Error{Class: ClassTooLarge}
	ErrUnsupportedVersion = &Error{Class: ClassUnsupportedVersion}
	ErrTransport          = &Error{Class: ClassTransport}
	ErrProtocol           = &Error{Class: ClassProtocol}
)

// Error is a sanitized provider error. It never carries response bodies,
// URLs with query strings or header values; Hint is a config key, an env var
// name or a short fixed phrase.
type Error struct {
	Class  ErrorClass
	Status int
	Hint   string
}

var sentences = map[ErrorClass]string{
	ClassURLNotConfigured:   "the pull request URL does not match any configured provider",
	ClassURLMalformed:       "the URL matches a configured provider but is not a pull request URL",
	ClassAuth:               "authentication failed: check the token and its scopes",
	ClassNotFound:           "the requested resource was not found",
	ClassRateLimited:        "the server rate-limited the request",
	ClassUpstream:           "the server reported an internal error",
	ClassTooLarge:           "a response exceeded its size limit",
	ClassUnsupportedVersion: "the server version is not supported",
	ClassTransport:          "could not complete the request to the server",
	ClassProtocol:           "the server sent an unexpected response",
}

// Error returns the fixed sentence for the class, then " (HTTP n)" when
// Status is non-zero, then ": <Hint>" when Hint is set.
func (e *Error) Error() string {
	s, ok := sentences[e.Class]
	if !ok {
		s = "provider error"
	}
	if e.Status != 0 {
		s += " (HTTP " + strconv.Itoa(e.Status) + ")"
	}
	if e.Hint != "" {
		s += ": " + e.Hint
	}
	return s
}

// Is reports whether target is a *Error of the same class. Status and Hint
// are ignored, so errors.Is(err, provider.ErrAuth) works for any auth error.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Class == e.Class
}

// ClassifyStatus maps a non-2xx HTTP status to an error class. It is the one
// status-mapping function shared by all providers: 401/403 auth, 404
// not_found, 429 rate_limited, 5xx upstream; any other non-2xx status
// (including 3xx that was not followed and 4xx such as 400/409/422) is
// protocol. A 2xx status yields ok=false.
func ClassifyStatus(status int) (class ErrorClass, ok bool) {
	switch {
	case status >= 200 && status < 300:
		return "", false
	case status == 401 || status == 403:
		return ClassAuth, true
	case status == 404:
		return ClassNotFound, true
	case status == 429:
		return ClassRateLimited, true
	case status >= 500:
		return ClassUpstream, true
	default:
		return ClassProtocol, true
	}
}

// StatusError builds the sanitized error for a non-2xx status, or nil for a
// 2xx status. The response body is never consulted.
func StatusError(status int) *Error {
	class, ok := ClassifyStatus(status)
	if !ok {
		return nil
	}
	return &Error{Class: class, Status: status}
}

// TransportError classifies a transport-level failure. The returned error
// carries only a short fixed hint; err.Error() is never included because
// Go's *url.Error embeds the full request URL, query string included.
func TransportError(err error) *Error {
	hint := "connection failed"
	var (
		dnsErr   *net.DNSError
		netErr   net.Error
		unknown  x509.UnknownAuthorityError
		hostErr  x509.HostnameError
		certErr  x509.CertificateInvalidError
		recErr   tls.RecordHeaderError
		verifErr *tls.CertificateVerificationError
	)
	switch {
	case errors.Is(err, context.Canceled):
		hint = "canceled"
	case errors.As(err, &verifErr), errors.As(err, &unknown), errors.As(err, &hostErr),
		errors.As(err, &certErr), errors.As(err, &recErr):
		hint = "TLS verification failed"
	case errors.Is(err, context.DeadlineExceeded):
		hint = "timeout"
	case errors.As(err, &dnsErr):
		if dnsErr.IsTimeout {
			hint = "timeout"
		} else {
			hint = "DNS lookup failed"
		}
	case errors.As(err, &netErr) && netErr.Timeout():
		hint = "timeout"
	}
	return &Error{Class: ClassTransport, Hint: hint}
}
