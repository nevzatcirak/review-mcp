package tools

import (
	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// RequestErrorClass is the X-6 allowlist class of a serve-mode request
// error.
type RequestErrorClass string

// Serve-mode request error classes (P6 spec §1.3, §1.4).
const (
	// ClassCredentialsMissing: the call needs a credential the request did
	// not carry. Nothing was sent anywhere.
	ClassCredentialsMissing RequestErrorClass = "credentials_missing"
	// ClassServerBusy: every tool-call slot was taken; the call did not run.
	ClassServerBusy RequestErrorClass = "server_busy"
)

// Fixed sentences of the serve-mode request errors (X-6). They name the
// header to set and never contain a value.
const (
	MissingGiteaTokenMessage           = "no Gitea token in this request: set the X-Review-MCP-Gitea-Token header in your MCP client configuration"                       //nolint:gosec // G101 false positive: a fixed user-facing sentence, not a credential
	MissingBitbucketServerTokenMessage = "no Bitbucket Server token in this request: set the X-Review-MCP-Bitbucket-Server-Token header in your MCP client configuration" //nolint:gosec // G101 false positive: a fixed user-facing sentence, not a credential
	MissingLLMAPIKeyMessage            = "no LLM API key in this request: set the X-Review-MCP-LLM-API-Key header in your MCP client configuration"                       //nolint:gosec // G101 false positive: a fixed user-facing sentence, not a credential
	ServerBusyMessage                  = "the server is busy: retry shortly"
)

// RequestError is a serve-mode request error. Its text is one of the fixed
// sentences above.
type RequestError struct {
	Class   RequestErrorClass
	Message string
}

// Error returns the fixed sentence (the class name for a bare sentinel).
func (e *RequestError) Error() string {
	if e.Message == "" {
		return string(e.Class)
	}
	return e.Message
}

// UserMessage is the sentence an entry point may show to a client (X-6).
func (e *RequestError) UserMessage() string { return e.Error() }

// Is reports whether target is a *RequestError of the same class.
func (e *RequestError) Is(target error) bool {
	t, ok := target.(*RequestError)
	return ok && t.Class == e.Class
}

// Class sentinels and the fixed errors.
var (
	ErrCredentialsMissing = &RequestError{Class: ClassCredentialsMissing}
	ErrServerBusy         = &RequestError{Class: ClassServerBusy, Message: ServerBusyMessage}

	errMissingGitea = &RequestError{Class: ClassCredentialsMissing, Message: MissingGiteaTokenMessage}
	errMissingBBS   = &RequestError{Class: ClassCredentialsMissing, Message: MissingBitbucketServerTokenMessage}
	errMissingLLM   = &RequestError{Class: ClassCredentialsMissing, Message: MissingLLMAPIKeyMessage}
)

// MissingLLMKey returns the credentials_missing error of the LLM API key.
func MissingLLMKey() error { return errMissingLLM }

// RequireCredentials wraps the resolver of one serve-mode tool call. After
// the inner resolver has matched the URL (which performs no I/O), it fails
// with credentials_missing when cfg lacks the token of the matched provider
// or, when needLLM is set, the LLM API key. The provider is then dropped
// unused, so no outbound request is made (P6 spec §1.3).
//
// cfg is the request-scoped configuration of this call; the wrapper is built
// per call and kept by nobody.
func RequireCredentials(inner PRResolver, cfg *config.Config, needLLM bool) PRResolver {
	return credentialResolver{inner: inner, cfg: cfg, needLLM: needLLM}
}

type credentialResolver struct {
	inner   PRResolver
	cfg     *config.Config
	needLLM bool
}

func (r credentialResolver) Resolve(rawURL string) (provider.PRRef, provider.Provider, error) {
	ref, p, err := r.inner.Resolve(rawURL)
	if err != nil {
		return ref, p, err
	}
	if err := missingCredential(r.cfg, ref.Kind, r.needLLM); err != nil {
		return provider.PRRef{}, nil, err
	}
	return ref, p, nil
}

// missingCredential returns the credentials_missing error of the first
// credential the call lacks: the provider token, then the LLM API key.
func missingCredential(cfg *config.Config, kind provider.Kind, needLLM bool) error {
	if cfg == nil {
		cfg = &config.Config{} // no configuration carries no credential
	}
	switch kind {
	case provider.KindGitea:
		if !cfg.Secrets.GiteaToken.IsSet() {
			return errMissingGitea
		}
	case provider.KindBitbucketServer:
		if !cfg.Secrets.BitbucketServerToken.IsSet() {
			return errMissingBBS
		}
	}
	if needLLM && !cfg.Secrets.LLMAPIKey.IsSet() {
		return errMissingLLM
	}
	return nil
}
