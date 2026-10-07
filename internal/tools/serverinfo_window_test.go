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
