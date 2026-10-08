// Package tools holds the transport-agnostic logic of the MCP tools. It must
// not import the MCP SDK: the mcpserver package adapts these functions to it.
package tools

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/version"
)

// Status values reported by server_info.
const (
	// StatusOK means the configuration loaded and validated.
	StatusOK = "ok"
	// StatusConfigInvalid means the server runs degraded because the
	// configuration failed to load or validate.
	StatusConfigInvalid = "config_invalid"
)

// genericProblem is reported when the load error is not a
// *config.ValidationError. The error text is deliberately not echoed: only
// ValidationError is known to be free of secrets.
const genericProblem = "the configuration could not be loaded; see the server log on stderr for details"

// ProviderInfo describes an enabled provider.
type ProviderInfo struct {
	Kind    string `json:"kind" jsonschema:"provider kind, for example gitea or bitbucket_server"`
	BaseURL string `json:"base_url" jsonschema:"provider base URL with credentials and query values removed"`
}

// ServerInfoResult is the structured result of the server_info tool.
type ServerInfoResult struct {
	Name      string         `json:"name" jsonschema:"server name"`
	Version   string         `json:"version" jsonschema:"server version"`
	Commit    string         `json:"commit" jsonschema:"VCS commit the binary was built from, empty if unknown"`
	GoVersion string         `json:"go_version" jsonschema:"Go toolchain version the binary was built with"`
	Status    string         `json:"status" jsonschema:"ok, or config_invalid when the configuration has problems (degraded start)"`
	Problems  []string       `json:"problems" jsonschema:"configuration problems; empty when status is ok"`
	Warnings  []string       `json:"warnings" jsonschema:"non-fatal configuration warnings"`
	Providers []ProviderInfo `json:"providers" jsonschema:"enabled providers"`
	Config    config.Summary `json:"config" jsonschema:"effective non-secret configuration; secrets appear only as set or unset"`
	// Serve is present in serve mode only.
	Serve *ServeInfo `json:"serve,omitempty" jsonschema:"serve-mode transport details; absent in stdio mode"`
}

// ServeInfo describes the serve transport and the credential headers of the
// request that called server_info (P6 spec §1.3). Header values are never
// reported.
type ServeInfo struct {
	Transport    string `json:"transport" jsonschema:"always serve"`
	Listen       string `json:"listen" jsonschema:"the address the server listens on (host:port)"`
	LLMKeySource string `json:"llm_key_source" jsonschema:"header or server: where the LLM API key comes from"`
	// RequestHeaders maps each credential header name to set, unset or
	// malformed.
	RequestHeaders map[string]string `json:"request_headers" jsonschema:"for this request, each credential header as set, unset or malformed; never its value"`
}

// TransportServe is the ServeInfo.Transport value.
const TransportServe = "serve"

// ServerInfo builds the server_info result. cfg and rep may be nil (treated as
// defaults / no report). loadErr is the error returned by config loading: a
// *config.ValidationError contributes its problems; any other non-nil error
// yields one generic problem without echoing its text.
func ServerInfo(cfg *config.Config, rep *config.Report, loadErr error) ServerInfoResult {
	if cfg == nil {
		cfg = config.Defaults()
	}
	bi := version.Info()
	sum := cfg.Summary(rep)
	reportContextWindow(&sum, cfg)
	reportRepoContext(&sum, cfg)

	res := ServerInfoResult{
		Name:      ServerName,
		Version:   bi.Version,
		Commit:    bi.Commit,
		GoVersion: bi.GoVersion,
		Status:    StatusOK,
		Problems:  []string{},
		Warnings:  append([]string{}, sum.Warnings...),
		Providers: make([]ProviderInfo, 0, len(sum.Providers)),
		Config:    sum,
	}
	for _, p := range sum.Providers {
		res.Providers = append(res.Providers, ProviderInfo{Kind: p.Kind, BaseURL: p.BaseURL})
	}
	if loadErr != nil {
		res.Status = StatusConfigInvalid
		var ve *config.ValidationError
		if errors.As(loadErr, &ve) && len(ve.Problems) > 0 {
			res.Problems = append(res.Problems, ve.Problems...)
		} else {
			res.Problems = append(res.Problems, genericProblem)
		}
	}
	return res
}

// ContextWindowAuto is the llm.context_window value server_info reports
// while the window is unset and not yet resolved.
const ContextWindowAuto = "auto (endpoint)"

// reportContextWindow shows how an unset llm.context_window is (or will be)
// resolved (X-15): "auto (endpoint)" until the first call has asked the
// endpoint, then "<n> (endpoint, 90% of <m>)". server_info only reads the
// process-wide result of that probe; it never makes a request itself, so it
// stays free of network I/O. A configured value is reported as it is: its
// source field already says where it came from.
func reportContextWindow(sum *config.Summary, cfg *config.Config) {
	const key = "llm.context_window"
	v, ok := sum.Values[key]
	if !ok || cfg.LLM.ContextWindow > 0 {
		return
	}
	v.Value = ContextWindowAuto
	if n, src, ok := llm.ResolvedContextWindow(cfg.LLM.BaseURL, cfg.LLM.Model); ok {
		v.Value = strconv.Itoa(n) + " (" + src + ")"
	}
	sum.Values[key] = v
}

// reportRepoContext shows, when context.repo.enabled is set, whether the
// system git can serve it (§3.0 item 4): "enabled, git <version>" or
// "enabled, unavailable: <fixed reason>". The git version is checked once
// per process ("git --version", no network). Disabled, the value stays
// false.
func reportRepoContext(sum *config.Summary, cfg *config.Config) {
	const key = "context.repo.enabled"
	v, ok := sum.Values[key]
	if !ok || !cfg.Context.Repo.Enabled {
		return
	}
	v.Value = repoContextStatus()
	sum.Values[key] = v
}

// repoContextStatus is gitctx.Status; a variable so tests need no git.
var repoContextStatus = gitctx.Status

// ServerName is the implementation name reported to MCP clients.
const ServerName = "review-mcp"

// RenderServerInfoMarkdown renders r as portable markdown (the "client"
// profile of DQ-16): headings, lists and code spans only, never raw HTML.
// Every dynamic string is placed in a code span so it cannot inject markup.
func RenderServerInfoMarkdown(r ServerInfoResult) string {
	var b strings.Builder
	b.WriteString("# " + r.Name + " server info\n\n")
	b.WriteString("- Version: " + codeSpan(r.Version) + "\n")
	if r.Commit != "" {
		b.WriteString("- Commit: " + codeSpan(r.Commit) + "\n")
	}
	b.WriteString("- Go: " + codeSpan(r.GoVersion) + "\n")
	b.WriteString("- Status: " + codeSpan(r.Status) + "\n")

	if len(r.Problems) > 0 {
		b.WriteString("\n## Configuration problems\n\n")
		b.WriteString("The server is running in degraded mode. Fix these and restart it.\n\n")
		for _, p := range r.Problems {
			b.WriteString("- " + codeSpan(p) + "\n")
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, w := range r.Warnings {
			b.WriteString("- " + codeSpan(w) + "\n")
		}
	}

	b.WriteString("\n## Providers\n\n")
	if len(r.Providers) == 0 {
		b.WriteString("None enabled.\n")
	}
	for _, p := range r.Providers {
		b.WriteString("- " + codeSpan(p.Kind) + ": " + codeSpan(p.BaseURL) + "\n")
	}

	if r.Serve != nil {
		b.WriteString("\n## Serve\n\n")
		b.WriteString("- Transport: " + codeSpan(r.Serve.Transport) + "\n")
		b.WriteString("- Listen: " + codeSpan(r.Serve.Listen) + "\n")
		b.WriteString("- LLM key source: " + codeSpan(r.Serve.LLMKeySource) + "\n")
		b.WriteString("\nCredential headers of this request:\n\n")
		for _, k := range sortedKeys(r.Serve.RequestHeaders) {
			b.WriteString("- " + codeSpan(k) + ": " + codeSpan(r.Serve.RequestHeaders[k]) + "\n")
		}
	}

	b.WriteString("\n## Secrets\n\n")
	for _, k := range sortedKeys(r.Config.Secrets) {
		b.WriteString("- " + codeSpan(k) + ": " + r.Config.Secrets[k] + "\n")
	}

	b.WriteString("\n## Effective configuration\n\n")
	for _, k := range sortedKeys(r.Config.Values) {
		v := r.Config.Values[k]
		b.WriteString("- " + codeSpan(k) + " = " + codeSpan(jsonText(v.Value)) + " (" + string(v.Source) + ")\n")
	}
	return b.String()
}

func jsonText(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return "<unrenderable>"
	}
	return string(out)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// codeSpan wraps s in a markdown code span. The delimiter is one backtick
// longer than the longest backtick run in s, and line breaks become spaces so
// the span cannot be terminated early or break the surrounding list item.
func codeSpan(s string) string { return mdutil.CodeSpan(s) }
