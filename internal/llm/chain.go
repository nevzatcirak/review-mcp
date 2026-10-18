package llm

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Chain is the fallback model chain of one run (X-38, v2.1 spec V-6 §3): the
// primary client and up to three fallback clients, tried in order when a
// model call cannot be served by the current model. It implements the
// Completer of the pipelines, plus CompleteNext for an answer that stays
// unusable after the same-model re-ask. A Chain belongs to one run (one tool
// call): after a call falls back, the rest of the run starts at the model
// that served it (sticky).
type Chain struct {
	models []*Client
	// factor is llm.token_estimate_factor (the primary's), for the size
	// check of a fallback.
	factor float64

	mu sync.Mutex
	// cur is the index the next call starts at (sticky).
	cur int
	// last is the index of the model that served the last call, -1 before
	// one did.
	last int
	// pending is the reason the run moved to cur without a call served
	// there yet (a conversation that restarts on the next model), for the
	// next call served at a fallback.
	pending string
	use     ChainUse
}

// ChainUse is what a run's chain reports (§3.4).
type ChainUse struct {
	// Model is the model that served the last call ("" before one did).
	Model string
	// ModelsUsed are the distinct models that served calls, in order of
	// first use.
	ModelsUsed []string
	// FallbackCalls is the number of calls a fallback served.
	FallbackCalls int
	// FallbackModel is the model of the first fallback that served a call,
	// and FallbackReason the reason of the first move (a Reason constant).
	FallbackModel, FallbackReason string
	// SizeSkips counts, per fallback model in chain order, the calls that
	// skipped it because the prompt does not fit its context window.
	SizeSkips []SizeSkip
}

// SizeSkip is a fallback skipped for size (§3.2), and how often.
type SizeSkip struct {
	Model string
	Calls int
}

// Reasons of a move to the next model, as the fallback note names them
// (§3.4 item 2).
const (
	ReasonRateLimited = "rate limited"
	ReasonServerError = "server error"
	ReasonTimedOut    = "timed out"
	ReasonUnreachable = "unreachable"
	ReasonUnusable    = "unusable answer"
)

// NewChain returns the chain of primary and fallbacks; factor is
// llm.token_estimate_factor. Without fallbacks it behaves as primary.
func NewChain(primary *Client, fallbacks []*Client, factor float64) *Chain {
	return &Chain{models: append([]*Client{primary}, fallbacks...), factor: factor, last: -1}
}

// Complete sends the prompt to the current model of the run and moves on
// along the chain while the call cannot be served (§3.2): when the model's
// own retries are used up for a server error, a timeout or a transport
// failure, or it stays rate limited. An auth error, a refused request, a
// canceled context and any other failure end the call. When every model
// tried failed, the error of the first one is returned, with the note that
// the fallbacks failed as well (§3.4 item 4).
func (c *Chain) Complete(ctx context.Context, system, user string) (*Response, error) {
	c.mu.Lock()
	start := c.cur
	c.mu.Unlock()
	var resp *Response
	_, err := c.from(ctx, start, "", c.promptNeed(system, user), func(m *Client) error {
		r, err := m.Complete(ctx, system, user)
		resp = r
		return err
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// promptNeed is the estimate of a single-pass prompt.
func (c *Chain) promptNeed(system, user string) func() int {
	return func() int { return tokens.Estimate(system, c.factor) + tokens.Estimate(user, c.factor) }
}

// ErrRestart reports that a later turn of a conversation failed on the
// model that started it, in a way that moves to the next model: the
// conversation must be started again from its first turn, which the chain
// then sends to the next model (V-8 spec §1 item 3). It wraps the failure.
var ErrRestart = errors.New("llm: the conversation must restart on the next model")

// restartError is ErrRestart wrapping the failure of the turn.
type restartError struct{ err error }

func (e *restartError) Error() string        { return e.err.Error() }
func (e *restartError) Unwrap() []error      { return []error{ErrRestart, e.err} }
func (e *restartError) Is(target error) bool { return target == ErrRestart }

// Converse sends one turn of a conversation (§1 item 3). The run's chain
// stays sticky, and a conversation never changes model in the middle:
//   - its first turn (no assistant message yet) moves along the chain as
//     Complete does, so a run whose primary fails before the first turn
//     starts on the fallback;
//   - a later turn goes only to the model the run is on. When it fails in
//     a way that would move a call on and a later model exists, the run
//     moves to that model and the error wraps ErrRestart: the caller may
//     start the conversation again once (or continue single-pass, with a
//     note). Any other failure is returned as it is.
//
// DESIGN-QUESTION (WP-12a DQ-4): "the run starts on the fallback" is the
// chain's sticky index, so after a restart every later call of the run,
// single-pass ones included, starts at the fallback, as after any move of
// Complete. The fallback note names the reason of the failed turn.
// Recommendation: keep.
func (c *Chain) Converse(ctx context.Context, conv Conversation) (Turn, error) {
	c.mu.Lock()
	start := c.cur
	c.mu.Unlock()
	var turn Turn
	call := func(m *Client) error {
		t, err := m.Converse(ctx, conv)
		turn = t
		return err
	}
	if !conv.started() {
		_, err := c.from(ctx, start, "", func() int { return conv.estimate(c.factor) }, call)
		if err != nil {
			return Turn{}, err
		}
		return turn, nil
	}
	m := c.models[start]
	err := call(m)
	if err == nil {
		c.served(start, "")
		return turn, nil
	}
	if ctx.Err() != nil {
		return Turn{}, ctx.Err()
	}
	r, moves := moveReason(err)
	m.logger.Debug("llm model call failed", "model_index", start, "moves_on", moves, "reason", r, "turn", "later")
	if !moves || start+1 >= len(c.models) {
		return Turn{}, err
	}
	c.mu.Lock()
	if c.cur == start {
		c.cur, c.pending = start+1, r
	}
	c.mu.Unlock()
	return Turn{}, &restartError{err: err}
}

// CompleteNext sends the original prompt once to the model after the one
// that served the last call, because its answer stayed unusable after the
// same-model re-ask (§3.2 item 3); the chain then moves on as Complete does
// from there. ok is false when no later model could take the prompt (none
// is configured, or every one was skipped or failed); the caller then
// reports the unusable answer.
//
// DESIGN-QUESTION (WP-8c DQ-3): this is the one method the parse path needs
// (spec §3.3): the pipelines reach it through an optional interface
// (llmrun.CompleteNext), so their Completer interface is unchanged. A
// failure of the later models is not returned: the run's error stays the
// unusable answer, which is what went wrong first. Recommendation: keep.
func (c *Chain) CompleteNext(ctx context.Context, system, user string) (*Response, bool, error) {
	c.mu.Lock()
	start := c.last + 1
	if c.last < 0 {
		start = c.cur + 1
	}
	c.mu.Unlock()
	if start >= len(c.models) {
		return nil, false, nil
	}
	var resp *Response
	_, err := c.from(ctx, start, ReasonUnusable, c.promptNeed(system, user), func(m *Client) error {
		r, err := m.Complete(ctx, system, user)
		resp = r
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, nil
	}
	return resp, true, nil
}

// from runs call along the chain from index start. moved is the reason the
// call is already on the move (CompleteNext), "" for a call at its starting
// model; need is the prompt's estimate, for the size check of a fallback.
// It returns the index that served the call.
//
// DESIGN-QUESTION (WP-8c DQ-4): "the error of the primary" (§3.4 item 4) is
// read as the error of the first model this call tried: after a sticky
// move that is the fallback the run had moved to, since the primary was not
// asked again. The note counts the fallbacks this call tried and that
// failed (skipped ones are not counted); when the first model fails with a
// class that does not move on, no fallback was tried and there is no note.
// Recommendation: keep.
func (c *Chain) from(ctx context.Context, start int, moved string, need func() int, call func(*Client) error) (int, error) {
	var first error
	failed := 0
	reason := moved
	for i := start; i < len(c.models); i++ {
		m := c.models[i]
		if i > 0 {
			if ok, err := c.fits(ctx, i, need); err != nil {
				return -1, err
			} else if !ok {
				continue
			}
		}
		err := call(m)
		if err == nil {
			c.served(i, reason)
			return i, nil
		}
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		if errors.Is(err, ErrNoTools) {
			// The model is up but refuses tools: the run stays on it and
			// continues single-pass there (§1 item 5).
			//
			// DESIGN-QUESTION (WP-12a DQ-6): when the primary failed first
			// (say, a server error) and the fallback refuses tools, the
			// run moves to the fallback, as if it had served, and goes on
			// single-pass there with the no-tools note; the fallback note
			// names the primary's failure. Trying the primary again
			// single-pass would ask a model that just failed.
			// Recommendation: keep.
			if i > start {
				c.moved(i, reason)
			}
			return -1, err
		}
		if first == nil && moved == "" {
			first = err
		} else {
			failed++
		}
		r, moves := moveReason(err)
		m.logger.Debug("llm model call failed", "model_index", i, "moves_on", moves, "reason", r)
		if !moves {
			break
		}
		if reason == "" {
			reason = r
		}
	}
	if first == nil {
		return -1, errors.New("llm: no model of the chain could take the prompt")
	}
	if failed > 0 {
		return -1, withAllFailed(first, failed)
	}
	return -1, first
}

// moveReason reports whether a failed call moves on to the next model
// (§3.2), and the reason the fallback note names. A rate-limited error
// moves on only once the client gave up on it: its rate-limit retries are
// used up, or the needed wait was too long; both reach here, since the
// client returns a 429 only then.
//
// DESIGN-QUESTION (WP-8c DQ-5): the spec lists the classes that move on
// (server error, timeout, transport, rate limited) and those that do not
// (auth, bad request, a canceled context, config_invalid). The classes it
// names in neither list do not move on: not found (a wrong model name, the
// same on every endpoint of a gateway), context too long (a run-level
// fallback for it is backlog, owner default 7) and protocol (an endpoint
// that answers in an unexpected shape). Recommendation: keep.
func moveReason(err error) (string, bool) {
	cls, ok := ClassOf(err)
	if !ok {
		return "", false
	}
	switch cls {
	case ClassRateLimited:
		return ReasonRateLimited, true
	case ClassUpstream:
		return ReasonServerError, true
	case ClassTimeout:
		return ReasonTimedOut, true
	case ClassTransport:
		return ReasonUnreachable, true
	}
	return "", false
}

// fits reports whether the prompt fits fallback i's context window (§3.2):
// the prompt's estimate (llm.token_estimate_factor) plus the fallback's
// maximum output must be at most its window. A window that is not
// configured is resolved from the fallback's endpoint (X-15); a skipped
// fallback is counted for the run's note.
//
// DESIGN-QUESTION (WP-8c DQ-6): without llm.max_output_tokens the output is
// counted as the budget's reserve of 1000 tokens (tokens.Budget), the same
// room the run's own budget leaves; a window that cannot be resolved skips
// the fallback for the call without counting it as a size skip (the note
// would be wrong) and logs it. Recommendation: keep.
func (c *Chain) fits(ctx context.Context, i int, prompt func() int) (bool, error) {
	m := c.models[i]
	window := m.cfg.ContextWindow
	if window <= 0 {
		n, _, err := m.ResolveContextWindow(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			m.logger.Debug("llm fallback skipped", "model_index", i, "reason", "window_unknown")
			return false, nil
		}
		window = n
	}
	need := prompt() + tokens.Budget{MaxOutputTokens: m.cfg.MaxOutputTokens}.HardReserve()
	if need <= window {
		return true, nil
	}
	m.logger.Debug("llm fallback skipped", "model_index", i, "reason", "size", "need", need, "window", window)
	c.mu.Lock()
	defer c.mu.Unlock()
	model := m.cfg.Model
	for k := range c.use.SizeSkips {
		if c.use.SizeSkips[k].Model == model {
			c.use.SizeSkips[k].Calls++
			return false, nil
		}
	}
	c.use.SizeSkips = append(c.use.SizeSkips, SizeSkip{Model: model, Calls: 1})
	return false, nil
}

// moved records that the run moved to model i for reason without a call
// served there yet; the next call starts at it.
func (c *Chain) moved(i int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur, c.pending = i, reason
}

// served records that model i served a call, after moving for reason ("" at
// the starting model). The run stays on it (sticky, owner default 6).
func (c *Chain) served(i int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reason == "" && i > 0 {
		reason = c.pending
	}
	c.pending = ""
	c.cur, c.last = i, i
	model := c.models[i].cfg.Model
	c.use.Model = model
	if !slices.Contains(c.use.ModelsUsed, model) {
		c.use.ModelsUsed = append(c.use.ModelsUsed, model)
	}
	if i > 0 {
		c.use.FallbackCalls++
		if c.use.FallbackModel == "" {
			c.use.FallbackModel, c.use.FallbackReason = model, reason
		}
	}
}

// withAllFailed returns err with the note that n fallbacks failed as well,
// keeping its class; an error that is not an *Error is returned as it is.
func withAllFailed(err error, n int) error {
	var le *Error
	if !errors.As(err, &le) {
		return err
	}
	cp := *le
	cp.note = "All " + strconv.Itoa(n) + " fallback models failed as well."
	return &cp
}

// Use returns what the chain reports of the run so far (§3.4).
func (c *Chain) Use() ChainUse {
	c.mu.Lock()
	defer c.mu.Unlock()
	u := c.use
	u.ModelsUsed = append([]string(nil), u.ModelsUsed...)
	u.SizeSkips = append([]SizeSkip(nil), u.SizeSkips...)
	return u
}

// ResolveContextWindow is the primary's: the run budgets for the primary
// model (X-15).
func (c *Chain) ResolveContextWindow(ctx context.Context) (int, string, error) {
	return c.models[0].ResolveContextWindow(ctx)
}

// RateLimitWaits sums the rate-limit waits of every model of the chain.
func (c *Chain) RateLimitWaits() (int, time.Duration) {
	n, d := 0, time.Duration(0)
	for _, m := range c.models {
		mn, md := m.RateLimitWaits()
		n, d = n+mn, d+md
	}
	return n, d
}

// CloseIdleConnections closes the idle connections of every model.
func (c *Chain) CloseIdleConnections() {
	if c == nil {
		return
	}
	for _, m := range c.models {
		m.CloseIdleConnections()
	}
}
