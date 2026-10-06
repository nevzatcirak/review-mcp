package review

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestPrepareMakesNoModelCall: Prepare is steps 1 to 6; it fetches, measures
// and renders but never calls the model, and what it returns is what Run
// goes on with.
func TestPrepareMakesNoModelCall(t *testing.T) {
	h := newHarness(goodAnswer)
	h.deps.LLM = nil // a dry run needs no LLM client
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.llm.calls) != 0 {
		t.Fatalf("Prepare called the model %d times", len(h.llm.calls))
	}
	if pl.Empty || pl.Prompts.System == "" || !strings.Contains(pl.Prompts.User, descMarker) {
		t.Fatalf("plan has no prompts: empty=%v", pl.Empty)
	}
	m := pl.Result.Metadata
	if m.PromptTokens <= 0 || m.DiffTokens <= 0 || m.RequestTokens <= m.PromptTokens || m.LLMCalls != 0 {
		t.Errorf("metadata = %+v", m)
	}
	if pl.Budget.PromptTokens != m.PromptTokens || pl.Budget.ContextWindow != 32000 {
		t.Errorf("budget = %+v", pl.Budget)
	}
	if got := pl.Result.Coverage.Included; !reflect.DeepEqual(got, []string{"src/app.go", "src/util.go"}) {
		t.Errorf("included = %v", got)
	}
	if h.prov.posted != nil {
		t.Error("Prepare posted a comment")
	}

	// Run on the same inputs sends exactly the planned prompts.
	h2 := newHarness(goodAnswer)
	res, err := Run(context.Background(), h2.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if len(h2.llm.calls) != 1 || h2.llm.calls[0].system != pl.Prompts.System || h2.llm.calls[0].user != pl.Prompts.User {
		t.Error("Run did not send the prompts Prepare returned")
	}
	if res.Metadata.RequestTokens != m.RequestTokens || res.Metadata.PromptTokens != m.PromptTokens {
		t.Errorf("Run metadata %+v differs from the plan %+v", res.Metadata, m)
	}
}

func TestPrepareDegradedConfig(t *testing.T) {
	h := newHarness(goodAnswer)
	h.deps.ConfigErr = errors.New("bad")
	if _, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL}); !errors.Is(err, ErrConfigInvalid) {
		t.Errorf("err = %v", err)
	}
	if h.resolver.calls != 0 || h.prov.calls != 0 {
		t.Error("a degraded config reached the resolver or the provider")
	}
}

func TestPrepareEmptyDiff(t *testing.T) {
	h := newHarness(goodAnswer)
	h.prov.files = h.prov.files[2:] // only the filtered vendor file
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if !pl.Empty || pl.Result.Metadata.RequestTokens != 0 || pl.Prompts.System != "" {
		t.Errorf("plan = empty %v, request tokens %d", pl.Empty, pl.Result.Metadata.RequestTokens)
	}
}

func TestRunReportsProgressStages(t *testing.T) {
	h := newHarness(goodAnswer)
	var stages []string
	h.deps.Progress = func(s string) { stages = append(stages, s) }
	if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stages, "|"); got != "fetching|preparing diff|calling model" {
		t.Errorf("stages = %q", got)
	}
}
