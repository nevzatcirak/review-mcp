package review_test

// The inline canary of spec P7 §3.3: the review pipeline with the real
// Gitea and Bitbucket Server providers, against httptest servers that check
// inline anchors the way the real servers do.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
)

// The server's diff of src/app.go: lines 8 to 14 of the head, line 11
// added, three lines of context on each side (as both servers show it).
const canaryPatch = "@@ -8,6 +8,7 @@\n line 8\n line 9\n line 10\n+line 11\n line 12\n line 13\n line 14\n"

// serverLines are the new-side lines of canaryPatch and their line types.
var serverLines = map[int]string{8: "CONTEXT", 9: "CONTEXT", 10: "CONTEXT", 11: "ADDED", 12: "CONTEXT", 13: "CONTEXT", 14: "CONTEXT"}

func canaryFile() provider.FilePatch {
	var head, base strings.Builder
	for n := 1; n <= 30; n++ {
		fmt.Fprintf(&head, "line %d\n", n)
		if n != 11 {
			fmt.Fprintf(&base, "line %d\n", n)
		}
	}
	h, b := head.String(), base.String()
	return provider.FilePatch{Path: "src/app.go", Type: provider.ChangeModified, Additions: 1, Patch: canaryPatch,
		BaseContent: &b, HeadContent: &h, BaseStatus: provider.ContentFull, HeadStatus: provider.ContentFull}
}

// canaryAnswer has three findings: on the added line 11, on the context
// line 9, and on line 6, which only the prompt's extended context shows.
const canaryAnswer = "```yaml\nreview:\n  key_issues_to_review:\n" +
	"    - relevant_file: src/app.go\n      issue_header: Added line\n      issue_content: The new line is wrong.\n      start_line: 11\n      end_line: 11\n" +
	"    - relevant_file: src/app.go\n      issue_header: Context line\n      issue_content: The unchanged line no longer fits.\n      start_line: 9\n      end_line: 10\n" +
	"    - relevant_file: src/app.go\n      issue_header: Outside the hunk\n      issue_content: This line is not in the diff.\n      start_line: 6\n      end_line: 6\n" +
	"```\n"

const unanchorableNote = "1 finding could not be placed on a changed line and is listed in the overview only."

type answerLLM struct{}

func (answerLLM) Complete(context.Context, string, string) (*llm.Response, error) {
	return &llm.Response{Content: canaryAnswer}, nil
}

// canaryProvider is the real provider for every write and link, with the PR
// and its diff served from memory. It records the inline items it is given.
type canaryProvider struct {
	provider.Provider
	mu    sync.Mutex
	items []provider.InlineComment
}

func (c *canaryProvider) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	return &provider.PullRequest{Title: "Change line 11", HeadSHA: "headsha", BaseSHA: "basesha", SourceBranch: "feature"}, nil
}

func (c *canaryProvider) GetDiff(context.Context, provider.PRRef, *provider.PullRequest, provider.DiffOptions) (*provider.Diff, error) {
	return &provider.Diff{Files: []provider.FilePatch{canaryFile()}}, nil
}

func (c *canaryProvider) PostInlineComments(ctx context.Context, ref provider.PRRef, pr *provider.PullRequest, items []provider.InlineComment) ([]provider.InlineResult, error) {
	c.mu.Lock()
	c.items = append(c.items, items...)
	c.mu.Unlock()
	return c.Provider.PostInlineComments(ctx, ref, pr, items)
}

type canaryResolver struct {
	ref provider.PRRef
	p   provider.Provider
}

func (r canaryResolver) Resolve(string) (provider.PRRef, provider.Provider, error) {
	return r.ref, r.p, nil
}

// wire records the write requests a fake server received, in order.
type wire struct {
	mu   sync.Mutex
	reqs []wireReq
}

type wireReq struct {
	method, path string
	body         map[string]any
}

func (w *wire) add(r *http.Request) map[string]any {
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	if r.Method != http.MethodGet {
		w.mu.Lock()
		w.reqs = append(w.reqs, wireReq{r.Method, r.URL.Path, body})
		w.mu.Unlock()
	}
	return body
}

func (w *wire) list() []wireReq {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.reqs)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func canaryConfig() *config.Config {
	cfg := config.Defaults()
	cfg.LLM.BaseURL = "https://llm.example.com/v1"
	cfg.LLM.Model = "test-model"
	cfg.LLM.ContextWindow = 32000
	return cfg
}

// fakeBitbucket accepts an inline comment only on a line of its diff with
// the line's own type, and answers 400 otherwise, as Bitbucket Server does.
func fakeBitbucket(t *testing.T) (*httptest.Server, *wire) {
	t.Helper()
	wr := &wire{}
	const pr = "/rest/api/1.0/projects/PRJ/repos/demo/pull-requests/7"
	next := 100
	var mu sync.Mutex
	// overviews are the PR-level comments posted (id -> text), served back
	// by the activities listing and edited through GET and PUT.
	overviews := map[int]string{}
	version := map[int]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := wr.add(r)
		mu.Lock()
		defer mu.Unlock()
		commentID, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, pr+"/comments/"))
		switch {
		case r.Method == "GET" && r.URL.Path == pr+"/activities":
			acts := []any{}
			for id := 101; id <= next; id++ {
				if text, ok := overviews[id]; ok {
					acts = append(acts, map[string]any{"action": "COMMENTED", "commentAction": "ADDED",
						"comment": map[string]any{"id": id, "text": text, "createdDate": id,
							"author": map[string]any{"id": 42, "name": "review-bot", "displayName": "Review Bot"}}})
				}
			}
			writeJSON(w, map[string]any{"values": acts, "isLastPage": true})
		case r.Method == "GET" && r.URL.Path == "/rest/api/1.0/application-properties":
			w.Header().Set("X-AUSERNAME", "review-bot")
			w.Header().Set("X-AUSERID", "42")
			writeJSON(w, map[string]any{"version": "8.9.0"})
		case r.Method == "POST" && r.URL.Path == pr+"/comments":
			if a, ok := body["anchor"].(map[string]any); ok {
				line, _ := a["line"].(float64)
				want, inDiff := serverLines[int(line)]
				if !inDiff || a["lineType"] != want || a["fileType"] != "TO" || a["diffType"] != "EFFECTIVE" || a["path"] != "src/app.go" {
					w.WriteHeader(http.StatusBadRequest)
					writeJSON(w, map[string]any{"errors": []any{map[string]any{"message": "The anchor does not match the diff."}}})
					return
				}
			}
			next++
			if body["anchor"] == nil {
				overviews[next], _ = body["text"].(string)
			}
			writeJSON(w, map[string]any{"id": next, "version": 0})
		case r.Method == "GET" && overviews[commentID] != "":
			writeJSON(w, map[string]any{"id": commentID, "version": version[commentID],
				"author": map[string]any{"id": 42, "name": "review-bot"}})
		case r.Method == "PUT" && overviews[commentID] != "":
			overviews[commentID], _ = body["text"].(string)
			version[commentID]++
			writeJSON(w, map[string]any{"id": commentID, "version": version[commentID]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, wr
}

// fakeGitea answers a review whose comment is not on a line of its diff
// with 500, as Gitea does.
func fakeGitea(t *testing.T) (*httptest.Server, *wire) {
	t.Helper()
	wr := &wire{}
	const api = "/api/v1/repos/octo/demo"
	const web = "https://your-gitea.example/octo/demo/pulls/7"
	var mu sync.Mutex
	var reviewComments []any
	reviewed := false
	// overview is the PR-level comment 55 once posted ("" before), served
	// back by the issue-comment listing and edited through PATCH.
	overview := ""
	bot := map[string]any{"id": 42, "login": "review-bot"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := wr.add(r)
		mu.Lock()
		defer mu.Unlock()
		page1 := r.URL.Query().Get("page") == "1"
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/user":
			writeJSON(w, map[string]any{"id": 42, "login": "review-bot"})
		case r.Method == "POST" && r.URL.Path == api+"/issues/7/comments":
			overview, _ = body["body"].(string)
			writeJSON(w, map[string]any{"id": 55, "html_url": web + "#issuecomment-55"})
		case r.Method == "GET" && r.URL.Path == api+"/issues/7/comments":
			out := []any{}
			if page1 && overview != "" {
				out = append(out, map[string]any{"id": 55, "user": bot, "body": overview, "type": "comment",
					"html_url": web + "#issuecomment-55", "pull_request_url": web})
			}
			writeJSON(w, out)
		case r.Method == "GET" && r.URL.Path == api+"/issues/comments/55":
			writeJSON(w, map[string]any{"id": 55, "user": bot,
				"html_url": web + "#issuecomment-55", "pull_request_url": web})
		case r.Method == "PATCH" && r.URL.Path == api+"/issues/comments/55":
			overview, _ = body["body"].(string)
			writeJSON(w, map[string]any{"id": 55})
		case r.Method == "GET" && r.URL.Path == api+"/pulls/7/reviews":
			out := []any{}
			if page1 && reviewed {
				out = append(out, map[string]any{"id": 101, "state": "COMMENT", "user": map[string]any{"id": 42, "login": "review-bot"}})
			}
			writeJSON(w, out)
		case r.Method == "POST" && r.URL.Path == api+"/pulls/7/reviews":
			comments, _ := body["comments"].([]any)
			for _, c := range comments {
				cm, _ := c.(map[string]any)
				pos, _ := cm["new_position"].(float64)
				if _, ok := serverLines[int(pos)]; !ok || cm["path"] != "src/app.go" {
					http.Error(w, "position outside the diff", http.StatusInternalServerError)
					return
				}
			}
			for i, c := range comments {
				cm, _ := c.(map[string]any)
				id := 1000 + i
				reviewComments = append(reviewComments, map[string]any{"id": id, "path": cm["path"], "body": cm["body"],
					"position": cm["new_position"], "html_url": web + "/files#issuecomment-" + strconv.Itoa(id)})
			}
			reviewed = true
			writeJSON(w, map[string]any{"id": 101, "state": "COMMENT", "html_url": web + "#pullrequestreview-101"})
		case r.Method == "GET" && r.URL.Path == api+"/pulls/7/reviews/101/comments":
			out := []any{}
			if page1 {
				out = reviewComments
			}
			writeJSON(w, out)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, wr
}

type canarySetup struct {
	ref provider.PRRef
	p   provider.Provider
	wr  *wire
}

func canarySetups() map[string]func(t *testing.T) canarySetup {
	type setup = canarySetup
	return map[string]func(t *testing.T) setup{
		"gitea": func(t *testing.T) setup {
			srv, wr := fakeGitea(t)
			cfg := canaryConfig()
			cfg.Gitea.BaseURL = srv.URL
			cfg.Secrets.GiteaToken = config.NewSecret("FAKE-TOKEN")
			p, err := gitea.NewFactory().New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			return setup{provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7,
				URL: srv.URL + "/octo/demo/pulls/7"}, p, wr}
		},
		"bitbucket_server": func(t *testing.T) setup {
			srv, wr := fakeBitbucket(t)
			cfg := canaryConfig()
			cfg.BitbucketServer.BaseURL = srv.URL
			cfg.Secrets.BitbucketServerToken = config.NewSecret("FAKE-TOKEN")
			p, err := bitbucketserver.NewFactory().New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			return setup{provider.PRRef{Kind: provider.KindBitbucketServer, Namespace: "PRJ", Repo: "demo", Number: 7,
				URL: srv.URL + "/projects/PRJ/repos/demo/pull-requests/7"}, p, wr}
		},
	}
}

func TestInlineCanaryBothProviders(t *testing.T) {
	for name, mk := range canarySetups() {
		t.Run(name, func(t *testing.T) {
			s := mk(t)
			cp := &canaryProvider{Provider: s.p}
			res, err := review.Run(context.Background(), review.Deps{
				Config: canaryConfig(), Resolver: canaryResolver{s.ref, cp}, LLM: answerLLM{},
				RenderProvider: render.Provider, RenderInline: render.Inline,
			}, review.Args{PRURL: s.ref.URL, Publish: true})
			if err != nil {
				t.Fatal(err)
			}
			if res.Publish == nil || !res.Publish.Published {
				t.Fatalf("publish = %+v", res.Publish)
			}
			if in := res.Publish.Inline; in == nil || *in != (review.InlineSummary{Posted: 2, Unanchorable: 1}) {
				t.Errorf("inline = %+v", res.Publish.Inline)
			}
			type got struct {
				line int
				typ  provider.LineType
			}
			var items []got
			for _, it := range cp.items {
				items = append(items, got{it.Line, it.LineType})
			}
			if !slices.Equal(items, []got{{11, provider.LineAdded}, {9, provider.LineContext}}) {
				t.Errorf("inline items = %+v", items)
			}
			kis := res.Review.KeyIssuesToReview
			if len(kis) != 3 || kis[0].InlineURL == "" || kis[1].InlineURL == "" || kis[2].InlineURL != "" {
				t.Fatalf("findings = %+v", kis)
			}
			if !slices.Contains(res.Notes, unanchorableNote) || slices.Contains(res.Notes, review.NoteOverviewLookupFailed) {
				t.Errorf("notes = %q", res.Notes)
			}
			checkWire(t, name, s.wr.list(), kis)
		})
	}
}

// checkWire checks the order and content of the write requests: the
// overview first, with the note; then the inline comments with their
// computed line types; then the overview edit with the inline links.
func checkWire(t *testing.T, name string, reqs []wireReq, kis []review.KeyIssue) {
	t.Helper()
	text := func(r wireReq) string {
		if s, ok := r.body["text"].(string); ok {
			return s
		}
		s, _ := r.body["body"].(string)
		return s
	}
	var kinds []string
	var inline []map[string]any
	var overview, edited string
	for _, r := range reqs {
		switch {
		case r.method == "POST" && strings.HasSuffix(r.path, "/reviews"):
			kinds = append(kinds, "inline")
			for _, c := range r.body["comments"].([]any) {
				inline = append(inline, c.(map[string]any))
			}
		case r.method == "POST" && r.body["anchor"] != nil:
			kinds = append(kinds, "inline")
			inline = append(inline, r.body)
		case r.method == "POST":
			kinds = append(kinds, "overview")
			overview = text(r)
		case r.method == "PUT" || r.method == "PATCH":
			kinds = append(kinds, "edit")
			edited = text(r)
		default:
			kinds = append(kinds, r.method)
		}
	}
	want := []string{"overview", "inline", "edit"}
	if name == "bitbucket_server" {
		want = []string{"overview", "inline", "inline", "edit"}
	}
	if !slices.Equal(kinds, want) {
		t.Errorf("write requests %v, want %v", kinds, want)
	}
	if len(inline) != 2 {
		t.Fatalf("inline comments on the wire = %d", len(inline))
	}
	for i, c := range inline {
		body := text(wireReq{body: c})
		fp := review.Fingerprint("src/app.go", kis[i].IssueHeader, kis[i].IssueContent)
		if got, ok := review.ParseFingerprintMarker(body); !ok || got != fp {
			t.Errorf("inline %d: marker %q, want %q in %q", i, got, fp, body)
		}
		if !strings.HasPrefix(body, "**"+kis[i].IssueHeader+"**\n\n") {
			t.Errorf("inline %d: body %q", i, body)
		}
	}
	if name == "bitbucket_server" {
		for i, wantType := range []string{"ADDED", "CONTEXT"} {
			a := inline[i]["anchor"].(map[string]any)
			if a["lineType"] != wantType || a["line"] != float64([]int{11, 9}[i]) {
				t.Errorf("anchor %d = %v", i, a)
			}
		}
		if !strings.Contains(text(wireReq{body: inline[1]}), "Lines 9–10") {
			t.Errorf("the context finding does not name its range")
		}
	} else {
		for i, line := range []float64{11, 9} {
			if inline[i]["new_position"] != line || inline[i]["old_position"] != float64(0) {
				t.Errorf("review comment %d = %v", i, inline[i])
			}
		}
	}
	if !strings.Contains(overview, unanchorableNote) || strings.Contains(overview, kis[0].InlineURL) {
		t.Errorf("overview:\n%s", overview)
	}
	if !strings.Contains(edited, unanchorableNote) || !strings.Contains(edited, kis[0].InlineURL) ||
		!strings.Contains(edited, kis[1].InlineURL) {
		t.Errorf("edited overview:\n%s", edited)
	}
}

// TestPersistentOverviewRealProviders runs two publishes against the same
// fake server with the real providers: the second run finds the first
// run's overview through ListThreads and CurrentUser, and the provider's
// own EditComment ownership check accepts it (the lookup and the edit agree
// on who "we" are). The second run's writes are its inline comments and
// one edit; no second overview is posted. Inline deduplication is WP-PR-7e,
// so the second run posts its inline comments again.
func TestPersistentOverviewRealProviders(t *testing.T) {
	for name, mk := range canarySetups() {
		t.Run(name, func(t *testing.T) {
			s := mk(t)
			cp := &canaryProvider{Provider: s.p}
			deps := review.Deps{
				Config: canaryConfig(), Resolver: canaryResolver{s.ref, cp}, LLM: answerLLM{},
				RenderProvider: render.Provider, RenderInline: render.Inline,
			}
			if _, err := review.Run(context.Background(), deps, review.Args{PRURL: s.ref.URL, Publish: true}); err != nil {
				t.Fatal(err)
			}
			first := len(s.wr.list())
			res, err := review.Run(context.Background(), deps, review.Args{PRURL: s.ref.URL, Publish: true})
			if err != nil {
				t.Fatal(err)
			}
			if p := res.Publish; p == nil || !p.Published || !p.Updated || p.URL == "" {
				t.Fatalf("second publish = %+v", res.Publish)
			}
			var kinds []string
			var edited string
			for _, r := range s.wr.list()[first:] {
				switch {
				case r.method == "PUT" || r.method == "PATCH":
					kinds = append(kinds, "edit")
					edited, _ = r.body["text"].(string)
					if edited == "" {
						edited, _ = r.body["body"].(string)
					}
				case strings.HasSuffix(r.path, "/reviews") || r.body["anchor"] != nil:
					kinds = append(kinds, "inline")
				default:
					kinds = append(kinds, "overview")
				}
			}
			want := []string{"inline", "edit"}
			if name == "bitbucket_server" {
				want = []string{"inline", "inline", "edit"}
			}
			if !slices.Equal(kinds, want) {
				t.Errorf("second run writes %v, want %v", kinds, want)
			}
			if !review.HasOverviewMarker(edited) || !strings.Contains(edited, res.Review.KeyIssuesToReview[0].InlineURL) ||
				slices.Contains(res.Notes, review.NoteOverviewReplaced) || slices.Contains(res.Notes, review.NoteOverviewLookupFailed) {
				t.Errorf("edited overview:\n%s\nnotes %q", edited, res.Notes)
			}
		})
	}
}
