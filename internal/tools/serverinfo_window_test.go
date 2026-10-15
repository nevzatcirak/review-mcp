package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
)

// X-15: server_info reads the process-wide result of the probe and never
// makes a request itself.
func TestServerInfoContextWindowBeforeAndAfterResolution(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"example-model","max_model_len":32768}]}`))
	}))
	t.Cleanup(srv.Close)

	env := validEnv()
	delete(env, "REVIEW_MCP_LLM_CONTEXT_WINDOW")
	env["REVIEW_MCP_LLM_BASE_URL"] = srv.URL + "/v1"
	cfg, rep, err := load(env)
	if err != nil {
		t.Fatalf("an unset llm.context_window must validate: %v", err)
	}

	value := func() any { return ServerInfo(cfg, rep, nil).Config.Values["llm.context_window"].Value }
	if got := value(); got != "auto (endpoint)" {
		t.Errorf("before resolution: %v, want %q", got, "auto (endpoint)")
	}
	if hits != 0 {
		t.Fatalf("server_info made %d requests", hits)
	}

	c, err := llm.New(cfg.LLM, cfg.Secrets.LLMAPIKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ResolveContextWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := value(); got != "29491 (endpoint, 90% of 32768)" {
		t.Errorf("after resolution: %v", got)
	}
	md := RenderServerInfoMarkdown(ServerInfo(cfg, rep, nil))
	if !strings.Contains(md, "`llm.context_window` = `\"29491 (endpoint, 90% of 32768)\"`") {
		t.Errorf("the markdown does not show the resolved window:\n%s", md)
	}
	if hits != 1 {
		t.Errorf("requests = %d, want the one probe", hits)
	}
}

// A configured value is reported as the integer, never probed.
func TestServerInfoConfiguredContextWindow(t *testing.T) {
	cfg, rep, err := load(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	v := ServerInfo(cfg, rep, nil).Config.Values["llm.context_window"]
	if v.Value != 32000 || v.Source != config.OriginEnv {
		t.Errorf("configured window = %#v", v)
	}
}

// TestServerInfoFallbacks (X-38, v2.1 spec V-6 §3.1): server_info lists each
// fallback with its model, redacted base URL, key source and context window
// (configured, or "auto (endpoint)" until resolved), and never a key.
func TestServerInfoFallbacks(t *testing.T) {
	env := validEnv()
	env["REVIEW_MCP_LLM_FALLBACK_1_MODEL"] = "second-model"
	env["REVIEW_MCP_LLM_FALLBACK_2_MODEL"] = "third-model"
	env["REVIEW_MCP_LLM_FALLBACK_2_BASE_URL"] = "https://fallback.example/v1"
	env["REVIEW_MCP_LLM_FALLBACK_2_CONTEXT_WINDOW"] = "128000"
	env["REVIEW_MCP_LLM_FALLBACK_3_MODEL"] = "fourth-model"
	env["REVIEW_MCP_LLM_FALLBACK_3_BASE_URL"] = "https://fallback.example/v1"
	env["REVIEW_MCP_LLM_FALLBACK_3_API_KEY"] = "sk-fallback-own-key"
	cfg, rep, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	r := ServerInfo(cfg, rep, nil)
	want := []config.FallbackSummary{
		{Model: "second-model", BaseURL: r.Config.Values["llm.base_url"].Value.(string), Key: config.KeyPrimary, ContextWindow: ContextWindowAuto},
		{Model: "third-model", BaseURL: "https://fallback.example/v1", Key: config.KeyNone, ContextWindow: 128000},
		{Model: "fourth-model", BaseURL: "https://fallback.example/v1", Key: config.KeyOwn, ContextWindow: ContextWindowAuto},
	}
	if len(r.Config.Fallbacks) != 3 {
		t.Fatalf("fallbacks %+v", r.Config.Fallbacks)
	}
	for i := range want {
		if r.Config.Fallbacks[i] != want[i] {
			t.Errorf("fallback %d: %+v, want %+v", i+1, r.Config.Fallbacks[i], want[i])
		}
	}
	md := RenderServerInfoMarkdown(r)
	if !strings.Contains(md, "2. `third-model` at `https://fallback.example/v1`, key `none`, context window `128000`") {
		t.Errorf("markdown:\n%s", md)
	}
	if strings.Contains(md, "sk-fallback-own-key") {
		t.Error("the fallback key is in the markdown")
	}
}
