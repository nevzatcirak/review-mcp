package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/review"
)

const (
	// localFileMarker is in a file of the synthetic repository that the
	// change does not touch; localSecretMarker in an untracked .env.
	localFileMarker   = "LOCAL-FILE-MARKER-7c41"
	localSecretMarker = "LOCAL-SECRET-MARKER-7c41"
)

// localWorkTree is a synthetic repository for local_review: main holds
// src/app.go, the branch feat changes its line 2 (with diffMarker), and
// refs/remotes/origin/main is main, as after a clone. HOME points at an
// empty directory for the test.
type localWorkTree struct {
	dir, base, head string
}

func newLocalWorkTree(t *testing.T) *localWorkTree {
	t.Helper()
	g, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; the local_review tests need it")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("USERPROFILE", home)
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(g, args...) //nolint:gosec // G204: the system git with fixed test arguments
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com"}
		if runtime.GOOS == "windows" {
			cmd.Env = append(cmd.Env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "TMP="+os.TempDir(), "TEMP="+os.TempDir())
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "core.autocrlf", "false")
	write("src/app.go", "package main\nvar a = 1\nfunc main() {}\n")
	write("docs/notes.md", "# notes "+localFileMarker+"\n")
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	w := &localWorkTree{dir: dir, base: run("rev-parse", "HEAD")}
	run("update-ref", "refs/remotes/origin/main", w.base)
	run("checkout", "-q", "-b", "feat")
	write("src/app.go", "package main\nvar a = 2 // "+diffMarker+"\nfunc main() {}\n")
	run("commit", "-q", "-am", "change a "+titleMarker)
	w.head = run("rev-parse", "HEAD")
	write(".env", "TOKEN="+localSecretMarker+"\n")
	return w
}

// localEnv is a valid configuration pointing at the fake LLM.
func localEnv(l *fakeLLMHost) map[string]string {
	env := validEnv()
	env["REVIEW_MCP_LLM_BASE_URL"] = l.srv.URL + "/v1"
	env["REVIEW_MCP_LOG_LEVEL"] = "debug"
	return env
}

// TestLocalReviewToolDefinition covers canary (j): the annotations, the
// description of the spec and an input schema without publish, findings,
// incremental or related (local work publishes nothing and has no earlier
// review or related pull requests).
// Mutation: accept a publish argument.
func TestLocalReviewToolDefinition(t *testing.T) {
	cs := connect(t, depsFor(validEnv(), nil))
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var tl *mcp.Tool
	for _, x := range list.Tools {
		if x.Name == "local_review" {
			tl = x
		}
	}
	if tl == nil {
		t.Fatal("local_review is not registered in stdio mode")
	}
	if !strings.HasPrefix(tl.Description, localReviewDescription) {
		t.Errorf("description = %q", tl.Description)
	}
	a := tl.Annotations
	if a == nil || !a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v", a)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal([]byte(mustJSON(t, tl.InputSchema)), &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"publish", "findings", "incremental", "related", "inline_findings", "pr_url",
		"security_concerns", "performance_concerns"} {
		if _, ok := schema.Properties[name]; ok {
			t.Errorf("the input schema has %q", name)
		}
	}
	for _, name := range []string{"repo_path", "base", "include_uncommitted", "include_untracked", "focus",
		"extra_instructions", "output_language", "max_findings", "issue_context", "wait_seconds"} {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("the input schema lacks %q", name)
		}
	}
	if !slices.Equal(schema.Required, []string{"repo_path"}) {
		t.Errorf("required = %v", schema.Required)
	}
	out := mustJSON(t, tl.OutputSchema)
	for _, want := range []string{`"untracked_held_back"`, `"merge_base"`, `"null"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the output schema lacks %s", want)
		}
	}
}

// TestLocalReviewNotInServe is canary (h), the tool half: in serve mode
// local_review is absent (the server's disk is not the user's), and with
// local.enabled false it is absent in stdio mode too.
// Mutation: register it in serve mode.
func TestLocalReviewNotInServe(t *testing.T) {
	srv := httptest.NewServer(NewHTTPHandler(serveDeps(t, nil)))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	if names := toolNames(t, cs); slices.Contains(names, "local_review") {
		t.Errorf("serve mode registers local_review: %v", names)
	}
	if strings.Contains(cs.InitializeResult().Instructions, "local_review") {
		t.Error("the serve instructions mention local_review")
	}
	env := validEnv()
	env["REVIEW_MCP_LOCAL_ENABLED"] = "false"
	off := connect(t, depsFor(env, nil))
	if names := toolNames(t, off); slices.Contains(names, "local_review") {
		t.Errorf("local.enabled=false registers local_review: %v", names)
	}
	if strings.Contains(off.InitializeResult().Instructions, "local_review") {
		t.Error("the instructions mention local_review while it is off")
	}
}

// TestLocalReviewCall reviews a synthetic work tree end to end: the model
// sees the local diff; the result has pr.kind local, pr.url null, the
// local object, the stale-base note, no CI and no publish; an untracked
// .env is held back and counted with its note and never reaches the model.
func TestLocalReviewCall(t *testing.T) {
	w := newLocalWorkTree(t)
	l := newFakeLLMHost(t, 200, goodAnswer)
	env := localEnv(l)
	env["REVIEW_MCP_REVIEW_CI_STATUS"] = "true"
	cs := connect(t, realDeps(env, nil))
	res := callTool(t, cs, "local_review", map[string]any{"repo_path": w.dir, "include_untracked": true, "max_findings": 3})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	raw := mustJSON(t, res.StructuredContent)
	var got review.Result
	decodeStructured(t, res, &got)
	if got.PR.Kind != "local" || !strings.Contains(raw, `"url":null`) || got.PR.Title != "change a "+titleMarker {
		t.Errorf("pr = %+v (raw %s)", got.PR, raw[:min(len(raw), 300)])
	}
	lo := got.Local
	if lo == nil || lo.RepoPath != w.dir || lo.BaseRef != "origin/main" || lo.BaseSHA != w.base || lo.MergeBase != w.base ||
		lo.HeadSHA != w.head || !lo.Uncommitted || lo.UntrackedFiles != 0 || lo.UntrackedHeldBack != 1 {
		t.Errorf("local = %+v", lo)
	}
	if got.CI != nil || got.Publish != nil {
		t.Errorf("ci = %+v, publish = %+v; want neither", got.CI, got.Publish)
	}
	notes := strings.Join(got.Notes, "\n")
	for _, want := range []string{"the base origin/main is the local copy of a remote-tracking branch",
		"1 untracked files were left out because their names suggest secrets."} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "CI") {
		t.Errorf("a CI note appeared:\n%s", notes)
	}
	bodies := l.recorded()
	if len(bodies) != 1 || !strings.Contains(bodies[0], diffMarker) {
		t.Fatalf("LLM requests = %d", len(bodies))
	}
	if strings.Contains(bodies[0], localSecretMarker) {
		t.Error("the held-back .env reached the model")
	}
	if got.Review == nil || len(got.Review.KeyIssuesToReview) != 1 || got.Review.KeyIssuesToReview[0].Link != "" {
		t.Errorf("review = %+v", got.Review)
	}
}

// TestLocalReviewRefusals: a relative path and an unknown base are refused
// with their fixed sentences before any model call.
func TestLocalReviewRefusals(t *testing.T) {
	w := newLocalWorkTree(t)
	l := newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(localEnv(l), nil))
	for args, want := range map[*map[string]any]string{
		{"repo_path": "relative/dir"}:                    "repo_path must be an absolute path",
		{"repo_path": w.dir, "base": "nope"}:             "base names no commit in the repository",
		{"repo_path": w.dir, "base": "HEAD~1"}:           "base must be a ref name",
		{"repo_path": w.dir, "max_findings": 99}:         "max_findings must be an integer from 1 to 20",
		{"repo_path": filepath.Join(w.dir, "missing")}:   "repo_path does not exist",
		{"repo_path": w.dir, "output_language": "x y z"}: "output_language must be a locale code",
	} {
		res := callTool(t, cs, "local_review", *args)
		if !res.IsError || !strings.Contains(textOf(t, res), want) {
			t.Errorf("%v: %s", *args, textOf(t, res))
		}
	}
	if n := len(l.recorded()); n != 0 {
		t.Errorf("the model was called %d times", n)
	}
}

// TestLeakLocalReview is canary (k): with log.level=debug, a local review
// logs no file content, no absolute path beyond the work tree's base name
// and no token.
// Mutation: log the work tree's full path.
func TestLeakLocalReview(t *testing.T) {
	w := newLocalWorkTree(t)
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(localEnv(l), logger))
	res := callTool(t, cs, "local_review", map[string]any{"repo_path": w.dir, "include_untracked": true,
		"extra_instructions": "be brief " + argMarker})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	logText := logs.String()
	if !strings.Contains(logText, "level=DEBUG") || !strings.Contains(logText, "local_review") ||
		!strings.Contains(logText, filepath.Base(w.dir)) {
		t.Fatalf("debug logging did not run; the leak check would be vacuous:\n%s", logText)
	}
	for _, m := range []string{diffMarker, titleMarker, localFileMarker, localSecretMarker, argMarker, headerMarker,
		contentMarker, w.dir, filepath.ToSlash(w.dir), filepath.Dir(w.dir)} {
		if strings.Contains(logText, m) {
			t.Errorf("%q leaked into the logs", m)
		}
	}
	for _, secret := range allSecrets {
		if strings.Contains(logText, secret) {
			t.Errorf("secret %q leaked into the logs", secret)
		}
	}
}

// TestLocalReviewWithoutNotes: a review with no note (a local base, no
// untracked file) still has a list of notes, as the output schema needs.
func TestLocalReviewWithoutNotes(t *testing.T) {
	w := newLocalWorkTree(t)
	if err := os.Remove(filepath.Join(w.dir, ".env")); err != nil {
		t.Fatal(err)
	}
	l := newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, realDeps(localEnv(l), nil))
	res := callTool(t, cs, "local_review", map[string]any{"repo_path": w.dir, "base": "main"})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	if raw := mustJSON(t, res.StructuredContent); !strings.Contains(raw, `"notes":[`) {
		t.Errorf("notes are not a list: %s", raw)
	}
}
