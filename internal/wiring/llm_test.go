package wiring

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// keyHost is a fake model endpoint that records, per model, the
// Authorization header of each request; models in fail answer 503.
type keyHost struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth map[string][]string
}

func newKeyHost(t *testing.T, fail ...string) *keyHost {
	h := &keyHost{auth: map[string][]string{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.auth[body.Model] = append(h.auth[body.Model], r.Header.Get("Authorization"))
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		for _, f := range fail {
			if f == body.Model {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *keyHost) headers(model string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.auth[model]
}

// TestFallbackKeyRule [canary target] (X-38, v2.1 spec V-6 §3.1, the leak
// guard), on the headers the fake servers received: a fallback on the
// primary's base URL without a key of its own gets the primary key; one on
// another host without its own key gets no Authorization header at all;
// a fallback's own key wins. The primary key never reaches another host.
func TestFallbackKeyRule(t *testing.T) {
	const primaryKey, ownKey = "sk-primary-key-0123", "sk-fallback-own-0456"
	primaryHost := newKeyHost(t, "primary-model", "same-host-model")
	otherHost := newKeyHost(t, "other-host-model")
	cfg := config.Defaults()
	cfg.LLM.BaseURL = primaryHost.srv.URL + "/v1"
	cfg.LLM.Model = "primary-model"
	cfg.LLM.ContextWindow = 32000
	cfg.LLM.MaxRetries = 0
	cfg.Secrets.LLMAPIKey = config.NewSecret(primaryKey)
	cfg.LLM.Fallback = []config.LLMFallback{
		{Model: "same-host-model", BaseURL: strings.ToUpper(primaryHost.srv.URL[:4]) + primaryHost.srv.URL[4:] + "/v1/", ContextWindow: 32000},
		{Model: "other-host-model", BaseURL: otherHost.srv.URL + "/v1", ContextWindow: 32000},
		{Model: "own-key-model", BaseURL: otherHost.srv.URL + "/v1", ContextWindow: 32000, APIKey: config.NewSecret(ownKey)},
	}
	c, err := NewLLM(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), "s", "u"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host  *keyHost
		model string
		want  string
	}{
		{primaryHost, "primary-model", "Bearer " + primaryKey},
		{primaryHost, "same-host-model", "Bearer " + primaryKey},
		{otherHost, "other-host-model", ""},
		{otherHost, "own-key-model", "Bearer " + ownKey},
	} {
		if got := tc.host.headers(tc.model); len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: Authorization %q, want %q", tc.model, got, tc.want)
		}
	}
	otherHost.mu.Lock()
	defer otherHost.mu.Unlock()
	for model, hs := range otherHost.auth {
		for _, h := range hs {
			if strings.Contains(h, primaryKey) {
				t.Errorf("the primary key reached another host (%s)", model)
			}
		}
	}
}
