package gitlab

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
const testToken = "glpat-FAKE-TOKEN-WP6A-do-not-leak" //nolint:gosec // synthetic test value

// testSentinel is in the body of every error response of the fake; no
// error text may carry it.
const testSentinel = "GL-FAKE-SENTINEL-response-body-7a2e"

// Paths of the fake: the web base has the context path /gitlab, the
// project is group/sub/project.
const (
	fakeAPI     = "/gitlab/api/v4"
	fakeProject = "/projects/group%2Fsub%2Fproject"
	fakeMR      = fakeProject + "/merge_requests/7"
)

// fake is a GitLab API test server. Handlers are keyed by "METHOD
// escaped-path" (no query) below the API prefix; requests are recorded.
type fake struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	reqs     []*http.Request
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{t: t, handlers: map[string]http.HandlerFunc{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Clone(context.Background()))
		h := f.handlers[r.Method+" "+r.URL.EscapedPath()]
		f.mu.Unlock()
		if r.Header.Get("PRIVATE-TOKEN") != testToken {
			t.Errorf("request %s: unexpected token header shape", r.URL.EscapedPath())
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
	f.handlers[method+" "+fakeAPI+path] = h
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
// the context path /gitlab.
func (f *fake) config() *config.Config {
	cfg := config.Defaults()
	cfg.GitLab.BaseURL = f.srv.URL + "/gitlab"
	cfg.Secrets.GitLabToken = config.NewSecret(testToken)
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
	return provider.PRRef{Kind: provider.KindGitLab, Namespace: "group/sub", Repo: "project", Number: 7}
}

const (
	testHead = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	testBase = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
)

// mrJSON is a merge request payload; refs false serves empty diff_refs.
func mrJSON(refs bool) map[string]any {
	m := map[string]any{
		"iid": 7, "title": "Add feature", "description": "Description", "state": "opened", "draft": false,
		"detailed_merge_status": "mergeable", "changes_count": "1",
		"web_url":       "https://gitlab.example.com/group/sub/project/-/merge_requests/7",
		"source_branch": "feature", "target_branch": "main", "sha": testHead,
		"author":            map[string]any{"id": 101, "username": "alice", "name": "Alice Example"},
		"source_project_id": 4242, "target_project_id": 4242,
		"diff_refs": map[string]any{"base_sha": "", "head_sha": "", "start_sha": ""},
	}
	if refs {
		m["diff_refs"] = map[string]any{"base_sha": testBase, "head_sha": testHead, "start_sha": testBase}
	}
	return m
}
