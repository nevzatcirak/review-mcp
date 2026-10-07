package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/logging"
)

const (
	modelsPath = "/models"
	// probeTimeout bounds the context-window probe (X-15).
	probeTimeout = 10 * time.Second
	// MinContextWindow is the smallest context window review-mcp works
	// with; it is also the floor of a resolved value and the minimum of
	// llm.context_window.
	MinContextWindow = 4096
)

// Fixed sentences of the probe (X-15). They name the configuration key to
// change and never echo endpoint content.
const (
	modelNotListedSentence = "the LLM endpoint does not list the configured model; check llm.model"
	noWindowSentence       = "the LLM endpoint does not report the model's context window; set llm.context_window"
	windowTooSmallSentence = "the LLM endpoint reports a context window below the 4096-token minimum; set llm.context_window if the endpoint is wrong"
)

// windowFields is the exact field order of X-15: the first one holding a
// positive integer wins. Training-size fields (n_ctx_train, meta.n_ctx_train
// and any other metadata) are deliberately absent: a server often serves a
// smaller context than the model was trained with.
var windowFields = [...]string{"max_model_len", "context_length", "context_window", "max_context_length"}

// modelEntry is one entry of the model list. Only the id and the window
// fields are decoded; every other field is ignored.
type modelEntry struct {
	ID     string                     `json:"id"`
	Fields map[string]json.RawMessage `json:"-"`
}

func (m *modelEntry) UnmarshalJSON(b []byte) error {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	m.Fields = make(map[string]json.RawMessage, len(windowFields))
	for _, f := range windowFields {
		if v, ok := all[f]; ok {
			m.Fields[f] = v
		}
	}
	if raw, ok := all["id"]; ok {
		// A non-string id simply never matches.
		_ = json.Unmarshal(raw, &m.ID)
	}
	return nil
}

// parseModelList accepts {"data":[...]} and a bare array.
func parseModelList(data []byte) ([]modelEntry, bool) {
	data = bytes.TrimSpace(data)
	var list []modelEntry
	if len(data) > 0 && data[0] == '[' {
		return list, json.Unmarshal(data, &list) == nil
	}
	var wrapped struct {
		Data []modelEntry `json:"data"`
	}
	if json.Unmarshal(data, &wrapped) != nil {
		return nil, false
	}
	return wrapped.Data, true
}

// positiveInt reads a JSON number or a numeric string holding a positive
// integer.
func positiveInt(raw json.RawMessage) (int64, bool) {
	s := strings.TrimSpace(string(raw))
	if strings.HasPrefix(s, `"`) {
		var str string
		if json.Unmarshal(raw, &str) != nil {
			return 0, false
		}
		s = strings.TrimSpace(str)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// windowFrom applies X-15 to the entry whose id equals model.
func windowFrom(entries []modelEntry, model string) (reported int64, le *Error) {
	for i := range entries {
		if entries[i].ID != model {
			continue
		}
		for _, f := range windowFields {
			if raw, ok := entries[i].Fields[f]; ok {
				if n, ok := positiveInt(raw); ok {
					return n, nil
				}
			}
		}
		return 0, &Error{Class: ClassProtocol, Sentence: noWindowSentence}
	}
	return 0, &Error{Class: ClassNotFound, Sentence: modelNotListedSentence}
}

// UsableWindow turns the window m the endpoint reports into the budget:
// 90 % rounded down, at least MinContextWindow. A reported window below the
// minimum is refused rather than raised, so the result never exceeds what
// the server serves.
func UsableWindow(m int64) (int, bool) {
	if m < MinContextWindow {
		return 0, false
	}
	// floor(9m/10) without overflowing for any int64 m.
	n := m/10*9 + m%10*9/10
	if n < MinContextWindow {
		n = MinContextWindow
	}
	const maxInt = int64(^uint(0) >> 1)
	return int(min(n, maxInt)), true
}

// ResolveContextWindow returns the context window to budget for, from the
// endpoint (X-15): GET {base_url}/models, the entry whose id equals
// llm.model. It uses the client's authentication, redirect policy and
// response cap, a 10 s timeout and no retries. source is
// "endpoint, 90% of <m>".
//
// A success is cached per process and (base_url, model); a failure is not.
// Concurrent first calls share one probe. The cache holds the number only:
// the key that authorised the probe is never stored.
func (c *Client) ResolveContextWindow(ctx context.Context) (n int, source string, err error) {
	key := c.baseStr + "\x00" + c.cfg.Model
	r, err := windows.do(ctx, key, func() (windowResult, error) {
		m, le := c.probe(ctx)
		if le != nil {
			return windowResult{}, le
		}
		n, ok := UsableWindow(m)
		if !ok {
			return windowResult{}, &Error{Class: ClassProtocol, Sentence: windowTooSmallSentence}
		}
		return windowResult{n: n, source: "endpoint, 90% of " + strconv.FormatInt(m, 10)}, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		return 0, "", err
	}
	return r.n, r.source, nil
}

// probe fetches the model list and returns the window the endpoint reports.
func (c *Client) probe(ctx context.Context) (int64, *Error) {
	full := c.baseStr + modelsPath
	rctx, cancel := context.WithTimeout(ctx, c.probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, full, nil)
	if err != nil {
		return 0, &Error{Class: ClassProtocol, Detail: "invalid request"}
	}
	req.Header.Set("Accept", "application/json")
	c.setAuth(req)

	start := c.now()
	logFail := func(status int, class ErrorClass) {
		c.logger.Debug("llm models probe", "method", http.MethodGet, "url", logging.RedactURL(full),
			"status", status, "class", string(class), "duration_ms", c.now().Sub(start).Milliseconds())
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		le := c.transportError(ctx, rctx, err)
		logFail(0, le.Class)
		return 0, le
	}
	defer func() { _ = resp.Body.Close() }()
	status := resp.StatusCode
	if status < 200 || status >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainBytes))
		le := c.statusError(status, nil)
		logFail(status, le.Class)
		return 0, le
	}
	if resp.ContentLength > c.maxBytes {
		le := &Error{Class: ClassProtocol, Status: status, Detail: "response exceeded the size limit"}
		logFail(status, le.Class)
		return 0, le
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		le := c.transportError(ctx, rctx, err)
		logFail(status, le.Class)
		return 0, le
	}
	if int64(len(data)) > c.maxBytes {
		le := &Error{Class: ClassProtocol, Status: status, Detail: "response exceeded the size limit"}
		logFail(status, le.Class)
		return 0, le
	}
	entries, ok := parseModelList(data)
	if !ok {
		le := &Error{Class: ClassProtocol, Status: status, Detail: "response is not a model list"}
		logFail(status, le.Class)
		return 0, le
	}
	m, le := windowFrom(entries, c.cfg.Model)
	if le != nil {
		le.Status = status
		logFail(status, le.Class)
		return 0, le
	}
	c.logger.Debug("llm models probe", "method", http.MethodGet, "url", logging.RedactURL(full),
		"status", status, "class", "ok", "duration_ms", c.now().Sub(start).Milliseconds(), "reported_window", m)
	return m, nil
}

// ResolvedContextWindow returns the cached result for (baseURL, model), for
// readers that must not probe (server_info). ok is false until a probe has
// succeeded in this process.
func ResolvedContextWindow(baseURL, model string) (n int, source string, ok bool) {
	r, ok := windows.get(strings.TrimRight(baseURL, "/") + "\x00" + model)
	return r.n, r.source, ok
}

// windowResult is what the cache keeps: two non-secret values.
type windowResult struct {
	n      int
	source string
}

// windowCache is the process-wide cache of resolved windows. It coalesces
// concurrent first calls: one caller probes (the leader) while the others
// wait for it, so a burst of tool calls on a cold process sends one request
// to a possibly slow endpoint. A failed leader caches nothing; a waiter then
// becomes the next leader with its own context and key.
type windowCache struct {
	mu       sync.Mutex
	vals     map[string]windowResult
	inflight map[string]chan struct{}
}

var windows = &windowCache{vals: map[string]windowResult{}, inflight: map[string]chan struct{}{}}

func (w *windowCache) get(key string) (windowResult, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.vals[key]
	return r, ok
}

func (w *windowCache) do(ctx context.Context, key string, probe func() (windowResult, error)) (windowResult, error) {
	for {
		w.mu.Lock()
		if r, ok := w.vals[key]; ok {
			w.mu.Unlock()
			return r, nil
		}
		ch, busy := w.inflight[key]
		if !busy {
			ch = make(chan struct{})
			w.inflight[key] = ch
			w.mu.Unlock()
			return w.lead(key, ch, probe)
		}
		w.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return windowResult{}, ctx.Err()
		}
	}
}

func (w *windowCache) lead(key string, ch chan struct{}, probe func() (windowResult, error)) (windowResult, error) {
	var (
		r   windowResult
		err error
	)
	// The deferred release also runs if probe panics, so waiters never hang.
	defer func() {
		w.mu.Lock()
		if err == nil && r.n > 0 {
			w.vals[key] = r
		}
		delete(w.inflight, key)
		w.mu.Unlock()
		close(ch)
	}()
	r, err = probe()
	return r, err
}

// NoContextWindowError is the error for a call that has neither
// llm.context_window nor a client able to ask the endpoint.
func NoContextWindowError() *Error {
	return &Error{Class: ClassProtocol, Sentence: noWindowSentence}
}
