package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/logging"
)

// GitHub tokens of the identity tests: an installation token, the shape of
// a workflow's job token, and a personal access token.
const (
	ciInstallToken = "ghs_FAKE-installation-token-QX42" //nolint:gosec // synthetic test value
	ciPATToken     = "ghp_FAKE-personal-token-QX42"     //nolint:gosec // synthetic test value
)

// ciIdentityLine is the info log of the GitHub Actions bot default.
const ciIdentityLine = "using the GitHub Actions bot identity"

// TestCISelfLoginDefault: on GitHub Actions with an installation token and
// github.self_login unset, ci names the GitHub Actions bot as its own login
// and logs it; a configured login is kept, and Gitea Actions, GitLab CI, no
// CI, a personal access token and a disabled GitHub get no default (V-4 §4).
func TestCISelfLoginDefault(t *testing.T) {
	gh := map[string]string{"GITHUB_ACTIONS": "true"}
	for _, tc := range []struct {
		name, set, token string
		noGitHub         bool
		vars             map[string]string
		want             string
	}{
		{name: "github actions", token: ciInstallToken, vars: gh, want: ciGitHubActionsBot},
		{name: "configured login kept", set: "octo-app[bot]", token: ciInstallToken, vars: gh, want: "octo-app[bot]"},
		{name: "personal access token", token: ciPATToken, vars: gh},
		{name: "gitea actions", token: ciInstallToken, vars: with(gh, "GITEA_ACTIONS", "true")},
		{name: "gitlab ci", token: ciInstallToken, vars: map[string]string{"GITLAB_CI": "true"}},
		{name: "no ci", token: ciInstallToken},
		{name: "github not enabled", token: ciInstallToken, noGitHub: true, vars: gh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			if !tc.noGitHub {
				cfg.GitHub.BaseURL = "https://github.example.com"
			}
			cfg.GitHub.SelfLogin = tc.set
			cfg.Secrets.GitHubToken = config.NewSecret(tc.token)
			var logs bytes.Buffer
			ciSelfLoginDefault(cfg, newCIVars(tc.vars).get, logging.New(&logs, logLevel(cfg, nil)), "ci review: ")
			if cfg.GitHub.SelfLogin != tc.want {
				t.Errorf("self_login = %q, want %q", cfg.GitHub.SelfLogin, tc.want)
			}
			logged := strings.Contains(logs.String(), "ci review: "+ciIdentityLine)
			if applied := tc.want == ciGitHubActionsBot; logged != applied {
				t.Errorf("logged %v, applied %v:\n%s", logged, applied, logs.String())
			}
			if strings.Contains(logs.String(), "QX42") {
				t.Error("the log leaks the token")
			}
		})
	}
}

// TestCIRunUsesBotIdentity: runCI applies the default from the pipeline's
// variables, before the run, and only on GitHub Actions.
func TestCIRunUsesBotIdentity(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	env := reviewDiagEnv(g, l)
	env["REVIEW_MCP_GITHUB_BASE_URL"], env["REVIEW_MCP_GITHUB_TOKEN"] = "https://github.example.com", ciInstallToken
	for _, tc := range []struct {
		name string
		vars map[string]string
		want bool
	}{
		{"github actions", map[string]string{"GITHUB_ACTIONS": "true"}, true},
		{"gitea actions", map[string]string{"GITHUB_ACTIONS": "true", "GITEA_ACTIONS": "true"}, false},
		{"no ci", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ciCall(t, env, tc.vars, "review", "--pr-url", prURLOf(g, 8))
			if r.code != 0 {
				t.Fatalf("exit %d; stderr:\n%s", r.code, r.errs)
			}
			if got := strings.Contains(r.errs, "ci review: "+ciIdentityLine); got != tc.want {
				t.Errorf("bot identity logged %v, want %v:\n%s", got, tc.want, r.errs)
			}
			if strings.Contains(r.out+r.errs, ciInstallToken) {
				t.Error("the output leaks the GitHub token")
			}
			assertCIClean(t, r)
		})
	}
}

// TestStdioNoBotIdentity [canary]: the stdio server never applies the
// GitHub Actions bot default, even inside a GitHub Actions job with an
// installation token: the bot login is a ci default only (V-4 §4 item 2).
func TestStdioNoBotIdentity(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITEA_ACTIONS", "")
	env := validEnv()
	env["REVIEW_MCP_GITHUB_TOKEN"] = ciInstallToken
	var cfg *config.Config
	load := func() (*config.Config, *config.Report, error) {
		c, rep, err := loaderFor(env)()
		cfg = c
		return c, rep, err
	}
	var out, errb bytes.Buffer
	if code := runWith(nil, strings.NewReader(""), &out, &errb, load); code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errb.String())
	}
	if cfg == nil {
		t.Fatal("the configuration was not loaded")
	}
	if cfg.GitHub.SelfLogin != "" {
		t.Errorf("stdio set github.self_login to %q", cfg.GitHub.SelfLogin)
	}
	if strings.Contains(errb.String(), ciIdentityLine) {
		t.Errorf("stdio logged the bot identity:\n%s", errb.String())
	}
}
