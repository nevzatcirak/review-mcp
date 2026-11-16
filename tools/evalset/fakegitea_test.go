package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: the test server of git http-backend; Go is far past 1.6.3
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeGitea is a small Gitea for the tests: the API calls setup makes, and
// git's smart HTTP through git http-backend (CGI), on bare repositories in
// a temporary directory. It checks the token on every request.
type fakeGitea struct {
	t     *testing.T
	srv   *httptest.Server
	root  string // GIT_PROJECT_ROOT: <root>/<owner>/<name>.git
	login string
	token string

	mu     sync.Mutex
	pulls  map[string][]map[string]any // "owner/name" -> pull requests
	authOK int                         // requests that carried the token
}

// newFakeGitea starts the server, or skips the test where git http-backend
// is not usable (Windows, as in internal/gitctx).
func newFakeGitea(t *testing.T, login, token string) *fakeGitea {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("git http-backend over CGI is not used on Windows (as in internal/gitctx)")
	}
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git is not installed")
	}
	backend := filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git http-backend is not installed")
	}
	f := &fakeGitea{t: t, root: t.TempDir(), login: login, token: token, pulls: map[string][]map[string]any{}}
	git := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + f.root, "GIT_HTTP_EXPORT_ALL=1"}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			if r.Header.Get("Authorization") != "token "+token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			f.count()
			f.api(w, r)
			return
		}
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("evalset:"+token))
		if r.Header.Get("Authorization") != want {
			w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f.count()
		git.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitea) count() {
	f.mu.Lock()
	f.authOK++
	f.mu.Unlock()
}

func (f *fakeGitea) bare(owner, name string) string {
	return filepath.Join(f.root, owner, name+".git")
}

func (f *fakeGitea) git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output() //nolint:gosec // G204: test arguments
	return strings.TrimSpace(string(out)), err
}

// createBare makes an empty bare repository that accepts pushes.
func (f *fakeGitea) createBare(owner, name string) {
	dir := f.bare(owner, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		f.t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "--bare"}, {"config", "http.receivepack", "true"}, {"symbolic-ref", "HEAD", "refs/heads/main"}} {
		if _, err := f.git(dir, args...); err != nil {
			f.t.Fatalf("git %v: %v", args, err)
		}
	}
}

func (f *fakeGitea) api(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case len(parts) == 1 && parts[0] == "user":
		reply(map[string]any{"login": f.login})
	case r.Method == http.MethodPost && (len(parts) == 2 && parts[0] == "user" && parts[1] == "repos" ||
		len(parts) == 3 && parts[0] == "orgs" && parts[2] == "repos"):
		var in struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		owner := f.login
		if parts[0] == "orgs" {
			owner = parts[1]
		}
		f.createBare(owner, in.Name)
		w.WriteHeader(http.StatusCreated)
		reply(map[string]any{"name": in.Name})
	case len(parts) >= 3 && parts[0] == "repos":
		owner, name := parts[1], parts[2]
		dir := f.bare(owner, name)
		if _, err := os.Stat(dir); err != nil {
			http.NotFound(w, r)
			return
		}
		rest := parts[3:]
		key := owner + "/" + name
		switch {
		case len(rest) == 0:
			reply(map[string]any{"name": name})
		case len(rest) == 2 && rest[0] == "branches":
			sha, err := f.git(dir, "rev-parse", "--verify", "-q", "refs/heads/"+rest[1])
			if err != nil {
				http.NotFound(w, r)
				return
			}
			reply(map[string]any{"commit": map[string]any{"id": sha}})
		case len(rest) == 1 && rest[0] == "pulls" && r.Method == http.MethodGet:
			f.mu.Lock()
			list := append([]map[string]any{}, f.pulls[key]...)
			f.mu.Unlock()
			reply(list)
		case len(rest) == 1 && rest[0] == "pulls" && r.Method == http.MethodPost:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			sha, err := f.git(dir, "rev-parse", "--verify", "-q", "refs/heads/"+in["head"])
			if err != nil {
				http.Error(w, "no head", http.StatusUnprocessableEntity)
				return
			}
			f.mu.Lock()
			n := len(f.pulls[key]) + 1
			p := map[string]any{
				"html_url": fmt.Sprintf("%s/%s/%s/pulls/%d", f.srv.URL, owner, name, n),
				"title":    in["title"], "body": in["body"],
				"head": map[string]any{"ref": in["head"], "sha": sha},
				"base": map[string]any{"ref": in["base"]},
			}
			f.pulls[key] = append(f.pulls[key], p)
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			reply(p)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}
