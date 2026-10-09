package github

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	issueURL = "https://api.github.example.com/repos/octo/demo/issues/7"
	pullURL  = "https://api.github.example.com/repos/octo/demo/pulls/7"
)

func user(login string, id int) map[string]any { return map[string]any{"login": login, "id": id} }

func rcJSON(id int, line, orig any, side string, replyTo any, at string) map[string]any {
	m := map[string]any{"id": id, "user": user("bob", 102), "body": "c", "path": "a.go", "line": line,
		"original_line": orig, "side": side, "created_at": at, "pull_request_url": pullURL}
	if replyTo != nil {
		m["in_reply_to_id"] = replyTo
	}
	return m
}

// TestListThreadsShapes: replies are grouped by in_reply_to_id (even when
// the root is not the oldest in the list), a null line falls back to
// original_line and marks the thread outdated, a base-side comment has no
// new-side line, a review with a body is a general entry by the reviewer, a
// pending or empty review is not listed, and Resolved is nil.
func TestListThreadsShapes(t *testing.T) {
	f := newFake(t, "/api/v3")
	f.json("/repos/octo/demo/issues/7/comments", []any{
		map[string]any{"id": 5, "user": user("alice", 101), "body": "hi", "created_at": "2026-01-01T00:00:00Z", "issue_url": issueURL},
	})
	f.json("/repos/octo/demo/pulls/7/comments", []any{
		rcJSON(20, 6, 6, "RIGHT", nil, "2026-01-01T01:00:00Z"),
		rcJSON(21, 6, 6, "RIGHT", 20, "2026-01-01T02:00:00Z"),
		rcJSON(30, nil, 9, "RIGHT", nil, "2026-01-01T03:00:00Z"),
		rcJSON(40, 3, 3, "LEFT", nil, "2026-01-01T04:00:00Z"),
	})
	f.json("/repos/octo/demo/pulls/7/reviews", []any{
		map[string]any{"id": 70, "user": user("carol", 103), "body": "Overall fine", "state": "APPROVED", "submitted_at": "2026-01-01T05:00:00Z"},
		map[string]any{"id": 71, "user": user("carol", 103), "body": "", "state": "COMMENTED"},
		map[string]any{"id": 72, "user": user("dave", 104), "body": "draft", "state": "PENDING"},
	})
	p, _ := f.provider(f.config(""), time.Now())
	got, err := p.ListThreads(t.Context(), testRef())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id, kind, author string
		line, n          int
		outdated         bool
		reply            bool
	}
	var rows []row
	for _, th := range got {
		if th.Resolved != nil {
			t.Errorf("thread %s: Resolved = %v, want nil", th.ID, *th.Resolved)
		}
		rows = append(rows, row{th.ID, string(th.Kind), th.Comments[0].Author, th.Line, len(th.Comments), th.Outdated, th.ReplyInThread})
	}
	want := []row{
		{"5", "general", "alice", 0, 1, false, false},
		{"70", "general", "carol", 0, 1, false, false},
		{"40", "inline", "bob", 0, 1, false, true},
		{"20", "inline", "bob", 6, 2, false, true},
		{"30", "inline", "bob", 9, 1, true, true},
	}
	// SortThreads orders inline threads by path, then line.
	slices.SortStableFunc(rows[2:], func(a, b row) int { return a.line - b.line })
	slices.SortStableFunc(want[2:], func(a, b row) int { return a.line - b.line })
	if !slices.Equal(rows, want) {
		t.Errorf("threads =\n%+v\nwant\n%+v", rows, want)
	}
}

func jsonBody(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("request body: %v", err)
	}
	return m
}

// TestReplyKinds: a reply to a review comment that is itself a reply goes to
// the root's replies endpoint; a reply to a review body is a quoting issue
// comment; an id that exists in two spaces is refused before any write.
func TestReplyKinds(t *testing.T) {
	f := newFake(t, "/api/v3")
	notFound := func(w http.ResponseWriter, _ *http.Request) { fakeError(w, http.StatusNotFound) }
	f.handle(http.MethodGet, "/repos/octo/demo/issues/comments/22", notFound)
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/comments/22", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, rcJSON(22, 6, 6, "RIGHT", 20, "2026-01-01T02:00:00Z"))
	})
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/7/reviews/22", notFound)
	var replied string
	f.handle(http.MethodPost, "/repos/octo/demo/pulls/7/comments/20/replies", func(w http.ResponseWriter, r *http.Request) {
		replied = jsonBody(t, r)["body"]
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": 99, "html_url": "https://github.example.com/octo/demo/pull/7#discussion_r99"})
	})
	// 70 names a review body only.
	f.handle(http.MethodGet, "/repos/octo/demo/issues/comments/70", notFound)
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/comments/70", notFound)
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/7/reviews/70", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": 70, "user": user("carol", 103), "body": "Overall fine", "state": "APPROVED"})
	})
	var quoted string
	f.handle(http.MethodPost, "/repos/octo/demo/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		quoted = jsonBody(t, r)["body"]
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": 100})
	})
	// 80 names an issue comment and a review comment.
	f.handle(http.MethodGet, "/repos/octo/demo/issues/comments/80", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"id": 80, "user": user("alice", 101), "issue_url": issueURL})
	})
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/comments/80", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, rcJSON(80, 1, 1, "RIGHT", nil, "2026-01-01T02:00:00Z"))
	})
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/7/reviews/80", notFound)
	p, _ := f.provider(f.config(""), time.Now())

	rr, err := p.ReplyToComment(t.Context(), testRef(), "22", "answer")
	if err != nil || !rr.InThread || rr.Comment.ID != "99" || replied != "answer" {
		t.Errorf("reply to a reply: %+v, %v, body %q", rr, err, replied)
	}
	rr, err = p.ReplyToComment(t.Context(), testRef(), "70", "answer")
	if err != nil || rr.InThread || rr.Comment.ID != "100" || !strings.HasPrefix(quoted, "> Replying to @carol\n\n") || !strings.HasSuffix(quoted, "answer") {
		t.Errorf("reply to a review: %+v, %v, body %q", rr, err, quoted)
	}
	n := len(f.requests())
	if _, err = p.ReplyToComment(t.Context(), testRef(), "80", "x"); !errors.Is(err, provider.ErrProtocol) ||
		!strings.Contains(err.Error(), NoteAmbiguousID) {
		t.Errorf("ambiguous id: err = %v", err)
	}
	for _, r := range f.requests()[n:] {
		if strings.Contains(r, "/replies") || r == "/api/v3/repos/octo/demo/issues/7/comments" {
			t.Errorf("write after an ambiguous id: %s", r)
		}
	}
}

// TestEditReviewComment: a review comment is patched at /pulls/comments/{id}
// after the ownership check; a comment of another PR is not found.
func TestEditReviewComment(t *testing.T) {
	f := newFake(t, "/api/v3")
	notFound := func(w http.ResponseWriter, _ *http.Request) { fakeError(w, http.StatusNotFound) }
	f.handle(http.MethodGet, "/user", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, user("bob", 102)) })
	f.handle(http.MethodGet, "/repos/octo/demo/issues/comments/22", notFound)
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/comments/22", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, rcJSON(22, 6, 6, "RIGHT", nil, "2026-01-01T02:00:00Z"))
	})
	var patched string
	f.handle(http.MethodPatch, "/repos/octo/demo/pulls/comments/22", func(w http.ResponseWriter, r *http.Request) {
		patched = jsonBody(t, r)["body"]
		writeJSON(w, map[string]any{"id": 22})
	})
	f.handle(http.MethodGet, "/repos/octo/demo/issues/comments/23", notFound)
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/comments/23", func(w http.ResponseWriter, _ *http.Request) {
		m := rcJSON(23, 6, 6, "RIGHT", nil, "2026-01-01T02:00:00Z")
		m["pull_request_url"] = "https://api.github.example.com/repos/octo/other/pulls/7"
		writeJSON(w, m)
	})
	p, _ := f.provider(f.config(""), time.Now())
	if err := p.EditComment(t.Context(), testRef(), "22", "new"); err != nil || patched != "new" {
		t.Errorf("edit: %v, patched %q", err, patched)
	}
	if err := p.EditComment(t.Context(), testRef(), "23", "new"); !errors.Is(err, provider.ErrNotFound) {
		t.Errorf("comment of another repository: err = %v, want not_found", err)
	}
}
