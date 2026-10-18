package review

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

// toolRepo is the fake backend of repository context with the head reads of
// the repository tools (repoctx.HeadReader).
type toolRepo struct {
	*fakeRepo
	files map[string]string
	errs  map[string]error
	reads []string
}

func (r *toolRepo) FileAt(_ context.Context, _ gitctx.Checkout, _, path string, _ int) ([]byte, error) {
	r.reads = append(r.reads, path)
	if err := r.errs[path]; err != nil {
		return nil, err
	}
	s, ok := r.files[path]
	if !ok {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	return []byte(s), nil
}

func (r *toolRepo) DirEntries(context.Context, gitctx.Checkout, string, string) ([]provider.DirEntry, error) {
	return []provider.DirEntry{{Name: "src", Dir: true}}, nil
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
}

func (f *toolLLM) Converse(_ context.Context, conv llm.Conversation) (llm.Turn, error) {
	cp := conv
	cp.Messages = slices.Clone(conv.Messages)
	f.convs = append(f.convs, cp)
	return f.steps[min(len(f.convs), len(f.steps))-1](conv)
}

func (f *toolLLM) ToolsRefused() bool { return f.refused }

func readCall(id, path string) llm.ToolCall {
	args := `{"path":"` + path + `"}`
	return llm.ToolCall{ID: id, Name: repotools.ReadFile, Arguments: args, Args: json.RawMessage(args)}
}

func callTools(calls ...llm.ToolCall) func(llm.Conversation) (llm.Turn, error) {
	return func(llm.Conversation) (llm.Turn, error) { return llm.Turn{ToolCalls: calls}, nil }
}

func toolAnswer(text string) func(llm.Conversation) (llm.Turn, error) {
	return func(llm.Conversation) (llm.Turn, error) { return llm.Turn{Text: text}, nil }
}

// toolHarness is a harness with repository context on, tool rounds allowed
// and a model with tool calling.
func toolHarness(rounds int, steps ...func(llm.Conversation) (llm.Turn, error)) (*harness, *toolRepo, *toolLLM) {
	h, fb := repoHarness(defFiles(), nil)
	tr := &toolRepo{fakeRepo: fb, files: map[string]string{"pkg/store.go": "package pkg\n\n// Find returns nil, nil when the key is missing.\n"}}
	h.deps.RepoContext = tr
	h.deps.Config.Context.Repo.MaxToolRounds = rounds
	tl := &toolLLM{fakeLLM: h.llm, steps: steps}
	h.deps.LLM = tl
	return h, tr, tl
}

func runTools(t *testing.T, h *harness, args Args) *Result {
	t.Helper()
	args.PRURL = testPRURL
	res, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestToolsTwoRoundsThenAnswer: with tool rounds allowed, the model reads
// the head commit in two rounds and then answers; the answer is the review.
// Every request carries the system prompt with the tools' block and the
// user prompt of the single pass, the whole diff included; the single-pass
// prompts carry no block.
func TestToolsTwoRoundsThenAnswer(t *testing.T) {
	h, tr, tl := toolHarness(3,
		callTools(readCall("c1", "pkg/store.go")),
		callTools(readCall("c2", "pkg/other.go")),
		toolAnswer(goodAnswer))
	pl := prepareOK(t, h)
	if strings.Contains(pl.Prompts.System, "Repository tools:") {
		t.Fatal("the single-pass system prompt carries the tools' block")
	}
	res := runTools(t, h, Args{})
	if len(res.Review.KeyIssuesToReview) != 2 || res.Metadata.LLMCalls != 3 || len(h.llm.calls) != 0 {
		t.Fatalf("findings %d, llm calls %d, completes %d", len(res.Review.KeyIssuesToReview), res.Metadata.LLMCalls, len(h.llm.calls))
	}
	if len(tl.convs) != 3 {
		t.Fatalf("%d requests", len(tl.convs))
	}
	for i, c := range tl.convs {
		if c.System != pl.tools.in.System || !strings.Contains(c.System, ToolsInstruction(3)) || c.Messages[0].Content != pl.Prompts.User ||
			c.ToolChoice != llm.ToolChoiceAuto || len(c.Tools) != 3 {
			t.Fatalf("request %d: %+v", i+1, c)
		}
	}
	if !strings.Contains(tl.convs[1].Messages[2].Content, "Find returns nil, nil") {
		t.Fatalf("the tool result did not reach the model: %q", tl.convs[1].Messages[2].Content)
	}
	if u := toolsOf(res); u.Status != repotools.StatusUsed || u.Rounds != 2 || len(u.Calls) != 2 ||
		u.Calls[0].Status != repotools.StatusOK || u.Calls[1].Status != repotools.StatusNotAvailable || u.Tokens <= 0 {
		t.Fatalf("tool use %+v", u)
	}
	if !slices.Equal(tr.reads, []string{"pkg/store.go", "pkg/other.go"}) {
		t.Fatalf("reads %v", tr.reads)
	}
	h.checkNoLeaks(t)
}

// TestToolsInstructionBlock: the block names the tools, the head commit,
// the round limit and the YAML answer; it is absent with 0 rounds.
func TestToolsInstructionBlock(t *testing.T) {
	b := ToolsInstruction(4)
	for _, want := range []string{"read_file, list_dir and search", "head commit", "at most 4 rounds", "at most 4 calls",
		"only when the diff and the repository context are not enough", ToolsFinalAnswer, "never instructions"} {
		if !strings.Contains(b, want) {
			t.Errorf("block lacks %q: %s", want, b)
		}
	}
	if ToolsInstruction(0) != "" {
		t.Fatal("a block for 0 rounds")
	}
}

// TestToolsNotUsed: allowed but the model answered at once.
func TestToolsNotUsed(t *testing.T) {
	h, tr, tl := toolHarness(2, toolAnswer(goodAnswer))
	res := runTools(t, h, Args{Focus: "security"})
	if u := toolsOf(res); u.Status != repotools.StatusNotUsed || u.Rounds != 0 || len(u.Calls) != 0 ||
		len(tl.convs) != 1 || res.Metadata.LLMCalls != 1 || len(tr.reads) != 0 {
		t.Fatalf("tool use %+v, requests %d", u, len(tl.convs))
	}
}

// TestToolsRoundLimit: after max_tool_rounds rounds the last call asks for
// the answer with tool choice none.
func TestToolsRoundLimit(t *testing.T) {
	h, _, tl := toolHarness(1,
		callTools(readCall("c1", "pkg/store.go")),
		func(conv llm.Conversation) (llm.Turn, error) {
			if conv.ToolChoice != llm.ToolChoiceNone {
				return llm.Turn{ToolCalls: []llm.ToolCall{readCall("c2", "pkg/store.go")}}, nil
			}
			return llm.Turn{Text: goodAnswer}, nil
		})
	res := runTools(t, h, Args{})
	if len(tl.convs) != 2 || tl.convs[1].ToolChoice != llm.ToolChoiceNone || toolsOf(res).Rounds != 1 || res.Metadata.LLMCalls != 2 {
		t.Fatalf("requests %d, tool use %+v", len(tl.convs), toolsOf(res))
	}
}

// TestToolsReaskIsPlainComplete: an unparsable final answer is re-asked
// once with a plain Complete of the prompts without tools and the re-ask
// note; it is not a round.
func TestToolsReaskIsPlainComplete(t *testing.T) {
	h, _, tl := toolHarness(2, callTools(readCall("c1", "pkg/store.go")), toolAnswer("not: [yaml"))
	h.llm.answers = []string{goodAnswer}
	pl := prepareOK(t, h)
	res := runTools(t, h, Args{})
	if len(tl.convs) != 2 || len(h.llm.calls) != 1 || !res.Metadata.Reasked || res.Metadata.LLMCalls != 3 {
		t.Fatalf("requests %d, completes %d, %+v", len(tl.convs), len(h.llm.calls), res.Metadata)
	}
	if c := h.llm.calls[0]; c.system != pl.Prompts.System || !strings.Contains(c.user, ReaskNote) {
		t.Fatal("the re-ask is not the single-pass re-ask")
	}
	if toolsOf(res).Status != repotools.StatusUsed || toolsOf(res).Rounds != 1 {
		t.Fatalf("tool use %+v", toolsOf(res))
	}
}

// TestToolsRefusedGoesSinglePass: an endpoint that refuses tools gives the
// single-pass review, never an error; a chain known to refuse them makes
// no tool request at all.
func TestToolsRefusedGoesSinglePass(t *testing.T) {
	h, _, tl := toolHarness(3, func(llm.Conversation) (llm.Turn, error) { return llm.Turn{}, llm.ErrNoTools })
	res := runTools(t, h, Args{})
	if len(tl.convs) != 1 || len(h.llm.calls) != 1 || len(res.Review.KeyIssuesToReview) != 2 || res.Metadata.LLMCalls != 2 {
		t.Fatalf("requests %d, completes %d, llm calls %d", len(tl.convs), len(h.llm.calls), res.Metadata.LLMCalls)
	}
	if strings.Contains(h.llm.calls[0].system, "Repository tools:") {
		t.Fatal("the single pass carries the tools' block")
	}
	if u := toolsOf(res); u.Status != repotools.StatusSkipped || u.Reason != repotools.ReasonEndpointNoTools {
		t.Fatalf("tool use %+v", u)
	}

	h, _, tl = toolHarness(3, toolAnswer(goodAnswer))
	tl.refused = true
	res = runTools(t, h, Args{})
	if len(tl.convs) != 0 || len(h.llm.calls) != 1 || toolsOf(res).Reason != repotools.ReasonEndpointNoTools {
		t.Fatalf("requests %d, completes %d, %+v", len(tl.convs), len(h.llm.calls), toolsOf(res))
	}
}

// TestToolsConversationFailed: a conversation that cannot go on (a second
// restart: the first turn of the restarted conversation fails too) gives
// the single-pass review.
func TestToolsConversationFailed(t *testing.T) {
	restart := fmt.Errorf("turn: %w", llm.ErrRestart)
	h, _, tl := toolHarness(3,
		callTools(readCall("c1", "pkg/store.go")),
		func(llm.Conversation) (llm.Turn, error) { return llm.Turn{}, restart })
	res := runTools(t, h, Args{})
	if len(h.llm.calls) != 1 || toolsOf(res).Status != repotools.StatusSkipped || toolsOf(res).Reason != repotools.ReasonConversationFailed ||
		len(tl.convs) != 3 || res.Metadata.LLMCalls != 4 {
		t.Fatalf("requests %d, completes %d, %+v, llm calls %d", len(tl.convs), len(h.llm.calls), toolsOf(res), res.Metadata.LLMCalls)
	}
}

// TestToolsOffAndSkipped: the reasons a run does not use tools.
func TestToolsOffAndSkipped(t *testing.T) {
	t.Run("rounds 0", func(t *testing.T) {
		h, _, tl := toolHarness(0, toolAnswer(goodAnswer))
		res := runTools(t, h, Args{})
		if toolsOf(res).Status != repotools.StatusOff || toolsOf(res).Reason != repotools.ReasonOff || len(tl.convs) != 0 {
			t.Fatalf("%+v", toolsOf(res))
		}
	})
	t.Run("repository context off", func(t *testing.T) {
		h, _, tl := toolHarness(3, toolAnswer(goodAnswer))
		h.deps.Config.Context.Repo.Enabled = false
		res := runTools(t, h, Args{})
		if toolsOf(res).Status != repotools.StatusOff || toolsOf(res).Reason != repotools.ReasonRepoContextOff || len(tl.convs) != 0 {
			t.Fatalf("%+v", toolsOf(res))
		}
		validateResult(t, res)
	})
	t.Run("repository context unavailable", func(t *testing.T) {
		h, tr, tl := toolHarness(3, toolAnswer(goodAnswer))
		tr.ensureErr = &gitctx.Error{Reason: gitctx.ReasonAuth}
		res := runTools(t, h, Args{})
		if toolsOf(res).Status != repotools.StatusSkipped || toolsOf(res).Reason != repotools.ReasonRepoContextUnavailable || len(tl.convs) != 0 {
			t.Fatalf("%+v", toolsOf(res))
		}
	})
	t.Run("a model without tool calling", func(t *testing.T) {
		h, _, _ := toolHarness(3, toolAnswer(goodAnswer))
		h.deps.LLM = h.llm
		res := runTools(t, h, Args{})
		if toolsOf(res).Reason != repotools.ReasonEndpointNoTools || len(h.llm.calls) != 1 {
			t.Fatalf("%+v", toolsOf(res))
		}
	})
	t.Run("nothing to review", func(t *testing.T) {
		h, _, tl := toolHarness(3, toolAnswer(goodAnswer))
		h.prov.files = nil
		res := runTools(t, h, Args{})
		if toolsOf(res).Status != repotools.StatusSkipped || toolsOf(res).Reason != repotools.ReasonNoModelCall || len(tl.convs) != 0 {
			t.Fatalf("%+v", toolsOf(res))
		}
	})
	t.Run("review in parts", func(t *testing.T) {
		h, _, tl := toolHarness(3, toolAnswer(goodAnswer))
		h.llm.answers = []string{goodAnswer}
		h.prov.files = toolBigFiles(6, 400)
		h.deps.Config.LLM.ContextWindow = 9000
		res := runTools(t, h, Args{MaxChunks: 4})
		if res.Coverage.ModelCalls < 2 {
			t.Fatalf("not reviewed in parts: %d calls", res.Coverage.ModelCalls)
		}
		if toolsOf(res).Status != repotools.StatusSkipped || toolsOf(res).Reason != repotools.ReasonReviewInParts || len(tl.convs) != 0 {
			t.Fatalf("%+v", toolsOf(res))
		}
	})
	t.Run("a repository file cannot turn them on", func(t *testing.T) {
		h, _, tl := toolHarness(0, toolAnswer(goodAnswer))
		withRepoConfig(h, "[context.repo]\nmax_tool_rounds = 3\nmax_tool_tokens = 40000\n")
		res := runTools(t, h, Args{})
		if toolsOf(res).Status != repotools.StatusOff || len(tl.convs) != 0 {
			t.Fatalf("%+v", toolsOf(res))
		}
		if !slices.Contains(res.RepoConfig.Ignored, "context.repo.max_tool_rounds") ||
			!slices.Contains(res.RepoConfig.Ignored, "context.repo.max_tool_tokens") {
			t.Fatalf("repo_config %+v", res.RepoConfig)
		}
	})
}

// bigFiles are n modified files of lines lines each, each defining a
// symbol.
func toolBigFiles(n, lines int) []provider.FilePatch {
	var out []provider.FilePatch
	for i := range n {
		var b strings.Builder
		fmt.Fprintf(&b, "@@ -1,1 +1,%d @@\n a\n", lines+1)
		for k := range lines {
			fmt.Fprintf(&b, "+func F%d_%d() int { return %d }\n", i, k, k)
		}
		out = append(out, provider.FilePatch{Path: fmt.Sprintf("pkg/f%d.go", i), Type: provider.ChangeModified,
			Patch: b.String(), BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap})
	}
	return out
}

// TestToolsNeverTrimTheDiff [canary]: across a sweep of context windows,
// no tool round goes out with the diff trimmed or over the window: every
// request carries the user prompt with the whole diff and fits the window
// beside the output reserve. The model reads a large file, so in the
// smaller windows the results press the room beside the diff. The sweep
// uses the tools, has results cut for the room, and leaves the tools out
// for room.
func TestToolsNeverTrimTheDiff(t *testing.T) {
	used, pressed, noRoom := false, false, false
	for window := 3000; window <= 12000; window += 100 {
		h, tr, tl := toolHarness(3,
			callTools(toolCall("c1", repotools.ReadFile, `{"path":"pkg/big.go","end_line":120}`),
				toolCall("c2", repotools.ReadFile, `{"path":"pkg/big.go","start_line":121}`)),
			callTools(toolCall("c3", repotools.ReadFile, `{"path":"pkg/big.go","start_line":60}`)),
			toolAnswer(goodAnswer))
		tr.files["pkg/big.go"] = numberedLines(1, 240, nil)
		h.deps.Config.Context.Repo.MaxToolTokens = 40000
		h.prov.files = toolBigFiles(1, 120)
		h.deps.Config.LLM.ContextWindow = window
		h.deps.Config.Review.MaxChunks = 1
		pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
		if err != nil {
			continue // the diff does not fit this window at all
		}
		if c := pl.Result.Coverage; pl.Result.Metadata.DiffTrimmed || len(c.Clipped) > 0 || len(c.Included) != 1 {
			continue // the diff is not whole even single-pass
		}
		res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
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

// toolsOf is coverage.repo_context.tools of a result, never nil in
// pr_review.
func toolsOf(res *Result) *llmrun.RepoTools {
	return res.Coverage.RepoContext.Tools
}

// withoutTools is rc without its tools, for the tests of repository
// context that compare the rest.
func withoutTools(rc llmrun.RepoContext) llmrun.RepoContext {
	rc.Tools = nil
	return rc
}

func toolCall(id, name, args string) llm.ToolCall {
	tc := llm.ToolCall{ID: id, Name: name, Arguments: args}
	if json.Valid([]byte(args)) && strings.HasPrefix(args, "{") {
		tc.Args = json.RawMessage(args)
	}
	return tc
}

// TestToolsResultsValidate [WP-10d]: every result case of the repository
// tools validates against the declared schema, with its notes and
// metadata: used (every call status), not_used, off, and skipped for every
// reason.
func TestToolsResultsValidate(t *testing.T) {
	type want struct {
		status, reason string
		rounds         int
		calls          []string
		note           string
	}
	check := func(t *testing.T, res *Result, w want) {
		t.Helper()
		validateResult(t, res)
		rt := toolsOf(res)
		var st []string
		for _, c := range rt.Calls {
			st = append(st, c.Status)
		}
		if rt.Status != w.status || rt.Reason != w.reason || rt.Rounds != w.rounds || !slices.Equal(st, w.calls) ||
			res.Metadata.ToolRounds != w.rounds {
			t.Fatalf("tools %+v, calls %v, tool_rounds %d; want %+v", rt, st, res.Metadata.ToolRounds, w)
		}
		if rt.Calls == nil {
			t.Fatal("calls is null")
		}
		if w.note != "" && !slices.Contains(res.Notes, w.note) {
			t.Fatalf("notes %q lack %q", res.Notes, w.note)
		}
		raw, _ := json.Marshal(res)
		if !strings.Contains(string(raw), `"tools":{"status":"`+w.status+`"`) || !strings.Contains(string(raw), `"tool_rounds":`) {
			t.Fatalf("JSON %s", raw)
		}
	}

	t.Run("used, every call status, budget used up", func(t *testing.T) {
		h, tr, _ := toolHarness(3,
			callTools(
				toolCall("c1", repotools.ReadFile, `{"path":"pkg/store.go"}`),
				toolCall("c2", repotools.ReadFile, `{"path":"pkg/missing.go"}`),
				toolCall("c3", repotools.ReadFile, `{"path":"../../etc/passwd"}`),
				toolCall("c4", repotools.ReadFile, `{"path":`),
				toolCall("c5", repotools.ReadFile, `{"path":"pkg/huge.go"}`),
				toolCall("c6", repotools.ListDir, `{"path":""}`)),
			callTools(toolCall("c7", repotools.ReadFile, `{"path":"pkg/big.go"}`), toolCall("c8", repotools.Search, `{"text":"Find"}`)),
			toolAnswer(goodAnswer))
		tr.files["pkg/big.go"] = numberedLines(1, 300, nil)
		tr.errs = map[string]error{"pkg/huge.go": &provider.Error{Class: provider.ClassTooLarge}}
		h.deps.Config.Context.Repo.MaxToolTokens = 500
		res := runTools(t, h, Args{})
		check(t, res, want{repotools.StatusUsed, "", 2,
			[]string{"ok", "not_available", "not_allowed", "invalid_arguments", "too_many_calls", "too_many_calls", "budget_exhausted", "budget_exhausted"},
			repotools.NoteBudgetUsedUp(4)})
		if c := toolsOf(res).Calls; c[0].Target != "pkg/store.go" || c[0].Tokens <= 0 || c[2].Target != "../../etc/passwd" || c[3].Target != "" {
			t.Fatalf("calls %+v", c)
		}
	})
	t.Run("too large", func(t *testing.T) {
		h, tr, _ := toolHarness(3, callTools(toolCall("c1", repotools.ReadFile, `{"path":"pkg/huge.go"}`)), toolAnswer(goodAnswer))
		tr.errs = map[string]error{"pkg/huge.go": &provider.Error{Class: provider.ClassTooLarge}}
		check(t, runTools(t, h, Args{}), want{repotools.StatusUsed, "", 1, []string{"too_large"}, ""})
	})
	t.Run("not used", func(t *testing.T) {
		h, _, _ := toolHarness(3, toolAnswer(goodAnswer))
		check(t, runTools(t, h, Args{}), want{repotools.StatusNotUsed, "", 0, nil, ""})
	})
	t.Run("off", func(t *testing.T) {
		h, _, _ := toolHarness(0, toolAnswer(goodAnswer))
		check(t, runTools(t, h, Args{}), want{repotools.StatusOff, repotools.ReasonOff, 0, nil, ""})
	})
	t.Run("repository context unavailable", func(t *testing.T) {
		h, tr, _ := toolHarness(3, toolAnswer(goodAnswer))
		tr.ensureErr = &gitctx.Error{Reason: gitctx.ReasonTimeout}
		check(t, runTools(t, h, Args{}), want{repotools.StatusSkipped, repotools.ReasonRepoContextUnavailable, 0, nil, ""})
	})
	t.Run("endpoint without tools", func(t *testing.T) {
		h, _, _ := toolHarness(3, func(llm.Conversation) (llm.Turn, error) { return llm.Turn{}, llm.ErrNoTools })
		check(t, runTools(t, h, Args{}), want{repotools.StatusSkipped, repotools.ReasonEndpointNoTools, 0, nil, llm.NoToolsNote})
	})
	t.Run("review in parts", func(t *testing.T) {
		h, _, _ := toolHarness(3, toolAnswer(goodAnswer))
		h.llm.answers = []string{goodAnswer}
		h.prov.files = toolBigFiles(6, 400)
		h.deps.Config.LLM.ContextWindow = 9000
		check(t, runTools(t, h, Args{MaxChunks: 4}), want{repotools.StatusSkipped, repotools.ReasonReviewInParts, 0, nil, repotools.NoteReviewInParts})
	})
	t.Run("conversation failed", func(t *testing.T) {
		h, _, _ := toolHarness(3, func(llm.Conversation) (llm.Turn, error) { return llm.Turn{}, llm.ErrContextTooLong })
		check(t, runTools(t, h, Args{}), want{repotools.StatusSkipped, repotools.ReasonConversationFailed, 0, nil, repotools.NoteConversationFailed})
	})
	t.Run("nothing to review", func(t *testing.T) {
		h, _, _ := toolHarness(3, toolAnswer(goodAnswer))
		h.prov.files = nil
		check(t, runTools(t, h, Args{}), want{repotools.StatusSkipped, repotools.ReasonNoModelCall, 0, nil, ""})
	})
	t.Run("publish of supplied findings", func(t *testing.T) {
		h, _, _ := toolHarness(3, toolAnswer(goodAnswer))
		res := runTools(t, h, Args{Publish: true, Findings: []SuppliedFinding{supplied("pkg/a.go", 2, 2, "H", "C")}})
		check(t, res, want{repotools.StatusSkipped, repotools.ReasonNoModelCall, 0, nil, ""})
	})
	t.Run("no room", func(t *testing.T) {
		for window := 3000; window <= 9000; window += 100 {
			h, _, _ := toolHarness(3, toolAnswer(goodAnswer))
			h.prov.files = toolBigFiles(1, 120)
			h.deps.Config.LLM.ContextWindow = window
			res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
			if err != nil || toolsOf(res).Reason != repotools.ReasonNoRoom {
				continue
			}
			check(t, res, want{repotools.StatusSkipped, repotools.ReasonNoRoom, 0, nil, repotools.NoteNoRoom})
			return
		}
		t.Fatal("no window left the tools out for room")
	})
}
