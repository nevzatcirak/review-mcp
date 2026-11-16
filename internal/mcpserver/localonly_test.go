package mcpserver

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// localOnlyEnv is a configuration with only llm.* set, pointing at the fake
// LLM; no provider host is anywhere in it.
func localOnlyEnv(l *fakeLLMHost) map[string]string {
	env := localEnv(l)
	for _, k := range []string{"REVIEW_MCP_GITEA_BASE_URL", "REVIEW_MCP_GITEA_TOKEN",
		"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL", "REVIEW_MCP_BITBUCKET_SERVER_TOKEN",
		"REVIEW_MCP_GITHUB_BASE_URL", "REVIEW_MCP_GITHUB_TOKEN",
		"REVIEW_MCP_GITLAB_BASE_URL", "REVIEW_MCP_GITLAB_TOKEN"} {
		delete(env, k)
	}
	return env
}

func localOnlyDeps(t *testing.T, l *fakeLLMHost) Deps {
	t.Helper()
	d := realDeps(localOnlyEnv(l), nil)
	if d.LoadErr != nil {
		t.Fatalf("a local-only configuration must load: %v", d.LoadErr)
	}
	d.Jobs = NewJobs(nil)
	t.Cleanup(d.Jobs.Close)
	return d
}

func listToolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range list.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	return names
}

// TestLocalOnlyToolSet: [canary] (p). In a local-only start tools/list names
// exactly job_result, local_review and server_info.
// Mutation: register the full set -> fails.
func TestLocalOnlyToolSet(t *testing.T) {
	l := newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, localOnlyDeps(t, l))
	got := strings.Join(listToolNames(t, cs), ",")
	if want := "job_result,local_review,server_info"; got != want {
		t.Errorf("tools = %s, want %s", got, want)
	}
	if ins := cs.InitializeResult().Instructions; ins != localOnlyInstructions {
		t.Errorf("instructions = %q", ins)
	}
}

// TestLocalOnlyWithoutJobs: without a job store job_result is not
// registered, as in the provider case.
func TestLocalOnlyWithoutJobs(t *testing.T) {
	l := newFakeLLMHost(t, 200, goodAnswer)
	d := localOnlyDeps(t, l)
	d.Jobs = nil
	got := strings.Join(listToolNames(t, connect(t, d)), ",")
	if want := "local_review,server_info"; got != want {
		t.Errorf("tools = %s, want %s", got, want)
	}
}

// TestLocalOnlyServerInfo: local_only is true in a local-only start and
// false with a provider and in a degraded start.
func TestLocalOnlyServerInfo(t *testing.T) {
	l := newFakeLLMHost(t, 200, goodAnswer)
	info := structuredOf(t, callServerInfo(t, connect(t, localOnlyDeps(t, l))))
	if !info.LocalOnly || info.Status != "ok" || len(info.Providers) != 0 {
		t.Errorf("local-only: %+v", info)
	}
	if info := structuredOf(t, callServerInfo(t, connect(t, depsFor(validEnv(), nil)))); info.LocalOnly {
		t.Error("local_only with providers")
	}
	// Degraded: no provider, local.enabled = false.
	env := localOnlyEnv(l)
	env["REVIEW_MCP_LOCAL_ENABLED"] = "false"
	cs := connect(t, depsFor(env, nil))
	if info := structuredOf(t, callServerInfo(t, cs)); info.LocalOnly || info.Status != "config_invalid" {
		t.Errorf("degraded: %+v", info)
	}
	if n := len(listToolNames(t, cs)); n < 10 {
		t.Errorf("a degraded start registers %d tools, want the full set", n)
	}
}

// TestLocalOnlyLocalReview: a local review succeeds end to end in a
// local-only start; the only host is the fake LLM.
func TestLocalOnlyLocalReview(t *testing.T) {
	w := newLocalWorkTree(t)
	l := newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, localOnlyDeps(t, l))
	res := callTool(t, cs, "local_review", map[string]any{"repo_path": w.dir})
	if res.IsError {
		t.Fatalf("tool error: %s", textOf(t, res))
	}
	if n := len(l.recorded()); n != 1 {
		t.Errorf("LLM requests = %d", n)
	}
}

// TestLocalOnlyPrompt: pr_url is refused with InvalidParams and the fixed
// sentence; repo_path works; with a provider pr_url works as before.
func TestLocalOnlyPrompt(t *testing.T) {
	w := newLocalWorkTree(t)
	l := newFakeLLMHost(t, 200, goodAnswer)
	cs := connect(t, localOnlyDeps(t, l))
	get := func(cs *mcp.ClientSession, args map[string]string) (string, error) {
		res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "multi_perspective_review", Arguments: args})
		if err != nil {
			return "", err
		}
		return res.Messages[0].Content.(*mcp.TextContent).Text, nil
	}
	_, err := get(cs, map[string]string{"pr_url": "https://git.example.invalid/o/r/pulls/1"})
	const want = "no git provider is configured; only local reviews are available (pass repo_path)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("pr_url: %v", err)
	}
	if text, err := get(cs, map[string]string{"repo_path": w.dir}); err != nil || !strings.Contains(text, "local_review") {
		t.Errorf("repo_path: %v %q", err, text)
	}
	prov := connect(t, depsFor(validEnv(), nil))
	if _, err := get(prov, map[string]string{"pr_url": "https://your-gitea.example/octo/demo/pulls/7"}); err != nil {
		t.Errorf("with a provider: %v", err)
	}
}
