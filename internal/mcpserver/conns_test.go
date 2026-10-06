package mcpserver

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// connCounter counts the server-side connections of a fake that are still
// open: new, active or idle, and not yet closed or hijacked.
type connCounter struct {
	mu    sync.Mutex
	open  map[net.Conn]struct{}
	total int
}

// startCounted starts h on a test server whose connections are counted
// through http.Server.ConnState. The caller closes the server.
func startCounted(h http.Handler) (*httptest.Server, *connCounter) {
	c := &connCounter{open: map[net.Conn]struct{}{}}
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ConnState = func(conn net.Conn, st http.ConnState) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch st {
		case http.StateNew:
			c.open[conn] = struct{}{}
			c.total++
		case http.StateClosed, http.StateHijacked:
			delete(c.open, conn)
		}
	}
	srv.Start()
	return srv, c
}

// requireReleased fails unless the fake saw connections and every one of
// them is closed within 2 s.
func (c *connCounter) requireReleased(t *testing.T, who string) {
	t.Helper()
	const bound = 2 * time.Second
	deadline := time.Now().Add(bound)
	for {
		c.mu.Lock()
		open, total := len(c.open), c.total
		c.mu.Unlock()
		if total == 0 {
			t.Fatalf("%s: no connection was ever opened; the check would prove nothing", who)
		}
		if open == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s: %d of %d server-side connections still open %v after the last tool call ended (per-call HTTP clients were not closed)",
				who, open, total, bound)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestStdioPerCallConnectionsAreReleased (C1): in stdio mode too, the
// provider and LLM clients built for a tool call close their keep-alive
// connections when the call ends.
func TestStdioPerCallConnectionsAreReleased(t *testing.T) {
	g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(reviewEnv(g, l), nil))
	for range 3 {
		if res := callTool(t, cs, "pr_review", map[string]any{"pr_url": reviewPRURL(g)}); res.IsError {
			t.Fatalf("pr_review: %s", textOf(t, res))
		}
		if res := callTool(t, cs, "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": "What changed?"}); res.IsError {
			t.Fatalf("pr_ask: %s", textOf(t, res))
		}
	}
	g.conns.requireReleased(t, "gitea")
	l.conns.requireReleased(t, "llm")
}
