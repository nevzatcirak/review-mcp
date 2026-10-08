package config

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestValidationRules(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string // substring of one problem
	}{
		{"context window too small", map[string]string{"REVIEW_MCP_LLM_CONTEXT_WINDOW": "4095"}, "llm.context_window: 4095 is below the minimum 4096"},
		{"context window negative", map[string]string{"REVIEW_MCP_LLM_CONTEXT_WINDOW": "-1"}, "llm.context_window: -1 is below the minimum 4096"},
		{"max output zero", map[string]string{"REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS": "0"}, "llm.max_output_tokens: 0 must be greater than 0"},
		{"max output >= window", map[string]string{"REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS": "32000"}, "must be less than llm.context_window"},
		{"temperature high", map[string]string{"REVIEW_MCP_LLM_TEMPERATURE": "2.1"}, "llm.temperature: 2.1 is out of range (0-2)"},
		{"temperature negative", map[string]string{"REVIEW_MCP_LLM_TEMPERATURE": "-0.1"}, "llm.temperature"},
		{"temperature NaN", map[string]string{"REVIEW_MCP_LLM_TEMPERATURE": "NaN"}, "llm.temperature"},
		{"timeout zero", map[string]string{"REVIEW_MCP_LLM_TIMEOUT_SECONDS": "0"}, "llm.timeout_seconds: 0 is out of range (1-3600)"},
		{"timeout high", map[string]string{"REVIEW_MCP_LLM_TIMEOUT_SECONDS": "3601"}, "llm.timeout_seconds"},
		{"diff max tokens below minimum", map[string]string{"REVIEW_MCP_DIFF_MAX_TOKENS": "999"}, "diff.max_tokens: 999 is below the minimum 1000"},
		{"diff max tokens negative", map[string]string{"REVIEW_MCP_DIFF_MAX_TOKENS": "-5"}, "diff.max_tokens: -5 is below the minimum 1000"},
		{"retries high", map[string]string{"REVIEW_MCP_LLM_MAX_RETRIES": "6"}, "llm.max_retries: 6 is out of range (0-5)"},
		{"retries negative", map[string]string{"REVIEW_MCP_LLM_MAX_RETRIES": "-1"}, "llm.max_retries"},
		{"wait negative", map[string]string{"REVIEW_MCP_LLM_WAIT_SECONDS": "-1"}, "llm.wait_seconds: -1 is out of range (0-600)"},
		{"wait high", map[string]string{"REVIEW_MCP_LLM_WAIT_SECONDS": "601"}, "llm.wait_seconds: 601 is out of range (0-600)"},
		{"estimate factor high", map[string]string{"REVIEW_MCP_LLM_TOKEN_ESTIMATE_FACTOR": "2.5"}, "llm.token_estimate_factor"},
		{"locale bad", map[string]string{"REVIEW_MCP_OUTPUT_LANGUAGE": "english_US"}, "output.language"},
		{"locale too short", map[string]string{"REVIEW_MCP_OUTPUT_LANGUAGE": "e"}, "output.language"},
		{"extra before high is an error not a clamp", map[string]string{"REVIEW_MCP_DIFF_EXTRA_LINES_BEFORE": "11"}, "diff.extra_lines_before: 11 is out of range (0-10)"},
		{"extra after negative", map[string]string{"REVIEW_MCP_DIFF_EXTRA_LINES_AFTER": "-1"}, "diff.extra_lines_after"},
		{"extension without dot", map[string]string{"REVIEW_MCP_DIFF_SKIP_EXTEND_EXTENSIONS": ".md,txt"}, "diff.skip_extend_extensions[1]"},
		{"large patch policy", map[string]string{"REVIEW_MCP_DIFF_LARGE_PATCH_POLICY": "truncate"}, "diff.large_patch_policy"},
		{"description tokens", map[string]string{"REVIEW_MCP_DIFF_MAX_DESCRIPTION_TOKENS": "0"}, "diff.max_description_tokens"},
		{"commits tokens", map[string]string{"REVIEW_MCP_DIFF_MAX_COMMITS_TOKENS": "0"}, "diff.max_commits_tokens"},
		{"files full content", map[string]string{"REVIEW_MCP_DIFF_MAX_FILES_FULL_CONTENT": "0"}, "diff.max_files_full_content"},
		{"max file bytes", map[string]string{"REVIEW_MCP_DIFF_MAX_FILE_BYTES": "1023"}, "diff.max_file_bytes"},
		{"diff bytes below file bytes", map[string]string{"REVIEW_MCP_DIFF_MAX_DIFF_BYTES": "1024"}, "diff.max_diff_bytes: 1024 must be at least diff.max_file_bytes"},
		{"regex invalid", map[string]string{"REVIEW_MCP_IGNORE_REGEX": "ok,(unclosed"}, "ignore.regex[1]: pattern does not compile"},
		{"regex lookahead unsupported by RE2", map[string]string{"REVIEW_MCP_IGNORE_REGEX": "(?=x)"}, "ignore.regex[0]"},
		{"glob malformed", map[string]string{"REVIEW_MCP_IGNORE_GLOB": "ok/**,a/[b"}, "ignore.glob[1]: not a valid glob pattern"},
		{"framework unknown", map[string]string{"REVIEW_MCP_DIFF_IGNORE_GENERATED_FRAMEWORKS": "protobuf,nosuchfw"}, "diff.ignore_generated_frameworks[1]: unknown framework \"nosuchfw\" (valid names: go_gen, graphql,"},
		{"max findings low", map[string]string{"REVIEW_MCP_REVIEW_MAX_FINDINGS": "0"}, "review.max_findings: 0 is out of range (1-20)"},
		{"max findings high", map[string]string{"REVIEW_MCP_REVIEW_MAX_FINDINGS": "21"}, "review.max_findings"},
		{"max chunks low", map[string]string{"REVIEW_MCP_REVIEW_MAX_CHUNKS": "0"}, "review.max_chunks: 0 is out of range (1-32)"},
		{"max chunks high", map[string]string{"REVIEW_MCP_REVIEW_MAX_CHUNKS": "33"}, "review.max_chunks: 33 is out of range (1-32)"},
		{"max total findings low", map[string]string{"REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS": "0"}, "review.max_total_findings: 0 is out of range (1-50)"},
		{"max total findings high", map[string]string{"REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS": "51"}, "review.max_total_findings: 51 is out of range (1-50)"},
		{"max total findings below max findings", map[string]string{"REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS": "4", "REVIEW_MCP_REVIEW_MAX_FINDINGS": "5"},
			"review.max_total_findings: 4 must be at least review.max_findings (5)"},
		{"log level", map[string]string{"REVIEW_MCP_LOG_LEVEL": "loud"}, "log.level: invalid log level"},
		{"repo cache dir relative", map[string]string{"REVIEW_MCP_CONTEXT_REPO_CACHE_DIR": "cache/repos"}, `context.repo.cache_dir: "cache/repos" must be an absolute path`},
		{"repo idle days zero", map[string]string{"REVIEW_MCP_CONTEXT_REPO_IDLE_DAYS": "0"}, "context.repo.idle_days: 0 is out of range (1-365)"},
		{"repo idle days high", map[string]string{"REVIEW_MCP_CONTEXT_REPO_IDLE_DAYS": "366"}, "context.repo.idle_days: 366 is out of range (1-365)"},
		{"repo fetch timeout zero", map[string]string{"REVIEW_MCP_CONTEXT_REPO_FETCH_TIMEOUT_SECONDS": "0"}, "context.repo.fetch_timeout_seconds: 0 is out of range (1-600)"},
		{"repo fetch timeout high", map[string]string{"REVIEW_MCP_CONTEXT_REPO_FETCH_TIMEOUT_SECONDS": "601"}, "context.repo.fetch_timeout_seconds: 601 is out of range (1-600)"},
		{"repo max symbols zero", map[string]string{"REVIEW_MCP_CONTEXT_REPO_MAX_SYMBOLS": "0"}, "context.repo.max_symbols: 0 is out of range (1-50)"},
		{"repo max symbols high", map[string]string{"REVIEW_MCP_CONTEXT_REPO_MAX_SYMBOLS": "51"}, "context.repo.max_symbols: 51 is out of range (1-50)"},
		{"repo max hits zero", map[string]string{"REVIEW_MCP_CONTEXT_REPO_MAX_HITS_PER_SYMBOL": "0"}, "context.repo.max_hits_per_symbol: 0 is out of range (1-20)"},
		{"repo max hits high", map[string]string{"REVIEW_MCP_CONTEXT_REPO_MAX_HITS_PER_SYMBOL": "21"}, "context.repo.max_hits_per_symbol: 21 is out of range (1-20)"},
		{"repo max tokens low", map[string]string{"REVIEW_MCP_CONTEXT_REPO_MAX_TOKENS": "199"}, "context.repo.max_tokens: 199 is out of range (200-16000)"},
		{"repo max tokens high", map[string]string{"REVIEW_MCP_CONTEXT_REPO_MAX_TOKENS": "16001"}, "context.repo.max_tokens: 16001 is out of range (200-16000)"},
		{"repo max cache zero", map[string]string{"REVIEW_MCP_CONTEXT_REPO_MAX_CACHE_MB": "0", "REVIEW_MCP_CONTEXT_REPO_MAX_REPO_MB": "1"}, "context.repo.max_cache_mb: 0 is out of range (1-1048576)"},
		{"repo max repo zero", map[string]string{"REVIEW_MCP_CONTEXT_REPO_MAX_REPO_MB": "0"}, "context.repo.max_repo_mb: 0 is out of range (1-1048576)"},
		{"repo max repo above max cache", map[string]string{"REVIEW_MCP_CONTEXT_REPO_MAX_CACHE_MB": "100", "REVIEW_MCP_CONTEXT_REPO_MAX_REPO_MB": "101"}, "context.repo.max_repo_mb: 101 must be at most context.repo.max_cache_mb (100)"},
		{"web url without base url", map[string]string{"REVIEW_MCP_GITEA_BASE_URL": "", "REVIEW_MCP_GITEA_TOKEN": "", "REVIEW_MCP_GITEA_WEB_URL": "https://web.example.com", "REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com", "REVIEW_MCP_BITBUCKET_SERVER_TOKEN": fakeBitbkt}, "gitea.web_url is set but gitea.base_url is not"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := envWith(tc.env)
			if tc.env["REVIEW_MCP_GITEA_BASE_URL"] == "" && strings.Contains(tc.name, "without base url") {
				delete(env, "REVIEW_MCP_GITEA_BASE_URL")
				delete(env, "REVIEW_MCP_GITEA_TOKEN")
			}
			_, _, err := Load(MemSource{Env: env})
			probs := problemsOf(t, err)
			if !hasProblem(probs, tc.want) {
				t.Fatalf("want problem containing %q, got %v", tc.want, probs)
			}
			if len(probs) != 1 {
				t.Errorf("want exactly one problem, got %v", probs)
			}
		})
	}
}

// X-15: llm.context_window is optional; unset (absent or 0) is valid and
// leaves the value to be resolved from the endpoint.
func TestContextWindowUnsetIsValid(t *testing.T) {
	for name, set := range map[string]string{"absent": "", "zero": "0"} {
		t.Run(name, func(t *testing.T) {
			env := envWith(nil)
			delete(env, "REVIEW_MCP_LLM_CONTEXT_WINDOW")
			if set != "" {
				env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = set
			}
			cfg, _, err := Load(MemSource{Env: env})
			if err != nil {
				t.Fatalf("unset llm.context_window must validate: %v", err)
			}
			if cfg.LLM.ContextWindow != 0 {
				t.Errorf("ContextWindow = %d, want 0 (unset)", cfg.LLM.ContextWindow)
			}
		})
	}
}

func TestValidationBoundariesAccepted(t *testing.T) {
	env := envWith(map[string]string{
		"REVIEW_MCP_LLM_CONTEXT_WINDOW":               "4096",
		"REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS":            "4095",
		"REVIEW_MCP_LLM_TEMPERATURE":                  "2",
		"REVIEW_MCP_LLM_TIMEOUT_SECONDS":              "3600",
		"REVIEW_MCP_LLM_MAX_RETRIES":                  "0",
		"REVIEW_MCP_LLM_TOKEN_ESTIMATE_FACTOR":        "0",
		"REVIEW_MCP_LLM_WAIT_SECONDS":                 "600",
		"REVIEW_MCP_OUTPUT_LANGUAGE":                  "zh-Hans-CN",
		"REVIEW_MCP_DIFF_EXTRA_LINES_BEFORE":          "10",
		"REVIEW_MCP_DIFF_EXTRA_LINES_AFTER":           "0",
		"REVIEW_MCP_DIFF_MAX_FILE_BYTES":              "1024",
		"REVIEW_MCP_DIFF_MAX_DIFF_BYTES":              "1024",
		"REVIEW_MCP_REVIEW_MAX_FINDINGS":              "20",
		"REVIEW_MCP_REVIEW_MAX_CHUNKS":                "32",
		"REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS":        "50",
		"REVIEW_MCP_LLM_REASONING_EFFORT":             "high",
		"REVIEW_MCP_IGNORE_REGEX":                     `^docs/.*\.md$`,
		"REVIEW_MCP_DIFF_IGNORE_GENERATED_FRAMEWORKS": "protobuf",
	})
	mustLoad(t, MemSource{Env: env})
	// The lower bounds of the chunking keys; max_total_findings may equal
	// max_findings.
	mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_REVIEW_MAX_CHUNKS": "1",
		"REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS": "3", "REVIEW_MCP_REVIEW_MAX_FINDINGS": "3"})})
	// With its default (10), max_total_findings does not reject a
	// max_findings above it (a valid v1.0 configuration); the pipeline caps
	// at the larger of the two.
	cfg, _ := mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_REVIEW_MAX_FINDINGS": "15"})})
	if got := EffectiveMaxTotalFindings(cfg.Review, cfg.Review.MaxFindings); got != 15 {
		t.Errorf("effective max_total_findings = %d, want 15", got)
	}
	// wait_seconds 0 is valid: answer with a job id at once (X-16).
	mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_LLM_WAIT_SECONDS": "0"})})
}

func TestEmptyStringsInFileListsRejected(t *testing.T) {
	file := "[ignore]\nglob = [\"\"]\n[diff]\nignore_generated_frameworks = [\" \"]\n[llm]\nreasoning_effort = \"\"\n"
	_, _, err := Load(memSrc(envWith(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"}), map[string]string{"/c.toml": file}))
	probs := problemsOf(t, err)
	for _, w := range []string{"ignore.glob[0]", "diff.ignore_generated_frameworks[0]", "llm.reasoning_effort"} {
		if !hasProblem(probs, w) {
			t.Errorf("missing %q in %v", w, probs)
		}
	}
}

// TestURLUserinfoRejectedAndNeverEchoed is a canary: embedded credentials are an
// error and the password never appears in any error or warning text. [canary]
func TestURLUserinfoRejectedAndNeverEchoed(t *testing.T) {
	keys := map[string]string{
		"llm.base_url":              "REVIEW_MCP_LLM_BASE_URL",
		"gitea.base_url":            "REVIEW_MCP_GITEA_BASE_URL",
		"gitea.web_url":             "REVIEW_MCP_GITEA_WEB_URL",
		"bitbucket_server.base_url": "REVIEW_MCP_BITBUCKET_SERVER_BASE_URL",
	}
	for key, envName := range keys {
		for _, raw := range []string{
			"https://user:" + fakeURLUserinfo + "@host.example.com/path",
			"https://" + fakeURLUserinfo + "@host.example.com",
			"https://user:" + fakeURLUserinfo + "@",
			"http://user:" + fakeURLUserinfo + "@host.example.com:8080/a?x=1#frag",
		} {
			env := envWith(map[string]string{envName: raw})
			if key == "bitbucket_server.base_url" {
				env["REVIEW_MCP_BITBUCKET_SERVER_TOKEN"] = fakeBitbkt
			}
			_, rep, err := Load(MemSource{Env: env})
			probs := problemsOf(t, err)
			want := "credentials must not be embedded in " + key
			if !hasProblem(probs, want) {
				t.Errorf("%s=%q: want %q in %v", envName, raw, want, probs)
			}
			assertNoSecrets(t, key+" error", err.Error())
			assertNoSecrets(t, key+" warnings", strings.Join(rep.Warnings, "\n"))
		}
	}
}

func TestURLPasswordNeverEchoedInOtherErrors(t *testing.T) {
	for _, raw := range []string{
		"ftp://user:" + fakeURLUserinfo + "@host.example.com",
		"user:" + fakeURLUserinfo + "@host.example.com",
		"http://[::1",
		"https://host.example.com/path#" + fakeURLUserinfo,
		"mailto:a:" + fakeURLUserinfo + "@example.com",
		"://" + fakeURLUserinfo,
		"https:" + fakeURLUserinfo + "@example.com",
	} {
		_, _, err := Load(MemSource{Env: envWith(map[string]string{"REVIEW_MCP_LLM_BASE_URL": raw})})
		if err == nil {
			t.Errorf("%q: expected an error", raw)
			continue
		}
		assertNoSecrets(t, raw, err.Error())
	}
}

func TestURLRules(t *testing.T) {
	bad := map[string]string{
		"ftp://host.example.com":      "scheme must be http or https",
		"host.example.com":            "llm.base_url",
		"https://":                    "must have a host",
		"https://host.example.com/#x": "must not contain a fragment",
		"https://host.example.com#":   "must not contain a fragment",
		"https:///path":               "must have a host",
	}
	for raw, want := range bad {
		_, _, err := Load(MemSource{Env: envWith(map[string]string{"REVIEW_MCP_LLM_BASE_URL": raw})})
		if !hasProblem(problemsOf(t, err), want) {
			t.Errorf("%q: want %q, got %v", raw, want, err)
		}
	}
}

func TestURLNormalizationAndContextPath(t *testing.T) {
	env := envWith(map[string]string{
		"REVIEW_MCP_LLM_BASE_URL":              "https://llm.example.com/v1/",
		"REVIEW_MCP_GITEA_BASE_URL":            "http://your-gitea.example:3000//",
		"REVIEW_MCP_GITEA_WEB_URL":             "https://web.example.com/",
		"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com/stash/",
		"REVIEW_MCP_BITBUCKET_SERVER_TOKEN":    fakeBitbkt,
	})
	cfg, _ := mustLoad(t, MemSource{Env: env})
	if cfg.LLM.BaseURL != "https://llm.example.com/v1" ||
		cfg.Gitea.BaseURL != "http://your-gitea.example:3000" ||
		cfg.Gitea.WebURL != "https://web.example.com" ||
		cfg.BitbucketServer.BaseURL != "https://bitbucket.example.com/stash" {
		t.Errorf("unexpected normalization: %q %q %q %q", cfg.LLM.BaseURL, cfg.Gitea.BaseURL, cfg.Gitea.WebURL, cfg.BitbucketServer.BaseURL)
	}
}

func TestProviderEnablementRules(t *testing.T) {
	t.Run("gitea enabled without token", func(t *testing.T) {
		env := minimalEnv()
		delete(env, "REVIEW_MCP_GITEA_TOKEN")
		_, _, err := Load(MemSource{Env: env})
		if !hasProblem(problemsOf(t, err), "REVIEW_MCP_GITEA_TOKEN is required because gitea.base_url is set") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("bitbucket enabled without token", func(t *testing.T) {
		env := envWith(map[string]string{"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com"})
		_, _, err := Load(MemSource{Env: env})
		if !hasProblem(problemsOf(t, err), "REVIEW_MCP_BITBUCKET_SERVER_TOKEN is required") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("no provider", func(t *testing.T) {
		env := minimalEnv()
		delete(env, "REVIEW_MCP_GITEA_BASE_URL")
		delete(env, "REVIEW_MCP_GITEA_TOKEN")
		_, _, err := Load(MemSource{Env: env})
		if !hasProblem(problemsOf(t, err), "no provider enabled") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("token for disabled provider warns", func(t *testing.T) {
		env := envWith(map[string]string{"REVIEW_MCP_BITBUCKET_SERVER_TOKEN": fakeBitbkt})
		_, rep := mustLoad(t, MemSource{Env: env})
		if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "REVIEW_MCP_BITBUCKET_SERVER_TOKEN is set but bitbucket_server is not enabled") {
			t.Errorf("warnings: %v", rep.Warnings)
		}
		assertNoSecrets(t, "warning", rep.Warnings[0])
	})
	t.Run("both providers", func(t *testing.T) {
		env := envWith(map[string]string{"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com", "REVIEW_MCP_BITBUCKET_SERVER_TOKEN": fakeBitbkt})
		cfg, rep := mustLoad(t, MemSource{Env: env})
		if len(cfg.Summary(rep).Providers) != 2 {
			t.Error("want two providers")
		}
	})
}

func TestTLSSettings(t *testing.T) {
	env := envWith(map[string]string{
		"REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY":            "true",
		"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL":             "https://bitbucket.example.com",
		"REVIEW_MCP_BITBUCKET_SERVER_TOKEN":                fakeBitbkt,
		"REVIEW_MCP_BITBUCKET_SERVER_INSECURE_SKIP_VERIFY": "1",
	})
	_, rep := mustLoad(t, MemSource{Env: env})
	got := strings.Join(rep.Warnings, "|")
	for _, w := range []string{"TLS verification disabled for gitea", "TLS verification disabled for bitbucket_server"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing warning %q in %v", w, rep.Warnings)
		}
	}
}

func TestCACertRules(t *testing.T) {
	files := fstest.MapFS{
		"certs/ca.pem":       {Data: []byte("not really a cert; only readability is checked")},
		"certs/sub/file.pem": {Data: []byte("x")},
	}
	src := func(env map[string]string, fsys MemSource) MemSource { fsys.Env = env; return fsys }
	base := MemSource{FS: files}

	mustLoad(t, src(envWith(map[string]string{"REVIEW_MCP_GITEA_CA_CERT": "/certs/ca.pem"}), base))

	for name, tc := range map[string]struct {
		path string
		fsys MemSource
		want string
	}{
		"missing":    {"/certs/none.pem", base, "gitea.ca_cert: cannot read CA certificate file"},
		"directory":  {"/certs/sub", base, "is not a regular file"},
		"unreadable": {"/certs/ca.pem", MemSource{FS: unreadableFS{files}}, "cannot read CA certificate file"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Load(src(envWith(map[string]string{"REVIEW_MCP_GITEA_CA_CERT": tc.path}), tc.fsys))
			if !hasProblem(problemsOf(t, err), tc.want) {
				t.Errorf("want %q, got %v", tc.want, err)
			}
		})
	}
	_, _, err := Load(src(envWith(map[string]string{
		"REVIEW_MCP_BITBUCKET_SERVER_CA_CERT": "/certs/none.pem",
	}), base))
	if !hasProblem(problemsOf(t, err), "bitbucket_server.ca_cert") {
		t.Errorf("bitbucket ca_cert not validated: %v", err)
	}
}

func TestIgnoreAndFrameworkAccepted(t *testing.T) {
	env := envWith(map[string]string{
		"REVIEW_MCP_IGNORE_GLOB":                      "**/*.pb.go,vendor/**",
		"REVIEW_MCP_DIFF_IGNORE_GENERATED_FRAMEWORKS": "protobuf,graphql",
	})
	if _, _, err := Load(MemSource{Env: env}); err != nil {
		t.Fatalf("valid ignore settings rejected: %v", err)
	}
}

func TestGlobProblemDoesNotEchoPattern(t *testing.T) {
	env := envWith(map[string]string{"REVIEW_MCP_IGNORE_GLOB": "secret-dir/[b"})
	_, _, err := Load(MemSource{Env: env})
	for _, p := range problemsOf(t, err) {
		if strings.Contains(p, "secret-dir") {
			t.Errorf("problem echoes the raw pattern: %q", p)
		}
	}
}

func TestDiffMaxTokens(t *testing.T) {
	cfg, _ := mustLoad(t, MemSource{Env: minimalEnv()})
	if cfg.Diff.MaxTokens != nil {
		t.Errorf("diff.max_tokens is %d by default, want unset", *cfg.Diff.MaxTokens)
	}
	cfg, rep := mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_DIFF_MAX_TOKENS": "24000"})})
	if cfg.Diff.MaxTokens == nil || *cfg.Diff.MaxTokens != 24000 {
		t.Errorf("env REVIEW_MCP_DIFF_MAX_TOKENS: got %v, want 24000", cfg.Diff.MaxTokens)
	}
	if rep.Sources["diff.max_tokens"] != OriginEnv {
		t.Errorf("origin = %v, want env", rep.Sources["diff.max_tokens"])
	}
	cfg, _ = mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_DIFF_MAX_TOKENS": "1000"})})
	if cfg.Diff.MaxTokens == nil || *cfg.Diff.MaxTokens != 1000 {
		t.Error("1000 is the minimum and must load")
	}
}
