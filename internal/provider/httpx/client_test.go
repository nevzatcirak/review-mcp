package httpx

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// tokenAuth shows the pattern provider packages use: the Secret is revealed
// only inside the returned func, at the moment the header is set. httpx
// itself never sees a Secret.
func tokenAuth(s config.Secret) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "token "+s.Reveal()) }
}

const testToken = "TOKEN-9f3a2b" //nolint:gosec // test fixture, not a credential

func newClient(t *testing.T, base string, mod func(*Options)) *Client {
	t.Helper()
	o := Options{BaseURL: base, Auth: tokenAuth(config.NewSecret(testToken)), UserAgent: "review-mcp/test"}
	if mod != nil {
		mod(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDoSendsAuthUAAndBody(t *testing.T) {
	var gotAuth, gotUA, gotCT, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotCT = r.Header.Get("Authorization"), r.UserAgent(), r.Header.Get("Content-Type")
		gotPath = r.URL.EscapedPath() + "?" + r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := newClient(t, srv.URL+"/ctx", nil)
	var out struct{ OK bool }
	if err := c.SendJSON(context.Background(), http.MethodPost, "/api/a%2Fb?x=1", map[string]string{"body": "hi"}, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || gotAuth != "token "+testToken || gotUA != "review-mcp/test" || gotCT != "application/json" ||
		gotPath != "/ctx/api/a%2Fb?x=1" || gotBody != `{"body":"hi"}` {
		t.Fatalf("auth=%q ua=%q ct=%q path=%q body=%q", gotAuth, gotUA, gotCT, gotPath, gotBody)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[int]*provider.Error{
		401: provider.ErrAuth, 403: provider.ErrAuth, 404: provider.ErrNotFound,
		429: provider.ErrRateLimited, 500: provider.ErrUpstream, 503: provider.ErrUpstream, 400: provider.ErrProtocol,
	}
	for status, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("BODY-MARKER-7c1e"))
		}))
		c := newClient(t, srv.URL, nil)
		body, st, err := c.Get(context.Background(), "/x", 100, "k")
		srv.Close()
		var pe *provider.Error
		if !errors.Is(err, want) || !errors.As(err, &pe) || pe.Status != status || st != status || body != nil {
			t.Errorf("status %d: err=%v st=%d body=%q", status, err, st, body)
		}
	}
}

// [canary] A body of exactly the cap is accepted; one byte more is rejected.
func TestCapBoundary(t *testing.T) {
	const capN = 64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := capN
		if r.URL.Query().Get("over") != "" {
			n = capN + 1
		}
		if r.URL.Query().Get("chunked") != "" {
			w.(http.Flusher).Flush() // forces chunked: no Content-Length
		}
		_, _ = w.Write(bytes.Repeat([]byte("a"), n))
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, nil)
	for _, q := range []string{"", "?chunked=1"} {
		b, _, err := c.Get(context.Background(), "/f"+q, capN, "diff.max_file_bytes")
		if err != nil || len(b) != capN {
			t.Fatalf("exact cap%s: len=%d err=%v", q, len(b), err)
		}
		sep := "?"
		if q != "" {
			sep = "&"
		}
		_, _, err = c.Get(context.Background(), "/f"+q+sep+"over=1", capN, "diff.max_file_bytes")
		var pe *provider.Error
		if !errors.Is(err, provider.ErrTooLarge) || !errors.As(err, &pe) || pe.Hint != "diff.max_file_bytes" {
			t.Fatalf("cap+1%s: err=%v", q, err)
		}
	}
}

func TestGetJSONUsesJSONCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			_, _ = w.Write([]byte(`{"a": BODY-MARKER-7c1e`))
			return
		}
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, nil)
	var out struct{ A int }
	if err := c.GetJSON(context.Background(), "/ok", &out); err != nil || out.A != 1 {
		t.Fatal(err, out)
	}
	err := c.GetJSON(context.Background(), "/bad", &out)
	if !errors.Is(err, provider.ErrProtocol) || strings.Contains(err.Error(), "BODY-MARKER") {
		t.Fatalf("got %v", err)
	}
	if MaxJSONBytes != 10<<20 || JSONCapKey != "(json response limit)" {
		t.Fatal("json cap constants changed")
	}
}

// [canary] A redirect to a second server is refused and that server
// receives no request at all.
func TestRedirectToOtherOriginIsNotFollowed(t *testing.T) {
	var otherHits atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization reached the other server")
		}
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, nil)
	_, _, err := c.Get(context.Background(), "/x", 100, "k")
	if !errors.Is(err, provider.ErrProtocol) {
		t.Fatalf("got %v, want protocol", err)
	}
	if otherHits.Load() != 0 {
		t.Fatalf("other server received %d requests", otherHits.Load())
	}
}

func TestRedirectPolicySameOriginAndBasePath(t *testing.T) {
	var auths []string
	mux := http.NewServeMux()
	mux.HandleFunc("/base/a", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/base/b", http.StatusFound) })
	mux.HandleFunc("/base/b", func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("done"))
	})
	mux.HandleFunc("/base/up", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/other/b", http.StatusFound) })
	mux.HandleFunc("/base/sib", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/basement", http.StatusFound) })
	mux.HandleFunc("/base/dots", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/base/../x", http.StatusFound) })
	mux.HandleFunc("/base/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/base/loop", http.StatusFound) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected hit %s", r.URL.Path) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newClient(t, srv.URL+"/base", nil)

	b, _, err := c.Get(context.Background(), "/a", 100, "k")
	if err != nil || string(b) != "done" {
		t.Fatalf("same-origin redirect: %q %v", b, err)
	}
	if len(auths) != 1 || auths[0] != "token "+testToken {
		t.Fatalf("auth on redirected request: %v", auths)
	}
	for _, p := range []string{"/up", "/sib", "/dots", "/loop"} {
		if _, _, err := c.Get(context.Background(), p, 100, "k"); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%s: got %v, want protocol", p, err)
		}
	}
}

func TestRedirectDifferentPortRefused(t *testing.T) {
	var hits atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()
	// Same host (127.0.0.1), different port.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/x", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, nil)
	if _, _, err := c.Do(context.Background(), "POST", "/x", strings.NewReader("{}"), "application/json", 100, "k"); !errors.Is(err, provider.ErrProtocol) {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatal("redirect target was contacted")
	}
}

func writeCertPEM(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(p, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()

	t.Run("default fails verification", func(t *testing.T) {
		c := newClient(t, srv.URL, nil)
		_, _, err := c.Get(context.Background(), "/", 10, "k")
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != provider.ClassTransport || pe.Hint != "TLS verification failed" {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("ca cert path succeeds", func(t *testing.T) {
		c := newClient(t, srv.URL, func(o *Options) { o.CACertPath = writeCertPEM(t, srv) })
		if b, _, err := c.Get(context.Background(), "/", 10, "k"); err != nil || string(b) != "ok" {
			t.Fatal(string(b), err)
		}
	})
	t.Run("insecure skip verify succeeds", func(t *testing.T) {
		c := newClient(t, srv.URL, func(o *Options) { o.InsecureSkipVerify = true })
		if _, _, err := c.Get(context.Background(), "/", 10, "k"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("custom ReadFile source", func(t *testing.T) {
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
		c := newClient(t, srv.URL, func(o *Options) {
			o.CACertPath = "/virtual/ca.pem"
			o.ReadFile = func(string) ([]byte, error) { return pemBytes, nil }
		})
		if _, _, err := c.Get(context.Background(), "/", 10, "k"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBadCACert(t *testing.T) {
	dir := t.TempDir()
	empty, garbage := filepath.Join(dir, "empty.pem"), filepath.Join(dir, "garbage.pem")
	_ = os.WriteFile(empty, nil, 0o600)
	_ = os.WriteFile(garbage, []byte("not a pem"), 0o600)
	for _, p := range []string{empty, garbage, filepath.Join(dir, "missing.pem")} {
		_, err := New(Options{BaseURL: "https://example.com", CACertPath: p})
		if err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: err=%v, want error naming the path", p, err)
		}
	}
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	for _, u := range []string{"", "ftp://example.com", "https://u:p@example.com", "example.com", "https://"} {
		if _, err := New(Options{BaseURL: u}); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestTimeoutAndCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	c := newClient(t, srv.URL, nil)
	c.timeout = 50 * time.Millisecond
	_, _, err := c.Get(context.Background(), "/", 10, "k")
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Hint != "timeout" {
		t.Fatalf("got %v", err)
	}
	if RequestTimeout != 60*time.Second {
		t.Fatal("request timeout constant changed")
	}
	c.timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Get(ctx, "/", 10, "k"); !errors.Is(err, provider.ErrTransport) {
		t.Fatalf("got %v", err)
	}
}

func TestConnectionRefusedAndBadPath(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	c := newClient(t, base, nil)
	_, _, err := c.Get(context.Background(), "/x?token=QV", 10, "k")
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != provider.ClassTransport || pe.Hint != "connection failed" || strings.Contains(err.Error(), "QV") {
		t.Fatalf("got %v", err)
	}
	for _, p := range []string{"x", "", "@evil.example/x"} {
		if _, _, err := c.Get(context.Background(), p, 10, "k"); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("path %q: got %v", p, err)
		}
	}
}

// [canary] At debug level, across every error status and failure mode,
// neither the token nor a response-body marker appears in any log line or
// any error string.
func TestNoLeakInLogsOrErrors(t *testing.T) {
	const marker = "BODY-MARKER-7c1e"
	var tokenSeen atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "token "+testToken {
			tokenSeen.Store(true)
		}
		switch r.URL.Path {
		case "/s200":
			_, _ = w.Write([]byte(marker))
		case "/big":
			_, _ = w.Write([]byte(marker + marker))
		case "/redir":
			http.Redirect(w, r, other.URL+"/?"+marker, http.StatusFound)
		case "/badjson":
			_, _ = w.Write([]byte("{" + marker))
		default:
			var st int
			switch r.URL.Path {
			case "/s401":
				st = 401
			case "/s403":
				st = 403
			case "/s404":
				st = 404
			case "/s429":
				st = 429
			case "/s500":
				st = 500
			case "/s400":
				st = 400
			}
			w.WriteHeader(st)
			_, _ = w.Write([]byte(marker))
		}
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := logging.New(&logBuf, slog.LevelDebug)
	c := newClient(t, srv.URL, func(o *Options) { o.Logger = logger })

	var errStrings []string
	for _, p := range []string{"/s200", "/s400", "/s401", "/s403", "/s404", "/s429", "/s500", "/big", "/redir"} {
		_, _, err := c.Get(context.Background(), p+"?access_token="+testToken+"&q=QVAL-1", 20, "diff.max_diff_bytes")
		if err != nil {
			errStrings = append(errStrings, err.Error())
		}
	}
	var sink map[string]any
	if err := c.GetJSON(context.Background(), "/badjson", &sink); err != nil {
		errStrings = append(errStrings, err.Error())
	}
	if err := c.SendJSON(context.Background(), "POST", "/s500", map[string]string{"body": "REQ-BODY-3d"}, nil); err != nil {
		errStrings = append(errStrings, err.Error())
	}
	srv2 := httptest.NewServer(http.NotFoundHandler())
	dead := srv2.URL
	srv2.Close()
	cd := newClient(t, dead, func(o *Options) { o.Logger = logger })
	if _, _, err := cd.Get(context.Background(), "/x?t="+testToken, 10, "k"); err != nil {
		errStrings = append(errStrings, err.Error())
	}

	if !tokenSeen.Load() {
		t.Fatal("test is vacuous: server never saw the token header")
	}
	if len(errStrings) < 10 || !strings.Contains(logBuf.String(), "http request") {
		t.Fatalf("expected errors and debug logs, got %d errors, log %q", len(errStrings), logBuf.String())
	}
	all := logBuf.String() + "\n" + strings.Join(errStrings, "\n")
	for _, bad := range []string{testToken, marker, "QVAL-1", "REQ-BODY-3d", "Authorization", "access_token=" + testToken} {
		if strings.Contains(all, bad) {
			t.Errorf("output leaks %q:\n%s", bad, all)
		}
	}
}
