package repotools

import (
	"slices"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// TestSchemaEnumsHoldEveryValue: the declared schema lists every status,
// reason and call status this package uses, and nothing else.
func TestSchemaEnumsHoldEveryValue(t *testing.T) {
	s := llmrun.RepoToolsSchema("x")
	props := s["properties"].(map[string]any)
	enum := func(m map[string]any) []string {
		var out []string
		for _, v := range m["enum"].([]any) {
			out = append(out, v.(string))
		}
		slices.Sort(out)
		return out
	}
	sorted := func(v ...string) []string { slices.Sort(v); return v }
	if got, want := enum(props["status"].(map[string]any)), sorted(StatusUsed, StatusNotUsed, StatusOff, StatusSkipped); !slices.Equal(got, want) {
		t.Errorf("status enum %v, want %v", got, want)
	}
	if got, want := enum(props["reason"].(map[string]any)), sorted("", ReasonOff, ReasonRepoContextUnavailable, ReasonReviewInParts,
		ReasonEndpointNoTools, ReasonNoRoom, ReasonNoModelCall, ReasonConversationFailed); !slices.Equal(got, want) {
		t.Errorf("reason enum %v, want %v", got, want)
	}
	call := props["calls"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	if got, want := enum(call["status"].(map[string]any)), sorted(StatusOK, StatusNotAllowed, StatusNotAvailable, StatusTooLarge,
		StatusBudgetExhausted, StatusInvalidArguments, StatusTooManyCalls); !slices.Equal(got, want) {
		t.Errorf("call status enum %v, want %v", got, want)
	}
}

// TestUseReportAndNotes: the use as coverage (calls never null) and its
// fixed notes.
func TestUseReportAndNotes(t *testing.T) {
	r := Use{Status: StatusOff, Reason: ReasonOff}.Report()
	if r.Calls == nil || len(r.Calls) != 0 || r.Status != StatusOff {
		t.Fatalf("%+v", r)
	}
	u := Use{Status: StatusUsed, Rounds: 2, Tokens: 90, Exhausted: true, Calls: []Call{
		{Tool: ReadFile, Target: "a.go", Status: StatusOK, Tokens: 80},
		{Tool: ReadFile, Target: "../x", Status: StatusNotAllowed, Tokens: 10},
		{Tool: ReadFile, Target: "b.go", Status: StatusBudgetExhausted},
		{Tool: ListDir, Status: StatusTooManyCalls},
	}}
	if r := u.Report(); len(r.Calls) != 4 || r.Calls[1] != (llmrun.RepoToolCall{Tool: ReadFile, Target: "../x", Status: StatusNotAllowed, Tokens: 10}) {
		t.Fatalf("%+v", r)
	}
	if got := u.Notes(); !slices.Equal(got, []string{"The repository tool budget was used up after 2 calls."}) {
		t.Fatalf("%q", got)
	}
	for reason, note := range map[string]string{ReasonEndpointNoTools: llm.NoToolsNote, ReasonReviewInParts: NoteReviewInParts,
		ReasonConversationFailed: NoteConversationFailed, ReasonNoRoom: NoteNoRoom} {
		if got := (Use{Status: StatusSkipped, Reason: reason}).Notes(); !slices.Equal(got, []string{note}) {
			t.Errorf("%s: %q", reason, got)
		}
	}
	for _, reason := range []string{ReasonRepoContextUnavailable, ReasonNoModelCall, ReasonOff} {
		if got := (Use{Status: StatusSkipped, Reason: reason}).Notes(); len(got) != 0 {
			t.Errorf("%s: %q", reason, got)
		}
	}
}
