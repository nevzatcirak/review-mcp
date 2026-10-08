package config

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Load builds the effective configuration from compiled defaults, the optional
// TOML file named by REVIEW_MCP_CONFIG, and the environment (low to high
// precedence), then validates it.
//
// Failure contract (degraded start): Load never returns a nil Config or nil
// Report. When anything is wrong it returns a best-effort Config (defaults plus
// every value that could be applied), the Report, and a *ValidationError that
// lists every problem at once. Callers that can run degraded (the MCP server's
// server_info tool) should use the Config and Report anyway; callers that
// cannot should treat a non-nil error as fatal. The Config must not be used to
// perform reviews when err != nil.
//
// Load validates for stdio mode; LoadWith takes the mode.
func Load(src Source) (*Config, *Report, error) { return LoadWith(src, LoadOptions{}) }

// LoadOptions select the mode the configuration is validated for.
type LoadOptions struct {
	// Mode is ModeStdio (the zero value) or ModeServe.
	Mode Mode
	// Listen, when non-empty, overrides serve.listen (the serve command's
	// --listen flag). Its source is reported as OriginFlag.
	Listen string
}

// LoadWith is Load for the given options. In stdio mode the serve.* keys are
// read but ignored: they are never validated and a malformed serve.*
// environment value is not an error. In serve mode the rules of P6 spec §1.2
// apply on top of the stdio rules, except that the provider tokens (and,
// with serve.llm_key_source = header, the LLM API key) must be unset because
// they arrive with each request.
func LoadWith(src Source, opts LoadOptions) (*Config, *Report, error) {
	cfg := Defaults()
	rep := newReport()
	l := &loader{src: src, cfg: cfg, rep: rep, bad: map[string]bool{}, mode: opts.Mode}

	l.loadFile()
	l.loadEnv()
	if opts.Listen != "" {
		cfg.Serve.Listen = opts.Listen
		rep.Sources["serve.listen"] = OriginFlag
		delete(l.bad, "serve.listen")
	}
	l.loadSecrets()
	l.warnUnknownEnv()
	l.validate()

	if len(l.problems) > 0 {
		out := make([]string, len(l.problems))
		for i, p := range l.problems {
			out[i] = sanitize(p)
		}
		return cfg, rep, &ValidationError{Problems: out}
	}
	return cfg, rep, nil
}

type loader struct {
	src      Source
	cfg      *Config
	rep      *Report
	problems []string
	// bad marks keys whose layer value was malformed, so validation does not
	// pile a second, redundant complaint on top.
	bad map[string]bool
	// mode selects the mode-specific validation rules.
	mode Mode
}

func (l *loader) problem(format string, args ...any) {
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

func (l *loader) warn(format string, args ...any) {
	l.rep.Warnings = append(l.rep.Warnings, sanitize(fmt.Sprintf(format, args...)))
}

// secretKeyRE matches any key whose last segment looks like a credential.
var secretKeyRE = regexp.MustCompile(`(?i)^(token|secret|password|api_key|apikey|access_token)$`)

func isSecretKey(key string) bool {
	last := key
	if i := strings.LastIndex(key, "."); i >= 0 {
		last = key[i+1:]
	}
	return secretKeyRE.MatchString(last)
}

// reason strips the duplicated path from *fs.PathError messages.
func reason(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

func (l *loader) loadFile() {
	path, ok := l.src.LookupEnv(EnvConfig)
	if !ok || path == "" {
		return
	}
	data, err := l.src.ReadFile(path)
	if err != nil {
		l.problem("cannot read config file %q: %s", path, reason(err))
		return
	}

	var fc Config
	md, decErr := toml.Decode(string(data), &fc)
	var pe toml.ParseError
	if errors.As(decErr, &pe) {
		// Upstream messages can echo file content (for example an unquoted
		// secret token), so only the line number is reported.
		l.problem("config file %q: syntax error at line %d", path, pe.Position.Line)
		return
	}

	// Classify every key actually present in the file, before trusting any
	// decoded value. Secrets are always reported (even under an unknown
	// section); unknown keys are reported once per unknown prefix.
	rejected := false
	var secretPrefixes, unknownPrefixes []string
	for _, k := range md.Keys() {
		key := strings.Join(k, ".")
		if sections[key] {
			continue // a known section header, also a nested one ([context.repo])
		}
		if underAny(key, secretPrefixes) {
			continue
		}
		if isSecretKey(key) {
			rejected = true
			secretPrefixes = append(secretPrefixes, key)
			if env, ok := keyToEnv[key]; ok {
				l.problem("secret %q must not be set in the config file; use environment variable %s", key, env)
			} else {
				l.problem("secret %q must not be set in the config file; use an environment variable", key)
			}
			continue
		}
		if underAny(key, unknownPrefixes) {
			continue
		}
		if _, ok := byKey[key]; !ok {
			rejected = true
			unknownPrefixes = append(unknownPrefixes, key)
			l.problem("unknown config key %q in %s", key, path)
		}
	}
	if decErr != nil {
		l.problem("%s", decodeProblem(path, decErr))
		return
	}
	if rejected {
		return // do not apply a partially rejected file
	}

	for _, k := range md.Keys() {
		key := strings.Join(k, ".")
		e, ok := byKey[key]
		if !ok {
			continue
		}
		reflect.ValueOf(e.ptr(l.cfg)).Elem().Set(reflect.ValueOf(e.ptr(&fc)).Elem())
		l.rep.Sources[key] = OriginFile
	}
}

// lastKeyRE extracts the key from BurntSushi/toml type errors, which do not
// expose it in a typed field (v1.6.0): `toml: line N (last key "llm.x"): ...`.
// Only the key is used, and only when it is a known config key; no other part
// of the upstream message is ever relayed because it may echo file values.
var lastKeyRE = regexp.MustCompile(`\(last key "([^"]*)"\)`)

func decodeProblem(path string, err error) string {
	if m := lastKeyRE.FindStringSubmatch(err.Error()); m != nil {
		if _, ok := byKey[m[1]]; ok {
			return fmt.Sprintf("config file %q: invalid value type for key %q", path, m[1])
		}
	}
	return fmt.Sprintf("config file %q: invalid value type", path)
}

func underAny(key string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(key, p+".") {
			return true
		}
	}
	return false
}

func (l *loader) loadEnv() {
	for _, e := range table {
		raw, ok := l.src.LookupEnv(e.env)
		if !ok {
			continue
		}
		if err := setFromEnv(e.ptr(l.cfg), e.env, raw); err != nil {
			l.bad[e.key] = true
			if l.mode != ModeServe && isServeKey(e.key) {
				continue // stdio ignores serve.*: a malformed value is not an error
			}
			l.problems = append(l.problems, err.Error())
			continue
		}
		l.rep.Sources[e.key] = OriginEnv
	}
}

// loadSecrets reads the credential variables. A secret variable that is set
// but empty (or only whitespace) is treated as unset, and surrounding
// whitespace is trimmed from a non-empty value.
//
// A set-but-empty secret variable counts as unset (MCP clients commonly expand
// an undefined ${VAR} to ""), so a required secret still yields a "required"
// problem naming the variable.
func (l *loader) loadSecrets() {
	for _, s := range secretTable {
		raw, ok := l.src.LookupEnv(s.env)
		if !ok {
			continue
		}
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		*s.ptr(l.cfg) = NewSecret(v)
		l.rep.Sources[s.key] = OriginEnv
	}
}

func (l *loader) warnUnknownEnv() {
	var unknown []string
	for _, kv := range l.src.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, EnvPrefix) && !knownEnv[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for i, n := range unknown {
		if i > 0 && unknown[i-1] == n {
			continue
		}
		l.warn("unknown environment variable %s (ignored)", n)
	}
}

// setFromEnv parses raw strictly according to the type behind ptr and stores
// it. Errors name the variable and the expected type; the value is echoed only
// for typed scalars (never for secrets, which are not parsed here).
func setFromEnv(ptr any, env, raw string) error {
	if p, ok := ptr.(*[]string); ok {
		items, err := parseList(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", env, err)
		}
		*p = items
		return nil
	}
	if raw == "" {
		return fmt.Errorf("%s is set but empty (expected %s; unset the variable to keep the default)", env, expectedType(ptr))
	}
	switch p := ptr.(type) {
	case *string:
		*p = raw
	case **string:
		v := raw
		*p = &v
	case *int:
		v, err := parseInt(raw)
		if err != nil {
			return badValue(env, raw, ptr)
		}
		*p = v
	case **int:
		v, err := parseInt(raw)
		if err != nil {
			return badValue(env, raw, ptr)
		}
		*p = &v
	case **int64:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return badValue(env, raw, ptr)
		}
		*p = &v
	case *float64:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return badValue(env, raw, ptr)
		}
		*p = v
	case **float64:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return badValue(env, raw, ptr)
		}
		*p = &v
	case *bool:
		v, err := parseBool(raw)
		if err != nil {
			return badValue(env, raw, ptr)
		}
		*p = v
	default:
		return fmt.Errorf("%s: internal error: unsupported field type %T", env, ptr)
	}
	return nil
}

func parseInt(s string) (int, error) {
	v, err := strconv.ParseInt(s, 10, strconv.IntSize)
	return int(v), err
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, errors.New("not a boolean")
}

// parseList splits a comma-separated list. An empty (or all-blank) string is
// the empty list; empty items are rejected.
func parseList(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("empty item at position %d in comma-separated list", i+1)
		}
		out = append(out, p)
	}
	return out, nil
}

func expectedType(ptr any) string {
	switch ptr.(type) {
	case *string, **string:
		return "a non-empty string"
	case *int, **int, **int64:
		return "a base-10 integer"
	case *float64, **float64:
		return "a decimal number"
	case *bool:
		return "a boolean (true, false, 1 or 0)"
	case *[]string:
		return "a comma-separated list"
	}
	return "a value"
}

func badValue(env, raw string, ptr any) error {
	return fmt.Errorf("%s: invalid value %q (expected %s)", env, raw, expectedType(ptr))
}
