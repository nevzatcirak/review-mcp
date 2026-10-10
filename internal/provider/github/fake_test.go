package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// testToken is synthetic.
const testToken = "ghp-FAKE-TOKEN-WP2J-do-not-leak" //nolint:gosec // synthetic test value

// testSentinel is in the body of every error response of the fake; no
// error text may carry it.
const testSentinel = "GH-FAKE-SENTINEL-response-body-3c1d"

// fake is a GitHub API test server. Handlers are keyed by "METHOD
// escaped-path" (no query); requests are recorded.
type fake struct {
	t      *testing.T
	srv    *httptest.Server
	prefix string // API prefix, such as "/api/v3"

	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	reqs     []*http.Request
}

func newFake(t *testing.T, prefix string) *fake {
	t.Helper()
	f := &fake{t: t, prefix: prefix, handlers: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Clone(context.Background()))
		h := f.handlers[r.Method+" "+r.URL.EscapedPath()]
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("request %s: unexpected Authorization header shape", r.URL.EscapedPath())
		}
		if h == nil {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			fakeError(w, http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) handle(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method+" "+f.prefix+path] = h
}

func (f *fake) json(path string, v any) {
	f.handle(http.MethodGet, path, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, v) })
}

// requests returns the escaped paths (with query) of the requests so far.
func (f *fake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.URL.RequestURI())
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fakeError(w http.ResponseWriter, status int) {
	http.Error(w, "fake error "+testSentinel+" "+testToken, status)
}

// config returns a configuration whose web base is the fake's origin plus
// webPath, so that the derived API base is {origin}{webPath}/api/v3.
func (f *fake) config(webPath string) *config.Config {
	cfg := config.Defaults()
	cfg.GitHub.BaseURL = f.srv.URL + webPath
	cfg.Secrets.GitHubToken = config.NewSecret(testToken)
	return cfg
}

// provider builds a provider from cfg with a fixed clock and a recording
// sleep that does not wait.
func (f *fake) provider(cfg *config.Config, clock time.Time) (*Provider, *[]time.Duration) {
	f.t.Helper()
	pp, err := NewFactory().New(cfg, nil)
	if err != nil {
		f.t.Fatalf("New: %v", err)
	}
	p := pp.(*Provider)
	var slept []time.Duration
	p.now = func() time.Time { return clock }
	p.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	return p, &slept
}

func testRef() provider.PRRef {
	return provider.PRRef{Kind: provider.KindGitHub, Namespace: "octo", Repo: "demo", Number: 7}
}

// prJSON is a pull request payload with the given base and head revisions.
func prJSON(baseSHA, headSHA string) map[string]any {
	return map[string]any{
		"number": 7, "title": "Add feature", "body": "Description", "state": "open", "draft": false,
		"merged": false, "merged_at": nil, "mergeable": true, "mergeable_state": "clean", "changed_files": 1,
		"html_url": "https://github.example.com/octo/demo/pull/7",
		"user":     map[string]any{"login": "alice", "id": 101},
		"head":     map[string]any{"ref": "feature", "sha": headSHA},
		"base":     map[string]any{"ref": "main", "sha": baseSHA},
	}
}
