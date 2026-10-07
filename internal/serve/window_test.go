package serve

import (
	"log/slog"
	"strings"
	"testing"
)

const unsetWindow = "REVIEW_MCP_LLM_CONTEXT_WINDOW"

// X-15 in serve mode: with llm_key_source = header the probe runs with the
// calling request's key; the resolved number is shared by later calls, and no
// key is cached or logged.
func TestContextWindowProbeUsesTheRequestKey(t *testing.T) {
	const (
		key1 = "LLMKEY-ONE-5d2b81"
		key2 = "LLMKEY-TWO-5d2b81"
	)
	ts := startServer(t, serverOpts{
		unset: []string{unsetWindow},
		env:   map[string]string{"REVIEW_MCP_LOG_LEVEL": "debug"},
		level: slog.LevelDebug,
	})

	_, r := ts.callTool(t, creds("gitea-token-01", key1), "server_info", map[string]any{})
	if !strings.Contains(r.text(), "auto (endpoint)") {
		t.Errorf("server_info before any call does not say auto (endpoint): %q", r.text())
	}
	if got := ts.llm.models.Load(); got != 0 {
		t.Fatalf("server_info probed the endpoint (%d requests)", got)
	}

	_, r = ts.callTool(t, creds("gitea-token-01", key1), "pr_review", map[string]any{"pr_url": ts.prURL(1)})
	if r.IsError {
		t.Fatalf("pr_review: %q", r.text())
	}
	_, r = ts.callTool(t, creds("gitea-token-02", key2), "pr_review", map[string]any{"pr_url": ts.prURL(2)})
	if r.IsError {
		t.Fatalf("second pr_review: %q", r.text())
	}
	if got := ts.llm.models.Load(); got != 1 {
		t.Errorf("probes = %d, want 1 for two calls of two clients", got)
	}
	ts.llm.mu.Lock()
	keys := append([]string(nil), ts.llm.modelKeys...)
	ts.llm.mu.Unlock()
	if len(keys) != 1 || keys[0] != key1 {
		t.Errorf("the probe used %v, want the first request's key %q", keys, key1)
	}
	for _, o := range ts.llm.observations() {
		if o.cred != key1 && o.cred != key2 {
			t.Errorf("chat request with an unexpected credential %q", o.cred)
		}
	}

	// The number is shared; the key behind it is not kept.
	_, r = ts.callTool(t, creds("gitea-token-03", ""), "server_info", map[string]any{})
	if !strings.Contains(r.text(), "36000 (endpoint, 90% of 40000)") {
		t.Errorf("server_info after resolution: %q", r.text())
	}
	for _, k := range []string{key1, key2} {
		if strings.Contains(ts.logs.String(), k) || strings.Contains(r.text(), k) {
			t.Errorf("key %s leaked into the logs or a result", k)
		}
	}
	if !strings.Contains(ts.logs.String(), "llm models probe") {
		t.Errorf("the log holds no probe line; the leak check would be vacuous:\n%s", ts.logs.String())
	}
}

// A failing probe sends nothing to the provider and nothing to the model.
func TestFailedContextWindowProbeMakesNoProviderRequest(t *testing.T) {
	ts := startServer(t, serverOpts{unset: []string{unsetWindow}, level: slog.LevelInfo})
	ts.llm.modelsStatus.Store(401)

	_, r := ts.callTool(t, creds("gitea-token-01", "llm-key-01"), "pr_review", map[string]any{"pr_url": ts.prURL(1), "publish": true})
	if !r.IsError || !strings.Contains(r.text(), "rejected the credentials") {
		t.Fatalf("pr_review = error %v %q, want the auth error", r.IsError, r.text())
	}
	_, r = ts.callTool(t, creds("gitea-token-01", "llm-key-01"), "pr_ask", map[string]any{"pr_url": ts.prURL(1), "question": "Why?"})
	if !r.IsError {
		t.Fatal("pr_ask succeeded with a failing probe")
	}
	if n := len(ts.gitea.observations()); n != 0 {
		t.Errorf("provider requests = %d, want 0", n)
	}
	if n := ts.llm.hits.Load(); n != 0 {
		t.Errorf("chat requests = %d, want 0", n)
	}
	// Failures are not cached: once the endpoint is up, the next call works.
	ts.llm.modelsStatus.Store(0)
	_, r = ts.callTool(t, creds("gitea-token-01", "llm-key-01"), "pr_review", map[string]any{"pr_url": ts.prURL(1)})
	if r.IsError {
		t.Fatalf("pr_review after the endpoint recovered: %q", r.text())
	}
}
