package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func getLinked(t *testing.T, cs *mcp.ClientSession, args map[string]string) (string, error) {
	t.Helper()
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "review_linked_prs", Arguments: args})
	if err != nil {
		return "", err
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("messages %+v", res.Messages)
	}
	return res.Messages[0].Content.(*mcp.TextContent).Text, nil
}

// TestLinkedPrompt: review_linked_prs tells the client to call pr_review
// once per URL with the others as related, to publish only with publish
// true, each to its own pull request, and never to merge the reviews;
// the URLs appear only inside JSON strings.
func TestLinkedPrompt(t *testing.T) {
	cs := connect(t, depsFor(validEnv(), nil))
	a, b, c := "https://your-gitea.example/octo/ui/pulls/4", "https://gitlab.example/grp/api/-/merge_requests/9?tab=1",
		"https://your-gitea.example/octo/docs/pulls/2#x"
	text, err := getLinked(t, cs, map[string]string{"pr_urls": a + ",\n" + b + "  " + c})
	if err != nil {
		t.Fatal(err)
	}
	bn, cn := "https://gitlab.example/grp/api/-/merge_requests/9", "https://your-gitea.example/octo/docs/pulls/2"
	for _, want := range []struct {
		url string
		rel []string
	}{{a, []string{bn, cn}}, {bn, []string{a, cn}}, {cn, []string{a, bn}}} {
		raw, _ := json.Marshal(map[string]any{"pr_url": want.url, "related": want.rel})
		if !strings.Contains(text, "   - "+string(raw)+"\n") {
			t.Errorf("the prompt lacks the call %s:\n%s", raw, text)
		}
	}
	if !strings.Contains(text, "Do not publish") || strings.Contains(text, `"publish":true`) ||
		!strings.Contains(text, "Never merge the reviews across pull requests") {
		t.Errorf("prompt:\n%s", text)
	}
	if strings.Count(text, a) != strings.Count(text, `"`+a+`"`) {
		t.Error("a URL appears outside a JSON string")
	}

	text, err = getLinked(t, cs, map[string]string{"pr_urls": a + " " + bn, "publish": " TRUE "})
	if err != nil || !strings.Contains(text, `"publish":true`) || !strings.Contains(text, "publishes its review to its own pull request only") {
		t.Errorf("publish: %v\n%s", err, text)
	}
}

// TestLinkedPromptArguments: invalid arguments are refused with
// CodeInvalidParams and a fixed message that repeats no value.
func TestLinkedPromptArguments(t *testing.T) {
	cs := connect(t, depsFor(validEnv(), nil))
	ok := "https://your-gitea.example/octo/ui/pulls/4 https://your-gitea.example/octo/api/pulls/5"
	withCredentials := "https://a.example/1 https://u:" + "SECRET-VALUE" + "@a.example/2"
	for name, args := range map[string]map[string]string{
		"one URL":     {"pr_urls": "https://your-gitea.example/octo/ui/pulls/4"},
		"six URLs":    {"pr_urls": strings.Repeat("https://your-gitea.example/octo/ui/pulls/4 ", 0) + "https://a.example/1 https://a.example/2 https://a.example/3 https://a.example/4 https://a.example/5 https://a.example/6"},
		"a repeat":    {"pr_urls": "https://a.example/1 https://a.example/1?x=2"},
		"credentials": {"pr_urls": withCredentials},
		"not http":    {"pr_urls": "https://a.example/1 ftp://a.example/2"},
		"missing":     {},
		"publish":     {"pr_urls": ok, "publish": "SECRET-VALUE"},
	} {
		_, err := getLinked(t, cs, args)
		var je *jsonrpc.Error
		if err == nil || !errors.As(err, &je) || je.Code != jsonrpc.CodeInvalidParams || strings.Contains(err.Error(), "SECRET-VALUE") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// TestLinkedPromptRateLimit: with a client-side rate limit the first step
// carries the queueing sentence, as multi_perspective_review's does.
func TestLinkedPromptRateLimit(t *testing.T) {
	env := validEnv()
	env["REVIEW_MCP_LLM_REQUESTS_PER_MINUTE"] = "30"
	cs := connect(t, depsFor(env, nil))
	text, err := getLinked(t, cs, map[string]string{"pr_urls": "https://a.example/1 https://a.example/2"})
	if err != nil || !strings.Contains(text, rateLimitGuidance) {
		t.Errorf("%v\n%s", err, text)
	}
	cs = connect(t, depsFor(validEnv(), nil))
	if text, _ := getLinked(t, cs, map[string]string{"pr_urls": "https://a.example/1 https://a.example/2"}); strings.Contains(text, rateLimitGuidance) {
		t.Error("the queueing sentence without a rate limit")
	}
}
