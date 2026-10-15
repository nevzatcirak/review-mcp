package llmrun

import "github.com/nevzatcirak/review-mcp/internal/llm"

// ChainReporter is a model client with a fallback chain (*llm.Chain) that
// reports which models served the run's calls (X-38).
type ChainReporter interface {
	Use() llm.ChainUse
}

// FallbackReport is what a tool's metadata and notes say of the models of
// its run (v2.1 spec V-6 §3.4).
type FallbackReport struct {
	// Model is the model that served the last call, "" when unknown.
	Model string
	// ModelsUsed are the distinct models that served calls, in order of
	// first use; never nil.
	ModelsUsed []string
	// FallbackCalls is the number of calls a fallback served.
	FallbackCalls int
	// Notes are the fallback note and the size-skip notes.
	Notes []string
}

// Fallback reports the models of the run's client c. For a client without a
// chain (a test double), a run that made calls (calls > 0) used model, the
// configured one.
func Fallback(c any, model string, calls int) FallbackReport {
	r, ok := c.(ChainReporter)
	if !ok {
		rep := FallbackReport{ModelsUsed: []string{}}
		if calls > 0 {
			rep.ModelsUsed = []string{model}
		}
		return rep
	}
	u := r.Use()
	rep := FallbackReport{Model: u.Model, ModelsUsed: append([]string{}, u.ModelsUsed...), FallbackCalls: u.FallbackCalls}
	if u.FallbackCalls > 0 {
		rep.Notes = append(rep.Notes, FallbackNote(u.FallbackCalls, u.FallbackModel, u.FallbackReason))
	}
	for _, s := range u.SizeSkips {
		rep.Notes = append(rep.Notes, SizeSkipNote(s.Model, s.Calls))
	}
	return rep
}

// FallbackNote is the note of a run whose calls a fallback served (§3.4
// item 2): "N model calls were served by a fallback model (`second-model`)
// because the primary model was unavailable (rate limited)." The model is
// the first fallback that served a call, the reason that of the first move.
//
// DESIGN-QUESTION (WP-8c DQ-7): the sentence keeps the spec's words for
// every reason, also "unusable answer", where the primary did answer but
// its answer could not be used. The count form follows the other notes
// ("1 model call was served"). Recommendation: keep.
func FallbackNote(calls int, model, reason string) string {
	return CountPhrase(calls, "model call was", "model calls were") + " served by a fallback model (`" + model +
		"`) because the primary model was unavailable (" + reason + ")."
}

// SizeSkipNote is the note of a fallback skipped for size (§3.4 item 3):
// "The fallback model `x` was skipped for N calls because the prompt does
// not fit its context window."
func SizeSkipNote(model string, calls int) string {
	return "The fallback model `" + model + "` was skipped for " + CountPhrase(calls, "call", "calls") +
		" because the prompt does not fit its context window."
}
