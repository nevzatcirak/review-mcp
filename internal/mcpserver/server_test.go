package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

const (
	fakeLLMKey      = "FAKE-llm-key-ZQ7X-do-not-leak"
	fakeGitea       = "FAKE-gitea-token-ZQ7X-do-not-leak"
	fakeBitbkt      = "FAKE-bitbucket-token-ZQ7X-do-not-leak"
	fakeURLUserinfo = "FAKE-url-password-ZQ7X"
)

var allSecrets = []string{fakeLLMKey, fakeGitea, fakeBitbkt, fakeURLUserinfo}

func validEnv() map[string]string {
	return map[string]string{
		"REVIEW_MCP_LLM_BASE_URL":              "https://llm.example.com/v1",
		"REVIEW_MCP_LLM_MODEL":                 "example-model",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW":        "32000",
		"REVIEW_MCP_LLM_API_KEY":               fakeLLMKey,
		"REVIEW_MCP_GITEA_BASE_URL":            "https://your-gitea.example",
		"REVIEW_MCP_GITEA_TOKEN":               fakeGitea,
		"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com/bb",
		"REVIEW_MCP_BITBUCKET_SERVER_TOKEN":    fakeBitbkt,
	}
}

// invalidEnv still carries every secret but fails validation.
func invalidEnv() map[string]string {
	return map[string]string{
		"REVIEW_MCP_LLM_API_KEY":               fakeLLMKey,
		"REVIEW_MCP_GITEA_TOKEN":               fakeGitea,
		"REVIEW_MCP_BITBUCKET_SERVER_TOKEN":    fakeBitbkt,
		"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "ftp://user:" + fakeURLUserinfo + "@bitbucket.example.com",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW":        "12",
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func depsFor(env map[string]string, logger *slog.Logger) Deps {
	cfg, rep, err := config.Load(config.MemSource{Env: env})
	return Deps{Config: cfg, Report: rep, LoadErr: err, Logger: logger}
}

func connect(t *testing.T, deps Deps) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	st, ct := mcp.NewInMemoryTransports()
	srv := New(deps)
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callServerInfo(t *testing.T, cs *mcp.ClientSession) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "server_info"})
	if err != nil {
		t.Fatalf("call server_info: %v", err)
	}
	if res.IsError {
		t.Fatalf("server_info returned a tool error: %+v", res.Content)
	}
	return res
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

func structuredOf(t *testing.T, res *mcp.CallToolResult) tools.ServerInfoResult {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out tools.ServerInfoResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structured content does not decode: %v\n%s", err, raw)
	}
	return out
}

func TestInitializeAndListTools(t *testing.T) {
	cs := connect(t, depsFor(validEnv(), nil))

	init := cs.InitializeResult()
	if init == nil || init.ServerInfo == nil {
		t.Fatal("no initialize result")
	}
	if init.ServerInfo.Name != "review-mcp" || init.ServerInfo.Version == "" {
		t.Errorf("server info = %+v", init.ServerInfo)
	}

	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "server_info" {
		t.Fatalf("tools = %+v, want exactly server_info", list.Tools)
	}
	tool := list.Tools[0]
	if !strings.HasSuffix(tool.Description, ".") || strings.Count(tool.Description, ". ") != 0 {
		t.Errorf("description should be one sentence: %q", tool.Description)
	}
	a := tool.Annotations
	if a == nil || !a.ReadOnlyHint || !a.IdempotentHint ||
		a.DestructiveHint == nil || *a.DestructiveHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v", a)
	}
	if tool.InputSchema == nil || tool.OutputSchema == nil {
		t.Fatalf("schemas missing: in=%v out=%v", tool.InputSchema, tool.OutputSchema)
	}
	schema, err := json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatal(err)
	}
	if s.Type != "object" {
		t.Errorf("output schema type = %q", s.Type)
	}
	for _, k := range []string{"name", "version", "commit", "go_version", "status", "problems", "warnings", "providers", "config"} {
		if _, ok := s.Properties[k]; !ok {
			t.Errorf("output schema lacks property %q", k)
		}
	}
}

func TestCallServerInfoTextAndStructured(t *testing.T) {
	cs := connect(t, depsFor(validEnv(), nil))
	// ListTools first so the client caches the output schema and validates
	// the structured content of the call against it.
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	res := callServerInfo(t, cs)

	text := textOf(t, res)
	if !strings.HasPrefix(text, "# review-mcp server info") {
		t.Errorf("text is not the markdown rendering:\n%s", text)
	}
	got := structuredOf(t, res)
	if got.Name != "review-mcp" || got.Status != tools.StatusOK {
		t.Errorf("structured = %+v", got)
	}
	if len(got.Providers) != 2 {
		t.Errorf("providers = %+v", got.Providers)
	}
	if got.Config.Secrets["llm.api_key"] != "set" || len(got.Config.Values) == 0 {
		t.Errorf("config summary incomplete: %+v", got.Config.Secrets)
	}
	// The text must be exactly what the tools package renders for it.
	if want := tools.RenderServerInfoMarkdown(got); want != text {
		t.Errorf("text and structured content disagree")
	}
}

func TestCallServerInfoDegraded(t *testing.T) {
	cs := connect(t, depsFor(invalidEnv(), nil))
	if _, err := cs.ListTools(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	res := callServerInfo(t, cs)
	got := structuredOf(t, res)
	if got.Status != tools.StatusConfigInvalid || len(got.Problems) < 2 {
		t.Fatalf("degraded result = status %q problems %v", got.Status, got.Problems)
	}
	if !strings.Contains(textOf(t, res), "## Configuration problems") {
		t.Error("markdown lacks the problems section")
	}
}

func TestEmptyListsSerializeAsArrays(t *testing.T) {
	cs := connect(t, Deps{Config: config.Defaults()})
	res := callServerInfo(t, cs)
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"problems", "warnings", "providers"} {
		if string(m[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, m[k])
		}
	}
}

// TestLeakNoSecretsAnywhere is the X-8 canary: with every secret configured,
// a full session (initialize, list, call) must not expose any secret value in
// tool results (text and structured), schemas, or logs, in both the valid and
// the degraded (invalid config that still carries secrets) path.
func TestLeakNoSecretsAnywhere(t *testing.T) {
	for name, env := range map[string]map[string]string{"valid": validEnv(), "degraded": invalidEnv()} {
		t.Run(name, func(t *testing.T) {
			var logs syncBuffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			deps := depsFor(env, logger)
			// Mimic what main logs about the config, so the degraded-start
			// error path is part of the captured logs.
			if deps.LoadErr != nil {
				logger.Error("configuration invalid", "error", deps.LoadErr)
			}
			for _, w := range deps.Report.Warnings {
				logger.Warn(w)
			}

			cs := connect(t, deps)
			list, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			res := callServerInfo(t, cs)

			var surfaces []string
			for _, v := range []any{list, res} {
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				surfaces = append(surfaces, string(raw))
			}
			surfaces = append(surfaces, textOf(t, res), logs.String())
			for _, secret := range allSecrets {
				for _, s := range surfaces {
					if strings.Contains(s, secret) {
						t.Errorf("secret %q leaked into %.80q...", secret, s)
					}
				}
			}
			if logs.String() == "" {
				t.Error("log capture is empty; the leak check would be vacuous")
			}
		})
	}
}

func TestRunIOEndsCleanlyOnEOF(t *testing.T) {
	srv := New(depsFor(validEnv(), nil))
	inR, inW := newPipe()
	outR, outW := newPipe()
	done := make(chan error, 1)
	go func() { done <- RunIO(context.Background(), srv, inR, outW) }()

	_ = inW.Close() // stdin EOF without any traffic
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunIO returned %v on EOF, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunIO did not return after stdin EOF")
	}
	_ = outR.Close()
}
