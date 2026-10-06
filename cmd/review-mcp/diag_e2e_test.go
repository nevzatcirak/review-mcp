package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// runBinary runs the built binary with a controlled environment and returns
// its exit code, stdout and stderr.
func runBinary(t *testing.T, bin string, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: bin is the binary this test just built in t.TempDir()
	cmd.Env = envList(env)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return code, out.String(), errb.String()
}

// TestE2EDiagLeak [canary] runs the real binary's diag commands against fake
// providers with debug logging and scans both streams for the tokens and the
// response-body marker (placed in the PR description, commit bodies and error
// bodies), including the error paths.
func TestE2EDiagLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the build-and-run end-to-end test in -short mode")
	}
	bin := buildBinary(t)
	g, b := newFakeGitea(t), newFakeBBS(t)
	env := diagEnv(g, b) // includes REVIEW_MCP_LOG_LEVEL=debug

	check := func(t *testing.T, code int, out, errs string, wantCode int) {
		t.Helper()
		if code != wantCode {
			t.Errorf("exit %d, want %d; stderr:\n%s", code, wantCode, errs)
		}
		assertNoLeak(t, "stdout", out)
		assertNoLeak(t, "stderr", errs)
	}

	t.Run("gitea pr", func(t *testing.T) {
		code, out, errs := runBinary(t, bin, env, "diag", "pr", g.giteaPR(), "--show-patch", "src/app.go")
		check(t, code, out, errs, 0)
		m, rest := decodeReport(t, out)
		if m["kind"] != "gitea" || !strings.HasPrefix(rest, "\n--- patch: src/app.go ---\n@@ ") {
			t.Errorf("stdout = %q", out)
		}
		if !strings.Contains(errs, "level=DEBUG") {
			t.Errorf("expected debug logs on stderr:\n%s", errs)
		}
	})
	t.Run("bitbucket pr", func(t *testing.T) {
		code, out, errs := runBinary(t, bin, env, "diag", "pr", b.bbsPR(), "--show-patch", "src/app.go")
		check(t, code, out, errs, 0)
		if m, _ := decodeReport(t, out); m["kind"] != "bitbucket_server" {
			t.Errorf("stdout = %q", out)
		}
		if !strings.Contains(errs, "level=DEBUG") {
			t.Errorf("expected debug logs on stderr:\n%s", errs)
		}
	})
	t.Run("gitea comment", func(t *testing.T) {
		code, out, errs := runBinary(t, bin, env, "diag", "comment", g.giteaPR(), "--body", "hello")
		check(t, code, out, errs, 0)
		if !strings.Contains(out, `"id": "55"`) {
			t.Errorf("stdout = %q", out)
		}
	})
	t.Run("bitbucket comment", func(t *testing.T) {
		code, out, errs := runBinary(t, bin, env, "diag", "comment", b.bbsPR(), "--body", "hello")
		check(t, code, out, errs, 0)
		if !strings.Contains(out, `"id": "77"`) {
			t.Errorf("stdout = %q", out)
		}
	})
	t.Run("401 from the fake", func(t *testing.T) {
		bad := newFakeGitea(t)
		bad.failStatus.Store(401)
		badBBS := newFakeBBS(t)
		badBBS.failStatus.Store(401)
		for _, tc := range []struct {
			env map[string]string
			url string
		}{
			{diagEnv(bad, nil), bad.giteaPR()},
			{diagEnv(nil, badBBS), badBBS.bbsPR()},
		} {
			code, out, errs := runBinary(t, bin, tc.env, "diag", "pr", tc.url)
			check(t, code, out, errs, 1)
			want := (&provider.Error{Class: provider.ClassAuth, Status: 401}).Error()
			if out != "" || !hasLine(errs, want) {
				t.Errorf("stdout %q; stderr lacks the line %q:\n%s", out, want, errs)
			}
		}
	})
	t.Run("unconfigured host", func(t *testing.T) {
		before := g.requests() + b.requests()
		code, out, errs := runBinary(t, bin, env, "diag", "pr", "https://other.example.net/octo/demo/pulls/7")
		check(t, code, out, errs, 1)
		want := (&provider.Error{Class: provider.ClassURLNotConfigured}).Error()
		found := false
		for _, l := range strings.Split(errs, "\n") {
			if strings.HasPrefix(l, want) {
				found = true
			}
		}
		if out != "" || !found {
			t.Errorf("stdout %q; stderr lacks the sentence %q:\n%s", out, want, errs)
		}
		if after := g.requests() + b.requests(); after != before {
			t.Errorf("unconfigured host caused %d requests", after-before)
		}
	})
	t.Run("invalid config", func(t *testing.T) {
		before := g.requests()
		bad := diagEnv(g, nil)
		bad["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12"
		code, out, errs := runBinary(t, bin, bad, "diag", "pr", g.giteaPR())
		check(t, code, out, errs, 1)
		if out != "" || strings.TrimSpace(errs) == "" || g.requests() != before {
			t.Errorf("stdout %q stderr %q requests %d", out, errs, g.requests()-before)
		}
	})
	t.Run("usage", func(t *testing.T) {
		code, out, errs := runBinary(t, bin, env, "diag", "comment", g.giteaPR())
		check(t, code, out, errs, 2)
		if out != "" || !strings.Contains(errs, "usage:") {
			t.Errorf("stdout %q stderr %q", out, errs)
		}
	})
}
