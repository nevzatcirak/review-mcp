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
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repotools"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

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
	if u := res.tools; u.status != repotools.StatusUsed || u.rounds != 2 || len(u.calls) != 2 ||
		u.calls[0].Status != repotools.StatusOK || u.calls[1].Status != repotools.StatusNotAvailable || u.tokens <= 0 {
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
	if u := res.tools; u.status != repotools.StatusNotUsed || u.rounds != 0 || len(u.calls) != 0 ||
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
	if len(tl.convs) != 2 || tl.convs[1].ToolChoice != llm.ToolChoiceNone || res.tools.rounds != 1 || res.Metadata.LLMCalls != 2 {
		t.Fatalf("requests %d, tool use %+v", len(tl.convs), res.tools)
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
	if res.tools.status != repotools.StatusUsed || res.tools.rounds != 1 {
		t.Fatalf("tool use %+v", res.tools)
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
	if u := res.tools; u.status != repotools.StatusSkipped || u.reason != repotools.ReasonEndpointNoTools {
		t.Fatalf("tool use %+v", u)
	}

	h, _, tl = toolHarness(3, toolAnswer(goodAnswer))
	tl.refused = true
	res = runTools(t, h, Args{})
	if len(tl.convs) != 0 || len(h.llm.calls) != 1 || res.tools.reason != repotools.ReasonEndpointNoTools {
		t.Fatalf("requests %d, completes %d, %+v", len(tl.convs), len(h.llm.calls), res.tools)
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
	if len(h.llm.calls) != 1 || res.tools.status != repotools.StatusSkipped || res.tools.reason != repotools.ReasonConversationFailed ||
		len(tl.convs) != 3 || res.Metadata.LLMCalls != 4 {
		t.Fatalf("requests %d, completes %d, %+v, llm calls %d", len(tl.convs), len(h.llm.calls), res.tools, res.Metadata.LLMCalls)
	}
}

// TestToolsOffAndSkipped: the reasons a run does not use tools.
func TestToolsOffAndSkipped(t *testing.T) {
	t.Run("rounds 0", func(t *testing.T) {
		h, _, tl := toolHarness(0, toolAnswer(goodAnswer))
		res := runTools(t, h, Args{})
		if res.tools.status != repotools.StatusOff || res.tools.reason != repotools.ReasonOff || len(tl.convs) != 0 {
			t.Fatalf("%+v", res.tools)
		}
	})
	t.Run("repository context off", func(t *testing.T) {
		h, _, tl := toolHarness(3, toolAnswer(goodAnswer))
		h.deps.Config.Context.Repo.Enabled = false
		res := runTools(t, h, Args{})
		if res.tools.status != repotools.StatusOff || len(tl.convs) != 0 {
			t.Fatalf("%+v", res.tools)
		}
	})
	t.Run("repository context unavailable", func(t *testing.T) {
		h, tr, tl := toolHarness(3, toolAnswer(goodAnswer))
		tr.ensureErr = &gitctx.Error{Reason: gitctx.ReasonAuth}
		res := runTools(t, h, Args{})
		if res.tools.status != repotools.StatusSkipped || res.tools.reason != repotools.ReasonRepoContextUnavailable || len(tl.convs) != 0 {
			t.Fatalf("%+v", res.tools)
		}
	})
	t.Run("a model without tool calling", func(t *testing.T) {
		h, _, _ := toolHarness(3, toolAnswer(goodAnswer))
		h.deps.LLM = h.llm
		res := runTools(t, h, Args{})
		if res.tools.reason != repotools.ReasonEndpointNoTools || len(h.llm.calls) != 1 {
			t.Fatalf("%+v", res.tools)
		}
	})
	t.Run("nothing to review", func(t *testing.T) {
		h, _, tl := toolHarness(3, toolAnswer(goodAnswer))
		h.prov.files = nil
		res := runTools(t, h, Args{})
		if res.tools.status != repotools.StatusSkipped || res.tools.reason != repotools.ReasonNoModelCall || len(tl.convs) != 0 {
			t.Fatalf("%+v", res.tools)
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
		if res.tools.status != repotools.StatusSkipped || res.tools.reason != repotools.ReasonReviewInParts || len(tl.convs) != 0 {
			t.Fatalf("%+v", res.tools)
		}
	})
	t.Run("a repository file cannot turn them on", func(t *testing.T) {
		h, _, tl := toolHarness(0, toolAnswer(goodAnswer))
		withRepoConfig(h, "[context.repo]\nmax_tool_rounds = 3\nmax_tool_tokens = 40000\n")
		res := runTools(t, h, Args{})
		if res.tools.status != repotools.StatusOff || len(tl.convs) != 0 {
			t.Fatalf("%+v", res.tools)
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
// no tool round goes out with the diff trimmed: every request carries the
// user prompt with the whole diff and fits the window beside the output
// reserve. The sweep both uses the tools and leaves them out for room.
func TestToolsNeverTrimTheDiff(t *testing.T) {
	used, noRoom := false, false
	for window := 3000; window <= 9000; window += 100 {
		h, _, tl := toolHarness(3,
			callTools(readCall("c1", "pkg/store.go"), readCall("c2", "pkg/store.go")),
			callTools(readCall("c3", "pkg/store.go")),
			toolAnswer(goodAnswer))
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
				t.Fatalf("window %d: request %d estimates %d over the limit %d", window, i+1, n, limit)
			}
		}
		used = used || res.tools.status == repotools.StatusUsed
		noRoom = noRoom || res.tools.reason == repotools.ReasonNoRoom
	}
	if !used || !noRoom {
		t.Fatalf("the sweep never used tools (%v) or never left them out for room (%v)", used, noRoom)
	}
}
