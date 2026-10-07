// Package config loads, layers and validates the review-mcp configuration.
//
// Layers, low to high precedence: compiled defaults, an optional TOML file
// named by REVIEW_MCP_CONFIG, then environment variables. Per-call tool
// arguments are applied elsewhere. Secrets are environment-only. The key set,
// environment names and defaults are the §5 table of
// docs/design/v1-design-decisions.md.
package config

// Config is the effective configuration.
//
// Optional values without a default (diff.max_tokens, llm.max_output_tokens, llm.temperature,
// llm.seed, llm.reasoning_effort) are pointers: nil means "unset" and, per
// DQ-26, "do not send".
type Config struct {
	LLM             LLM             `toml:"llm" json:"llm"`
	Gitea           Gitea           `toml:"gitea" json:"gitea"`
	BitbucketServer BitbucketServer `toml:"bitbucket_server" json:"bitbucket_server"`
	Output          Output          `toml:"output" json:"output"`
	Diff            Diff            `toml:"diff" json:"diff"`
	Ignore          Ignore          `toml:"ignore" json:"ignore"`
	Review          Review          `toml:"review" json:"review"`
	Ask             Ask             `toml:"ask" json:"ask"`
	Context         Context         `toml:"context" json:"context"`
	Log             Log             `toml:"log" json:"log"`
	Serve           Serve           `toml:"serve" json:"serve"`
	Secrets         Secrets         `toml:"-" json:"secrets"`
}

// LLM configures the OpenAI-compatible endpoint.
type LLM struct {
	BaseURL             string   `toml:"base_url" json:"base_url"`
	Model               string   `toml:"model" json:"model"`
	ContextWindow       int      `toml:"context_window" json:"context_window"` // optional (X-15); 0 means unset, resolved from the endpoint
	MaxOutputTokens     *int     `toml:"max_output_tokens" json:"max_output_tokens"`
	Temperature         *float64 `toml:"temperature" json:"temperature"`
	Seed                *int64   `toml:"seed" json:"seed"`
	ReasoningEffort     *string  `toml:"reasoning_effort" json:"reasoning_effort"`
	TimeoutSeconds      int      `toml:"timeout_seconds" json:"timeout_seconds"`
	MaxRetries          int      `toml:"max_retries" json:"max_retries"`
	TokenEstimateFactor float64  `toml:"token_estimate_factor" json:"token_estimate_factor"`
	// WaitSeconds is how long a stdio pr_review or pr_ask call waits for its
	// result before it answers with a job id (X-16); serve mode ignores it.
	WaitSeconds int `toml:"wait_seconds" json:"wait_seconds"`
}

// Gitea configures the Gitea provider; it is enabled iff BaseURL is set.
type Gitea struct {
	BaseURL            string `toml:"base_url" json:"base_url"`
	WebURL             string `toml:"web_url" json:"web_url"`
	CACert             string `toml:"ca_cert" json:"ca_cert"`
	InsecureSkipVerify bool   `toml:"insecure_skip_verify" json:"insecure_skip_verify"`
}

// BitbucketServer configures the Bitbucket Server provider; it is enabled iff
// BaseURL is set (including any context path).
type BitbucketServer struct {
	BaseURL            string `toml:"base_url" json:"base_url"`
	CACert             string `toml:"ca_cert" json:"ca_cert"`
	InsecureSkipVerify bool   `toml:"insecure_skip_verify" json:"insecure_skip_verify"`
}

// Output configures result rendering.
type Output struct {
	Language string `toml:"language" json:"language"`
}

// Diff configures diff acquisition and budgeting.
type Diff struct {
	ExtraLinesBefore     int      `toml:"extra_lines_before" json:"extra_lines_before"`
	ExtraLinesAfter      int      `toml:"extra_lines_after" json:"extra_lines_after"`
	SkipExtendExtensions []string `toml:"skip_extend_extensions" json:"skip_extend_extensions"`
	LargePatchPolicy     string   `toml:"large_patch_policy" json:"large_patch_policy"`
	MaxDescriptionTokens int      `toml:"max_description_tokens" json:"max_description_tokens"`
	MaxCommitsTokens     int      `toml:"max_commits_tokens" json:"max_commits_tokens"`
	MaxFilesFullContent  int      `toml:"max_files_full_content" json:"max_files_full_content"`
	MaxFileBytes         int      `toml:"max_file_bytes" json:"max_file_bytes"`
	MaxDiffBytes         int      `toml:"max_diff_bytes" json:"max_diff_bytes"`
	// MaxTokens caps the diff token budget below what the context window
	// allows (X-17); nil means no cap.
	MaxTokens                 *int     `toml:"max_tokens" json:"max_tokens"`
	IgnoreGeneratedFrameworks []string `toml:"ignore_generated_frameworks" json:"ignore_generated_frameworks"`
}

// Ignore configures file exclusion.
type Ignore struct {
	Glob  []string `toml:"glob" json:"glob"`
	Regex []string `toml:"regex" json:"regex"`
}

// Review configures the pr_review tool.
type Review struct {
	MaxFindings           int    `toml:"max_findings" json:"max_findings"`
	RequireTests          bool   `toml:"require_tests" json:"require_tests"`
	RequireSecurity       bool   `toml:"require_security" json:"require_security"`
	RequirePerformance    bool   `toml:"require_performance" json:"require_performance"`
	RequireEffortEstimate bool   `toml:"require_effort_estimate" json:"require_effort_estimate"`
	ExtraInstructions     string `toml:"extra_instructions" json:"extra_instructions"`
	// InlineFindings posts each anchorable finding as an inline comment when
	// a review is published (X-11).
	InlineFindings bool `toml:"inline_findings" json:"inline_findings"`
	// PersistentOverview edits the overview comment of an earlier run in
	// place instead of posting a new one (X-12).
	PersistentOverview bool `toml:"persistent_overview" json:"persistent_overview"`
	// MaxDiscussionTokens is the token budget of the existing-discussion
	// block of the prompt (X-13); 0 turns the block off.
	MaxDiscussionTokens int `toml:"max_discussion_tokens" json:"max_discussion_tokens"`
	// MaxChunks is the most model calls ("parts") one review makes when the
	// prepared diff does not hold every reviewable file (X-19); 1 turns
	// chunking off.
	MaxChunks int `toml:"max_chunks" json:"max_chunks"`
	// MaxTotalFindings caps the findings of a review after the parts are
	// merged (X-19); each part still asks for at most MaxFindings.
	MaxTotalFindings int `toml:"max_total_findings" json:"max_total_findings"`
}

// Ask configures the pr_ask tool.
type Ask struct {
	ExtraInstructions string `toml:"extra_instructions" json:"extra_instructions"`
}

// Context configures the context added to prompts beyond the diff.
type Context struct {
	Repo ContextRepo `toml:"repo" json:"repo"`
}

// ContextRepo configures repository context (X-22, RC-1 to RC-10): a cached
// shallow fetch of the PR head that is searched for the uses of changed
// symbols.
type ContextRepo struct {
	// Enabled turns repository context on (stdio only, RC-1).
	Enabled bool `toml:"enabled" json:"enabled"`
	// CacheDir holds the cached repositories; "" means the OS user cache
	// directory plus review-mcp/repos.
	CacheDir string `toml:"cache_dir" json:"cache_dir"`
	// IdleDays: a cached repository unused for longer is deleted.
	IdleDays int `toml:"idle_days" json:"idle_days"`
	// MaxCacheMB caps the cache; least-recently-used repositories are
	// deleted until it fits.
	MaxCacheMB int `toml:"max_cache_mb" json:"max_cache_mb"`
	// MaxRepoMB caps one repository, measured after the fetch.
	MaxRepoMB int `toml:"max_repo_mb" json:"max_repo_mb"`
	// FetchTimeoutSeconds limits the fetch of a pull request head.
	FetchTimeoutSeconds int `toml:"fetch_timeout_seconds" json:"fetch_timeout_seconds"`
}

// Log configures logging (stderr only).
type Log struct {
	Level string `toml:"level" json:"level"`
}

// Defaults returns the compiled defaults: exactly the "Default" column of §5.
// Keys without a default (URLs, model, context window, sampling parameters,
// max output tokens) stay zero/nil.
func Defaults() *Config {
	return &Config{
		LLM: LLM{
			TimeoutSeconds:      300,
			MaxRetries:          1,
			TokenEstimateFactor: 0.3,
			WaitSeconds:         45,
		},
		Output: Output{Language: "en-US"},
		Diff: Diff{
			ExtraLinesBefore:          5,
			ExtraLinesAfter:           1,
			SkipExtendExtensions:      []string{".md", ".txt"},
			LargePatchPolicy:          "clip",
			MaxDescriptionTokens:      500,
			MaxCommitsTokens:          500,
			MaxFilesFullContent:       50,
			MaxFileBytes:              1048576,
			MaxDiffBytes:              20971520,
			IgnoreGeneratedFrameworks: []string{},
		},
		Ignore: Ignore{
			Glob:  []string{"vendor/**"},
			Regex: []string{},
		},
		Review: Review{
			MaxFindings:           3,
			RequireTests:          true,
			RequireSecurity:       true,
			RequirePerformance:    true,
			RequireEffortEstimate: true,
			InlineFindings:        true,
			PersistentOverview:    true,
			MaxDiscussionTokens:   1500,
			MaxChunks:             8,
			MaxTotalFindings:      10,
		},
		Context: Context{Repo: ContextRepo{
			IdleDays:            7,
			MaxCacheMB:          2048,
			MaxRepoMB:           500,
			FetchTimeoutSeconds: 60,
		}},
		Log: Log{Level: "info"},
		Serve: Serve{
			Listen:             DefaultServeListen,
			LLMKeySource:       LLMKeySourceHeader,
			AllowedOrigins:     []string{},
			MaxConcurrentCalls: 4,
		},
	}
}
