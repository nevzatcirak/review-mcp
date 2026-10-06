package tools

import (
	"errors"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/credentials"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

type kindResolver struct {
	kind  provider.Kind
	err   error
	calls int
}

func (f *kindResolver) Resolve(string) (provider.PRRef, provider.Provider, error) {
	f.calls++
	if f.err != nil {
		return provider.PRRef{}, nil, f.err
	}
	return provider.PRRef{Kind: f.kind, Number: 1}, nil, nil
}

func secretsCfg(gitea, bbs, llm string) *config.Config {
	return config.Defaults().WithSecrets(config.Secrets{
		GiteaToken: config.NewSecret(gitea), BitbucketServerToken: config.NewSecret(bbs), LLMAPIKey: config.NewSecret(llm),
	})
}

func TestRequireCredentials(t *testing.T) {
	tests := []struct {
		name    string
		kind    provider.Kind
		cfg     *config.Config
		needLLM bool
		want    string // "" means success
	}{
		{"gitea ok", provider.KindGitea, secretsCfg("g", "", ""), false, ""},
		{"gitea missing", provider.KindGitea, secretsCfg("", "b", "k"), true, MissingGiteaTokenMessage},
		{"bbs missing", provider.KindBitbucketServer, secretsCfg("g", "", "k"), false, MissingBitbucketServerTokenMessage},
		{"bbs ok", provider.KindBitbucketServer, secretsCfg("", "b", ""), false, ""},
		{"llm missing", provider.KindGitea, secretsCfg("g", "", ""), true, MissingLLMAPIKeyMessage},
		{"provider before llm", provider.KindGitea, secretsCfg("", "", ""), true, MissingGiteaTokenMessage},
		{"llm not needed", provider.KindGitea, secretsCfg("g", "", ""), false, ""},
		{"nil config", provider.KindBitbucketServer, nil, true, MissingBitbucketServerTokenMessage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := &kindResolver{kind: tc.kind}
			_, _, err := RequireCredentials(inner, tc.cfg, tc.needLLM).Resolve("https://your-gitea.example/o/r/pulls/1")
			if inner.calls != 1 {
				t.Errorf("inner resolver called %d times", inner.calls)
			}
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if !errors.Is(err, ErrCredentialsMissing) || UserMessage(err) != tc.want {
				t.Errorf("err = %v, message %q", err, UserMessage(err))
			}
		})
	}
	// URL resolution comes first: its error wins over a missing credential.
	inner := &kindResolver{err: provider.ErrURLNotConfigured}
	_, _, err := RequireCredentials(inner, secretsCfg("", "", ""), true).Resolve("https://other.example/x")
	if !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Errorf("err = %v, want url_not_configured", err)
	}
}

func TestRequestErrorMessages(t *testing.T) {
	if got := UserMessage(ErrServerBusy); got != "the server is busy: retry shortly" {
		t.Errorf("server_busy = %q", got)
	}
	if !errors.Is(ErrServerBusy, &RequestError{Class: ClassServerBusy}) || errors.Is(ErrServerBusy, ErrCredentialsMissing) {
		t.Error("class matching")
	}
	if got := UserMessage(MissingLLMKey()); got != "no LLM API key in this request: set the X-Review-MCP-LLM-API-Key header in your MCP client configuration" {
		t.Errorf("llm = %q", got)
	}
	if got := UserMessage(&credentials.MalformedError{Header: credentials.HeaderLLMAPIKey}); got != "malformed credential header: X-Review-MCP-LLM-API-Key" {
		t.Errorf("malformed = %q", got)
	}
	if ErrCredentialsMissing.Error() != "credentials_missing" {
		t.Errorf("sentinel text = %q", ErrCredentialsMissing.Error())
	}
}
