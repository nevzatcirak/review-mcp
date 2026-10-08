package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

const serveEnvToken = "FAKE-serve-env-gitea-token-QX42-do-not-leak" //nolint:gosec // synthetic test value

func serveTestEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"REVIEW_MCP_LLM_BASE_URL":       "https://llm.example.com/v1",
		"REVIEW_MCP_LLM_MODEL":          "example-model",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
		"REVIEW_MCP_GITEA_BASE_URL":     "https://your-gitea.example",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func serveLoaderFor(env map[string]string) serveLoader {
	return func(opts config.LoadOptions) (*config.Config, *config.Report, error) {
		return config.LoadWith(config.MemSource{Env: env}, opts)
	}
}

// countingListen records whether a listener was ever requested.
type countingListen struct{ calls atomic.Int64 }

func (c *countingListen) listen(network, address string) (net.Listener, error) {
	c.calls.Add(1)
	return net.Listen(network, address)
}

// TestServeRefusesInvalidConfigWithoutListening: [canary] (P6 §1.6 #2 and
// #4, and RC-1): environment provider tokens, an insecure non-loopback bind
// and repository context fail validation, exit 2 and open no listener.
func TestServeRefusesInvalidConfigWithoutListening(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"gitea token in env", serveTestEnv(map[string]string{"REVIEW_MCP_GITEA_TOKEN": serveEnvToken}), nil,
			"serve mode takes provider tokens from request headers; unset REVIEW_MCP_GITEA_TOKEN"},
		{"bitbucket token in env", serveTestEnv(map[string]string{"REVIEW_MCP_BITBUCKET_SERVER_TOKEN": serveEnvToken}), nil,
			"serve mode takes provider tokens from request headers; unset REVIEW_MCP_BITBUCKET_SERVER_TOKEN"},
		{"repository context (RC-1)", serveTestEnv(map[string]string{"REVIEW_MCP_CONTEXT_REPO_ENABLED": "true"}), nil,
			config.ServeRepoContextSentence},
		{"insecure bind by flag", serveTestEnv(nil), []string{"--listen", "0.0.0.0:8787"}, "is not a loopback address"},
		{"insecure bind by env", serveTestEnv(map[string]string{"REVIEW_MCP_SERVE_LISTEN": "0.0.0.0:8787"}), nil, "is not a loopback address"},
		{"server key source without access token", serveTestEnv(map[string]string{
			"REVIEW_MCP_SERVE_LLM_KEY_SOURCE": "server", "REVIEW_MCP_LLM_API_KEY": serveEnvToken,
		}), nil, "REVIEW_MCP_SERVE_ACCESS_TOKEN is required because serve.llm_key_source is server"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr lockedBuffer
			cl := &countingListen{}
			// The deadline only matters when validation wrongly passes: the
			// server then stops after it and the test fails on the exit code
			// and the listener count instead of hanging.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			code := runServe(ctx, tc.args, &stderr, serveLoaderFor(tc.env), cl.listen)
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if n := cl.calls.Load(); n != 0 {
				t.Errorf("a listener was opened %d times", n)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr lacks %q:\n%s", tc.want, stderr.String())
			}
			if strings.Contains(stderr.String(), serveEnvToken) {
				t.Error("the token leaked to stderr")
			}
		})
	}
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestServeStartsAndStopsCleanly(t *testing.T) {
	addr := freeLoopbackAddr(t)
	var stderr lockedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- runServe(ctx, []string{"--listen", addr}, &stderr, serveLoaderFor(serveTestEnv(nil)), net.Listen)
	}()

	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stderr.String(), "review-mcp serving") {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("no startup line:\n%s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Errorf("healthz: %d %q", resp.StatusCode, body)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0\n%s", code, stderr.String())
		}
	case <-time.After(40 * time.Second):
		t.Fatal("serve did not stop")
	}
	out := stderr.String()
	startup := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "review-mcp serving") {
			startup = l
		}
	}
	for _, want := range []string{"level=INFO", "url=http://" + addr + "/mcp", "tls=off", "llm_key_source=header"} {
		if !strings.Contains(startup, want) {
			t.Errorf("startup line lacks %q: %q", want, startup)
		}
	}
	if strings.Count(out, "review-mcp serving") != 1 || !strings.Contains(out, "server stopped") {
		t.Errorf("stderr:\n%s", out)
	}
}

func TestServeListenFailureExits1(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var stderr lockedBuffer
	code := runServe(context.Background(), []string{"--listen", ln.Addr().String()}, &stderr, serveLoaderFor(serveTestEnv(nil)), net.Listen)
	if code != 1 || !strings.Contains(stderr.String(), "cannot listen") {
		t.Errorf("exit %d, stderr:\n%s", code, stderr.String())
	}
}
