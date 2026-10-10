package describe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/github"
)

// ghPR is a GitHub pull request served by ghServer: the fields pr_describe
// can change and the draft flag it must not.
type ghPR struct {
	title, body string
	draft       bool
}

// ghServer is a fake GitHub API for one pull request (octo/demo#7). Every
// PATCH body is recorded raw, so a test sees exactly what was sent.
type ghServer struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	pr      ghPR
	gets    int
	patches []string
	// beforeGet runs at the start of the n-th GET of the pull request.
	beforeGet func(n int)
}

const ghToken = "ghp-FAKE-TOKEN-WP2M-do-not-leak" //nolint:gosec // synthetic test value

func newGHServer(t *testing.T, pr ghPR) *ghServer {
	g := &ghServer{t: t, pr: pr}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *ghServer) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+ghToken {
		g.t.Errorf("request %s %s: unexpected Authorization header shape", r.Method, r.URL.EscapedPath())
	}
	if r.URL.EscapedPath() != "/api/v3/repos/octo/demo/pulls/7" {
		http.NotFound(w, r) // the compare lookup: the base falls back
		return
	}
	switch r.Method {
	case http.MethodGet:
		g.gets++
		if g.beforeGet != nil {
			g.beforeGet(g.gets)
		}
	case http.MethodPatch:
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, "bad", http.StatusUnprocessableEntity)
			return
		}
		b, _ := json.Marshal(raw)
		g.patches = append(g.patches, string(b))
		if v, ok := raw["title"]; ok {
			_ = json.Unmarshal(v, &g.pr.title)
		}
		if v, ok := raw["body"]; ok {
			_ = json.Unmarshal(v, &g.pr.body)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"number": 7, "title": g.pr.title, "body": g.pr.body, "state": "open", "draft": g.pr.draft,
		"mergeable": true, "mergeable_state": "clean", "html_url": "https://github.example.com/octo/demo/pull/7",
		"user": map[string]any{"login": "alice", "id": 1},
		"head": map[string]any{"ref": "feature", "sha": "abc"},
		"base": map[string]any{"ref": "main", "sha": "def"},
	})
}

// ghProvider is the real GitHub provider for the pull request calls and the
// harness's fake for the diff and the commits, which the description flow
// reads from elsewhere.
type ghProvider struct {
	provider.Provider
	fake *fakeProvider
}

func (p ghProvider) GetDiff(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, o provider.DiffOptions) (*provider.Diff, error) {
	return p.fake.GetDiff(ctx, ref, pr, o)
}

func (p ghProvider) GetCommitMessages(ctx context.Context, ref provider.PRRef) ([]string, error) {
	return p.fake.GetCommitMessages(ctx, ref)
}

type ghResolver struct{ p provider.Provider }

func (r ghResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: provider.KindGitHub, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

// githubHarness runs pr_describe against the real GitHub provider and g.
func githubHarness(t *testing.T, g *ghServer) *harness {
	t.Helper()
	h := newHarness(map[int][]string{0: {oneCallAnswer}})
	cfg := config.Defaults()
	cfg.GitHub.BaseURL = g.srv.URL
	cfg.Secrets.GitHubToken = config.NewSecret(ghToken)
	p, err := github.NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.deps.Resolver = ghResolver{ghProvider{Provider: p, fake: h.prov}}
	h.deps.RenderProvider = testRender
	return h
}

// The description flow on GitHub is the Gitea path: no version, a re-read
// right before the write, a partial PATCH with only the fields that change.
func TestPublishDescriptionOnGitHub(t *testing.T) {
	author := "Raises the retry count. " + descMarker
	g := newGHServer(t, ghPR{title: "Retry more " + titleMarker, body: author, draft: true})
	h := githubHarness(t, g)

	res := h.run(t, descArgs(false))
	if !res.Publish.Published || res.Publish.Error != "" || res.Publish.TitleUpdated {
		t.Fatalf("publish = %+v", res.Publish)
	}
	if len(g.patches) != 1 || g.gets != 2 {
		t.Fatalf("patches %q gets %d, want one PATCH after the pipeline's read and one re-read", g.patches, g.gets)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(g.patches[0]), &sent); err != nil || len(sent) != 1 || sent["body"] == "" {
		t.Fatalf("the PATCH carries %q, want the body only", g.patches[0])
	}
	if !strings.HasPrefix(g.pr.body, author+"\n\n"+RegionStart+"\n") || !g.pr.draft {
		t.Errorf("body %q draft %v: the author's text must stay and the draft flag too", g.pr.body, g.pr.draft)
	}

	// A second run with the same answer writes nothing: nothing changes.
	res = h.run(t, descArgs(false))
	if !res.Publish.Published || len(g.patches) != 1 {
		t.Errorf("second run: %+v, %d patches, want the write skipped", res.Publish, len(g.patches))
	}
}

// A description edited between the pipeline's read and the re-read is
// recomputed on the fresh text: the human edit survives, one PATCH.
func TestPublishDescriptionOnGitHubConcurrentEdit(t *testing.T) {
	g := newGHServer(t, ghPR{title: "Retry more", body: "Original."})
	edited := "Edited by a human meanwhile."
	g.beforeGet = func(n int) {
		if n == 2 { // the re-read
			g.pr.body = edited
		}
	}
	h := githubHarness(t, g)
	res := h.run(t, descArgs(false))
	if !res.Publish.Published || res.Publish.Error != "" {
		t.Fatalf("publish = %+v", res.Publish)
	}
	if !strings.HasPrefix(g.pr.body, edited+"\n\n"+RegionStart) || len(g.patches) != 1 || g.gets != 3 {
		t.Errorf("body %q, patches %d, gets %d: want the human edit kept, one write, one re-read per computation", g.pr.body, len(g.patches), g.gets)
	}

	// Changed on the second re-read too: nothing is written.
	g2 := newGHServer(t, ghPR{title: "Retry more", body: "Original."})
	g2.beforeGet = func(n int) {
		if n >= 2 {
			g2.pr.body = "Edit number " + strconv.Itoa(n)
		}
	}
	res = githubHarness(t, g2).run(t, descArgs(false))
	if res.Publish.Published || res.Publish.Error != MsgChangedWhileUpdating || len(g2.patches) != 0 {
		t.Errorf("publish %+v, patches %d, want a refusal without a write", res.Publish, len(g2.patches))
	}
}

// update_title sends the title and never a draft flag. The draft state is a
// flag on GitHub, so the WIP-prefix rule has nothing to protect: a draft
// whose title happens to start with "WIP:" keeps that prefix (harmless: the
// text is the author's own), and every other title is simply replaced.
func TestPublishDescriptionOnGitHubTitle(t *testing.T) {
	const generated = "Raise the retry count and test it"
	for _, tc := range []struct {
		name, title string
		draft       bool
		want        string
	}{
		{"draft without a prefix", "Retry more", true, generated},
		{"draft with a WIP title", "WIP: retry more", true, "WIP: " + generated},
		{"not a draft with a WIP title", "WIP: retry more", false, generated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGHServer(t, ghPR{title: tc.title, body: "Original."})
			g.pr.draft = tc.draft
			res := githubHarness(t, g).run(t, descArgs(true))
			if !res.Publish.Published || !res.Publish.TitleUpdated || g.pr.title != tc.want || g.pr.draft != tc.draft {
				t.Errorf("publish %+v title %q draft %v, want title %q draft %v", res.Publish, g.pr.title, g.pr.draft, tc.want, tc.draft)
			}
			for _, p := range g.patches {
				if strings.Contains(p, "draft") {
					t.Errorf("a PATCH carries the draft flag: %s", p)
				}
			}
		})
	}
}
