// Package credentials is the serve-mode request credential contract (P6 spec
// §1.3, X-10): which headers carry which credential, how a value is checked,
// and how a request's headers become the Secrets of one tool call.
//
// Nothing here stores a value. Every function reads the header map it is
// given and returns; the caller owns the result for the duration of one
// request only.
package credentials

import (
	"net/http"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// Request headers that carry credentials.
const (
	HeaderGiteaToken           = "X-Review-MCP-Gitea-Token"            //nolint:gosec // G101 false positive: a header name, not a credential
	HeaderBitbucketServerToken = "X-Review-MCP-Bitbucket-Server-Token" //nolint:gosec // G101 false positive: a header name, not a credential
	HeaderLLMAPIKey            = "X-Review-MCP-LLM-API-Key"            //nolint:gosec // G101 false positive: a header name, not a credential
	HeaderAuthorization        = "Authorization"
)

// MaxValueBytes is the longest accepted credential value, after trimming.
const MaxValueBytes = 4096

// ToolHeaders are the headers that carry a tool credential, in the order
// they are checked.
var ToolHeaders = []string{HeaderGiteaToken, HeaderBitbucketServerToken, HeaderLLMAPIKey}

// MalformedError reports a credential header whose value failed the checks.
// Its text names the header and never contains the value.
type MalformedError struct {
	Header string
}

// Error returns the fixed sentence "malformed credential header: <name>".
func (e *MalformedError) Error() string { return "malformed credential header: " + e.Header }

// UserMessage is the sentence an entry point may show to a client (X-6).
func (e *MalformedError) UserMessage() string { return e.Error() }

// Value returns the credential carried by header name in h.
//
// The value is trimmed of spaces and tabs; an empty value means absent
// ("", nil). A value longer than MaxValueBytes, one containing any byte
// outside visible ASCII (0x21-0x7E), or a header sent more than once is
// rejected with a *MalformedError.
func Value(h http.Header, name string) (string, error) {
	vals := h.Values(name)
	switch len(vals) {
	case 0:
		return "", nil
	case 1:
	default:
		// DESIGN-QUESTION: is a credential header sent twice malformed? —
		// chose malformed because picking one of two values silently could
		// act under the wrong identity.
		return "", &MalformedError{Header: name}
	}
	v := strings.Trim(vals[0], " \t")
	if v == "" {
		return "", nil
	}
	if len(v) > MaxValueBytes || !visibleASCII(v) {
		return "", &MalformedError{Header: name}
	}
	return v, nil
}

func visibleASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// Check validates every tool credential header of h, in ToolHeaders order,
// and returns the first *MalformedError. It keeps nothing.
func Check(h http.Header) error {
	for _, name := range ToolHeaders {
		if _, err := Value(h, name); err != nil {
			return err
		}
	}
	return nil
}

// Secrets builds the Secrets of one tool call from the request headers h.
// The provider tokens always come from h. The LLM API key comes from h, or,
// when serverKeySource is true (serve.llm_key_source = server), it is
// serverKey and the LLM header is ignored. accessToken is carried over
// unchanged so that a per-call configuration reports it like the startup
// one; no tool reads it.
func Secrets(h http.Header, serverKeySource bool, serverKey, accessToken config.Secret) (config.Secrets, error) {
	gitea, err := Value(h, HeaderGiteaToken)
	if err != nil {
		return config.Secrets{}, err
	}
	bbs, err := Value(h, HeaderBitbucketServerToken)
	if err != nil {
		return config.Secrets{}, err
	}
	key := serverKey
	if !serverKeySource {
		v, err := Value(h, HeaderLLMAPIKey)
		if err != nil {
			return config.Secrets{}, err
		}
		key = config.NewSecret(v)
	}
	return config.Secrets{
		LLMAPIKey:            key,
		GiteaToken:           config.NewSecret(gitea),
		BitbucketServerToken: config.NewSecret(bbs),
		ServeAccessToken:     accessToken,
	}, nil
}

// Presence reports, for each credential header of §1.3 (the three tool
// headers and Authorization), "set" when it carries a well-formed non-empty
// value, "unset" when it is absent or empty, and "malformed" otherwise. It
// never returns a value.
func Presence(h http.Header) map[string]string {
	out := make(map[string]string, len(ToolHeaders)+1)
	for _, name := range ToolHeaders {
		v, err := Value(h, name)
		switch {
		case err != nil:
			out[name] = "malformed"
		case v == "":
			out[name] = "unset"
		default:
			out[name] = "set"
		}
	}
	out[HeaderAuthorization] = "unset"
	if strings.TrimSpace(h.Get(HeaderAuthorization)) != "" {
		out[HeaderAuthorization] = "set"
	}
	return out
}
