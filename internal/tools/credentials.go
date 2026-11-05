package tools

import (
	"slices"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
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
	MissingGitHubTokenMessage          = "no GitHub token in this request: set the X-Review-MCP-GitHub-Token header in your MCP client configuration"                     //nolint:gosec // G101 false positive: a fixed user-facing sentence, not a credential
	MissingGitLabTokenMessage          = "no GitLab token in this request: set the X-Review-MCP-GitLab-Token header in your MCP client configuration"                     //nolint:gosec // G101 false positive: a fixed user-facing sentence, not a credential
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

// ErrorClass is the error's class, for packages that cannot import this one
// (the related pull requests of internal/related).
func (e *RequestError) ErrorClass() string { return string(e.Class) }

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
	errMissingGH    = &RequestError{Class: ClassCredentialsMissing, Message: MissingGitHubTokenMessage}
	errMissingGL    = &RequestError{Class: ClassCredentialsMissing, Message: MissingGitLabTokenMessage}
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

// ResolveItem resolves a related item's URL (v2.5 spec V-13b) through the
// inner resolver, then applies the same credential check as Resolve to the
// matched provider of a pull request or a repository.
func (r credentialResolver) ResolveItem(rawURL string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
	kind, ref, repo, p, err := resolveItem(r.inner, rawURL)
	if err != nil {
		return kind, ref, repo, p, err
	}
	pk := ref.Kind
	if kind == provider.ItemRepository {
		pk = repo.Kind
	}
	if err := missingCredential(r.cfg, pk, r.needLLM); err != nil {
		return "", provider.PRRef{}, provider.RepoRef{}, nil, err
	}
	return kind, ref, repo, p, nil
}

// resolveItem resolves rawURL with r's ResolveItem (related.ItemResolver),
// or, for a resolver without it, as a pull request URL.
func resolveItem(r PRResolver, rawURL string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
	if ir, ok := r.(related.ItemResolver); ok {
		return ir.ResolveItem(rawURL)
	}
	ref, p, err := r.Resolve(rawURL)
	if err != nil {
		return "", provider.PRRef{}, provider.RepoRef{}, nil, err
	}
	return provider.ItemPullRequest, ref, provider.RepoRef{}, p, nil
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
	case provider.KindGitHub:
		if !cfg.Secrets.GitHubToken.IsSet() {
			return errMissingGH
		}
	case provider.KindGitLab:
		if !cfg.Secrets.GitLabToken.IsSet() {
			return errMissingGL
		}
	}
	if needLLM && !cfg.Secrets.LLMAPIKey.IsSet() {
		return errMissingLLM
	}
	return nil
}

// ListSource gives the pull request lists (X-48) the providers to query,
// without a pull request URL. It is implemented by the credential wrapper
// of serve mode and, through ListSourceOf, by *provider.Resolver.
type ListSource interface {
	// EnabledKinds are the enabled and registered provider kinds, in a
	// fixed order. It builds nothing.
	EnabledKinds() []provider.Kind
	// ListProviders builds the providers of kinds, in the fixed kind
	// order, and names the kinds it did not build because the call has no
	// token for them (serve mode; always empty in stdio mode). A kind that
	// is not enabled is ignored.
	ListProviders(kinds []provider.Kind) (providers []provider.Provider, noToken []provider.Kind)
}

// enabledResolver is the part of *provider.Resolver a ListSource needs.
type enabledResolver interface {
	EnabledKinds() []provider.Kind
	EnabledOf(kinds ...provider.Kind) []provider.Provider
}

// ListSourceOf returns the ListSource of r: the credential wrapper itself,
// or a plain adapter of a resolver that enables its providers. It is nil
// for a resolver that does either.
func ListSourceOf(r PRResolver) ListSource {
	switch v := r.(type) {
	case ListSource:
		return v
	case enabledResolver:
		return plainListSource{v}
	}
	return nil
}

// plainListSource is the ListSource of stdio mode: every call has the
// startup tokens, so no kind is skipped.
type plainListSource struct{ r enabledResolver }

func (s plainListSource) EnabledKinds() []provider.Kind { return s.r.EnabledKinds() }

func (s plainListSource) ListProviders(kinds []provider.Kind) ([]provider.Provider, []provider.Kind) {
	return s.r.EnabledOf(kinds...), nil
}

// EnabledKinds returns the inner resolver's enabled kinds; none when the
// inner resolver does not enable providers.
func (r credentialResolver) EnabledKinds() []provider.Kind {
	if in, ok := r.inner.(enabledResolver); ok {
		return in.EnabledKinds()
	}
	return nil
}

// ListProviders builds, through the inner resolver, only the providers of
// the kinds whose token this call carries. A kind without its token is
// returned in noToken and nothing is built for it, so no request can leave
// for it (the list equivalent of the credentials_missing of Resolve).
func (r credentialResolver) ListProviders(kinds []provider.Kind) ([]provider.Provider, []provider.Kind) {
	in, ok := r.inner.(enabledResolver)
	if !ok {
		return nil, nil
	}
	var withToken, noToken []provider.Kind
	for _, k := range in.EnabledKinds() {
		if !slices.Contains(kinds, k) {
			continue
		}
		if missingCredential(r.cfg, k, false) != nil {
			noToken = append(noToken, k)
			continue
		}
		withToken = append(withToken, k)
	}
	return in.EnabledOf(withToken...), noToken
}
