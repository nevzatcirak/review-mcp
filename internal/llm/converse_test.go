package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// callsJSON is an answer calling tools; calls is the raw tool_calls value
// and content the raw content value ("" for none).
func callsJSON(content, calls, finish string) string {
	s := `{"choices":[{"message":{"role":"assistant"`
	if content != "" {
		s += `,"content":` + content
	}
	if calls != "" {
		s += `,"tool_calls":` + calls
	}
	return s + `},"finish_reason":"` + finish + `"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
}

// readFileCall is one well-formed call of a tool named read_file.
const readFileCall = `[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]`

// convFake is an endpoint answering with answer(n): a status and a body.
func convFake(t *testing.T, answer func(n int) (int, string)) *fake {
	return newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		st, body := answer(n)
		writeJSON(w, st, body)
	})
}

var testTools = []Tool{
	{Name: "read_file", Description: "Read a file.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)},
	{Name: "list_dir", Description: "List a directory.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
}

// startConv is a first turn: the system text and one user message.
func startConv() Conversation {
	return Conversation{System: "sys", Messages: []Message{{Role: RoleUser, Content: "usr"}}, Tools: testTools, ToolChoice: ToolChoiceAuto}
}

// TestConverseRequest: the wire shape of a conversation (§1 item 1): the
// tools as functions with their schema, the tool choice, an assistant
// message with tool calls (content null) and a tool result with its call
// id; the sampling parameters as for Complete.
func TestConverseRequest(t *testing.T) {
	f := convFake(t, func(int) (int, string) { return 200, okJSON("done") })
	cfg := baseCfg(f.srv.URL)
	temp := 0.0
	cfg.Temperature = &temp
	c := newClient(t, cfg, nil, nil)
	conv := startConv()
	conv.Messages = append(conv.Messages,
		Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", Arguments: `{"path":"a.go"}`}}},
		ToolResult("call_1", "package a"),
		Message{Role: RoleAssistant, Content: "thinking", ToolCalls: []ToolCall{{ID: "call_2", Name: "list_dir", Arguments: ""}}},
		ToolResult("call_2", "a.go"))
	turn, err := c.Converse(context.Background(), conv)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Text != "done" || len(turn.ToolCalls) != 0 || turn.Usage != (Usage{11, 7, 18}) {
		t.Fatalf("turn %+v", turn)
	}
	want := `{"model":"test-model","messages":[` +
		`{"role":"system","content":"sys"},` +
		`{"role":"user","content":"usr"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},` +
		`{"role":"tool","content":"package a","tool_call_id":"call_1"},` +
		`{"role":"assistant","content":"thinking","tool_calls":[{"id":"call_2","type":"function","function":{"name":"list_dir","arguments":""}}]},` +
		`{"role":"tool","content":"a.go","tool_call_id":"call_2"}],` +
		`"temperature":0,` +
		`"tools":[{"type":"function","function":{"name":"read_file","description":"Read a file.","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}},` +
		`{"type":"function","function":{"name":"list_dir","description":"List a directory.","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],` +
		`"tool_choice":"auto"}`
	if got := string(f.body(0)); got != want {
		t.Fatalf("request\n got %s\nwant %s", got, want)
	}

	// "none" is sent with the tools; without tools no choice is sent.
	conv.ToolChoice = ToolChoiceNone
	if _, err := c.Converse(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(f.body(1)), `"tool_choice":"none"}`) {
		t.Fatalf("tool choice none: %s", f.body(1))
	}
	conv.Tools = nil
	if _, err := c.Converse(context.Background(), conv); err != nil {
		t.Fatal(err)
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(f.body(2), &req); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tools", "tool_choice"} {
		if _, ok := req[k]; ok {
			t.Errorf("%s sent without tools: %s", k, f.body(2))
		}
	}
}

// TestConverseToolCalls: the calls of an answer, their arguments as the
// model sent them and as a JSON object when they are one of at most 4 KiB
// (§1 item 5); calls that cannot be answered are dropped.
func TestConverseToolCalls(t *testing.T) {
	big := `{"path":"` + strings.Repeat("a", MaxToolArgumentBytes) + `"}`
	bigJSON, _ := json.Marshal(big)
	calls := `[` +
		`{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\",\"start_line\":3}"}},` +
		`{"id":"c2","type":"function","function":{"name":"list_dir","arguments":{"path":"src"}}},` +
		`{"id":"c3","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}},` +
		`{"id":"c4","type":"function","function":{"name":"read_file","arguments":"[1,2]"}},` +
		`{"id":"c5","type":"function","function":{"name":"read_file","arguments":` + string(bigJSON) + `}},` +
		`{"id":"c6","function":{"name":"list_dir","arguments":""}},` +
		`{"id":"c7","type":"function","function":{"name":"list_dir"}},` +
		`{"type":"function","function":{"name":"read_file","arguments":"{}"}},` +
		`{"id":"c9","type":"function","function":{"arguments":"{}"}},` +
		`{"id":"c10","type":"retrieval","function":{"name":"read_file","arguments":"{}"}},` +
		`42,"x",null,` +
		`{"id":"c11","type":"function","function":{"name":"search","arguments":"{\"text\":\"x\"} "}}]`
	f := convFake(t, func(int) (int, string) { return 200, callsJSON("", calls, "tool_calls") })
	c := newClient(t, baseCfg(f.srv.URL), nil, nil)
	turn, err := c.Converse(context.Background(), startConv())
	if err != nil {
		t.Fatal(err)
	}
	type got struct{ id, name, args, obj string }
	var gs []got
	for _, tc := range turn.ToolCalls {
		gs = append(gs, got{tc.ID, tc.Name, tc.Arguments, string(tc.Args)})
	}
	want := []got{
		{"c1", "read_file", `{"path":"a.go","start_line":3}`, `{"path":"a.go","start_line":3}`},
		{"c2", "list_dir", `{"path":"src"}`, `{"path":"src"}`},
		{"c3", "read_file", `{"path":`, ""},
		{"c4", "read_file", `[1,2]`, ""},
		{"c5", "read_file", big, ""},
		{"c6", "list_dir", "", "{}"},
		{"c7", "list_dir", "", "{}"},
		{"c11", "search", `{"text":"x"} `, `{"text":"x"}`},
	}
	if !slices.Equal(gs, want) {
		t.Fatalf("calls\n got %q\nwant %q", gs, want)
	}
	if turn.Text != "" || turn.Truncated || turn.Usage != (Usage{3, 2, 5}) {
		t.Fatalf("turn %+v", turn)
	}
	// The turn goes back into the conversation as it came.
	m := turn.Message()
	if m.Role != RoleAssistant || len(m.ToolCalls) != len(want) || m.ToolCalls[4].Arguments != big {
		t.Fatalf("message %+v", m)
	}
}

// TestConverseGarbageToolCalls: a tool_calls value that is not a list of
// calls yields no calls; the answer's text still counts, and an answer
// with neither text nor calls is an unexpected response.
func TestConverseGarbageToolCalls(t *testing.T) {
	for _, calls := range []string{`"read_file"`, `{"id":"c1"}`, `[]`, `[1]`, `null`, `[{"id":"c1"}]`} {
		t.Run(calls, func(t *testing.T) {
			f := convFake(t, func(n int) (int, string) {
				if n == 1 {
					return 200, callsJSON(`"the answer"`, calls, "stop")
				}
				return 200, callsJSON(`""`, calls, "stop")
			})
			c := newClient(t, baseCfg(f.srv.URL), nil, nil)
			turn, err := c.Converse(context.Background(), startConv())
			if err != nil || turn.Text != "the answer" || turn.ToolCalls != nil {
				t.Fatalf("with text: %+v %v", turn, err)
			}
			_, err = c.Converse(context.Background(), startConv())
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("without text: %v", err)
			}
		})
	}
}

// TestConverseTruncated: finish_reason "length" marks the turn.
func TestConverseTruncated(t *testing.T) {
	f := convFake(t, func(int) (int, string) { return 200, callsJSON(`"part"`, "", "length") })
	c := newClient(t, baseCfg(f.srv.URL), nil, nil)
	turn, err := c.Converse(context.Background(), startConv())
	if err != nil || !turn.Truncated || turn.Text != "part" {
		t.Fatalf("%+v %v", turn, err)
	}
}

// TestConverseRetries: Converse has Complete's retries: a server error is
// retried, an auth error is not.
func TestConverseRetries(t *testing.T) {
	f := convFake(t, func(n int) (int, string) {
		if n == 1 {
			return 503, `{}`
		}
		return 200, callsJSON("", readFileCall, "tool_calls")
	})
	fs := &fakeSleep{}
	c := newClient(t, baseCfg(f.srv.URL), nil, fs)
	turn, err := c.Converse(context.Background(), startConv())
	if err != nil || len(turn.ToolCalls) != 1 || f.calls() != 2 || len(fs.delays) != 1 {
		t.Fatalf("%+v %v calls %d delays %v", turn, err, f.calls(), fs.delays)
	}
	g := convFake(t, func(int) (int, string) { return 401, `{}` })
	c = newClient(t, baseCfg(g.srv.URL), nil, &fakeSleep{})
	if _, err := c.Converse(context.Background(), startConv()); !errors.Is(err, ErrAuth) || g.calls() != 1 {
		t.Fatalf("auth: %v calls %d", err, g.calls())
	}
}

// TestConverseRateLimitMidConversation: a 429 on a later turn cools the
// endpoint down; the turn waits for it in the limiter (X-37) and is sent
// again, with the whole conversation.
func TestConverseRateLimitMidConversation(t *testing.T) {
	clk := newFakeClock()
	rf := &rateFake{}
	rf.fake = newFake(t, func(n int, w http.ResponseWriter, _ *http.Request) {
		switch n {
		case 1:
			writeJSON(w, 200, callsJSON("", readFileCall, "tool_calls"))
		case 2:
			w.Header().Set("Retry-After", "5")
			writeJSON(w, 429, `{}`)
		default:
			writeJSON(w, 200, okJSON("final"))
		}
	})
	c := rateClient(t, rf, clk, nil, 2, 60)
	conv := startConv()
	turn, err := c.Converse(t.Context(), conv)
	if err != nil {
		t.Fatal(err)
	}
	conv.Messages = append(conv.Messages, turn.Message(), ToolResult("call_1", "package a"))
	turn, err = c.Converse(t.Context(), conv)
	if err != nil || turn.Text != "final" {
		t.Fatalf("%+v %v", turn, err)
	}
	if got := clk.recorded(); !slices.Equal(got, []time.Duration{5 * time.Second}) {
		t.Fatalf("sleeps %v", got)
	}
	if n, _ := c.RateLimitWaits(); n != 1 || rf.calls() != 3 {
		t.Fatalf("waits %d calls %d", n, rf.calls())
	}
	if string(rf.body(1)) != string(rf.body(2)) || !strings.Contains(string(rf.body(2)), `"tool_call_id":"call_1"`) {
		t.Fatalf("the retry differs:\n%s\n%s", rf.body(1), rf.body(2))
	}
}

// chainConvClient is a chain model on f: no retries of its own.
func chainConvClient(t *testing.T, f *fake, model string) *Client {
	t.Helper()
	cfg := baseCfg(f.srv.URL)
	cfg.Model, cfg.MaxRetries, cfg.MaxRateRetries = model, 0, 0
	c, err := New(cfg, config.NewSecret(testKey), nil, WithSleeper((&fakeSleep{}).sleep), withRegistry(&registry{}))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestChainConverseFirstTurnMovesOn: a primary that fails before the first
// turn moves the conversation to the fallback, as Complete does.
func TestChainConverseFirstTurnMovesOn(t *testing.T) {
	p := convFake(t, func(int) (int, string) { return 500, `{}` })
	fb := convFake(t, func(int) (int, string) { return 200, callsJSON("", readFileCall, "tool_calls") })
	ch := NewChain(chainConvClient(t, p, "primary"), []*Client{chainConvClient(t, fb, "fallback")}, 1)
	turn, err := ch.Converse(t.Context(), startConv())
	if err != nil || len(turn.ToolCalls) != 1 {
		t.Fatalf("%+v %v", turn, err)
	}
	u := ch.Use()
	if p.calls() != 1 || fb.calls() != 1 || u.Model != "fallback" || u.FallbackReason != ReasonServerError {
		t.Fatalf("calls %d/%d use %+v", p.calls(), fb.calls(), u)
	}
}

// TestChainConverseRestart: a later turn stays on the model that started
// the conversation. When it fails in a way that moves on, the error wraps
// ErrRestart and the run moves to the fallback, which then gets the
// conversation from its start; with no later model, or a failure that does
// not move on, the error is returned as it is (§1 item 3).
func TestChainConverseRestart(t *testing.T) {
	later := func(conv Conversation, turn Turn) Conversation {
		conv.Messages = append(slices.Clone(conv.Messages), turn.Message(), ToolResult("call_1", "package a"))
		return conv
	}
	t.Run("restart on the fallback", func(t *testing.T) {
		p := convFake(t, func(n int) (int, string) {
			if n == 1 {
				return 200, callsJSON("", readFileCall, "tool_calls")
			}
			return 502, `{}`
		})
		fb := convFake(t, func(n int) (int, string) {
			if n == 1 {
				return 200, callsJSON("", readFileCall, "tool_calls")
			}
			return 200, okJSON("final")
		})
		ch := NewChain(chainConvClient(t, p, "primary"), []*Client{chainConvClient(t, fb, "fallback")}, 1)
		conv := startConv()
		turn, err := ch.Converse(t.Context(), conv)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ch.Converse(t.Context(), later(conv, turn))
		if fb.calls() != 0 {
			t.Fatalf("a later turn was served by another model: the fallback got the middle of a conversation (error %v)", err)
		}
		if !errors.Is(err, ErrRestart) || !errors.Is(err, ErrUpstream) {
			t.Fatalf("a later turn that failed on its model did not ask for a restart: %v", err)
		}
		turn, err = ch.Converse(t.Context(), conv)
		if err != nil {
			t.Fatal(err)
		}
		if turn, err = ch.Converse(t.Context(), later(conv, turn)); err != nil || turn.Text != "final" {
			t.Fatalf("%+v %v", turn, err)
		}
		if p.calls() != 2 || fb.calls() != 2 {
			t.Fatalf("calls %d/%d", p.calls(), fb.calls())
		}
		var first chatRequest
		if err := json.Unmarshal(fb.body(0), &first); err != nil || len(first.Messages) != 2 {
			t.Fatalf("the restart did not start from the first turn: %s", fb.body(0))
		}
		u := ch.Use()
		if u.Model != "fallback" || u.FallbackModel != "fallback" || u.FallbackReason != ReasonServerError {
			t.Fatalf("use %+v", u)
		}
	})
	t.Run("no fallback", func(t *testing.T) {
		p := convFake(t, func(n int) (int, string) {
			if n == 1 {
				return 200, callsJSON("", readFileCall, "tool_calls")
			}
			return 502, `{}`
		})
		ch := NewChain(chainConvClient(t, p, "primary"), nil, 1)
		conv := startConv()
		turn, err := ch.Converse(t.Context(), conv)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ch.Converse(t.Context(), later(conv, turn))
		if errors.Is(err, ErrRestart) || !errors.Is(err, ErrUpstream) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a failure that does not move on", func(t *testing.T) {
		p := convFake(t, func(n int) (int, string) {
			if n == 1 {
				return 200, callsJSON("", readFileCall, "tool_calls")
			}
			return 401, `{}`
		})
		fb := convFake(t, func(int) (int, string) { return 200, okJSON("x") })
		ch := NewChain(chainConvClient(t, p, "primary"), []*Client{chainConvClient(t, fb, "fallback")}, 1)
		conv := startConv()
		turn, err := ch.Converse(t.Context(), conv)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ch.Converse(t.Context(), later(conv, turn))
		if errors.Is(err, ErrRestart) || !errors.Is(err, ErrAuth) || fb.calls() != 0 {
			t.Fatalf("%v, fallback calls %d", err, fb.calls())
		}
		if u := ch.Use(); u.Model != "primary" {
			t.Fatalf("use %+v", u)
		}
	})
}

// TestConverseToolsRefused: an endpoint that refuses tools (HTTP 400 or 422
// whose body mentions tools or functions), or answers for tool calls
// without one it can read, gives ErrNoTools, never an error of the run. The
// process remembers it for the endpoint and model: a later conversation
// there sends no request, from any client of the process; Complete still
// works; another model on the endpoint still gets tools (§1 item 4).
func TestConverseToolsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"400 tools", 400, `{"error":{"message":"tools are not supported by this model"}}`},
		{"400 tool_choice", 400, `{"error":{"message":"Unknown parameter: 'tool_choice'."}}`},
		{"422 function calling", 422, `{"detail":"Function calling is not enabled"}`},
		{"400 upper case", 400, `{"error":"TOOL USE UNSUPPORTED"}`},
		{"tool_calls without calls", 200, callsJSON(`"I would read a.go"`, "", "tool_calls")},
		{"tool_calls with garbage", 200, callsJSON("", `[{"function":{"arguments":"{}"}}]`, "tool_calls")},
		{"tool_calls not a list", 200, callsJSON("", `"read_file"`, "tool_calls")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := convFake(t, func(n int) (int, string) {
				if n == 1 {
					return tc.status, tc.body
				}
				return 200, okJSON("single pass")
			})
			reg := &registry{}
			newC := func(model string) *Client {
				cfg := baseCfg(f.srv.URL)
				cfg.Model = model
				c, err := New(cfg, config.NewSecret(testKey), nil, WithSleeper((&fakeSleep{}).sleep), withRegistry(reg))
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			c := newC("test-model")
			if c.ToolSupport() != ToolsUnknown {
				t.Fatalf("before: %v", c.ToolSupport())
			}
			_, err := c.Converse(t.Context(), startConv())
			if !errors.Is(err, ErrNoTools) {
				t.Fatalf("refusal: %v", err)
			}
			if _, ok := ClassOf(err); ok {
				t.Fatalf("the refusal is an LLM error of the run: %v", err)
			}
			if c.ToolSupport() != ToolsRefused || f.calls() != 1 {
				t.Fatalf("support %v after %d requests", c.ToolSupport(), f.calls())
			}
			// Remembered for the process: another client, no request.
			again := newC("test-model")
			if _, err := again.Converse(t.Context(), startConv()); !errors.Is(err, ErrNoTools) || f.calls() != 1 {
				t.Fatalf("second conversation: %v after %d requests", err, f.calls())
			}
			// Single-pass still works and carries no tools.
			if resp, err := again.Complete(t.Context(), "s", "u"); err != nil || resp.Content != "single pass" {
				t.Fatalf("complete: %+v %v", resp, err)
			}
			if strings.Contains(string(f.body(1)), "tool") {
				t.Fatalf("single pass carried tools: %s", f.body(1))
			}
			// Another model of the endpoint is not known yet.
			other := newC("other-model")
			if other.ToolSupport() != ToolsUnknown {
				t.Fatalf("other model: %v", other.ToolSupport())
			}
			if _, err := other.Converse(t.Context(), startConv()); err != nil || f.calls() != 3 || other.ToolSupport() != ToolsAccepted {
				t.Fatalf("other model: %v, %d requests, %v", err, f.calls(), other.ToolSupport())
			}
		})
	}
}

// TestConverseNotRefused: answers that do not show a refusal. A model that
// answers without calling tools accepted them; a 400 about the context
// length stays that error even when it names tools; other refusals stay
// the run's errors; a refusal wording without tools in the request is not
// a refusal of tools.
func TestConverseNotRefused(t *testing.T) {
	t.Run("answer without calls", func(t *testing.T) {
		f := convFake(t, func(int) (int, string) { return 200, okJSON("no need to read") })
		c := newClient(t, baseCfg(f.srv.URL), nil, nil)
		turn, err := c.Converse(t.Context(), startConv())
		if err != nil || turn.Text != "no need to read" || c.ToolSupport() != ToolsAccepted {
			t.Fatalf("%+v %v %v", turn, err, c.ToolSupport())
		}
	})
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		tools    bool
		want     error
		requests int
	}{
		{"context length naming tools", 400, `{"error":{"message":"maximum context length exceeded, including tool definitions"}}`, true, ErrContextTooLong, 1},
		{"other 400", 400, `{"error":{"message":"invalid temperature"}}`, true, ErrBadRequest, 1},
		{"413 naming tools", 413, `{"error":{"message":"payload with tools too large"}}`, true, ErrBadRequest, 1},
		{"404 naming tools", 404, `{"error":{"message":"no tools here"}}`, true, ErrNotFound, 1},
		{"500 naming tools", 500, `{"error":{"message":"tool runner crashed"}}`, true, ErrUpstream, 2},
		{"400 tools without tools", 400, `{"error":{"message":"tools are not supported"}}`, false, ErrBadRequest, 1},
		{"tool_calls without tools", 200, callsJSON("", "", "tool_calls"), false, ErrProtocol, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := convFake(t, func(int) (int, string) { return tc.status, tc.body })
			c := newClient(t, baseCfg(f.srv.URL), nil, &fakeSleep{})
			conv := startConv()
			if !tc.tools {
				conv.Tools = nil
			}
			_, err := c.Converse(t.Context(), conv)
			if errors.Is(err, ErrNoTools) || !errors.Is(err, tc.want) || f.calls() != tc.requests {
				t.Fatalf("%v after %d requests, want %v", err, f.calls(), tc.want)
			}
			if c.ToolSupport() != ToolsUnknown {
				t.Fatalf("support %v", c.ToolSupport())
			}
		})
	}
}

// TestChainToolsRefused: a fallback is treated like the primary. When the
// primary fails and the fallback refuses tools, the run is on the fallback
// and continues single-pass there; the chain reports the refusal of the
// model the run is on.
func TestChainToolsRefused(t *testing.T) {
	p := convFake(t, func(int) (int, string) { return 500, `{}` })
	fb := convFake(t, func(n int) (int, string) {
		if n == 1 {
			return 400, `{"error":{"message":"tools are not supported"}}`
		}
		return 200, okJSON("single pass")
	})
	ch := NewChain(chainConvClient(t, p, "primary"), []*Client{chainConvClient(t, fb, "fallback")}, 1)
	if ch.ToolsRefused() {
		t.Fatal("refused before any request")
	}
	if _, err := ch.Converse(t.Context(), startConv()); !errors.Is(err, ErrNoTools) {
		t.Fatalf("%v", err)
	}
	if !ch.ToolsRefused() {
		t.Fatal("the fallback's refusal is not reported")
	}
	resp, err := ch.Complete(t.Context(), "s", "u")
	if err != nil || resp.Content != "single pass" || p.calls() != 1 || fb.calls() != 2 {
		t.Fatalf("%+v %v, calls %d/%d", resp, err, p.calls(), fb.calls())
	}
	if u := ch.Use(); u.Model != "fallback" || u.FallbackReason != ReasonServerError {
		t.Fatalf("use %+v", u)
	}

	// A primary that refuses keeps the run on it.
	p2 := convFake(t, func(n int) (int, string) {
		if n == 1 {
			return 422, `{"detail":"function calling is not supported"}`
		}
		return 200, okJSON("single pass")
	})
	fb2 := convFake(t, func(int) (int, string) { return 200, okJSON("x") })
	ch = NewChain(chainConvClient(t, p2, "primary"), []*Client{chainConvClient(t, fb2, "fallback")}, 1)
	if _, err := ch.Converse(t.Context(), startConv()); !errors.Is(err, ErrNoTools) || fb2.calls() != 0 || !ch.ToolsRefused() {
		t.Fatalf("%v, fallback calls %d", err, fb2.calls())
	}
	if _, err := ch.Complete(t.Context(), "s", "u"); err != nil || p2.calls() != 2 || fb2.calls() != 0 {
		t.Fatalf("%v, calls %d/%d", err, p2.calls(), fb2.calls())
	}
}

// TestToolSupportString: the states as server_info names them.
func TestToolSupportString(t *testing.T) {
	for s, want := range map[ToolSupport]string{ToolsUnknown: "not known yet", ToolsAccepted: "accepted", ToolsRefused: "refused"} {
		if s.String() != want {
			t.Errorf("%d: %q", s, s.String())
		}
	}
}
