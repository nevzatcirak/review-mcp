package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	headSHA   = "1234567890abcdef1234567890abcdef12345678"
	pullPath  = "/repos/octo/demo/pulls/7"
	postRev   = "POST /api/v3" + pullPath + "/reviews"
	postOne   = "POST /api/v3" + pullPath + "/comments"
	listRev55 = "GET /api/v3" + pullPath + "/reviews/55/comments?per_page=100"
)

// Bodies of the inline items; they must never reach a log or an error.
const (
	bodyOne   = "INLINE-BODY-one-7c2e"
	bodyRange = "INLINE-BODY-range-7c2e"
	bodyOut   = "INLINE-BODY-outside-7c2e"
)

func inlineItems() []provider.InlineComment {
	return []provider.InlineComment{
		{Path: "a.go", Line: 4, LineType: provider.LineAdded, Body: bodyOne},
		{Path: "a.go", Line: 10, EndLine: 12, LineType: provider.LineContext, Body: bodyRange},
		{Path: "b.go", Line: 99, LineType: provider.LineContext, Body: bodyOut},
	}
}

// recorded is the JSON body of each write request, in order.
type recorded struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (r *recorded) add(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	b, _ := io.ReadAll(req.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Errorf("request body is not JSON: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, m)
	return m
}

func (r *recorded) all() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.bodies)
}

// position is the position fields of a comment request, "path:line" plus
// ":start_line" for a range, with the sides checked.
func position(t *testing.T, c map[string]any) string {
	t.Helper()
	if c["side"] != "RIGHT" {
		t.Errorf("side = %v, want RIGHT", c["side"])
	}
	s := c["path"].(string) + ":" + strconv.Itoa(int(c["line"].(float64)))
	if sl, ok := c["start_line"]; ok {
		if c["start_side"] != "RIGHT" {
			t.Errorf("start_side = %v, want RIGHT", c["start_side"])
		}
		s += ":" + strconv.Itoa(int(sl.(float64)))
	} else if _, ok := c["start_side"]; ok {
		t.Errorf("start_side without start_line: %v", c)
	}
	if _, ok := c["position"]; ok {
		t.Errorf("the deprecated position field is sent: %v", c)
	}
	return s
}

func inlineProvider(t *testing.T, f *fake) (*Provider, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	pp, err := NewFactory().New(f.config(""), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	p := pp.(*Provider)
	p.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	p.sleep = func(context.Context, time.Duration) error { return errors.New("no wait expected") }
	return p, &logs
}

// checkQuiet fails t when the logs or a result's error carry an item body,
// the token or response body text.
func checkQuiet(t *testing.T, logs string, res []provider.InlineResult) {
	t.Helper()
	texts := []string{logs}
	for _, r := range res {
		texts = append(texts, r.Error)
	}
	for _, s := range texts {
		for _, bad := range []string{bodyOne, bodyRange, bodyOut, testToken, testSentinel} {
			if strings.Contains(s, bad) {
				t.Errorf("%q leaks into %q", bad, s)
			}
		}
	}
}

// TestPostInlineBatch: the items go into one review (event COMMENT on the
// head commit, no review body; a range sends start_line and start_side),
// and each result gets its comment's id and URL from the review's
// comments, matched by path, body and line. One POST and one GET.
func TestPostInlineBatch(t *testing.T) {
	f := newFake(t, "/api/v3")
	var rec recorded
	f.handle(http.MethodPost, pullPath+"/reviews", func(w http.ResponseWriter, r *http.Request) {
		rec.add(t, r)
		writeJSON(w, map[string]any{"id": 55, "state": "COMMENTED", "html_url": "https://github.example.com/octo/demo/pull/7#pullrequestreview-55"})
	})
	// The listing order differs from the items', and a same-body comment on
	// another line comes first: matching prefers the item's line.
	f.json(pullPath+"/reviews/55/comments", []any{
		map[string]any{"id": 503, "path": "b.go", "line": 99, "body": bodyOut, "html_url": "https://github.example.com/c/503"},
		map[string]any{"id": 502, "path": "a.go", "line": 11, "body": bodyRange, "html_url": "https://github.example.com/c/502"},
		map[string]any{"id": 504, "path": "a.go", "line": 12, "body": bodyRange, "html_url": "https://github.example.com/c/504"},
		map[string]any{"id": 501, "path": "a.go", "line": 4, "body": bodyOne, "html_url": "https://github.example.com/c/501"},
	})
	p, logs := inlineProvider(t, f)
	res, err := p.PostInlineComments(t.Context(), testRef(), &provider.PullRequest{HeadSHA: headSHA}, inlineItems())
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.InlineResult{
		{Posted: true, ID: "501", URL: "https://github.example.com/c/501", Reason: provider.InlineReasonPosted},
		{Posted: true, ID: "504", URL: "https://github.example.com/c/504", Reason: provider.InlineReasonPosted},
		{Posted: true, ID: "503", URL: "https://github.example.com/c/503", Reason: provider.InlineReasonPosted},
	}
	if !slices.Equal(res, want) {
		t.Errorf("results\n%+v\nwant\n%+v", res, want)
	}
	bodies := rec.all()
	if len(bodies) != 1 {
		t.Fatalf("%d review requests, want 1", len(bodies))
	}
	b := bodies[0]
	if b["event"] != "COMMENT" || b["commit_id"] != headSHA {
		t.Errorf("review event %v, commit %v", b["event"], b["commit_id"])
	}
	if _, ok := b["body"]; ok {
		t.Errorf("the review has a body of its own: %v", b["body"])
	}
	var got []string
	for _, c := range b["comments"].([]any) {
		m := c.(map[string]any)
		if _, ok := m["commit_id"]; ok {
			t.Errorf("a review comment carries commit_id: %v", m)
		}
		got = append(got, position(t, m)+" "+m["body"].(string))
	}
	if wantPos := []string{"a.go:4 " + bodyOne, "a.go:12:10 " + bodyRange, "b.go:99 " + bodyOut}; !slices.Equal(got, wantPos) {
		t.Errorf("comments %q, want %q", got, wantPos)
	}
	if reqs, wantReqs := methodsAndPaths(f), []string{postRev, listRev55}; !slices.Equal(reqs, wantReqs) {
		t.Errorf("requests %q, want %q", reqs, wantReqs)
	}
	checkQuiet(t, logs.String(), res)
}

// methodsAndPaths lists the fake's requests as "METHOD path?query".
func methodsAndPaths(f *fake) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.Method+" "+r.URL.RequestURI())
	}
	return out
}

// TestPostInlineBatchIDLookupFails: a review that was created but whose
// comments cannot be listed still posts every item, with the review's URL
// and no id.
func TestPostInlineBatchIDLookupFails(t *testing.T) {
	f := newFake(t, "/api/v3")
	f.handle(http.MethodPost, pullPath+"/reviews", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": 55, "html_url": "https://github.example.com/r/55"})
	})
	f.handle(http.MethodGet, pullPath+"/reviews/55/comments", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, 500) })
	p, _ := inlineProvider(t, f)
	res, err := p.PostInlineComments(t.Context(), testRef(), &provider.PullRequest{HeadSHA: headSHA}, inlineItems()[:2])
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r != (provider.InlineResult{Posted: true, URL: "https://github.example.com/r/55", Reason: provider.InlineReasonPosted}) {
			t.Errorf("item %d: %+v", i, r)
		}
	}
}

// TestPostInlineBatchRefused: a 422 for the review posts each item alone
// (commit_id, path, line, side, and start_line and start_side for the
// range). The item GitHub refuses with 422 is unanchorable; one that fails
// with a server error is failed; the others are posted with their ids.
func TestPostInlineBatchRefused(t *testing.T) {
	f := newFake(t, "/api/v3")
	f.handle(http.MethodPost, pullPath+"/reviews", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, 422) })
	var rec recorded
	f.handle(http.MethodPost, pullPath+"/comments", func(w http.ResponseWriter, r *http.Request) {
		m := rec.add(t, r)
		switch m["body"] {
		case bodyOut:
			fakeError(w, 422)
		case bodyOne:
			fakeError(w, 502)
		default:
			w.WriteHeader(http.StatusCreated)
			writeJSON(w, map[string]any{"id": 777, "html_url": "https://github.example.com/c/777"})
		}
	})
	items := inlineItems()
	items = append(items, provider.InlineComment{Path: "a.go", Line: 5, LineType: provider.LineAdded, Body: "Fourth."})
	p, logs := inlineProvider(t, f)
	res, err := p.PostInlineComments(t.Context(), testRef(), &provider.PullRequest{HeadSHA: headSHA}, items)
	if err != nil {
		t.Fatal(err)
	}
	wantReasons := []provider.InlineReason{provider.InlineReasonFailed, provider.InlineReasonPosted,
		provider.InlineReasonUnanchorable, provider.InlineReasonPosted}
	for i, r := range res {
		if r.Reason != wantReasons[i] {
			t.Errorf("item %d: reason %q, want %q (%+v)", i, r.Reason, wantReasons[i], r)
		}
		if r.Posted != (r.Reason == provider.InlineReasonPosted) || r.Posted == (r.Error != "") {
			t.Errorf("item %d: inconsistent result %+v", i, r)
		}
	}
	if res[1].ID != "777" || res[1].URL != "https://github.example.com/c/777" {
		t.Errorf("posted item: %+v", res[1])
	}
	if !strings.HasPrefix(res[2].Error, provider.ErrProtocol.Error()) || !strings.HasPrefix(res[0].Error, provider.ErrUpstream.Error()) {
		t.Errorf("errors %q, %q", res[2].Error, res[0].Error)
	}
	var got []string
	for _, b := range rec.all() {
		if b["commit_id"] != headSHA {
			t.Errorf("commit_id = %v", b["commit_id"])
		}
		got = append(got, position(t, b))
	}
	if want := []string{"a.go:4", "a.go:12:10", "b.go:99", "a.go:5"}; !slices.Equal(got, want) {
		t.Errorf("positions %q, want %q", got, want)
	}
	if reqs, want := methodsAndPaths(f), []string{postRev, postOne, postOne, postOne, postOne}; !slices.Equal(reqs, want) {
		t.Errorf("requests %q, want %q", reqs, want)
	}
	checkQuiet(t, logs.String(), res)
}

// TestPostInlineBatchStops: an auth failure, a rate limit (with a reset
// too far away to wait for) or any status other than 422 for the review
// reports every item failed with that error, without another request: only
// a 422 says that nothing was created.
func TestPostInlineBatchStops(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		header map[string]string
		class  *provider.Error
	}{
		"auth":         {401, nil, provider.ErrAuth},
		"forbidden":    {403, nil, provider.ErrAuth},
		"rate limit":   {403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1700003600"}, provider.ErrRateLimited},
		"secondary":    {429, map[string]string{"Retry-After": "3600"}, provider.ErrRateLimited},
		"server error": {500, nil, provider.ErrUpstream},
		"not found":    {404, nil, provider.ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, "/api/v3")
			f.handle(http.MethodPost, pullPath+"/reviews", func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				fakeError(w, tc.status)
			})
			p, logs := inlineProvider(t, f)
			res, err := p.PostInlineComments(t.Context(), testRef(), &provider.PullRequest{HeadSHA: headSHA}, inlineItems())
			if err != nil {
				t.Fatal(err)
			}
			for i, r := range res {
				if r.Posted || r.Reason != provider.InlineReasonFailed || !strings.HasPrefix(r.Error, tc.class.Error()) {
					t.Errorf("item %d: %+v, want failed with %q", i, r, tc.class.Error())
				}
			}
			if reqs := methodsAndPaths(f); !slices.Equal(reqs, []string{postRev}) {
				t.Errorf("requests %q, want the review only", reqs)
			}
			checkQuiet(t, logs.String(), res)
		})
	}
}

// TestPostInlineFallbackStops: an auth or rate-limit failure while the
// items are posted one by one reports the remaining items with the same
// error, without sending them.
func TestPostInlineFallbackStops(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		header map[string]string
		class  *provider.Error
	}{
		"auth":       {401, nil, provider.ErrAuth},
		"rate limit": {403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1700003600"}, provider.ErrRateLimited},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, "/api/v3")
			f.handle(http.MethodPost, pullPath+"/reviews", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, 422) })
			var n atomic.Int32
			f.handle(http.MethodPost, pullPath+"/comments", func(w http.ResponseWriter, _ *http.Request) {
				if n.Add(1) == 1 {
					w.WriteHeader(http.StatusCreated)
					writeJSON(w, map[string]any{"id": 1, "html_url": "https://github.example.com/c/1"})
					return
				}
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				fakeError(w, tc.status)
			})
			p, _ := inlineProvider(t, f)
			res, err := p.PostInlineComments(t.Context(), testRef(), &provider.PullRequest{HeadSHA: headSHA}, inlineItems())
			if err != nil {
				t.Fatal(err)
			}
			if !res[0].Posted {
				t.Errorf("item 0: %+v", res[0])
			}
			for i, r := range res[1:] {
				if r.Posted || r.Reason != provider.InlineReasonFailed || !strings.HasPrefix(r.Error, tc.class.Error()) {
					t.Errorf("item %d: %+v, want failed with %q", i+1, r, tc.class.Error())
				}
			}
			if res[1].Error != res[2].Error {
				t.Errorf("the unsent item's error %q differs from %q", res[2].Error, res[1].Error)
			}
			if reqs := methodsAndPaths(f); !slices.Equal(reqs, []string{postRev, postOne, postOne}) {
				t.Errorf("requests %q, want the review and two comments", reqs)
			}
		})
	}
}

// TestPostInlineOutcomeUnknown: a created review whose answer cannot be
// read is not posted again item by item (it may exist).
func TestPostInlineOutcomeUnknown(t *testing.T) {
	f := newFake(t, "/api/v3")
	f.handle(http.MethodPost, pullPath+"/reviews", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "{not json "+testSentinel)
	})
	p, _ := inlineProvider(t, f)
	res, err := p.PostInlineComments(t.Context(), testRef(), &provider.PullRequest{HeadSHA: headSHA}, inlineItems())
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res {
		if r.Posted || r.Reason != provider.InlineReasonFailed || r.Error == "" {
			t.Errorf("item %d: %+v", i, r)
		}
	}
	if reqs := methodsAndPaths(f); !slices.Equal(reqs, []string{postRev}) {
		t.Errorf("requests %q", reqs)
	}
	checkQuiet(t, "", res)
}

// TestPostInlineRefusedBeforeRequest: invalid items, an unknown head commit
// and an invalid reference fail before any request; no items need none.
func TestPostInlineRefusedBeforeRequest(t *testing.T) {
	f := newFake(t, "/api/v3")
	p, _ := inlineProvider(t, f)
	ctx, pr := t.Context(), &provider.PullRequest{HeadSHA: headSHA}
	backwards := []provider.InlineComment{{Path: "a.go", Line: 5, EndLine: 4, LineType: provider.LineAdded, Body: "x"}}
	for name, call := range map[string]func() error{
		"range backwards": func() error { _, err := p.PostInlineComments(ctx, testRef(), pr, backwards); return err },
		"no head": func() error {
			_, err := p.PostInlineComments(ctx, testRef(), &provider.PullRequest{}, inlineItems())
			return err
		},
		"nil pr": func() error { _, err := p.PostInlineComments(ctx, testRef(), nil, inlineItems()); return err },
		"bad ref": func() error {
			ref := testRef()
			ref.Namespace = ".."
			_, err := p.PostInlineComments(ctx, ref, pr, inlineItems())
			return err
		},
	} {
		if err := call(); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%s: err = %v, want protocol", name, err)
		}
	}
	if res, err := p.PostInlineComments(ctx, testRef(), pr, nil); err != nil || res == nil || len(res) != 0 {
		t.Errorf("no items: %v, %v", res, err)
	}
	if reqs := f.requests(); len(reqs) != 0 {
		t.Errorf("requests %q, want none", reqs)
	}
}

// TestInlineCapabilities: GitHub posts inline comments, and its native
// suggestion block replaces the comment's range.
func TestInlineCapabilities(t *testing.T) {
	c := (&Provider{}).Capabilities()
	if !c.InlineComments || !c.SuggestionBlocks || c.NativeSuggestionStyle() != provider.SuggestionStyleRange {
		t.Errorf("capabilities = %+v", c)
	}
}
