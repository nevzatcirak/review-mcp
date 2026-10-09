package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildBinary compiles this package into t.TempDir() and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "review-mcp")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".") //nolint:gosec // G204: fixed arguments; bin is a path under t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func envList(env map[string]string) []string {
	out := []string{}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// TestE2EStdio drives the real binary over newline-delimited JSON-RPC.
func TestE2EStdio(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the build-and-run end-to-end test in -short mode")
	}
	bin := buildBinary(t)

	for name, tc := range map[string]struct {
		env      map[string]string
		wantInfo string // status expected in structuredContent
	}{
		"valid":   {validEnv(), "ok"},
		"invalid": {invalidEnv(), "config_invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin) //nolint:gosec // G204: bin is the binary this test just built in t.TempDir()
			cmd.Env = envList(tc.env)            // controlled: nothing else is inherited
			var stderr lockedBuffer
			cmd.Stderr = &stderr
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdoutPipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}

			lines := make(chan string, 16)
			var raw bytes.Buffer
			go func() {
				sc := bufio.NewScanner(stdoutPipe)
				sc.Buffer(make([]byte, 1<<20), 1<<20)
				for sc.Scan() {
					lines <- sc.Text()
				}
				close(lines)
			}()

			var all []string
			recv := func() map[string]json.RawMessage {
				t.Helper()
				select {
				case l, ok := <-lines:
					if !ok {
						t.Fatalf("stdout closed early; stderr:\n%s", stderr.String())
					}
					all = append(all, l)
					raw.WriteString(l + "\n")
					var m map[string]json.RawMessage
					if err := json.Unmarshal([]byte(l), &m); err != nil {
						t.Fatalf("stdout line is not JSON: %q", l)
					}
					return m
				case <-time.After(30 * time.Second):
					t.Fatalf("timed out waiting for a response; stderr:\n%s", stderr.String())
					return nil
				}
			}
			send := func(s string) {
				t.Helper()
				if _, err := stdin.Write([]byte(s + "\n")); err != nil {
					t.Fatal(err)
				}
			}

			send(handshake[0])
			init := recv()
			var ir struct {
				ServerInfo struct{ Name, Version string } `json:"serverInfo"`
			}
			mustResult(t, init, &ir)
			if ir.ServerInfo.Name != "review-mcp" {
				t.Errorf("server name = %q", ir.ServerInfo.Name)
			}

			send(handshake[1])
			send(handshake[2])
			var lr struct {
				Tools []struct{ Name string } `json:"tools"`
			}
			mustResult(t, recv(), &lr)
			var names []string
			for _, tl := range lr.Tools {
				names = append(names, tl.Name)
			}
			sort.Strings(names)
			if strings.Join(names, ",") != "job_result,pr_ask,pr_comment_create,pr_comment_reply,pr_comments,pr_describe,pr_improve,pr_info,pr_review,server_info" {
				t.Errorf("tools = %v", names)
			}

			send(handshake[3])
			var cr struct {
				Content []struct{ Type, Text string } `json:"content"`
				Struct  struct {
					Status   string   `json:"status"`
					Problems []string `json:"problems"`
				} `json:"structuredContent"`
				IsError bool `json:"isError"`
			}
			mustResult(t, recv(), &cr)
			if cr.IsError || len(cr.Content) != 1 || cr.Content[0].Type != "text" ||
				!strings.HasPrefix(cr.Content[0].Text, "# review-mcp server info") {
				t.Errorf("call result content = %+v", cr)
			}
			if cr.Struct.Status != tc.wantInfo {
				t.Errorf("status = %q, want %q", cr.Struct.Status, tc.wantInfo)
			}
			if tc.wantInfo == "config_invalid" && len(cr.Struct.Problems) == 0 {
				t.Error("degraded server_info lists no problems")
			}

			// Close stdin: the server must end the session and exit 0.
			_ = stdin.Close()
			for l := range lines { // drain; anything extra must be JSON-RPC too
				all = append(all, l)
				raw.WriteString(l + "\n")
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("exit after stdin EOF: %v; stderr:\n%s", err, stderr.String())
			}

			// [canary] stdout carries only JSON-RPC messages.
			for _, l := range all {
				var m map[string]json.RawMessage
				if err := json.Unmarshal([]byte(l), &m); err != nil || string(m["jsonrpc"]) != `"2.0"` {
					t.Errorf("non-JSON-RPC output on stdout: %q", l)
				}
			}
			// X-8: no secret on either stream.
			for _, s := range allSecrets {
				if strings.Contains(raw.String(), s) {
					t.Errorf("secret %q leaked to stdout", s)
				}
				if strings.Contains(stderr.String(), s) {
					t.Errorf("secret %q leaked to stderr", s)
				}
			}
			if stderr.String() == "" {
				t.Error("stderr is empty; expected the startup log")
			}
		})
	}
}

func mustResult(t *testing.T, msg map[string]json.RawMessage, into any) {
	t.Helper()
	if e, ok := msg["error"]; ok {
		t.Fatalf("JSON-RPC error: %s", e)
	}
	if err := json.Unmarshal(msg["result"], into); err != nil {
		t.Fatalf("decode result: %v (%s)", err, msg["result"])
	}
}

// TestE2ESignalShutdown checks that SIGTERM ends the server cleanly even while
// stdin is still open.
func TestE2ESignalShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the build-and-run end-to-end test in -short mode")
	}
	bin := buildBinary(t)
	cmd := exec.Command(bin) //nolint:gosec // G204: bin is the binary this test just built in t.TempDir()
	cmd.Env = envList(validEnv())
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait for the startup line so the signal handler is installed.
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stderr.String(), "review-mcp starting") {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit after SIGTERM: %v; stderr:\n%s", err, stderr.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("server did not exit after SIGTERM")
	}
}
