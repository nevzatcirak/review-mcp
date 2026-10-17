package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/credentials"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const promptPR = "https://your-gitea.example/octo/demo/pulls/7"

// promptFake serves the one pull request and the files of its base revision
// that the focus default of the prompt reads. The embedded nil interface
// makes any other call panic: the prompt reads the pull request and one
// file, and nothing else.
type promptFake struct {
	provider.Provider

	mu      sync.Mutex
	file    string // the content of .review-mcp.toml at the base revision; "" is absent
	fileErr error
	prErr   error
	prCalls int
	reads   []string // "<path>@<rev>"
}

const (
	promptBase = "base-sha"
	promptHead = "head-sha"
)

func (f *promptFake) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prCalls++
	if f.prErr != nil {
		return nil, f.prErr
	}
	return &provider.PullRequest{Title: "t", HeadSHA: promptHead, BaseSHA: promptBase}, nil
}

func (f *promptFake) GetFileAt(_ context.Context, _ provider.PRRef, path, rev string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, path+"@"+rev)
	if f.fileErr != nil {
		return nil, f.fileErr
	}
	if f.file == "" || rev != promptBase {
		return nil, provider.ErrNotFound
	}
	return []byte(f.file), nil
}

func promptText(t *testing.T, cs *mcp.ClientSession, args map[string]string) string {
	t.Helper()
	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "multi_perspective_review", Arguments: args})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	return res.Messages[0].Content.(*mcp.TextContent).Text
}

func focusLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "   - {") {
			out = append(out, l)
		}
	}
	return out
}

func wantFocuses(focuses ...string) string {
	return multiPerspectiveText(promptPR, focuses, false)
}

var builtinDefault = []string{"security", "architecture", "performance", "tests"}

func promptSession(t *testing.T, f *promptFake) *mcp.ClientSession {
	t.Helper()
	deps, _ := withFake(depsFor(validEnv(), nil), f)
	return connect(t, deps)
}

// TestPromptTakesFocusesFromTheRepositoryConfig: without a focuses argument
// the prompt lists review.focuses of the repository config read at the base
// revision (X-40); it reads the pull request and that one file, nothing
// else.
func TestPromptTakesFocusesFromTheRepositoryConfig(t *testing.T) {
	f := &promptFake{file: "[review]\nfocuses = [\"security\", \"tests\"]\n"}
	cs := promptSession(t, f)
	text := promptText(t, cs, map[string]string{"pr_url": promptPR})
	if text != wantFocuses("security", "tests") {
		t.Errorf("message = %s", text)
	}
	if got := focusLines(text); len(got) != 2 || strings.Contains(text, "architecture") {
		t.Errorf("focus lines %q", got)
	}
	if f.prCalls != 1 || len(f.reads) != 1 || f.reads[0] != ".review-mcp.toml@"+promptBase {
		t.Errorf("pr reads %d, file reads %q: want the pull request once and the file once at the base revision", f.prCalls, f.reads)
	}
}

func TestPromptNormalisesTheRepositoryFocuses(t *testing.T) {
	f := &promptFake{file: "[review]\nfocuses = [\" UX\", \"tests\", \"ux\"]\n"}
	cs := promptSession(t, f)
	if text := promptText(t, cs, map[string]string{"pr_url": promptPR}); text != wantFocuses("ux", "tests") {
		t.Errorf("message = %s", text)
	}
}

// TestPromptFallsBackToTheBuiltInDefault: every way the repository's list can
// be missing, unusable or unreadable gives the built-in default, without an
// error.
func TestPromptFallsBackToTheBuiltInDefault(t *testing.T) {
	for name, f := range map[string]*promptFake{
		"no file":          {},
		"no focuses key":   {file: "[review]\nmax_findings = 2\n"},
		"empty list":       {file: "[review]\nfocuses = []\n"},
		"unknown value":    {file: "[review]\nfocuses = [\"security\", \"naming\"]\n"},
		"not a list":       {file: "[review]\nfocuses = \"security\"\n"},
		"not toml":         {file: "[review\nfocuses = "},
		"read error":       {fileErr: &provider.Error{Class: provider.ClassTransport}},
		"plain read error": {fileErr: errors.New("boom")},
		"pull request err": {prErr: &provider.Error{Class: provider.ClassAuth}, file: "[review]\nfocuses = [\"ux\"]\n"},
	} {
		t.Run(name, func(t *testing.T) {
			cs := promptSession(t, f)
			if text := promptText(t, cs, map[string]string{"pr_url": promptPR}); text != wantFocuses(builtinDefault...) {
				t.Errorf("message = %s", text)
			}
		})
	}
}

// TestPromptWithFocusesReadsNothing: given focuses are used as given and the
// prompt makes no request.
func TestPromptWithFocusesReadsNothing(t *testing.T) {
	f := &promptFake{file: "[review]\nfocuses = [\"ux\"]\n"}
	cs := promptSession(t, f)
	if text := promptText(t, cs, map[string]string{"pr_url": promptPR, "focuses": "tests,security"}); text != wantFocuses("tests", "security") {
		t.Errorf("message = %s", text)
	}
	if f.prCalls != 0 || len(f.reads) != 0 {
		t.Errorf("pr reads %d, file reads %q: want none", f.prCalls, f.reads)
	}
}

// TestPromptWithRepoConfigOffReadsNothing: repo_config.enabled=false.
func TestPromptWithRepoConfigOffReadsNothing(t *testing.T) {
	f := &promptFake{file: "[review]\nfocuses = [\"ux\"]\n"}
	env := validEnv()
	env["REVIEW_MCP_REPO_CONFIG_ENABLED"] = "false"
	deps, n := withFake(depsFor(env, nil), f)
	cs := connect(t, deps)
	if text := promptText(t, cs, map[string]string{"pr_url": promptPR}); text != wantFocuses(builtinDefault...) {
		t.Errorf("message = %s", text)
	}
	if f.prCalls != 0 || len(f.reads) != 0 || n.Load() != 0 {
		t.Errorf("pr reads %d, file reads %q, resolvers built %d: want none", f.prCalls, f.reads, n.Load())
	}
}

// TestPromptWithAUrlNoProviderMatches: a URL no provider serves gives the
// default, not an error.
func TestPromptWithAUrlNoProviderMatches(t *testing.T) {
	f := &promptFake{file: "[review]\nfocuses = [\"ux\"]\n"}
	cs := promptSession(t, f)
	other := "https://other.example/octo/demo/pulls/7"
	if text := promptText(t, cs, map[string]string{"pr_url": other}); text != multiPerspectiveText(other, builtinDefault, false) {
		t.Errorf("message = %s", text)
	}
	if f.prCalls != 0 {
		t.Errorf("pull request read %d times for an unmatched URL", f.prCalls)
	}
}

// TestPromptServeUsesTheRequestCredentials: in serve mode the read uses the
// provider token of the prompt's own request, and without one it falls back
// to the default.
func TestPromptServeUsesTheRequestCredentials(t *testing.T) {
	f := &promptFake{file: "[review]\nfocuses = [\"ux\"]\n"}
	deps, _ := withFake(serveDeps(t, nil), f)

	h := http.Header{}
	h.Set(credentials.HeaderGiteaToken, "request-token")
	req := &mcp.GetPromptRequest{Extra: &mcp.RequestExtra{Header: h}}
	if got := defaultFocusesFor(context.Background(), deps, req, promptPR); strings.Join(got, ",") != "ux" {
		t.Errorf("focuses %q, want the repository's", got)
	}

	f2 := &promptFake{file: "[review]\nfocuses = [\"ux\"]\n"}
	deps2, _ := withFake(serveDeps(t, nil), f2)
	if got := defaultFocusesFor(context.Background(), deps2, &mcp.GetPromptRequest{}, promptPR); strings.Join(got, ",") != strings.Join(builtinDefault, ",") {
		t.Errorf("focuses %q, want the default without a credential", got)
	}
	if f2.prCalls != 0 || len(f2.reads) != 0 {
		t.Errorf("a request without a token read %d / %q", f2.prCalls, f2.reads)
	}
}
