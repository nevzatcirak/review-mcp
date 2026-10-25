package mcpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pr_review_decision over MCP (v2.7 spec V-14 §2.2, §2.5, §2.6).

const decisionHeadSHA = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"

// decisionGitea is a fake Gitea for pr_review_decision: an open pull
// request by alice at decisionHeadSHA, the token's user review-bot, and the
// reviews, to which an approval or change request adds one. It records the
// writes as "METHOD path".
type decisionGitea struct {
	srv *httptest.Server

	mu      sync.Mutex
	reviews []map[string]any
	writes  []string
}

func newDecisionGitea(t *testing.T) *decisionGitea {
	t.Helper()
	g := &decisionGitea{}
	const api = "/api/v1/repos/octo/demo"
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token "+fakeGitea {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		writeJ := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		firstPage := r.URL.Query().Get("page") == "" || r.URL.Query().Get("page") == "1"
		switch p := r.URL.Path; {
		case r.Method == http.MethodGet && p == api+"/pulls/7":
			writeJ(map[string]any{"title": "T", "body": "", "state": "open",
				"html_url": "https://your-gitea.example/octo/demo/pulls/7", "merge_base": "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2",
				"user": map[string]any{"id": 7, "login": "alice"},
				"head": map[string]any{"ref": "feature", "sha": decisionHeadSHA},
				"base": map[string]any{"ref": "main", "sha": "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"}})
		case r.Method == http.MethodGet && p == "/api/v1/user":
			writeJ(map[string]any{"id": 42, "login": "review-bot"})
		case r.Method == http.MethodGet && p == api+"/pulls/7/reviews":
			out := []any{}
			if firstPage {
				for _, rv := range g.reviews {
					out = append(out, rv)
				}
			}
			writeJ(out)
		case r.Method == http.MethodGet && strings.HasPrefix(p, api+"/pulls/7/reviews/"):
			writeJ([]any{})
		case r.Method == http.MethodPost && p == api+"/pulls/7/reviews":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			g.writes = append(g.writes, r.Method+" "+p)
			rv := map[string]any{"id": 500 + len(g.reviews), "state": in["event"], "body": in["body"], "commit_id": in["commit_id"],
				"user": map[string]any{"id": 42, "login": "review-bot"}, "submitted_at": "2026-01-02T03:04:05Z"}
			g.reviews = append(g.reviews, rv)
			writeJ(rv)
		case r.Method != http.MethodGet:
			g.writes = append(g.writes, r.Method+" "+p)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *decisionGitea) writesSoFar() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.writes)
}

func decisionEnv(g *decisionGitea, extra map[string]string) map[string]string {
	env := validEnv()
	env["REVIEW_MCP_GITEA_BASE_URL"] = g.srv.URL
	env["REVIEW_MCP_DECISION_ENABLED"] = "true"
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// TestDecisionToolRegistration: with the default configuration
// pr_review_decision is not listed and the instructions do not name it;
// with decision.enabled it is listed with the verbatim description, the
// annotations of §2.5 and the decision enum, and the instructions end with
// the decision sentence.
func TestDecisionToolRegistration(t *testing.T) {
	cs := connect(t, depsFor(validEnv(), nil))
	if names := toolNames(t, cs); slices.Contains(names, "pr_review_decision") {
		t.Fatalf("pr_review_decision is listed with the default configuration: %v", names)
	}
	if ins := cs.InitializeResult().Instructions; strings.Contains(ins, "pr_review_decision") {
		t.Errorf("the default instructions name pr_review_decision:\n%s", ins)
	}

	env := validEnv()
	env["REVIEW_MCP_DECISION_ENABLED"] = "true"
	cs = connect(t, depsFor(env, nil))
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var tool *mcp.Tool
	for _, tl := range list.Tools {
		if tl.Name == "pr_review_decision" {
			tool = tl
		}
	}
	if tool == nil {
		t.Fatalf("pr_review_decision is not listed with decision.enabled: %v", toolNames(t, cs))
	}
	if tool.Description != prReviewDecisionDescription || !strings.HasPrefix(tool.Description, "Records the token's user's own review decision") {
		t.Errorf("description %q", tool.Description)
	}
	a := tool.Annotations
	if a == nil || a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || !*a.DestructiveHint ||
		a.OpenWorldHint == nil || !*a.OpenWorldHint {
		t.Errorf("annotations %+v", a)
	}
	raw, _ := json.Marshal(tool.InputSchema)
	var in struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	slices.Sort(in.Required)
	if !slices.Equal(in.Required, []string{"decision", "head_sha", "pr_url"}) {
		t.Errorf("required %q", in.Required)
	}
	if got := in.Properties["decision"].Enum; !slices.Equal(got, []string{"approve", "request_changes", "clear"}) {
		t.Errorf("decision enum %q", got)
	}
	want := "`pr_review_decision` records the user's own approval or change request: call it only when the user " +
		"explicitly asks for that decision, with the `head_sha` that was reviewed."
	if ins := cs.InitializeResult().Instructions; !strings.HasSuffix(ins, "\n\n"+want) {
		t.Errorf("the instructions do not end with the decision sentence:\n%s", ins)
	}
}

// TestDecisionToolResultsValidate [WP-10d]: a decision, its unchanged
// repeat and the refusal of a moved head, through MCP on a fake Gitea.
// The results validate against the declared output schema; the unchanged
// call and the refusal write nothing; no secret reaches a text, a result or
// the log.
func TestDecisionToolResultsValidate(t *testing.T) {
	g := newDecisionGitea(t)
	var logs syncBuffer
	cs := connect(t, realDeps(decisionEnv(g, nil), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	schemas := outputSchemas(t, cs)
	prURL := g.srv.URL + "/octo/demo/pulls/7"
	args := map[string]any{"pr_url": prURL, "decision": "approve", "head_sha": strings.ToUpper(decisionHeadSHA), "body": "Looks good."}

	out := callValid(t, cs, schemas, "pr_review_decision", args)
	if out["state"] != "APPROVED" || out["unchanged"] != false || out["body_posted"] != "review" || out["head_sha"] != decisionHeadSHA {
		t.Fatalf("result %v", out)
	}
	if w := g.writesSoFar(); !slices.Equal(w, []string{"POST /api/v1/repos/octo/demo/pulls/7/reviews"}) {
		t.Fatalf("writes %q", w)
	}

	out = callValid(t, cs, schemas, "pr_review_decision", args)
	if out["unchanged"] != true || out["state"] != "" {
		t.Fatalf("repeated approval: %v", out)
	}
	res := callTool(t, cs, "pr_review_decision", map[string]any{"pr_url": prURL, "decision": "approve",
		"head_sha": strings.Repeat("c3", 20)})
	if !res.IsError || textOf(t, res) != "the pull request head is now a1a1a1a, not c3c3c3c; review the new commits before deciding" {
		t.Fatalf("moved head: error %v, %q", res.IsError, textOf(t, res))
	}
	if w := g.writesSoFar(); len(w) != 1 {
		t.Errorf("the unchanged call or the refusal wrote: %q", w)
	}
	if !strings.Contains(logs.String(), "msg=pr_review_decision decision=approve unchanged=true body_posted=\"\" provider=gitea") {
		t.Errorf("no debug line for the unchanged call:\n%s", logs.String())
	}
	for _, s := range allSecrets {
		if strings.Contains(logs.String(), s) {
			t.Errorf("the log carries a secret")
		}
	}
	if strings.Contains(logs.String(), "Looks good.") {
		t.Error("the log carries the body")
	}

	cs = connect(t, realDeps(decisionEnv(g, map[string]string{"REVIEW_MCP_DECISION_ALLOWED": "request_changes"}), nil))
	res = callTool(t, cs, "pr_review_decision", args)
	if !res.IsError || textOf(t, res) != "decision.allowed does not include approve" {
		t.Errorf("not allowed: error %v, %q", res.IsError, textOf(t, res))
	}
	if w := g.writesSoFar(); len(w) != 1 {
		t.Errorf("the refusal wrote: %q", w)
	}
}
