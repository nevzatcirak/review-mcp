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
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
)

const (
	botName = "review-bot"
	botID   = "42"
)

// registerIdentity serves application-properties with the identity headers
// Bitbucket Server sets on every authenticated response. It also answers
// the version probe.
func (f *fakeBBS) registerIdentity(name, id string) {
	f.handle("GET", propsAPI, func(w http.ResponseWriter, _ *http.Request) {
		if name != "" {
			w.Header().Set("X-AUSERNAME", name)
		}
		if id != "" {
			w.Header().Set("X-AUSERID", id)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version": "8.9.0"}`)
	})
}

func commentAt(id string) string { return commentsAPI + "/" + id }

func editable(id, version int, name string, uid int) map[string]any {
	return map[string]any{"id": id, "version": version, "text": "old",
		"author": map[string]any{"id": uid, "name": name, "slug": name, "displayName": name + " Display"}}
}

type putBody struct {
	Text    string `json:"text"`
	Version int    `json:"version"`
}

func puts(t *testing.T, f *fakeBBS, path string) []putBody {
	t.Helper()
	var out []putBody
	for _, r := range f.requests() {
		if r.Method == "PUT" && r.Path == path {
			var b putBody
			if err := json.Unmarshal([]byte(r.Body), &b); err != nil {
				t.Fatal(err)
			}
			out = append(out, b)
		}
	}
	return out
}

func TestCurrentUserFromResponseHeaders(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.registerIdentity(botName, botID)
	u, err := f.provider(t, nil).CurrentUser(context.Background())
	if err != nil || u != (provider.User{ID: botID, Name: botName}) {
		t.Fatalf("user = %+v err %v", u, err)
	}
	if f.count("GET", propsAPI) != 1 {
		t.Fatalf("requests = %+v", f.requests())
	}

	f2 := newFake(t, "")
	f2.registerIdentity(botName, "not-a-number")
	if u, err := f2.provider(t, nil).CurrentUser(context.Background()); err != nil || u != (provider.User{Name: botName}) {
		t.Fatalf("bad id header: user = %+v err %v", u, err)
	}

	// An anonymous response has no X-AUSERNAME: never guess an identity.
	f3 := newFake(t, "")
	f3.registerIdentity("", "")
	if _, err := f3.provider(t, nil).CurrentUser(context.Background()); !errors.Is(err, provider.ErrProtocol) {
		t.Fatalf("missing header: err = %v", err)
	}

	f4 := newFake(t, "")
	f4.handleStatus("GET", propsAPI, 401)
	_, err = f4.provider(t, nil).CurrentUser(context.Background())
	if !errors.Is(err, provider.ErrAuth) || strings.Contains(err.Error(), testMarker) || strings.Contains(err.Error(), testToken) {
		t.Fatalf("401: err = %v", err)
	}
}

func TestEditComment(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.registerIdentity(botName, botID)
	f.handleJSON("GET", commentAt("15"), editable(15, 3, botName, 42))
	f.handleJSON("PUT", commentAt("15"), editable(15, 4, botName, 42))
	const body = "new\n\n[//]: # (review-mcp:overview:v1)"
	if err := f.provider(t, nil).EditComment(context.Background(), ref(), "015", body); err != nil {
		t.Fatal(err)
	}
	if got := puts(t, f, commentAt("15")); !reflect.DeepEqual(got, []putBody{{Text: body, Version: 3}}) {
		t.Fatalf("PUT bodies = %+v", got)
	}
}

// [canary] ownership: a comment written by someone else is never edited.
func TestEditCommentOfAnotherUserIsRefusedWithoutPut(t *testing.T) {
	for name, c := range map[string]struct {
		comment    map[string]any
		headerName string
		headerID   string
	}{
		"other user":             {editable(15, 3, "alice", 7), botName, botID},
		"same name, other id":    {editable(15, 3, botName, 7), botName, botID},
		"other name, no id hdr":  {editable(15, 3, "alice", 7), botName, ""},
		"no author in response":  {map[string]any{"id": 15, "version": 3}, botName, botID},
		"display name collision": {map[string]any{"id": 15, "version": 3, "author": map[string]any{"displayName": botName}}, botName, ""},
	} {
		f := newFake(t, "")
		f.registerIdentity(c.headerName, c.headerID)
		f.handleJSON("GET", commentAt("15"), c.comment)
		f.handleJSON("PUT", commentAt("15"), map[string]any{})
		err := f.provider(t, nil).EditComment(context.Background(), ref(), "15", "hijack")
		if !errors.Is(err, provider.ErrNotOwner) {
			t.Errorf("%s: err = %v", name, err)
		}
		if err != nil && err.Error() != "the comment was not written by the token's user, so it was not changed" {
			t.Errorf("%s: sentence = %q", name, err.Error())
		}
		if n := f.count("PUT", commentAt("15")); n != 0 {
			t.Errorf("%s: %d PUT requests recorded", name, n)
		}
	}
}

// Without a user id header, the name decides, case-insensitively.
func TestEditCommentNameFallback(t *testing.T) {
	f := newFake(t, "")
	f.registerIdentity(botName, "")
	f.handleJSON("GET", commentAt("15"), editable(15, 0, strings.ToUpper(botName), 42))
	f.handleJSON("PUT", commentAt("15"), map[string]any{})
	if err := f.provider(t, nil).EditComment(context.Background(), ref(), "15", "b"); err != nil {
		t.Fatal(err)
	}
	if got := puts(t, f, commentAt("15")); len(got) != 1 || got[0].Version != 0 {
		t.Fatalf("PUT bodies = %+v", got)
	}
}

// A 409 is retried once after a fresh read; a second 409 is reported.
func TestEditCommentVersionConflictRetriesOnce(t *testing.T) {
	for _, conflicts := range []int{1, 2, 3} {
		f := newFake(t, "")
		f.registerIdentity(botName, botID)
		var mu sync.Mutex
		version, putN := 3, 0
		f.handle("GET", commentAt("15"), func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(editable(15, version, botName, 42))
		})
		f.handle("PUT", commentAt("15"), func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			putN++
			if putN <= conflicts {
				version++ // someone else edited in between
				http.Error(w, "conflict "+testMarker, http.StatusConflict)
				return
			}
			_, _ = io.WriteString(w, "{}")
		})
		err := f.provider(t, nil).EditComment(context.Background(), ref(), "15", "b")
		got := puts(t, f, commentAt("15"))
		switch conflicts {
		case 1:
			if err != nil || !reflect.DeepEqual(got, []putBody{{"b", 3}, {"b", 4}}) || f.count("GET", commentAt("15")) != 2 {
				t.Errorf("one conflict: err = %v puts = %+v", err, got)
			}
		default:
			var pe *provider.Error
			if !errors.Is(err, provider.ErrConflict) || !errors.As(err, &pe) || pe.Status != 409 || len(got) != 2 ||
				strings.Contains(err.Error(), testMarker) {
				t.Errorf("%d conflicts: err = %v puts = %+v", conflicts, err, got)
			}
			if err != nil && err.Error() != "the request conflicts with the current state on the server (HTTP 409): the comment was changed by someone else at the same time" {
				t.Errorf("sentence = %q", err.Error())
			}
		}
	}
}

// The re-read after a conflict repeats the ownership check.
func TestEditCommentRetryRechecksOwner(t *testing.T) {
	f := newFake(t, "")
	f.registerIdentity(botName, botID)
	reads := 0
	f.handle("GET", commentAt("15"), func(w http.ResponseWriter, _ *http.Request) {
		reads++
		owner, uid := botName, 42
		if reads > 1 {
			owner, uid = "alice", 7
		}
		_ = json.NewEncoder(w).Encode(editable(15, reads, owner, uid))
	})
	f.handleStatus("PUT", commentAt("15"), http.StatusConflict)
	if err := f.provider(t, nil).EditComment(context.Background(), ref(), "15", "b"); !errors.Is(err, provider.ErrNotOwner) {
		t.Fatalf("err = %v", err)
	}
	if n := f.count("PUT", commentAt("15")); n != 1 {
		t.Fatalf("PUT count = %d, want 1", n)
	}
}

func TestEditCommentErrorClasses(t *testing.T) {
	for _, c := range []struct {
		getStatus, putStatus int
		want                 error
	}{
		{404, 0, provider.ErrNotFound}, {403, 0, provider.ErrAuth}, {500, 0, provider.ErrUpstream},
		{0, 401, provider.ErrAuth}, {0, 400, provider.ErrProtocol}, {0, 404, provider.ErrNotFound},
	} {
		f := newFake(t, "")
		f.registerIdentity(botName, botID)
		if c.getStatus != 0 {
			f.handleStatus("GET", commentAt("15"), c.getStatus)
		} else {
			f.handleJSON("GET", commentAt("15"), editable(15, 1, botName, 42))
			f.handleStatus("PUT", commentAt("15"), c.putStatus)
		}
		err := f.provider(t, nil).EditComment(context.Background(), ref(), "15", "b")
		if !errors.Is(err, c.want) || strings.Contains(err.Error(), testMarker) || strings.Contains(err.Error(), testToken) {
			t.Errorf("%+v: err = %v", c, err)
		}
	}
	// A comment without a version cannot be edited safely.
	f := newFake(t, "")
	f.registerIdentity(botName, botID)
	f.handleJSON("GET", commentAt("15"), map[string]any{"id": 15, "author": map[string]any{"id": 42, "name": botName}})
	if err := f.provider(t, nil).EditComment(context.Background(), ref(), "15", "b"); !errors.Is(err, provider.ErrProtocol) || f.count("PUT", commentAt("15")) != 0 {
		t.Fatalf("no version: err = %v", err)
	}
}

func TestEditCommentInvalidInputMakesNoRequest(t *testing.T) {
	f := newFake(t, "")
	p := f.provider(t, nil)
	for _, c := range []struct{ id, body, hint string }{
		{"1", "", "empty body"}, {"1", "\t", "empty body"}, {"x", "b", "invalid comment id"}, {"-1", "b", "invalid comment id"},
	} {
		err := p.EditComment(context.Background(), ref(), c.id, c.body)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != provider.ClassProtocol || pe.Hint != c.hint {
			t.Errorf("(%q,%q): err = %v", c.id, c.body, err)
		}
	}
	if err := p.EditComment(context.Background(), provider.PRRef{Namespace: "~", Repo: "demo", Number: 7}, "1", "b"); !errors.Is(err, provider.ErrProtocol) {
		t.Errorf("bad ref: err = %v", err)
	}
	if len(f.requests()) != 0 {
		t.Fatalf("requests were made: %+v", f.requests())
	}
}

// ---- inline comments ----

func sampleItems() []provider.InlineComment {
	return []provider.InlineComment{
		{Path: "src/app.go", Line: 2, LineType: provider.LineAdded, Body: "first " + testMarker},
		{Path: "src/app.go", Line: 1, LineType: provider.LineContext, Body: "second"},
		{Path: "src/renamed.go", OldPath: "src/old_name.go", Line: 2, LineType: provider.LineAdded, Body: "third"},
	}
}

func posted(t *testing.T, f *fakeBBS) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range f.requests() {
		if r.Method == "POST" && r.Path == commentsAPI {
			var m map[string]any
			if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
				t.Fatal(err)
			}
			out = append(out, m)
		}
	}
	return out
}

// serveComments answers POST .../comments with ids 500, 501, ... ; status,
// when non-nil, may fail the n-th post (1-based).
func (f *fakeBBS) serveComments(status func(n int) int) {
	var mu sync.Mutex
	n := 0
	f.handle("POST", commentsAPI, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		cur := n
		mu.Unlock()
		if status != nil {
			if st := status(cur); st != 0 {
				http.Error(w, "boom "+testMarker+" "+testToken, st)
				return
			}
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 499 + cur, "version": 0})
	})
}

func TestPostInlineComments(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.registerIdentity(botName, botID)
	f.serveComments(nil)
	got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), &provider.PullRequest{}, sampleItems())
	if err != nil {
		t.Fatal(err)
	}
	base := f.baseURL() + "/projects/PROJ/repos/demo/pull-requests/7/overview?commentId="
	want := []provider.InlineResult{
		{Posted: true, ID: "500", URL: base + "500"},
		{Posted: true, ID: "501", URL: base + "501"},
		{Posted: true, ID: "502", URL: base + "502"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %+v", got)
	}
	anchor := func(path, src string, line int, lt string) map[string]any {
		a := map[string]any{"diffType": "EFFECTIVE", "path": path, "line": float64(line), "lineType": lt, "fileType": "TO"}
		if src != "" {
			a["srcPath"] = src
		}
		return a
	}
	wantPosts := []map[string]any{
		{"text": "first " + testMarker, "anchor": anchor("src/app.go", "", 2, "ADDED")},
		{"text": "second", "anchor": anchor("src/app.go", "", 1, "CONTEXT")},
		{"text": "third", "anchor": anchor("src/renamed.go", "src/old_name.go", 2, "ADDED")},
	}
	if p := posted(t, f); !reflect.DeepEqual(p, wantPosts) {
		t.Fatalf("posts = %#v", p)
	}
}

// Each item reports its own outcome; an auth failure stops the rest.
func TestPostInlineCommentsPerItemOutcome(t *testing.T) {
	f := newFake(t, "")
	f.registerIdentity(botName, botID)
	f.serveComments(func(n int) int {
		if n == 2 {
			return http.StatusBadRequest // e.g. an anchor the server rejects
		}
		return 0
	})
	got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), &provider.PullRequest{}, sampleItems())
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Posted || got[1].Posted || got[1].Error != "the server sent an unexpected response (HTTP 400)" || !got[2].Posted || got[2].ID != "502" {
		t.Fatalf("results = %+v", got)
	}

	f2 := newFake(t, "")
	f2.registerIdentity(botName, botID)
	f2.serveComments(func(int) int { return http.StatusForbidden })
	got, err = f2.provider(t, nil).PostInlineComments(context.Background(), ref(), &provider.PullRequest{}, sampleItems())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Posted || r.Error != "authentication failed: check the token and its scopes (HTTP 403)" {
			t.Errorf("result = %+v", r)
		}
	}
	if n := f2.count("POST", commentsAPI); n != 1 {
		t.Fatalf("POST count = %d, want 1", n)
	}
}

func TestPostInlineCommentsInvalidInputMakesNoRequest(t *testing.T) {
	f := newFake(t, "")
	p := f.provider(t, nil)
	ok := provider.InlineComment{Path: "a.go", Line: 1, LineType: provider.LineContext, Body: "b"}
	for name, c := range map[string]struct {
		pr    *provider.PullRequest
		items []provider.InlineComment
		hint  string
	}{
		"negative line": {&provider.PullRequest{}, []provider.InlineComment{ok, {Path: "a.go", Line: -1, LineType: provider.LineAdded, Body: "b"}}, "invalid inline comment line"},
		"no line type":  {&provider.PullRequest{}, []provider.InlineComment{{Path: "a.go", Line: 1, Body: "b"}}, "invalid inline comment line type"},
		"empty body":    {&provider.PullRequest{}, []provider.InlineComment{{Path: "a.go", Line: 1, LineType: provider.LineAdded}}, "empty body"},
		"empty path":    {&provider.PullRequest{}, []provider.InlineComment{{Line: 1, LineType: provider.LineAdded, Body: "b"}}, "invalid inline comment path"},
		"nil pr":        {nil, []provider.InlineComment{ok}, "the pull request is unknown"},
	} {
		got, err := p.PostInlineComments(context.Background(), ref(), c.pr, c.items)
		var pe *provider.Error
		if got != nil || !errors.As(err, &pe) || pe.Class != provider.ClassProtocol || pe.Hint != c.hint {
			t.Errorf("%s: got %v err = %v", name, got, err)
		}
	}
	if got, err := p.PostInlineComments(context.Background(), ref(), &provider.PullRequest{}, nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no items: got %#v err %v", got, err)
	}
	if len(f.requests()) != 0 {
		t.Fatalf("requests were made: %+v", f.requests())
	}
}

func TestWriteDebugLogsDoNotLeak(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.registerIdentity(botName, botID)
	f.serveComments(func(n int) int {
		if n == 1 {
			return 400
		}
		return 0
	})
	f.handleJSON("GET", commentAt("15"), editable(15, 1, botName, 42))
	f.handleStatus("PUT", commentAt("15"), http.StatusConflict)
	f.handleJSON("GET", commentAt("16"), editable(16, 1, authorMarker, 7))

	var logs bytes.Buffer
	p, err := bitbucketserver.NewFactory().New(f.config(), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	items := []provider.InlineComment{
		{Path: "src/" + pathMarker + ".go", Line: 1, LineType: provider.LineAdded, Body: "x " + testMarker},
		{Path: "src/" + pathMarker + ".go", Line: 2, LineType: provider.LineAdded, Body: "y " + testMarker},
	}
	res, err := p.PostInlineComments(ctx, ref(), &provider.PullRequest{}, items)
	if err != nil || res[0].Posted || !res[1].Posted {
		t.Fatalf("res %+v err %v", res, err)
	}
	errC := p.EditComment(ctx, ref(), "15", "edited "+testMarker)
	errO := p.EditComment(ctx, ref(), "16", "edited "+testMarker)
	for _, e := range []error{errC, errO} {
		if e == nil {
			t.Fatal("expected error")
		}
		if s := e.Error(); strings.Contains(s, testToken) || strings.Contains(s, testMarker) || strings.Contains(s, authorMarker) {
			t.Errorf("error leaks: %s", s)
		}
	}
	if strings.Contains(res[0].Error, testMarker) || strings.Contains(res[0].Error, testToken) {
		t.Errorf("item error leaks: %s", res[0].Error)
	}
	log := logs.String()
	if !strings.Contains(log, "inline comments posted") || !strings.Contains(log, "version conflict") {
		t.Fatalf("debug log lacks expected entries, test is not meaningful:\n%s", log)
	}
	for name, m := range map[string]string{"token": testToken, "body marker": testMarker, "author marker": authorMarker,
		"path marker": pathMarker, "user name": botName} {
		if strings.Contains(log, m) {
			t.Errorf("%s appears in logs:\n%s", name, log)
		}
	}
}
