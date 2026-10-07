package provider

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

var allClasses = []ErrorClass{
	ClassURLNotConfigured, ClassURLMalformed, ClassAuth, ClassNotFound, ClassRateLimited,
	ClassUpstream, ClassTooLarge, ClassUnsupportedVersion, ClassTransport, ClassProtocol,
	ClassNotOwner, ClassConflict,
}

var sentinels = map[ErrorClass]*Error{
	ClassURLNotConfigured: ErrURLNotConfigured, ClassURLMalformed: ErrURLMalformed, ClassAuth: ErrAuth,
	ClassNotFound: ErrNotFound, ClassRateLimited: ErrRateLimited, ClassUpstream: ErrUpstream,
	ClassTooLarge: ErrTooLarge, ClassUnsupportedVersion: ErrUnsupportedVersion,
	ClassTransport: ErrTransport, ClassProtocol: ErrProtocol,
	ClassNotOwner: ErrNotOwner, ClassConflict: ErrConflict,
}

func TestErrorFormat(t *testing.T) {
	e := &Error{Class: ClassNotFound, Status: 404, Hint: "gitea.base_url"}
	if got, want := e.Error(), "the requested resource was not found (HTTP 404): gitea.base_url"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := (&Error{Class: ClassAuth}).Error(); got != "authentication failed: check the token and its scopes" {
		t.Fatalf("got %q", got)
	}
}

func TestErrorsIsPerClass(t *testing.T) {
	for _, c := range allClasses {
		err := fmt.Errorf("wrapped: %w", &Error{Class: c, Status: 418, Hint: "x"})
		if !errors.Is(err, sentinels[c]) {
			t.Errorf("%s: errors.Is with own sentinel failed", c)
		}
		for _, o := range allClasses {
			if o != c && errors.Is(err, sentinels[o]) {
				t.Errorf("%s matched sentinel of %s", c, o)
			}
		}
	}
}

// [canary] Error() of every class, built the way the producers build them
// (including from hostile transport errors), never contains the token, a
// response-body marker or a query value.
func TestErrorStringsNeverLeak(t *testing.T) {
	const (
		token  = "TOKEN-9f3a" //nolint:gosec // test fixture, not a credential
		marker = "BODY-MARKER-7c1e"
		qval   = "QUERYVAL-55aa"
	)
	hostile := &url.Error{Op: "Get", URL: "https://example.com/x?secret=" + qval + "&t=" + token, Err: errors.New(marker)}
	var errs []error
	for _, c := range allClasses {
		errs = append(errs, &Error{Class: c}, &Error{Class: c, Status: 500, Hint: "diff.max_file_bytes"})
	}
	for _, s := range []int{400, 401, 403, 404, 429, 500, 503} {
		errs = append(errs, StatusError(s))
	}
	errs = append(errs, TransportError(hostile), TransportError(fmt.Errorf("%w: %w", context.DeadlineExceeded, hostile)))
	for _, err := range errs {
		for _, bad := range []string{token, marker, qval} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("%q leaks %q", err.Error(), bad)
			}
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

func TestTransportErrorHints(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"net timeout", &url.Error{Op: "Get", URL: "https://example.com/?a=b", Err: timeoutErr{}}, "timeout"},
		{"unknown authority", &url.Error{Err: x509.UnknownAuthorityError{}}, "TLS verification failed"},
		{"hostname", &url.Error{Err: x509.HostnameError{Host: "h"}}, "TLS verification failed"},
		{"record header", tls.RecordHeaderError{Msg: "m"}, "TLS verification failed"},
		{"cert verification", &tls.CertificateVerificationError{Err: errors.New("x")}, "TLS verification failed"},
		{"dns", &net.DNSError{Err: "no such host", Name: "h"}, "DNS lookup failed"},
		{"refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, "connection failed"},
		{"other", errors.New("boom"), "connection failed"},
		{"canceled", context.Canceled, "canceled"},
	}
	for _, tc := range cases {
		e := TransportError(tc.err)
		if e.Class != ClassTransport || e.Hint != tc.want {
			t.Errorf("%s: got class=%s hint=%q, want %q", tc.name, e.Class, e.Hint, tc.want)
		}
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := map[int]ErrorClass{401: ClassAuth, 403: ClassAuth, 404: ClassNotFound, 429: ClassRateLimited,
		500: ClassUpstream, 502: ClassUpstream, 400: ClassProtocol, 409: ClassProtocol, 302: ClassProtocol}
	for s, want := range cases {
		if got, ok := ClassifyStatus(s); !ok || got != want {
			t.Errorf("status %d: got %q ok=%v, want %q", s, got, ok, want)
		}
	}
	for _, s := range []int{200, 201, 204} {
		if _, ok := ClassifyStatus(s); ok || StatusError(s) != nil {
			t.Errorf("status %d should not be an error", s)
		}
	}
}
