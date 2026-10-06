package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// serveEnv is the smallest valid serve-mode environment: Gitea enabled by
// its base URL only, no secret in the environment (llm_key_source = header).
func serveEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"REVIEW_MCP_LLM_BASE_URL":       "https://llm.example.com/v1",
		"REVIEW_MCP_LLM_MODEL":          "example-model",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
		"REVIEW_MCP_GITEA_BASE_URL":     "https://your-gitea.example",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func loadServe(t *testing.T, src Source) (*Config, *Report, error) {
	t.Helper()
	return LoadWith(src, LoadOptions{Mode: ModeServe})
}

func serveProblems(t *testing.T, env map[string]string, files map[string]string) []string {
	t.Helper()
	_, _, err := loadServe(t, memSrc(env, files))
	if err == nil {
		return nil
	}
	ps := problemsOf(t, err)
	for _, p := range ps {
		assertNoSecrets(t, "problem", p)
	}
	assertNoSecrets(t, "error", err.Error())
	return ps
}

func TestWithSecretsIsShallowCopyAndLeavesOriginalUnchanged(t *testing.T) {
	orig, _ := mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_LLM_MAX_OUTPUT_TOKENS": "1000"})})
	before := *orig
	beforeJSON, _ := json.Marshal(orig)

	req := Secrets{GiteaToken: NewSecret("request-token"), LLMAPIKey: NewSecret("request-key")}
	cp := orig.WithSecrets(req)

	if cp == orig {
		t.Fatal("WithSecrets returned the original pointer")
	}
	if cp.Secrets.GiteaToken.Reveal() != "request-token" || cp.Secrets.LLMAPIKey.Reveal() != "request-key" ||
		cp.Secrets.BitbucketServerToken.IsSet() || cp.Secrets.ServeAccessToken.IsSet() {
		t.Errorf("copy secrets = %+v", cp.Secrets)
	}
	// The original keeps its own secrets and every other value.
	if orig.Secrets.GiteaToken.Reveal() != fakeGitea || orig.Secrets.LLMAPIKey.Reveal() != fakeLLMKey {
		t.Error("WithSecrets modified the original's secrets")
	}
	if !reflect.DeepEqual(before, *orig) {
		t.Error("WithSecrets modified the original")
	}
	afterJSON, _ := json.Marshal(orig)
	if string(beforeJSON) != string(afterJSON) {
		t.Error("the original's serialized form changed")
	}
	// Shallow: non-secret fields are shared, not deep-copied.
	if cp.LLM.MaxOutputTokens != orig.LLM.MaxOutputTokens {
		t.Error("optional pointer was copied, want shared")
	}
	if &cp.Diff.SkipExtendExtensions[0] != &orig.Diff.SkipExtendExtensions[0] {
		t.Error("slice backing array was copied, want shared")
	}
	cp.Secrets = Secrets{}
	if !orig.Secrets.GiteaToken.IsSet() {
		t.Error("changing the copy's secrets changed the original")
	}
}

func TestServeDefaults(t *testing.T) {
	d := Defaults().Serve
	want := Serve{Listen: "127.0.0.1:8787", LLMKeySource: "header", AllowedOrigins: []string{}, MaxConcurrentCalls: 4}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("serve defaults = %+v, want %+v", d, want)
	}
	cfg, _, err := loadServe(t, memSrc(serveEnv(nil), nil))
	if err != nil {
		t.Fatalf("minimal serve env: %v", err)
	}
	if !reflect.DeepEqual(cfg.Serve, want) {
		t.Errorf("loaded serve = %+v", cfg.Serve)
	}
}

func TestStdioIgnoresServeKeys(t *testing.T) {
	env := envWith(map[string]string{
		"REVIEW_MCP_SERVE_LISTEN":               "not-an-address",
		"REVIEW_MCP_SERVE_TLS_CERT":             "/missing/cert.pem",
		"REVIEW_MCP_SERVE_ALLOW_INSECURE_HTTP":  "maybe", // malformed bool
		"REVIEW_MCP_SERVE_LLM_KEY_SOURCE":       "bogus",
		"REVIEW_MCP_SERVE_ALLOWED_ORIGINS":      "https://app.example.com/path",
		"REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS": "abc", // malformed int
		"REVIEW_MCP_SERVE_ACCESS_TOKEN":         fakeAccess,
	})
	_, rep, err := Load(memSrc(env, nil))
	if err != nil {
		t.Fatalf("stdio must ignore serve.* keys, got %v", err)
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "SERVE") || strings.Contains(w, "serve.") {
			t.Errorf("stdio warned about a serve key: %s", w)
		}
	}

	// A [serve] section in the file is accepted in stdio mode too.
	file := "[serve]\nlisten = \"0.0.0.0:1\"\nmax_concurrent_calls = 1000\n"
	if _, _, err := Load(memSrc(envWith(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"}), map[string]string{"/c.toml": file})); err != nil {
		t.Fatalf("stdio with a [serve] section: %v", err)
	}
}

// TestServeRefusesEnvProviderTokens: [canary] (P6 §1.6 #2, validation part).
func TestServeRefusesEnvProviderTokens(t *testing.T) {
	ps := serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_GITEA_TOKEN": fakeGitea}), nil)
	if !hasProblem(ps, "serve mode takes provider tokens from request headers; unset REVIEW_MCP_GITEA_TOKEN") {
		t.Errorf("gitea token in env not refused: %v", ps)
	}
	// Refused even when the provider is not enabled.
	ps = serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_BITBUCKET_SERVER_TOKEN": fakeBitbkt}), nil)
	if !hasProblem(ps, "serve mode takes provider tokens from request headers; unset REVIEW_MCP_BITBUCKET_SERVER_TOKEN") {
		t.Errorf("bitbucket token in env not refused: %v", ps)
	}
	// An enabled provider needs no token in serve mode, only its base URL.
	env := serveEnv(map[string]string{"REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com/stash"})
	if ps := serveProblems(t, env, nil); ps != nil {
		t.Errorf("serve with two providers and no env tokens: %v", ps)
	}
	// X-2 is unchanged: at least one provider must be enabled.
	env = serveEnv(nil)
	delete(env, "REVIEW_MCP_GITEA_BASE_URL")
	if ps := serveProblems(t, env, nil); !hasProblem(ps, "no provider enabled") {
		t.Errorf("no provider: %v", ps)
	}
}

func TestServeLLMKeySourceRules(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string // nil: valid
	}{
		{"header, no key", nil, nil},
		{"header, key in env", map[string]string{"REVIEW_MCP_LLM_API_KEY": fakeLLMKey},
			[]string{"serve mode with serve.llm_key_source = header takes the LLM API key from request headers; unset REVIEW_MCP_LLM_API_KEY"}},
		{"server, nothing", map[string]string{"REVIEW_MCP_SERVE_LLM_KEY_SOURCE": "server"},
			[]string{"REVIEW_MCP_LLM_API_KEY is required because serve.llm_key_source is server",
				"REVIEW_MCP_SERVE_ACCESS_TOKEN is required because serve.llm_key_source is server"}},
		{"server, key only", map[string]string{"REVIEW_MCP_SERVE_LLM_KEY_SOURCE": "server", "REVIEW_MCP_LLM_API_KEY": fakeLLMKey},
			[]string{"REVIEW_MCP_SERVE_ACCESS_TOKEN is required because serve.llm_key_source is server"}},
		{"server, both", map[string]string{"REVIEW_MCP_SERVE_LLM_KEY_SOURCE": "server", "REVIEW_MCP_LLM_API_KEY": fakeLLMKey, "REVIEW_MCP_SERVE_ACCESS_TOKEN": fakeAccess}, nil},
		{"header with access token", map[string]string{"REVIEW_MCP_SERVE_ACCESS_TOKEN": fakeAccess}, nil},
		{"bogus source", map[string]string{"REVIEW_MCP_SERVE_LLM_KEY_SOURCE": "Header"},
			[]string{`serve.llm_key_source: "Header" must be "header" or "server"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ps := serveProblems(t, serveEnv(tc.env), nil)
			if tc.want == nil && ps != nil {
				t.Fatalf("unexpected problems: %v", ps)
			}
			if len(ps) != len(tc.want) {
				t.Fatalf("problems = %v, want %v", ps, tc.want)
			}
			for _, w := range tc.want {
				if !hasProblem(ps, w) {
					t.Errorf("missing %q in %v", w, ps)
				}
			}
		})
	}
}

// TestServeNonLoopbackBindNeedsTLSOrOptOut: [canary] (P6 §1.6 #4).
func TestServeNonLoopbackBindNeedsTLSOrOptOut(t *testing.T) {
	const pem = "-----BEGIN PLACEHOLDER-----\n"
	files := map[string]string{"/tls/cert.pem": pem, "/tls/key.pem": pem}
	tlsEnv := map[string]string{"REVIEW_MCP_SERVE_TLS_CERT": "/tls/cert.pem", "REVIEW_MCP_SERVE_TLS_KEY": "/tls/key.pem"}
	with := func(listen string, extra map[string]string) map[string]string {
		env := serveEnv(extra)
		env["REVIEW_MCP_SERVE_LISTEN"] = listen
		return env
	}
	const insecure = "is not a loopback address"

	for _, listen := range []string{"0.0.0.0:8787", ":8787", "192.0.2.10:8787", "[::]:8787", "review.example.com:8787"} {
		ps := serveProblems(t, with(listen, nil), nil)
		if len(ps) != 1 || !hasProblem(ps, insecure) {
			t.Errorf("%s without TLS: problems = %v", listen, ps)
		}
	}
	for _, listen := range []string{"127.0.0.1:8787", "127.3.4.5:1", "[::1]:65535", "localhost:8787", "LOCALHOST:8787"} {
		if ps := serveProblems(t, with(listen, nil), nil); ps != nil {
			t.Errorf("%s (loopback): %v", listen, ps)
		}
	}
	// TLS files configured and readable: accepted.
	if ps := serveProblems(t, with("0.0.0.0:8787", tlsEnv), files); ps != nil {
		t.Errorf("0.0.0.0 with TLS: %v", ps)
	}
	// Explicit opt-out: accepted, with a startup warning.
	_, rep, err := loadServe(t, memSrc(with("0.0.0.0:8787", map[string]string{"REVIEW_MCP_SERVE_ALLOW_INSECURE_HTTP": "true"}), nil))
	if err != nil {
		t.Errorf("0.0.0.0 with allow_insecure_http: %v", err)
	}
	if !hasProblem(rep.Warnings, "serve.allow_insecure_http is true") {
		t.Errorf("no warning for allow_insecure_http: %v", rep.Warnings)
	}
	// TLS files must be readable at startup.
	ps := serveProblems(t, with("0.0.0.0:8787", tlsEnv), map[string]string{"/tls/cert.pem": pem})
	if !hasProblem(ps, "serve.tls_key: cannot read TLS key file") || hasProblem(ps, insecure) {
		t.Errorf("missing key file: %v", ps)
	}
	mfs := fstest.MapFS{"tls/cert.pem": {Data: []byte(pem)}, "tls/key.pem": {Data: []byte(pem)}}
	src := MemSource{Env: with("0.0.0.0:8787", tlsEnv), FS: unreadableFS{mfs}}
	_, _, err = loadServe(t, src)
	if err == nil || !hasProblem(problemsOf(t, err), "serve.tls_cert: cannot read TLS certificate file") {
		t.Errorf("unreadable cert: %v", err)
	}
	// Both or neither.
	ps = serveProblems(t, with("127.0.0.1:8787", map[string]string{"REVIEW_MCP_SERVE_TLS_CERT": "/tls/cert.pem"}), files)
	if !hasProblem(ps, "serve.tls_cert and serve.tls_key must be set together") {
		t.Errorf("cert without key: %v", ps)
	}
}

func TestServeListenAndRangeRules(t *testing.T) {
	bad := map[string]string{
		"127.0.0.1":       "must be host:port",
		"127.0.0.1:0":     "must be host:port",
		"127.0.0.1:65536": "must be host:port",
		"127.0.0.1:http":  "must be host:port",
		"127.0.0.1:+80":   "must be host:port",
	}
	for listen, want := range bad {
		ps := serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_SERVE_LISTEN": listen}), nil)
		if len(ps) != 1 || !hasProblem(ps, want) {
			t.Errorf("listen %q: %v", listen, ps)
		}
	}
	for n, ok := range map[string]bool{"0": false, "1": true, "64": true, "65": false} {
		ps := serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS": n}), nil)
		if ok != (ps == nil) || (!ok && !hasProblem(ps, "serve.max_concurrent_calls: "+n+" is out of range (1-64)")) {
			t.Errorf("max_concurrent_calls %s: %v", n, ps)
		}
	}
	// A malformed serve value is an error in serve mode.
	ps := serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS": "abc"}), nil)
	if len(ps) != 1 || !hasProblem(ps, "REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS: invalid value") {
		t.Errorf("malformed max_concurrent_calls: %v", ps)
	}
}

func TestServeAllowedOrigins(t *testing.T) {
	good := "https://app.example.com,http://localhost:3000,https://[::1]:8443"
	if ps := serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_SERVE_ALLOWED_ORIGINS": good}), nil); ps != nil {
		t.Errorf("valid origins: %v", ps)
	}
	for _, o := range []string{"https://app.example.com/", "https://app.example.com/x", "ftp://app.example.com",
		"https://user:" + fakeURLUserinfo + "@app.example.com", "app.example.com", "https://APP.example.com",
		"https://app.example.com?x=1", "https://app.example.com:0", "null", "*"} {
		ps := serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_SERVE_ALLOWED_ORIGINS": o}), nil)
		if len(ps) != 1 || !hasProblem(ps, "serve.allowed_origins[0]: not an exact origin") {
			t.Errorf("origin %q: %v", o, ps)
		}
	}
}

func TestServeListenFlagOverride(t *testing.T) {
	env := serveEnv(map[string]string{"REVIEW_MCP_SERVE_LISTEN": "127.0.0.1:9000"})
	cfg, rep, err := LoadWith(memSrc(env, nil), LoadOptions{Mode: ModeServe, Listen: "127.0.0.1:9100"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Serve.Listen != "127.0.0.1:9100" || rep.Sources["serve.listen"] != OriginFlag {
		t.Errorf("listen = %q from %q", cfg.Serve.Listen, rep.Sources["serve.listen"])
	}
	// The flag value is validated like any other.
	_, _, err = LoadWith(memSrc(serveEnv(nil), nil), LoadOptions{Mode: ModeServe, Listen: "0.0.0.0:8787"})
	if err == nil || !hasProblem(problemsOf(t, err), "is not a loopback address") {
		t.Errorf("flag 0.0.0.0: %v", err)
	}
}

func TestServeAllViolationsTogetherAndTokenFree(t *testing.T) {
	env := serveEnv(map[string]string{
		"REVIEW_MCP_GITEA_TOKEN":                fakeGitea,
		"REVIEW_MCP_BITBUCKET_SERVER_TOKEN":     fakeBitbkt,
		"REVIEW_MCP_LLM_API_KEY":                fakeLLMKey,
		"REVIEW_MCP_SERVE_ACCESS_TOKEN":         fakeAccess,
		"REVIEW_MCP_SERVE_LISTEN":               "0.0.0.0:8787",
		"REVIEW_MCP_SERVE_ALLOWED_ORIGINS":      "https://u:" + fakeURLUserinfo + "@app.example.com",
		"REVIEW_MCP_SERVE_MAX_CONCURRENT_CALLS": "100",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW":         "10",
	})
	ps := serveProblems(t, env, nil) // also asserts no secret in any problem
	for _, want := range []string{
		"unset REVIEW_MCP_GITEA_TOKEN", "unset REVIEW_MCP_BITBUCKET_SERVER_TOKEN", "unset REVIEW_MCP_LLM_API_KEY",
		"is not a loopback address", "serve.allowed_origins[0]", "serve.max_concurrent_calls", "llm.context_window",
	} {
		if !hasProblem(ps, want) {
			t.Errorf("missing %q in %v", want, ps)
		}
	}
}

func TestServeAccessTokenInFileRejected(t *testing.T) {
	file := "[serve]\naccess_token = \"" + fakeAccess + "\"\n"
	env := serveEnv(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"})
	ps := serveProblems(t, env, map[string]string{"/c.toml": file})
	if !hasProblem(ps, `secret "serve.access_token" must not be set in the config file; use environment variable REVIEW_MCP_SERVE_ACCESS_TOKEN`) {
		t.Errorf("problems = %v", ps)
	}
}

func TestSummaryIncludesServeKeys(t *testing.T) {
	cfg, rep, err := loadServe(t, memSrc(serveEnv(map[string]string{"REVIEW_MCP_SERVE_ACCESS_TOKEN": fakeAccess}), nil))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Summary(rep)
	for _, k := range []string{"serve.listen", "serve.tls_cert", "serve.tls_key", "serve.allow_insecure_http",
		"serve.llm_key_source", "serve.allowed_origins", "serve.max_concurrent_calls"} {
		if _, ok := s.Values[k]; !ok {
			t.Errorf("summary lacks %s", k)
		}
	}
	if s.Values["serve.listen"].Value != "127.0.0.1:8787" {
		t.Errorf("serve.listen = %v", s.Values["serve.listen"])
	}
	if s.Secrets["serve.access_token"] != "set" || s.Secrets["gitea.token"] != "unset" || s.Secrets["llm.api_key"] != "unset" {
		t.Errorf("secrets = %v", s.Secrets)
	}
	out, _ := json.Marshal(s)
	assertNoSecrets(t, "summary", string(out))
}

func TestIsLoopbackHost(t *testing.T) {
	for h, want := range map[string]bool{
		"localhost": true, "LocalHost": true, "127.0.0.1": true, "127.255.0.9": true, "::1": true,
		"": false, "0.0.0.0": false, "::": false, "192.0.2.1": false, "localhost.example.com": false,
		"rebind.example": false, "128.0.0.1": false,
	} {
		if got := IsLoopbackHost(h); got != want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", h, got, want)
		}
	}
}
