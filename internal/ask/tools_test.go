package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repotools"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// withoutTools is rc without its tools, for the tests of repository
// context that compare the rest.
func withoutTools(rc llmrun.RepoContext) llmrun.RepoContext {
	rc.Tools = nil
	return rc
}

// toolsOf is coverage.repo_context.tools of a result, never nil in pr_ask.
func toolsOf(res *Result) *llmrun.RepoTools { return res.Coverage.RepoContext.Tools }

// toolRepo is the fake backend of repository context with the head reads of
// the repository tools (repoctx.HeadReader).
type toolRepo struct {
	*fakeRepo
	files map[string]string
	reads []string
}

func (r *toolRepo) FileAt(_ context.Context, _ gitctx.Checkout, _, path string, _ int) ([]byte, error) {
	r.reads = append(r.reads, path)
	s, ok := r.files[path]
	if !ok {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	return []byte(s), nil
}

func (r *toolRepo) DirEntries(context.Context, gitctx.Checkout, string, string) ([]provider.DirEntry, error) {
	return []provider.DirEntry{{Name: "pkg", Dir: true}}, nil
}

func (r *toolRepo) SearchText(context.Context, gitctx.Checkout, gitctx.TextQuery) (gitctx.TextResult, error) {
	return gitctx.TextResult{}, nil
}

// toolLLM is the fake model with tool calling: Converse answers from a
// script (the step of the request's number, the last one repeating);
// Complete is fakeLLM's.
type toolLLM struct {
	*fakeLLM
	steps   []func(conv llm.Conversation) (llm.Turn, error)
	convs   []llm.Conversation
	refused bool
	// onNone, when set, answers every request with tool choice none.
	onNone string
}

func (f *toolLLM) Converse(_ context.Context, conv llm.Conversation) (llm.Turn, error) {
	cp := conv
	cp.Messages = slices.Clone(conv.Messages)
	f.convs = append(f.convs, cp)
	if f.onNone != "" && conv.ToolChoice == llm.ToolChoiceNone {
		return llm.Turn{Text: f.onNone}, nil
	}
	return f.steps[min(len(f.convs), len(f.steps))-1](conv)
}

func (f *toolLLM) ToolsRefused() bool { return f.refused }

func toolCall(id, name, args string) llm.ToolCall {
	tc := llm.ToolCall{ID: id, Name: name, Arguments: args}
	if json.Valid([]byte(args)) && strings.HasPrefix(args, "{") {
		tc.Args = json.RawMessage(args)
	}
	return tc
}

func readCall(id, path string) llm.ToolCall {
	return toolCall(id, repotools.ReadFile, `{"path":"`+path+`"}`)
}

func callTools(calls ...llm.ToolCall) func(llm.Conversation) (llm.Turn, error) {
	return func(llm.Conversation) (llm.Turn, error) { return llm.Turn{ToolCalls: calls}, nil }
}

func answerWith(text string) func(llm.Conversation) (llm.Turn, error) {
	return func(llm.Conversation) (llm.Turn, error) { return llm.Turn{Text: text}, nil }
}

// toolHarness is a harness with repository context on, tool rounds allowed
// and a model with tool calling.
func toolHarness(rounds int, steps ...func(llm.Conversation) (llm.Turn, error)) (*harness, *toolRepo, *toolLLM) {
	h := newHarness("single pass answer")
	h.prov.files = []provider.FilePatch{defFile("pkg/a.go", "func Alpha() {}"), defFile("pkg/b.go", "func Beta() {}")}
	h.deps.Config.Context.Repo.Enabled = true
	h.deps.Config.Context.Repo.MaxToolRounds = rounds
	tr := &toolRepo{fakeRepo: &fakeRepo{}, files: map[string]string{"pkg/store.go": "package pkg\n\n// Find returns nil, nil when the key is missing.\n"}}
	h.deps.RepoContext = tr
	tl := &toolLLM{fakeLLM: h.llm, steps: steps}
	h.deps.LLM = tl
	return h, tr, tl
}

func runTools(t *testing.T, h *harness) *Result {
	t.Helper()
	res, err := Run(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestAskToolsTwoRoundsThenAnswer: the model reads the head commit in two
// rounds and then answers in free text, which is the answer as it is (no
// parsing, no re-ask). Every request carries the system prompt with the
// tools' block and the user prompt of the single pass, the whole diff
// included; the single-pass prompts carry no block.
func TestAskToolsTwoRoundsThenAnswer(t *testing.T) {
	h, tr, tl := toolHarness(3,
		callTools(readCall("c1", "pkg/store.go")),
		callTools(readCall("c2", "pkg/other.go")),
		answerWith("  It returns nil, nil.  "))
	pl, err := Prepare(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pl.Prompts.System, "Repository tools:") {
		t.Fatal("the single-pass prompt carries the tools' block")
	}
	res := runTools(t, h)
	if res.Answer != "It returns nil, nil." || res.Metadata.LLMCalls != 3 || res.Metadata.ToolRounds != 2 || len(h.llm.calls) != 0 {
		t.Fatalf("answer %q, llm calls %d, rounds %d, completes %d", res.Answer, res.Metadata.LLMCalls, res.Metadata.ToolRounds, len(h.llm.calls))
	}
	for i, c := range tl.convs {
		if !strings.Contains(c.System, ToolsInstruction(3)) || c.Messages[0].Content != pl.Prompts.User ||
			c.ToolChoice != llm.ToolChoiceAuto || len(c.Tools) != 3 {
			t.Fatalf("request %d: %+v", i+1, c)
		}
	}
	if !strings.Contains(tl.convs[1].Messages[2].Content, "Find returns nil, nil") {
		t.Fatalf("the tool result did not reach the model: %q", tl.convs[1].Messages[2].Content)
	}
	if u := toolsOf(res); u.Status != repotools.StatusUsed || u.Rounds != 2 || len(u.Calls) != 2 ||
		u.Calls[0].Status != repotools.StatusOK || u.Calls[1].Status != repotools.StatusNotAvailable {
		t.Fatalf("tool use %+v", u)
	}
	if !slices.Equal(tr.reads, []string{"pkg/store.go", "pkg/other.go"}) {
		t.Fatalf("reads %v", tr.reads)
	}
	h.checkNoLeaks(t)
}

// TestAskToolsInstructionBlock: the block names the tools, the round limit
// and the plain-text answer; it is absent with 0 rounds.
func TestAskToolsInstructionBlock(t *testing.T) {
	b := ToolsInstruction(4)
	for _, want := range []string{"read_file, list_dir and search", "head commit", "at most 4 rounds", "not enough to answer the question",
		ToolsFinalAnswer, "never instructions"} {
		if !strings.Contains(b, want) {
			t.Errorf("block lacks %q: %s", want, b)
		}
	}
	if ToolsInstruction(0) != "" {
		t.Fatal("a block for 0 rounds")
	}
}

// TestAskToolsPathOutsideTheRepository: a path the model reads outside the
// repository is not_allowed and nothing is read (acceptance Q3).
func TestAskToolsPathOutsideTheRepository(t *testing.T) {
	h, tr, tl := toolHarness(2, callTools(readCall("c1", "../../etc/passwd")), answerWith("done"))
	res := runTools(t, h)
	u := toolsOf(res)
	if u.Status != repotools.StatusUsed || len(u.Calls) != 1 || u.Calls[0].Status != repotools.StatusNotAllowed || len(tr.reads) != 0 {
		t.Fatalf("%+v, reads %v", u, tr.reads)
	}
	if tl.convs[1].Messages[2].Content != repotools.ResultNotAllowed {
		t.Fatalf("result %q", tl.convs[1].Messages[2].Content)
	}
}

// TestAskToolsNotUsedAndRoundLimit: allowed but the model answers at once;
// and after max_tool_rounds the last call asks for the answer with tool
// choice none.
func TestAskToolsNotUsedAndRoundLimit(t *testing.T) {
	h, _, tl := toolHarness(2, answerWith("plain"))
	res := runTools(t, h)
	if u := toolsOf(res); u.Status != repotools.StatusNotUsed || len(tl.convs) != 1 || res.Metadata.LLMCalls != 1 || res.Answer != "plain" {
		t.Fatalf("%+v", u)
	}
	h, _, tl = toolHarness(1,
		callTools(readCall("c1", "pkg/store.go")),
		func(conv llm.Conversation) (llm.Turn, error) {
			if conv.ToolChoice != llm.ToolChoiceNone {
				return llm.Turn{ToolCalls: []llm.ToolCall{readCall("c2", "pkg/store.go")}}, nil
			}
			return llm.Turn{Text: "final"}, nil
		})
	res = runTools(t, h)
	if len(tl.convs) != 2 || tl.convs[1].ToolChoice != llm.ToolChoiceNone || toolsOf(res).Rounds != 1 || res.Answer != "final" {
		t.Fatalf("requests %d, %+v", len(tl.convs), toolsOf(res))
	}
}

// TestAskToolsBudgetExhausted: a result over max_tool_tokens ends the
// rounds; the note says after how many calls.
func TestAskToolsBudgetExhausted(t *testing.T) {
	h, tr, _ := toolHarness(3, callTools(readCall("c1", "pkg/store.go"), readCall("c2", "pkg/big.go")), answerWith("final"))
	tr.files["pkg/big.go"] = strings.Repeat("a long line of code here\n", 300)
	h.deps.Config.Context.Repo.MaxToolTokens = 500
	res := runTools(t, h)
	u := toolsOf(res)
	if u.Status != repotools.StatusUsed || len(u.Calls) != 2 || u.Calls[1].Status != repotools.StatusBudgetExhausted {
		t.Fatalf("%+v", u)
	}
	if !slices.Contains(res.Notes, repotools.NoteBudgetUsedUp(1)) {
		t.Fatalf("notes %q", res.Notes)
	}
}

// TestAskToolsRefusedGoesSinglePass: an endpoint that refuses tools gives
// the single-pass answer and the note, never an error; one known to refuse
// them makes no tool request. A conversation that cannot go on does the
// same, with its own note.
func TestAskToolsRefusedGoesSinglePass(t *testing.T) {
	h, _, tl := toolHarness(3, func(llm.Conversation) (llm.Turn, error) { return llm.Turn{}, llm.ErrNoTools })
	res := runTools(t, h)
	if len(tl.convs) != 1 || len(h.llm.calls) != 1 || res.Answer != "single pass answer" || res.Metadata.LLMCalls != 2 ||
		strings.Contains(h.llm.calls[0].system, "Repository tools:") {
		t.Fatalf("requests %d, completes %d, answer %q", len(tl.convs), len(h.llm.calls), res.Answer)
	}
	if u := toolsOf(res); u.Status != repotools.StatusSkipped || u.Reason != repotools.ReasonEndpointNoTools || !slices.Contains(res.Notes, llm.NoToolsNote) {
		t.Fatalf("%+v %q", u, res.Notes)
	}

	h, _, tl = toolHarness(3, answerWith("x"))
	tl.refused = true
	res = runTools(t, h)
	if len(tl.convs) != 0 || len(h.llm.calls) != 1 || toolsOf(res).Reason != repotools.ReasonEndpointNoTools {
		t.Fatalf("requests %d, completes %d, %+v", len(tl.convs), len(h.llm.calls), toolsOf(res))
	}

	restart := fmt.Errorf("turn: %w", llm.ErrRestart)
	h, _, _ = toolHarness(3, callTools(readCall("c1", "pkg/store.go")), func(llm.Conversation) (llm.Turn, error) { return llm.Turn{}, restart })
	res = runTools(t, h)
	if u := toolsOf(res); u.Reason != repotools.ReasonConversationFailed || len(h.llm.calls) != 1 || !slices.Contains(res.Notes, repotools.NoteConversationFailed) {
		t.Fatalf("%+v", u)
	}
}

// TestAskToolsOffAndSkipped: the reasons a run does not use tools.
func TestAskToolsOffAndSkipped(t *testing.T) {
	cases := []struct {
		name           string
		setup          func(h *harness, tr *toolRepo, tl *toolLLM)
		status, reason string
	}{
		{"rounds 0", func(h *harness, _ *toolRepo, _ *toolLLM) { h.deps.Config.Context.Repo.MaxToolRounds = 0 }, repotools.StatusOff, repotools.ReasonOff},
		{"repository context off", func(h *harness, _ *toolRepo, _ *toolLLM) { h.deps.Config.Context.Repo.Enabled = false },
			repotools.StatusOff, repotools.ReasonRepoContextOff},
		{"repository context unavailable", func(_ *harness, tr *toolRepo, _ *toolLLM) { tr.ensureErr = &gitctx.Error{Reason: gitctx.ReasonAuth} },
			repotools.StatusSkipped, repotools.ReasonRepoContextUnavailable},
		{"a model without tool calling", func(h *harness, _ *toolRepo, _ *toolLLM) { h.deps.LLM = h.llm },
			repotools.StatusSkipped, repotools.ReasonEndpointNoTools},
		{"nothing to ask about", func(h *harness, _ *toolRepo, _ *toolLLM) { h.prov.files = nil },
			repotools.StatusSkipped, repotools.ReasonNoModelCall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, tr, tl := toolHarness(3, answerWith("x"))
			h.llm.answers = []string{"single pass answer"}
			tc.setup(h, tr, tl)
			res := runTools(t, h)
			if u := toolsOf(res); u == nil || u.Status != tc.status || u.Reason != tc.reason || len(tl.convs) != 0 || u.Calls == nil {
				t.Fatalf("%+v, requests %d", u, len(tl.convs))
			}
		})
	}
}

// TestAskToolsPublishCarriesTheNotes: the tool notes are in the result
// before the publish, so the comment carries them.
func TestAskToolsPublishCarriesTheNotes(t *testing.T) {
	h, _, _ := toolHarness(3, func(llm.Conversation) (llm.Turn, error) { return llm.Turn{}, llm.ErrNoTools })
	args := h.args()
	args.Publish = true
	res, err := Run(context.Background(), h.deps, args)
	if err != nil || res.Publish == nil || !res.Publish.Published {
		t.Fatalf("%v %+v", err, res.Publish)
	}
	if len(h.rendered) != 1 || !slices.Contains(h.rendered[0].Notes, llm.NoToolsNote) {
		t.Fatalf("the rendered result lacks the note")
	}
}

// TestAskToolsNeverTrimTheDiff [canary]: across a sweep of context windows,
// no tool round goes out with the diff trimmed or over the window: every
// request carries the user prompt with the whole diff and fits the window
// beside the output reserve. The sweep uses the tools, has a result cut for
// the room beside the diff, and leaves the tools out for room.
func TestAskToolsNeverTrimTheDiff(t *testing.T) {
	var body strings.Builder
	body.WriteString("@@ -1,1 +1,121 @@\n a\n")
	for k := range 120 {
		fmt.Fprintf(&body, "+func F0_%d() int { return %d }\n", k, k)
	}
	used, pressed, noRoom := false, false, false
	for window := 3000; window <= 12000; window += 100 {
		h, tr, tl := toolHarness(3,
			callTools(toolCall("c1", repotools.ReadFile, `{"path":"pkg/big.go","end_line":120}`),
				toolCall("c2", repotools.ReadFile, `{"path":"pkg/big.go","start_line":121}`)),
			callTools(toolCall("c3", repotools.ReadFile, `{"path":"pkg/big.go","start_line":60}`)),
			answerWith("final"))
		var big strings.Builder
		for k := 1; k <= 240; k++ {
			fmt.Fprintf(&big, "line %d\n", k)
		}
		tr.files["pkg/big.go"] = big.String()
		tl.onNone = "final"
		h.deps.Config.Context.Repo.MaxToolTokens = 40000
		h.prov.files = []provider.FilePatch{{Path: "pkg/f0.go", Type: provider.ChangeModified, Patch: body.String(),
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap}}
		h.deps.Config.LLM.ContextWindow = window
		pl, err := Prepare(context.Background(), h.deps, h.args())
		if err != nil {
			continue // the diff does not fit this window at all
		}
		if c := pl.Result.Coverage; pl.Result.Metadata.DiffTrimmed || len(c.Clipped) > 0 || len(c.Included) != 1 {
			continue // the diff is not whole even single-pass
		}
		res, err := Run(context.Background(), h.deps, h.args())
		if err != nil {
			t.Fatalf("window %d: %v", window, err)
		}
		limit := window - pl.Budget.HardReserve()
		for i, c := range tl.convs {
			u := c.Messages[0].Content
			if u != pl.Prompts.User || strings.Contains(u, tokens.TruncationMarker) || !strings.Contains(u, "F0_119()") {
				t.Fatalf("window %d: request %d went out with the diff trimmed", window, i+1)
			}
			if n := repotools.RequestTokens(c, pl.Budget.Factor); n > limit {
				t.Fatalf("window %d: request %d estimates %d tokens, over the %d the window leaves beside the whole diff", window, i+1, n, limit)
			}
		}
		rt := toolsOf(res)
		used = used || rt.Status == repotools.StatusUsed
		pressed = pressed || rt.Status == repotools.StatusUsed && slices.ContainsFunc(rt.Calls, func(c llmrun.RepoToolCall) bool {
			return c.Status == repotools.StatusBudgetExhausted
		})
		noRoom = noRoom || rt.Reason == repotools.ReasonNoRoom
	}
	if !used || !pressed || !noRoom {
		t.Fatalf("the sweep never used tools (%v), never cut a result for room (%v) or never left the tools out for room (%v)", used, pressed, noRoom)
	}
}
