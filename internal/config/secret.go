package config

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

const (
	redactedText = "[REDACTED]"
	unsetText    = "[UNSET]"
)

// Secret holds a credential. Every formatting, logging and serialization path
// yields "[REDACTED]" (set) or "[UNSET]" (unset); the only way to read the
// value is Reveal.
type Secret struct {
	value string
}

// NewSecret wraps v. An empty v yields an unset Secret.
func NewSecret(v string) Secret { return Secret{value: v} }

// Reveal returns the raw secret. Call it only at the point of use (for
// example when building an Authorization header) and never log the result.
func (s Secret) Reveal() string { return s.value }

// IsSet reports whether a non-empty value is held.
func (s Secret) IsSet() bool { return s.value != "" }

// String implements fmt.Stringer.
func (s Secret) String() string {
	if s.IsSet() {
		return redactedText
	}
	return unsetText
}

// GoString implements fmt.GoStringer (%#v).
func (s Secret) GoString() string { return s.String() }

// Format implements fmt.Formatter: every verb prints the placeholder.
func (s Secret) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, s.String())
}

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// MarshalText implements encoding.TextMarshaler.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Secrets holds every credential. In stdio mode they come from the
// environment only. In serve mode the provider tokens (and, with
// serve.llm_key_source = header, the LLM API key) come from the headers of
// each HTTP request instead, through Config.WithSecrets (X-10).
type Secrets struct {
	LLMAPIKey            Secret `json:"llm_api_key"`
	GiteaToken           Secret `json:"gitea_token"`
	BitbucketServerToken Secret `json:"bitbucket_server_token"`
	GitHubToken          Secret `json:"github_token"`
	// ServeAccessToken is the serve-mode access token
	// (REVIEW_MCP_SERVE_ACCESS_TOKEN). Only the HTTP middleware reads it.
	ServeAccessToken Secret `json:"serve_access_token"`
}
