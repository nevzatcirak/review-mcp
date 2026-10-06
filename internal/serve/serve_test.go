package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/credentials"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

func TestMalformedCredentialHeaderRejected(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})
	const marker = "MALFORMED-MARKER-8d2c"
	body := callBody(1, "server_info", nil)
	for _, name := range credentials.ToolHeaders {
		for _, v := range []string{marker + " inner space", marker + "\u00e9", strings.Repeat("a", credentials.MaxValueBytes+1)} {
			resp := ts.post(t, PathMCP, map[string]string{name: v}, body)
			want := "malformed credential header: " + name + "\n"
			if resp.status != http.StatusBadRequest || resp.body != want {
				t.Errorf("%s: status %d body %q", name, resp.status, resp.body)
			}
		}
	}
	if ts.resolves.Load() != 0 || strings.Contains(ts.logs.String(), marker) {
		t.Error("a malformed request ran a tool or leaked its value")
	}
	// Trimmed, max-length and empty values pass.
	for _, v := range []string{"  tok\t", strings.Repeat("a", credentials.MaxValueBytes), ""} {
		if resp := ts.post(t, PathMCP, map[string]string{credentials.HeaderGiteaToken: v}, body); resp.status != http.StatusOK {
			t.Errorf("value of %d bytes: status %d", len(v), resp.status)
		}
	}
}

func TestServerInfoInServeMode(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})
	_, r := ts.callTool(t, creds("gitea-secret-value", ""), "server_info", map[string]any{})
	if r.IsError {
		t.Fatalf("server_info: %q", r.text())
	}
	var res tools.ServerInfoResult
	if err := json.Unmarshal(r.Structured, &res); err != nil {
		t.Fatal(err)
	}
	if res.Serve == nil || res.Serve.Transport != "serve" || res.Serve.LLMKeySource != "header" ||
		res.Serve.Listen != "127.0.0.1:"+ts.port {
		t.Fatalf("serve info = %+v", res.Serve)
	}
	want := map[string]string{
		credentials.HeaderGiteaToken: "set", credentials.HeaderBitbucketServerToken: "unset",
		credentials.HeaderLLMAPIKey: "unset", credentials.HeaderAuthorization: "unset",
	}
	for k, v := range want {
		if res.Serve.RequestHeaders[k] != v {
			t.Errorf("%s = %q, want %q", k, res.Serve.RequestHeaders[k], v)
		}
	}
	if res.Status != tools.StatusOK || res.Config.Secrets["gitea.token"] != "set" {
		t.Errorf("status %q, secrets %v", res.Status, res.Config.Secrets)
	}
	for _, s := range []string{"## Serve", "`serve`", "`X-Review-MCP-Gitea-Token`: `set`"} {
		if !strings.Contains(r.text(), s) {
			t.Errorf("markdown lacks %q", s)
		}
	}
	if strings.Contains(r.text()+string(r.Structured), "gitea-secret-value") {
		t.Error("header value leaked")
	}
}

// TestConcurrencyGate: a full gate fails tool calls at once with
// server_busy; other methods are not counted.
func TestConcurrencyGate(t *testing.T) {
	ts := startServer(t, serverOpts{env: map[string]string{"REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS": "1"}, level: slog.LevelInfo})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	hold := func() { <-release }
	ts.gitea.onRequest.Store(&hold)

	first := make(chan toolResult, 1)
	go func() {
		_, r := ts.callTool(t, creds("gitea-token", ""), "pr_comments", map[string]any{"pr_url": ts.prURL(1)})
		first <- r
	}()
	select { // the first call holds the only slot
	case <-ts.gitea.arrived:
	case <-time.After(20 * time.Second):
		t.Fatal("first call never reached the provider")
	}

	for _, tool := range []string{"pr_comments", "server_info"} {
		got := make(chan response, 1)
		go func() {
			got <- ts.post(t, PathMCP, creds("gitea-token", ""), callBody(1, tool, map[string]any{"pr_url": ts.prURL(2)}))
		}()
		var resp response
		select {
		case resp = <-got:
		case <-time.After(5 * time.Second):
			unblock()
			t.Fatalf("%s while full waited instead of failing at once", tool)
		}
		var r toolResult
		_ = json.Unmarshal(rpcMessage(t, resp.body)["result"], &r)
		if !r.IsError || r.text() != tools.ServerBusyMessage {
			t.Errorf("%s while full: isError=%v %q", tool, r.IsError, r.text())
		}
	}
	// Non-tool methods pass while the gate is full.
	for _, b := range []string{
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"0"}}}`,
	} {
		resp := ts.post(t, PathMCP, nil, b)
		if m := rpcMessage(t, resp.body); resp.status != http.StatusOK || m["error"] != nil || m["result"] == nil {
			t.Errorf("%s while full: %d %q", b, resp.status, resp.body)
		}
	}
	if ts.resolves.Load() != 1 {
		t.Errorf("resolver factory calls = %d, want 1 (busy calls must not run)", ts.resolves.Load())
	}

	unblock()
	if r := <-first; r.IsError {
		t.Errorf("first call failed: %q", r.text())
	}
	// The slot is free again.
	if _, r := ts.callTool(t, creds("gitea-token", ""), "server_info", map[string]any{}); r.IsError {
		t.Errorf("after release: %q", r.text())
	}
}

func TestBodyLimit(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})
	// Declared length over the limit: our fixed 413.
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"pad":"` + strings.Repeat("x", MaxBodyBytes) + `"}}}`
	resp := ts.post(t, PathMCP, nil, big)
	if resp.status != http.StatusRequestEntityTooLarge || resp.body != BodyTooLarge {
		t.Errorf("declared oversize: %d %q", resp.status, resp.body)
	}
	// Chunked (no declared length) over the limit: MaxBytesReader stops the
	// read and the request fails with 413.
	req, _ := http.NewRequest(http.MethodPost, ts.url+PathMCP, io.MultiReader(strings.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if r := do(t, req); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked oversize: %d %q", r.status, r.body)
	}
	// Just under the limit is read in full.
	pad := MaxBodyBytes - len(`{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"pad":""}}}`)
	ok := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"pad":"` + strings.Repeat("x", pad) + `"}}}`
	if len(ok) != MaxBodyBytes {
		t.Fatalf("test body is %d bytes", len(ok))
	}
	if r := ts.post(t, PathMCP, nil, ok); r.status != http.StatusOK {
		t.Errorf("body at the limit: %d %q", r.status, r.body)
	}
}

func TestRoutes(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})
	r := do(t, mustReq(t, http.MethodGet, ts.url+PathHealth))
	if r.status != http.StatusOK || r.body != BodyHealth {
		t.Errorf("GET /healthz: %d %q", r.status, r.body)
	}
	if r := do(t, mustReq(t, http.MethodHead, ts.url+PathHealth)); r.status != http.StatusOK {
		t.Errorf("HEAD /healthz: %d", r.status)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, PathHealth}, {http.MethodGet, "/"}, {http.MethodPost, "/mcp/"}, {http.MethodGet, "/mcp/x"},
		{http.MethodGet, "/healthz/"}, {http.MethodPost, "//mcp"}, {http.MethodGet, "/metrics"},
	} {
		r := do(t, mustReq(t, c.method, ts.url+c.path))
		if r.status != http.StatusNotFound || r.body != BodyNotFound {
			t.Errorf("%s %s: %d %q", c.method, c.path, r.status, r.body)
		}
	}
	// GET and DELETE on /mcp: 405 from the stateless handler, after the
	// checks.
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		if r := do(t, mustReq(t, m, ts.url+PathMCP)); r.status != http.StatusMethodNotAllowed {
			t.Errorf("%s /mcp: %d", m, r.status)
		}
	}
	// Accept must list both media types.
	req, _ := http.NewRequest(http.MethodPost, ts.url+PathMCP, strings.NewReader(callBody(1, "server_info", nil)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if r := do(t, req); r.status != http.StatusBadRequest {
		t.Errorf("Accept without text/event-stream: %d", r.status)
	}
	if ts.resolves.Load() != 0 {
		t.Error("a tool ran")
	}
}

// TestAccessLog: one info line per request with method, path, status,
// duration and bytes; the SDK's per-request records are at debug; no
// header, query or body; the remote address only at debug.
func TestAccessLog(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})
	const marker = "ACCESSLOG-MARKER-51ab"
	resp, _ := ts.callTool(t, creds(marker, ""), "server_info", map[string]any{})
	_ = do(t, mustReq(t, http.MethodGet, ts.url+PathHealth+"?q="+marker))
	_ = do(t, mustReq(t, http.MethodGet, ts.url+"/"+marker))

	lines := strings.Split(strings.TrimSpace(ts.logs.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want exactly 3 log lines at info (one per request), got %d:\n%s", len(lines), ts.logs.String())
	}
	wants := []string{
		`level=INFO msg="http request" method=POST path=/mcp status=200 duration_ms=`,
		`level=INFO msg="http request" method=GET path=/healthz status=200 duration_ms=`,
		`level=INFO msg="http request" method=GET path=(other) status=404 duration_ms=`,
	}
	for i, w := range wants {
		if !strings.Contains(lines[i], w) {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
	if !strings.Contains(lines[0], " bytes="+strconv.Itoa(len(resp.body))) || !strings.Contains(lines[1], " bytes=3") {
		t.Errorf("byte counts: %q / %q (body %d)", lines[0], lines[1], len(resp.body))
	}
	if strings.Contains(ts.logs.String(), marker) || strings.Contains(ts.logs.String(), "127.0.0.1:") {
		t.Errorf("header, query, path or remote address logged at info:\n%s", ts.logs.String())
	}

	dbg := startServer(t, serverOpts{level: slog.LevelDebug})
	_, _ = dbg.callTool(t, nil, "server_info", map[string]any{})
	out := dbg.logs.String()
	for _, w := range []string{`level=DEBUG msg="http request remote address" remote=127.0.0.1:`, `level=DEBUG msg="server session connected"`} {
		if !strings.Contains(out, w) {
			t.Errorf("debug log lacks %q:\n%s", w, out)
		}
	}
	if n := strings.Count(out, "level=INFO"); n != 1 {
		t.Errorf("%d info lines for one request at debug level:\n%s", n, out)
	}
}

// TestGracefulShutdown: cancellation stops new connections, lets an
// in-flight request finish, and Serve returns nil.
func TestGracefulShutdown(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo})
	release := make(chan struct{})
	hold := func() { <-release }
	ts.gitea.onRequest.Store(&hold)

	inflight := make(chan toolResult, 1)
	go func() {
		_, r := ts.callTool(t, creds("gitea-token", ""), "pr_comments", map[string]any{"pr_url": ts.prURL(1)})
		inflight <- r
	}()
	<-ts.gitea.arrived
	ts.cancel()

	// New connections are refused once the listener is closed.
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", strings.TrimPrefix(ts.url, "http://"), time.Second)
		if err != nil {
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener still accepts after shutdown began")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-ts.done:
		t.Fatalf("Serve returned (%v) before the in-flight request finished", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if r := <-inflight; r.IsError {
		t.Errorf("in-flight request failed: %q", r.text())
	}
	select {
	case err := <-ts.done:
		if err != nil {
			t.Errorf("Serve = %v, want nil", err)
		}
		ts.done <- nil // for the cleanup
	case <-time.After(20 * time.Second):
		t.Fatal("Serve did not return after the drain")
	}
}

// TestShutdownDrainTimeout: a request still running when the drain timeout
// expires is cut off and Serve returns nil.
func TestShutdownDrainTimeout(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelInfo, shutdownTimeout: 300 * time.Millisecond})
	release := make(chan struct{})
	defer close(release)
	hold := func() { <-release }
	ts.gitea.onRequest.Store(&hold)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, ts.url+PathMCP, strings.NewReader(callBody(1, "pr_comments", map[string]any{"pr_url": ts.prURL(1)})))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set(credentials.HeaderGiteaToken, "gitea-token")
		if resp, err := httpClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-ts.gitea.arrived
	start := time.Now()
	ts.cancel()
	select {
	case err := <-ts.done:
		if err != nil {
			t.Errorf("Serve = %v", err)
		}
		if d := time.Since(start); d < 300*time.Millisecond || d > 10*time.Second {
			t.Errorf("returned after %v", d)
		}
		ts.done <- nil
	case <-time.After(20 * time.Second):
		t.Fatal("Serve did not return after the drain timeout")
	}
	if !strings.Contains(ts.logs.String(), "drain timeout reached") {
		t.Errorf("no drain-timeout line:\n%s", ts.logs.String())
	}
}

// writeSelfSigned writes a self-signed certificate for 127.0.0.1 and its key.
func writeSelfSigned(t *testing.T) (certPath, keyPath string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "review-mcp test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(c)
	return certPath, keyPath, pool
}

// osSource reads real files but takes the environment from env.
type osSource struct{ env map[string]string }

func (s osSource) LookupEnv(k string) (string, bool) { v, ok := s.env[k]; return v, ok }
func (s osSource) Environ() []string                 { return nil }

//nolint:gosec // G304: test paths under t.TempDir()
func (s osSource) ReadFile(p string) ([]byte, error)  { return os.ReadFile(p) }
func (s osSource) Stat(p string) (os.FileInfo, error) { return os.Stat(p) }

func TestTLS(t *testing.T) {
	certPath, keyPath, pool := writeSelfSigned(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg, rep, err := config.LoadWith(osSource{env: map[string]string{
		"REVIEW_MCP_LLM_BASE_URL": "https://llm.example.com/v1", "REVIEW_MCP_LLM_MODEL": "m",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000", "REVIEW_MCP_GITEA_BASE_URL": "https://your-gitea.example",
		"REVIEW_MCP_SERVE_LISTEN": ln.Addr().String(), "REVIEW_MCP_SERVE_TLS_CERT": certPath, "REVIEW_MCP_SERVE_TLS_KEY": keyPath,
	}}, config.LoadOptions{Mode: config.ModeServe})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{Config: cfg, Report: rep, NewResolver: wiring.NewResolver, NewLLM: wiring.NewLLM})
	if err != nil {
		t.Fatal(err)
	}
	if !srv.TLSEnabled() || !strings.HasPrefix(srv.URL(), "https://127.0.0.1:") || !strings.HasSuffix(srv.URL(), "/mcp") {
		t.Errorf("URL = %q", srv.URL())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()

	url := "https://" + ln.Addr().String() + PathHealth
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("TLS 1.2+ client: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Errorf("status %d, TLS %+v", resp.StatusCode, resp.TLS)
	}
	//nolint:gosec // G402: the test proves that a TLS 1.1 client is refused
	oldTLS := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}
	old := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: oldTLS}}
	if resp, err := old.Get(url); err == nil {
		_ = resp.Body.Close()
		t.Error("a TLS 1.1 client was served")
	}
	// Plain HTTP on the TLS port is not served.
	if resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + ln.Addr().String() + PathHealth); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Error("plain HTTP served on the TLS listener")
		}
	}
}

func TestNewRejectsBadTLSFiles(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(cert, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Serve.TLSCert, cfg.Serve.TLSKey = cert, cert
	_, err := New(Options{Config: cfg})
	if !errors.Is(err, ErrTLSLoad) || strings.Contains(err.Error(), dir) {
		t.Errorf("err = %v", err)
	}
}

// TestConcurrentRequestsDoNotRace exercises the recorder and middleware under
// the race detector with mixed endpoints.
func TestConcurrentRequestsDoNotRace(t *testing.T) {
	ts := startServer(t, serverOpts{level: slog.LevelDebug, env: map[string]string{"REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS": "64"}})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = ts.callTool(t, nil, "server_info", map[string]any{}) }()
		go func() { defer wg.Done(); _ = do(t, mustReq(t, http.MethodGet, ts.url+PathHealth)) }()
	}
	wg.Wait()
}
