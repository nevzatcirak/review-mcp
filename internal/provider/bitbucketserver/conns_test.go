package bitbucketserver_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
)

var _ provider.IdleCloser = (*bitbucketserver.Provider)(nil)

// TestProviderCloseIdleConnections: the provider releases the keep-alive
// connection of its per-call HTTP client (a 404 still leaves one idle).
func TestProviderCloseIdleConnections(t *testing.T) {
	var mu sync.Mutex
	open := map[net.Conn]struct{}{}
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.Config.ConnState = func(c net.Conn, st http.ConnState) {
		mu.Lock()
		defer mu.Unlock()
		switch st {
		case http.StateNew:
			open[c] = struct{}{}
		case http.StateClosed, http.StateHijacked:
			delete(open, c)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(open)
	}

	cfg := config.Defaults()
	cfg.BitbucketServer.BaseURL = srv.URL
	cfg.Secrets.BitbucketServerToken = config.NewSecret(testToken)
	p, err := bitbucketserver.NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.GetPullRequest(context.Background(), ref()); err == nil {
		t.Fatal("want an error from the 404 fake")
	}
	if n := count(); n != 1 {
		t.Fatalf("before close: %d open connections, want 1 idle keep-alive connection", n)
	}
	p.(provider.IdleCloser).CloseIdleConnections()
	deadline := time.Now().Add(2 * time.Second)
	for count() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := count(); n != 0 {
		t.Fatalf("after CloseIdleConnections: %d server-side connections still open", n)
	}
	p.(provider.IdleCloser).CloseIdleConnections() // a second call is harmless

	var nilProvider *bitbucketserver.Provider
	nilProvider.CloseIdleConnections()
}
