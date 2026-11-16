package main

import (
	"strings"
	"testing"
)

const noProviderPrefix = "no provider enabled: set gitea.base_url"

// localOnlyEnv is a configuration with only llm.* set (WP-24d).
func localOnlyEnv() map[string]string { return diagEnv(nil, nil) }

// TestCILocalOnlyRefused: ci reads a pull request, so a local-only
// configuration exits 1 with the "no provider enabled" sentence, before any
// network call.
func TestCILocalOnlyRefused(t *testing.T) {
	r := ciCall(t, localOnlyEnv(), nil, "review", "--pr-url", "https://gitea.example.invalid/o/r/pulls/1")
	if r.code != 1 || r.out != "" || !strings.Contains(r.errs, noProviderPrefix) {
		t.Errorf("exit %d, stdout %q, stderr:\n%s", r.code, r.out, r.errs)
	}
	if r.loads != 1 {
		t.Errorf("loads = %d", r.loads)
	}
}

// TestDiagLocalOnly lists every diag subcommand with whether it requires a
// provider. Those that do exit 1 with the sentence; diag cache reads no pull
// request and runs.
func TestDiagLocalOnly(t *testing.T) {
	const pr = "https://gitea.example.invalid/o/r/pulls/1"
	for _, tc := range []struct {
		name     string
		args     []string
		provider bool
	}{
		{"pr", []string{"pr", pr}, true},
		{"comment", []string{"comment", pr, "--body", "x"}, true},
		{"comments", []string{"comments", pr}, true},
		{"reply", []string{"reply", pr, "--comment-id", "1", "--body", "x"}, true},
		{"diff", []string{"diff", pr}, true},
		{"review", []string{"review", pr, "--dry-run"}, true},
		{"ask", []string{"ask", pr, "--question", "why", "--dry-run"}, true},
		{"describe", []string{"describe", pr, "--dry-run"}, true},
		{"improve", []string{"improve", pr, "--dry-run"}, true},
		{"list", []string{"list", "authored"}, true},
		{"cache", []string{"cache"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errs := diag(localOnlyEnv(), tc.args...)
			if tc.provider {
				if code != 1 || !strings.Contains(errs, noProviderPrefix) {
					t.Errorf("exit %d, stderr:\n%s", code, errs)
				}
			} else if strings.Contains(errs, noProviderPrefix) {
				t.Errorf("a command that reads no pull request refused:\n%s", errs)
			}
		})
	}
}

// TestStdioLocalOnlyStartup: a local-only stdio start is valid, logs
// providers=[] and then the fixed info line.
func TestStdioLocalOnlyStartup(t *testing.T) {
	code, _, stderr := session(t, nil, localOnlyEnv(), handshake...)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if strings.Contains(stderr, "degraded") || !strings.Contains(stderr, "providers=[]") ||
		!strings.Contains(stderr, "config_valid=true") {
		t.Errorf("startup:\n%s", stderr)
	}
	if !strings.Contains(stderr, "no git provider is configured; only local reviews are available") {
		t.Errorf("startup info line missing:\n%s", stderr)
	}
}

// TestStdioLocalDisabledDegraded: local.enabled = false with no provider
// is still the degraded start with the same sentence.
func TestStdioLocalDisabledDegraded(t *testing.T) {
	env := localOnlyEnv()
	env["REVIEW_MCP_LOCAL_ENABLED"] = "false"
	_, _, stderr := session(t, nil, env, handshake...)
	if !strings.Contains(stderr, "degraded mode") || !strings.Contains(stderr, noProviderPrefix) {
		t.Errorf("stderr:\n%s", stderr)
	}
}
