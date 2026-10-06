package bitbucketserver_test

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
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
)

const (
	activitiesAPI = prAPI + "/activities"
	commentsAPI   = prAPI + "/comments"

	authorMarker = "AUTHMARKER-6e0b17"
	pathMarker   = "PATHMARKER-2f9d44"
)

const baseMS = 1_700_000_000_000

func ms(n int) int64     { return int64(baseMS + n*1000) }
func tm(n int) time.Time { return time.UnixMilli(ms(n)).UTC() }
func user(name, display string) map[string]any {
	return map[string]any{"name": name, "displayName": display}
}

func bcomment(id int, name, text string, created int, extra map[string]any, replies ...any) map[string]any {
	m := map[string]any{
		"id": id, "version": 0, "text": text, "author": user(name, name+" Display"),
		"createdDate": ms(created), "updatedDate": ms(created + 1), "comments": replies,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func activity(action, commentAction string, comment any, extra map[string]any) map[string]any {
	m := map[string]any{"id": 1, "action": action, "createdDate": ms(0)}
	if commentAction != "" {
		m["commentAction"] = commentAction
	}
	if comment != nil {
		m["comment"] = comment
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func anchor(path string, line int, fileType string, orphaned bool) map[string]any {
	return map[string]any{"path": path, "line": line, "lineType": "ADDED", "fileType": fileType, "orphaned": orphaned}
}

// threadsFixture registers two activity pages. Comment 4 is a reply to reply
// 2 (two levels deep) created after reply 3, so depth-first order (2,4,3)
// differs from creation order (2,3,4).
func (f *fakeBBS) threadsFixture() {
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	page1 := []any{
		activity("APPROVED", "", nil, nil),
		activity("COMMENTED", "ADDED", bcomment(1, "alice", "general root "+testMarker, 1, map[string]any{"state": "OPEN",
			"author": map[string]any{"id": 9, "name": "alice", "displayName": "alice Display"}},
			bcomment(2, "bob", "reply one", 2, nil, bcomment(4, "alice", "nested reply", 5, nil)),
			bcomment(3, "alice", "reply two", 3, nil),
		), nil),
		activity("COMMENTED", "ADDED", bcomment(10, "alice", "inline root", 1, map[string]any{"state": "OPEN"}),
			map[string]any{"commentAnchor": anchor("src/app.go", 12, "TO", false)}),
		activity("COMMENTED", "EDITED", bcomment(10, "alice", "inline root (edited)", 1, nil),
			map[string]any{"commentAnchor": anchor("src/app.go", 12, "TO", false)}),
		activity("RESCOPED", "", nil, nil),
	}
	page2 := []any{
		activity("COMMENTED", "ADDED", bcomment(20, "bob", "resolved", 20, map[string]any{
			"state": "RESOLVED", "anchor": anchor("src/app.go", 3, "TO", false)}), nil),
		activity("COMMENTED", "ADDED", bcomment(30, "bob", "orphaned and reopened", 30, map[string]any{
			"state": "RESOLVED", "threadResolved": false, "anchor": anchor("src/gone.go", 5, "TO", true)}), nil),
		activity("COMMENTED", "ADDED", bcomment(40, "alice", "old side", 40, nil),
			map[string]any{"commentAnchor": anchor("src/app.go", 8, "FROM", false)}),
		activity("COMMENTED", "ADDED", bcomment(50, "alice", "deleted later", 50, nil), nil),
		activity("COMMENTED", "DELETED", bcomment(50, "alice", "deleted later", 50, nil), nil),
		activity("COMMENTED", "ADDED", bcomment(60, "alice", "has deleted reply", 6, map[string]any{"state": "OPEN"},
			bcomment(61, "bob", "gone", 6, map[string]any{"deleted": true}),
			bcomment(62, "bob", "kept", 7, nil)), nil),
		activity("COMMENTED", "ADDED", bcomment(70, "alice", "", 70, nil), nil),
		activity("COMMENTED", "ADDED", nil, nil),
		activity("COMMENTED", "ADDED", map[string]any{
			"id": 80, "text": "no state, display name only", "author": map[string]any{"displayName": "Carol C"},
			"createdDate": ms(8), "updatedDate": ms(8), "comments": []any{},
		}, map[string]any{"threadResolved": true}),
	}
	f.handlePaged(activitiesAPI, nil, page1, page2)
}

func TestListThreads(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.threadsFixture()
	p := f.provider(t, nil)
	got, err := p.ListThreads(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	overview := f.baseURL() + "/projects/PROJ/repos/demo/pull-requests/7/overview?commentId="
	want := []provider.Thread{
		{ID: "1", Kind: provider.ThreadGeneral, Resolved: &no, ReplyInThread: true, Comments: []provider.CommentItem{
			{ID: "1", Author: "alice", Body: "general root " + testMarker, CreatedAt: tm(1), UpdatedAt: tm(2), AuthorLogin: "alice", AuthorID: "9", URL: overview + "1"},
			{ID: "2", Author: "bob", Body: "reply one", CreatedAt: tm(2), UpdatedAt: tm(3), AuthorLogin: "bob"},
			{ID: "3", Author: "alice", Body: "reply two", CreatedAt: tm(3), UpdatedAt: tm(4), AuthorLogin: "alice"},
			{ID: "4", Author: "alice", Body: "nested reply", CreatedAt: tm(5), UpdatedAt: tm(6), AuthorLogin: "alice"},
		}},
		{ID: "60", Kind: provider.ThreadGeneral, Resolved: &no, ReplyInThread: true, Comments: []provider.CommentItem{
			{ID: "60", Author: "alice", Body: "has deleted reply", CreatedAt: tm(6), UpdatedAt: tm(7), AuthorLogin: "alice", URL: overview + "60"},
			{ID: "62", Author: "bob", Body: "kept", CreatedAt: tm(7), UpdatedAt: tm(8), AuthorLogin: "bob"},
		}},
		{ID: "80", Kind: provider.ThreadGeneral, Resolved: &yes, ReplyInThread: true, Comments: []provider.CommentItem{
			{ID: "80", Author: "Carol C", Body: "no state, display name only", CreatedAt: tm(8), UpdatedAt: tm(8), URL: overview + "80"},
		}},
		{ID: "40", Kind: provider.ThreadInline, Path: "src/app.go", Line: 0, ReplyInThread: true, Comments: []provider.CommentItem{
			{ID: "40", Author: "alice", Body: "old side", CreatedAt: tm(40), UpdatedAt: tm(41), AuthorLogin: "alice", URL: overview + "40"}}},
		{ID: "20", Kind: provider.ThreadInline, Path: "src/app.go", Line: 3, Resolved: &yes, ReplyInThread: true, Comments: []provider.CommentItem{
			{ID: "20", Author: "bob", Body: "resolved", CreatedAt: tm(20), UpdatedAt: tm(21), AuthorLogin: "bob", URL: overview + "20"}}},
		{ID: "10", Kind: provider.ThreadInline, Path: "src/app.go", Line: 12, Resolved: &no, ReplyInThread: true, Comments: []provider.CommentItem{
			{ID: "10", Author: "alice", Body: "inline root", CreatedAt: tm(1), UpdatedAt: tm(2), AuthorLogin: "alice", URL: overview + "10"}}},
		{ID: "30", Kind: provider.ThreadInline, Path: "src/gone.go", Line: 5, Outdated: true, Resolved: &no, ReplyInThread: true, Comments: []provider.CommentItem{
			{ID: "30", Author: "bob", Body: "orphaned and reopened", CreatedAt: tm(30), UpdatedAt: tm(31), AuthorLogin: "bob", URL: overview + "30"}}},
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("threads mismatch, got:\n%s", gj)
	}
	// One probe, then both activity pages.
	if f.count("GET", propsAPI) != 1 || f.count("GET", activitiesAPI) != 2 {
		t.Fatalf("requests = %+v", f.requests())
	}
	// The unfiltered activity listing must have been the only data request.
	for _, r := range f.requests() {
		if r.Method != "GET" {
			t.Errorf("unexpected %s %s", r.Method, r.Path)
		}
	}
}

// [canary] replies nested two levels deep must be flattened.
func TestNestedRepliesFlattenedTwoLevels(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	deep := bcomment(5, "bob", "level three", 9, nil)
	f.handlePaged(activitiesAPI, nil, []any{
		activity("COMMENTED", "ADDED", bcomment(1, "alice", "root", 1, nil,
			bcomment(2, "bob", "level one", 2, nil, bcomment(3, "alice", "level two", 4, nil, deep))), nil),
	})
	got, err := f.provider(t, nil).ListThreads(context.Background(), ref())
	if err != nil || len(got) != 1 {
		t.Fatalf("threads %+v err %v", got, err)
	}
	var idsGot []string
	for _, c := range got[0].Comments {
		idsGot = append(idsGot, c.ID)
	}
	if !reflect.DeepEqual(idsGot, []string{"1", "2", "3", "5"}) {
		t.Fatalf("comment ids = %v", idsGot)
	}
}

// [canary] system and non-comment activities never become threads.
func TestNonCommentActivitiesExcluded(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	f.handlePaged(activitiesAPI, nil, []any{
		activity("APPROVED", "", nil, nil),
		activity("UNAPPROVED", "", nil, nil),
		activity("OPENED", "", nil, nil),
		activity("RESCOPED", "", nil, nil),
		activity("MERGED", "", nil, nil),
		activity("REVIEWED", "", nil, nil),
		// Wrong action or commentAction with a live comment attached; each has
		// its own comment id so no other rule can hide a missing filter.
		activity("RESCOPED", "ADDED", bcomment(1, "alice", "text", 1, nil), nil),
		activity("COMMENTED", "EDITED", bcomment(2, "alice", "text", 2, nil), nil),
		activity("COMMENTED", "DELETED", bcomment(3, "alice", "text", 3, nil), nil),
		activity("COMMENTED", "", bcomment(4, "alice", "text", 4, nil), nil),
		activity("APPROVED", "ADDED", bcomment(5, "alice", "text", 5, nil), nil),
	})
	got, err := f.provider(t, nil).ListThreads(context.Background(), ref())
	if err != nil || len(got) != 0 || got == nil {
		t.Fatalf("threads = %#v err %v, want empty non-nil", got, err)
	}
}

func TestListThreadsUnsupportedVersionStopsBeforeActivities(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "6.10.0"})
	_, err := f.provider(t, nil).ListThreads(context.Background(), ref())
	if !errors.Is(err, provider.ErrUnsupportedVersion) || len(f.requests()) != 1 {
		t.Fatalf("err %v requests %+v", err, f.requests())
	}
}

func TestListThreadsPersonalNamespaceAndInvalidRef(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	f.handlePaged("/rest/api/1.0/projects/~jdoe/repos/demo/pull-requests/7/activities", nil, []any{})
	r := ref()
	r.Namespace = "~jdoe"
	if got, err := f.provider(t, nil).ListThreads(context.Background(), r); err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v", got, err)
	}
	f2 := newFake(t, "")
	p := f2.provider(t, nil)
	for _, bad := range []provider.PRRef{{Namespace: "..", Repo: "demo", Number: 7}, {Namespace: "PROJ", Repo: "demo"}} {
		if _, err := p.ListThreads(context.Background(), bad); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%+v: err = %v", bad, err)
		}
		if _, err := p.ReplyToComment(context.Background(), bad, "1", "x"); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%+v: reply err = %v", bad, err)
		}
	}
	if len(f2.requests()) != 0 {
		t.Fatalf("requests were made: %+v", f2.requests())
	}
}

func TestReplyToComment(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	var ct string
	f.handle("POST", commentsAPI, func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id": 99, "text": "echo"}`)
	})
	p := f.provider(t, nil)
	res, err := p.ReplyToComment(context.Background(), ref(), "0010", "thanks \"x\"\n## h")
	if err != nil {
		t.Fatal(err)
	}
	if !res.InThread || res.Comment.ID != "99" ||
		res.Comment.URL != f.baseURL()+"/projects/PROJ/repos/demo/pull-requests/7/overview?commentId=99" {
		t.Fatalf("result = %+v", res)
	}
	var posts []recorded
	for _, r := range f.requests() {
		if r.Method == "POST" {
			posts = append(posts, r)
		}
	}
	if len(posts) != 1 || posts[0].Path != commentsAPI ||
		posts[0].Body != `{"text":"thanks \"x\"\n## h","parent":{"id":10}}` {
		t.Fatalf("posts = %+v", posts)
	}
	if ct != "application/json" || f.count("GET", propsAPI) != 1 {
		t.Fatalf("content type %q, probes %d", ct, f.count("GET", propsAPI))
	}
}

func TestReplyToUnknownCommentIsNotFound(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	f.handle("POST", commentsAPI, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing "+testMarker+testToken, http.StatusNotFound)
	})
	_, err := f.provider(t, nil).ReplyToComment(context.Background(), ref(), "12345", "hello")
	if !errors.Is(err, provider.ErrNotFound) || strings.Contains(err.Error(), testMarker) {
		t.Fatalf("err = %v", err)
	}
}

func TestReplyErrorsAndMissingIDInResponse(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "6.0.0"})
	_, err := f.provider(t, nil).ReplyToComment(context.Background(), ref(), "1", "x")
	if !errors.Is(err, provider.ErrUnsupportedVersion) {
		t.Fatalf("err = %v", err)
	}
	f = newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	f.handle("POST", commentsAPI, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) })
	res, err := f.provider(t, nil).ReplyToComment(context.Background(), ref(), "1", "x")
	if err != nil || res.Comment.ID != "" || !res.InThread || strings.Contains(res.Comment.URL, "commentId") {
		t.Fatalf("res %+v err %v", res, err)
	}
}

// [canary] invalid input must fail before any request, probe included.
func TestReplyInvalidInputMakesNoRequest(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	p := f.provider(t, nil)
	for _, c := range []struct{ id, body, hint string }{
		{"1", "", "empty body"}, {"1", " \r\n\t", "empty body"},
		{"abc", "x", "invalid comment id"}, {"", "x", "invalid comment id"}, {"-3", "x", "invalid comment id"},
		{"0", "x", "invalid comment id"}, {"1/2", "x", "invalid comment id"}, {"12345678901234567890", "x", "invalid comment id"},
	} {
		_, err := p.ReplyToComment(context.Background(), ref(), c.id, c.body)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != provider.ClassProtocol || pe.Hint != c.hint {
			t.Errorf("(%q,%q): err = %v", c.id, c.body, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("%d requests were made: %+v", n, f.requests())
	}
}

func TestCommentsErrorClassMapping(t *testing.T) {
	for _, status := range []int{401, 404, 429, 500} {
		f := newFake(t, "")
		f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
		f.handleStatus("GET", activitiesAPI, status)
		_, err := f.provider(t, nil).ListThreads(context.Background(), ref())
		cls, _ := provider.ClassifyStatus(status)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != cls || strings.Contains(err.Error(), testMarker) {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestCommentsDebugLogsDoNotLeak(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	body := "BODY " + testMarker
	f.handlePaged(activitiesAPI, nil, []any{
		activity("COMMENTED", "ADDED", bcomment(1, authorMarker, body, 1, nil,
			bcomment(2, authorMarker, body, 2, nil)),
			map[string]any{"commentAnchor": anchor("src/"+pathMarker+".go", 4, "TO", false)}),
	})
	f.handle("POST", commentsAPI, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id": 9, "text": "`+body+`", "author": {"name": "`+authorMarker+`"}}`)
	})
	var logs bytes.Buffer
	p, err := bitbucketserver.NewFactory().New(f.config(), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ths, err := p.ListThreads(ctx, ref())
	if err != nil || len(ths) != 1 {
		t.Fatalf("threads %+v err %v", ths, err)
	}
	var data strings.Builder
	data.WriteString(ths[0].Path)
	for _, c := range ths[0].Comments {
		data.WriteString(c.Body + c.Author)
	}
	for _, m := range []string{testMarker, authorMarker, pathMarker} {
		if !strings.Contains(data.String(), m) {
			t.Fatalf("marker %s expected in returned data", m)
		}
	}
	if _, err := p.ReplyToComment(ctx, ref(), "1", "reply "+testMarker+authorMarker); err != nil {
		t.Fatal(err)
	}
	f.handleStatus("POST", commentsAPI, http.StatusInternalServerError)
	_, errUp := p.ReplyToComment(ctx, ref(), "1", "x")
	_, errV := p.ReplyToComment(ctx, ref(), "x", "y")
	for _, e := range []error{errUp, errV} {
		if e == nil {
			t.Fatal("expected error")
		}
		if s := e.Error(); strings.Contains(s, testToken) || strings.Contains(s, testMarker) || strings.Contains(s, authorMarker) {
			t.Errorf("error leaks: %s", s)
		}
	}
	log := logs.String()
	if !strings.Contains(log, "http request") || !strings.Contains(log, "threads="+strconv.Itoa(1)) {
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
