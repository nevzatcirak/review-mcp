package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

const (
	testLLMKey = "FAKE-llm-key-ZQ7X-do-not-leak"
	testGitea  = "FAKE-gitea-token-ZQ7X-do-not-leak"
	testBitbkt = "FAKE-bitbucket-token-ZQ7X-do-not-leak"
)

func validEnv() map[string]string {
	return map[string]string{
		"REVIEW_MCP_LLM_BASE_URL":                "https://llm.example.com/v1",
		"REVIEW_MCP_LLM_MODEL":                   "example-model",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW":          "32000",
		"REVIEW_MCP_LLM_API_KEY":                 testLLMKey,
		"REVIEW_MCP_GITEA_BASE_URL":              "https://your-gitea.example",
		"REVIEW_MCP_GITEA_TOKEN":                 testGitea,
		"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL":   "https://bitbucket.example.com/bb",
		"REVIEW_MCP_BITBUCKET_SERVER_TOKEN":      testBitbkt,
		"REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY":  "true", // produces a warning
		"REVIEW_MCP_SOMETHING_UNKNOWN":           "x",
		"REVIEW_MCP_REVIEW_EXTRA_INSTRUCTIONS":   "use <b>bold</b> and `ticks`\nsecond line",
		"REVIEW_MCP_DIFF_SKIP_EXTEND_EXTENSIONS": ".md,.txt",
	}
}

func load(env map[string]string) (*config.Config, *config.Report, error) {
	return config.Load(config.MemSource{Env: env})
}

func TestServerInfoValid(t *testing.T) {
	cfg, rep, err := load(validEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := ServerInfo(cfg, rep, nil)
	if r.Name != "review-mcp" || r.Status != StatusOK {
		t.Errorf("name/status = %q/%q", r.Name, r.Status)
	}
	if len(r.Problems) != 0 {
		t.Errorf("problems = %v, want none", r.Problems)
	}
	if len(r.Providers) != 2 || r.Providers[0].Kind != "gitea" || r.Providers[1].Kind != "bitbucket_server" {
		t.Errorf("providers = %+v", r.Providers)
	}
	if len(r.Warnings) == 0 {
		t.Error("expected warnings (insecure TLS, unknown variable)")
	}
	if r.Version == "" || r.GoVersion == "" {
		t.Errorf("version info missing: %+v", r)
	}
	if r.Config.Secrets["llm.api_key"] != "set" {
		t.Errorf("secrets = %v, want a set llm key", r.Config.Secrets)
	}
}

func TestServerInfoInvalidConfig(t *testing.T) {
	env := map[string]string{
		"REVIEW_MCP_LLM_API_KEY": testLLMKey,
		"REVIEW_MCP_GITEA_TOKEN": testGitea,
		// no base URLs, model or context window: several problems at once
	}
	cfg, rep, err := load(env)
	if err == nil {
		t.Fatal("expected a validation error")
	}
	r := ServerInfo(cfg, rep, err)
	if r.Status != StatusConfigInvalid {
		t.Errorf("status = %q", r.Status)
	}
	var ve *config.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError, got %T", err)
	}
	if len(r.Problems) != len(ve.Problems) || len(r.Problems) < 2 {
		t.Errorf("problems = %v, want all of %v", r.Problems, ve.Problems)
	}
	assertNoSecrets(t, r, RenderServerInfoMarkdown(r))
}

func TestServerInfoNonValidationError(t *testing.T) {
	cfg, rep, _ := load(validEnv())
	r := ServerInfo(cfg, rep, fmt.Errorf("boom %s", testLLMKey))
	if r.Status != StatusConfigInvalid || len(r.Problems) != 1 {
		t.Fatalf("result = %+v", r)
	}
	if strings.Contains(r.Problems[0], "boom") || strings.Contains(r.Problems[0], testLLMKey) {
		t.Errorf("generic problem echoes the error text: %q", r.Problems[0])
	}
}

func TestServerInfoNilInputs(t *testing.T) {
	r := ServerInfo(nil, nil, nil)
	if r.Status != StatusOK || r.Problems == nil || r.Warnings == nil || r.Providers == nil {
		t.Errorf("nil inputs gave %+v", r)
	}
}

func TestServerInfoSlicesSerializeAsArrays(t *testing.T) {
	r := ServerInfo(config.Defaults(), nil, nil)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"problems", "warnings", "providers"} {
		if string(m[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, m[k])
		}
	}
}

func assertNoSecrets(t *testing.T, r ServerInfoResult, md string) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{testLLMKey, testGitea, testBitbkt} {
		if strings.Contains(string(raw), s) || strings.Contains(md, s) {
			t.Errorf("secret %q leaked", s)
		}
	}
}

func TestRenderMarkdownContent(t *testing.T) {
	cfg, rep, err := load(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	r := ServerInfo(cfg, rep, nil)
	md := RenderServerInfoMarkdown(r)
	for _, want := range []string{
		"# review-mcp server info", "## Providers", "## Secrets", "## Effective configuration",
		"`gitea`: `https://your-gitea.example`", "## Warnings", "`llm.model` = `\"example-model\"` (env)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "## Configuration problems") {
		t.Error("valid config must not render a problems section")
	}
	assertNoSecrets(t, r, md)
	assertNoHTML(t, md)
}

func TestRenderMarkdownInvalid(t *testing.T) {
	r := ServerInfo(config.Defaults(), nil, &config.ValidationError{Problems: []string{
		"llm.base_url is required", "bad <script>alert(1)</script> value", "has `backticks` inside",
	}})
	md := RenderServerInfoMarkdown(r)
	if !strings.Contains(md, "## Configuration problems") || !strings.Contains(md, "None enabled.") {
		t.Errorf("unexpected markdown:\n%s", md)
	}
	assertNoHTML(t, md)
}

// TestRenderMarkdownNoHTML feeds markup through every dynamic field.
func TestRenderMarkdownNoHTML(t *testing.T) {
	r := ServerInfo(config.Defaults(), nil, &config.ValidationError{Problems: []string{"<img src=x onerror=1>"}})
	r.Warnings = []string{"<b>warn</b>", "``<i>``"}
	r.Version = "<u>v</u>"
	r.Providers = []ProviderInfo{{Kind: "<k>", BaseURL: "<a href=x>"}}
	r.Config.Values["review.extra_instructions"] = config.SummaryValue{Value: "<div>x</div>\n<p>y", Source: config.OriginEnv}
	assertNoHTML(t, RenderServerInfoMarkdown(r))
}

func TestAssertNoHTMLCatchesRawHTML(t *testing.T) {
	if got := htmlOutsideCode("fine `<b>` text\n<div>raw</div>"); got == "" {
		t.Fatal("helper failed to detect raw HTML outside a code span")
	}
	if got := htmlOutsideCode("fine `<b>` and ``a ` <i> b`` text"); got != "" {
		t.Fatalf("helper flagged code-span content: %q", got)
	}
}

func assertNoHTML(t *testing.T, md string) {
	t.Helper()
	if got := htmlOutsideCode(md); got != "" {
		t.Errorf("raw HTML-like text outside code spans: %q", got)
	}
}

// htmlOutsideCode returns the first '<' followed by a letter found outside
// code spans, or "". Code spans are delimited by equal-length backtick runs.
func htmlOutsideCode(md string) string {
	rs := []rune(md)
	for i := 0; i < len(rs); i++ {
		switch {
		case rs[i] == '`':
			n := 0
			for i+n < len(rs) && rs[i+n] == '`' {
				n++
			}
			end := closingRun(rs, i+n, n)
			if end < 0 {
				i += n - 1 // unterminated: the backticks are literal text
				continue
			}
			i = end + n - 1
		case rs[i] == '<' && i+1 < len(rs) && unicode.IsLetter(rs[i+1]):
			return string(rs[i:min(i+20, len(rs))])
		}
	}
	return ""
}

// closingRun finds the start of the next backtick run of exactly n backticks.
func closingRun(rs []rune, from, n int) int {
	for j := from; j < len(rs); j++ {
		if rs[j] != '`' {
			continue
		}
		k := 0
		for j+k < len(rs) && rs[j+k] == '`' {
			k++
		}
		if k == n {
			return j
		}
		j += k - 1
	}
	return -1
}

func TestCodeSpan(t *testing.T) {
	tests := map[string]string{
		"plain":    "`plain`",
		"a`b":      "``a`b``",
		"`lead":    "`` `lead ``",
		"two\nlns": "`two lns`",
		"":         "` `",
	}
	for in, want := range tests {
		if got := codeSpan(in); got != want {
			t.Errorf("codeSpan(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestServerInfoReportsServeKeys: the summary lists the serve.* keys and the
// serve access token as set or unset only (P6 §1.2).
func TestServerInfoReportsServeKeys(t *testing.T) {
	const access = "FAKE-serve-access-ZQ7X-do-not-leak" //nolint:gosec // synthetic test value
	env := validEnv()
	env["REVIEW_MCP_SERVE_ACCESS_TOKEN"] = access
	env["REVIEW_MCP_SERVE_LISTEN"] = "127.0.0.1:9000"
	cfg, rep, err := load(env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := ServerInfo(cfg, rep, nil)
	if r.Config.Secrets["serve.access_token"] != "set" {
		t.Errorf("secrets = %v", r.Config.Secrets)
	}
	if v := r.Config.Values["serve.listen"]; v.Value != "127.0.0.1:9000" || v.Source != config.OriginEnv {
		t.Errorf("serve.listen = %+v", v)
	}
	md := RenderServerInfoMarkdown(r)
	if !strings.Contains(md, "`serve.access_token`: set") || !strings.Contains(md, "`serve.max_concurrent_calls` = `4` (default)") {
		t.Errorf("markdown lacks the serve keys:\n%s", md)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), access) || strings.Contains(md, access) {
		t.Error("access token leaked")
	}
	assertNoSecrets(t, r, md)
}
