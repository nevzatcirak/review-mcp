package config

import "strings"

// providerEnabled reports whether any git provider has a base URL. It is the
// one definition of "a provider is enabled" shared by validation and
// LocalOnly. Validation trims the URLs before it asks; so does this.
func (c *Config) providerEnabled() bool {
	for _, u := range []string{c.Gitea.BaseURL, c.BitbucketServer.BaseURL, c.GitHub.BaseURL, c.GitLab.BaseURL} {
		if strings.TrimSpace(u) != "" {
			return true
		}
	}
	return false
}

// LocalOnly reports a local-only configuration (WP-24d): no git provider is
// enabled and local.enabled is true. It is derived, not a setting, and this
// is the one place that derives it.
func (c *Config) LocalOnly() bool {
	return c != nil && c.Local.Enabled && !c.providerEnabled()
}

// noProviderProblem is the problem raised when no provider is enabled where
// one is required.
func noProviderProblem() string {
	return "no provider enabled: set gitea.base_url (" + keyToEnv["gitea.base_url"] +
		"), bitbucket_server.base_url (" + keyToEnv["bitbucket_server.base_url"] +
		"), github.base_url (" + keyToEnv["github.base_url"] +
		") and/or gitlab.base_url (" + keyToEnv["gitlab.base_url"] + ")"
}

// RequireProvider is called by every entry point that reads a pull request
// (ci and the diag subcommands that resolve one) right after the
// configuration loads. A local-only configuration is valid for stdio and
// "local review" but cannot serve those: it returns the same "no provider
// enabled" sentence validation uses, as a *ValidationError.
func RequireProvider(c *Config) error {
	if c.LocalOnly() {
		return &ValidationError{Problems: []string{sanitize(noProviderProblem())}}
	}
	return nil
}
