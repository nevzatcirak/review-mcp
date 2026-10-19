package repotools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Repository tools in related repositories (v2.5 spec V-13b §3.1).

// consumerSHA is the pinned commit of the related repository of the tests.
const consumerSHA = "fedcba9876543210fedcba9876543210fedcba98"

// twoTrees is the head commit and a related repository in memory, each by
// its commit: a read is answered from the tree of the checkout it names, at
// the revision it names, which must be that checkout's commit.
type twoTrees struct {
	mu      sync.Mutex
	files   map[string]string // "<commit>:<path>"
	reads   []string          // "<commit 4>:<op> <path>"
	queries []gitctx.TextQuery
	hits    map[string][]gitctx.Hit // by commit
}

func (f *twoTrees) record(c gitctx.Checkout, s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, c.HeadSHA[:4]+":"+s)
}

func (f *twoTrees) FileAt(_ context.Context, c gitctx.Checkout, rev, path string, _ int) ([]byte, error) {
	f.record(c, "file "+path)
	if rev != c.HeadSHA {
		return nil, fmt.Errorf("read at %s of a checkout at %s", rev, c.HeadSHA)
	}
	s, ok := f.files[rev+":"+path]
	if !ok {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	return []byte(s), nil
}

func (f *twoTrees) DirEntries(_ context.Context, c gitctx.Checkout, rev, dir string) ([]provider.DirEntry, error) {
	f.record(c, "dir "+dir)
	if rev != c.HeadSHA {
		return nil, fmt.Errorf("list at %s of a checkout at %s", rev, c.HeadSHA)
	}
	return []provider.DirEntry{{Name: "client", Dir: true}, {Name: "README.md"}}, nil
}

func (f *twoTrees) SearchText(_ context.Context, c gitctx.Checkout, q gitctx.TextQuery) (gitctx.TextResult, error) {
	f.record(c, "search "+q.Text)
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.mu.Unlock()
	return gitctx.TextResult{Hits: f.hits[c.HeadSHA]}, nil
}

// consumerRepo is the related repository R2 of the tests: R1 is a related
// pull request, which the tools never read.
func consumerRepo(paths ...string) Repo {
	return Repo{Label: "R2", Name: "grp/web-ui", Checkout: gitctx.Checkout{GitDir: "/cache/web-ui", HeadSHA: consumerSHA},
		Paths: paths}
}

func twoTreesBox(f *twoTrees, budget int, repos ...Repo) *Box {
	return New(f, gitctx.Checkout{GitDir: "/cache/demo", HeadSHA: headSHA}, nil,
		Settings{MaxToolTokens: budget, MaxFileBytes: 1 << 20}, nil).WithRepos(repos)
}

func newTwoTrees() *twoTrees {
	return &twoTrees{files: map[string]string{
		headSHA + ":src/client/profile.ts":     "HEAD-PROFILE\n",
		consumerSHA + ":src/client/profile.ts": "const who = p.userName\nCONSUMER-PROFILE\n",
		consumerSHA + ":docs/notes.md":         "CONSUMER-NOTES\n",
	}, hits: map[string][]gitctx.Hit{consumerSHA: {{Path: "src/client/list.ts", Line: 3, StartLine: 3, Snippet: "row.userName"}}}}
}

// TestToolsReadRelatedRepository: a call with repo "R2" reads the related
// repository's checkout at its pinned commit, never the head commit; the
// fixed result header names the label; the call is accounted with its repo
// (the primary's calls without one); the files shown count as seen for that
// label; read_file, list_dir and search all honour it.
func TestToolsReadRelatedRepository(t *testing.T) {
	f := newTwoTrees()
	b := twoTreesBox(f, 6000, consumerRepo())
	got, c := one(t, b, call("1", ReadFile, `{"path":"src/client/profile.ts","repo":"R2"}`))
	if c.Status != StatusOK || c.Repo != "R2" || !strings.HasPrefix(got, RepoHeader("R2")) ||
		!strings.Contains(got, "CONSUMER-PROFILE") || strings.Contains(got, "HEAD-PROFILE") {
		t.Fatalf("read of R2: %+v\n%s", c, got)
	}
	if !strings.Contains(RepoHeader("R2"), "related repository R2 at its pinned commit") {
		t.Fatalf("header %q", RepoHeader("R2"))
	}
	got, c = one(t, b, call("2", ReadFile, `{"path":"src/client/profile.ts"}`))
	if c.Status != StatusOK || c.Repo != "" || !strings.HasPrefix(got, Header) || !strings.Contains(got, "HEAD-PROFILE") {
		t.Fatalf("read of the head commit: %+v\n%s", c, got)
	}
	got, c = one(t, b, call("3", ListDir, `{"path":"src","repo":"R2"}`))
	if c.Status != StatusOK || c.Repo != "R2" || !strings.HasPrefix(got, RepoHeader("R2")) || !strings.Contains(got, "client/") {
		t.Fatalf("list of R2: %+v\n%s", c, got)
	}
	got, c = one(t, b, call("4", Search, `{"text":"userName","repo":"R2"}`))
	if c.Status != StatusOK || c.Repo != "R2" || !strings.HasPrefix(got, RepoHeader("R2")) || !strings.Contains(got, "[src/client/list.ts:3]") {
		t.Fatalf("search of R2: %+v\n%s", c, got)
	}
	want := []string{"fedc:file src/client/profile.ts", "0123:file src/client/profile.ts", "fedc:dir src", "fedc:search userName"}
	if !slices.Equal(f.reads, want) {
		t.Fatalf("reads %q\nwant  %q", f.reads, want)
	}
	if seen := b.Seen("R2"); !slices.Equal(seen, []string{"src/client/profile.ts", "src/client/list.ts"}) {
		t.Fatalf("seen in R2 %q", seen)
	}
	if seen := b.Seen(""); len(seen) != 0 {
		t.Fatalf("the head commit's reads count as seen: %q", seen)
	}
}

// TestToolsRepoLabel [canary 1]: a label that is not a readable related
// repository's is invalid_arguments and reads nothing: R1, a related pull
// request (not among the box's repositories, spec §0 item 7), an unknown
// label, a label of the wrong type, and any repo argument in a run without
// related repositories, which accounts as in 2.4 (no repo).
func TestToolsRepoLabel(t *testing.T) {
	f := newTwoTrees()
	b := twoTreesBox(f, 6000, consumerRepo())
	for _, tc := range []struct {
		tc   llm.ToolCall
		repo string
	}{
		{call("1", ReadFile, `{"path":"src/client/profile.ts","repo":"R1"}`), "R1"},
		{call("2", ListDir, `{"path":"src","repo":"R1"}`), "R1"},
		{call("3", Search, `{"text":"userName","repo":"R1"}`), "R1"},
		{call("4", ReadFile, `{"path":"src/client/profile.ts","repo":"R9"}`), "R9"},
		{call("5", ReadFile, `{"path":"src/client/profile.ts","repo":"r2"}`), "r2"},
		{call("6", ReadFile, `{"path":"src/client/profile.ts","repo":2}`), ""},
	} {
		got, c := one(t, b, tc.tc)
		if got != ResultInvalidArguments || c.Status != StatusInvalidArguments || c.Repo != tc.repo {
			t.Errorf("%s: %q %+v", tc.tc.Arguments, got, c)
		}
	}
	plain := twoTreesBox(f, 6000)
	got, c := one(t, plain, call("7", ReadFile, `{"path":"src/client/profile.ts","repo":"R2"}`))
	if got != ResultInvalidArguments || c.Status != StatusInvalidArguments || c.Repo != "" {
		t.Errorf("repo without related repositories: %q %+v", got, c)
	}
	if len(f.reads) != 0 {
		t.Fatalf("reads %q", f.reads)
	}
}

// TestToolsRelatedPathRules: a related repository's reads follow the path
// rules of the head commit (not_allowed, nothing read), and a repository
// with Paths (from the repository config) is confined to them: a path
// outside, and its root, are not_allowed; a search without a directory
// searches below the prefixes only; the head commit is never confined.
func TestToolsRelatedPathRules(t *testing.T) {
	f := newTwoTrees()
	b := twoTreesBox(f, 6000, consumerRepo("src/client/", "api"))
	for _, tc := range []llm.ToolCall{
		call("1", ReadFile, `{"path":"../demo/secret.go","repo":"R2"}`),
		call("2", ReadFile, `{"path":".git/config","repo":"R2"}`),
		call("3", ReadFile, `{"path":"docs/notes.md","repo":"R2"}`),
		call("4", ReadFile, `{"path":"src/clientele.ts","repo":"R2"}`),
		call("5", ListDir, `{"path":"","repo":"R2"}`),
		call("6", ListDir, `{"path":"src","repo":"R2"}`),
		call("7", Search, `{"text":"userName","path":"docs","repo":"R2"}`),
	} {
		got, c := one(t, b, tc)
		if got != ResultNotAllowed || c.Status != StatusNotAllowed || c.Repo != "R2" {
			t.Errorf("%s %s: %q %+v", tc.Name, tc.Arguments, got, c)
		}
	}
	if len(f.reads) != 0 {
		t.Fatalf("a refused path was read: %q", f.reads)
	}
	for _, tc := range []llm.ToolCall{
		call("8", ReadFile, `{"path":"src/client/profile.ts","repo":"R2"}`),
		call("9", ListDir, `{"path":"api/","repo":"R2"}`),
		call("10", Search, `{"text":"userName","repo":"R2"}`),
		call("11", Search, `{"text":"userName","path":"src/client","repo":"R2"}`),
		call("12", ReadFile, `{"path":"docs/notes.md"}`),
	} {
		want := StatusOK
		if tc.ID == "12" {
			want = StatusNotAvailable // not in the head commit's tree, but never confined
		}
		if _, c := one(t, b, tc); c.Status != want {
			t.Errorf("%s %s: %+v", tc.Name, tc.Arguments, c)
		}
	}
	if q := f.queries[0]; q.Dir != "" || !slices.Equal(q.Paths, []string{"src/client/", "api"}) {
		t.Errorf("search without a directory: %+v", q)
	}
	if q := f.queries[1]; q.Dir != "src/client" || q.Paths != nil {
		t.Errorf("search in a directory: %+v", q)
	}
}

// TestToolsRelatedFilter: a search of a related repository applies that
// repository's filter (the operator's), the head commit's search the run's.
func TestToolsRelatedFilter(t *testing.T) {
	run, err := filter.New(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	op, err := filter.New(config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	f := newTwoTrees()
	r := consumerRepo()
	r.Filter = op
	b := New(f, gitctx.Checkout{GitDir: "/cache/demo", HeadSHA: headSHA}, run, Settings{MaxToolTokens: 6000, MaxFileBytes: 1 << 20}, nil).
		WithRepos([]Repo{r})
	one(t, b, call("1", Search, `{"text":"userName","repo":"R2"}`))
	one(t, b, call("2", Search, `{"text":"userName"}`))
	if f.queries[0].Filter != op || f.queries[1].Filter != run {
		t.Fatalf("filters %p %p, want %p %p", f.queries[0].Filter, f.queries[1].Filter, op, run)
	}
}

// TestToolsBudgetShared: max_tool_tokens is one budget for the head commit
// and every related repository.
func TestToolsBudgetShared(t *testing.T) {
	f := newTwoTrees()
	b := twoTreesBox(f, 6000, consumerRepo())
	one(t, b, call("1", ReadFile, `{"path":"src/client/profile.ts","repo":"R2"}`))
	first := b.Tokens()
	one(t, b, call("2", ReadFile, `{"path":"src/client/profile.ts"}`))
	if first <= 0 || b.Tokens() <= first {
		t.Fatalf("tokens %d then %d", first, b.Tokens())
	}
	small := twoTreesBox(newTwoTrees(), first, consumerRepo())
	one(t, small, call("1", ReadFile, `{"path":"src/client/profile.ts","repo":"R2"}`))
	got, c := one(t, small, call("2", ReadFile, `{"path":"src/client/profile.ts"}`))
	if got != ResultBudgetExhausted || c.Status != StatusBudgetExhausted || !small.Exhausted() {
		t.Fatalf("the head commit's read after the related one used the budget: %q %+v", got, c)
	}
}

// TestToolsRelatedLogs: a call of a related repository logs its label,
// never its path or its search text.
func TestToolsRelatedLogs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b := New(newTwoTrees(), gitctx.Checkout{HeadSHA: headSHA}, nil, Settings{MaxToolTokens: 6000, MaxFileBytes: 1 << 20}, log).
		WithRepos([]Repo{consumerRepo()})
	one(t, b, call("1", ReadFile, `{"path":"src/client/profile.ts","repo":"R2"}`))
	one(t, b, call("2", Search, `{"text":"userName","repo":"R2"}`))
	out := buf.String()
	if !strings.Contains(out, "repo=R2") {
		t.Errorf("log lacks the label: %s", out)
	}
	for _, leak := range []string{"profile.ts", "userName", "web-ui", consumerSHA[:12]} {
		if strings.Contains(out, leak) {
			t.Errorf("log carries %q: %s", leak, out)
		}
	}
}

// golden24Tools are the tool definitions of 2.4 as a request sends them
// (the Tools of a Start, as JSON), byte for byte.
const golden24Tools = `[{"Name":"read_file","Description":"Read a file of the pull request's head commit, with line numbers like the diff. At most 400 lines per call; without a range, the first 400.","Parameters":{"type":"object","properties":{"path":{"type":"string","description":"repository-relative path, / separated"},"start_line":{"type":"integer","minimum":1,"description":"first line, 1-based (optional)"},"end_line":{"type":"integer","minimum":1,"description":"last line, 1-based (optional)"}},"required":["path"],"additionalProperties":false}},{"Name":"list_dir","Description":"List a directory of the pull request's head commit. Directories end with /. At most 200 entries.","Parameters":{"type":"object","properties":{"path":{"type":"string","description":"repository-relative directory, \"\" for the root"}},"required":["path"],"additionalProperties":false}},{"Name":"search","Description":"Search the pull request's head commit for a fixed string (not a pattern, case-sensitive). At most 50 matches, each with one line around it.","Parameters":{"type":"object","properties":{"text":{"type":"string","minLength":2,"maxLength":200,"description":"the text to find"},"path":{"type":"string","description":"limit the search to this directory (optional)"}},"required":["text"],"additionalProperties":false}}]`

// golden24Request is the wire request of a first tool round in 2.4 (system
// "sys", user "usr"), byte for byte.
const golden24Request = `{"model":"test-model","messages":[{"role":"system","content":"sys"},{"role":"user","content":"usr"}],` +
	`"tools":[{"type":"function","function":{"name":"read_file","description":"Read a file of the pull request's head commit, with line numbers like the diff. At most 400 lines per call; without a range, the first 400.","parameters":{"type":"object","properties":{"path":{"type":"string","description":"repository-relative path, / separated"},"start_line":{"type":"integer","minimum":1,"description":"first line, 1-based (optional)"},"end_line":{"type":"integer","minimum":1,"description":"last line, 1-based (optional)"}},"required":["path"],"additionalProperties":false}}},` +
	`{"type":"function","function":{"name":"list_dir","description":"List a directory of the pull request's head commit. Directories end with /. At most 200 entries.","parameters":{"type":"object","properties":{"path":{"type":"string","description":"repository-relative directory, \"\" for the root"}},"required":["path"],"additionalProperties":false}}},` +
	`{"type":"function","function":{"name":"search","description":"Search the pull request's head commit for a fixed string (not a pattern, case-sensitive). At most 50 matches, each with one line around it.","parameters":{"type":"object","properties":{"text":{"type":"string","minLength":2,"maxLength":200,"description":"the text to find"},"path":{"type":"string","description":"limit the search to this directory (optional)"}},"required":["text"],"additionalProperties":false}}}],` +
	`"tool_choice":"auto"}`

// TestToolSchemasGoldenWithoutRepositories: without a readable related
// repository the tool definitions and the request that carries them are
// those of 2.4, byte for byte (Definitions, DefinitionsFor(nil) and Start
// alike, on the wire too), and the instruction block has no line on them.
func TestToolSchemasGoldenWithoutRepositories(t *testing.T) {
	for name, tools := range map[string][]llm.Tool{"Definitions": Definitions(), "DefinitionsFor(nil)": DefinitionsFor(nil),
		"Start": Start("s", "u").Tools} {
		if got, _ := json.Marshal(tools); string(got) != golden24Tools {
			t.Errorf("%s:\n got %s\nwant %s", name, got, golden24Tools)
		}
	}
	var mu sync.Mutex
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		mu.Lock()
		body = buf.Bytes()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()
	c, err := llm.New(config.LLM{BaseURL: srv.URL, Model: "test-model", ContextWindow: 8192, TimeoutSeconds: 30, MaxRetries: 1},
		config.NewSecret("test-key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Converse(context.Background(), Start("sys", "usr")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if string(body) != golden24Request {
		t.Errorf("request\n got %s\nwant %s", body, golden24Request)
	}
	if Instruction(3, "final") != Instruction(3, "final", nil...) || strings.Contains(Instruction(3, "final"), "argument repo") {
		t.Errorf("instruction without repositories: %q", Instruction(3, "final"))
	}
}

// TestToolSchemasWithRepositories: with readable related repositories each
// tool gets the optional argument repo, an enum of their labels, and its
// schema stays an object without additional properties; the instruction
// names the labels and their repositories in one line.
func TestToolSchemasWithRepositories(t *testing.T) {
	r3 := consumerRepo()
	r3.Label, r3.Name = "R3", "grp/mobile\napp"
	defs := DefinitionsFor([]Repo{consumerRepo(), r3})
	for _, d := range defs {
		var s struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Type string   `json:"type"`
				Enum []string `json:"enum"`
			} `json:"properties"`
			Required             []string `json:"required"`
			AdditionalProperties *bool    `json:"additionalProperties"`
		}
		if err := json.Unmarshal(d.Parameters, &s); err != nil {
			t.Fatalf("%s: %v", d.Name, err)
		}
		repo, ok := s.Properties["repo"]
		if s.Type != "object" || s.AdditionalProperties == nil || *s.AdditionalProperties || !ok || repo.Type != "string" ||
			!slices.Equal(repo.Enum, []string{"R2", "R3"}) || slices.Contains(s.Required, "repo") {
			t.Errorf("%s: %s", d.Name, d.Parameters)
		}
		if !strings.Contains(d.Description, "With repo, it reads that related repository at its pinned commit instead.") {
			t.Errorf("%s: %q", d.Name, d.Description)
		}
	}
	in := Instruction(3, "final", consumerRepo(), r3)
	line := "- With the argument repo they read a related repository of the related block at its pinned commit " +
		"instead of the head commit: R2 for grp/web-ui, R3 for grp/mobile app.\n"
	if !strings.Contains(in, line) || strings.Count(in, "\n") != strings.Count(Instruction(3, "final"), "\n")+1 {
		t.Errorf("instruction %q", in)
	}
	if !strings.Contains(QuestionInstruction(2, "final", consumerRepo()), "R2 for grp/web-ui") {
		t.Error("the question instruction does not name the repository")
	}
}

// TestRepoToolCallJSON: a call of a related repository carries its label in
// coverage.repo_context.tools.calls[].repo; a call of the head commit
// serialises as in 2.4.
func TestRepoToolCallJSON(t *testing.T) {
	u := Use{Status: StatusUsed, Calls: []Call{{Tool: ReadFile, Target: "a.go", Status: StatusOK, Tokens: 5},
		{Tool: ReadFile, Target: "b.ts", Status: StatusOK, Tokens: 6, Repo: "R2"}}}
	got, err := json.Marshal(u.Report().Calls)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"tool":"read_file","target":"a.go","status":"ok","tokens":5},` +
		`{"tool":"read_file","target":"b.ts","status":"ok","tokens":6,"repo":"R2"}]`
	if string(got) != want {
		t.Fatalf("calls %s\nwant  %s", got, want)
	}
	_ = llmrun.RepoToolCall(Call{Repo: "R2"})
}
