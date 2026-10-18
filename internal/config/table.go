package config

import "strings"

// Environment variable names that are not §5 keys.
const (
	// EnvConfig names the optional TOML file.
	EnvConfig = "REVIEW_MCP_CONFIG"

	// EnvPrefix is the prefix of every recognised variable; any other
	// variable carrying it produces an "unknown environment variable" warning.
	EnvPrefix = "REVIEW_MCP_"

	envLLMAPIKey            = "REVIEW_MCP_LLM_API_KEY"            //nolint:gosec // G101 false positive: an environment variable name, not a credential
	envGiteaToken           = "REVIEW_MCP_GITEA_TOKEN"            //nolint:gosec // G101 false positive: an environment variable name, not a credential
	envBitbucketServerToken = "REVIEW_MCP_BITBUCKET_SERVER_TOKEN" //nolint:gosec // G101 false positive: an environment variable name, not a credential
	envGitHubToken          = "REVIEW_MCP_GITHUB_TOKEN"           //nolint:gosec // G101 false positive: an environment variable name, not a credential
	envGitLabToken          = "REVIEW_MCP_GITLAB_TOKEN"           //nolint:gosec // G101 false positive: an environment variable name, not a credential
	envServeAccessToken     = "REVIEW_MCP_SERVE_ACCESS_TOKEN"     //nolint:gosec // G101 false positive: an environment variable name, not a credential
)

// entry binds one §5 key to its environment variable and its Config field.
// ptr returns a pointer to the field: *string, *int, *bool, *float64,
// *[]string, or a pointer to a pointer (**int, **int64, **float64, **string)
// for optional values.
type entry struct {
	key string
	env string
	ptr func(*Config) any
}

// table is the fixed, hand-written key/env table (DQ-24). It deliberately
// contains no reflection; a test cross-checks it against the Config struct so
// neither can drift from the other.
var table = []entry{
	{"llm.base_url", "REVIEW_MCP_LLM_BASE_URL", func(c *Config) any { return &c.LLM.BaseURL }},
	{"llm.model", "REVIEW_MCP_LLM_MODEL", func(c *Config) any { return &c.LLM.Model }},
	{"llm.context_window", "REVIEW_MCP_LLM_CONTEXT_WINDOW", func(c *Config) any { return &c.LLM.ContextWindow }},
	{"llm.max_output_tokens", "REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS", func(c *Config) any { return &c.LLM.MaxOutputTokens }},
	{"llm.temperature", "REVIEW_MCP_LLM_TEMPERATURE", func(c *Config) any { return &c.LLM.Temperature }},
	{"llm.seed", "REVIEW_MCP_LLM_SEED", func(c *Config) any { return &c.LLM.Seed }},
	{"llm.reasoning_effort", "REVIEW_MCP_LLM_REASONING_EFFORT", func(c *Config) any { return &c.LLM.ReasoningEffort }},
	{"llm.timeout_seconds", "REVIEW_MCP_LLM_TIMEOUT_SECONDS", func(c *Config) any { return &c.LLM.TimeoutSeconds }},
	{"llm.max_retries", "REVIEW_MCP_LLM_MAX_RETRIES", func(c *Config) any { return &c.LLM.MaxRetries }},
	{"llm.token_estimate_factor", "REVIEW_MCP_LLM_TOKEN_ESTIMATE_FACTOR", func(c *Config) any { return &c.LLM.TokenEstimateFactor }},
	{"llm.wait_seconds", "REVIEW_MCP_LLM_WAIT_SECONDS", func(c *Config) any { return &c.LLM.WaitSeconds }},
	// Rate limiting rows: v2.1 spec V-6 §2.1 (X-37).
	{"llm.requests_per_minute", "REVIEW_MCP_LLM_REQUESTS_PER_MINUTE", func(c *Config) any { return &c.LLM.RequestsPerMinute }},
	{"llm.max_concurrent_requests", "REVIEW_MCP_LLM_MAX_CONCURRENT_REQUESTS", func(c *Config) any { return &c.LLM.MaxConcurrentRequests }},
	{"llm.max_rate_wait_seconds", "REVIEW_MCP_LLM_MAX_RATE_WAIT_SECONDS", func(c *Config) any { return &c.LLM.MaxRateWaitSeconds }},
	{"llm.max_rate_retries", "REVIEW_MCP_LLM_MAX_RATE_RETRIES", func(c *Config) any { return &c.LLM.MaxRateRetries }},

	{"gitea.base_url", "REVIEW_MCP_GITEA_BASE_URL", func(c *Config) any { return &c.Gitea.BaseURL }},
	{"gitea.web_url", "REVIEW_MCP_GITEA_WEB_URL", func(c *Config) any { return &c.Gitea.WebURL }},
	{"gitea.ca_cert", "REVIEW_MCP_GITEA_CA_CERT", func(c *Config) any { return &c.Gitea.CACert }},
	{"gitea.insecure_skip_verify", "REVIEW_MCP_GITEA_INSECURE_SKIP_VERIFY", func(c *Config) any { return &c.Gitea.InsecureSkipVerify }},

	{"bitbucket_server.base_url", "REVIEW_MCP_BITBUCKET_SERVER_BASE_URL", func(c *Config) any { return &c.BitbucketServer.BaseURL }},
	{"bitbucket_server.ca_cert", "REVIEW_MCP_BITBUCKET_SERVER_CA_CERT", func(c *Config) any { return &c.BitbucketServer.CACert }},
	{"bitbucket_server.insecure_skip_verify", "REVIEW_MCP_BITBUCKET_SERVER_INSECURE_SKIP_VERIFY", func(c *Config) any { return &c.BitbucketServer.InsecureSkipVerify }},

	// github.* rows: v2 spec 2B §1 item 1 (X-28).
	{"github.base_url", "REVIEW_MCP_GITHUB_BASE_URL", func(c *Config) any { return &c.GitHub.BaseURL }},
	{"github.api_url", "REVIEW_MCP_GITHUB_API_URL", func(c *Config) any { return &c.GitHub.APIURL }},
	{"github.ca_cert", "REVIEW_MCP_GITHUB_CA_CERT", func(c *Config) any { return &c.GitHub.CACert }},
	{"github.insecure_skip_verify", "REVIEW_MCP_GITHUB_INSECURE_SKIP_VERIFY", func(c *Config) any { return &c.GitHub.InsecureSkipVerify }},

	// gitlab.* rows: v2 spec 2C §1 item 1 (X-34).
	{"gitlab.base_url", "REVIEW_MCP_GITLAB_BASE_URL", func(c *Config) any { return &c.GitLab.BaseURL }},
	{"gitlab.ca_cert", "REVIEW_MCP_GITLAB_CA_CERT", func(c *Config) any { return &c.GitLab.CACert }},
	{"gitlab.insecure_skip_verify", "REVIEW_MCP_GITLAB_INSECURE_SKIP_VERIFY", func(c *Config) any { return &c.GitLab.InsecureSkipVerify }},

	{"output.language", "REVIEW_MCP_OUTPUT_LANGUAGE", func(c *Config) any { return &c.Output.Language }},

	{"diff.extra_lines_before", "REVIEW_MCP_DIFF_EXTRA_LINES_BEFORE", func(c *Config) any { return &c.Diff.ExtraLinesBefore }},
	{"diff.extra_lines_after", "REVIEW_MCP_DIFF_EXTRA_LINES_AFTER", func(c *Config) any { return &c.Diff.ExtraLinesAfter }},
	{"diff.skip_extend_extensions", "REVIEW_MCP_DIFF_SKIP_EXTEND_EXTENSIONS", func(c *Config) any { return &c.Diff.SkipExtendExtensions }},
	{"diff.large_patch_policy", "REVIEW_MCP_DIFF_LARGE_PATCH_POLICY", func(c *Config) any { return &c.Diff.LargePatchPolicy }},
	{"diff.max_description_tokens", "REVIEW_MCP_DIFF_MAX_DESCRIPTION_TOKENS", func(c *Config) any { return &c.Diff.MaxDescriptionTokens }},
	{"diff.max_commits_tokens", "REVIEW_MCP_DIFF_MAX_COMMITS_TOKENS", func(c *Config) any { return &c.Diff.MaxCommitsTokens }},
	{"diff.max_files_full_content", "REVIEW_MCP_DIFF_MAX_FILES_FULL_CONTENT", func(c *Config) any { return &c.Diff.MaxFilesFullContent }},
	{"diff.max_file_bytes", "REVIEW_MCP_DIFF_MAX_FILE_BYTES", func(c *Config) any { return &c.Diff.MaxFileBytes }},
	{"diff.max_diff_bytes", "REVIEW_MCP_DIFF_MAX_DIFF_BYTES", func(c *Config) any { return &c.Diff.MaxDiffBytes }},
	{"diff.max_tokens", "REVIEW_MCP_DIFF_MAX_TOKENS", func(c *Config) any { return &c.Diff.MaxTokens }},
	{"diff.ignore_generated_frameworks", "REVIEW_MCP_DIFF_IGNORE_GENERATED_FRAMEWORKS", func(c *Config) any { return &c.Diff.IgnoreGeneratedFrameworks }},

	{"ignore.glob", "REVIEW_MCP_IGNORE_GLOB", func(c *Config) any { return &c.Ignore.Glob }},
	{"ignore.regex", "REVIEW_MCP_IGNORE_REGEX", func(c *Config) any { return &c.Ignore.Regex }},

	{"review.max_findings", "REVIEW_MCP_REVIEW_MAX_FINDINGS", func(c *Config) any { return &c.Review.MaxFindings }},
	{"review.require_tests", "REVIEW_MCP_REVIEW_REQUIRE_TESTS", func(c *Config) any { return &c.Review.RequireTests }},
	{"review.require_security", "REVIEW_MCP_REVIEW_REQUIRE_SECURITY", func(c *Config) any { return &c.Review.RequireSecurity }},
	{"review.require_performance", "REVIEW_MCP_REVIEW_REQUIRE_PERFORMANCE", func(c *Config) any { return &c.Review.RequirePerformance }},
	{"review.require_effort_estimate", "REVIEW_MCP_REVIEW_REQUIRE_EFFORT_ESTIMATE", func(c *Config) any { return &c.Review.RequireEffortEstimate }},
	{"review.extra_instructions", "REVIEW_MCP_REVIEW_EXTRA_INSTRUCTIONS", func(c *Config) any { return &c.Review.ExtraInstructions }},
	{"review.inline_findings", "REVIEW_MCP_REVIEW_INLINE_FINDINGS", func(c *Config) any { return &c.Review.InlineFindings }},
	{"review.persistent_overview", "REVIEW_MCP_REVIEW_PERSISTENT_OVERVIEW", func(c *Config) any { return &c.Review.PersistentOverview }},
	{"review.max_discussion_tokens", "REVIEW_MCP_REVIEW_MAX_DISCUSSION_TOKENS", func(c *Config) any { return &c.Review.MaxDiscussionTokens }},
	{"review.ci_status", "REVIEW_MCP_REVIEW_CI_STATUS", func(c *Config) any { return &c.Review.CIStatus }},
	{"review.max_ci_tokens", "REVIEW_MCP_REVIEW_MAX_CI_TOKENS", func(c *Config) any { return &c.Review.MaxCITokens }},
	{"review.guidelines_paths", "REVIEW_MCP_REVIEW_GUIDELINES_PATHS", func(c *Config) any { return &c.Review.GuidelinesPaths }},
	{"review.max_guidelines_tokens", "REVIEW_MCP_REVIEW_MAX_GUIDELINES_TOKENS", func(c *Config) any { return &c.Review.MaxGuidelinesTokens }},
	{"review.file_tree", "REVIEW_MCP_REVIEW_FILE_TREE", func(c *Config) any { return &c.Review.FileTree }},
	{"review.max_tree_tokens", "REVIEW_MCP_REVIEW_MAX_TREE_TOKENS", func(c *Config) any { return &c.Review.MaxTreeTokens }},
	{"review.max_chunks", "REVIEW_MCP_REVIEW_MAX_CHUNKS", func(c *Config) any { return &c.Review.MaxChunks }},
	{"review.max_total_findings", "REVIEW_MCP_REVIEW_MAX_TOTAL_FINDINGS", func(c *Config) any { return &c.Review.MaxTotalFindings }},
	{"review.incremental", "REVIEW_MCP_REVIEW_INCREMENTAL", func(c *Config) any { return &c.Review.Incremental }},
	{"review.docs_style", "REVIEW_MCP_REVIEW_DOCS_STYLE", func(c *Config) any { return &c.Review.DocsStyle }},
	{"review.incremental_max_files", "REVIEW_MCP_REVIEW_INCREMENTAL_MAX_FILES", func(c *Config) any { return &c.Review.IncrementalMaxFiles }},
	{"review.issue_compliance", "REVIEW_MCP_REVIEW_ISSUE_COMPLIANCE", func(c *Config) any { return &c.Review.IssueCompliance }},
	{"review.max_issue_tokens", "REVIEW_MCP_REVIEW_MAX_ISSUE_TOKENS", func(c *Config) any { return &c.Review.MaxIssueTokens }},

	{"ask.extra_instructions", "REVIEW_MCP_ASK_EXTRA_INSTRUCTIONS", func(c *Config) any { return &c.Ask.ExtraInstructions }},

	// improve.* rows: v2 spec §1.9 (X-27).
	{"improve.max_suggestions", "REVIEW_MCP_IMPROVE_MAX_SUGGESTIONS", func(c *Config) any { return &c.Improve.MaxSuggestions }},
	{"improve.max_suggestions_per_part", "REVIEW_MCP_IMPROVE_MAX_SUGGESTIONS_PER_PART", func(c *Config) any { return &c.Improve.MaxSuggestionsPerPart }},
	{"improve.min_score", "REVIEW_MCP_IMPROVE_MIN_SCORE", func(c *Config) any { return &c.Improve.MinScore }},

	// context.repo.* rows: v1.1 design note §2 (X-22).
	{"context.repo.enabled", "REVIEW_MCP_CONTEXT_REPO_ENABLED", func(c *Config) any { return &c.Context.Repo.Enabled }},
	{"context.repo.cache_dir", "REVIEW_MCP_CONTEXT_REPO_CACHE_DIR", func(c *Config) any { return &c.Context.Repo.CacheDir }},
	{"context.repo.idle_days", "REVIEW_MCP_CONTEXT_REPO_IDLE_DAYS", func(c *Config) any { return &c.Context.Repo.IdleDays }},
	{"context.repo.max_cache_mb", "REVIEW_MCP_CONTEXT_REPO_MAX_CACHE_MB", func(c *Config) any { return &c.Context.Repo.MaxCacheMB }},
	{"context.repo.max_repo_mb", "REVIEW_MCP_CONTEXT_REPO_MAX_REPO_MB", func(c *Config) any { return &c.Context.Repo.MaxRepoMB }},
	{"context.repo.fetch_timeout_seconds", "REVIEW_MCP_CONTEXT_REPO_FETCH_TIMEOUT_SECONDS", func(c *Config) any { return &c.Context.Repo.FetchTimeoutSeconds }},
	{"context.repo.max_symbols", "REVIEW_MCP_CONTEXT_REPO_MAX_SYMBOLS", func(c *Config) any { return &c.Context.Repo.MaxSymbols }},
	{"context.repo.max_hits_per_symbol", "REVIEW_MCP_CONTEXT_REPO_MAX_HITS_PER_SYMBOL", func(c *Config) any { return &c.Context.Repo.MaxHitsPerSymbol }},
	{"context.repo.max_tokens", "REVIEW_MCP_CONTEXT_REPO_MAX_TOKENS", func(c *Config) any { return &c.Context.Repo.MaxTokens }},
	// v2.4 spec V-8 §0 (X-43).
	{"context.repo.max_tool_rounds", "REVIEW_MCP_CONTEXT_REPO_MAX_TOOL_ROUNDS", func(c *Config) any { return &c.Context.Repo.MaxToolRounds }},
	{"context.repo.max_tool_tokens", "REVIEW_MCP_CONTEXT_REPO_MAX_TOOL_TOKENS", func(c *Config) any { return &c.Context.Repo.MaxToolTokens }},

	// repo_config.* rows: v2.1 spec V-5 §1.1 (X-40).
	{"repo_config.enabled", "REVIEW_MCP_REPO_CONFIG_ENABLED", func(c *Config) any { return &c.RepoConfig.Enabled }},
	{"repo_config.path", "REVIEW_MCP_REPO_CONFIG_PATH", func(c *Config) any { return &c.RepoConfig.Path }},

	// related.* rows: v2.3 spec V-13a §0 (X-41).
	{"related.max_tokens", "REVIEW_MCP_RELATED_MAX_TOKENS", func(c *Config) any { return &c.Related.MaxTokens }},
	{"related.contract_globs", "REVIEW_MCP_RELATED_CONTRACT_GLOBS", func(c *Config) any { return &c.Related.ContractGlobs }},

	{"log.level", "REVIEW_MCP_LOG_LEVEL", func(c *Config) any { return &c.Log.Level }},

	// serve.* rows: P6 spec §1.2. They are read in every mode but validated
	// (and used) only in serve mode.
	{"serve.listen", "REVIEW_MCP_SERVE_LISTEN", func(c *Config) any { return &c.Serve.Listen }},
	{"serve.tls_cert", "REVIEW_MCP_SERVE_TLS_CERT", func(c *Config) any { return &c.Serve.TLSCert }},
	{"serve.tls_key", "REVIEW_MCP_SERVE_TLS_KEY", func(c *Config) any { return &c.Serve.TLSKey }},
	{"serve.allow_insecure_http", "REVIEW_MCP_SERVE_ALLOW_INSECURE_HTTP", func(c *Config) any { return &c.Serve.AllowInsecureHTTP }},
	{"serve.llm_key_source", "REVIEW_MCP_SERVE_LLM_KEY_SOURCE", func(c *Config) any { return &c.Serve.LLMKeySource }},
	{"serve.allowed_origins", "REVIEW_MCP_SERVE_ALLOWED_ORIGINS", func(c *Config) any { return &c.Serve.AllowedOrigins }},
	{"serve.max_concurrent_calls", "REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS", func(c *Config) any { return &c.Serve.MaxConcurrentCalls }},
	{"serve.max_queued_calls", "REVIEW_MCP_SERVE_MAX_QUEUED_CALLS", func(c *Config) any { return &c.Serve.MaxQueuedCalls }},
}

// secretEntry binds a secret to its environment variable. Secrets have no
// file key; the key is used only in reports and messages.
type secretEntry struct {
	key string
	env string
	ptr func(*Config) *Secret
}

var secretTable = []secretEntry{
	{"llm.api_key", envLLMAPIKey, func(c *Config) *Secret { return &c.Secrets.LLMAPIKey }},
	{"gitea.token", envGiteaToken, func(c *Config) *Secret { return &c.Secrets.GiteaToken }},
	{"bitbucket_server.token", envBitbucketServerToken, func(c *Config) *Secret { return &c.Secrets.BitbucketServerToken }},
	{"github.token", envGitHubToken, func(c *Config) *Secret { return &c.Secrets.GitHubToken }},
	{"gitlab.token", envGitLabToken, func(c *Config) *Secret { return &c.Secrets.GitLabToken }},
	{"serve.access_token", envServeAccessToken, func(c *Config) *Secret { return &c.Secrets.ServeAccessToken }},
}

// urlKeys are the keys whose values are URLs (redacted in summaries).
var urlKeys = map[string]bool{
	"llm.base_url":              true,
	"gitea.base_url":            true,
	"gitea.web_url":             true,
	"bitbucket_server.base_url": true,
	"github.base_url":           true,
	"github.api_url":            true,
	"gitlab.base_url":           true,
}

var (
	byKey    = map[string]entry{}
	sections = map[string]bool{}
	knownEnv = map[string]bool{EnvConfig: true}
	keyToEnv = map[string]string{}
)

func init() {
	for _, e := range table {
		byKey[e.key] = e
		// Every proper prefix is a section: "context" and "context.repo"
		// for context.repo.enabled.
		for k := e.key; strings.Contains(k, "."); {
			k = k[:strings.LastIndex(k, ".")]
			sections[k] = true
		}
		knownEnv[e.env] = true
		keyToEnv[e.key] = e.env
	}
	for _, s := range secretTable {
		knownEnv[s.env] = true
		keyToEnv[s.key] = s.env
	}
}
