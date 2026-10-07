package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

const windowModel = "example-model"

// resetWindowCache empties the process-wide cache around a test.
func resetWindowCache(t *testing.T) {
	t.Helper()
	clear := func() {
		windows.mu.Lock()
		defer windows.mu.Unlock()
		windows.vals = map[string]windowResult{}
	}
	clear()
	t.Cleanup(clear)
}

func windowClient(t *testing.T, url string, opts ...Option) *Client {
	t.Helper()
	cfg := baseCfg(url)
	cfg.Model = windowModel
	cfg.ContextWindow = 0
	c, err := New(cfg, config.NewSecret(testKey), nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func modelsHandler(body string) func(int, http.ResponseWriter, *http.Request) {
	return func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, body) }
}

func TestResolveContextWindowShapes(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantN      int
		wantSource string
		wantErr    string // substring of the error text
		wantClass  ErrorClass
	}{
		{"vllm max_model_len", 200,
			`{"object":"list","data":[{"id":"example-model","object":"model","max_model_len":32768,"owned_by":"vllm"}]}`,
			29491, "endpoint, 90% of 32768", "", ""},
		{"openrouter context_length", 200,
			`{"data":[{"id":"other"},{"id":"example-model","context_length":131072,"pricing":{"prompt":"0"}}]}`,
			117964, "endpoint, 90% of 131072", "", ""},
		{"string number", 200,
			`{"data":[{"id":"example-model","context_window":"16384"}]}`,
			14745, "endpoint, 90% of 16384", "", ""},
		{"bare array", 200,
			`[{"id":"example-model","max_context_length":10000}]`,
			9000, "endpoint, 90% of 10000", "", ""},
		{"unknown fields ignored", 200,
			`{"data":[{"id":"example-model","max_model_len":8192,"extra":{"nested":[1,2]},"status":null}],"first_id":"x"}`,
			7372, "endpoint, 90% of 8192", "", ""},
		{"training-only field", 200,
			`{"data":[{"id":"example-model","n_ctx_train":131072,"meta":{"n_ctx_train":131072,"n_vocab":32000}}]}`,
			0, "", "does not report the model's context window; set llm.context_window", ClassProtocol},
		{"model not listed", 200,
			`{"data":[{"id":"other-model","max_model_len":8192}]}`,
			0, "", "the LLM endpoint does not list the configured model; check llm.model", ClassNotFound},
		{"id must match exactly", 200,
			`{"data":[{"id":"Example-Model","max_model_len":8192},{"id":"example-model-2","max_model_len":8192}]}`,
			0, "", "does not list the configured model", ClassNotFound},
		{"empty list", 200, `{"data":[]}`,
			0, "", "does not list the configured model", ClassNotFound},
		{"empty bare array", 200, `[]`,
			0, "", "does not list the configured model", ClassNotFound},
		{"not a model list", 200, `{"error":"nope"}`,
			0, "", "does not list the configured model", ClassNotFound},
		{"not json", 200, `<html>hi</html>`,
			0, "", "unexpected response", ClassProtocol},
		{"window below the minimum", 200,
			`{"data":[{"id":"example-model","max_model_len":2048}]}`,
			0, "", "below the 4096-token minimum", ClassProtocol},
		{"zero is not positive", 200,
			`{"data":[{"id":"example-model","max_model_len":0}]}`,
			0, "", "does not report the model's context window", ClassProtocol},
		{"unauthorized", 401, `{"error":"bad key"}`, 0, "", "rejected the credentials", ClassAuth},
		{"not found", 404, `{"error":"no"}`, 0, "", "not found", ClassNotFound},
		{"server error", 500, `oops`, 0, "", "internal error", ClassUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetWindowCache(t)
			f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, tc.status, tc.body) })
			n, src, err := windowClient(t, f.srv.URL).ResolveContextWindow(context.Background())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if n != tc.wantN || src != tc.wantSource {
					t.Errorf("got %d %q, want %d %q", n, src, tc.wantN, tc.wantSource)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if cl, _ := ClassOf(err); cl != tc.wantClass {
				t.Errorf("class = %q, want %q", cl, tc.wantClass)
			}
			if tc.status != 200 && tc.status != 404 {
				return
			}
			// A failure is never cached.
			if _, _, ok := ResolvedContextWindow(f.srv.URL, windowModel); ok {
				t.Error("a failed probe was cached")
			}
		})
	}
}

func TestResolveContextWindowRequestShape(t *testing.T) {
	resetWindowCache(t)
	f := newFake(t, modelsHandler(`{"data":[{"id":"example-model","max_model_len":8192}]}`))
	if _, _, err := windowClient(t, f.srv.URL+"/v1/").ResolveContextWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.paths; len(got) != 1 || got[0] != "/v1/models" {
		t.Errorf("paths = %v, want one GET of /v1/models", got)
	}
	if got := f.auths[0]; got != "Bearer "+testKey {
		t.Errorf("Authorization = %q, want the client's bearer key", got)
	}
}

func TestResolveContextWindowTimeout(t *testing.T) {
	resetWindowCache(t)
	release := make(chan struct{})
	f := newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	c := windowClient(t, f.srv.URL, WithProbeTimeout(50*time.Millisecond))
	_, _, err := c.ResolveContextWindow(context.Background())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if _, _, ok := ResolvedContextWindow(f.srv.URL, windowModel); ok {
		t.Error("a timed-out probe was cached")
	}
}

func TestResolveContextWindowTransportError(t *testing.T) {
	resetWindowCache(t)
	f := newFake(t, modelsHandler(`{}`))
	url := f.srv.URL
	f.srv.Close()
	_, _, err := windowClient(t, url).ResolveContextWindow(context.Background())
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("err = %v, want a transport error", err)
	}
	if strings.Contains(err.Error(), url) {
		t.Errorf("the error leaks the URL: %v", err)
	}
}

func TestResolveContextWindowCanceled(t *testing.T) {
	resetWindowCache(t)
	f := newFake(t, func(_ int, _ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, _, err := windowClient(t, f.srv.URL).ResolveContextWindow(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestResolveContextWindowResponseCap(t *testing.T) {
	resetWindowCache(t)
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"example-model","max_model_len":8192,"pad":"` + strings.Repeat("x", 4096) + `"}]}`))
	})
	c := windowClient(t, f.srv.URL)
	c.maxBytes = 1024
	_, _, err := c.ResolveContextWindow(context.Background())
	if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("err = %v, want the size-limit protocol error", err)
	}
}

func TestResolveContextWindowRedirectPinned(t *testing.T) {
	resetWindowCache(t)
	other := newFake(t, modelsHandler(`{"data":[{"id":"example-model","max_model_len":8192}]}`))
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", other.srv.URL+"/models")
		w.WriteHeader(http.StatusFound)
	})
	_, _, err := windowClient(t, f.srv.URL).ResolveContextWindow(context.Background())
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want a refused redirect", err)
	}
	if other.calls() != 0 {
		t.Error("the redirect was followed to another origin")
	}
}

// X-15: the first of max_model_len, context_length, context_window,
// max_context_length that holds a positive integer wins.
func TestWindowFieldPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		fields string
		want   int
	}{
		{"max_model_len first", `"max_model_len":10000,"context_length":20000,"context_window":30000,"max_context_length":40000`, 10000},
		{"context_length second", `"context_length":20000,"context_window":30000,"max_context_length":40000`, 20000},
		{"context_window third", `"context_window":30000,"max_context_length":40000`, 30000},
		{"max_context_length last", `"max_context_length":40000`, 40000},
		{"order in the JSON does not matter", `"max_context_length":40000,"context_length":20000`, 20000},
		{"a non-positive field is skipped", `"max_model_len":0,"context_length":"-5","context_window":"abc","max_context_length":40000`, 40000},
		{"null and object are skipped", `"max_model_len":null,"context_length":{"a":1},"context_window":30000`, 30000},
		{"a numeric string wins in order", `"max_model_len":"10000","context_length":20000`, 10000},
		{"training fields never fill in", `"n_ctx_train":999999,"max_model_len":10000`, 10000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetWindowCache(t)
			f := newFake(t, modelsHandler(`{"data":[{"id":"example-model",`+tc.fields+`}]}`))
			n, _, err := windowClient(t, f.srv.URL).ResolveContextWindow(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if want, _ := UsableWindow(int64(tc.want)); n != want {
				t.Errorf("n = %d, want %d (90%% of %d)", n, want, tc.want)
			}
		})
	}
}

func TestUsableWindowArithmetic(t *testing.T) {
	cases := []struct {
		m      int64
		want   int
		wantOK bool
	}{
		{32768, 29491, true},  // 29491.2 rounds down
		{8192, 7372, true},    // 7372.8 rounds down
		{100000, 90000, true}, // exact
		{10, 0, false},        // below the minimum
		{4095, 0, false},      // one below the minimum
		{4096, 4096, true},    // 90 % is 3686: the floor applies, and 4096 <= m
		{4500, 4096, true},    // 90 % is 4050: the floor applies
		{4551, 4096, true},    // 90 % is 4095.9: the floor applies
		{4552, 4096, true},    // 90 % is 4096.8: rounds down to the floor itself
		{4600, 4140, true},    // above the floor
	}
	for _, tc := range cases {
		n, ok := UsableWindow(tc.m)
		if n != tc.want || ok != tc.wantOK {
			t.Errorf("UsableWindow(%d) = %d, %v; want %d, %v", tc.m, n, ok, tc.want, tc.wantOK)
		}
	}
}

func TestUsableWindowHugeValueDoesNotOverflow(t *testing.T) {
	const m = int64(1) << 62
	n, ok := UsableWindow(m)
	if !ok || n <= 0 || int64(n) > m {
		t.Errorf("UsableWindow(%d) = %d, %v", m, n, ok)
	}
}

func TestContextWindowCacheOneProbe(t *testing.T) {
	resetWindowCache(t)
	f := newFake(t, modelsHandler(`{"data":[{"id":"example-model","max_model_len":8192}]}`))
	for i := range 2 {
		// A fresh client per call, like the per-call clients of the tools.
		n, src, err := windowClient(t, f.srv.URL).ResolveContextWindow(context.Background())
		if err != nil || n != 7372 || src != "endpoint, 90% of 8192" {
			t.Fatalf("call %d: %d %q %v", i, n, src, err)
		}
	}
	if got := f.calls(); got != 1 {
		t.Errorf("probes = %d, want 1 for two calls", got)
	}
	n, src, ok := ResolvedContextWindow(f.srv.URL+"/", windowModel)
	if !ok || n != 7372 || src != "endpoint, 90% of 8192" {
		t.Errorf("ResolvedContextWindow = %d %q %v", n, src, ok)
	}
	// Another model on the same endpoint is another entry.
	if _, _, ok := ResolvedContextWindow(f.srv.URL, "other-model"); ok {
		t.Error("the cache is not keyed by model")
	}
}

func TestContextWindowNoCacheAfterFailure(t *testing.T) {
	resetWindowCache(t)
	f := newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			writeJSON(w, 500, `down`)
			return
		}
		writeJSON(w, 200, `{"data":[{"id":"example-model","max_model_len":8192}]}`)
	})
	c := windowClient(t, f.srv.URL)
	if _, _, err := c.ResolveContextWindow(context.Background()); err == nil {
		t.Fatal("the first probe must fail")
	}
	n, _, err := c.ResolveContextWindow(context.Background())
	if err != nil || n != 7372 {
		t.Fatalf("second probe: %d %v", n, err)
	}
	if got := f.calls(); got != 2 {
		t.Errorf("probes = %d, want 2 (the failure was not cached)", got)
	}
}

// Concurrent first calls share one probe; when it fails, a waiter probes
// itself instead of inheriting the failure.
func TestContextWindowCoalescesConcurrentProbes(t *testing.T) {
	resetWindowCache(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		writeJSON(w, 200, `{"data":[{"id":"example-model","max_model_len":8192}]}`)
	})
	const callers = 8
	var wg sync.WaitGroup
	results := make([]int, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _, _ = windowClient(t, f.srv.URL).ResolveContextWindow(context.Background())
		}()
		if i == 0 {
			<-started // the first caller is the leader, mid-request
		}
	}
	time.Sleep(50 * time.Millisecond) // let the others queue behind it
	close(release)
	wg.Wait()
	if got := f.calls(); got != 1 {
		t.Errorf("probes = %d, want 1 for %d concurrent callers", got, callers)
	}
	for i, n := range results {
		if n != 7372 {
			t.Errorf("caller %d got %d", i, n)
		}
	}
}

func TestContextWindowWaiterHonorsContext(t *testing.T) {
	resetWindowCache(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		writeJSON(w, 200, `{"data":[{"id":"example-model","max_model_len":8192}]}`)
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = windowClient(t, f.srv.URL).ResolveContextWindow(context.Background())
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, err := windowClient(t, f.srv.URL).ResolveContextWindow(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiter err = %v, want its own deadline", err)
	}
	close(release)
	<-done
}

// The probe authenticates with the key of the client that makes it; the
// cache keeps the number only, and the key is in no log line.
func TestContextWindowKeyIsNeverCachedOrLogged(t *testing.T) {
	resetWindowCache(t)
	const perCallKey = "sk-PERCALL-0123456789abcdef"
	f := newFake(t, modelsHandler(`{"data":[{"id":"example-model","max_model_len":8192}]}`))
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := baseCfg(f.srv.URL)
	cfg.Model, cfg.ContextWindow = windowModel, 0
	c, err := New(cfg, config.NewSecret(perCallKey), logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ResolveContextWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.auths[0]; got != "Bearer "+perCallKey {
		t.Errorf("the probe used %q, want the per-call key", got)
	}
	// A later call with a different key is served from the cache: no request,
	// so the first key is not reused and the second one is not needed.
	c2, _ := New(cfg, config.NewSecret("sk-OTHER-0123456789abcdef"), logger)
	if _, _, err := c2.ResolveContextWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls() != 1 {
		t.Errorf("probes = %d, want 1", f.calls())
	}
	if !strings.Contains(logs.String(), "llm models probe") {
		t.Errorf("expected a debug line for the probe, got %q", logs.String())
	}
	for _, secret := range []string{perCallKey, "sk-OTHER", "Bearer"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log contains %q: %s", secret, logs.String())
		}
	}
	if strings.Contains(fmt.Sprintf("%+v", windows.vals), "sk-") {
		t.Error("the cache holds key material")
	}
}
