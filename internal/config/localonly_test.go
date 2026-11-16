package config

import (
	"errors"
	"testing"
)

// llmOnlyEnv is a configuration with only the llm.* settings.
func llmOnlyEnv() map[string]string {
	env := minimalEnv()
	delete(env, "REVIEW_MCP_GITEA_BASE_URL")
	delete(env, "REVIEW_MCP_GITEA_TOKEN")
	return env
}

const noProviderSentence = "no provider enabled: set gitea.base_url (REVIEW_MCP_GITEA_BASE_URL), bitbucket_server.base_url (REVIEW_MCP_BITBUCKET_SERVER_BASE_URL), " +
	"github.base_url (REVIEW_MCP_GITHUB_BASE_URL) and/or gitlab.base_url (REVIEW_MCP_GITLAB_BASE_URL)"

// TestLocalOnlyConfigurationLoads: [canary] (n). Only llm.* is set: the
// configuration is valid and LocalOnly is true. Mutation: raise the "no
// provider enabled" problem unconditionally again -> Load fails.
func TestLocalOnlyConfigurationLoads(t *testing.T) {
	cfg, rep, err := Load(MemSource{Env: llmOnlyEnv()})
	if err != nil {
		t.Fatalf("a configuration with only llm.* must load: %v", err)
	}
	if !cfg.LocalOnly() {
		t.Error("LocalOnly() = false")
	}
	if n := len(cfg.Summary(rep).Providers); n != 0 {
		t.Errorf("Summary lists %d providers", n)
	}
	if err := RequireProvider(cfg); err == nil {
		t.Error("RequireProvider accepted a local-only configuration")
	} else {
		var ve *ValidationError
		if !errors.As(err, &ve) || len(ve.Problems) != 1 || ve.Problems[0] != noProviderSentence {
			t.Errorf("RequireProvider: %v", err)
		}
	}
}

// TestLocalDisabledNeedsProvider: [canary] (o). The same configuration with
// local.enabled = false fails with today's sentence. Mutation: drop the
// local.enabled condition -> Load succeeds and the test fails.
func TestLocalDisabledNeedsProvider(t *testing.T) {
	env := llmOnlyEnv()
	env["REVIEW_MCP_LOCAL_ENABLED"] = "false"
	cfg, _, err := Load(MemSource{Env: env})
	if !hasProblem(problemsOf(t, err), noProviderSentence) {
		t.Fatalf("want %q, got %v", noProviderSentence, err)
	}
	if cfg.LocalOnly() {
		t.Error("LocalOnly() with local.enabled = false")
	}
}

// TestLocalOnlyFalseWithProvider: a provider turns local-only off, and
// RequireProvider then passes.
func TestLocalOnlyFalseWithProvider(t *testing.T) {
	cfg, _ := mustLoad(t, MemSource{Env: minimalEnv()})
	if cfg.LocalOnly() {
		t.Error("LocalOnly() with a provider")
	}
	if err := RequireProvider(cfg); err != nil {
		t.Errorf("RequireProvider: %v", err)
	}
}
