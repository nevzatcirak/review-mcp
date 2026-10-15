// Package config loads, layers and validates the review-mcp configuration.
//
// Layers, low to high precedence: compiled defaults, an optional TOML file
// named by REVIEW_MCP_CONFIG, then environment variables. Per-call tool
// arguments are applied elsewhere. Secrets are environment-only. The key set,
// environment names and defaults are the §5 table of
// docs/design/v1-design-decisions.md.
package config

import "strings"

// Config is the effective configuration.
//
// Optional values without a default (diff.max_tokens, llm.max_output_tokens, llm.temperature,
// llm.seed, llm.reasoning_effort) are pointers: nil means "unset" and, per
// DQ-26, "do not send".
type Config struct {
	LLM             LLM             `toml:"llm" json:"llm"`
	Gitea           Gitea           `toml:"gitea" json:"gitea"`
	BitbucketServer BitbucketServer `toml:"bitbucket_server" json:"bitbucket_server"`
	GitHub          GitHub          `toml:"github" json:"github"`
	GitLab          GitLab          `toml:"gitlab" json:"gitlab"`
	Output          Output          `toml:"output" json:"output"`
	Diff            Diff            `toml:"diff" json:"diff"`
	Ignore          Ignore          `toml:"ignore" json:"ignore"`
	Review          Review          `toml:"review" json:"review"`
	Ask             Ask             `toml:"ask" json:"ask"`
	Improve         Improve         `toml:"improve" json:"improve"`
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
	// RequestsPerMinute is the most HTTP requests (every attempt, retries
	// included) started in any 60-second window, per endpoint and shared by
	// every call of the process (X-37); 0 is no client limit.
	RequestsPerMinute int `toml:"requests_per_minute" json:"requests_per_minute"`
	// MaxConcurrentRequests is the most requests in flight at once, per
	// endpoint (X-37); 0 is unlimited.
	MaxConcurrentRequests int `toml:"max_concurrent_requests" json:"max_concurrent_requests"`
	// MaxRateWaitSeconds is the longest one wait for the rate limit may be
	// (X-37); a longer needed wait is not waited for.
	MaxRateWaitSeconds int `toml:"max_rate_wait_seconds" json:"max_rate_wait_seconds"`
	// MaxRateRetries is the number of retries after HTTP 429 for one model
	// call, counted apart from MaxRetries (X-37).
	MaxRateRetries int `toml:"max_rate_retries" json:"max_rate_retries"`
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

// GitHub configures the GitHub provider (github.com or GitHub Enterprise
// Server); it is enabled iff BaseURL is set. BaseURL is the web base that
// pull request URLs start with; no host is assumed (X-2). APIURL is
// optional: when it is unset the API base is derived from BaseURL
// (EffectiveAPIURL).
type GitHub struct {
	BaseURL            string `toml:"base_url" json:"base_url"`
	APIURL             string `toml:"api_url" json:"api_url"`
	CACert             string `toml:"ca_cert" json:"ca_cert"`
	InsecureSkipVerify bool   `toml:"insecure_skip_verify" json:"insecure_skip_verify"`
}

// gitHubDotCom is the one web base whose API lives on another host.
const (
	gitHubDotCom    = "https://github.com"
	gitHubDotComAPI = "https://api.github.com"
)

// EffectiveAPIURL returns the API base the GitHub provider calls, without a
// trailing slash: github.api_url when it is set; otherwise
// https://api.github.com when the web base is exactly https://github.com
// (any letter case, no port, no path), and {base_url}/api/v3 (GitHub
// Enterprise Server) for any other web base. It is "" when the provider is
// not enabled.
func (g GitHub) EffectiveAPIURL() string {
	if api := strings.TrimRight(strings.TrimSpace(g.APIURL), "/"); api != "" {
		return api
	}
	base := strings.TrimRight(strings.TrimSpace(g.BaseURL), "/")
	switch {
	case base == "":
		return ""
	case strings.EqualFold(base, gitHubDotCom):
		return gitHubDotComAPI
	}
	return base + "/api/v3"
}

// GitLab configures the GitLab provider (gitlab.com or self-managed
// GitLab); it is enabled iff BaseURL is set. BaseURL is the web base that
// merge request URLs start with, including any context path (such as
// https://example.com/gitlab); no host is assumed (X-2). The API base is
// always derived from it (APIURL); there is no separate setting.
type GitLab struct {
	BaseURL            string `toml:"base_url" json:"base_url"`
	CACert             string `toml:"ca_cert" json:"ca_cert"`
	InsecureSkipVerify bool   `toml:"insecure_skip_verify" json:"insecure_skip_verify"`
}

// APIURL returns the API base the GitLab provider calls, {base_url}/api/v4,
// without a trailing slash. It is "" when the provider is not enabled.
func (g GitLab) APIURL() string {
	base := strings.TrimRight(strings.TrimSpace(g.BaseURL), "/")
	if base == "" {
		return ""
	}
	return base + "/api/v4"
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
	// CIStatus reads the head commit's CI statuses and checks for the
	// review: the prompt block, the overview line and the result's ci
	// (X-31). pr_info always reads them.
	CIStatus bool `toml:"ci_status" json:"ci_status"`
	// MaxCITokens is the token budget of the CI block of the prompt (X-31);
	// 0 turns the block off.
	MaxCITokens int `toml:"max_ci_tokens" json:"max_ci_tokens"`
	// GuidelinesPaths are the candidate paths of the project's guidelines
	// file, relative to the repository root; pr_review and pr_improve read
	// the first one that exists at the pull request's base revision (X-32).
	// An empty list turns the guidelines off.
	GuidelinesPaths []string `toml:"guidelines_paths" json:"guidelines_paths"`
	// GuidelinesPathsExplicit is true when review.guidelines_paths was set
	// in the configuration file or the environment rather than left at its
	// default: only then is a missing guidelines file worth a note. Load
	// sets it from the Report; it is not a configuration key.
	GuidelinesPathsExplicit bool `toml:"-" json:"-"`
	// MaxGuidelinesTokens is the token budget of the guidelines block of
	// the prompt (X-32); 0 turns the guidelines off.
	MaxGuidelinesTokens int `toml:"max_guidelines_tokens" json:"max_guidelines_tokens"`
	// FileTree lists the directories the change touches, at the base
	// revision, for pr_review and pr_improve (X-32).
	FileTree bool `toml:"file_tree" json:"file_tree"`
	// MaxTreeTokens is the token budget of the file-tree block of the
	// prompt (X-32); 0 turns the block off.
	MaxTreeTokens int `toml:"max_tree_tokens" json:"max_tree_tokens"`
	// MaxChunks is the most model calls ("parts") one review makes when the
	// prepared diff does not hold every reviewable file (X-19); 1 turns
	// chunking off.
	MaxChunks int `toml:"max_chunks" json:"max_chunks"`
	// MaxTotalFindings caps the findings of a review after the parts are
	// merged (X-19); each part still asks for at most MaxFindings.
	MaxTotalFindings int `toml:"max_total_findings" json:"max_total_findings"`
	// Incremental reviews only the files of the pull request whose content
	// changed since the last review review-mcp published on it (X-36);
	// pr_review's incremental argument replaces it for a call.
	Incremental bool `toml:"incremental" json:"incremental"`
	// IncrementalMaxFiles is the most files the change detection of an
	// incremental review compares through the provider's API (X-36); a
	// pull request with more files is reviewed in full, with a note.
	IncrementalMaxFiles int `toml:"incremental_max_files" json:"incremental_max_files"`
}

// Ask configures the pr_ask tool.
type Ask struct {
	ExtraInstructions string `toml:"extra_instructions" json:"extra_instructions"`
}

// Improve configures the pr_improve tool (v2 spec §1.9, X-27).
type Improve struct {
	// MaxSuggestions caps the suggestions of a run after the parts are
	// merged and deduplicated.
	MaxSuggestions int `toml:"max_suggestions" json:"max_suggestions"`
	// MaxSuggestionsPerPart is the most suggestions one model call (one
	// part, or the whole run in one call) is asked for.
	MaxSuggestionsPerPart int `toml:"max_suggestions_per_part" json:"max_suggestions_per_part"`
	// MinScore drops the suggestions whose self-review score (0 to 10) is
	// below it; unscored suggestions are kept.
	MinScore int `toml:"min_score" json:"min_score"`
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
	// MaxSymbols caps the symbols taken from the diff and searched for (RC-7).
	MaxSymbols int `toml:"max_symbols" json:"max_symbols"`
	// MaxHitsPerSymbol caps the uses kept per symbol (RC-7).
	MaxHitsPerSymbol int `toml:"max_hits_per_symbol" json:"max_hits_per_symbol"`
	// MaxTokens is the token budget of the repository-context block of the
	// prompt (RC-8); a part of a review in parts gets its own.
	MaxTokens int `toml:"max_tokens" json:"max_tokens"`
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
			MaxRateWaitSeconds:  60,
			MaxRateRetries:      4,
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
			CIStatus:              true,
			MaxCITokens:           200,
			GuidelinesPaths:       []string{".review-mcp/guidelines.md", "CONTRIBUTING.md"},
			MaxGuidelinesTokens:   1500,
			FileTree:              true,
			MaxTreeTokens:         600,
			MaxChunks:             8,
			MaxTotalFindings:      10,
			IncrementalMaxFiles:   300,
		},
		Improve: Improve{
			MaxSuggestions:        8,
			MaxSuggestionsPerPart: 4,
			MinScore:              7,
		},
		Context: Context{Repo: ContextRepo{
			IdleDays:            7,
			MaxCacheMB:          2048,
			MaxRepoMB:           500,
			FetchTimeoutSeconds: 60,
			MaxSymbols:          20,
			MaxHitsPerSymbol:    5,
			MaxTokens:           2000,
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
