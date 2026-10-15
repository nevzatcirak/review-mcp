package config

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/logging"
)

// Fallback models (X-38, v2.1 spec V-6 §3.1): up to MaxFallbacks entries of
// [[llm.fallback]], tried in order when a model call cannot be served by the
// current model. The environment configures entry N (1 to 3) with
// REVIEW_MCP_LLM_FALLBACK_<N>_<FIELD>; the API key of a fallback is a secret
// and comes from REVIEW_MCP_LLM_FALLBACK_<N>_API_KEY only.

// MaxFallbacks is the most fallback models.
const MaxFallbacks = 3

// LLMFallback is one fallback model. Unset fields take the primary's value
// (EffectiveFallback), except ReasoningEffort, which is unset by default,
// and ContextWindow, which is resolved from the fallback's endpoint like an
// unset llm.context_window (X-15).
type LLMFallback struct {
	Model           string  `toml:"model" json:"model"`
	BaseURL         string  `toml:"base_url" json:"base_url"`
	ContextWindow   int     `toml:"context_window" json:"context_window"`
	MaxOutputTokens *int    `toml:"max_output_tokens" json:"max_output_tokens"`
	ReasoningEffort *string `toml:"reasoning_effort" json:"reasoning_effort"`
	TimeoutSeconds  int     `toml:"timeout_seconds" json:"timeout_seconds"`
	// APIKey is REVIEW_MCP_LLM_FALLBACK_<N>_API_KEY; never in the file.
	APIKey Secret `toml:"-" json:"-"`
}

// Key sources of a fallback (FallbackKey), as server_info shows them.
const (
	// KeyOwn: the fallback's own REVIEW_MCP_LLM_FALLBACK_<N>_API_KEY.
	KeyOwn = "own"
	// KeyPrimary: the primary key, because the fallback's base URL is the
	// primary's.
	KeyPrimary = "primary"
	// KeyNone: no key is sent.
	KeyNone = "none"
)

// fallbackField is one field of a fallback entry: its file key below
// llm.fallback, its environment suffix and its value in an entry.
type fallbackField struct {
	key, env string
	ptr      func(*LLMFallback) any
}

var fallbackFields = []fallbackField{
	{"model", "MODEL", func(f *LLMFallback) any { return &f.Model }},
	{"base_url", "BASE_URL", func(f *LLMFallback) any { return &f.BaseURL }},
	{"context_window", "CONTEXT_WINDOW", func(f *LLMFallback) any { return &f.ContextWindow }},
	{"max_output_tokens", "MAX_OUTPUT_TOKENS", func(f *LLMFallback) any { return &f.MaxOutputTokens }},
	{"reasoning_effort", "REASONING_EFFORT", func(f *LLMFallback) any { return &f.ReasoningEffort }},
	{"timeout_seconds", "TIMEOUT_SECONDS", func(f *LLMFallback) any { return &f.TimeoutSeconds }},
}

// fallbackSection is the file section of the fallback entries.
const fallbackSection = "llm.fallback"

// fallbackEnv is the environment variable of field suffix of entry n
// (1-based).
func fallbackEnv(n int, suffix string) string {
	return "REVIEW_MCP_LLM_FALLBACK_" + strconv.Itoa(n) + "_" + suffix
}

// fallbackKeyEnvPattern names the key variables in messages.
const fallbackKeyEnvPattern = "REVIEW_MCP_LLM_FALLBACK_<N>_API_KEY"

func init() {
	sections[fallbackSection] = true
	keyToEnv[fallbackSection+".api_key"] = fallbackKeyEnvPattern
	for n := 1; n <= MaxFallbacks; n++ {
		for _, f := range fallbackFields {
			knownEnv[fallbackEnv(n, f.env)] = true
		}
		knownEnv[fallbackEnv(n, "API_KEY")] = true
	}
}

// isFallbackKey reports whether a file key is a field of llm.fallback.
func isFallbackKey(key string) bool {
	field, ok := strings.CutPrefix(key, fallbackSection+".")
	if !ok {
		return false
	}
	for _, f := range fallbackFields {
		if f.key == field {
			return true
		}
	}
	return false
}

// loadFallbackEnv applies the fallback variables on top of the file's
// entries (§3.1): entry N exists when the file has it or
// REVIEW_MCP_LLM_FALLBACK_<N>_MODEL is set, and every set variable of N
// replaces that field of the entry. Entry N without entry N-1 is a gap.
//
// DESIGN-QUESTION (WP-8c DQ-1): the spec says an environment entry exists
// when its _MODEL is set. The other variables of N (the API key above all,
// which the file may not hold) also apply to a file entry N; a variable of
// an N that neither the file nor _MODEL creates is a problem, so a
// misnumbered variable is not silently ignored. Recommendation: keep.
func (l *loader) loadFallbackEnv() {
	fb := &l.cfg.LLM.Fallback
	for n := 1; n <= MaxFallbacks; n++ {
		if _, ok := l.src.LookupEnv(fallbackEnv(n, "MODEL")); ok && n > len(*fb) {
			if n > len(*fb)+1 {
				l.problem("%s is set but fallback %d is not configured; fallbacks are numbered from 1 without gaps",
					fallbackEnv(n, "MODEL"), len(*fb)+1)
				continue
			}
			*fb = append(*fb, LLMFallback{})
		}
		for _, f := range fallbackFields {
			env := fallbackEnv(n, f.env)
			raw, ok := l.src.LookupEnv(env)
			if !ok {
				continue
			}
			if n > len(*fb) {
				l.problem("%s is set but fallback %d has no model; set %s", env, n, fallbackEnv(n, "MODEL"))
				continue
			}
			if err := setFromEnv(f.ptr(&(*fb)[n-1]), env, raw); err != nil {
				l.problems = append(l.problems, err.Error())
				l.bad[fallbackEnv(n, "")] = true
				continue
			}
			l.rep.Sources[fallbackSection] = OriginEnv
		}
		env := fallbackEnv(n, "API_KEY")
		raw, ok := l.src.LookupEnv(env)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		if n > len(*fb) {
			l.problem("%s is set but fallback %d has no model; set %s", env, n, fallbackEnv(n, "MODEL"))
			continue
		}
		(*fb)[n-1].APIKey = NewSecret(strings.TrimSpace(raw))
	}
}

// validateFallbacks checks the fallback entries (§3.1). A set field must be
// valid on its own; an unset one takes the primary's value.
func (l *loader) validateFallbacks() {
	fb := l.cfg.LLM.Fallback
	if len(fb) > MaxFallbacks {
		l.problem("llm.fallback: at most %d fallback models may be configured (found %d)", MaxFallbacks, len(fb))
		return
	}
	for i := range fb {
		f := &fb[i]
		n := i + 1
		if l.bad[fallbackEnv(n, "")] {
			continue
		}
		name := "llm.fallback " + strconv.Itoa(n)
		f.Model = strings.TrimSpace(f.Model)
		if f.Model == "" {
			l.problem("%s: model is required (%s)", name, fallbackEnv(n, "MODEL"))
		}
		if f.BaseURL != "" {
			l.checkURL(name+" base_url", &f.BaseURL)
		}
		if f.ContextWindow != 0 && f.ContextWindow < minContextWindow {
			l.problem("%s: context_window %d is below the minimum %d", name, f.ContextWindow, minContextWindow)
		}
		if v := f.MaxOutputTokens; v != nil {
			switch {
			case *v <= 0:
				l.problem("%s: max_output_tokens %d must be greater than 0", name, *v)
			case f.ContextWindow > 0 && *v >= f.ContextWindow:
				l.problem("%s: max_output_tokens %d must be less than its context_window (%d)", name, *v, f.ContextWindow)
			}
		}
		if f.ReasoningEffort != nil && strings.TrimSpace(*f.ReasoningEffort) == "" {
			l.problem("%s: reasoning_effort must not be empty when set", name)
		}
		if f.TimeoutSeconds != 0 && (f.TimeoutSeconds < 1 || f.TimeoutSeconds > 3600) {
			l.problem("%s: timeout_seconds %d is out of range (1-3600)", name, f.TimeoutSeconds)
		}
		// DESIGN-QUESTION (WP-8c DQ-2): in serve mode with
		// serve.llm_key_source = header the primary key comes from each
		// request and REVIEW_MCP_LLM_API_KEY must be unset; a server-side
		// fallback key would let every caller spend the operator's key, so
		// it is refused the same way. Such a fallback uses the caller's key
		// on the primary's base URL, and no key elsewhere. Recommendation:
		// keep.
		if l.mode == ModeServe && l.cfg.Serve.LLMKeySource == LLMKeySourceHeader && f.APIKey.IsSet() {
			l.problem("serve mode with serve.llm_key_source = header takes the LLM API key from request headers; unset %s",
				fallbackEnv(n, "API_KEY"))
		}
	}
}

// EffectiveFallback returns the configuration of fallback i (0-based) as a
// complete llm.* block: the fallback's model; its base URL, maximum output,
// timeout and context window where set, else the primary's base URL,
// maximum output and timeout, and 0 for the context window (resolved from
// the fallback's endpoint, X-15); its reasoning effort, else none. Every
// other key (temperature, seed, the retries and the rate limits) is the
// primary's. The result has no fallbacks of its own.
func (c *Config) EffectiveFallback(i int) LLM {
	f := c.LLM.Fallback[i]
	out := c.LLM
	out.Fallback = nil
	// The optional values are copied, so that the result shares no pointer
	// with the primary.
	out.MaxOutputTokens = clonePtr(c.LLM.MaxOutputTokens)
	out.Temperature = clonePtr(c.LLM.Temperature)
	out.Seed = clonePtr(c.LLM.Seed)
	out.Model = f.Model
	if f.BaseURL != "" {
		out.BaseURL = f.BaseURL
	}
	out.ContextWindow = f.ContextWindow
	if f.MaxOutputTokens != nil {
		v := *f.MaxOutputTokens
		out.MaxOutputTokens = &v
	}
	out.ReasoningEffort = nil
	if f.ReasoningEffort != nil {
		v := *f.ReasoningEffort
		out.ReasoningEffort = &v
	}
	if f.TimeoutSeconds != 0 {
		out.TimeoutSeconds = f.TimeoutSeconds
	}
	return out
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// FallbackKey returns the key fallback i (0-based) sends and its source
// (§3.1, the leak guard): its own key when set; else the primary key, but
// only when the fallback's effective base URL is the primary's after
// normalisation (NormalBaseURL); else no key. The primary key is never sent
// to another host.
func (c *Config) FallbackKey(i int) (Secret, string) {
	f := c.LLM.Fallback[i]
	switch {
	case f.APIKey.IsSet():
		return f.APIKey, KeyOwn
	case SameBaseURL(c.EffectiveFallback(i).BaseURL, c.LLM.BaseURL) && c.Secrets.LLMAPIKey.IsSet():
		return c.Secrets.LLMAPIKey, KeyPrimary
	}
	return Secret{}, KeyNone
}

// SameBaseURL reports whether a and b are the same base URL after
// normalisation (NormalBaseURL); an unparsable URL equals nothing.
func SameBaseURL(a, b string) bool {
	ua, errA := url.Parse(strings.TrimSpace(a))
	ub, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil || ua.Hostname() == "" || ub.Hostname() == "" {
		return false
	}
	return NormalBaseURL(ua) == NormalBaseURL(ub)
}

// NormalBaseURL is a base URL as review-mcp compares endpoints: the scheme
// and host in lower case, without a default port, credentials, query or
// fragment, and the path without a trailing slash.
func NormalBaseURL(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p := u.Port(); p != "" && p != defaultPort(u.Scheme) {
		host += ":" + p
	}
	return strings.ToLower(u.Scheme) + "://" + host + strings.TrimRight(u.EscapedPath(), "/")
}

func defaultPort(scheme string) string {
	if strings.EqualFold(scheme, "https") {
		return "443"
	}
	return "80"
}

// FallbackSummary describes one fallback model in server_info.
type FallbackSummary struct {
	Model string `json:"model"`
	// BaseURL is the effective base URL, redacted.
	BaseURL string `json:"base_url"`
	// Key is KeyOwn, KeyPrimary or KeyNone.
	Key string `json:"key"`
	// ContextWindow is the configured window, or nil when it is resolved
	// from the endpoint; server_info replaces nil with the resolution.
	ContextWindow any `json:"context_window"`
}

// fallbackSummaries lists the fallback models for Summary.
func (c *Config) fallbackSummaries() []FallbackSummary {
	out := []FallbackSummary{}
	if len(c.LLM.Fallback) > MaxFallbacks {
		return out
	}
	for i, f := range c.LLM.Fallback {
		_, src := c.FallbackKey(i)
		s := FallbackSummary{Model: f.Model, BaseURL: logging.RedactURL(c.EffectiveFallback(i).BaseURL), Key: src}
		if f.ContextWindow > 0 {
			s.ContextWindow = f.ContextWindow
		}
		out = append(out, s)
	}
	return out
}
