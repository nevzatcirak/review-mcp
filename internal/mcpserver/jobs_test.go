package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/jobs"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

// ---- fakes and helpers of the background-job tests (X-16) ----------------

// llmGate holds every chat completion of a gated fake LLM until released.
type llmGate struct {
	open      chan struct{}
	once      sync.Once
	arrived   chan struct{} // one value per request that reached the gate
	cancelled atomic.Int64  // requests whose context ended while held
}

func (g *llmGate) release() { g.once.Do(func() { close(g.open) }) }

// waitArrived waits until n requests reached the gate.
func (g *llmGate) waitArrived(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-g.arrived:
		case <-time.After(20 * time.Second):
			t.Fatalf("only %d of %d LLM requests arrived", i, n)
		}
	}
}

// newGatedLLMHost is newFakeLLMHost whose answers wait for the gate: a
// model that is slow until the test says otherwise.
func newGatedLLMHost(t *testing.T, status int, answer string) (*fakeLLMHost, *llmGate) {
	t.Helper()
	g := &llmGate{open: make(chan struct{}), arrived: make(chan struct{}, 64)}
	f := &fakeLLMHost{status: status, answer: answer}
	f.srv, f.conns = startCounted(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		g.arrived <- struct{}{}
		select {
		case <-g.open:
		case <-r.Context().Done():
			g.cancelled.Add(1)
			return
		}
		if f.status != http.StatusOK {
			http.Error(w, f.answer, f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": f.answer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
		})
	}))
	t.Cleanup(g.release)
	t.Cleanup(f.srv.Close)
	return f, g
}

// testClock is the injected clock of the job store.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// withJobs returns deps with a fresh job store (closed at the end of the
// test) on clock (nil: the wall clock).
func withJobs(t *testing.T, deps Deps, clock *testClock) Deps {
	t.Helper()
	var now func() time.Time
	if clock != nil {
		now = clock.Now
	}
	deps.Jobs = NewJobs(now)
	t.Cleanup(deps.Jobs.Close)
	return deps
}

// rawMsg is one JSON-RPC message as it crossed the stdio wire; Result keeps
// the exact bytes the server wrote.
type rawMsg struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// rawClient drives a server over the real stdio code path (RunIO) with
// hand-written JSON-RPC, so a test can compare tool results byte for byte.
type rawClient struct {
	t  *testing.T
	in *io.PipeWriter

	mu       sync.Mutex
	nextID   int
	waiters  map[int]chan rawMsg
	progress map[string][]string // progress token -> messages, in order
}

func startRaw(t *testing.T, deps Deps) *rawClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &rawClient{t: t, in: inW, waiters: map[int]chan rawMsg{}, progress: map[string][]string{}}
	done := make(chan error, 1)
	go func() { done <- RunIO(context.Background(), New(deps), inR, outW) }()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var m rawMsg
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				continue
			}
			if m.ID != nil && m.Method == "" {
				c.mu.Lock()
				ch := c.waiters[*m.ID]
				c.mu.Unlock()
				if ch != nil {
					ch <- m
				}
				continue
			}
			if m.Method == "notifications/progress" {
				var p struct {
					Token   any    `json:"progressToken"`
					Message string `json:"message"`
				}
				_ = json.Unmarshal(m.Params, &p)
				c.mu.Lock()
				key := fmt.Sprint(p.Token)
				c.progress[key] = append(c.progress[key], p.Message)
				c.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("RunIO did not return after stdin EOF")
		}
		_ = outR.Close()
	})
	c.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "raw-test-client", "version": "0"},
	})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return c
}

func (c *rawClient) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func (c *rawClient) call(method string, params any) rawMsg {
	c.t.Helper()
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rawMsg, 1)
	c.waiters[id] = ch
	c.mu.Unlock()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case m := <-ch:
		if m.Error != nil {
			c.t.Fatalf("%s: JSON-RPC error %s", method, m.Error)
		}
		return m
	case <-time.After(60 * time.Second):
		c.t.Fatalf("%s: no answer", method)
	}
	return rawMsg{}
}

// tool calls a tool and returns the raw result bytes; token, when not nil,
// is the progress token.
func (c *rawClient) tool(name string, args map[string]any, token any) json.RawMessage {
	c.t.Helper()
	params := map[string]any{"name": name, "arguments": args}
	if token != nil {
		params["_meta"] = map[string]any{"progressToken": token}
	}
	return c.call("tools/call", params).Result
}

func (c *rawClient) progressOf(token string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.progress[token]...)
}

// decodedResult is a decoded raw tool result.
type decodedResult struct {
	IsError bool `json:"isError"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Structured json.RawMessage `json:"structuredContent"`
}

func decodeRaw(t *testing.T, raw json.RawMessage) decodedResult {
	t.Helper()
	var r decodedResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("result does not decode: %v\n%s", err, raw)
	}
	return r
}

func (r decodedResult) text() string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

var jobIDRE = regexp.MustCompile(`^job_[a-z2-7]{26}$`)

// mustRunning checks a running result (P8 spec §2.2) and returns its job id.
func mustRunning(t *testing.T, raw json.RawMessage, noun string) RunningResult {
	t.Helper()
	r := decodeRaw(t, raw)
	if r.IsError {
		t.Fatalf("want a running result, got the error %q", r.text())
	}
	var st RunningResult
	dec := json.NewDecoder(bytes.NewReader(r.Structured))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil || st.Status != "running" {
		t.Fatalf("want the running status, got %s (%v)", r.Structured, err)
	}
	if !jobIDRE.MatchString(st.JobID) || st.Stage == "" || st.ElapsedSeconds < 0 {
		t.Fatalf("running status = %+v", st)
	}
	want := fmt.Sprintf("The %s is still running (stage: %s, %d s so far). Call `job_result` with job_id `%s` to get the result.",
		noun, st.Stage, st.ElapsedSeconds, st.JobID)
	if r.text() != want {
		t.Fatalf("running text = %q\nwant          %q", r.text(), want)
	}
	return st
}

var reviewedAtRE = regexp.MustCompile(`"reviewed_at":"([^"]*)"`)

// sameRun replaces the reviewed_at time of a with the one of b. That field
// is the wall-clock time of each run, so two runs agree on it only by
// chance; every other byte must be equal.
func sameRun(a, b json.RawMessage) json.RawMessage {
	ma, mb := reviewedAtRE.FindSubmatch(a), reviewedAtRE.FindSubmatch(b)
	if ma == nil || mb == nil {
		return a
	}
	return bytes.ReplaceAll(a, ma[1], mb[1])
}

func assertSameBytes(t *testing.T, what string, got, want json.RawMessage) {
	t.Helper()
	if !bytes.Equal(sameRun(got, want), want) {
		t.Errorf("%s differs from the synchronous result:\n got %s\nwant %s", what, got, want)
	}
}

// ---- tool surface ---------------------------------------------------------

func TestJobResultToolDefinition(t *testing.T) {
	cs := connect(t, withJobs(t, realDeps(validEnv(), nil), nil))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	var names []string
	for _, tl := range list.Tools {
		byName[tl.Name] = tl
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "job_result,pr_ask,pr_comment_create,pr_comment_reply,pr_comments,pr_info,pr_review,server_info" {
		t.Fatalf("stdio tools = %s", got)
	}

	tl := byName["job_result"]
	if tl.Description != "Returns the result of a long-running pr_review or pr_ask call that answered with a job_id, waiting up to wait_seconds for it to finish. If the result says the review is partial, tell the user how many files were not reviewed and never state that those files have no issues." {
		t.Errorf("description = %q", tl.Description)
	}
	a := tl.Annotations
	if a == nil || !a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v", a)
	}
	raw, _ := json.Marshal(tl.InputSchema)
	var in struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if strings.Join(in.Required, ",") != "job_id" || len(in.Properties) != 2 ||
		in.Properties["job_id"]["description"] == nil || in.Properties["wait_seconds"]["description"] == nil {
		t.Errorf("input schema = %s", raw)
	}
	var out struct {
		OneOf []any `json:"oneOf"`
	}
	rawOut, _ := json.Marshal(tl.OutputSchema)
	if err := json.Unmarshal(rawOut, &out); err != nil || len(out.OneOf) != 3 {
		t.Errorf("output schema is not review | answer | running: %s", rawOut)
	}

	// In stdio mode pr_review and pr_ask say that a slow call answers with a
	// job id and still publishes, and their output schema allows the
	// running status next to the unchanged result schema.
	for name, base := range map[string]string{"pr_review": prReviewDescription, "pr_ask": prAskDescription} {
		tl := byName[name]
		if !strings.HasPrefix(tl.Description, base+" ") ||
			!strings.Contains(tl.Description, "job_result") || !strings.Contains(tl.Description, "even if job_result is never called") {
			t.Errorf("%s description = %q", name, tl.Description)
		}
		rawOut, _ := json.Marshal(tl.OutputSchema)
		var o struct {
			Type  string           `json:"type"`
			OneOf []map[string]any `json:"oneOf"`
		}
		if err := json.Unmarshal(rawOut, &o); err != nil || o.Type != "object" || len(o.OneOf) != 2 {
			t.Errorf("%s output schema = %s", name, rawOut)
		}
	}
}

// ---- fast and slow runs ---------------------------------------------------

// TestJobFastRunIsByteIdentical: a run that finishes within wait_seconds
// answers exactly as the synchronous path does: the same text, the same
// structured content, byte for byte, and no job id.
func TestJobFastRunIsByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		tool, answer string
		args         func(pr string) map[string]any
	}{
		{"pr_review", goodAnswer, func(pr string) map[string]any { return map[string]any{"pr_url": pr, "max_findings": 3} }},
		{"pr_ask", askAnswer, func(pr string) map[string]any { return map[string]any{"pr_url": pr, "question": askQuestion} }},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			g, l := newFakeGiteaHost(t), newFakeLLMHost(t, 200, tc.answer)
			deps := realDeps(reviewEnv(g, l), nil)
			want := startRaw(t, deps).tool(tc.tool, tc.args(reviewPRURL(g)), nil)
			got := startRaw(t, withJobs(t, deps, nil)).tool(tc.tool, tc.args(reviewPRURL(g)), nil)
			if r := decodeRaw(t, want); r.IsError || len(r.Structured) == 0 {
				t.Fatalf("synchronous call failed: %s", want)
			}
			assertSameBytes(t, "fast result", got, want)
			if bytes.Contains(got, []byte("job_")) || bytes.Contains(got, []byte(`"running"`)) {
				t.Errorf("a fast result mentions a job: %s", got)
			}
		})
	}
}

// TestJobSlowRunAnswersWithJobID: a run longer than wait_seconds answers
// with the running status; job_result returns the original result once the
// run is done, byte for byte the synchronous one. [canary target: a job
// context tied to the request would end with the first answer.]
func TestJobSlowRunAnswersWithJobID(t *testing.T) {
	for _, tc := range []struct {
		tool, noun, answer string
		args               func(pr string) map[string]any
	}{
		{"pr_review", "review", goodAnswer, func(pr string) map[string]any { return map[string]any{"pr_url": pr, "wait_seconds": 1} }},
		{"pr_ask", "answer", askAnswer, func(pr string) map[string]any {
			return map[string]any{"pr_url": pr, "question": askQuestion, "wait_seconds": 1}
		}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			g := newFakeGiteaHost(t)
			l, gate := newGatedLLMHost(t, 200, tc.answer)
			deps := realDeps(reviewEnv(g, l), nil)
			c := startRaw(t, withJobs(t, deps, nil))

			start := time.Now()
			st := mustRunning(t, c.tool(tc.tool, tc.args(reviewPRURL(g)), nil), tc.noun)
			if time.Since(start) < time.Second {
				t.Errorf("answered after %v, before wait_seconds", time.Since(start))
			}
			if st.Stage != "calling model" {
				t.Errorf("stage = %q, want calling model", st.Stage)
			}
			// Polling without waiting gives the same status again.
			again := mustRunning(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 0}, nil), tc.noun)
			if again.JobID != st.JobID || again.Stage != "calling model" {
				t.Errorf("second status = %+v", again)
			}

			gate.release()
			got := c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, nil)
			want := startRaw(t, deps).tool(tc.tool, tc.args(reviewPRURL(g)), nil)
			if r := decodeRaw(t, want); r.IsError {
				t.Fatalf("synchronous call failed: %s", want)
			}
			assertSameBytes(t, "job_result", got, want)
			// A finished job answers again and again (idempotent).
			assertSameBytes(t, "second job_result", c.tool("job_result", map[string]any{"job_id": st.JobID}, nil), got)
			if n := gate.cancelled.Load(); n != 0 {
				t.Errorf("%d LLM requests were cancelled", n)
			}
		})
	}
}

// TestJobWaitSecondsZero: wait_seconds = 0 answers at once with the running
// status, from the argument or from llm.wait_seconds; the argument wins.
func TestJobWaitSecondsZero(t *testing.T) {
	g := newFakeGiteaHost(t)
	l, gate := newGatedLLMHost(t, 200, askAnswer)
	env := reviewEnv(g, l)
	env["REVIEW_MCP_LLM_WAIT_SECONDS"] = "0"
	c := startRaw(t, withJobs(t, realDeps(env, nil), nil))

	start := time.Now()
	st := mustRunning(t, c.tool("pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "wait_seconds": 0}, nil), "answer")
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("wait_seconds 0 answered after %v", d)
	}
	// From the configuration: no argument, llm.wait_seconds = 0.
	mustRunning(t, c.tool("pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion}, nil), "answer")

	// The argument overrides the configuration.
	gate.release()
	r := decodeRaw(t, c.tool("pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "wait_seconds": 30}, nil))
	if r.IsError || bytes.Contains(r.Structured, []byte(`"running"`)) || !strings.Contains(r.text(), askAnswerMarker) {
		t.Errorf("wait_seconds 30 over a config of 0 did not wait: %s", r.Structured)
	}
	if res := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, nil)); res.IsError {
		t.Errorf("first job: %q", res.text())
	}
}

// TestJobFailureKeepsTheFixedSentence: a run that fails in the background
// answers job_result with the tool error the synchronous call returns.
func TestJobFailureKeepsTheFixedSentence(t *testing.T) {
	g := newFakeGiteaHost(t)
	l, gate := newGatedLLMHost(t, http.StatusInternalServerError, "boom "+answerMarker)
	env := reviewEnv(g, l)
	env["REVIEW_MCP_LLM_MAX_RETRIES"] = "0"
	deps := realDeps(env, nil)
	c := startRaw(t, withJobs(t, deps, nil))

	st := mustRunning(t, c.tool("pr_review", map[string]any{"pr_url": reviewPRURL(g), "wait_seconds": 0}, nil), "review")
	gate.release()
	got := c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, nil)
	want := startRaw(t, deps).tool("pr_review", map[string]any{"pr_url": reviewPRURL(g)}, nil)
	r := decodeRaw(t, got)
	if !r.IsError || r.text() == "" || bytes.Contains(got, []byte(answerMarker)) {
		t.Fatalf("job_result of a failed run = %s", got)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("failed job_result differs from the synchronous error:\n got %s\nwant %s", got, want)
	}
}

// TestJobFifthConcurrentRunIsRefused: four runs may go on at once; the
// fifth call gets the fixed sentence and nothing of it runs.
func TestJobFifthConcurrentRunIsRefused(t *testing.T) {
	g := newFakeGiteaHost(t)
	l, gate := newGatedLLMHost(t, 200, askAnswer)
	c := startRaw(t, withJobs(t, realDeps(reviewEnv(g, l), nil), nil))
	args := map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "wait_seconds": 0}

	var ids []string
	for range jobs.MaxRunning {
		ids = append(ids, mustRunning(t, c.tool("pr_ask", args, nil), "answer").JobID)
	}
	gate.waitArrived(t, jobs.MaxRunning)
	r := decodeRaw(t, c.tool("pr_ask", args, nil))
	if !r.IsError || r.text() != "too many background jobs are running; wait for one to finish" {
		t.Fatalf("fifth call = %+v %q", r, r.text())
	}
	if n := len(l.recorded()); n != jobs.MaxRunning {
		t.Errorf("LLM requests = %d, want %d (the fifth must not run)", n, jobs.MaxRunning)
	}

	gate.release()
	for _, id := range ids {
		if r := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": id, "wait_seconds": 30}, nil)); r.IsError {
			t.Errorf("job %s: %q", id, r.text())
		}
	}
	if r := decodeRaw(t, c.tool("pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion}, nil)); r.IsError {
		t.Errorf("a call after the jobs finished: %q", r.text())
	}
}

// TestJobExpiresAfterTTL: a finished job's result is kept for 30 minutes
// (injected clock), then job_result says the id is unknown or expired.
func TestJobExpiresAfterTTL(t *testing.T) {
	g := newFakeGiteaHost(t)
	l, gate := newGatedLLMHost(t, 200, askAnswer)
	clock := newTestClock()
	c := startRaw(t, withJobs(t, realDeps(reviewEnv(g, l), nil), clock))

	st := mustRunning(t, c.tool("pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "wait_seconds": 0}, nil), "answer")
	// A running job never expires.
	clock.Advance(2 * jobs.TTL)
	gate.release()
	if r := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, nil)); r.IsError {
		t.Fatalf("job_result: %q", r.text())
	}
	clock.Advance(jobs.TTL - time.Second)
	if r := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID}, nil)); r.IsError {
		t.Fatalf("job_result within the TTL: %q", r.text())
	}
	clock.Advance(time.Second)
	r := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID}, nil))
	if !r.IsError || r.text() != "unknown or expired job_id" {
		t.Errorf("job_result after the TTL = %q", r.text())
	}
}

func TestJobResultUnknownIDAndBadWait(t *testing.T) {
	c := startRaw(t, withJobs(t, realDeps(validEnv(), nil), nil))
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"job_id": "job_aaaaaaaaaaaaaaaaaaaaaaaaaa"}, "unknown or expired job_id"},
		{map[string]any{"job_id": "not-a-job", "wait_seconds": 0}, "unknown or expired job_id"},
		{map[string]any{"job_id": "job_x", "wait_seconds": 601}, tools.InvalidWaitSecondsMessage},
		{map[string]any{"job_id": "job_x", "wait_seconds": -1}, tools.InvalidWaitSecondsMessage},
	} {
		r := decodeRaw(t, c.tool("job_result", tc.args, nil))
		if !r.IsError || r.text() != tc.want {
			t.Errorf("job_result %v = %q, want %q", tc.args, r.text(), tc.want)
		}
	}
}

// TestJobChecksFailBeforeAnyJob: what fails without network I/O fails at
// once, as before X-16, even with wait_seconds = 0; nothing is sent
// anywhere and no job slot is taken.
func TestJobChecksFailBeforeAnyJob(t *testing.T) {
	g := newFakeGiteaHost(t)
	l, gate := newGatedLLMHost(t, 200, askAnswer)
	c := startRaw(t, withJobs(t, realDeps(reviewEnv(g, l), nil), nil))
	for _, tc := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"pr_review", map[string]any{"pr_url": reviewPRURL(g), "max_findings": 0, "wait_seconds": 0}, tools.InvalidMaxFindingsMessage},
		{"pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": " ", "wait_seconds": 0}, ""},
		{"pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "output_language": "Turkish", "wait_seconds": 0}, tools.InvalidOutputLanguageMessage},
		{"pr_review", map[string]any{"pr_url": "https://elsewhere.example/octo/demo/pulls/7", "wait_seconds": 0}, ""},
		{"pr_review", map[string]any{"pr_url": reviewPRURL(g), "wait_seconds": 601}, tools.InvalidWaitSecondsMessage},
		{"pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "wait_seconds": -5}, tools.InvalidWaitSecondsMessage},
	} {
		r := decodeRaw(t, c.tool(tc.tool, tc.args, nil))
		if !r.IsError || (tc.want != "" && r.text() != tc.want) || strings.Contains(r.text(), "job_") {
			t.Errorf("%s %v = %q, want the immediate error %q", tc.tool, tc.args, r.text(), tc.want)
		}
	}
	if g.hits.Load() != 0 || l.hits.Load() != 0 {
		t.Errorf("requests after failed checks: provider %d, LLM %d", g.hits.Load(), l.hits.Load())
	}
	// Every job slot is still free.
	for range jobs.MaxRunning {
		mustRunning(t, c.tool("pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "wait_seconds": 0}, nil), "answer")
	}
	gate.release()
}

// TestJobPublishesWithoutJobResult: publish=true happens inside the run, so
// the comments are posted even though nobody asks for the result.
func TestJobPublishesWithoutJobResult(t *testing.T) {
	g := newFakeGiteaHost(t)
	l, gate := newGatedLLMHost(t, 200, goodAnswer)
	c := startRaw(t, withJobs(t, realDeps(reviewEnv(g, l), nil), nil))
	mustRunning(t, c.tool("pr_review", map[string]any{"pr_url": reviewPRURL(g), "publish": true, "wait_seconds": 0}, nil), "review")
	gate.waitArrived(t, 1)
	if len(g.overviewBodies()) != 0 {
		t.Fatal("published before the model answered")
	}
	gate.release()
	waitFor(t, func() bool { return len(g.overviewBodies()) == 1 })
	if b := g.overviewBodies()[0]; !strings.Contains(b, headerMarker) {
		t.Errorf("published overview lacks the finding")
	}
}

// TestJobShutdownCancelsRunningJobs: closing the store (SIGINT, SIGTERM,
// stdin EOF) cancels the run's context, so its LLM request ends too; a
// later call cannot start a job.
func TestJobShutdownCancelsRunningJobs(t *testing.T) {
	g := newFakeGiteaHost(t)
	l, gate := newGatedLLMHost(t, 200, askAnswer)
	deps := withJobs(t, realDeps(reviewEnv(g, l), nil), nil)
	c := startRaw(t, deps)
	st := mustRunning(t, c.tool("pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "wait_seconds": 0}, nil), "answer")
	gate.waitArrived(t, 1)

	deps.Jobs.Close()
	waitFor(t, func() bool { return gate.cancelled.Load() == 1 })
	r := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, nil))
	if !r.IsError || r.text() == "" {
		t.Errorf("job_result of a cancelled job = %q", r.text())
	}
	r = decodeRaw(t, c.tool("pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion}, nil))
	if !r.IsError || r.text() != jobs.ClosedSentence {
		t.Errorf("call after shutdown = %q", r.text())
	}
}

// TestJobProgressWhileWaiting: a waiting call turns the job's stages into
// its own progress notifications, and job_result does the same for its
// request.
func TestJobProgressWhileWaiting(t *testing.T) {
	g := newFakeGiteaHost(t)
	l, gate := newGatedLLMHost(t, 200, goodAnswer)
	c := startRaw(t, withJobs(t, realDeps(reviewEnv(g, l), nil), nil))

	st := mustRunning(t, c.tool("pr_review", map[string]any{"pr_url": reviewPRURL(g), "wait_seconds": 1}, "tok-call"), "review")
	if got := strings.Join(c.progressOf("tok-call"), "|"); got != "fetching|preparing diff|calling model" {
		t.Errorf("stages of the first call = %q", got)
	}
	gate.release()
	if r := decodeRaw(t, c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, "tok-poll")); r.IsError {
		t.Fatal(r.text())
	}
	if got := strings.Join(c.progressOf("tok-poll"), "|"); got != "fetching|preparing diff|calling model|rendering" {
		t.Errorf("stages of job_result = %q", got)
	}
	// Nothing is sent for the first request after it was answered.
	if got := strings.Join(c.progressOf("tok-call"), "|"); got != "fetching|preparing diff|calling model" {
		t.Errorf("progress for an answered request: %q", got)
	}
}

// TestJobConcurrentClients runs under -race: eight clients start jobs and
// poll the same and different jobs at once while the model is released.
func TestJobConcurrentClients(t *testing.T) {
	g := newFakeGiteaHost(t)
	l, gate := newGatedLLMHost(t, 200, askAnswer)
	deps := withJobs(t, realDeps(reviewEnv(g, l), nil), nil)
	const clients = 8
	sessions := make([]*mcp.ClientSession, clients)
	for i := range sessions {
		sessions[i] = connect(t, deps)
	}
	ids := make([]string, jobs.MaxRunning)
	var wg sync.WaitGroup
	for i := range jobs.MaxRunning {
		wg.Go(func() {
			res := callTool(t, sessions[i], "pr_ask", map[string]any{"pr_url": reviewPRURL(g), "question": askQuestion, "wait_seconds": 0})
			var st RunningResult
			decodeStructured(t, res, &st)
			ids[i] = st.JobID
		})
	}
	wg.Wait()
	go func() {
		time.Sleep(100 * time.Millisecond)
		gate.release()
	}()
	results := make([][]string, clients)
	for i := range clients {
		wg.Go(func() {
			for _, id := range []string{ids[i%len(ids)], ids[(i+1)%len(ids)]} {
				for {
					res := callTool(t, sessions[i], "job_result", map[string]any{"job_id": id, "wait_seconds": 0})
					if res.IsError {
						t.Errorf("job_result %s: %s", id, textOf(t, res))
						return
					}
					s := mustJSON(t, res.StructuredContent)
					if !strings.Contains(s, `"status":"running"`) {
						results[i] = append(results[i], id+" "+s)
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
		})
	}
	wg.Wait()
	byID := map[string]string{}
	for _, rs := range results {
		for _, r := range rs {
			id, s, _ := strings.Cut(r, " ")
			if prev, ok := byID[id]; ok && prev != s {
				t.Errorf("job %s answered differently to two clients", id)
			}
			byID[id] = s
		}
	}
	if len(byID) != jobs.MaxRunning {
		t.Errorf("finished jobs seen = %d", len(byID))
	}
}

// TestLeakJobs: [canary] markers in the question, the arguments and the PR
// content (title, branch, description, diff) go through background runs of
// pr_ask and pr_review; the logs carry none of them, only the job ids.
func TestLeakJobs(t *testing.T) {
	for _, tc := range []struct {
		tool, noun, answer string
		args               func(pr string) map[string]any
		resultMarker       string
	}{
		{"pr_ask", "answer", askAnswer, func(pr string) map[string]any {
			return map[string]any{"pr_url": pr, "question": askQuestion, "extra_instructions": "be brief " + argMarker, "wait_seconds": 0}
		}, askAnswerMarker},
		{"pr_review", "review", goodAnswer, func(pr string) map[string]any {
			return map[string]any{"pr_url": pr, "extra_instructions": "be brief " + argMarker, "wait_seconds": 0}
		}, headerMarker},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			var logs syncBuffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			g := newFakeGiteaHost(t)
			l, gate := newGatedLLMHost(t, 200, tc.answer)
			c := startRaw(t, withJobs(t, realDeps(reviewEnv(g, l), logger), nil))

			st := mustRunning(t, c.tool(tc.tool, tc.args(reviewPRURL(g)), "tok-leak"), tc.noun)
			gate.release()
			done := c.tool("job_result", map[string]any{"job_id": st.JobID, "wait_seconds": 30}, "tok-leak-2")
			if r := decodeRaw(t, done); r.IsError || !strings.Contains(r.text(), tc.resultMarker) {
				t.Fatalf("job_result = %s", done)
			}
			bodies := l.recorded()
			if len(bodies) != 1 {
				t.Fatalf("LLM requests = %d", len(bodies))
			}
			for _, m := range []string{descMarker, titleMarker, branchMarker, diffMarker, argMarker} {
				if !strings.Contains(bodies[0], m) {
					t.Fatalf("marker %q did not reach the LLM; the leak check would be vacuous", m)
				}
			}
			logText := logs.String()
			if !strings.Contains(logText, st.JobID) {
				t.Fatalf("the job id is not in the debug log; the check would be vacuous:\n%s", logText)
			}
			for _, m := range []string{questionMarker, descMarker, titleMarker, branchMarker, diffMarker, argMarker,
				askAnswerMarker, headerMarker, contentMarker, securityMarker, performanceMarker} {
				if strings.Contains(logText, m) {
					t.Errorf("marker %q leaked into the logs", m)
				}
			}
			for _, s := range allSecrets {
				if strings.Contains(logText, s) {
					t.Errorf("secret %q leaked into the logs", s)
				}
			}
		})
	}
}
