package bitbucketserver_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
)

const (
	testToken  = "tok-LEAKCANARY-4b8e2d" //nolint:gosec // synthetic test value
	testMarker = "BODYMARKER-5a9c1f"
)

type recorded struct {
	Method string
	Path   string // escaped path, context prefix stripped
	Query  string
	Body   string
}

type rawEntry struct {
	status int
	body   string
}

// fakeBBS is an httptest Bitbucket Server. It checks the Authorization header
// on every request and fails the test on any unexpected path.
type fakeBBS struct {
	t      *testing.T
	srv    *httptest.Server
	prefix string // context path, e.g. "/bitbucket"

	mu       sync.Mutex
	reqs     []recorded
	handlers map[string]http.HandlerFunc // "METHOD /escaped/path"
	raw      map[string]rawEntry         // "sha:path" -> entry
	rawHook  func(r *http.Request)

	inflight, maxInflight atomic.Int32
	rawDelay              time.Duration
}

func newFake(t *testing.T, prefix string) *fakeBBS {
	t.Helper()
	f := &fakeBBS{t: t, prefix: prefix, handlers: map[string]http.HandlerFunc{}, raw: map[string]rawEntry{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBBS) baseURL() string { return f.srv.URL + f.prefix }

func (f *fakeBBS) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	esc := r.URL.EscapedPath()
	if want := "Bearer " + testToken; r.Header.Get("Authorization") != want {
		f.t.Errorf("request %s %s: unexpected Authorization header shape", r.Method, esc)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !strings.HasPrefix(esc, f.prefix+"/rest/api/") {
		f.t.Errorf("request outside %s/rest/api/: %s", f.prefix, esc)
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(esc, f.prefix)
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{r.Method, path, r.URL.RawQuery, string(body)})
	f.mu.Unlock()

	if strings.Contains(path, "/raw/") {
		f.serveRaw(w, r, path)
		return
	}
	f.mu.Lock()
	h, ok := f.handlers[r.Method+" "+path]
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("unexpected request %s %s", r.Method, path)
		http.NotFound(w, r)
		return
	}
	h(w, r)
}

func (f *fakeBBS) serveRaw(w http.ResponseWriter, r *http.Request, path string) {
	n := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxInflight.Load()
		if n <= m || f.maxInflight.CompareAndSwap(m, n) {
			break
		}
	}
	if f.rawHook != nil {
		f.rawHook(r)
	}
	if f.rawDelay > 0 {
		select {
		case <-time.After(f.rawDelay):
		case <-r.Context().Done():
			return
		}
	}
	idx := strings.Index(path, "/raw/")
	p, err := url.PathUnescape(path[idx+len("/raw/"):])
	if err != nil {
		f.t.Errorf("bad raw path %s", path)
	}
	key := r.URL.Query().Get("at") + ":" + p
	f.mu.Lock()
	e, ok := f.raw[key]
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("unexpected raw request %q", key)
		http.NotFound(w, r)
		return
	}
	if e.status != 0 && e.status != http.StatusOK {
		http.Error(w, e.body, e.status)
		return
	}
	_, _ = io.WriteString(w, e.body)
}

func (f *fakeBBS) handle(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method+" "+path] = h
}

func (f *fakeBBS) handleJSON(method, path string, v any) {
	f.handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
}

func (f *fakeBBS) handleStatus(method, path string, status int) {
	f.handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom "+testMarker+" "+testToken, status)
	})
}

// handlePaged serves a start/limit paged resource: the page whose first
// element index equals ?start= is returned, with isLastPage/nextPageStart.
// check, when non-nil, validates the request (query parameters).
func (f *fakeBBS) handlePaged(path string, check func(q url.Values), pages ...[]any) {
	starts := make([]int, len(pages))
	for i := 1; i < len(pages); i++ {
		starts[i] = starts[i-1] + len(pages[i-1])
	}
	f.handle("GET", path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("limit") != "100" {
			f.t.Errorf("%s: limit = %q, want 100", path, q.Get("limit"))
		}
		if check != nil {
			check(q)
		}
		start, _ := strconv.Atoi(q.Get("start"))
		for i := range pages {
			if starts[i] != start {
				continue
			}
			resp := map[string]any{"values": pages[i], "size": len(pages[i]), "start": start, "isLastPage": i == len(pages)-1}
			if i < len(pages)-1 {
				resp["nextPageStart"] = starts[i+1]
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		f.t.Errorf("%s: no page starts at %d", path, start)
		http.NotFound(w, r)
	})
}

func (f *fakeBBS) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func (f *fakeBBS) rawRequests() []recorded {
	var out []recorded
	for _, r := range f.requests() {
		if strings.Contains(r.Path, "/raw/") {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeBBS) count(method, path string) int {
	n := 0
	for _, r := range f.requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func (f *fakeBBS) setRaw(sha, path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raw[sha+":"+path] = rawEntry{body: body}
}

func (f *fakeBBS) setRawStatus(sha, path string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raw[sha+":"+path] = rawEntry{status: status, body: body}
}

// ---- fixtures ----

const (
	v1Repo     = "/rest/api/1.0/projects/PROJ/repos/demo"
	latestRepo = "/rest/api/latest/projects/PROJ/repos/demo"
	prAPI      = v1Repo + "/pull-requests/7"
	mergeBase  = latestRepo + "/pull-requests/7/merge-base"
	propsAPI   = "/rest/api/1.0/application-properties"
	webPR      = "https://bitbucket.example.com/bitbucket/projects/PROJ/repos/demo/pull-requests/7"
)

func ref() provider.PRRef {
	return provider.PRRef{Kind: provider.KindBitbucketServer, Namespace: "PROJ", Repo: "demo", Number: 7}
}

func prJSON() map[string]any {
	return map[string]any{
		"title":       "Add feature",
		"description": "Description " + testMarker,
		"state":       "OPEN",
		"author":      map[string]any{"user": map[string]any{"name": "jdoe", "displayName": "J. Doe"}},
		"fromRef":     map[string]any{"displayId": "feature", "latestCommit": "headsha"},
		"toRef":       map[string]any{"displayId": "main", "latestCommit": "targetsha"},
		"links":       map[string]any{"self": []any{map[string]any{"href": webPR}}},
	}
}

func change(typ, path, src string) map[string]any {
	c := map[string]any{"type": typ, "path": map[string]any{"toString": path}}
	if src != "" {
		c["srcPath"] = map[string]any{"toString": src}
	}
	return c
}

// sampleChanges is the changes listing in API order.
func sampleChanges() []any {
	return []any{
		change("MOVE", "src/renamed.go", "src/old_name.go"),
		change("MODIFY", "src/app.go", ""),
		change("DELETE", "old/gone.go", ""),
		change("ADD", "src/new.go", ""),
	}
}

// standard registers the sample PR (merge-base strategy), changes and
// contents.
func (f *fakeBBS) standard() {
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	f.handleJSON("GET", prAPI, prJSON())
	f.handleJSON("GET", mergeBase, map[string]any{"id": "mergesha"})
	f.handlePaged(prAPI+"/changes", nil, sampleChanges())
	f.setRaw("headsha", "src/app.go", "package main\nvar a = 2 // "+testMarker+"\n")
	f.setRaw("mergesha", "src/app.go", "package main\nvar a = 1\n")
	f.setRaw("headsha", "src/new.go", "package main\nvar n = 1\n")
	f.setRaw("mergesha", "old/gone.go", "package old\nvar g = 1\n")
	f.setRaw("headsha", "src/renamed.go", "package main\nvar r = 2\n")
	f.setRaw("mergesha", "src/old_name.go", "package main\nvar r = 1\n")
}

func (f *fakeBBS) config() *config.Config {
	cfg := config.Defaults()
	cfg.BitbucketServer.BaseURL = f.baseURL()
	cfg.Secrets.BitbucketServerToken = config.NewSecret(testToken)
	return cfg
}

func (f *fakeBBS) provider(t *testing.T, mutate func(*config.Config)) provider.Provider {
	t.Helper()
	cfg := f.config()
	if mutate != nil {
		mutate(cfg)
	}
	p, err := bitbucketserver.NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
