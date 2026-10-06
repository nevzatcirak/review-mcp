package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretPlaceholders(t *testing.T) {
	set, unset := NewSecret(fakeLLMKey), Secret{}
	if !set.IsSet() || unset.IsSet() || NewSecret("").IsSet() {
		t.Error("IsSet wrong")
	}
	if set.Reveal() != fakeLLMKey || unset.Reveal() != "" {
		t.Error("Reveal wrong")
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%10s", "%-12v"} {
		if got := strings.TrimSpace(fmt.Sprintf(verb, set)); got != "[REDACTED]" {
			t.Errorf("set %s = %q", verb, got)
		}
		if got := strings.TrimSpace(fmt.Sprintf(verb, unset)); got != "[UNSET]" {
			t.Errorf("unset %s = %q", verb, got)
		}
	}
	if set.String() != "[REDACTED]" || set.GoString() != "[REDACTED]" || unset.String() != "[UNSET]" {
		t.Error("String/GoString wrong")
	}
	b, err := set.MarshalText()
	if err != nil || string(b) != "[REDACTED]" {
		t.Errorf("MarshalText = %q, %v", b, err)
	}
	b, err = unset.MarshalJSON()
	if err != nil || string(b) != `"[UNSET]"` {
		t.Errorf("MarshalJSON = %q, %v", b, err)
	}
}

// TestSecretNeverAppearsInAnyOutput is a canary: formatted, logged and
// serialized forms of the whole Config and the Summary contain no secret. [canary]
func TestSecretNeverAppearsInAnyOutput(t *testing.T) {
	env := envWith(map[string]string{
		"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com/stash",
		"REVIEW_MCP_BITBUCKET_SERVER_TOKEN":    fakeBitbkt,
	})
	cfg, rep := mustLoad(t, MemSource{Env: env})
	sum := cfg.Summary(rep)

	outputs := map[string]string{}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		outputs["fmt Config "+verb] = fmt.Sprintf(verb, cfg)
		outputs["fmt *Config "+verb] = fmt.Sprintf(verb, *cfg)
		outputs["fmt Secrets "+verb] = fmt.Sprintf(verb, cfg.Secrets)
		outputs["fmt Secret "+verb] = fmt.Sprintf(verb, cfg.Secrets.LLMAPIKey)
		outputs["fmt Summary "+verb] = fmt.Sprintf(verb, sum)
	}
	outputs["Sprint"] = fmt.Sprint(cfg.Secrets, cfg.Secrets.GiteaToken)
	outputs["Errorf"] = fmt.Errorf("failed with %v and %w", cfg.Secrets.LLMAPIKey, fmt.Errorf("%s", cfg.Secrets.GiteaToken)).Error()

	for name, handler := range map[string]func(*bytes.Buffer) slog.Handler{
		"slog text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"slog json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		var buf bytes.Buffer
		log := slog.New(handler(&buf))
		log.Info("loaded",
			"key", cfg.Secrets.LLMAPIKey,
			"secrets", cfg.Secrets,
			slog.Any("any", cfg.Secrets.GiteaToken),
			slog.Group("g", "tok", cfg.Secrets.BitbucketServerToken),
			"config", cfg,
		)
		outputs[name] = buf.String()
	}

	for name, v := range map[string]any{"json Config": cfg, "json *Config": *cfg, "json Secrets": cfg.Secrets, "json Summary": sum} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		outputs[name] = string(b)
		b, err = json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		outputs[name+" indent"] = string(b)
	}

	for name, out := range outputs {
		if out == "" {
			t.Errorf("%s produced no output", name)
		}
		assertNoSecrets(t, name, out)
	}
	if !strings.Contains(outputs["json Config"], `"llm_api_key":"[REDACTED]"`) {
		t.Errorf("Config JSON should show the placeholder: %s", outputs["json Config"])
	}
}

func TestSummaryContentAndSources(t *testing.T) {
	file := "[review]\nmax_findings = 9\n"
	env := envWith(map[string]string{
		"REVIEW_MCP_CONFIG":                     "/c.toml",
		"REVIEW_MCP_LLM_BASE_URL":               "https://llm.example.com/v1?api-version=secret-ish",
		"REVIEW_MCP_LLM_TEMPERATURE":            "0.2",
		"REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY": "true",
		"REVIEW_MCP_NOPE":                       "x",
	})
	cfg, rep := mustLoad(t, memSrc(env, map[string]string{"/c.toml": file}))
	sum := cfg.Summary(rep)

	if len(sum.Values) != len(table) {
		t.Errorf("Summary has %d values, want %d (every non-secret key)", len(sum.Values), len(table))
	}
	if v := sum.Values["review.max_findings"]; v.Value != 9 || v.Source != OriginFile {
		t.Errorf("review.max_findings = %+v", v)
	}
	if v := sum.Values["llm.temperature"]; v.Value != 0.2 || v.Source != OriginEnv {
		t.Errorf("llm.temperature = %+v", v)
	}
	if v := sum.Values["llm.seed"]; v.Value != nil || v.Source != OriginDefault {
		t.Errorf("unset optional should be nil/default: %+v", v)
	}
	if v := sum.Values["llm.base_url"]; v.Value != "https://llm.example.com/v1?api-version=REDACTED" {
		t.Errorf("URL must be redacted: %+v", v)
	}
	if sum.Secrets["llm.api_key"] != "set" || sum.Secrets["gitea.token"] != "set" || sum.Secrets["bitbucket_server.token"] != "unset" {
		t.Errorf("secrets: %v", sum.Secrets)
	}
	if len(sum.Providers) != 1 || sum.Providers[0] != (ProviderSummary{Kind: "gitea", BaseURL: "https://your-gitea.example"}) {
		t.Errorf("providers: %+v", sum.Providers)
	}
	joined := strings.Join(sum.Warnings, "|")
	if !strings.Contains(joined, "unknown environment variable REVIEW_MCP_NOPE") || !strings.Contains(joined, "TLS verification disabled for gitea") {
		t.Errorf("warnings: %v", sum.Warnings)
	}

	b, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-ish") {
		t.Errorf("query value leaked in summary: %s", b)
	}
	assertNoSecrets(t, "summary json", string(b))

	// The source slice in the summary must not alias the config.
	sum.Values["diff.skip_extend_extensions"].Value.([]string)[0] = "mutated"
	if cfg.Diff.SkipExtendExtensions[0] != ".md" {
		t.Error("Summary aliases config slices")
	}
}

func TestSummaryNilReport(t *testing.T) {
	s := Defaults().Summary(nil)
	if s.Values["log.level"].Source != OriginDefault || s.Secrets["llm.api_key"] != "unset" {
		t.Errorf("%+v", s)
	}
}
