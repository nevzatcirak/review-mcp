package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

const (
	testKey      = "sk-TESTKEY-0123456789abcdef"
	promptMarker = "PROMPT-MARKER-7f3a"
	respMarker   = "RESPONSE-MARKER-9c1d"
)

// fake is an httptest LLM endpoint that records request bodies and headers.
type fake struct {
	mu      sync.Mutex
	bodies  [][]byte
	auths   []string
	paths   []string
	handler func(n int, w http.ResponseWriter, r *http.Request)
	srv     *httptest.Server
}

func newFake(t *testing.T, h func(n int, w http.ResponseWriter, r *http.Request)) *fake {
	t.Helper()
	f := &fake{handler: h}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 0, 1024)
		buf := bytes.NewBuffer(b)
		_, _ = buf.ReadFrom(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, buf.Bytes())
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.paths = append(f.paths, r.URL.Path)
		n := len(f.bodies)
		f.mu.Unlock()
		f.handler(n, w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func (f *fake) body(i int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

func okJSON(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
	})
	return string(b)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body)) //nolint:gosec // test fake; the body is a fixed fixture, not user input
}

func baseCfg(url string) config.LLM {
	return config.LLM{BaseURL: url, Model: "test-model", ContextWindow: 8192, TimeoutSeconds: 30, MaxRetries: 1}
}

// fakeSleep records requested delays without sleeping.
type fakeSleep struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *fakeSleep) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.delays = append(s.delays, d)
	s.mu.Unlock()
	return ctx.Err()
}

func newClient(t *testing.T, cfg config.LLM, logger *slog.Logger, fs *fakeSleep) *Client {
	t.Helper()
	opts := []Option{}
	if fs != nil {
		opts = append(opts, WithSleeper(fs.sleep))
	}
	c, err := New(cfg, config.NewSecret(testKey), logger, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestCompleteSuccess(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("hello")) })
	c := newClient(t, baseCfg(f.srv.URL+"/v1/"), nil, nil)
	resp, err := c.Complete(context.Background(), "sys", "usr")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello" || resp.Truncated || resp.Usage != (Usage{11, 7, 18}) {
		t.Fatalf("unexpected response %+v", resp)
	}
	if f.paths[0] != "/v1/chat/completions" {
		t.Fatalf("path %q", f.paths[0])
	}
	if f.auths[0] != "Bearer "+testKey {
		t.Fatalf("auth header %q", f.auths[0])
	}
	var req map[string]any
	if err := json.Unmarshal(f.body(0), &req); err != nil {
		t.Fatal(err)
	}
	msgs := req["messages"].([]any)
	if req["model"] != "test-model" || len(msgs) != 2 ||
		msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != "sys" ||
		msgs[1].(map[string]any)["role"] != "user" || msgs[1].(map[string]any)["content"] != "usr" {
		t.Fatalf("unexpected body %s", f.body(0))
	}
}

// [canary] unset sampling parameters must be absent from the JSON.
func TestUnsetSamplingParamsAbsent(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("x")) })
	c := newClient(t, baseCfg(f.srv.URL), nil, nil)
	if _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(f.body(0), &req); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"temperature", "seed", "reasoning_effort", "max_tokens", "max_completion_tokens"} {
		if _, ok := req[k]; ok {
			t.Errorf("unset key %q present in request: %s", k, f.body(0))
		}
	}
	if len(req) != 2 {
		t.Errorf("expected only model and messages, got %s", f.body(0))
	}
}

func TestSetSamplingParamsSentIncludingZero(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("x")) })
	cfg := baseCfg(f.srv.URL)
	temp, seed, mt, re := 0.0, int64(0), 512, "low"
	cfg.Temperature, cfg.Seed, cfg.MaxOutputTokens, cfg.ReasoningEffort = &temp, &seed, &mt, &re
	c := newClient(t, cfg, nil, nil)
	if _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(f.body(0), &req); err != nil {
		t.Fatal(err)
	}
	if v, ok := req["temperature"]; !ok || v != 0.0 {
		t.Errorf("temperature: %v %v", v, ok)
	}
	if v, ok := req["seed"]; !ok || v != 0.0 {
		t.Errorf("seed: %v %v", v, ok)
	}
	if req["max_tokens"] != 512.0 || req["reasoning_effort"] != "low" {
		t.Errorf("unexpected body %s", f.body(0))
	}
	if _, ok := req["max_completion_tokens"]; ok {
		t.Error("max_completion_tokens must not be sent")
	}
}

func TestRequestsAreIndependent(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("x")) })
	c := newClient(t, baseCfg(f.srv.URL), nil, nil)
	if _, err := c.Complete(context.Background(), "s1", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), "s2", "u2"); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(f.body(1), []byte("u1")) || bytes.Contains(f.body(0), []byte("u2")) {
		t.Fatalf("request bodies share state: %s | %s", f.body(0), f.body(1))
	}
}

func TestContentPartsArray(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, `{"choices":[{"message":{"content":[{"type":"text","text":"foo"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"bar"}],"reasoning_content":"ignored"},"finish_reason":"stop"}]}`)
	})
	c := newClient(t, baseCfg(f.srv.URL), nil, nil)
	resp, err := c.Complete(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "foobar" {
		t.Fatalf("content %q", resp.Content)
	}
}

func TestFinishReasonLength(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, `{"choices":[{"message":{"content":"partial"},"finish_reason":"length"}]}`)
	})
	c := newClient(t, baseCfg(f.srv.URL), nil, nil)
	resp, err := c.Complete(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Truncated || resp.Content != "partial" {
		t.Fatalf("%+v", resp)
	}
}

func TestErrorClasses(t *testing.T) {
	cfg := func(url string) config.LLM {
		c := baseCfg(url)
		c.MaxRetries = 0
		temp := 0.2
		mt := 100
		c.Temperature, c.MaxOutputTokens = &temp, &mt
		return c
	}
	tests := []struct {
		name   string
		status int
		body   string
		want   error
		class  ErrorClass
		hint   string
	}{
		{"401", 401, `{"error":"bad key"}`, ErrAuth, ClassAuth, "REVIEW_MCP_LLM_API_KEY"},
		{"403", 403, ``, ErrAuth, ClassAuth, "REVIEW_MCP_LLM_API_KEY"},
		{"404", 404, ``, ErrNotFound, ClassNotFound, "llm.base_url / llm.model"},
		{"429", 429, ``, ErrRateLimited, ClassRateLimited, ""},
		{"500", 500, ``, ErrUpstream, ClassUpstream, ""},
		{"503", 503, ``, ErrUpstream, ClassUpstream, ""},
		{"400 generic", 400, `{"error":"unsupported parameter"}`, ErrBadRequest, ClassBadRequest, "llm.max_output_tokens, llm.temperature"},
		{"422", 422, `{"detail":"x"}`, ErrBadRequest, ClassBadRequest, "llm.max_output_tokens, llm.temperature"},
		{"400 context length", 400, `{"error":{"code":"context_length_exceeded","message":"too long"}}`, ErrContextTooLong, ClassContextTooLong, "llm.context_window"},
		{"400 context window", 400, `This model's Maximum CONTEXT WINDOW is 8192`, ErrContextTooLong, ClassContextTooLong, "llm.context_window"},
		{"413 context", 413, `context length exceeded`, ErrContextTooLong, ClassContextTooLong, "llm.context_window"},
		{"400 context only", 400, `{"error":"context"}`, ErrBadRequest, ClassBadRequest, "llm.max_output_tokens, llm.temperature"},
		{"200 empty content", 200, `{"choices":[{"message":{"content":""}}]}`, ErrProtocol, ClassProtocol, ""},
		{"200 null content", 200, `{"choices":[{"message":{"content":null}}]}`, ErrProtocol, ClassProtocol, ""},
		{"200 no choices", 200, `{"choices":[]}`, ErrProtocol, ClassProtocol, ""},
		{"200 not json", 200, `<html>nope</html>`, ErrProtocol, ClassProtocol, ""},
		{"200 wrong shape", 200, `{"choices":"x"}`, ErrProtocol, ClassProtocol, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, tc.status, tc.body) })
			c := newClient(t, cfg(f.srv.URL), nil, &fakeSleep{})
			_, err := c.Complete(context.Background(), "s", "u")
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v got %v", tc.want, err)
			}
			var le *Error
			if !errors.As(err, &le) || le.Class != tc.class || le.Hint != tc.hint {
				t.Fatalf("class/hint: %+v", le)
			}
			if tc.status >= 300 && le.Status != tc.status {
				t.Fatalf("status %d", le.Status)
			}
			if le.UserMessage() != err.Error() {
				t.Fatal("UserMessage differs from Error")
			}
			if f.calls() != 1 {
				t.Fatalf("calls %d, want 1 (maxRetries=0)", f.calls())
			}
		})
	}
}

func TestBadRequestNoSamplingHint(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 400, `{}`) })
	c := newClient(t, baseCfg(f.srv.URL), nil, &fakeSleep{})
	_, err := c.Complete(context.Background(), "s", "u")
	var le *Error
	if !errors.As(err, &le) || le.Hint != "" || strings.Contains(err.Error(), "check") {
		t.Fatalf("unexpected %v", err)
	}
}

func TestNonRetryableClassesNotRetried(t *testing.T) {
	for _, st := range []int{400, 401, 403, 404, 422} {
		f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, st, `{}`) })
		cfg := baseCfg(f.srv.URL)
		cfg.MaxRetries = 3
		fs := &fakeSleep{}
		c := newClient(t, cfg, nil, fs)
		if _, err := c.Complete(context.Background(), "s", "u"); err == nil {
			t.Fatal("expected error")
		}
		if f.calls() != 1 || len(fs.delays) != 0 {
			t.Errorf("status %d: calls=%d sleeps=%v", st, f.calls(), fs.delays)
		}
	}
	// protocol (empty content) is not retried either
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, `{"choices":[{"message":{"content":""}}]}`)
	})
	cfg := baseCfg(f.srv.URL)
	cfg.MaxRetries = 3
	c := newClient(t, cfg, nil, &fakeSleep{})
	_, _ = c.Complete(context.Background(), "s", "u")
	if f.calls() != 1 {
		t.Errorf("protocol retried: %d calls", f.calls())
	}
}

func TestRetryCountsAndBackoff(t *testing.T) {
	for _, st := range []int{429, 500, 502} {
		for _, retries := range []int{0, 1, 3, 5} {
			f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, st, `{}`) })
			cfg := baseCfg(f.srv.URL)
			cfg.MaxRetries = retries
			fs := &fakeSleep{}
			c := newClient(t, cfg, nil, fs)
			_, err := c.Complete(context.Background(), "s", "u")
			if err == nil {
				t.Fatal("expected error")
			}
			if f.calls() != retries+1 {
				t.Errorf("status %d retries %d: %d calls", st, retries, f.calls())
			}
			want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}[:retries]
			if !slices.Equal(fs.delays, want) {
				t.Errorf("delays %v want %v", fs.delays, want)
			}
		}
	}
}

func TestRetrySucceedsAfterFailure(t *testing.T) {
	f := newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		if n == 1 {
			writeJSON(w, 503, `{}`)
			return
		}
		writeJSON(w, 200, okJSON("fine"))
	})
	cfg := baseCfg(f.srv.URL)
	temp := 0.5
	cfg.Temperature = &temp
	fs := &fakeSleep{}
	c := newClient(t, cfg, nil, fs)
	resp, err := c.Complete(context.Background(), "s", "u")
	if err != nil || resp.Content != "fine" {
		t.Fatalf("%v %v", resp, err)
	}
	if f.calls() != 2 || !bytes.Equal(f.body(0), f.body(1)) {
		t.Fatalf("calls %d; retry body differs", f.calls())
	}
}

func TestRetryAfter(t *testing.T) {
	fixed := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"seconds", "7", 7 * time.Second},
		{"capped at 30s", "3600", 30 * time.Second},
		{"exactly cap", "30", 30 * time.Second},
		{"http date", fixed.Add(12 * time.Second).UTC().Format(http.TimeFormat), 12 * time.Second},
		{"http date far", fixed.Add(2 * time.Hour).UTC().Format(http.TimeFormat), 30 * time.Second},
		{"http date past falls back", fixed.Add(-time.Hour).UTC().Format(http.TimeFormat), time.Second},
		{"zero falls back", "0", time.Second},
		{"negative falls back", "-5", time.Second},
		{"garbage falls back", "soon", time.Second},
		{"absent falls back", "", time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
				if n == 1 {
					if tc.header != "" {
						w.Header().Set("Retry-After", tc.header)
					}
					writeJSON(w, 429, `{}`)
					return
				}
				writeJSON(w, 200, okJSON("ok"))
			})
			fs := &fakeSleep{}
			c, err := New(baseCfg(f.srv.URL), config.NewSecret(testKey), nil, WithSleeper(fs.sleep), WithClock(func() time.Time { return fixed }))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Complete(context.Background(), "s", "u"); err != nil {
				t.Fatal(err)
			}
			if len(fs.delays) != 1 || fs.delays[0] != tc.want {
				t.Fatalf("delays %v want %v", fs.delays, tc.want)
			}
		})
	}
}

func TestContextCancellationStopsRetrying(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 500, `{}`) })
	cfg := baseCfg(f.srv.URL)
	cfg.MaxRetries = 5
	ctx, cancel := context.WithCancel(context.Background())
	sleeps := 0
	c, err := New(cfg, config.NewSecret(testKey), nil, WithSleeper(func(ctx context.Context, _ time.Duration) error {
		sleeps++
		cancel()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(ctx, "s", "u")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if f.calls() != 1 || sleeps != 1 {
		t.Fatalf("calls=%d sleeps=%d", f.calls(), sleeps)
	}
}

func TestAlreadyCanceledContext(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("x")) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newClient(t, baseCfg(f.srv.URL), nil, &fakeSleep{})
	_, err := c.Complete(ctx, "s", "u")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestTimeoutClassifiedAndRetried(t *testing.T) {
	release := make(chan struct{})
	f := newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		writeJSON(w, 200, okJSON("late"))
	})
	defer close(release)
	cfg := baseCfg(f.srv.URL)
	cfg.MaxRetries = 2
	fs := &fakeSleep{}
	c := newClient(t, cfg, nil, fs)
	c.timeout = 50 * time.Millisecond
	_, err := c.Complete(context.Background(), "s", "u")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
	if f.calls() != 3 || len(fs.delays) != 2 {
		t.Fatalf("calls=%d delays=%v", f.calls(), fs.delays)
	}
}

func TestTransportError(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("x")) })
	url := f.srv.URL
	f.srv.Close()
	cfg := baseCfg(url)
	cfg.MaxRetries = 2
	fs := &fakeSleep{}
	c := newClient(t, cfg, nil, fs)
	_, err := c.Complete(context.Background(), "s", "u")
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("got %v", err)
	}
	if len(fs.delays) != 2 {
		t.Fatalf("transport errors must be retried: %v", fs.delays)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error leaks the URL: %v", err)
	}
}

func TestResponseCap(t *testing.T) {
	big := okJSON(strings.Repeat("a", 4096))
	t.Run("content-length", func(t *testing.T) {
		f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, big) })
		c := newClient(t, baseCfg(f.srv.URL), nil, &fakeSleep{})
		c.maxBytes = 1024
		_, err := c.Complete(context.Background(), "s", "u")
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("got %v", err)
		}
		if f.calls() != 1 {
			t.Fatalf("oversized response retried: %d", f.calls())
		}
	})
	t.Run("chunked", func(t *testing.T) {
		f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			fl := w.(http.Flusher)
			for i := 0; i < 8; i++ {
				_, _ = w.Write([]byte(big[:len(big)/8]))
				fl.Flush()
			}
		})
		c := newClient(t, baseCfg(f.srv.URL), nil, &fakeSleep{})
		c.maxBytes = 1024
		_, err := c.Complete(context.Background(), "s", "u")
		if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("default is 8 MiB", func(t *testing.T) {
		if MaxResponseBytes != 8<<20 {
			t.Fatal("cap changed")
		}
	})
	t.Run("exactly at cap is accepted", func(t *testing.T) {
		f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, big) })
		c := newClient(t, baseCfg(f.srv.URL), nil, nil)
		c.maxBytes = int64(len(big))
		if _, err := c.Complete(context.Background(), "s", "u"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRedirectPinning(t *testing.T) {
	other := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("elsewhere")) })
	t.Run("cross-origin refused without a request", func(t *testing.T) {
		f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", other.srv.URL+"/chat/completions")
			w.WriteHeader(307)
		})
		c := newClient(t, baseCfg(f.srv.URL), nil, &fakeSleep{})
		_, err := c.Complete(context.Background(), "s", "u")
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("got %v", err)
		}
		if other.calls() != 0 {
			t.Fatalf("redirect target received %d requests (key leak)", other.calls())
		}
	})
	t.Run("outside base path refused", func(t *testing.T) {
		f := newFake(t, func(_ int, w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/other/chat/completions")
			w.WriteHeader(307)
		})
		c := newClient(t, baseCfg(f.srv.URL+"/v1"), nil, &fakeSleep{})
		_, err := c.Complete(context.Background(), "s", "u")
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("same base followed with body and auth", func(t *testing.T) {
		f := newFake(t, func(n int, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/chat/completions" {
				w.Header().Set("Location", "/v1/moved/chat/completions")
				w.WriteHeader(307)
				return
			}
			writeJSON(w, 200, okJSON("followed"))
		})
		c := newClient(t, baseCfg(f.srv.URL+"/v1"), nil, nil)
		resp, err := c.Complete(context.Background(), "s", "u")
		if err != nil || resp.Content != "followed" {
			t.Fatalf("%v %v", resp, err)
		}
		if f.calls() != 2 || !bytes.Equal(f.body(0), f.body(1)) || f.auths[1] != "Bearer "+testKey {
			t.Fatalf("redirect lost body or auth")
		}
	})
}

func TestNewValidation(t *testing.T) {
	good := baseCfg("https://llm.example.com/v1")
	if _, err := New(good, config.NewSecret(testKey), nil); err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		name string
		cfg  config.LLM
		key  config.Secret
	}{
		{"empty url", baseCfg(""), config.NewSecret(testKey)},
		{"userinfo", baseCfg("https://u:p@llm.example.com"), config.NewSecret(testKey)},
		{"scheme", baseCfg("ftp://llm.example.com"), config.NewSecret(testKey)},
		{"no key", good, config.NewSecret("")},
		{"no model", config.LLM{BaseURL: "https://llm.example.com"}, config.NewSecret(testKey)},
	}
	for _, tc := range bad {
		_, err := New(tc.cfg, tc.key, nil)
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if strings.Contains(err.Error(), "u:p") || strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: error leaks input: %v", tc.name, err)
		}
	}
}

func TestErrorFormatAndClassOf(t *testing.T) {
	e := &Error{Class: ClassAuth, Status: 401, Hint: hintAPIKey}
	want := "the LLM endpoint rejected the credentials (HTTP 401); check REVIEW_MCP_LLM_API_KEY"
	if e.Error() != want {
		t.Fatalf("%q", e.Error())
	}
	if c, ok := ClassOf(fmt.Errorf("wrap: %w", e)); !ok || c != ClassAuth {
		t.Fatal("ClassOf")
	}
	if _, ok := ClassOf(errors.New("x")); ok {
		t.Fatal("ClassOf on foreign error")
	}
	if errors.Is(e, ErrNotFound) || !errors.Is(e, ErrAuth) {
		t.Fatal("Is")
	}
	// Every class has a sentence.
	for _, c := range []ErrorClass{ClassAuth, ClassNotFound, ClassRateLimited, ClassContextTooLong, ClassBadRequest, ClassUpstream, ClassTimeout, ClassTransport, ClassProtocol} {
		if sentences[c] == "" {
			t.Errorf("no sentence for %s", c)
		}
	}
}

// [canary] the leak test: neither the API key nor a prompt or response
// marker may appear in captured debug logs or in any error string, across
// the success path and every error path. The fake server echoes the key,
// the prompt marker and a response marker in its error bodies, so a client
// that logged or returned a body would be caught.
func TestNoLeaksInLogsOrErrors(t *testing.T) {
	// Each status is exercised with a body that does and one that does not
	// mention the context length, so both the context_too_long and the
	// bad_request paths see an echoing body.
	echo := func(status int, wording string) func(int, http.ResponseWriter, *http.Request) {
		return func(_ int, w http.ResponseWriter, r *http.Request) {
			writeJSON(w, status, fmt.Sprintf(`{"error":{"message":"%s %s %s %s %s"}}`,
				wording, testKey, promptMarker, respMarker, r.Header.Get("Authorization")))
		}
	}
	type scenario struct {
		name    string
		handler func(int, http.ResponseWriter, *http.Request)
		mutate  func(*Client)
		closed  bool
	}
	scenarios := []scenario{
		{name: "success", handler: func(_ int, w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, okJSON("answer "+respMarker))
		}},
		{name: "parts", handler: func(_ int, w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, `{"choices":[{"message":{"content":[{"type":"text","text":"`+respMarker+`"}]},"finish_reason":"length"}]}`)
		}},
		{name: "empty", handler: func(_ int, w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, `{"choices":[{"message":{"content":""}}],"echo":"`+respMarker+promptMarker+testKey+`"}`)
		}},
		{name: "not json", handler: func(_ int, w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, `{"choices":[ `+respMarker+promptMarker+testKey)
		}},
		{name: "too big", handler: func(_ int, w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, 200, okJSON(respMarker+strings.Repeat("a", 5000)))
		}, mutate: func(c *Client) { c.maxBytes = 512 }},
		{name: "redirect", handler: func(_ int, w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", "http://other.example.com/?access_token="+testKey)
			w.WriteHeader(302)
		}},
		{name: "timeout", handler: func(_ int, w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}, mutate: func(c *Client) { c.timeout = 30 * time.Millisecond }},
		{name: "transport", handler: func(int, http.ResponseWriter, *http.Request) {}, closed: true},
	}
	for _, st := range []int{400, 401, 403, 404, 413, 422, 429, 500, 503, 302} {
		scenarios = append(scenarios,
			scenario{name: fmt.Sprintf("status %d context wording", st), handler: echo(st, "context length window")},
			scenario{name: fmt.Sprintf("status %d other wording", st), handler: echo(st, "invalid parameter")})
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			f := newFake(t, sc.handler)
			url := f.srv.URL
			if sc.closed {
				f.srv.Close()
			}
			cfg := baseCfg(url)
			cfg.MaxRetries = 2
			temp, seed, mt, re := 0.2, int64(7), 100, "low"
			cfg.Temperature, cfg.Seed, cfg.MaxOutputTokens, cfg.ReasoningEffort = &temp, &seed, &mt, &re
			c := newClient(t, cfg, logger, &fakeSleep{})
			if sc.mutate != nil {
				sc.mutate(c)
			}
			resp, err := c.Complete(context.Background(), "system "+promptMarker, "user "+promptMarker)
			var out []string
			out = append(out, logs.String())
			if err != nil {
				out = append(out, err.Error(), fmt.Sprintf("%v|%+v|%#v", err, err, err))
				var le *Error
				if errors.As(err, &le) {
					out = append(out, le.UserMessage())
				}
			}
			if resp != nil {
				// the response content legitimately carries respMarker; only
				// the logs and errors are checked for it below
				out = append(out, "")
			}
			for _, s := range out {
				for _, secret := range []string{testKey, promptMarker, respMarker, "Bearer", "Authorization"} {
					if strings.Contains(s, secret) {
						t.Fatalf("%q leaked into %q", secret, s)
					}
				}
			}
			if !strings.Contains(logs.String(), "llm request") {
				t.Fatalf("expected a debug log line, got %q", logs.String())
			}
		})
	}
}

func TestDebugLogShape(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("x")) })
	c := newClient(t, baseCfg(f.srv.URL), logger, nil)
	if _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"method=POST", "status=200", "prompt_tokens=11", "completion_tokens=7", "total_tokens=18", "duration_ms="} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log missing %q: %s", want, logs.String())
		}
	}
}
