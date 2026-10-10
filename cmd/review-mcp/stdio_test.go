package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

const (
	fakeLLMKey      = "FAKE-llm-key-ZQ7X-do-not-leak"
	fakeGitea       = "FAKE-gitea-token-ZQ7X-do-not-leak"
	fakeBitbkt      = "FAKE-bitbucket-token-ZQ7X-do-not-leak"
	fakeGitHub      = "FAKE-github-token-ZQ7X-do-not-leak"
	fakeURLUserinfo = "FAKE-url-password-ZQ7X"
)

var allSecrets = []string{fakeLLMKey, fakeGitea, fakeBitbkt, fakeGitHub, fakeURLUserinfo}

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
		"REVIEW_MCP_GITHUB_BASE_URL":           "https://github.example.com",
		"REVIEW_MCP_GITHUB_TOKEN":              fakeGitHub,
		"REVIEW_MCP_NOT_A_REAL_SETTING":        "1", // yields a warning
	}
}

func invalidEnv() map[string]string {
	return map[string]string{
		"REVIEW_MCP_LLM_API_KEY":               fakeLLMKey,
		"REVIEW_MCP_GITEA_TOKEN":               fakeGitea,
		"REVIEW_MCP_BITBUCKET_SERVER_TOKEN":    fakeBitbkt,
		"REVIEW_MCP_GITHUB_TOKEN":              fakeGitHub,
		"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "ftp://user:" + fakeURLUserinfo + "@bitbucket.example.com",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW":        "12",
	}
}

func loaderFor(env map[string]string) configLoader {
	return func() (*config.Config, *config.Report, error) {
		return config.Load(config.MemSource{Env: env})
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// responseIDs returns the ids (raw JSON) of the complete response lines in
// out: objects with an id and a result or error. A trailing partial line is
// ignored; server-initiated requests (they carry a method) are not responses.
func responseIDs(out string) map[string]bool {
	ids := map[string]bool{}
	complete := out[:strings.LastIndexByte(out, '\n')+1]
	for _, l := range strings.Split(complete, "\n") {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(l), &m) != nil {
			continue
		}
		_, hasResult := m["result"]
		_, hasError := m["error"]
		if _, isReq := m["method"]; !isReq && m["id"] != nil && (hasResult || hasError) {
			ids[string(m["id"])] = true
		}
	}
	return ids
}

// requestID returns the raw JSON id of a request line, or "" for a
// notification (no id) or a line that is not a JSON object.
func requestID(line string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &m) != nil {
		return ""
	}
	return string(m["id"])
}

// session runs runWith over pipes like a real client: it sends the given
// JSON-RPC lines, and after each one waits until every request with an id sent
// so far has a response on stdout (notifications get none). Only then does it
// close stdin, because the SDK ends the session on EOF and drops in-flight
// responses (P1 decision 6: real clients keep stdin open). With no id-bearing
// lines it closes immediately. It returns the exit code with everything
// written to stdout/stderr. Every request line must therefore be answered; no
// current caller sends one that is not.
func session(t *testing.T, args []string, env map[string]string, lines ...string) (code int, stdout, stderr string) {
	t.Helper()
	inR, inW := io.Pipe()
	var out, errb lockedBuffer
	done := make(chan int, 1)
	go func() { done <- runWith(args, inR, &out, &errb, loaderFor(env)) }()

	var pending []string // ids of requests sent, in order
	deadline := time.Now().Add(20 * time.Second)
	for _, l := range lines {
		if _, err := io.WriteString(inW, l+"\n"); err != nil {
			t.Fatalf("write request: %v", err)
		}
		if id := requestID(l); id != "" {
			pending = append(pending, id)
		}
		for {
			got := responseIDs(out.String())
			var missing []string
			for _, id := range pending {
				if !got[id] {
					missing = append(missing, id)
				}
			}
			if len(missing) == 0 {
				break
			}
			if time.Now().After(deadline) {
				_ = inW.Close()
				t.Fatalf("no response before the deadline for request ids %v; stdout:\n%s\nstderr:\n%s", missing, out.String(), errb.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	_ = inW.Close()
	select {
	case code = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("runWith did not return after stdin EOF")
	}
	return code, out.String(), errb.String()
}

var handshake = []string{
	`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
	`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"server_info","arguments":{}}}`,
}

func decodeLines(t *testing.T, stdout string) []map[string]json.RawMessage {
	t.Helper()
	var msgs []map[string]json.RawMessage
	sc := bufio.NewScanner(strings.NewReader(stdout))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("stdout line is not a JSON object: %q", sc.Text())
		}
		if string(m["jsonrpc"]) != `"2.0"` {
			t.Fatalf("stdout line is not JSON-RPC: %q", sc.Text())
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func TestStdioValidConfig(t *testing.T) {
	for _, args := range [][]string{nil, {"stdio"}} {
		code, stdout, stderr := session(t, args, validEnv(), handshake...)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
		}
		if got := len(decodeLines(t, stdout)); got != 3 {
			t.Errorf("responses = %d, want 3:\n%s", got, stdout)
		}
		if !strings.Contains(stderr, "review-mcp starting") || !strings.Contains(stderr, "providers=\"[gitea bitbucket_server github]\"") {
			t.Errorf("startup line missing or wrong:\n%s", stderr)
		}
		if !strings.Contains(stderr, "level=WARN") {
			t.Errorf("warnings are not logged at warn:\n%s", stderr)
		}
	}
}

func TestStdioDegradedStart(t *testing.T) {
	code, stdout, stderr := session(t, nil, invalidEnv(), handshake...)
	if code != 0 {
		t.Fatalf("degraded start must still serve and exit 0, got %d:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "degraded mode") || !strings.Contains(stderr, "level=ERROR") {
		t.Errorf("aggregated error not logged:\n%s", stderr)
	}
	if !strings.Contains(stdout, "config_invalid") {
		t.Errorf("server_info does not report config_invalid:\n%s", stdout)
	}
}

// TestStdioLeak is the X-8 canary at the main level: neither stdout nor
// stderr may contain a secret, with a valid and with an invalid config.
func TestStdioLeak(t *testing.T) {
	for name, env := range map[string]map[string]string{"valid": validEnv(), "invalid": invalidEnv()} {
		t.Run(name, func(t *testing.T) {
			_, stdout, stderr := session(t, nil, env, handshake...)
			if stderr == "" || stdout == "" {
				t.Fatalf("vacuous check: stdout %d bytes, stderr %d bytes", len(stdout), len(stderr))
			}
			for _, s := range allSecrets {
				if strings.Contains(stdout, s) {
					t.Errorf("secret %q leaked to stdout", s)
				}
				if strings.Contains(stderr, s) {
					t.Errorf("secret %q leaked to stderr", s)
				}
			}
		})
	}
}

func TestLogLevelFromConfig(t *testing.T) {
	env := validEnv()
	env["REVIEW_MCP_LOG_LEVEL"] = "error"
	_, _, stderr := session(t, nil, env)
	if strings.Contains(stderr, "review-mcp starting") {
		t.Errorf("log.level=error should suppress the info startup line:\n%s", stderr)
	}
	// Invalid config falls back to info.
	_, _, stderr = session(t, nil, invalidEnv())
	if !strings.Contains(stderr, "review-mcp starting") {
		t.Errorf("invalid config should log at info:\n%s", stderr)
	}
}
