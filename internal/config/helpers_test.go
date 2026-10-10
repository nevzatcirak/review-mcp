package config

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// Obviously fake, distinctive secret values used to prove nothing leaks.
const (
	fakeLLMKey      = "FAKE-llm-key-ZQ7X-do-not-leak"
	fakeGitea       = "FAKE-gitea-token-ZQ7X-do-not-leak"
	fakeBitbkt      = "FAKE-bitbucket-token-ZQ7X-do-not-leak"
	fakeGitHub      = "FAKE-github-token-ZQ7X-do-not-leak"
	fakeURLUserinfo = "FAKE-url-password-ZQ7X"
	fakeAccess      = "FAKE-serve-access-token-ZQ7X-do-not-leak"
)

var allFakeSecrets = []string{fakeLLMKey, fakeGitea, fakeBitbkt, fakeGitHub, fakeURLUserinfo, fakeAccess}

// minimalEnv is the smallest valid environment (Gitea enabled).
func minimalEnv() map[string]string {
	return map[string]string{
		"REVIEW_MCP_LLM_BASE_URL":       "https://llm.example.com/v1",
		"REVIEW_MCP_LLM_MODEL":          "example-model",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
		"REVIEW_MCP_LLM_API_KEY":        fakeLLMKey,
		"REVIEW_MCP_GITEA_BASE_URL":     "https://your-gitea.example",
		"REVIEW_MCP_GITEA_TOKEN":        fakeGitea,
	}
}

func envWith(extra map[string]string) map[string]string {
	env := minimalEnv()
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func memSrc(env map[string]string, files map[string]string) MemSource {
	m := fstest.MapFS{}
	for p, c := range files {
		m[strings.TrimLeft(p, "/")] = &fstest.MapFile{Data: []byte(c)}
	}
	return MemSource{Env: env, FS: m}
}

func problemsOf(t *testing.T, err error) []string {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %T (%v)", err, err)
	}
	return ve.Problems
}

func mustLoad(t *testing.T, src Source) (*Config, *Report) {
	t.Helper()
	cfg, rep, err := Load(src)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	return cfg, rep
}

func hasProblem(problems []string, sub string) bool {
	for _, p := range problems {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

func assertNoSecrets(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range allFakeSecrets {
		if strings.Contains(text, s) {
			t.Errorf("%s leaks secret %q: %s", what, s, text)
		}
	}
}

// unreadableFS stats normally but fails to open (and so to read) any file.
type unreadableFS struct{ fstest.MapFS }

func (u unreadableFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}

func (u unreadableFS) Stat(name string) (fs.FileInfo, error) { return u.MapFS.Stat(name) }

func (u unreadableFS) ReadFile(name string) ([]byte, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
}
