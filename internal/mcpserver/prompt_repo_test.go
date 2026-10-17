package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

	// block makes GetPullRequest (blockPR) or GetFileAt (blockFile) wait
	// until the context ends, like a host that does not answer.
	blockPR, blockFile bool

	mu      sync.Mutex
	file    string // the content of .review-mcp.toml at the base revision; "" is absent
	fileErr error
	prErr   error
	prCalls int
	reads   []string // "<path>@<rev>"
	// changed are the paths GetDiff lists; diffErr fails it. diffs counts
	// the calls and kept the files the Include of a call kept.
	changed []string
	diffErr error
	diffs   int
	kept    int
}

func (f *promptFake) GetDiff(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffs++
	if f.diffErr != nil {
		return nil, f.diffErr
	}
	d := &provider.Diff{}
	for _, p := range f.changed {
		if opts.Include != nil && !opts.Include(p) {
			d.Skipped = append(d.Skipped, provider.SkippedFile{Path: p, Reason: provider.SkipFiltered})
			continue
		}
		f.kept++
		d.Files = append(d.Files, provider.FilePatch{Path: p})
	}
	return d, nil
}

const (
	promptBase = "base-sha"
	promptHead = "head-sha"
)

func (f *promptFake) GetPullRequest(ctx context.Context, _ provider.PRRef) (*provider.PullRequest, error) {
	if f.blockPR {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prCalls++
	if f.prErr != nil {
		return nil, f.prErr
	}
	return &provider.PullRequest{Title: "t", HeadSHA: promptHead, BaseSHA: promptBase}, nil
}

func (f *promptFake) GetFileAt(ctx context.Context, _ provider.PRRef, path, rev string) ([]byte, error) {
	if f.blockFile {
		<-ctx.Done()
		return nil, ctx.Err()
	}
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

// TestPromptReadsAreBounded: a host that does not answer, at the pull request
// read or at the file read, cannot stall the prompt: the built-in default
// comes back within promptReadTimeout, which bounds both reads together.
func TestPromptReadsAreBounded(t *testing.T) {
	saved := promptReadTimeout
	t.Cleanup(func() { promptReadTimeout = saved })
	promptReadTimeout = 300 * time.Millisecond
	for name, f := range map[string]*promptFake{
		"pull request": {blockPR: true, file: "[review]\nfocuses = [\"ux\"]\n"},
		"file":         {blockFile: true},
	} {
		t.Run(name, func(t *testing.T) {
			cs := promptSession(t, f)
			start := time.Now()
			text := promptText(t, cs, map[string]string{"pr_url": promptPR})
			if d := time.Since(start); d > promptReadTimeout+2*time.Second {
				t.Errorf("the prompt took %v, deadline %v", d, promptReadTimeout)
			}
			if text != wantFocuses(builtinDefault...) {
				t.Errorf("message = %s", text)
			}
		})
	}
	if saved != 10*time.Second {
		t.Errorf("promptReadTimeout = %v, want 10 s", saved)
	}
}

// TestPromptAddsDocsWhenDocumentationChanges: without focuses and without
// review.focuses, docs joins the built-in default when the pull request
// changes a documentation file the ignore rules keep; the listing fetches no
// file content.
func TestPromptAddsDocsWhenDocumentationChanges(t *testing.T) {
	withDocs := append(slices.Clone(builtinDefault), "docs")
	for _, tc := range []struct {
		name string
		f    *promptFake
		want []string
	}{
		{"a guide changed", &promptFake{changed: []string{"internal/llm/client.go", "docs/setup.md"}}, withDocs},
		{"a README changed", &promptFake{changed: []string{"README"}}, withDocs},
		{"code only", &promptFake{changed: []string{"internal/llm/client.go"}}, builtinDefault},
		{"doc file ignored", &promptFake{changed: []string{"vendor/lib/README.md", "main.go"}}, builtinDefault},
		{"listing fails", &promptFake{changed: []string{"README.md"}, diffErr: &provider.Error{Class: provider.ClassUpstream}}, builtinDefault},
		{"repository list wins", &promptFake{changed: []string{"README.md"}, file: "[review]\nfocuses = [\"security\"]\n"}, []string{"security"}},
		{"repository list with docs", &promptFake{changed: []string{"main.go"}, file: "[review]\nfocuses = [\"docs\", \"tests\"]\n"}, []string{"docs", "tests"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := promptSession(t, tc.f)
			if text := promptText(t, cs, map[string]string{"pr_url": promptPR}); text != wantFocuses(tc.want...) {
				t.Errorf("message = %s", text)
			}
			if tc.f.kept != 0 {
				t.Errorf("the listing kept %d files: content would be fetched", tc.f.kept)
			}
		})
	}
	// The repository's list is used without listing the files.
	f := &promptFake{changed: []string{"README.md"}, file: "[review]\nfocuses = [\"ux\"]\n"}
	promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR})
	if f.diffs != 0 {
		t.Errorf("the files were listed %d times although the repository gives the list", f.diffs)
	}
	// Given focuses are used as given: no docs is added and nothing is read.
	f = &promptFake{changed: []string{"README.md"}}
	if text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR, "focuses": "tests"}); text != wantFocuses("tests") ||
		f.diffs != 0 || f.prCalls != 0 {
		t.Errorf("given focuses: message %s, diffs %d, pr reads %d", text, f.diffs, f.prCalls)
	}
	// docs is a focus value of the argument too.
	f = &promptFake{}
	if text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR, "focuses": "Docs,security"}); text != wantFocuses("docs", "security") {
		t.Errorf("message = %s", text)
	}
}
