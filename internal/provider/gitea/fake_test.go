package gitea_test

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
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
)

const (
	testToken  = "tok-LEAKCANARY-9f3a1c" //nolint:gosec // synthetic test value
	testMarker = "BODYMARKER-7c1d2e"
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

// fakeGitea is an httptest Gitea. It checks the Authorization header on every
// request and fails the test on any unexpected path.
type fakeGitea struct {
	t      *testing.T
	srv    *httptest.Server
	prefix string // context path, e.g. "/gitea"

	mu       sync.Mutex
	reqs     []recorded
	handlers map[string]http.HandlerFunc // "METHOD /escaped/path"
	raw      map[string]rawEntry         // "sha:path" -> entry
	rawHook  func(r *http.Request)

	inflight, maxInflight atomic.Int32
	rawDelay              time.Duration
}

func newFake(t *testing.T, prefix string) *fakeGitea {
	t.Helper()
	f := &fakeGitea{t: t, prefix: prefix, handlers: map[string]http.HandlerFunc{}, raw: map[string]rawEntry{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitea) baseURL() string { return f.srv.URL + f.prefix }

func (f *fakeGitea) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	esc := r.URL.EscapedPath()
	if want := "token " + testToken; r.Header.Get("Authorization") != want {
		f.t.Errorf("request %s %s: unexpected Authorization header shape", r.Method, esc)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !strings.HasPrefix(esc, f.prefix+"/api/v1/") {
		f.t.Errorf("request outside %s/api/v1/: %s", f.prefix, esc)
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
	h, ok := f.handlers[r.Method+" "+path]
	if !ok {
		f.t.Errorf("unexpected request %s %s", r.Method, path)
		http.NotFound(w, r)
		return
	}
	h(w, r)
}

func (f *fakeGitea) serveRaw(w http.ResponseWriter, r *http.Request, path string) {
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
	key := r.URL.Query().Get("ref") + ":" + p
	e, ok := f.raw[key]
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

func (f *fakeGitea) handle(method, path string, h http.HandlerFunc) {
	f.handlers[method+" "+path] = h
}

func (f *fakeGitea) handleJSON(method, path string, v any) {
	f.handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	})
}

// handlePages serves a paged JSON array: page N returns pages[N-1], and any
// page beyond the end returns [].
func (f *fakeGitea) handlePages(path string, pages ...any) {
	f.handle("GET", path, func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if r.URL.Query().Get("limit") != "50" {
			f.t.Errorf("%s: limit = %q, want 50", path, r.URL.Query().Get("limit"))
		}
		w.Header().Set("Content-Type", "application/json")
		if n < 1 || n > len(pages) {
			_, _ = io.WriteString(w, "[]")
			return
		}
		_ = json.NewEncoder(w).Encode(pages[n-1])
	})
}

func (f *fakeGitea) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func (f *fakeGitea) rawRequests() []recorded {
	var out []recorded
	for _, r := range f.requests() {
		if strings.Contains(r.Path, "/raw/") {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeGitea) setRaw(sha, path, body string) { f.raw[sha+":"+path] = rawEntry{body: body} }

// ---- fixtures ----

const (
	repoAPI = "/api/v1/repos/octo/demo"
	prAPI   = repoAPI + "/pulls/7"
)

func ref() provider.PRRef {
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7}
}

func prJSON(mergeBase string) map[string]any {
	return map[string]any{
		"title":      "Add feature",
		"body":       "Description " + testMarker,
		"state":      "open",
		"html_url":   "https://your-gitea.example/octo/demo/pulls/7",
		"merge_base": mergeBase,
		"user":       map[string]any{"login": "alice"},
		"head":       map[string]any{"ref": "feature", "sha": "headsha"},
		"base":       map[string]any{"ref": "main", "sha": "basesha"},
	}
}

const sampleDiff = `diff --git a/src/app.go b/src/app.go
index 1111111..2222222 100644
--- a/src/app.go
+++ b/src/app.go
@@ -1,3 +1,3 @@
 package main
-var a = 1
+var a = 2
 func main() {}
diff --git a/src/new.go b/src/new.go
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/src/new.go
@@ -0,0 +1,2 @@
+package main
+var n = 1
diff --git a/old/gone.go b/old/gone.go
deleted file mode 100644
index 4444444..0000000
--- a/old/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package old
-var g = 1
diff --git a/src/old_name.go b/src/renamed.go
similarity index 80%
rename from src/old_name.go
rename to src/renamed.go
index 5555555..6666666 100644
--- a/src/old_name.go
+++ b/src/renamed.go
@@ -1,2 +1,2 @@
 package main
-var r = 1
+var r = 2
diff --git a/assets/logo.png b/assets/logo.png
index 7777777..8888888 100644
Binary files a/assets/logo.png and b/assets/logo.png differ
`

func fileMeta(name, prev, status string, add, del int) map[string]any {
	return map[string]any{"filename": name, "previous_filename": prev, "status": status,
		"additions": add, "deletions": del, "changes": add + del}
}

// sampleFiles is the /files listing in API order (deliberately different
// from the diff order).
func sampleFiles() []any {
	return []any{
		fileMeta("src/renamed.go", "src/old_name.go", "renamed", 1, 1),
		fileMeta("src/app.go", "", "changed", 1, 1),
		fileMeta("old/gone.go", "", "deleted", 0, 2),
		fileMeta("src/new.go", "", "added", 2, 0),
		fileMeta("assets/logo.png", "", "changed", 0, 0),
	}
}

// standard registers the sample PR, diff, files and contents.
func (f *fakeGitea) standard(mergeBase string) {
	f.handleJSON("GET", prAPI, prJSON(mergeBase))
	f.handle("GET", prAPI+".diff", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sampleDiff)
	})
	f.handlePages(prAPI+"/files", sampleFiles())
	base := mergeBase
	if base == "" {
		base = "basesha"
	}
	f.setRaw("headsha", "src/app.go", "head app "+testMarker)
	f.setRaw(base, "src/app.go", "base app")
	f.setRaw("headsha", "src/new.go", "head new")
	f.setRaw(base, "old/gone.go", "base gone")
	f.setRaw("headsha", "src/renamed.go", "head renamed")
	f.setRaw(base, "src/old_name.go", "base old name")
}

func (f *fakeGitea) config() *config.Config {
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = f.baseURL()
	cfg.Secrets.GiteaToken = config.NewSecret(testToken)
	return cfg
}

func (f *fakeGitea) provider(t *testing.T, mutate func(*config.Config)) provider.Provider {
	t.Helper()
	cfg := f.config()
	if mutate != nil {
		mutate(cfg)
	}
	p, err := gitea.NewFactory().New(cfg, nil)
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
