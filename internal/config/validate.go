package config

import (
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/nevzatcirak/review-mcp/internal/filter/data"
	"github.com/nevzatcirak/review-mcp/internal/logging"
)

var localeRE = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// ValidLocale reports whether s is a locale code accepted by output.language
// (such as en-US or tr). The pr_review tool validates its output_language
// argument with it.
func ValidLocale(s string) bool { return localeRE.MatchString(s) }

const (
	minContextWindow = 4096
	// minDiffMaxTokens is the smallest diff.max_tokens (X-17).
	minDiffMaxTokens = 1000
)

// MaxWaitSeconds is the upper bound of llm.wait_seconds and of the
// wait_seconds tool argument (X-16): ten minutes.
const MaxWaitSeconds = 600

// validate checks the layered configuration, normalizes URLs in place and
// records every problem and warning.
func (l *loader) validate() {
	c := l.cfg
	l.validateLLM()
	l.validateProviders()

	if !ValidLocale(c.Output.Language) {
		l.problem("output.language: %q is not a locale code such as en-US or tr", c.Output.Language)
	}
	l.validateDiff()
	l.validateIgnore()

	if !l.bad["review.max_findings"] && (c.Review.MaxFindings < 1 || c.Review.MaxFindings > 20) {
		l.problem("review.max_findings: %d is out of range (1-20)", c.Review.MaxFindings)
	}
	if !l.bad["review.max_discussion_tokens"] && c.Review.MaxDiscussionTokens < 0 {
		l.problem("review.max_discussion_tokens: %d must not be negative (0 turns the discussion off)", c.Review.MaxDiscussionTokens)
	}
	if !l.bad["review.max_chunks"] && (c.Review.MaxChunks < MinMaxChunks || c.Review.MaxChunks > MaxMaxChunks) {
		l.problem("review.max_chunks: %d is out of range (%d-%d)", c.Review.MaxChunks, MinMaxChunks, MaxMaxChunks)
	}
	l.validateMaxTotalFindings()
	l.validateImprove()
	l.validateContextRepo()
	if _, err := logging.ParseLevel(c.Log.Level); err != nil {
		l.problem("log.level: %v", err)
	}
	if l.mode == ModeServe {
		l.validateServe()
	}
}

// Bounds of review.max_chunks and review.max_total_findings (X-19).
const (
	MinMaxChunks        = 1
	MaxMaxChunks        = 32
	MaxMaxTotalFindings = 50
	maxFindingsUpper    = 20
)

// validateMaxTotalFindings checks review.max_total_findings: at most
// MaxMaxTotalFindings and, when it is set, at least review.max_findings.
//
// Decision (lead; architect may override on 11e2): the spec's lower bound
// "≥ review.max_findings" is checked only when review.max_total_findings is
// set (file or environment). With its default (10), a v1.0 configuration
// with review.max_findings above 10 (allowed up to 20) stays valid, and the
// pipeline caps the merged findings at the larger of the two
// (EffectiveMaxTotalFindings), so such a configuration keeps its findings.
func (l *loader) validateMaxTotalFindings() {
	c := l.cfg.Review
	if l.bad["review.max_total_findings"] {
		return
	}
	if c.MaxTotalFindings < 1 || c.MaxTotalFindings > MaxMaxTotalFindings {
		l.problem("review.max_total_findings: %d is out of range (1-%d)", c.MaxTotalFindings, MaxMaxTotalFindings)
		return
	}
	if l.rep.Sources["review.max_total_findings"] == OriginDefault || l.bad["review.max_findings"] ||
		c.MaxFindings < 1 || c.MaxFindings > maxFindingsUpper {
		return
	}
	if c.MaxTotalFindings < c.MaxFindings {
		l.problem("review.max_total_findings: %d must be at least review.max_findings (%d)", c.MaxTotalFindings, c.MaxFindings)
	}
}

// EffectiveMaxTotalFindings is the cap on a review's merged findings: the
// larger of review.max_total_findings and maxFindings (the effective
// review.max_findings of the call), so that a single part never loses
// findings its own max_findings allowed.
func EffectiveMaxTotalFindings(r Review, maxFindings int) int {
	return max(r.MaxTotalFindings, maxFindings)
}

// Bounds of the improve.* keys (v2 spec §1.9).
const (
	MaxImproveSuggestions        = 30
	MaxImproveSuggestionsPerPart = 10
	MaxImproveMinScore           = 10
)

// validateImprove checks the improve.* keys: improve.max_suggestions 1 to
// 30, improve.max_suggestions_per_part 1 to 10 and improve.min_score 0 to
// 10 (the self-review score's range; 0 keeps every scored suggestion).
//
// DESIGN-QUESTION: what range does improve.max_suggestions_per_part take
// (the spec gives only its default, 4)? — chose 1 to 10: upstream's
// num_code_suggestions_per_chunk has no bound, but each suggestion carries
// two code snippets in the answer and again in the self-review request, so
// a larger number mostly spends output tokens on suggestions the score and
// the total cap drop. It is not tied to improve.max_suggestions: the total
// cap applies after the parts are merged.
func (l *loader) validateImprove() {
	c := l.cfg.Improve
	if !l.bad["improve.max_suggestions"] && (c.MaxSuggestions < 1 || c.MaxSuggestions > MaxImproveSuggestions) {
		l.problem("improve.max_suggestions: %d is out of range (1-%d)", c.MaxSuggestions, MaxImproveSuggestions)
	}
	if !l.bad["improve.max_suggestions_per_part"] && (c.MaxSuggestionsPerPart < 1 || c.MaxSuggestionsPerPart > MaxImproveSuggestionsPerPart) {
		l.problem("improve.max_suggestions_per_part: %d is out of range (1-%d)", c.MaxSuggestionsPerPart, MaxImproveSuggestionsPerPart)
	}
	if !l.bad["improve.min_score"] && (c.MinScore < 0 || c.MinScore > MaxImproveMinScore) {
		l.problem("improve.min_score: %d is out of range (0-%d)", c.MinScore, MaxImproveMinScore)
	}
}

// Bounds of the context.repo.* keys (X-22).
const (
	MaxContextRepoIdleDays     = 365
	MaxContextRepoCacheMB      = 1 << 20 // 1 TiB
	MaxContextRepoFetchTimeout = 600
	MaxContextRepoSymbols      = 50
	MaxContextRepoHits         = 20
	MinContextRepoTokens       = 200
	MaxContextRepoTokens       = 16000
)

// validateContextRepo checks the context.repo.* keys. They are validated
// whether or not context.repo.enabled is set, like every other key.
func (l *loader) validateContextRepo() {
	c := &l.cfg.Context.Repo
	rng := func(key string, v, lo, hi int) bool {
		if l.bad[key] {
			return false
		}
		if v < lo || v > hi {
			l.problem("%s: %d is out of range (%d-%d)", key, v, lo, hi)
			return false
		}
		return true
	}
	c.CacheDir = strings.TrimSpace(c.CacheDir)
	if c.CacheDir != "" && !filepath.IsAbs(c.CacheDir) {
		l.problem("context.repo.cache_dir: %q must be an absolute path", c.CacheDir)
	}
	rng("context.repo.idle_days", c.IdleDays, 1, MaxContextRepoIdleDays)
	rng("context.repo.fetch_timeout_seconds", c.FetchTimeoutSeconds, 1, MaxContextRepoFetchTimeout)
	rng("context.repo.max_symbols", c.MaxSymbols, 1, MaxContextRepoSymbols)
	rng("context.repo.max_hits_per_symbol", c.MaxHitsPerSymbol, 1, MaxContextRepoHits)
	rng("context.repo.max_tokens", c.MaxTokens, MinContextRepoTokens, MaxContextRepoTokens)
	cacheOK := rng("context.repo.max_cache_mb", c.MaxCacheMB, 1, MaxContextRepoCacheMB)
	repoOK := rng("context.repo.max_repo_mb", c.MaxRepoMB, 1, MaxContextRepoCacheMB)
	if cacheOK && repoOK && c.MaxRepoMB > c.MaxCacheMB {
		l.problem("context.repo.max_repo_mb: %d must be at most context.repo.max_cache_mb (%d)", c.MaxRepoMB, c.MaxCacheMB)
	}
}

func (l *loader) validateLLM() {
	c := &l.cfg.LLM
	if c.BaseURL == "" {
		l.problem("llm.base_url is required (%s)", keyToEnv["llm.base_url"])
	} else {
		l.checkURL("llm.base_url", &c.BaseURL)
	}
	if c.Model == "" && !l.bad["llm.model"] {
		l.problem("llm.model is required (%s)", keyToEnv["llm.model"])
	}
	// In serve mode the LLM API key rules depend on serve.llm_key_source;
	// validateServe applies them.
	if l.mode != ModeServe && !l.cfg.Secrets.LLMAPIKey.IsSet() {
		l.problem("%s is required (secret; environment only)", envLLMAPIKey)
	}

	switch {
	case l.bad["llm.context_window"]:
	case c.ContextWindow == 0:
		// Optional (X-15): unset means the endpoint is asked at the first use.
	case c.ContextWindow < minContextWindow:
		l.problem("llm.context_window: %d is below the minimum %d", c.ContextWindow, minContextWindow)
	}

	if c.MaxOutputTokens != nil && !l.bad["llm.max_output_tokens"] {
		v := *c.MaxOutputTokens
		switch {
		case v <= 0:
			l.problem("llm.max_output_tokens: %d must be greater than 0", v)
		case c.ContextWindow > 0 && v >= c.ContextWindow:
			l.problem("llm.max_output_tokens: %d must be less than llm.context_window (%d)", v, c.ContextWindow)
		}
	}
	if c.Temperature != nil && !l.bad["llm.temperature"] {
		if t := *c.Temperature; !(t >= 0 && t <= 2) { // also rejects NaN
			l.problem("llm.temperature: %v is out of range (0-2)", t)
		}
	}
	if c.ReasoningEffort != nil && strings.TrimSpace(*c.ReasoningEffort) == "" {
		l.problem("llm.reasoning_effort: must not be empty when set")
	}
	if !l.bad["llm.timeout_seconds"] && (c.TimeoutSeconds < 1 || c.TimeoutSeconds > 3600) {
		l.problem("llm.timeout_seconds: %d is out of range (1-3600)", c.TimeoutSeconds)
	}
	if !l.bad["llm.max_retries"] && (c.MaxRetries < 0 || c.MaxRetries > 5) {
		l.problem("llm.max_retries: %d is out of range (0-5)", c.MaxRetries)
	}
	if !l.bad["llm.wait_seconds"] && (c.WaitSeconds < 0 || c.WaitSeconds > MaxWaitSeconds) {
		l.problem("llm.wait_seconds: %d is out of range (0-%d)", c.WaitSeconds, MaxWaitSeconds)
	}
	if f := c.TokenEstimateFactor; !l.bad["llm.token_estimate_factor"] && !(f >= 0 && f <= 2) {
		l.problem("llm.token_estimate_factor: %v is out of range (0-2)", f)
	}
}

func (l *loader) validateProviders() {
	c := l.cfg
	g, b, h := &c.Gitea, &c.BitbucketServer, &c.GitHub
	g.BaseURL = strings.TrimSpace(g.BaseURL)
	g.WebURL = strings.TrimSpace(g.WebURL)
	b.BaseURL = strings.TrimSpace(b.BaseURL)
	h.BaseURL = strings.TrimSpace(h.BaseURL)
	h.APIURL = strings.TrimSpace(h.APIURL)

	giteaOn := g.BaseURL != ""
	bbOn := b.BaseURL != ""
	ghOn := h.BaseURL != ""

	// In serve mode provider tokens come from request headers: the
	// environment token must be unset whether or not the provider is enabled
	// (X-10), and an enabled provider needs only its base URL (X-2).
	serve := l.mode == ModeServe
	if serve {
		l.refuseServeEnvToken(c.Secrets.GiteaToken, envGiteaToken)
		l.refuseServeEnvToken(c.Secrets.BitbucketServerToken, envBitbucketServerToken)
		l.refuseServeEnvToken(c.Secrets.GitHubToken, envGitHubToken)
	}

	if giteaOn {
		l.checkURL("gitea.base_url", &g.BaseURL)
		if !serve && !c.Secrets.GiteaToken.IsSet() {
			l.problem("%s is required because gitea.base_url is set", envGiteaToken)
		}
	} else if !serve && c.Secrets.GiteaToken.IsSet() {
		l.warn("%s is set but gitea is not enabled (gitea.base_url is unset); the token is ignored", envGiteaToken)
	}
	if g.WebURL != "" {
		if !giteaOn {
			l.problem("gitea.web_url is set but gitea.base_url is not (%s)", keyToEnv["gitea.base_url"])
		}
		l.checkURL("gitea.web_url", &g.WebURL)
	}

	if bbOn {
		l.checkURL("bitbucket_server.base_url", &b.BaseURL)
		if !serve && !c.Secrets.BitbucketServerToken.IsSet() {
			l.problem("%s is required because bitbucket_server.base_url is set", envBitbucketServerToken)
		}
	} else if !serve && c.Secrets.BitbucketServerToken.IsSet() {
		l.warn("%s is set but bitbucket_server is not enabled (bitbucket_server.base_url is unset); the token is ignored", envBitbucketServerToken)
	}

	if ghOn {
		l.checkURL("github.base_url", &h.BaseURL)
		if !serve && !c.Secrets.GitHubToken.IsSet() {
			l.problem("%s is required because github.base_url is set", envGitHubToken)
		}
	} else if !serve && c.Secrets.GitHubToken.IsSet() {
		l.warn("%s is set but github is not enabled (github.base_url is unset); the token is ignored", envGitHubToken)
	}
	if h.APIURL != "" {
		if !ghOn {
			l.problem("github.api_url is set but github.base_url is not (%s)", keyToEnv["github.base_url"])
		}
		l.checkURL("github.api_url", &h.APIURL)
	}

	if !giteaOn && !bbOn && !ghOn {
		l.problem("no provider enabled: set gitea.base_url (%s), bitbucket_server.base_url (%s) and/or github.base_url (%s)",
			keyToEnv["gitea.base_url"], keyToEnv["bitbucket_server.base_url"], keyToEnv["github.base_url"])
	}

	l.checkCACert("gitea.ca_cert", g.CACert)
	l.checkCACert("bitbucket_server.ca_cert", b.CACert)
	l.checkCACert("github.ca_cert", h.CACert)
	if g.InsecureSkipVerify {
		l.warn("TLS verification disabled for gitea")
	}
	if b.InsecureSkipVerify {
		l.warn("TLS verification disabled for bitbucket_server")
	}
	if h.InsecureSkipVerify {
		l.warn("TLS verification disabled for github")
	}
}

// checkURL validates *p as an http(s) URL without userinfo or fragment and
// normalizes it by trimming whitespace and trailing slashes. Messages never
// echo the raw value: only RedactURL output (when the URL is safe to show) or
// no URL at all.
func (l *loader) checkURL(key string, p *string) {
	raw := strings.TrimSpace(*p)
	*p = raw
	u, err := url.Parse(raw)
	if err != nil {
		l.problem("%s: not a valid URL", key)
		return
	}
	if u.User != nil {
		l.problem("credentials must not be embedded in %s", key)
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		l.problem("%s: scheme must be http or https", key)
		return
	}
	if u.Hostname() == "" {
		l.problem("%s: URL must have a host", key)
		return
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		l.problem("%s: URL must not contain a fragment (%s)", key, logging.RedactURL(raw))
		return
	}
	*p = strings.TrimRight(raw, "/")
}

func (l *loader) checkCACert(key, path string) {
	if path == "" {
		return
	}
	info, err := l.src.Stat(path)
	if err != nil {
		l.problem("%s: cannot read CA certificate file %q: %s", key, path, reason(err))
		return
	}
	if !info.Mode().IsRegular() {
		l.problem("%s: %q is not a regular file", key, path)
		return
	}
	if _, err := l.src.ReadFile(path); err != nil {
		l.problem("%s: cannot read CA certificate file %q: %s", key, path, reason(err))
	}
}

func (l *loader) validateDiff() {
	d := &l.cfg.Diff
	rng := func(key string, v, lo, hi int) {
		if !l.bad[key] && (v < lo || v > hi) {
			l.problem("%s: %d is out of range (%d-%d); values are never clamped", key, v, lo, hi)
		}
	}
	min := func(key string, v, lo int) {
		if !l.bad[key] && v < lo {
			l.problem("%s: %d is below the minimum %d", key, v, lo)
		}
	}
	rng("diff.extra_lines_before", d.ExtraLinesBefore, 0, 10)
	rng("diff.extra_lines_after", d.ExtraLinesAfter, 0, 10)
	for i, ext := range d.SkipExtendExtensions {
		if !strings.HasPrefix(ext, ".") {
			l.problem("diff.skip_extend_extensions[%d]: %q must start with \".\"", i, ext)
		}
	}
	if d.LargePatchPolicy != "clip" && d.LargePatchPolicy != "skip" {
		l.problem("diff.large_patch_policy: %q must be \"clip\" or \"skip\"", d.LargePatchPolicy)
	}
	min("diff.max_description_tokens", d.MaxDescriptionTokens, 1)
	min("diff.max_commits_tokens", d.MaxCommitsTokens, 1)
	min("diff.max_files_full_content", d.MaxFilesFullContent, 1)
	min("diff.max_file_bytes", d.MaxFileBytes, 1024)
	if !l.bad["diff.max_diff_bytes"] && !l.bad["diff.max_file_bytes"] && d.MaxDiffBytes < d.MaxFileBytes {
		l.problem("diff.max_diff_bytes: %d must be at least diff.max_file_bytes (%d)", d.MaxDiffBytes, d.MaxFileBytes)
	}

	if d.MaxTokens != nil && !l.bad["diff.max_tokens"] && *d.MaxTokens < minDiffMaxTokens {
		l.problem("diff.max_tokens: %d is below the minimum %d", *d.MaxTokens, minDiffMaxTokens)
	}

	for i, n := range d.IgnoreGeneratedFrameworks {
		if strings.TrimSpace(n) == "" {
			l.problem("diff.ignore_generated_frameworks[%d]: must not be empty", i)
			continue
		}
		if _, ok := data.GeneratedCode[n]; !ok {
			l.problem("diff.ignore_generated_frameworks[%d]: unknown framework %q (valid names: %s)",
				i, n, strings.Join(data.FrameworkNames(), ", "))
		}
	}
}

func (l *loader) validateIgnore() {
	ig := &l.cfg.Ignore
	for i, g := range ig.Glob {
		switch {
		case strings.TrimSpace(g) == "":
			l.problem("ignore.glob[%d]: must not be empty", i)
		case !doublestar.ValidatePattern(g):
			// The index is reported instead of the pattern, like ignore.regex.
			l.problem("ignore.glob[%d]: not a valid glob pattern (doublestar syntax)", i)
		}
	}
	for i, r := range ig.Regex {
		if _, err := regexp.Compile(r); err != nil {
			l.problem("ignore.regex[%d]: pattern does not compile as RE2 (%s)", i, regexpReason(err))
		}
	}
}

// regexpReason returns the syntax-error code without the offending
// expression.
func regexpReason(err error) string {
	var se *syntax.Error
	if errors.As(err, &se) {
		return string(se.Code)
	}
	return "invalid pattern"
}
