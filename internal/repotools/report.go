package repotools

import (
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// Fixed notes of the repository tools (§2.3 item 3). The note of an
// endpoint that does not accept tools is llm.NoToolsNote.
const (
	NoteReviewInParts = "The review was made in parts, so repository tools were not used."
	// NoteConversationFailed and NoteNoRoom are the notes of the reasons
	// conversation_failed and no_room (WP-12b DQ-3).
	NoteConversationFailed = "A model call failed while the model was reading the repository through tools, so the review was made without them."
	NoteNoRoom             = "The diff left no room for repository tools, so the review was made without them."
)

// NoteBudgetUsedUp is the note of a run whose tool budget was used up after
// n calls.
func NoteBudgetUsedUp(n int) string {
	return "The repository tool budget was used up after " + strconv.Itoa(n) + " calls."
}

// Use is what a run did with the repository tools.
type Use struct {
	Status, Reason string
	Rounds         int
	Calls          []Call
	Tokens         int
	// Exhausted is true when a result did not fit the budget or the room
	// beside the diff.
	Exhausted bool
}

// Report is the use as coverage.repo_context.tools.
func (u Use) Report() *llmrun.RepoTools {
	r := &llmrun.RepoTools{Status: u.Status, Reason: u.Reason, Rounds: u.Rounds, Tokens: u.Tokens,
		Calls: make([]llmrun.RepoToolCall, 0, len(u.Calls))}
	for _, c := range u.Calls {
		r.Calls = append(r.Calls, llmrun.RepoToolCall(c))
	}
	return r
}

// Notes are the fixed notes of the use: why tools were not used, when that
// is worth a note, and a budget that was used up.
func (u Use) Notes() []string {
	var out []string
	switch u.Reason {
	case ReasonEndpointNoTools:
		out = append(out, llm.NoToolsNote)
	case ReasonReviewInParts:
		out = append(out, NoteReviewInParts)
	case ReasonConversationFailed:
		out = append(out, NoteConversationFailed)
	case ReasonNoRoom:
		out = append(out, NoteNoRoom)
	}
	if u.Exhausted {
		ran := 0
		for _, c := range u.Calls {
			if c.Status != StatusBudgetExhausted && c.Status != StatusTooManyCalls {
				ran++
			}
		}
		out = append(out, NoteBudgetUsedUp(ran))
	}
	return out
}
