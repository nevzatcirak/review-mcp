package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/credentials"
)

func serveDeps(t *testing.T, extra map[string]string) Deps {
	t.Helper()
	env := map[string]string{
		"REVIEW_MCP_LLM_BASE_URL":       "https://llm.example.com/v1",
		"REVIEW_MCP_LLM_MODEL":          "example-model",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
		"REVIEW_MCP_GITEA_BASE_URL":     "https://your-gitea.example",
	}
	for k, v := range extra {
		env[k] = v
	}
	cfg, rep, err := config.LoadWith(config.MemSource{Env: env}, config.LoadOptions{Mode: config.ModeServe})
	if err != nil {
		t.Fatalf("serve config: %v", err)
	}
	return Deps{Config: cfg, Report: rep, Serve: true}
}

func reqWith(h http.Header) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: h}}
}

func TestConfigForStdioReturnsStartupConfig(t *testing.T) {
	d := depsFor(validEnv(), nil)
	h := http.Header{}
	h.Set(credentials.HeaderGiteaToken, "ignored-in-stdio")
	got, err := d.ConfigFor(context.Background(), reqWith(h))
	if err != nil || got != d.Config {
		t.Fatalf("stdio ConfigFor = %p, %v; want the startup config %p", got, err, d.Config)
	}
	if got.Secrets.GiteaToken.Reveal() != fakeGitea {
		t.Error("stdio secrets changed")
	}
}

func TestConfigForServeOverlaysRequestHeaders(t *testing.T) {
	d := serveDeps(t, map[string]string{"REVIEW_MCP_SERVE_ACCESS_TOKEN": "access-1"})
	before := *d.Config

	h1 := http.Header{}
	h1.Set(credentials.HeaderGiteaToken, "  gitea-one\t")
	h1.Set(credentials.HeaderLLMAPIKey, "llm-one")
	h2 := http.Header{}
	h2.Set(credentials.HeaderGiteaToken, "gitea-two")
	h2.Set(credentials.HeaderBitbucketServerToken, "bbs-two")

	c1, err := d.ConfigFor(context.Background(), reqWith(h1))
	if err != nil {
		t.Fatal(err)
	}
	c2, err := d.ConfigFor(context.Background(), reqWith(h2))
	if err != nil {
		t.Fatal(err)
	}
	if c1 == d.Config || c2 == d.Config || c1 == c2 {
		t.Fatal("serve ConfigFor must return a fresh per-call copy")
	}
	if c1.Secrets.GiteaToken.Reveal() != "gitea-one" || c1.Secrets.LLMAPIKey.Reveal() != "llm-one" || c1.Secrets.BitbucketServerToken.IsSet() {
		t.Errorf("c1 secrets wrong: gitea=%q", c1.Secrets.GiteaToken.Reveal())
	}
	if c2.Secrets.GiteaToken.Reveal() != "gitea-two" || c2.Secrets.BitbucketServerToken.Reveal() != "bbs-two" || c2.Secrets.LLMAPIKey.IsSet() {
		t.Errorf("c2 secrets wrong")
	}
	if c1.Secrets.ServeAccessToken.Reveal() != "access-1" {
		t.Error("access token not carried into the per-call config")
	}
	if !reflect.DeepEqual(before, *d.Config) || d.Config.Secrets.GiteaToken.IsSet() {
		t.Error("ConfigFor modified the startup config")
	}
	// No request metadata: no credentials, no error.
	c3, err := d.ConfigFor(context.Background(), &mcp.CallToolRequest{})
	if err != nil || c3.Secrets.GiteaToken.IsSet() || c3.Secrets.LLMAPIKey.IsSet() {
		t.Errorf("request without headers: %v", err)
	}
}

func TestConfigForServerKeySource(t *testing.T) {
	d := serveDeps(t, map[string]string{
		"REVIEW_MCP_SERVE_LLM_KEY_SOURCE": "server",
		"REVIEW_MCP_LLM_API_KEY":          "server-key",
		"REVIEW_MCP_SERVE_ACCESS_TOKEN":   "access",
	})
	h := http.Header{}
	h.Set(credentials.HeaderLLMAPIKey, "header-key-ignored")
	h.Set(credentials.HeaderGiteaToken, "gitea")
	c, err := d.ConfigFor(context.Background(), reqWith(h))
	if err != nil {
		t.Fatal(err)
	}
	if c.Secrets.LLMAPIKey.Reveal() != "server-key" || c.Secrets.GiteaToken.Reveal() != "gitea" {
		t.Errorf("llm key = %q", c.Secrets.LLMAPIKey.Reveal())
	}
}

func TestConfigForMalformedHeader(t *testing.T) {
	d := serveDeps(t, nil)
	h := http.Header{}
	h.Set(credentials.HeaderGiteaToken, "bad\x01value-MARKER")
	_, err := d.ConfigFor(context.Background(), reqWith(h))
	var me *credentials.MalformedError
	if !errors.As(err, &me) || err.Error() != "malformed credential header: X-Review-MCP-Gitea-Token" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "MARKER") {
		t.Error("value echoed")
	}
}

// TestSDKInfoRecordsDemotedInServe: in serve mode the SDK's per-request info
// records ("server connecting", "server session connected | disconnected")
// are logged at debug, so the access line is the only info line per request.
func TestSDKInfoRecordsDemotedInServe(t *testing.T) {
	var infoBuf, debugBuf syncBuffer
	atInfo := sdkLogger(slog.New(slog.NewTextHandler(&infoBuf, &slog.HandlerOptions{Level: slog.LevelInfo})), true)
	atDebug := sdkLogger(slog.New(slog.NewTextHandler(&debugBuf, &slog.HandlerOptions{Level: slog.LevelDebug})), true)
	for _, l := range []*slog.Logger{atInfo, atDebug} {
		l.Info("server connecting")
		l.Info("server session connected", "session_id", "")
		l.Info("server session disconnected", "session_id", "")
		l.Warn("calling tools/call: boom", "error", "boom")
		l.Error("server connect error", "error", "x")
	}
	info := infoBuf.String()
	if strings.Contains(info, "server connecting") || strings.Contains(info, "session connected") || strings.Contains(info, "level=INFO") {
		t.Errorf("info-level capture holds demoted records:\n%s", info)
	}
	if !strings.Contains(info, "level=WARN") || !strings.Contains(info, "level=ERROR") {
		t.Errorf("warnings and errors must keep their level:\n%s", info)
	}
	debug := debugBuf.String()
	for _, want := range []string{`level=DEBUG msg="server connecting"`, `level=DEBUG msg="server session connected"`, `level=DEBUG msg="server session disconnected"`} {
		if !strings.Contains(debug, want) {
			t.Errorf("debug capture lacks %q:\n%s", want, debug)
		}
	}
	// stdio keeps the SDK's levels.
	var stdioBuf syncBuffer
	sdkLogger(slog.New(slog.NewTextHandler(&stdioBuf, nil)), false).Info("server session connected", "session_id", "s")
	if !strings.Contains(stdioBuf.String(), "level=INFO") {
		t.Errorf("stdio record demoted: %s", stdioBuf.String())
	}
}
