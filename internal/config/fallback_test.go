package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fallbackFile is a config file with two fallback models.
const fallbackFile = `
[[llm.fallback]]
model = "second-model"

[[llm.fallback]]
model = "third-model"
base_url = "https://fallback.example/v1/"
context_window = 128000
max_output_tokens = 4096
reasoning_effort = "low"
timeout_seconds = 120
`

func withFile(env map[string]string, file string) MemSource {
	env[EnvConfig] = "/etc/review-mcp.toml"
	return memSrc(env, map[string]string{"/etc/review-mcp.toml": file})
}

// TestFallbackFromFileAndEnv (X-38, v2.1 spec V-6 §3.1): the file's entries
// in order; an environment entry N exists when its _MODEL is set, and every
// set variable of N replaces that field of the file's entry N; the key comes
// from the environment only.
func TestFallbackFromFileAndEnv(t *testing.T) {
	env := minimalEnv()
	env["REVIEW_MCP_LLM_FALLBACK_1_BASE_URL"] = "https://llm.example/v1"
	env["REVIEW_MCP_LLM_FALLBACK_2_MODEL"] = "third-model-b"
	env["REVIEW_MCP_LLM_FALLBACK_2_API_KEY"] = " sk-fallback-two "
	env["REVIEW_MCP_LLM_FALLBACK_3_MODEL"] = "fourth-model"
	env["REVIEW_MCP_LLM_FALLBACK_3_TIMEOUT_SECONDS"] = "30"
	cfg, rep := mustLoad(t, withFile(env, fallbackFile))
	fb := cfg.LLM.Fallback
	if len(fb) != 3 {
		t.Fatalf("fallbacks %+v", fb)
	}
	if fb[0].Model != "second-model" || fb[0].BaseURL != "https://llm.example/v1" {
		t.Errorf("entry 1 %+v", fb[0])
	}
	if fb[1].Model != "third-model-b" || fb[1].BaseURL != "https://fallback.example/v1" || fb[1].ContextWindow != 128000 ||
		*fb[1].MaxOutputTokens != 4096 || *fb[1].ReasoningEffort != "low" || fb[1].TimeoutSeconds != 120 ||
		fb[1].APIKey.Reveal() != "sk-fallback-two" {
		t.Errorf("entry 2 %+v", fb[1])
	}
	if fb[2].Model != "fourth-model" || fb[2].TimeoutSeconds != 30 || fb[2].BaseURL != "" {
		t.Errorf("entry 3 %+v", fb[2])
	}
	if rep.Sources[fallbackSection] != OriginEnv {
		t.Errorf("source %q", rep.Sources[fallbackSection])
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "FALLBACK") {
			t.Errorf("warning %q", w)
		}
	}
}

// TestFallbackEffective: unset fields take the primary's value, except the
// reasoning effort (unset) and the context window (resolved from the
// fallback's endpoint); temperature, seed, retries and limits are shared.
func TestFallbackEffective(t *testing.T) {
	env := minimalEnv()
	env["REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS"] = "2000"
	env["REVIEW_MCP_LLM_REASONING_EFFORT"] = "high"
	env["REVIEW_MCP_LLM_TEMPERATURE"] = "0.2"
	env["REVIEW_MCP_LLM_REQUESTS_PER_MINUTE"] = "10"
	cfg, _ := mustLoad(t, withFile(env, fallbackFile))
	first := cfg.EffectiveFallback(0)
	if first.Model != "second-model" || first.BaseURL != cfg.LLM.BaseURL || first.ContextWindow != 0 ||
		*first.MaxOutputTokens != 2000 || first.ReasoningEffort != nil || first.TimeoutSeconds != cfg.LLM.TimeoutSeconds ||
		*first.Temperature != 0.2 || first.RequestsPerMinute != 10 || first.Fallback != nil {
		t.Errorf("fallback 1 %+v", first)
	}
	second := cfg.EffectiveFallback(1)
	if second.BaseURL != "https://fallback.example/v1" || second.ContextWindow != 128000 || *second.MaxOutputTokens != 4096 ||
		*second.ReasoningEffort != "low" || second.TimeoutSeconds != 120 {
		t.Errorf("fallback 2 %+v", second)
	}
	// The primary is not changed through the copy.
	*first.MaxOutputTokens = 1
	if *cfg.LLM.MaxOutputTokens != 2000 {
		t.Error("EffectiveFallback aliases the primary's max_output_tokens")
	}
}

// TestFallbackKey [leak guard]: a fallback's own key wins; without one the
// primary key is sent only on the primary's base URL (after normalisation),
// and no key anywhere else.
func TestFallbackKey(t *testing.T) {
	env := minimalEnv()
	env["REVIEW_MCP_LLM_BASE_URL"] = "https://llm.example/v1"
	file := `
[[llm.fallback]]
model = "same-host"
base_url = "HTTPS://LLM.example:443/v1/"

[[llm.fallback]]
model = "other-host"
base_url = "https://fallback.example/v1"

[[llm.fallback]]
model = "own-key"
base_url = "https://fallback.example/v1"
`
	env["REVIEW_MCP_LLM_FALLBACK_3_API_KEY"] = "sk-own-key"
	cfg, _ := mustLoad(t, withFile(env, file))
	for i, want := range []struct {
		source, key string
	}{{KeyPrimary, env["REVIEW_MCP_LLM_API_KEY"]}, {KeyNone, ""}, {KeyOwn, "sk-own-key"}} {
		k, src := cfg.FallbackKey(i)
		if src != want.source || k.Reveal() != want.key {
			t.Errorf("fallback %d: key source %q, want %q", i+1, src, want.source)
		}
	}
	// The default base URL is the primary's.
	cfg.LLM.Fallback[1].BaseURL = ""
	if _, src := cfg.FallbackKey(1); src != KeyPrimary {
		t.Errorf("no base URL: key source %q", src)
	}
	cfg.LLM.Fallback[1].BaseURL = "https://llm.example/v2"
	if _, src := cfg.FallbackKey(1); src != KeyNone {
		t.Errorf("another path on the primary's host: key source %q", src)
	}
}

// TestFallbackProblems: gaps, too many entries, a key in the file, missing
// models, invalid fields, and variables of a fallback that does not exist.
func TestFallbackProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		file string
		want string
	}{
		{"gap", map[string]string{"REVIEW_MCP_LLM_FALLBACK_2_MODEL": "m"}, "",
			"REVIEW_MCP_LLM_FALLBACK_2_MODEL is set but fallback 1 is not configured; fallbacks are numbered from 1 without gaps"},
		{"field without a model", map[string]string{"REVIEW_MCP_LLM_FALLBACK_1_BASE_URL": "https://fallback.example/v1"}, "",
			"REVIEW_MCP_LLM_FALLBACK_1_BASE_URL is set but fallback 1 has no model; set REVIEW_MCP_LLM_FALLBACK_1_MODEL"},
		{"key without a model", map[string]string{"REVIEW_MCP_LLM_FALLBACK_1_API_KEY": "sk-x"}, "",
			"REVIEW_MCP_LLM_FALLBACK_1_API_KEY is set but fallback 1 has no model; set REVIEW_MCP_LLM_FALLBACK_1_MODEL"},
		{"too many", nil, strings.Repeat("[[llm.fallback]]\nmodel = \"m\"\n", 4),
			"llm.fallback: at most 3 fallback models may be configured (found 4)"},
		{"key in the file", nil, "[[llm.fallback]]\nmodel = \"m\"\napi_key = \"sk-file\"\n",
			`secret "llm.fallback.api_key" must not be set in the config file; use environment variable REVIEW_MCP_LLM_FALLBACK_<N>_API_KEY`},
		{"unknown field", nil, "[[llm.fallback]]\nmodel = \"m\"\ncolour = \"red\"\n", `unknown config key "llm.fallback.colour"`},
		{"no model in the file", nil, "[[llm.fallback]]\nbase_url = \"https://fallback.example/v1\"\n",
			"llm.fallback 1: model is required (REVIEW_MCP_LLM_FALLBACK_1_MODEL)"},
		{"bad URL", map[string]string{"REVIEW_MCP_LLM_FALLBACK_1_MODEL": "m", "REVIEW_MCP_LLM_FALLBACK_1_BASE_URL": "ftp://fallback.example"}, "",
			"llm.fallback 1 base_url: scheme must be http or https"},
		{"URL with credentials", map[string]string{"REVIEW_MCP_LLM_FALLBACK_1_MODEL": "m", "REVIEW_MCP_LLM_FALLBACK_1_BASE_URL": "https://u:p@fallback.example"}, "", //nolint:gosec // a placeholder URL the validation must refuse
			"credentials must not be embedded in llm.fallback 1 base_url"},
		{"small window", map[string]string{"REVIEW_MCP_LLM_FALLBACK_1_MODEL": "m", "REVIEW_MCP_LLM_FALLBACK_1_CONTEXT_WINDOW": "1000"}, "",
			"llm.fallback 1: context_window 1000 is below the minimum 4096"},
		{"output over window", map[string]string{"REVIEW_MCP_LLM_FALLBACK_1_MODEL": "m", "REVIEW_MCP_LLM_FALLBACK_1_CONTEXT_WINDOW": "8192",
			"REVIEW_MCP_LLM_FALLBACK_1_MAX_OUTPUT_TOKENS": "8192"}, "",
			"llm.fallback 1: max_output_tokens 8192 must be less than its context_window (8192)"},
		{"empty effort", nil, "[[llm.fallback]]\nmodel = \"m\"\nreasoning_effort = \" \"\n",
			"llm.fallback 1: reasoning_effort must not be empty when set"},
		{"timeout", map[string]string{"REVIEW_MCP_LLM_FALLBACK_1_MODEL": "m", "REVIEW_MCP_LLM_FALLBACK_1_TIMEOUT_SECONDS": "3601"}, "",
			"llm.fallback 1: timeout_seconds 3601 is out of range (1-3600)"},
		{"not a number", map[string]string{"REVIEW_MCP_LLM_FALLBACK_1_MODEL": "m", "REVIEW_MCP_LLM_FALLBACK_1_CONTEXT_WINDOW": "big"}, "",
			`REVIEW_MCP_LLM_FALLBACK_1_CONTEXT_WINDOW: invalid value "big" (expected a base-10 integer)`},
	} {
		env := minimalEnv()
		for k, v := range tc.env {
			env[k] = v
		}
		src := MemSource{Env: env}
		if tc.file != "" {
			src = withFile(env, tc.file)
		}
		_, _, err := Load(src)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v\nwant %q", tc.name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "sk-") {
			t.Errorf("%s: a key in the problem: %v", tc.name, err)
		}
	}
}

// TestFallbackUnknownEnv: a fourth fallback is an unknown variable.
func TestFallbackUnknownEnv(t *testing.T) {
	env := minimalEnv()
	env["REVIEW_MCP_LLM_FALLBACK_4_MODEL"] = "m"
	_, rep := mustLoad(t, MemSource{Env: env})
	if !slices.Contains(rep.Warnings, "unknown environment variable REVIEW_MCP_LLM_FALLBACK_4_MODEL (ignored)") {
		t.Errorf("warnings %q", rep.Warnings)
	}
}

// TestFallbackServeHeaderKey: in serve mode with serve.llm_key_source =
// header a server-side fallback key is refused.
func TestFallbackServeHeaderKey(t *testing.T) {
	env := serveEnv(map[string]string{"REVIEW_MCP_LLM_FALLBACK_1_MODEL": "m", "REVIEW_MCP_LLM_FALLBACK_1_API_KEY": "sk-operator"})
	_, _, err := LoadWith(memSrc(env, nil), LoadOptions{Mode: ModeServe})
	want := "serve mode with serve.llm_key_source = header takes the LLM API key from request headers; unset REVIEW_MCP_LLM_FALLBACK_1_API_KEY"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v", err)
	}
}

// TestFallbackSummary: the summary lists each fallback's model, redacted
// base URL, key source and configured window, and never a key.
func TestFallbackSummary(t *testing.T) {
	env := minimalEnv()
	env["REVIEW_MCP_LLM_FALLBACK_2_API_KEY"] = "sk-summary-secret"
	cfg, rep := mustLoad(t, withFile(env, fallbackFile))
	sum := cfg.Summary(rep)
	got := fmt.Sprintf("%+v", sum.Fallbacks)
	want := []FallbackSummary{
		{Model: "second-model", BaseURL: cfg.LLM.BaseURL, Key: KeyPrimary},
		{Model: "third-model", BaseURL: "https://fallback.example/v1", Key: KeyOwn, ContextWindow: 128000},
	}
	if len(sum.Fallbacks) != 2 || sum.Fallbacks[0] != want[0] || sum.Fallbacks[1] != want[1] {
		t.Errorf("fallbacks %s", got)
	}
	b, _ := json.Marshal(sum)
	if strings.Contains(string(b), "sk-summary-secret") || strings.Contains(fmt.Sprintf("%+v %#v", cfg, cfg), "sk-summary-secret") {
		t.Error("a fallback key is in the summary or the printed configuration")
	}
}
