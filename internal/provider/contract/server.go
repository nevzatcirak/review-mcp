package contract

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Sentinel is embedded in the body of every error response of a fake
// server. No provider error text may ever contain it.
const Sentinel = "CONTRACT-SENTINEL-7f3e2a-response-body"

// WriteError writes an error response with status whose body carries
// Sentinel. Fixtures use it for every error they answer.
func WriteError(w http.ResponseWriter, status int) {
	http.Error(w, "fake server error "+Sentinel, status)
}

// request is one request a fake server received.
type request struct {
	Method, Path string
}

// requestLog records the requests of one served Spec.
type requestLog struct {
	mu      sync.Mutex
	started bool
	reqs    []request
}

func (l *requestLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, request{r.Method, r.URL.EscapedPath()})
}

func (l *requestLog) markStarted() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = true
}

func (l *requestLog) isStarted() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.started
}

// count returns the number of requests and of write requests (any method
// other than GET and HEAD) received so far.
func (l *requestLog) count() (all, writes int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.reqs {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writes++
		}
	}
	return len(l.reqs), writes
}

// StartServer starts the fake server of a Fixture and returns its base URL
// (scheme and host, no trailing slash). The server is closed when t ends.
// Every Fixture must serve through it: it records each request for the
// suite and applies pr.Env.Failure before h sees the request.
//
// With Failure.Status set, every request is answered with that status and a
// body that carries Sentinel. With Failure.Network set, the server accepts
// connections and closes them at once, so every request fails at the
// transport.
func StartServer(t *testing.T, pr Spec, h http.Handler) string {
	t.Helper()
	log := pr.Env.log
	if log == nil {
		// Called outside Run: record into a throwaway log.
		log = &requestLog{}
	}
	log.markStarted()
	if pr.Env.Failure.Network {
		return startDropping(t)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if s := pr.Env.Failure.Status; s != 0 {
			WriteError(w, s)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// startDropping listens on a loopback port and closes every connection
// without a response. Unlike a closed server, its port cannot be reused by
// another listener while the test runs.
func startDropping(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return "http://" + ln.Addr().String()
}
