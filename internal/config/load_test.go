package config

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDefaultsAppliedWhenNothingSet(t *testing.T) {
	cfg, rep := mustLoad(t, MemSource{Env: minimalEnv()})

	// Golden values: the "Default" column of §5, written out independently.
	checks := []struct {
		name      string
		got, want any
	}{
		{"llm.timeout_seconds", cfg.LLM.TimeoutSeconds, 120},
		{"llm.max_retries", cfg.LLM.MaxRetries, 1},
		{"llm.token_estimate_factor", cfg.LLM.TokenEstimateFactor, 0.3},
		{"gitea.insecure_skip_verify", cfg.Gitea.InsecureSkipVerify, false},
		{"bitbucket_server.insecure_skip_verify", cfg.BitbucketServer.InsecureSkipVerify, false},
		{"output.language", cfg.Output.Language, "en-US"},
		{"diff.extra_lines_before", cfg.Diff.ExtraLinesBefore, 5},
		{"diff.extra_lines_after", cfg.Diff.ExtraLinesAfter, 1},
		{"diff.skip_extend_extensions", cfg.Diff.SkipExtendExtensions, []string{".md", ".txt"}},
		{"diff.large_patch_policy", cfg.Diff.LargePatchPolicy, "clip"},
		{"diff.max_description_tokens", cfg.Diff.MaxDescriptionTokens, 500},
		{"diff.max_commits_tokens", cfg.Diff.MaxCommitsTokens, 500},
		{"diff.max_files_full_content", cfg.Diff.MaxFilesFullContent, 50},
		{"diff.max_file_bytes", cfg.Diff.MaxFileBytes, 1048576},
		{"diff.max_diff_bytes", cfg.Diff.MaxDiffBytes, 20971520},
		{"diff.ignore_generated_frameworks", cfg.Diff.IgnoreGeneratedFrameworks, []string{}},
		{"ignore.glob", cfg.Ignore.Glob, []string{"vendor/**"}},
		{"ignore.regex", cfg.Ignore.Regex, []string{}},
		{"review.max_findings", cfg.Review.MaxFindings, 3},
		{"review.require_tests", cfg.Review.RequireTests, true},
		{"review.require_security", cfg.Review.RequireSecurity, true},
		{"review.require_effort_estimate", cfg.Review.RequireEffortEstimate, true},
		{"review.extra_instructions", cfg.Review.ExtraInstructions, ""},
		{"ask.extra_instructions", cfg.Ask.ExtraInstructions, ""},
		{"log.level", cfg.Log.Level, "info"},
		// DQ-26: no defaults for these.
		{"llm.max_output_tokens", cfg.LLM.MaxOutputTokens == nil, true},
		{"llm.temperature", cfg.LLM.Temperature == nil, true},
		{"llm.seed", cfg.LLM.Seed == nil, true},
		{"llm.reasoning_effort", cfg.LLM.ReasoningEffort == nil, true},
		{"bitbucket_server.base_url", cfg.BitbucketServer.BaseURL, ""},
		{"gitea.web_url", cfg.Gitea.WebURL, ""},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.name, c.got, c.want)
		}
	}
	for key, src := range rep.Sources {
		switch key {
		case "llm.base_url", "llm.model", "llm.context_window", "gitea.base_url":
			if src != OriginEnv {
				t.Errorf("source[%s] = %s, want env", key, src)
			}
		case "llm.api_key", "gitea.token":
			if src != OriginEnv {
				t.Errorf("source[%s] = %s, want env", key, src)
			}
		default:
			if src != OriginDefault {
				t.Errorf("source[%s] = %s, want default", key, src)
			}
		}
	}
}

func TestRequiredMissingListsAllInOneError(t *testing.T) {
	cfg, rep, err := Load(MemSource{Env: map[string]string{}})
	if cfg == nil || rep == nil {
		t.Fatal("Load must return a best-effort Config and Report with the error")
	}
	probs := problemsOf(t, err)
	for _, want := range []string{
		"llm.base_url is required",
		"llm.model is required",
		"llm.context_window is required",
		"REVIEW_MCP_LLM_API_KEY is required",
		"no provider enabled",
	} {
		if !hasProblem(probs, want) {
			t.Errorf("missing problem %q in %v", want, probs)
		}
	}
	if len(probs) != 5 {
		t.Errorf("got %d problems, want exactly 5: %v", len(probs), probs)
	}
	if !strings.Contains(err.Error(), "llm.model is required") || !strings.Contains(err.Error(), "no provider enabled") {
		t.Errorf("Error() must join all problems: %v", err)
	}
	if cfg.Log.Level != "info" {
		t.Error("best-effort config must still carry defaults")
	}
}

func TestPrecedenceDefaultFileEnv(t *testing.T) {
	file := `
[diff]
extra_lines_before = 2
extra_lines_after = 3
[log]
level = "debug"
[review]
max_findings = 7
[llm]
temperature = 1
`
	env := envWith(map[string]string{
		"REVIEW_MCP_CONFIG":                 "/etc/review/review.toml",
		"REVIEW_MCP_LOG_LEVEL":              "warn",
		"REVIEW_MCP_DIFF_EXTRA_LINES_AFTER": "4",
		"REVIEW_MCP_REVIEW_REQUIRE_TESTS":   "0",
		"REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS":  "2000",
	})
	cfg, rep := mustLoad(t, memSrc(env, map[string]string{"/etc/review/review.toml": file}))

	want := map[string]struct {
		got any
		src Origin
	}{
		"diff.extra_lines_before": {cfg.Diff.ExtraLinesBefore, OriginFile},
		"diff.extra_lines_after":  {cfg.Diff.ExtraLinesAfter, OriginEnv}, // env beats file
		"log.level":               {cfg.Log.Level, OriginEnv},            // env beats file
		"review.max_findings":     {cfg.Review.MaxFindings, OriginFile},
		"review.require_tests":    {cfg.Review.RequireTests, OriginEnv},
		"review.require_security": {cfg.Review.RequireSecurity, OriginDefault},
	}
	wantVal := map[string]any{
		"diff.extra_lines_before": 2, "diff.extra_lines_after": 4, "log.level": "warn",
		"review.max_findings": 7, "review.require_tests": false, "review.require_security": true,
	}
	for k, w := range want {
		if !reflect.DeepEqual(w.got, wantVal[k]) {
			t.Errorf("%s = %v, want %v", k, w.got, wantVal[k])
		}
		if rep.Sources[k] != w.src {
			t.Errorf("source[%s] = %s, want %s", k, rep.Sources[k], w.src)
		}
	}
	if cfg.LLM.Temperature == nil || *cfg.LLM.Temperature != 1 || rep.Sources["llm.temperature"] != OriginFile {
		t.Errorf("integer literal for a float key should load from file; got %v (%s)", cfg.LLM.Temperature, rep.Sources["llm.temperature"])
	}
	if cfg.LLM.MaxOutputTokens == nil || *cfg.LLM.MaxOutputTokens != 2000 {
		t.Error("env optional value not applied")
	}
}

func TestFilePartialLeavesDefaultsIntact(t *testing.T) {
	file := "[ignore]\nglob = [\"dist/**\"]\n"
	cfg, rep := mustLoad(t, memSrc(envWith(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"}), map[string]string{"/c.toml": file}))
	if !reflect.DeepEqual(cfg.Ignore.Glob, []string{"dist/**"}) {
		t.Errorf("glob = %v", cfg.Ignore.Glob)
	}
	if cfg.Diff.ExtraLinesBefore != 5 || cfg.Review.MaxFindings != 3 || cfg.LLM.TimeoutSeconds != 120 {
		t.Error("untouched keys lost their defaults")
	}
	if rep.Sources["diff.extra_lines_before"] != OriginDefault {
		t.Error("untouched key must report default")
	}
}

func TestFileEmptyListOverridesDefault(t *testing.T) {
	file := "[diff]\nskip_extend_extensions = []\n"
	cfg, _ := mustLoad(t, memSrc(envWith(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"}), map[string]string{"/c.toml": file}))
	if len(cfg.Diff.SkipExtendExtensions) != 0 {
		t.Errorf("explicit empty list must override the default, got %v", cfg.Diff.SkipExtendExtensions)
	}
}

func TestFileErrors(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		path  string
		want  []string
	}{
		{"missing file", nil, "/nope.toml", []string{`cannot read config file "/nope.toml"`}},
		{"unknown key", map[string]string{"/c.toml": "[llm]\nbase_urll = \"x\"\n"}, "/c.toml", []string{`unknown config key "llm.base_urll" in /c.toml`}},
		{"unknown section reported once", map[string]string{"/c.toml": "[nosuch]\na = 1\nb = 2\n"}, "/c.toml", []string{`unknown config key "nosuch" in /c.toml`}},
		{"syntax error", map[string]string{"/c.toml": "[llm\n"}, "/c.toml", []string{"syntax error at line"}},
		{"type mismatch", map[string]string{"/c.toml": "[llm]\ncontext_window = \"big\"\n"}, "/c.toml", []string{`config file "/c.toml"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envWith(map[string]string{"REVIEW_MCP_CONFIG": tc.path})
			_, _, err := Load(memSrc(env, tc.files))
			probs := problemsOf(t, err)
			for _, w := range tc.want {
				if !hasProblem(probs, w) {
					t.Errorf("want problem containing %q, got %v", w, probs)
				}
			}
		})
	}

	_, _, err := Load(memSrc(envWith(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"}), map[string]string{"/c.toml": "[nosuch]\na = 1\nb = 2\n"}))
	if n := strings.Count(err.Error(), "unknown config key"); n != 1 {
		t.Errorf("unknown section should be reported once, got %d", n)
	}
}

func TestEmptyConfigEnvIsIgnored(t *testing.T) {
	mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_CONFIG": ""})})
}

// TestSecretInFileRejectedWithoutValue is a canary: a secret key in the file is
// a hard error that names the env var and never contains the value. [canary]
func TestSecretInFileRejectedWithoutValue(t *testing.T) {
	cases := []struct {
		toml    string
		key     string
		envName string
	}{
		{"[llm]\napi_key = \"" + fakeLLMKey + "\"\n", "llm.api_key", "REVIEW_MCP_LLM_API_KEY"},
		{"[gitea]\ntoken = \"" + fakeGitea + "\"\n", "gitea.token", "REVIEW_MCP_GITEA_TOKEN"},
		{"[bitbucket_server]\ntoken = \"" + fakeBitbkt + "\"\n", "bitbucket_server.token", "REVIEW_MCP_BITBUCKET_SERVER_TOKEN"},
		{"[other]\nPassword = \"" + fakeURLUserinfo + "\"\n", "other.Password", ""},
		{"[llm]\nApiKey = \"" + fakeURLUserinfo + "\"\n", "llm.ApiKey", ""},
		{"[x]\nsecret = \"" + fakeURLUserinfo + "\"\n", "x.secret", ""},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			env := envWith(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"})
			cfg, rep, err := Load(memSrc(env, map[string]string{"/c.toml": tc.toml}))
			probs := problemsOf(t, err)
			want := `secret "` + tc.key + `" must not be set in the config file`
			if !hasProblem(probs, want) {
				t.Fatalf("want %q in %v", want, probs)
			}
			if tc.envName != "" && !hasProblem(probs, "use environment variable "+tc.envName) {
				t.Errorf("must name %s: %v", tc.envName, probs)
			}
			assertNoSecrets(t, "error", err.Error())
			assertNoSecrets(t, "warnings", strings.Join(rep.Warnings, "\n"))
			// The rejected file must not have leaked a value into the config.
			if cfg.Secrets.LLMAPIKey.Reveal() != fakeLLMKey { // env value stays
				t.Error("env secret must be unaffected by a rejected file")
			}
		})
	}
}

func TestUnknownEnvVariableIsWarningNotError(t *testing.T) {
	env := envWith(map[string]string{
		"REVIEW_MCP_LLM_BASEURL": "typo",
		"REVIEW_MCP_BOGUS":       "x",
		"OTHER_VAR":              "ignored",
	})
	_, rep := mustLoad(t, MemSource{Env: env})
	for _, name := range []string{"REVIEW_MCP_LLM_BASEURL", "REVIEW_MCP_BOGUS"} {
		want := "unknown environment variable " + name + " (ignored)"
		found := false
		for _, w := range rep.Warnings {
			if w == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing warning %q in %v", want, rep.Warnings)
		}
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "OTHER_VAR") || strings.Contains(w, "typo") {
			t.Errorf("unexpected content in warning %q", w)
		}
	}
}

func TestKnownEnvVariablesDoNotWarn(t *testing.T) {
	env := minimalEnv()
	env["REVIEW_MCP_CONFIG"] = ""
	_, rep := mustLoad(t, MemSource{Env: env})
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings: %v", rep.Warnings)
	}
}

func TestEnvParsingStrict(t *testing.T) {
	good := []struct {
		env, val string
		check    func(*Config) bool
	}{
		{"REVIEW_MCP_REVIEW_REQUIRE_TESTS", "FALSE", func(c *Config) bool { return !c.Review.RequireTests }},
		{"REVIEW_MCP_REVIEW_REQUIRE_TESTS", "0", func(c *Config) bool { return !c.Review.RequireTests }},
		{"REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY", "True", func(c *Config) bool { return c.Gitea.InsecureSkipVerify }},
		{"REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY", "1", func(c *Config) bool { return c.Gitea.InsecureSkipVerify }},
		{"REVIEW_MCP_LLM_SEED", "-5", func(c *Config) bool { return *c.LLM.Seed == -5 }},
		{"REVIEW_MCP_LLM_TEMPERATURE", "0", func(c *Config) bool { return *c.LLM.Temperature == 0 }},
		{"REVIEW_MCP_DIFF_SKIP_EXTEND_EXTENSIONS", " .md , .rst ", func(c *Config) bool {
			return reflect.DeepEqual(c.Diff.SkipExtendExtensions, []string{".md", ".rst"})
		}},
		{"REVIEW_MCP_IGNORE_GLOB", "", func(c *Config) bool { return c.Ignore.Glob != nil && len(c.Ignore.Glob) == 0 }},
		{"REVIEW_MCP_IGNORE_REGEX", "a,b", func(c *Config) bool { return reflect.DeepEqual(c.Ignore.Regex, []string{"a", "b"}) }},
	}
	for _, g := range good {
		cfg, _ := mustLoad(t, MemSource{Env: envWith(map[string]string{g.env: g.val})})
		if !g.check(cfg) {
			t.Errorf("%s=%q not parsed as expected", g.env, g.val)
		}
	}

	bad := []struct {
		env, val, want string
	}{
		{"REVIEW_MCP_LLM_CONTEXT_WINDOW", "32k", "REVIEW_MCP_LLM_CONTEXT_WINDOW: invalid value \"32k\" (expected a base-10 integer)"},
		{"REVIEW_MCP_LLM_CONTEXT_WINDOW", "0x20", "REVIEW_MCP_LLM_CONTEXT_WINDOW: invalid value"},
		{"REVIEW_MCP_LLM_TEMPERATURE", "warm", "REVIEW_MCP_LLM_TEMPERATURE: invalid value \"warm\" (expected a decimal number)"},
		{"REVIEW_MCP_LLM_SEED", "1.5", "REVIEW_MCP_LLM_SEED: invalid value"},
		{"REVIEW_MCP_REVIEW_REQUIRE_TESTS", "yes", "REVIEW_MCP_REVIEW_REQUIRE_TESTS: invalid value \"yes\" (expected a boolean"},
		{"REVIEW_MCP_LLM_MODEL", "", "REVIEW_MCP_LLM_MODEL is set but empty"},
		{"REVIEW_MCP_LLM_MAX_RETRIES", "", "REVIEW_MCP_LLM_MAX_RETRIES is set but empty"},
		{"REVIEW_MCP_LLM_TEMPERATURE", "", "REVIEW_MCP_LLM_TEMPERATURE is set but empty"},
		{"REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY", "", "is set but empty"},
		{"REVIEW_MCP_DIFF_SKIP_EXTEND_EXTENSIONS", ".md,,.txt", "REVIEW_MCP_DIFF_SKIP_EXTEND_EXTENSIONS: empty item at position 2"},
		{"REVIEW_MCP_IGNORE_GLOB", "a,", "empty item at position 2"},
	}
	for _, b := range bad {
		_, _, err := Load(MemSource{Env: envWith(map[string]string{b.env: b.val})})
		probs := problemsOf(t, err)
		if !hasProblem(probs, b.want) {
			t.Errorf("%s=%q: want %q in %v", b.env, b.val, b.want, probs)
		}
		if len(probs) != 1 {
			t.Errorf("%s=%q: a malformed value should yield exactly one problem, got %v", b.env, b.val, probs)
		}
	}
}

func TestSecretEnvEmptyIsUnsetAndTrimmed(t *testing.T) {
	_, _, err := Load(MemSource{Env: envWith(map[string]string{"REVIEW_MCP_LLM_API_KEY": "  "})})
	if !hasProblem(problemsOf(t, err), "REVIEW_MCP_LLM_API_KEY is required") {
		t.Error("blank key must count as missing")
	}
	cfg, _ := mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_LLM_API_KEY": " " + fakeLLMKey + "\n"})})
	if cfg.Secrets.LLMAPIKey.Reveal() != fakeLLMKey {
		t.Error("surrounding whitespace must be trimmed")
	}
}

func TestAllProblemsAggregated(t *testing.T) {
	env := envWith(map[string]string{
		"REVIEW_MCP_CONFIG":                 "/c.toml",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW":     "abc",
		"REVIEW_MCP_DIFF_EXTRA_LINES_AFTER": "50",
		"REVIEW_MCP_LOG_LEVEL":              "loud",
	})
	_, _, err := Load(memSrc(env, map[string]string{"/c.toml": "[nosuch]\nx = 1\n[llm]\napi_key = \"" + fakeLLMKey + "\"\n"}))
	probs := problemsOf(t, err)
	for _, w := range []string{"unknown config key", "secret \"llm.api_key\"", "REVIEW_MCP_LLM_CONTEXT_WINDOW", "diff.extra_lines_after", "log.level"} {
		if !hasProblem(probs, w) {
			t.Errorf("missing %q in %v", w, probs)
		}
	}
}

func TestBestEffortConfigCarriesValidValues(t *testing.T) {
	cfg, rep, err := Load(MemSource{Env: envWith(map[string]string{"REVIEW_MCP_DIFF_MAX_FILE_BYTES": "5"})})
	if err == nil || cfg == nil || rep == nil {
		t.Fatal("expected error with config and report")
	}
	if cfg.LLM.Model != "example-model" || cfg.Gitea.BaseURL != "https://your-gitea.example" {
		t.Error("valid values must remain in the best-effort config")
	}
}

func TestMemSourceFallbacks(t *testing.T) {
	var m MemSource
	if _, err := m.ReadFile("/x"); err == nil {
		t.Error("expected error")
	}
	if _, err := m.Stat("/x"); err == nil {
		t.Error("expected error")
	}
	_ = fstest.MapFS{}
}

// TestFileDecodeErrorsNeverEchoValues is a canary: TOML decode failures must
// not relay upstream message text, which can echo file values. [canary]
func TestFileDecodeErrorsNeverEchoValues(t *testing.T) {
	cases := []struct {
		name, toml, want string
	}{
		{"bareword ghp", "[llm]\napi_key = ghp_SUPERSECRET123\n", "syntax error at line 2"},
		{"bareword sk", "[llm]\napi_key = sk-SUPERSECRET123\n", "syntax error at line 2"},
		{"unterminated string", "[llm]\napi_key = \"SUPERSECRET123\n", "syntax error at line 2"},
		{"type mismatch", "[llm]\ncontext_window = \"SUPERSECRET123\"\n", "invalid value type for key \"llm.context_window\""},
		{"unterminated array", "[diff]\nskip_extend_extensions = [\".md\", \"SUPERSECRET123\"\n", "syntax error at line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envWith(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"})
			_, _, err := Load(memSrc(env, map[string]string{"/c.toml": tc.toml}))
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("want %q in %q", tc.want, msg)
			}
			for _, bad := range []string{"SUPERSECRET123", "ghp_", "sk"} {
				if strings.Contains(msg, bad) {
					t.Errorf("error echoes %q: %s", bad, msg)
				}
			}
		})
	}
}
