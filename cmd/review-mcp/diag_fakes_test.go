package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Distinctive per-provider tokens and a marker that must never be echoed.
const (
	diagGiteaToken = "FAKE-diag-gitea-token-QX42-do-not-leak" //nolint:gosec // synthetic test value
	diagBBSToken   = "FAKE-diag-bbs-token-QX42-do-not-leak"   //nolint:gosec // synthetic test value
	diagMarker     = "FAKE-RESPONSE-MARKER-QX42-never-echo"

	// Markers placed in comment fixtures. They must reach the output of
	// "diag comments" and must never reach the logs.
	commentBodyMarker   = "FAKE-COMMENT-BODY-QX42-stdout-only"
	commentAuthorMarker = "fake-author-qx42"
)

// fakeHost is a minimal httptest provider host. It counts every request,
// records POST bodies, and in failStatus mode answers everything with that
// status and an error body carrying the marker and both tokens.
type fakeHost struct {
	srv        *httptest.Server
	hits       atomic.Int64
	failStatus atomic.Int32

	mu     sync.Mutex
	posted []string
}

func (f *fakeHost) requests() int64 { return f.hits.Load() }

func (f *fakeHost) posts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.posted...)
}

func (f *fakeHost) fail(w http.ResponseWriter) bool {
	st := int(f.failStatus.Load())
	if st == 0 {
		return false
	}
	http.Error(w, "denied "+diagMarker+" "+diagGiteaToken+" "+diagBBSToken, st)
	return true
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

const diagGiteaDiff = `diff --git a/src/app.go b/src/app.go
index 1111111..2222222 100644
--- a/src/app.go
+++ b/src/app.go
@@ -1,3 +1,3 @@
 package main
-var a = 1
+var a = 2
 func main() {}
diff --git a/src/old_name.go b/src/renamed.go
similarity index 80%
rename from src/old_name.go
rename to src/renamed.go
index 5555555..6666666 100644
--- a/src/old_name.go
+++ b/src/renamed.go
@@ -1,2 +1,2 @@
 package main
-var r = 1
+var r = 2
diff --git a/assets/logo.png b/assets/logo.png
index 7777777..8888888 100644
Binary files a/assets/logo.png and b/assets/logo.png differ
`

func giteaFileMeta(name, prev, status string, add, del int) map[string]any {
	return map[string]any{"filename": name, "previous_filename": prev, "status": status,
		"additions": add, "deletions": del, "changes": add + del}
}

// newFakeGitea serves one PR (octo/demo#7) under /gitea.
func newFakeGitea(t *testing.T) *fakeHost {
	t.Helper()
	f := &fakeHost{}
	const api = "/gitea/api/v1/repos/octo/demo"
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if f.fail(w) {
			return
		}
		if r.Header.Get("Authorization") != "token "+diagGiteaToken {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		p := r.URL.Path
		switch {
		case r.Method == "GET" && p == api+"/pulls/7":
			writeJSONResp(w, map[string]any{
				"title": "Add feature", "body": "Description " + diagMarker, "state": "open",
				"html_url": "https://your-gitea.example/octo/demo/pulls/7", "merge_base": "mergesha",
				"user": map[string]any{"login": "alice"},
				"head": map[string]any{"ref": "feature", "sha": "headsha"},
				"base": map[string]any{"ref": "main", "sha": "basesha"},
			})
		case r.Method == "GET" && p == api+"/pulls/7.diff":
			_, _ = io.WriteString(w, diagGiteaDiff)
		case r.Method == "GET" && p == api+"/pulls/7/files":
			if r.URL.Query().Get("page") != "1" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			writeJSONResp(w, []any{
				giteaFileMeta("src/renamed.go", "src/old_name.go", "renamed", 1, 1),
				giteaFileMeta("src/app.go", "", "changed", 1, 1),
				giteaFileMeta("assets/logo.png", "", "changed", 0, 0),
			})
		case r.Method == "GET" && p == api+"/pulls/7/commits":
			if r.URL.Query().Get("page") != "1" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			writeJSONResp(w, []any{ // newest first
				map[string]any{"commit": map[string]any{"message": "Second commit\n\nbody " + diagMarker}},
				map[string]any{"commit": map[string]any{"message": "First commit"}},
			})
		case r.Method == "GET" && strings.HasPrefix(p, api+"/raw/"):
			_, _ = io.WriteString(w, "content "+diagMarker)
		case r.Method == "GET" && p == api+"/issues/7/comments":
			if r.URL.Query().Get("page") != "1" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			writeJSONResp(w, []any{giteaIssueComment(101)})
		case r.Method == "GET" && p == api+"/issues/comments/101":
			writeJSONResp(w, giteaIssueComment(101))
		case r.Method == "GET" && p == api+"/pulls/7/reviews":
			if r.URL.Query().Get("page") != "1" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			writeJSONResp(w, []any{map[string]any{"id": 11, "state": "COMMENT"}})
		case r.Method == "GET" && p == api+"/pulls/7/reviews/11/comments":
			if r.URL.Query().Get("page") != "1" {
				_, _ = io.WriteString(w, "[]")
				return
			}
			writeJSONResp(w, []any{
				giteaReviewComment(201, "src/app.go", 10, 100, ""),
				giteaReviewComment(202, "src/app.go", 10, 105, ""),
				giteaReviewComment(301, "src/done.go", 3, 110, "bob"),
			})
		case r.Method == "POST" && p == api+"/issues/7/comments":
			f.mu.Lock()
			f.posted = append(f.posted, string(body))
			f.mu.Unlock()
			writeJSONResp(w, map[string]any{"id": 55, "html_url": "https://your-gitea.example/octo/demo/pulls/7#issuecomment-55"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func commentTime(sec int) string {
	return time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
}

func giteaIssueComment(id int) map[string]any {
	return map[string]any{
		"id": id, "user": map[string]any{"login": commentAuthorMarker}, "body": "general " + commentBodyMarker,
		"created_at": commentTime(1), "updated_at": commentTime(1), "type": "comment",
		"html_url":  "https://your-gitea.example/octo/demo/pulls/7#issuecomment-" + strconv.Itoa(id),
		"issue_url": "https://your-gitea.example/api/v1/repos/octo/demo/issues/7",
	}
}

func giteaReviewComment(id int, path string, position, sec int, resolver string) map[string]any {
	m := map[string]any{
		"id": id, "user": map[string]any{"login": "bob"}, "body": "inline " + commentBodyMarker, "path": path,
		"position": position, "original_position": position,
		"created_at": commentTime(sec), "updated_at": commentTime(sec),
	}
	if resolver != "" {
		m["resolver"] = map[string]any{"id": 2, "login": resolver}
	}
	return m
}

func (f *fakeHost) giteaBase() string { return f.srv.URL + "/gitea" }
func (f *fakeHost) giteaPR() string   { return f.srv.URL + "/gitea/octo/demo/pulls/7" }

// newFakeBBS serves one PR (PROJ/demo#7) under /bitbucket. File contents are
// marker-free because the provider turns them into the patch.
func newFakeBBS(t *testing.T) *fakeHost {
	t.Helper()
	f := &fakeHost{}
	const v1 = "/bitbucket/rest/api/1.0/projects/PROJ/repos/demo"
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if f.fail(w) {
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+diagBBSToken {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		p := r.URL.Path
		paged := func(values []any) {
			writeJSONResp(w, map[string]any{"values": values, "size": len(values), "start": 0, "isLastPage": true})
		}
		switch {
		case r.Method == "GET" && p == "/bitbucket/rest/api/1.0/application-properties":
			writeJSONResp(w, map[string]any{"version": "8.9.0"})
		case r.Method == "GET" && p == v1+"/pull-requests/7":
			writeJSONResp(w, map[string]any{
				"title": "Add feature", "description": "Description " + diagMarker, "state": "OPEN",
				"author":  map[string]any{"user": map[string]any{"name": "jdoe"}},
				"fromRef": map[string]any{"displayId": "feature", "latestCommit": "headsha"},
				"toRef":   map[string]any{"displayId": "main", "latestCommit": "targetsha"},
				"links":   map[string]any{"self": []any{map[string]any{"href": "https://bitbucket.example.com/bitbucket/projects/PROJ/repos/demo/pull-requests/7"}}},
			})
		case r.Method == "GET" && p == "/bitbucket/rest/api/latest/projects/PROJ/repos/demo/pull-requests/7/merge-base":
			writeJSONResp(w, map[string]any{"id": "mergesha"})
		case r.Method == "GET" && p == v1+"/pull-requests/7/changes":
			paged([]any{
				map[string]any{"type": "MODIFY", "path": map[string]any{"toString": "src/app.go"}},
				map[string]any{"type": "ADD", "path": map[string]any{"toString": "src/new.go"}},
			})
		case r.Method == "GET" && p == v1+"/pull-requests/7/commits":
			paged([]any{ // newest first
				map[string]any{"message": "Second commit\n\nbody " + diagMarker},
				map[string]any{"message": "First commit"},
			})
		case r.Method == "GET" && p == v1+"/raw/src/app.go":
			if r.URL.Query().Get("at") == "headsha" {
				_, _ = io.WriteString(w, "package main\nvar a = 2\n")
			} else {
				_, _ = io.WriteString(w, "package main\nvar a = 1\n")
			}
		case r.Method == "GET" && p == v1+"/raw/src/new.go":
			_, _ = io.WriteString(w, "package main\nvar n = 1\n")
		case r.Method == "GET" && p == v1+"/pull-requests/7/activities":
			ms := func(sec int) int64 { return 1_700_000_000_000 + int64(sec)*1000 }
			comment := func(id int, text string, sec int, extra map[string]any, replies ...any) map[string]any {
				m := map[string]any{
					"id": id, "text": text, "author": map[string]any{"name": commentAuthorMarker},
					"createdDate": ms(sec), "updatedDate": ms(sec), "comments": replies,
				}
				for k, v := range extra {
					m[k] = v
				}
				return m
			}
			anchor := map[string]any{"path": "src/app.go", "line": 12, "fileType": "TO"}
			paged([]any{
				map[string]any{"action": "APPROVED"},
				map[string]any{"action": "COMMENTED", "commentAction": "ADDED",
					"comment": comment(1, "general "+commentBodyMarker, 1, map[string]any{"state": "OPEN"},
						comment(2, "reply "+commentBodyMarker, 2, nil))},
				map[string]any{"action": "COMMENTED", "commentAction": "ADDED", "commentAnchor": anchor,
					"comment": comment(10, "inline "+commentBodyMarker, 3, map[string]any{"state": "OPEN"})},
				map[string]any{"action": "COMMENTED", "commentAction": "ADDED",
					"comment": comment(20, "resolved "+commentBodyMarker, 4, map[string]any{"state": "RESOLVED"})},
			})
		case r.Method == "POST" && p == v1+"/pull-requests/7/comments":
			f.mu.Lock()
			f.posted = append(f.posted, string(body))
			f.mu.Unlock()
			writeJSONResp(w, map[string]any{"id": 77})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHost) bbsBase() string { return f.srv.URL + "/bitbucket" }
func (f *fakeHost) bbsPR() string {
	return f.srv.URL + "/bitbucket/projects/PROJ/repos/demo/pull-requests/7"
}

// diagEnv builds a valid configuration enabling the given fakes.
func diagEnv(g, b *fakeHost) map[string]string {
	env := map[string]string{
		"REVIEW_MCP_LLM_BASE_URL":       "https://llm.example.com/v1",
		"REVIEW_MCP_LLM_MODEL":          "example-model",
		"REVIEW_MCP_LLM_CONTEXT_WINDOW": "32000",
		"REVIEW_MCP_LLM_API_KEY":        fakeLLMKey,
		"REVIEW_MCP_LOG_LEVEL":          "debug",
	}
	if g != nil {
		env["REVIEW_MCP_GITEA_BASE_URL"] = g.giteaBase()
		env["REVIEW_MCP_GITEA_TOKEN"] = diagGiteaToken
	}
	if b != nil {
		env["REVIEW_MCP_BITBUCKET_SERVER_BASE_URL"] = b.bbsBase()
		env["REVIEW_MCP_BITBUCKET_SERVER_TOKEN"] = diagBBSToken
	}
	return env
}
