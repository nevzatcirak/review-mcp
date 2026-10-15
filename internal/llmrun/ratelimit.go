package llmrun

import (
	"strconv"
	"time"
)

// RateLimitReporter is a model client that reports the rate-limit waits of
// its run (X-37); *llm.Client is one. A Completer without it reports none.
type RateLimitReporter interface {
	RateLimitWaits() (int, time.Duration)
}

// RateLimitWaits returns the rate-limit waits of the run's client c: the
// number of requests that waited and the total wait in milliseconds; 0 and
// 0 for a client that does not report them.
func RateLimitWaits(c any) (waits int, waitMS int64) {
	r, ok := c.(RateLimitReporter)
	if !ok {
		return 0, 0
	}
	n, d := r.RateLimitWaits()
	return n, d.Milliseconds()
}

// rateLimitNoteMin is the least total wait that RateLimitNote reports.
const rateLimitNoteMin = time.Second

// RateLimitNote is the note of a run the endpoint's rate limit delayed
// (v2.1 spec V-6 §2.3 item 2): "The LLM endpoint's rate limit delayed this
// run by N s.", with the total wait rounded to whole seconds; "" below one
// second.
//
// DESIGN-QUESTION (WP-8b DQ-4): each tool adds the waits and this note
// after its run, so they are in the tool result but not in a comment the
// run published: a delay of the model endpoint concerns the caller, not
// the readers of the pull request, and the waits are only complete once
// every model call is done. Recommendation: keep.
func RateLimitNote(waitMS int64) string {
	d := time.Duration(waitMS) * time.Millisecond
	if d < rateLimitNoteMin {
		return ""
	}
	return "The LLM endpoint's rate limit delayed this run by " + strconv.FormatInt(int64(d.Round(time.Second)/time.Second), 10) + " s."
}
