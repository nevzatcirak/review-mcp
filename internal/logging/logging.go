// Package logging configures structured logging and provides redaction
// helpers. Logs go to stderr only: stdout belongs to the MCP protocol.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

const (
	redacted       = "REDACTED"
	unparseableURL = "<unparseable URL>"
)

// New returns a text-format logger writing to w at the given level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// ParseLevel parses debug|info|warn|error (case-insensitive).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q: want debug, info, warn or error", s)
	}
}

// RedactURL removes userinfo and fragment from raw and replaces every
// query-parameter value with REDACTED. A query item without '=' can itself be
// a token, so it is replaced by REDACTED as a whole; empty items stay empty.
// Opaque URLs (for example "user:s3cret@host" or "mailto:a:b@example.com")
// are reduced to "<scheme>:REDACTED". Input that has neither scheme nor host
// but contains '@' (for example "s3cret@host/path") is returned as the bare
// REDACTED constant. On parse failure it returns "<unparseable URL>". In none
// of these cases is the redacted part of the input ever returned.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return unparseableURL
	}
	if u.Opaque != "" {
		return u.Scheme + ":" + redacted
	}
	if u.Host == "" && u.Scheme == "" && strings.Contains(raw, "@") {
		return redacted
	}
	u.User = nil
	// Fragments are dropped: they can carry tokens (implicit-grant style) and
	// are never needed in logs.
	u.Fragment = ""
	u.RawFragment = ""
	if u.RawQuery != "" {
		parts := strings.Split(u.RawQuery, "&")
		for i, p := range parts {
			switch k, _, ok := strings.Cut(p, "="); {
			case ok:
				parts[i] = k + "=" + redacted
			case p != "":
				parts[i] = redacted
			}
		}
		u.RawQuery = strings.Join(parts, "&")
	}
	return u.String()
}

var (
	// scheme://userinfo@ — userinfo runs up to the last '@' before the path.
	userinfoRE = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/?#]*@`)
	// Authorization: <scheme> <credentials> | Authorization=<value>; also
	// matches quoted forms such as JSON "Authorization": "Bearer x".
	authHeaderRE = regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?)(?:(?:bearer|token|basic|digest)\s+)?[^\s,;"']+`)
)

// RedactText masks URL userinfo and Authorization header values in s.
func RedactText(s string) string {
	s = userinfoRE.ReplaceAllString(s, "${1}"+redacted+"@")
	s = authHeaderRE.ReplaceAllString(s, "${1}"+redacted)
	return s
}
