package httpx

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// connCounter counts the server-side connections of an httptest server that
// are still open (new, active or idle; not yet closed or hijacked).
type connCounter struct {
	mu    sync.Mutex
	open  map[net.Conn]struct{}
	total int
}

// startCounted starts h on a test server whose connections are counted.
func startCounted(t *testing.T, h http.Handler) (*httptest.Server, *connCounter) {
	t.Helper()
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
	t.Cleanup(srv.Close)
	return srv, c
}

func (c *connCounter) counts() (open, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.open), c.total
}

// waitOpen polls until want connections are open or the bound expires.
func (c *connCounter) waitOpen(want int, bound time.Duration) int {
	deadline := time.Now().Add(bound)
	for {
		open, _ := c.counts()
		if open == want || time.Now().After(deadline) {
			return open
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCloseIdleConnectionsClosesKeepAliveConns(t *testing.T) {
	srv, conns := startCounted(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	c := newClient(t, srv.URL, nil)
	var out struct{ OK bool }
	for range 3 {
		if err := c.GetJSON(context.Background(), "/x", &out); err != nil {
			t.Fatal(err)
		}
	}
	// The keep-alive connection stays open until the client closes it.
	if open, total := conns.counts(); open != 1 || total != 1 {
		t.Fatalf("before close: open %d total %d, want 1 and 1 (one reused keep-alive connection)", open, total)
	}
	c.CloseIdleConnections()
	if open := conns.waitOpen(0, 2*time.Second); open != 0 {
		t.Fatalf("after CloseIdleConnections: %d server-side connections still open", open)
	}
	// A second call is harmless.
	c.CloseIdleConnections()
}

func TestCloseIdleConnectionsNilAndUnused(t *testing.T) {
	var nilClient *Client
	nilClient.CloseIdleConnections()
	(&Client{}).CloseIdleConnections()
	c := newClient(t, "https://your-gitea.example", nil)
	c.CloseIdleConnections()
	c.CloseIdleConnections()
}
