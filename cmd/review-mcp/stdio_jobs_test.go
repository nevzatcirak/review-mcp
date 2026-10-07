package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestStdioEOFCancelsBackgroundJob: a pr_review still running in the
// background (X-16) neither keeps the process alive nor outlives it. When
// stdin ends, runWith returns and the job's LLM request is cancelled.
func TestStdioEOFCancelsBackgroundJob(t *testing.T) {
	g := newFakeGitea(t)
	var arrived, cancelled atomic.Int64
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// net/http notices a closed client connection (and cancels
		// r.Context()) only once the request body has been read.
		_, _ = io.Copy(io.Discard, r.Body)
		arrived.Add(1)
		<-r.Context().Done() // a model that never answers
		cancelled.Add(1)
	}))
	t.Cleanup(llm.Close)
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_BASE_URL"] = llm.URL + "/v1"

	inR, inW := io.Pipe()
	var out, errb lockedBuffer
	done := make(chan int, 1)
	go func() { done <- runWith(nil, inR, &out, &errb, loaderFor(env)) }()
	for _, l := range []string{
		handshake[0],
		handshake[1],
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pr_review","arguments":{"pr_url":"` + prURLOf(g, 7) + `","wait_seconds":0}}}`,
	} {
		if _, err := io.WriteString(inW, l+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, "the running answer", func() bool { return responseIDs(out.String())["2"] })
	if !strings.Contains(out.String(), `"status":"running"`) {
		t.Fatalf("pr_review did not answer with a job id:\n%s\n%s", out.String(), errb.String())
	}
	waitUntil(t, "the LLM request", func() bool { return arrived.Load() == 1 })

	_ = inW.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, stderr:\n%s", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a running job kept runWith alive after stdin EOF")
	}
	waitUntil(t, "the LLM request to be cancelled", func() bool { return cancelled.Load() == 1 })
}

// TestE2ESignalWithBackgroundJob: SIGTERM ends the real binary at once
// even while a background job waits on the model. (The job's LLM request
// ends with the process either way; the in-process cancellation by
// Jobs.Close is what TestStdioEOFCancelsBackgroundJob checks.)
func TestE2ESignalWithBackgroundJob(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the build-and-run end-to-end test in -short mode")
	}
	g := newFakeGitea(t)
	var arrived atomic.Int64
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		arrived.Add(1)
		<-r.Context().Done()
	}))
	t.Cleanup(llm.Close)
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_BASE_URL"] = llm.URL + "/v1"

	bin := buildBinary(t)
	cmd := exec.Command(bin) //nolint:gosec // G204: bin is the binary this test just built in t.TempDir()
	cmd.Env = envList(env)
	var stdout, stderr lockedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{
		handshake[0],
		handshake[1],
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pr_review","arguments":{"pr_url":"` + prURLOf(g, 7) + `","wait_seconds":0}}}`,
	} {
		if _, err := io.WriteString(stdin, l+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, "the running answer", func() bool { return strings.Contains(stdout.String(), `"status":"running"`) })
	waitUntil(t, "the LLM request", func() bool { return arrived.Load() == 1 })

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
		t.Fatal("a running job kept the server alive after SIGTERM")
	}
	if !strings.Contains(stderr.String(), "shutting down on signal") {
		t.Errorf("no signal shutdown line:\n%s", stderr.String())
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
