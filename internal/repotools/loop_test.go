package repotools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// fakeModel answers each Converse with the next scripted step and records
// the conversations it got (copied).
type fakeModel struct {
	steps []func(conv llm.Conversation) (llm.Turn, error)
	got   []llm.Conversation
	// onNone, when set, answers every request with tool choice none.
	onNone string
}

func (f *fakeModel) Converse(_ context.Context, conv llm.Conversation) (llm.Turn, error) {
	cp := conv
	cp.Messages = append([]llm.Message(nil), conv.Messages...)
	f.got = append(f.got, cp)
	if f.onNone != "" && conv.ToolChoice == llm.ToolChoiceNone {
		return llm.Turn{Text: f.onNone}, nil
	}
	i := min(len(f.got)-1, len(f.steps)-1)
	return f.steps[i](conv)
}

func calling(calls ...llm.ToolCall) func(llm.Conversation) (llm.Turn, error) {
	return func(llm.Conversation) (llm.Turn, error) { return llm.Turn{ToolCalls: calls}, nil }
}

func answering(text string) func(llm.Conversation) (llm.Turn, error) {
	return func(llm.Conversation) (llm.Turn, error) { return llm.Turn{Text: text}, nil }
}

func failing(err error) func(llm.Conversation) (llm.Turn, error) {
	return func(llm.Conversation) (llm.Turn, error) { return llm.Turn{}, err }
}

func loopBox(budget int) (*Box, *fakeHead) {
	f := &fakeHead{files: map[string]string{"a.go": "package a\n", "b.go": "package b\n", "big.go": numberedFile(300)}}
	return newBox(f, budget), f
}

func loopIn(rounds int) LoopInput {
	return LoopInput{System: "system with the tools block", User: "user prompt with DIFF-WHOLE", MaxRounds: rounds, Limit: 1 << 20}
}

// TestLoopTwoRoundsThenAnswer: two rounds of calls, their results appended
// in order, then an answer without calls ends the conversation.
func TestLoopTwoRoundsThenAnswer(t *testing.T) {
	b, _ := loopBox(6000)
	m := &fakeModel{steps: []func(llm.Conversation) (llm.Turn, error){
		calling(call("c1", ReadFile, `{"path":"a.go"}`)),
		calling(call("c2", ReadFile, `{"path":"b.go"}`), call("c3", ListDir, `{"path":""}`)),
		answering("review: yaml"),
	}}
	res, err := b.Loop(context.Background(), m, loopIn(3))
	if err != nil || res.Answer.Text != "review: yaml" || res.Calls != 3 || res.Rounds != 2 || res.Fallback != "" {
		t.Fatalf("%+v %v", res, err)
	}
	for i, c := range m.got {
		if c.ToolChoice != llm.ToolChoiceAuto || len(c.Tools) != 3 || c.System != "system with the tools block" ||
			c.Messages[0].Content != "user prompt with DIFF-WHOLE" {
			t.Fatalf("request %d: %+v", i+1, c)
		}
	}
	last := m.got[2].Messages
	if len(last) != 6 || last[1].Role != llm.RoleAssistant || last[2].ToolCallID != "c1" || last[3].Role != llm.RoleAssistant ||
		last[4].ToolCallID != "c2" || last[5].ToolCallID != "c3" || !strings.Contains(last[2].Content, "1  package a") {
		t.Fatalf("conversation %+v", last)
	}
	if n := len(b.Calls()); n != 3 {
		t.Fatalf("%d calls accounted", n)
	}
}

// TestLoopNoCalls: an answer in the first round is the answer; no tool ran.
func TestLoopNoCalls(t *testing.T) {
	b, f := loopBox(6000)
	m := &fakeModel{steps: []func(llm.Conversation) (llm.Turn, error){answering("plain")}}
	res, err := b.Loop(context.Background(), m, loopIn(3))
	if err != nil || res.Answer.Text != "plain" || res.Calls != 1 || res.Rounds != 0 || f.count() != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// TestLoopRoundLimit: after max_tool_rounds rounds, one last call with tool
// choice none asks for the answer, with the tools still declared.
func TestLoopRoundLimit(t *testing.T) {
	b, _ := loopBox(40000)
	m := &fakeModel{steps: []func(llm.Conversation) (llm.Turn, error){
		calling(call("c1", ReadFile, `{"path":"a.go"}`)),
		calling(call("c2", ReadFile, `{"path":"b.go"}`)),
		func(conv llm.Conversation) (llm.Turn, error) {
			if conv.ToolChoice != llm.ToolChoiceNone {
				return llm.Turn{ToolCalls: []llm.ToolCall{call("c3", ReadFile, `{"path":"a.go"}`)}}, nil
			}
			return llm.Turn{Text: "final"}, nil
		},
	}}
	res, err := b.Loop(context.Background(), m, loopIn(2))
	if err != nil || res.Answer.Text != "final" || res.Calls != 3 || res.Rounds != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if c := m.got[2]; c.ToolChoice != llm.ToolChoiceNone || len(c.Tools) != 3 || len(c.Messages) != 5 {
		t.Fatalf("last request %+v", c)
	}
}

// TestLoopBudgetExhausted: a result over the budget ends the rounds; the
// answer is asked for at once with tool choice none.
func TestLoopBudgetExhausted(t *testing.T) {
	b, _ := loopBox(500)
	m := &fakeModel{steps: []func(llm.Conversation) (llm.Turn, error){
		calling(call("c1", ReadFile, `{"path":"a.go"}`), call("c2", ReadFile, `{"path":"big.go"}`)),
		answering("final"),
	}}
	res, err := b.Loop(context.Background(), m, loopIn(5))
	if err != nil || res.Answer.Text != "final" || res.Calls != 2 || res.Rounds != 1 || !b.Exhausted() {
		t.Fatalf("%+v %v", res, err)
	}
	if c := m.got[1]; c.ToolChoice != llm.ToolChoiceNone || c.Messages[3].Content != ResultBudgetExhausted {
		t.Fatalf("last request %+v", c)
	}
	st := []string{}
	for _, c := range b.Calls() {
		st = append(st, c.Status)
	}
	if fmt.Sprint(st) != "[ok budget_exhausted]" {
		t.Fatalf("%v", st)
	}
}

// TestLoopRequestsFitTheLimit: across a sweep of limits, every request
// sent fits the limit, the user prompt is never changed, and a turn whose
// calls cannot be answered within the limit is dropped.
func TestLoopRequestsFitTheLimit(t *testing.T) {
	user := "user prompt " + strings.Repeat("diff line\n", 50)
	start := RequestTokens(Start("sys", user), 0)
	dropped, used := false, false
	for limit := start; limit <= start+3000; limit += 40 {
		b, _ := loopBox(40000)
		m := &fakeModel{steps: []func(llm.Conversation) (llm.Turn, error){
			calling(call("c1", ReadFile, `{"path":"big.go"}`), call("c2", ReadFile, `{"path":"a.go"}`)),
			calling(call("c3", ReadFile, `{"path":"big.go","start_line":100}`)),
			answering("final"),
		}, onNone: "final"}
		res, err := b.Loop(context.Background(), m, LoopInput{System: "sys", User: user, MaxRounds: 3, Limit: limit})
		if err != nil || res.Answer.Text != "final" {
			t.Fatalf("limit %d: %+v %v", limit, res, err)
		}
		for i, c := range m.got {
			if n := RequestTokens(c, 0); n > limit {
				t.Fatalf("limit %d: request %d estimates %d", limit, i+1, n)
			}
			if c.Messages[0].Content != user {
				t.Fatalf("limit %d: request %d changed the user prompt", limit, i+1)
			}
		}
		used = used || res.Rounds > 0
		dropped = dropped || res.Rounds == 0 && len(b.Calls()) > 0
	}
	if !used || !dropped {
		t.Fatalf("the sweep never used tools (%v) or never dropped a turn (%v)", used, dropped)
	}
}

// TestLoopRestart: a later turn that fails on its model restarts the
// conversation once from its first turn; a second restart, or a
// context-length error, ends it for a single-pass answer; a refusal of
// tools ends it with FallbackNoTools; other errors are returned.
func TestLoopRestart(t *testing.T) {
	restart := fmt.Errorf("turn: %w", llm.ErrRestart)
	b, _ := loopBox(6000)
	m := &fakeModel{steps: []func(llm.Conversation) (llm.Turn, error){
		calling(call("c1", ReadFile, `{"path":"a.go"}`)),
		failing(restart),
		calling(call("c2", ReadFile, `{"path":"b.go"}`)),
		answering("final"),
	}}
	res, err := b.Loop(context.Background(), m, loopIn(3))
	if err != nil || res.Answer.Text != "final" || !res.Restarted || res.Calls != 4 || res.Rounds != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if len(m.got[2].Messages) != 1 {
		t.Fatalf("the restart did not start from the first turn: %+v", m.got[2].Messages)
	}

	for _, tc := range []struct {
		name  string
		steps []func(llm.Conversation) (llm.Turn, error)
		fb    string
		err   error
	}{
		{"second restart", []func(llm.Conversation) (llm.Turn, error){calling(call("c1", ReadFile, `{"path":"a.go"}`)), failing(restart)}, FallbackFailed, nil},
		{"context length", []func(llm.Conversation) (llm.Turn, error){failing(llm.ErrContextTooLong)}, FallbackFailed, nil},
		{"no tools", []func(llm.Conversation) (llm.Turn, error){failing(llm.ErrNoTools)}, FallbackNoTools, nil},
		{"no tools later", []func(llm.Conversation) (llm.Turn, error){calling(call("c1", ReadFile, `{"path":"a.go"}`)), failing(llm.ErrNoTools)}, FallbackNoTools, nil},
		{"auth", []func(llm.Conversation) (llm.Turn, error){failing(llm.ErrAuth)}, "", llm.ErrAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := loopBox(6000)
			res, err := b.Loop(context.Background(), &fakeModel{steps: tc.steps}, loopIn(3))
			if res.Fallback != tc.fb || !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
				t.Fatalf("%+v %v", res, err)
			}
		})
	}
}

// TestRequestTokens: a conversation of the two prompts estimates as the
// single-pass request plus the tools; every later message adds its text
// and its framing.
func TestRequestTokens(t *testing.T) {
	conv := llm.Conversation{System: "s", Messages: []llm.Message{{Role: llm.RoleUser, Content: "u"}}}
	if RequestTokens(conv, 0.2) != tokens.RequestTokens("s", "u", 0.2) {
		t.Fatal("two messages")
	}
	conv.Messages = append(conv.Messages, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c", Name: "read_file", Arguments: "{}"}}},
		llm.ToolResult("c", "result text"))
	want := tokens.RequestTokens("s", "u", 0.2) + tokens.MessageTokens("", 0.2) + tokens.MessageTokens("c read_file {}", 0.2) +
		tokens.MessageTokens("result text", 0.2)
	if got := RequestTokens(conv, 0.2); got != want {
		t.Fatalf("%d, want %d", got, want)
	}
	if RequestTokens(Start("s", "u"), 0) <= tokens.RequestTokens("s", "u", 0) {
		t.Fatal("the tools are not counted")
	}
}
