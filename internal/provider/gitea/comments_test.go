package gitea_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
)

const (
	issueComments = repoAPI + "/issues/7/comments"
	reviewsAPI    = prAPI + "/reviews"
	postComment   = repoAPI + "/issues/7/comments"

	authorMarker = "AUTHMARKER-3d5f90"
	pathMarker   = "PATHMARKER-81c2aa"
)

func ts(sec int) string {
	return time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
}

func at(sec int) time.Time {
	return time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second)
}

func icomment(id int, user, body string, sec int) map[string]any {
	return map[string]any{
		"id": id, "user": map[string]any{"login": user}, "body": body,
		"created_at": ts(sec), "updated_at": ts(sec + 1),
		"html_url":         "https://your-gitea.example/octo/demo/pulls/7#issuecomment-" + strconv.Itoa(id),
		"issue_url":        "https://your-gitea.example/api/v1/repos/octo/demo/issues/7",
		"pull_request_url": "https://your-gitea.example/api/v1/repos/octo/demo/pulls/7",
		"type":             "comment",
	}
}

func rcomment(id int, user, body, path string, position, origPosition, sec int, resolver string) map[string]any {
	m := map[string]any{
		"id": id, "user": map[string]any{"login": user}, "body": body, "path": path,
		"position": position, "original_position": origPosition,
		"created_at": ts(sec), "updated_at": ts(sec + 1), "diff_hunk": "@@ -1 +1 @@",
	}
	if resolver != "" {
		m["resolver"] = map[string]any{"id": 2, "login": resolver}
	} else {
		m["resolver"] = nil
	}
	return m
}

func reviewJSON(id int, state string) map[string]any { return map[string]any{"id": id, "state": state} }

func reviewCommentsAPI(id int) string { return reviewsAPI + "/" + strconv.Itoa(id) + "/comments" }

// commentsFixture registers the conversation used by the listing and reply
// tests. Review 13 is PENDING and its comments endpoint is deliberately not
// registered: any request to it fails the test.
func (f *fakeGitea) commentsFixture() {
	sys := icomment(102, "bob", "pushed 1 commit", 2)
	sys["type"] = "pull_push"
	// Page 1 is short (3 < limit 50) and is still followed by page 2.
	// Comment 104 carries its author's numeric id, the others a login only.
	withID := icomment(104, "bob", "page two general", 40)
	withID["user"] = map[string]any{"id": 7, "login": "bob"}
	f.handlePages(issueComments,
		[]any{icomment(103, "alice", "later general", 30), icomment(101, "alice", "first general "+testMarker, 1), sys},
		[]any{withID},
	)
	f.handlePages(reviewsAPI, []any{reviewJSON(11, "COMMENT"), reviewJSON(13, "PENDING")}, []any{reviewJSON(12, "APPROVED")})
	f.handlePages(reviewCommentsAPI(11), []any{
		rcomment(201, "alice", "root of line 10 "+testMarker, "src/app.go", 10, 10, 100, ""),
		rcomment(202, "alice", "resolved thread", "src/app.go", 3, 3, 110, "bob"),
		rcomment(205, "bob", "outdated", "src/old.go", 0, 5, 120, ""),
	})
	f.handlePages(reviewCommentsAPI(12), []any{
		rcomment(203, "bob", "reply across reviews", "src/app.go", 10, 10, 105, ""),
	})
}

func TestListThreads(t *testing.T) {
	f := newFake(t, "/gitea")
	f.commentsFixture()
	p := f.provider(t, nil)
	got, err := p.ListThreads(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	f1 := false
	tr := true
	want := []provider.Thread{
		{ID: "101", Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{
			{ID: "101", Author: "alice", Body: "first general " + testMarker, CreatedAt: at(1), UpdatedAt: at(2), AuthorLogin: "alice", URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-101"}}},
		{ID: "103", Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{
			{ID: "103", Author: "alice", Body: "later general", CreatedAt: at(30), UpdatedAt: at(31), AuthorLogin: "alice", URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-103"}}},
		{ID: "104", Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{
			{ID: "104", Author: "bob", Body: "page two general", CreatedAt: at(40), UpdatedAt: at(41), AuthorLogin: "bob", URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-104", AuthorID: "7"}}},
		{ID: "202", Kind: provider.ThreadInline, Path: "src/app.go", Line: 3, Resolved: &tr, Comments: []provider.CommentItem{
			{ID: "202", Author: "alice", Body: "resolved thread", CreatedAt: at(110), UpdatedAt: at(111), AuthorLogin: "alice"}}},
		{ID: "201", Kind: provider.ThreadInline, Path: "src/app.go", Line: 10, Resolved: &f1, Comments: []provider.CommentItem{
			{ID: "201", Author: "alice", Body: "root of line 10 " + testMarker, CreatedAt: at(100), UpdatedAt: at(101), AuthorLogin: "alice"},
			{ID: "203", Author: "bob", Body: "reply across reviews", CreatedAt: at(105), UpdatedAt: at(106), AuthorLogin: "bob"}}},
		{ID: "205", Kind: provider.ThreadInline, Path: "src/old.go", Line: 0, Outdated: true, Resolved: &f1, Comments: []provider.CommentItem{
			{ID: "205", Author: "bob", Body: "outdated", CreatedAt: at(120), UpdatedAt: at(121), AuthorLogin: "bob"}}},
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("threads mismatch, got:\n%s", gj)
	}
	for _, th := range got {
		if th.ReplyInThread {
			t.Errorf("thread %s: ReplyInThread must be false", th.ID)
		}
	}
	// Pagination: issue comments needed pages 1, 2 and the empty page 3.
	pages := map[string]int{}
	for _, r := range f.requests() {
		pages[r.Path]++
		if r.Method != "GET" {
			t.Errorf("unexpected %s %s", r.Method, r.Path)
		}
	}
	if pages[issueComments] != 3 || pages[reviewsAPI] != 3 {
		t.Errorf("request counts = %v", pages)
	}
	if pages[reviewCommentsAPI(13)] != 0 {
		t.Error("a PENDING review's comments were requested")
	}
}

func TestListThreadsNeverNilComments(t *testing.T) {
	f := newFake(t, "")
	f.handlePages(issueComments)
	f.handlePages(reviewsAPI)
	got, err := f.provider(t, nil).ListThreads(context.Background(), ref())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("threads = %#v err %v, want empty non-nil", got, err)
	}
}

// [canary] a short page of issue comments must not end pagination.
func TestIssueCommentsShortPageFollowedByMore(t *testing.T) {
	f := newFake(t, "")
	f.handlePages(issueComments,
		[]any{icomment(1, "alice", "a", 1)},
		[]any{icomment(2, "alice", "b", 2)},
		[]any{icomment(3, "alice", "c", 3)},
	)
	f.handlePages(reviewsAPI)
	got, err := f.provider(t, nil).ListThreads(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].ID != "3" {
		t.Fatalf("threads = %+v", got)
	}
}

// Gitea may serve review comments without pagination; a server that ignores
// page= must not loop to the page ceiling.
func TestReviewCommentsUnpaginatedServerTerminates(t *testing.T) {
	f := newFake(t, "")
	f.handlePages(issueComments)
	f.handlePages(reviewsAPI, []any{reviewJSON(11, "COMMENT")})
	n := 0
	f.handle("GET", reviewCommentsAPI(11), func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n > 5 {
			f.t.Error("review comments requested too often")
		}
		_ = json.NewEncoder(w).Encode([]any{rcomment(1, "alice", "x", "a.go", 4, 4, 1, "")})
	})
	got, err := f.provider(t, nil).ListThreads(context.Background(), ref())
	if err != nil || len(got) != 1 || len(got[0].Comments) != 1 {
		t.Fatalf("threads = %+v err %v", got, err)
	}
	if n != 2 {
		t.Errorf("review comments requests = %d, want 2", n)
	}
}

func TestListThreadsLineFieldAndGrouping(t *testing.T) {
	f := newFake(t, "")
	f.handlePages(issueComments)
	f.handlePages(reviewsAPI, []any{reviewJSON(11, "COMMENT")})
	withLine := rcomment(1, "alice", "r", "a.go", 99, 99, 1, "")
	withLine["line"] = 12
	reply := rcomment(2, "bob", "r2", "a.go", 77, 77, 2, "")
	reply["line"] = 12
	other := rcomment(3, "bob", "other", "a.go", 77, 77, 3, "")
	f.handlePages(reviewCommentsAPI(11), []any{reply, other, withLine})
	got, err := f.provider(t, nil).ListThreads(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Line != 12 || len(got[0].Comments) != 2 || got[0].ID != "1" || got[1].Line != 77 {
		t.Fatalf("threads = %+v", got)
	}
}

func TestListThreadsPathEscapingAndInvalidRef(t *testing.T) {
	f := newFake(t, "")
	r := provider.PRRef{Namespace: "o x", Repo: "d/emo", Number: 7}
	base := "/api/v1/repos/o%20x/d%2Femo"
	f.handlePages(base + "/issues/7/comments")
	f.handlePages(base + "/pulls/7/reviews")
	if _, err := f.provider(t, nil).ListThreads(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	f2 := newFake(t, "")
	p := f2.provider(t, nil)
	for _, bad := range []provider.PRRef{{Namespace: "..", Repo: "demo", Number: 7}, {Namespace: "octo", Repo: "demo"}} {
		if _, err := p.ListThreads(context.Background(), bad); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%+v: err = %v", bad, err)
		}
		if _, err := p.ReplyToComment(context.Background(), bad, "1", "x"); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%+v: reply err = %v", bad, err)
		}
	}
	if len(f2.requests()) != 0 {
		t.Fatal("requests were made")
	}
}

func notFound(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope "+testMarker, 404) }

func posts(f *fakeGitea) []recorded {
	var out []recorded
	for _, r := range f.requests() {
		if r.Method == "POST" {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeGitea) registerPost(id int) {
	f.handle("POST", postComment, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id": `+strconv.Itoa(id)+`, "html_url": "https://your-gitea.example/octo/demo/pulls/7#issuecomment-`+strconv.Itoa(id)+`"}`)
	})
}

func postedBody(t *testing.T, r recorded) string {
	t.Helper()
	var in struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(r.Body), &in); err != nil {
		t.Fatal(err)
	}
	return in.Body
}

func TestReplyToInlineComment(t *testing.T) {
	f := newFake(t, "/gitea")
	f.commentsFixture()
	f.handle("GET", repoAPI+"/issues/comments/201", notFound)
	f.handle("GET", repoAPI+"/issues/comments/205", notFound)
	f.registerPost(300)
	p := f.provider(t, nil)
	userBody := "Thanks!\n\n```go\nx := 1\n```\n"
	res, err := p.ReplyToComment(context.Background(), ref(), "201", userBody)
	if err != nil {
		t.Fatal(err)
	}
	if res.InThread || res.Comment.ID != "300" || res.Comment.URL != "https://your-gitea.example/octo/demo/pulls/7#issuecomment-300" {
		t.Fatalf("result = %+v", res)
	}
	ps := posts(f)
	if len(ps) != 1 {
		t.Fatalf("posts = %+v", ps)
	}
	if want := "> Replying to @alice on src/app.go:10\n\n" + userBody; postedBody(t, ps[0]) != want {
		t.Fatalf("posted body = %q, want %q", postedBody(t, ps[0]), want)
	}
	// Line 0 (outdated comment): header without a line number.
	if _, err := p.ReplyToComment(context.Background(), ref(), "205", "ok"); err != nil {
		t.Fatal(err)
	}
	ps = posts(f)
	if want := "> Replying to @bob on src/old.go\n\nok"; len(ps) != 2 || postedBody(t, ps[1]) != want {
		t.Fatalf("posts = %+v", ps)
	}
}

func TestReplyToGeneralComment(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", repoAPI+"/issues/comments/101", icomment(101, "alice", "hi", 1))
	f.handleJSON("GET", repoAPI+"/issues/comments/102", map[string]any{
		"id": 102, "user": map[string]any{"login": "bob"}, "html_url": "https://your-gitea.example/octo/demo/pulls/7#issuecomment-102"})
	f.registerPost(301)
	p := f.provider(t, nil)
	res, err := p.ReplyToComment(context.Background(), ref(), "0101", "  keep spaces  ")
	if err != nil || res.InThread || res.Comment.ID != "301" {
		t.Fatalf("res %+v err %v", res, err)
	}
	// Leading zeros were canonicalized in the lookup path; only one GET.
	if want := "> Replying to @alice\n\n  keep spaces  "; postedBody(t, posts(f)[0]) != want {
		t.Fatalf("body = %q", postedBody(t, posts(f)[0]))
	}
	// Only html_url present (ends in /7 after the fragment is dropped): accepted.
	if _, err := p.ReplyToComment(context.Background(), ref(), "102", "x"); err != nil {
		t.Fatalf("html_url-only comment: %v", err)
	}
}

func TestReplyIssueCommentOfAnotherPRIsNotFound(t *testing.T) {
	cases := map[string]map[string]any{
		"issue_url":                {"id": 500, "user": map[string]any{"login": "eve"}, "issue_url": "https://your-gitea.example/api/v1/repos/octo/demo/issues/8"},
		"pull_request_url":         {"id": 500, "user": map[string]any{"login": "eve"}, "pull_request_url": "https://your-gitea.example/api/v1/repos/octo/demo/pulls/17"},
		"html_url":                 {"id": 500, "user": map[string]any{"login": "eve"}, "html_url": "https://your-gitea.example/octo/demo/issues/70#issuecomment-500"},
		"one mismatch":             {"id": 500, "issue_url": ".../issues/7", "pull_request_url": ".../pulls/8"},
		"same number, other repo":  {"id": 500, "issue_url": "https://your-gitea.example/api/v1/repos/octo/other/issues/7"},
		"same number, other owner": {"id": 500, "pull_request_url": "https://your-gitea.example/api/v1/repos/mallory/demo/pulls/7"},
		"html_url other repo":      {"id": 500, "html_url": "https://your-gitea.example/octo/other/pulls/7#issuecomment-500"},
		"wrong kind segment":       {"id": 500, "html_url": "https://your-gitea.example/octo/demo/commits/7#issuecomment-500"},
		"ok field, bad field":      {"id": 500, "issue_url": "https://your-gitea.example/api/v1/repos/octo/demo/issues/7", "html_url": "https://your-gitea.example/octo/other/pulls/7"},
		"too few segments":         {"id": 500, "issue_url": "https://your-gitea.example/issues/7"},
		"no url fields":            {"id": 500, "user": map[string]any{"login": "eve"}, "body": "x"},
	}
	for name, body := range cases {
		f := newFake(t, "")
		f.handleJSON("GET", repoAPI+"/issues/comments/500", body)
		_, err := f.provider(t, nil).ReplyToComment(context.Background(), ref(), "500", "hello")
		var pe *provider.Error
		if !errors.Is(err, provider.ErrNotFound) || !errors.As(err, &pe) || pe.Hint != "" {
			t.Errorf("%s: err = %v", name, err)
		}
		if len(posts(f)) != 0 || len(f.requests()) != 1 {
			t.Errorf("%s: requests = %+v", name, f.requests())
		}
	}
}

func TestReplyUnknownIDIsNotFoundWithoutPost(t *testing.T) {
	f := newFake(t, "")
	f.commentsFixture()
	f.handle("GET", repoAPI+"/issues/comments/999", notFound)
	_, err := f.provider(t, nil).ReplyToComment(context.Background(), ref(), "999", "hello")
	if !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(posts(f)) != 0 {
		t.Fatal("a comment was posted")
	}
}

// A review comment that belongs to another PR is not in this PR's reviews, so
// it is not found (the by-id lookup 404s because it is not an issue comment).
func TestReplyReviewCommentOfAnotherPRIsNotFound(t *testing.T) {
	f := newFake(t, "")
	f.handlePages(reviewsAPI, []any{reviewJSON(11, "COMMENT")})
	f.handlePages(reviewCommentsAPI(11), []any{rcomment(201, "alice", "mine", "a.go", 1, 1, 1, "")})
	f.handle("GET", repoAPI+"/issues/comments/600", notFound) // belongs to PR 8's review
	_, err := f.provider(t, nil).ReplyToComment(context.Background(), ref(), "600", "hello")
	if !errors.Is(err, provider.ErrNotFound) || len(posts(f)) != 0 {
		t.Fatalf("err = %v posts = %v", err, posts(f))
	}
}

func TestReplyLookupErrorsPropagate(t *testing.T) {
	f := newFake(t, "")
	f.handle("GET", repoAPI+"/issues/comments/5", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", http.StatusForbidden) })
	_, err := f.provider(t, nil).ReplyToComment(context.Background(), ref(), "5", "hello")
	if !errors.Is(err, provider.ErrAuth) || len(posts(f)) != 0 {
		t.Fatalf("err = %v", err)
	}
}

func TestReplyInvalidInputMakesNoRequest(t *testing.T) {
	f := newFake(t, "")
	p := f.provider(t, nil)
	for _, c := range []struct{ id, body, hint string }{
		{"1", "", "empty body"}, {"1", " \n\t", "empty body"},
		{"abc", "x", "invalid comment id"}, {"", "x", "invalid comment id"}, {"-3", "x", "invalid comment id"},
		{"0", "x", "invalid comment id"}, {"1/2", "x", "invalid comment id"}, {"1 ", "x", "invalid comment id"},
	} {
		_, err := p.ReplyToComment(context.Background(), ref(), c.id, c.body)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != provider.ClassProtocol || pe.Hint != c.hint {
			t.Errorf("(%q,%q): err = %v", c.id, c.body, err)
		}
	}
	if len(f.requests()) != 0 {
		t.Fatalf("requests were made: %+v", f.requests())
	}
}

func TestCommentErrorClassMapping(t *testing.T) {
	for _, status := range []int{401, 404, 429, 500} {
		f := newFake(t, "")
		st := status
		h := func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom "+testMarker+testToken, st) }
		f.handle("GET", issueComments, h)
		_, err := f.provider(t, nil).ListThreads(context.Background(), ref())
		cls, _ := provider.ClassifyStatus(status)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != cls || strings.Contains(err.Error(), testMarker) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestCommentsDebugLogsDoNotLeak(t *testing.T) {
	f := newFake(t, "/gitea")
	const body = "BODY " + testMarker
	f.handlePages(issueComments, []any{icomment(101, authorMarker, body, 1)})
	f.handlePages(reviewsAPI, []any{reviewJSON(11, "COMMENT")})
	f.handlePages(reviewCommentsAPI(11), []any{rcomment(201, authorMarker, body, "src/"+pathMarker+".go", 4, 4, 5, "")})
	f.handle("GET", repoAPI+"/issues/comments/201", notFound)
	f.registerPost(300)
	f.handle("GET", repoAPI+"/issues/comments/777", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "x "+testMarker+authorMarker+testToken, 500)
	})

	var logs bytes.Buffer
	p, err := gitea.NewFactory().New(f.config(), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ths, err := p.ListThreads(ctx, ref())
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, th := range ths {
		for _, c := range th.Comments {
			out.WriteString(c.Body + c.Author + th.Path)
		}
	}
	for _, m := range []string{testMarker, authorMarker, pathMarker} {
		if !strings.Contains(out.String(), m) {
			t.Fatalf("marker %s expected in returned data", m)
		}
	}
	res, err := p.ReplyToComment(ctx, ref(), "201", "reply "+testMarker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(postedBody(t, posts(f)[0]), authorMarker) || res.InThread {
		t.Fatal("quote header should carry the author")
	}
	_, errR := p.ReplyToComment(ctx, ref(), "777", "x")
	_, errV := p.ReplyToComment(ctx, ref(), "1", " ")
	for _, e := range []error{errR, errV} {
		if e == nil {
			t.Fatal("expected error")
		}
		if s := e.Error(); strings.Contains(s, testToken) || strings.Contains(s, testMarker) || strings.Contains(s, authorMarker) {
			t.Errorf("error leaks: %s", s)
		}
	}
	log := logs.String()
	if !strings.Contains(log, "http request") || !strings.Contains(log, "threads=") {
		t.Fatalf("debug log lacks expected entries, test is not meaningful:\n%s", log)
	}
	for name, m := range map[string]string{"token": testToken, "body marker": testMarker, "author marker": authorMarker, "path marker": pathMarker} {
		if strings.Contains(log, m) {
			t.Errorf("%s appears in logs:\n%s", name, log)
		}
	}
	if strings.Contains(strings.ToLower(log), "authorization") {
		t.Error("an Authorization header name appears in logs")
	}
}

// Owner and repo compare case-insensitively; issues and pulls are both fine.
func TestReplyIssueCommentURLVariantsAccepted(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"case differences": {"id": 500, "user": map[string]any{"login": "alice"}, "issue_url": "https://your-gitea.example/api/v1/repos/OCTO/Demo/issues/7"},
		"pulls segment":    {"id": 500, "user": map[string]any{"login": "alice"}, "pull_request_url": "https://your-gitea.example/api/v1/repos/octo/demo/pulls/7"},
		"issues in html":   {"id": 500, "user": map[string]any{"login": "alice"}, "html_url": "https://your-gitea.example/octo/demo/issues/7#issuecomment-500"},
		"context path":     {"id": 500, "user": map[string]any{"login": "alice"}, "html_url": "https://your-gitea.example/gitea/octo/demo/pulls/7/"},
	} {
		f := newFake(t, "")
		f.handleJSON("GET", repoAPI+"/issues/comments/500", body)
		f.registerPost(301)
		if _, err := f.provider(t, nil).ReplyToComment(context.Background(), ref(), "500", "hello"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Escaped owner and repo segments are unescaped before comparison.
func TestReplyIssueCommentURLEscapedSegments(t *testing.T) {
	f := newFake(t, "")
	r := provider.PRRef{Namespace: "o x", Repo: "d/emo", Number: 7}
	base := "/api/v1/repos/o%20x/d%2Femo"
	f.handleJSON("GET", base+"/issues/comments/500", map[string]any{"id": 500, "user": map[string]any{"login": "alice"},
		"issue_url": "https://your-gitea.example/api/v1/repos/o%20x/d%2Femo/issues/7"})
	f.handle("POST", base+"/issues/7/comments", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"id": 1}`) })
	if _, err := f.provider(t, nil).ReplyToComment(context.Background(), r, "500", "hello"); err != nil {
		t.Fatal(err)
	}
}
