package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	testLLMKey = "FAKE-llm-key-ZQ7X-do-not-leak"
	testGitea  = "FAKE-gitea-token-ZQ7X-do-not-leak"
	testBitbkt = "FAKE-bitbucket-token-ZQ7X-do-not-leak"
	testGitHub = "FAKE-github-token-ZQ7X-do-not-leak"
	testGitLab = "FAKE-gitlab-token-ZQ7X-do-not-leak"
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
		"REVIEW_MCP_GITHUB_BASE_URL":             "https://github.example.com/ghe",
		"REVIEW_MCP_GITHUB_TOKEN":                testGitHub,
		"REVIEW_MCP_GITLAB_BASE_URL":             "https://example.com/gitlab",
		"REVIEW_MCP_GITLAB_TOKEN":                testGitLab,
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
	if len(r.Providers) != 4 || r.Providers[0].Kind != "gitea" || r.Providers[1].Kind != "bitbucket_server" ||
		r.Providers[2] != (ProviderInfo{Kind: "github", BaseURL: "https://github.example.com/ghe", APIURL: "https://github.example.com/ghe/api/v3"}) ||
		r.Providers[3] != (ProviderInfo{Kind: "gitlab", BaseURL: "https://example.com/gitlab", APIURL: "https://example.com/gitlab/api/v4"}) {
		t.Errorf("providers = %+v", r.Providers)
	}
	if len(r.Warnings) == 0 {
		t.Error("expected warnings (insecure TLS, unknown variable)")
	}
	if r.Version == "" || r.GoVersion == "" {
		t.Errorf("version info missing: %+v", r)
	}
	if r.Config.Secrets["llm.api_key"] != "set" || r.Config.Secrets["github.token"] != "set" || r.Config.Secrets["gitlab.token"] != "set" {
		t.Errorf("secrets = %v, want a set llm key, github token and gitlab token", r.Config.Secrets)
	}
	for _, k := range []string{"github.base_url", "github.api_url", "github.ca_cert", "github.insecure_skip_verify",
		"gitlab.base_url", "gitlab.ca_cert", "gitlab.insecure_skip_verify"} {
		if _, ok := r.Config.Values[k]; !ok {
			t.Errorf("config values lack %s", k)
		}
	}
}

// TestServerInfoListsPublishSettings: the effective configuration carries
// the review.* rows of WP-PR-7f, the incremental review's two settings
// (X-36) and the improve.* rows of v2 spec §1.9 with their values and
// origins, in the structured result and in the markdown.
func TestServerInfoListsPublishSettings(t *testing.T) {
	env := validEnv()
	env["REVIEW_MCP_REVIEW_INLINE_FINDINGS"] = "false"
	env["REVIEW_MCP_DIFF_MAX_TOKENS"] = "24000"
	env["REVIEW_MCP_REVIEW_MAX_CHUNKS"] = "4"
	env["REVIEW_MCP_IMPROVE_MAX_SUGGESTIONS"] = "12"
	env["REVIEW_MCP_REVIEW_INCREMENTAL"] = "true"
	cfg, rep, err := load(env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := ServerInfo(cfg, rep, nil)
	md := RenderServerInfoMarkdown(r)
	for _, w := range []struct{ key, value, origin string }{
		{"review.inline_findings", "false", "env"},
		{"review.persistent_overview", "true", "default"},
		{"review.max_discussion_tokens", "1500", "default"},
		{"review.max_chunks", "4", "env"},
		{"review.max_total_findings", "10", "default"},
		{"review.incremental", "true", "env"},
		{"review.incremental_max_files", "300", "default"},
		{"review.require_performance", "true", "default"},
		{"improve.max_suggestions", "12", "env"},
		{"improve.max_suggestions_per_part", "4", "default"},
		{"improve.min_score", "7", "default"},
		{"llm.wait_seconds", "45", "default"},
		{"diff.max_tokens", "24000", "env"},
		{"llm.timeout_seconds", "300", "default"},
		{"repo_config.enabled", "true", "default"},
	} {
		if _, ok := r.Config.Values[w.key]; !ok {
			t.Errorf("config values lack %s", w.key)
		}
		if line := "- `" + w.key + "` = `" + w.value + "` (" + w.origin + ")"; !strings.Contains(md, line) {
			t.Errorf("markdown lacks %q", line)
		}
	}
}

// TestServerInfoRepoContext: the context.repo.* rows appear with their
// origins; with context.repo.enabled the value is the git status (§3.0 item
// 4), and without it the plain false.
func TestServerInfoRepoContext(t *testing.T) {
	saved := repoContextStatus
	t.Cleanup(func() { repoContextStatus = saved })
	repoContextStatus = func() string { return "enabled, unavailable: git 2.31 or later is required" }

	env := validEnv()
	env["REVIEW_MCP_CONTEXT_REPO_IDLE_DAYS"] = "3"
	cfg, rep, err := load(env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	md := RenderServerInfoMarkdown(ServerInfo(cfg, rep, nil))
	for _, line := range []string{
		"- `context.repo.enabled` = `false` (default)",
		"- `context.repo.idle_days` = `3` (env)",
		"- `context.repo.cache_dir` = `\"\"` (default)",
		"- `context.repo.max_cache_mb` = `2048` (default)",
		"- `context.repo.max_repo_mb` = `500` (default)",
		"- `context.repo.fetch_timeout_seconds` = `60` (default)",
		"- `context.repo.max_symbols` = `20` (default)",
		"- `context.repo.max_hits_per_symbol` = `5` (default)",
		"- `context.repo.max_tokens` = `2000` (default)",
	} {
		if !strings.Contains(md, line) {
			t.Errorf("markdown lacks %q", line)
		}
	}

	env["REVIEW_MCP_CONTEXT_REPO_ENABLED"] = "true"
	cfg, rep, err = load(env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := ServerInfo(cfg, rep, nil)
	v := r.Config.Values["context.repo.enabled"]
	if v.Value != "enabled, unavailable: git 2.31 or later is required" || v.Source != config.OriginEnv {
		t.Errorf("context.repo.enabled = %v (%s)", v.Value, v.Source)
	}
	repoContextStatus = func() string { return "enabled, git 2.45.1" }
	if line := "- `context.repo.enabled` = `\"enabled, git 2.45.1\"` (env)"; !strings.Contains(RenderServerInfoMarkdown(ServerInfo(cfg, rep, nil)), line) {
		t.Errorf("markdown lacks %q", line)
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
	for _, s := range []string{testLLMKey, testGitea, testBitbkt, testGitHub, testGitLab} {
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
		"`gitea`: `https://your-gitea.example`", "## Warnings",
		"`github`: `https://github.example.com/ghe` (API `https://github.example.com/ghe/api/v3`)",
		"`gitlab`: `https://example.com/gitlab` (API `https://example.com/gitlab/api/v4`)", "`llm.model` = `\"example-model\"` (env)",
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
	if !strings.Contains(md, "`serve.access_token`: set") || !strings.Contains(md, "`serve.max_concurrent_calls` = `4` (default)") ||
		!strings.Contains(md, "`serve.max_queued_calls` = `32` (default)") {
		t.Errorf("markdown lacks the serve keys:\n%s", md)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), access) || strings.Contains(md, access) {
		t.Error("access token leaked")
	}
	assertNoSecrets(t, r, md)
}

// TestServerInfoProviderCapabilities: SetProviderCapabilities fills each
// provider's issues flag from the capabilities it is given; a kind whose
// capabilities are not known keeps false. The markdown names the providers
// that read issues.
func TestServerInfoProviderCapabilities(t *testing.T) {
	cfg, rep, err := load(validEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := ServerInfo(cfg, rep, nil)
	for _, p := range r.Providers {
		if p.Issues {
			t.Fatalf("%s reports issue reads before its capabilities are known", p.Kind)
		}
	}
	r.SetProviderCapabilities(func(kind string) (provider.Capabilities, bool) {
		switch kind {
		case "github":
			return provider.Capabilities{Issues: true}, true
		case "bitbucket_server":
			return provider.Capabilities{}, true
		}
		return provider.Capabilities{Issues: true}, false
	})
	for _, p := range r.Providers {
		if p.Issues != (p.Kind == "github") {
			t.Errorf("%s: issues %v, want %v", p.Kind, p.Issues, p.Kind == "github")
		}
	}
	md := RenderServerInfoMarkdown(r)
	if !strings.Contains(md, "(API `https://github.example.com/ghe/api/v3`), reads issues\n") ||
		strings.Count(md, "reads issues") != 1 {
		t.Errorf("markdown providers:\n%s", md)
	}
}

// TestServerInfoRepoTools: the tool rounds and tool tokens are reported with
// the other values, the configured models are listed with what this process
// knows of their tool calling (no request is made), and rounds above 0 with
// repository context off are a warning (WP-12b DQ-15).
func TestServerInfoRepoTools(t *testing.T) {
	env := validEnv()
	env["REVIEW_MCP_CONTEXT_REPO_MAX_TOOL_ROUNDS"] = "3"
	env["REVIEW_MCP_CONTEXT_REPO_MAX_TOOL_TOKENS"] = "9000"
	env["REVIEW_MCP_LLM_FALLBACK_1_MODEL"] = "fallback-model"
	cfg, rep, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	r := ServerInfo(cfg, rep, nil)
	if v := r.Config.Values["context.repo.max_tool_rounds"]; v.Value != 3 {
		t.Errorf("max_tool_rounds = %+v", v)
	}
	if v := r.Config.Values["context.repo.max_tool_tokens"]; v.Value != 9000 {
		t.Errorf("max_tool_tokens = %+v", v)
	}
	if len(r.RepoTools) != 2 || r.RepoTools[0].Model != "example-model" || r.RepoTools[1].Model != "fallback-model" ||
		r.RepoTools[0].BaseURL != "https://llm.example.com/v1" {
		t.Fatalf("repo tools %+v", r.RepoTools)
	}
	for _, m := range r.RepoTools {
		if m.Tools != "not known yet" {
			t.Errorf("%+v", m)
		}
	}
	warned := false
	for _, w := range r.Warnings {
		warned = warned || strings.Contains(w, "context.repo.max_tool_rounds is set but context.repo.enabled is off")
	}
	if !warned {
		t.Errorf("warnings %q lack the tool rounds warning", r.Warnings)
	}
	md := RenderServerInfoMarkdown(r)
	if !strings.Contains(md, "## Tool calling (repository tools)") || !strings.Contains(md, "`example-model` at `https://llm.example.com/v1`: tools: `not known yet`") {
		t.Errorf("markdown lacks the tool calling section:\n%s", md)
	}

	// With repository context on there is no warning, and the key is secret
	// free in the structured result.
	env["REVIEW_MCP_CONTEXT_REPO_ENABLED"] = "true"
	cfg, rep, err = load(env)
	if err != nil {
		t.Fatal(err)
	}
	r = ServerInfo(cfg, rep, nil)
	for _, w := range r.Warnings {
		if strings.Contains(w, "max_tool_rounds") {
			t.Errorf("a warning with repository context on: %q", w)
		}
	}
	if raw, _ := json.Marshal(r); strings.Contains(string(raw), testLLMKey) {
		t.Error("the LLM key reached the result")
	}
}

// TestServerInfoRelatedRepositoryKeys: server_info reports
// related.max_identifiers (default 20) and related.from_repo_config
// (default false), and an operator's values with their source (v2.5 spec
// V-13b §1.3 item 4).
func TestServerInfoRelatedRepositoryKeys(t *testing.T) {
	cfg, rep, err := load(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	r := ServerInfo(cfg, rep, nil)
	if v := r.Config.Values["related.max_identifiers"]; v.Value != 20 || v.Source != config.OriginDefault {
		t.Errorf("related.max_identifiers = %+v", v)
	}
	if v := r.Config.Values["related.from_repo_config"]; v.Value != false || v.Source != config.OriginDefault {
		t.Errorf("related.from_repo_config = %+v", v)
	}
	env := validEnv()
	env["REVIEW_MCP_RELATED_MAX_IDENTIFIERS"] = "50"
	env["REVIEW_MCP_RELATED_FROM_REPO_CONFIG"] = "true"
	if cfg, rep, err = load(env); err != nil {
		t.Fatal(err)
	}
	r = ServerInfo(cfg, rep, nil)
	if v := r.Config.Values["related.max_identifiers"]; v.Value != 50 || v.Source != config.OriginEnv {
		t.Errorf("related.max_identifiers = %+v", v)
	}
	if v := r.Config.Values["related.from_repo_config"]; v.Value != true || v.Source != config.OriginEnv {
		t.Errorf("related.from_repo_config = %+v", v)
	}
	md := RenderServerInfoMarkdown(r)
	if !strings.Contains(md, "`related.max_identifiers` = `50` (env)") || !strings.Contains(md, "`related.from_repo_config` = `true` (env)") {
		t.Errorf("markdown lacks the keys:\n%s", md)
	}
}
