package serve

import (
	"log/slog"
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

// startCounted starts h on a loopback test server whose connections are
// counted through http.Server.ConnState. The caller closes the server.
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

func (c *connCounter) counts() (open, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.open), c.total
}

// releaseBound is how long the closes may take to reach the fake's side
// after the last tool call returned.
const releaseBound = 2 * time.Second

// requireReleased fails unless the fake saw connections and every one of
// them is closed within releaseBound.
func (c *connCounter) requireReleased(t *testing.T, who string) {
	t.Helper()
	deadline := time.Now().Add(releaseBound)
	for {
		open, total := c.counts()
		if total == 0 {
			t.Fatalf("%s: no connection was ever opened; the check would prove nothing", who)
		}
		if open == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s: %d of %d server-side connections still open %v after the last tool call ended (per-call HTTP clients were not closed)",
				who, open, total, releaseBound)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestPerCallConnectionsAreReleased: [canary] (C1). Every tool call builds
// its own provider and LLM HTTP clients (per-call transports, P6 §1.1).
// Their keep-alive connections must be closed when the call ends, or a
// shared serve instance keeps idle connections and their goroutines for up
// to 90 s per call. N sequential calls run through the real serve stack
// (middleware, MCP handler, ConfigFor, wiring.NewResolver, wiring.NewLLM)
// and the fakes must see every connection closed shortly after.
func TestPerCallConnectionsAreReleased(t *testing.T) {
	const n = 10
	cases := []struct {
		tool    string
		args    func(ts *testServer, i int) map[string]any
		usesLLM bool
		// failGitea makes the provider fail every request (error path).
		failGitea bool
	}{
		{tool: "pr_review", usesLLM: true, args: func(ts *testServer, i int) map[string]any {
			return map[string]any{"pr_url": ts.prURL(i)}
		}},
		{tool: "pr_ask", usesLLM: true, args: func(ts *testServer, i int) map[string]any {
			return map[string]any{"pr_url": ts.prURL(i), "question": "What changed?"}
		}},
		{tool: "pr_comments", args: func(ts *testServer, i int) map[string]any {
			return map[string]any{"pr_url": ts.prURL(i)}
		}},
		{tool: "pr_review", failGitea: true, args: func(ts *testServer, i int) map[string]any {
			return map[string]any{"pr_url": ts.prURL(i)}
		}},
	}
	for _, tc := range cases {
		name := tc.tool
		if tc.failGitea {
			name += "_provider_error"
		}
		t.Run(name, func(t *testing.T) {
			ts := startServer(t, serverOpts{level: slog.LevelInfo})
			if tc.failGitea {
				ts.gitea.failStatus.Store(http.StatusInternalServerError)
			}
			hdr := creds("gitea-token-c1", "llm-key-c1")
			for i := 1; i <= n; i++ {
				_, r := ts.callTool(t, hdr, tc.tool, tc.args(ts, i))
				if r.IsError != tc.failGitea {
					t.Fatalf("call %d: isError %v, want %v (%q)", i, r.IsError, tc.failGitea, r.text())
				}
			}
			ts.gitea.conns.requireReleased(t, "gitea")
			if tc.usesLLM {
				if got := ts.llm.hits.Load(); got < n {
					t.Fatalf("llm: %d requests, want at least %d", got, n)
				}
				ts.llm.conns.requireReleased(t, "llm")
			}
		})
	}
}
