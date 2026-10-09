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
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
)

const (
	userAPI     = "/api/v1/user"
	commentAPI  = repoAPI + "/issues/comments/"
	botLogin    = "review-bot"
	botID       = 42
	reviewsBase = "https://your-gitea.example/octo/demo/pulls/7"
)

func (f *fakeGitea) registerUser() {
	f.handleJSON("GET", userAPI, map[string]any{"id": botID, "login": botLogin})
}

func countReq(f *fakeGitea, method, path string) int {
	n := 0
	for _, r := range f.requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func methods(f *fakeGitea, m string) []recorded {
	var out []recorded
	for _, r := range f.requests() {
		if r.Method == m {
			out = append(out, r)
		}
	}
	return out
}

func TestCurrentUser(t *testing.T) {
	f := newFake(t, "/gitea")
	f.registerUser()
	u, err := f.provider(t, nil).CurrentUser(context.Background())
	if err != nil || u != (provider.User{ID: "42", Name: botLogin}) {
		t.Fatalf("user = %+v err %v", u, err)
	}

	f2 := newFake(t, "")
	f2.handleJSON("GET", userAPI, map[string]any{"id": 1})
	if _, err := f2.provider(t, nil).CurrentUser(context.Background()); !errors.Is(err, provider.ErrProtocol) {
		t.Fatalf("missing login: err = %v", err)
	}
	f3 := newFake(t, "")
	f3.handle("GET", userAPI, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, testMarker, http.StatusUnauthorized) })
	_, err = f3.provider(t, nil).CurrentUser(context.Background())
	if !errors.Is(err, provider.ErrAuth) || strings.Contains(err.Error(), testMarker) {
		t.Fatalf("401: err = %v", err)
	}
}

func ownComment(id int, login string, uid int) map[string]any {
	c := icomment(id, login, "old body", 1)
	c["user"] = map[string]any{"id": uid, "login": login}
	return c
}

func patchBody(t *testing.T, f *fakeGitea) []string {
	t.Helper()
	var out []string
	for _, r := range methods(f, "PATCH") {
		out = append(out, postedBody(t, r))
	}
	return out
}

func TestEditComment(t *testing.T) {
	f := newFake(t, "/gitea")
	f.registerUser()
	f.handleJSON("GET", commentAPI+"101", ownComment(101, botLogin, botID))
	f.handleJSON("PATCH", commentAPI+"101", ownComment(101, botLogin, botID))
	const body = "new body\n\n[//]: # (review-mcp:overview:v1)"
	if err := f.provider(t, nil).EditComment(context.Background(), ref(), "0101", body); err != nil {
		t.Fatal(err)
	}
	if got := patchBody(t, f); !reflect.DeepEqual(got, []string{body}) {
		t.Fatalf("PATCH bodies = %q", got)
	}
	// The comment is read before the edit.
	reqs := f.requests()
	if len(reqs) != 3 || reqs[0].Path != commentAPI+"101" || reqs[2].Method != "PATCH" {
		t.Fatalf("requests = %+v", reqs)
	}
}

// [canary] ownership: a comment written by someone else is never edited.
func TestEditCommentOfAnotherUserIsRefusedWithoutPatch(t *testing.T) {
	for name, c := range map[string]map[string]any{
		"other user":                 ownComment(101, "alice", 7),
		"same login, other id":       ownComment(101, botLogin, 7),
		"no author":                  icomment(101, "", "x", 1),
		"other login, no ids at all": icomment(101, "alice", "x", 1),
	} {
		f := newFake(t, "")
		f.registerUser()
		f.handleJSON("GET", commentAPI+"101", c)
		f.handle("PATCH", commentAPI+"101", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") })
		err := f.provider(t, nil).EditComment(context.Background(), ref(), "101", "hijack")
		if !errors.Is(err, provider.ErrNotOwner) {
			t.Errorf("%s: err = %v", name, err)
		}
		if err != nil && err.Error() != "the comment was not written by the token's user, so it was not changed" {
			t.Errorf("%s: sentence = %q", name, err.Error())
		}
		if n := len(methods(f, "PATCH")); n != 0 {
			t.Errorf("%s: %d PATCH requests recorded", name, n)
		}
	}
}

// Without ids the login decides, case-insensitively.
func TestEditCommentLoginFallback(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", userAPI, map[string]any{"login": botLogin})
	f.handleJSON("GET", commentAPI+"5", icomment(5, strings.ToUpper(botLogin), "x", 1))
	f.handleJSON("PATCH", commentAPI+"5", map[string]any{})
	if err := f.provider(t, nil).EditComment(context.Background(), ref(), "5", "b"); err != nil {
		t.Fatal(err)
	}
}

func TestEditCommentOfAnotherRepositoryOrKindIsNotFound(t *testing.T) {
	foreign := ownComment(500, botLogin, botID)
	foreign["issue_url"] = "https://your-gitea.example/api/v1/repos/octo/other/issues/7"
	f := newFake(t, "")
	f.handleJSON("GET", commentAPI+"500", foreign)
	f.handle("GET", commentAPI+"501", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	f.handle("GET", commentAPI+"502", notFound)
	p := f.provider(t, nil)
	for _, id := range []string{"500", "501", "502"} {
		if err := p.EditComment(context.Background(), ref(), id, "b"); !errors.Is(err, provider.ErrNotFound) {
			t.Errorf("%s: err = %v", id, err)
		}
	}
	if len(methods(f, "PATCH")) != 0 || countReq(f, "GET", userAPI) != 0 {
		t.Fatalf("requests = %+v", f.requests())
	}
}

func TestEditCommentInvalidInputMakesNoRequest(t *testing.T) {
	f := newFake(t, "")
	p := f.provider(t, nil)
	for _, c := range []struct{ id, body, hint string }{
		{"1", "", "empty body"}, {"1", " \n", "empty body"}, {"x", "b", "invalid comment id"}, {"0", "b", "invalid comment id"},
	} {
		err := p.EditComment(context.Background(), ref(), c.id, c.body)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.Class != provider.ClassProtocol || pe.Hint != c.hint {
			t.Errorf("(%q,%q): err = %v", c.id, c.body, err)
		}
	}
	if err := p.EditComment(context.Background(), provider.PRRef{Namespace: "..", Repo: "demo", Number: 7}, "1", "b"); !errors.Is(err, provider.ErrProtocol) {
		t.Errorf("bad ref: err = %v", err)
	}
	if len(f.requests()) != 0 {
		t.Fatalf("requests were made: %+v", f.requests())
	}
}

func TestEditCommentErrorClasses(t *testing.T) {
	for _, c := range []struct {
		getStatus, patchStatus int
		want                   error
	}{
		{500, 0, provider.ErrUpstream}, {403, 0, provider.ErrAuth},
		{0, 403, provider.ErrAuth}, {0, 404, provider.ErrNotFound}, {0, 422, provider.ErrProtocol},
	} {
		f := newFake(t, "")
		f.registerUser()
		fail := func(st int) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom "+testMarker+testToken, st) }
		}
		if c.getStatus != 0 {
			f.handle("GET", commentAPI+"9", fail(c.getStatus))
		} else {
			f.handleJSON("GET", commentAPI+"9", ownComment(9, botLogin, botID))
			f.handle("PATCH", commentAPI+"9", fail(c.patchStatus))
		}
		err := f.provider(t, nil).EditComment(context.Background(), ref(), "9", "b")
		if !errors.Is(err, c.want) || strings.Contains(err.Error(), testMarker) || strings.Contains(err.Error(), testToken) {
			t.Errorf("%+v: err = %v", c, err)
		}
	}
}

// ---- inline comments ----

// fakeReviews is a stateful model of Gitea's review endpoints. A review post
// adds every comment to the poster's PENDING review (creating it), and then
// submits it, like CreatePullReview; reject decides, per post, whether the
// server fails after adding comments (leaving the PENDING review behind),
// and keepPending whether it answers with a PENDING review.
type fakeReviews struct {
	mu      sync.Mutex
	f       *fakeGitea
	nextID  int
	reviews []*fakeReview
	posts   []map[string]any

	reject      func(n int, comments []any) int // status, 0 = accept
	keepPending func(n int) bool
	// drop, when it returns a mode for post n, commits the review and then
	// breaks the connection: dropNoResponse closes it without a response,
	// dropBodyRead sends a 200 header and a truncated body.
	drop func(n int, comments []any) string
}

const (
	dropNoResponse = "no-response"
	dropBodyRead   = "body-read"
)

// breakConn hijacks the connection and closes it, after writing a 200
// header with a truncated body when mode is dropBodyRead.
func breakConn(t *testing.T, w http.ResponseWriter, mode string) {
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	if mode == dropBodyRead {
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"id\": ")
		_ = buf.Flush()
	}
	_ = conn.Close()
}

type fakeReview struct {
	id       int
	state    string
	login    string
	uid      int
	comments []map[string]any
}

func (r *fakeReview) json() map[string]any {
	return map[string]any{"id": r.id, "state": r.state, "html_url": reviewsBase + "#pullrequestreview-" + strconv.Itoa(r.id),
		"user": map[string]any{"id": r.uid, "login": r.login}}
}

func newFakeReviews(f *fakeGitea) *fakeReviews {
	fr := &fakeReviews{f: f, nextID: 100}
	f.handle("GET", reviewsAPI, func(w http.ResponseWriter, r *http.Request) {
		fr.mu.Lock()
		defer fr.mu.Unlock()
		out := []any{}
		if r.URL.Query().Get("page") == "1" {
			for _, rv := range fr.reviews {
				out = append(out, rv.json())
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	f.handle("POST", reviewsAPI, fr.post)
	for id := 1; id < 130; id++ {
		id := id
		f.handle("DELETE", reviewsAPI+"/"+strconv.Itoa(id), func(w http.ResponseWriter, _ *http.Request) {
			fr.mu.Lock()
			defer fr.mu.Unlock()
			for i, rv := range fr.reviews {
				if rv.id == id {
					fr.reviews = append(fr.reviews[:i], fr.reviews[i+1:]...)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			http.NotFound(w, nil)
		})
		f.handle("GET", reviewCommentsAPI(id), func(w http.ResponseWriter, _ *http.Request) {
			fr.mu.Lock()
			defer fr.mu.Unlock()
			out := []any{}
			for _, rv := range fr.reviews {
				if rv.id == id {
					for _, c := range rv.comments {
						out = append(out, c)
					}
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		})
	}
	return fr
}

func (fr *fakeReviews) post(w http.ResponseWriter, _ *http.Request) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	// The fake already consumed the request body; it is in the record.
	reqs := fr.f.requests()
	var in map[string]any
	_ = json.Unmarshal([]byte(reqs[len(reqs)-1].Body), &in)
	fr.posts = append(fr.posts, in)
	n := len(fr.posts)
	comments, _ := in["comments"].([]any)

	var pending *fakeReview
	for _, rv := range fr.reviews {
		if rv.state == "PENDING" && rv.uid == botID {
			pending = rv
		}
	}
	if pending == nil {
		fr.nextID++
		pending = &fakeReview{id: fr.nextID, state: "PENDING", login: botLogin, uid: botID}
		fr.reviews = append(fr.reviews, pending)
	}
	status := 0
	if fr.reject != nil {
		status = fr.reject(n, comments)
	}
	for i, c := range comments {
		if status != 0 && i == len(comments)-1 {
			break // the failing comment is the last one; earlier ones stay
		}
		cm, _ := c.(map[string]any)
		cid := 1000 + 10*n + i
		pos, _ := cm["new_position"].(float64)
		pending.comments = append(pending.comments, map[string]any{
			"id": cid, "path": cm["path"], "body": cm["body"], "position": int(pos),
			"user":     map[string]any{"id": botID, "login": botLogin},
			"html_url": reviewsBase + "/files#issuecomment-" + strconv.Itoa(cid),
		})
	}
	if status != 0 {
		http.Error(w, "boom "+testMarker, status)
		return
	}
	if fr.keepPending == nil || !fr.keepPending(n) {
		pending.state = "COMMENT"
	}
	if fr.drop != nil {
		if mode := fr.drop(n, comments); mode != "" {
			breakConn(fr.f.t, w, mode)
			return
		}
	}
	_ = json.NewEncoder(w).Encode(pending.json())
}

func (fr *fakeReviews) pendingLeft() int {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	n := 0
	for _, rv := range fr.reviews {
		if rv.state == "PENDING" {
			n++
		}
	}
	return n
}

func headPR() *provider.PullRequest { return &provider.PullRequest{HeadSHA: "headsha"} }

func sampleItems() []provider.InlineComment {
	return []provider.InlineComment{
		{Path: "src/app.go", Line: 2, LineType: provider.LineAdded, Body: "first " + testMarker},
		{Path: "src/app.go", Line: 3, LineType: provider.LineContext, Body: "second"},
		{Path: "src/renamed.go", OldPath: "src/old_name.go", Line: 2, LineType: provider.LineAdded, Body: "third"},
	}
}

func commentURL(id int) string { return reviewsBase + "/files#issuecomment-" + strconv.Itoa(id) }

func TestPostInlineCommentsOneReview(t *testing.T) {
	f := newFake(t, "/gitea")
	f.registerUser()
	fr := newFakeReviews(f)
	got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), sampleItems())
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.InlineResult{
		{Posted: true, ID: "1010", URL: commentURL(1010)},
		{Posted: true, ID: "1011", URL: commentURL(1011)},
		{Posted: true, ID: "1012", URL: commentURL(1012)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %+v", got)
	}
	if len(fr.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(fr.posts))
	}
	wantPost := map[string]any{"event": "COMMENT", "commit_id": "headsha", "body": "", "comments": []any{
		map[string]any{"path": "src/app.go", "body": "first " + testMarker, "new_position": float64(2), "old_position": float64(0)},
		map[string]any{"path": "src/app.go", "body": "second", "new_position": float64(3), "old_position": float64(0)},
		map[string]any{"path": "src/renamed.go", "body": "third", "new_position": float64(2), "old_position": float64(0)},
	}}
	if !reflect.DeepEqual(fr.posts[0], wantPost) {
		t.Fatalf("posted review = %#v", fr.posts[0])
	}
	if fr.pendingLeft() != 0 || len(methods(f, "DELETE")) != 0 {
		t.Fatal("unexpected pending review or delete")
	}
}

// [canary] no PENDING review: the batch fails after the server added some
// comments to a PENDING review. That review is deleted, each item is then
// posted on its own and reports its own outcome, and no review is left
// without its event.
func TestPostInlineCommentsBatchFailureFallsBackWithoutPendingReview(t *testing.T) {
	f := newFake(t, "")
	f.registerUser()
	fr := newFakeReviews(f)
	fr.reject = func(n int, comments []any) int {
		switch {
		case n == 1:
			return http.StatusInternalServerError // the batch
		case len(comments) == 1 && comments[0].(map[string]any)["body"] == "second":
			return http.StatusUnprocessableEntity // this item's line is refused
		}
		return 0
	}
	got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), sampleItems())
	if err != nil {
		t.Fatal(err)
	}
	if n := fr.pendingLeft(); n != 0 {
		t.Errorf("%d PENDING reviews left behind", n)
	}
	for _, r := range fr.reviews {
		if len(r.comments) != 1 {
			t.Errorf("review %d has %d comments; the batch's partial comments survived", r.id, len(r.comments))
		}
	}
	if len(fr.posts) != 4 || len(methods(f, "DELETE")) != 2 {
		t.Errorf("posts = %d deletes = %d", len(fr.posts), len(methods(f, "DELETE")))
	}
	want := []provider.InlineResult{
		{Posted: true, ID: "1020", URL: commentURL(1020)},
		{Error: "the server sent an unexpected response (HTTP 422)"},
		{Posted: true, ID: "1040", URL: commentURL(1040)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %+v", got)
	}
}

// A review that comes back PENDING (the event was not applied) is deleted,
// and the items report it.
func TestPostInlineCommentsPendingResponseIsDeleted(t *testing.T) {
	f := newFake(t, "")
	f.registerUser()
	fr := newFakeReviews(f)
	fr.keepPending = func(int) bool { return true }
	items := sampleItems()[:2]
	got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), items)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range got {
		if r.Posted || r.Error != "the server kept the review as a pending draft; it was deleted and the comment was not posted" {
			t.Errorf("item %d: %+v", i, r)
		}
	}
	if fr.pendingLeft() != 0 || len(fr.reviews) != 0 {
		t.Fatalf("reviews left: %d", len(fr.reviews))
	}
}

// A draft review the token's user already started would be submitted with
// our comments, so the call is refused before any post; another user's
// PENDING review (visible to admins) is neither a reason to refuse nor
// deleted.
func TestPostInlineCommentsExistingPendingReview(t *testing.T) {
	f := newFake(t, "")
	f.registerUser()
	fr := newFakeReviews(f)
	fr.reviews = []*fakeReview{{id: 5, state: "PENDING", login: botLogin, uid: botID}}
	_, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), sampleItems())
	if !errors.Is(err, provider.ErrConflict) || len(fr.posts) != 0 || len(fr.reviews) != 1 {
		t.Fatalf("err = %v posts = %d", err, len(fr.posts))
	}

	f2 := newFake(t, "")
	f2.registerUser()
	fr2 := newFakeReviews(f2)
	fr2.reviews = []*fakeReview{{id: 5, state: "PENDING", login: "alice", uid: 7}}
	fr2.reject = func(n int, _ []any) int {
		if n == 1 {
			return 500
		}
		return 0
	}
	got, err := f2.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), sampleItems()[:1])
	if err != nil || !got[0].Posted {
		t.Fatalf("got %+v err %v", got, err)
	}
	if fr2.reviews[0].id != 5 || fr2.reviews[0].state != "PENDING" {
		t.Fatal("another user's pending review was touched")
	}
	for _, r := range methods(f2, "DELETE") {
		if strings.HasSuffix(r.Path, "/reviews/5") {
			t.Fatal("another user's review was deleted")
		}
	}
}

// An auth failure of the batch is not retried item by item.
func TestPostInlineCommentsAuthFailureStops(t *testing.T) {
	f := newFake(t, "")
	f.registerUser()
	fr := newFakeReviews(f)
	fr.reject = func(int, []any) int { return http.StatusForbidden }
	got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), sampleItems())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Posted || r.Error != "authentication failed: check the token and its scopes (HTTP 403)" {
			t.Errorf("result = %+v", r)
		}
	}
	if len(fr.posts) != 1 || fr.pendingLeft() != 0 {
		t.Fatalf("posts = %d pending = %d", len(fr.posts), fr.pendingLeft())
	}
}

// When the leftover draft cannot be removed, no further review is posted:
// it would reuse (and submit) the draft.
func TestPostInlineCommentsCleanupFailureStopsPosting(t *testing.T) {
	f := newFake(t, "")
	f.registerUser()
	fr := newFakeReviews(f)
	fr.reject = func(int, []any) int { return 500 }
	for id := 101; id < 103; id++ {
		f.handle("DELETE", reviewsAPI+"/"+strconv.Itoa(id), func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", 500) })
	}
	got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), sampleItems())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Posted || r.Error != "a pending draft review could not be removed; the comment was not posted" {
			t.Errorf("result = %+v", r)
		}
	}
	if len(fr.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(fr.posts))
	}
}

// The review comment lookup failing still reports the items as posted, with
// the review's URL.
func TestPostInlineCommentsLookupFailureKeepsReviewURL(t *testing.T) {
	f := newFake(t, "")
	f.registerUser()
	newFakeReviews(f)
	f.handle("GET", reviewCommentsAPI(101), func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "x", 500) })
	got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), sampleItems()[:1])
	if err != nil || !reflect.DeepEqual(got, []provider.InlineResult{{Posted: true, URL: reviewsBase + "#pullrequestreview-101"}}) {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestPostInlineCommentsInvalidInputMakesNoRequest(t *testing.T) {
	f := newFake(t, "")
	p := f.provider(t, nil)
	ok := provider.InlineComment{Path: "a.go", Line: 1, LineType: provider.LineAdded, Body: "b"}
	mod := func(m func(*provider.InlineComment)) []provider.InlineComment {
		c := ok
		m(&c)
		return []provider.InlineComment{ok, c}
	}
	for name, c := range map[string]struct {
		pr    *provider.PullRequest
		items []provider.InlineComment
		hint  string
	}{
		"empty path":     {headPR(), mod(func(c *provider.InlineComment) { c.Path = " " }), "invalid inline comment path"},
		"newline path":   {headPR(), mod(func(c *provider.InlineComment) { c.Path = "a\n.go" }), "invalid inline comment path"},
		"bad old path":   {headPR(), mod(func(c *provider.InlineComment) { c.OldPath = "a\x00" }), "invalid inline comment path"},
		"zero line":      {headPR(), mod(func(c *provider.InlineComment) { c.Line = 0 }), "invalid inline comment line"},
		"bad line type":  {headPR(), mod(func(c *provider.InlineComment) { c.LineType = "ADDED" }), "invalid inline comment line type"},
		"empty body":     {headPR(), mod(func(c *provider.InlineComment) { c.Body = "\n " }), "empty body"},
		"nil pr":         {nil, []provider.InlineComment{ok}, "the pull request head commit is unknown"},
		"no head commit": {&provider.PullRequest{}, []provider.InlineComment{ok}, "the pull request head commit is unknown"},
	} {
		got, err := p.PostInlineComments(context.Background(), ref(), c.pr, c.items)
		var pe *provider.Error
		if got != nil || !errors.As(err, &pe) || pe.Class != provider.ClassProtocol || pe.Hint != c.hint {
			t.Errorf("%s: got %v err = %v", name, got, err)
		}
	}
	got, err := p.PostInlineComments(context.Background(), ref(), headPR(), nil)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("no items: got %#v err %v", got, err)
	}
	if len(f.requests()) != 0 {
		t.Fatalf("requests were made: %+v", f.requests())
	}
}

func TestWriteDebugLogsDoNotLeak(t *testing.T) {
	f := newFake(t, "/gitea")
	f.registerUser()
	fr := newFakeReviews(f)
	fr.reject = func(n int, _ []any) int {
		if n == 1 {
			return 500
		}
		return 0
	}
	c := ownComment(101, botLogin, botID)
	f.handleJSON("GET", commentAPI+"101", c)
	f.handleJSON("PATCH", commentAPI+"101", c)
	f.handleJSON("GET", commentAPI+"102", ownComment(102, authorMarker, 7))

	var logs bytes.Buffer
	p, err := gitea.NewFactory().New(f.config(), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	items := []provider.InlineComment{{Path: "src/" + pathMarker + ".go", Line: 1, LineType: provider.LineAdded, Body: "x " + testMarker}}
	if _, err := p.PostInlineComments(ctx, ref(), headPR(), items); err != nil {
		t.Fatal(err)
	}
	if err := p.EditComment(ctx, ref(), "101", "edited "+testMarker); err != nil {
		t.Fatal(err)
	}
	if err := p.EditComment(ctx, ref(), "102", "edited "+testMarker); err == nil || strings.Contains(err.Error(), authorMarker) {
		t.Fatalf("err = %v", err)
	}
	log := logs.String()
	if !strings.Contains(log, "batch failed") || !strings.Contains(log, "pending reviews deleted") {
		t.Fatalf("debug log lacks expected entries, test is not meaningful:\n%s", log)
	}
	for name, m := range map[string]string{"token": testToken, "body marker": testMarker, "author marker": authorMarker, "path marker": pathMarker} {
		if strings.Contains(log, m) {
			t.Errorf("%s appears in logs:\n%s", name, log)
		}
	}
}

func (fr *fakeReviews) countBody(body string) int {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	n := 0
	for _, rv := range fr.reviews {
		for _, c := range rv.comments {
			if c["body"] == body {
				n++
			}
		}
	}
	return n
}

const unknownOutcome = "the outcome of the review request is unknown; the comment may have been posted and was not posted again"

// [canary] outcome unknown: the server creates and submits the batch review,
// then the connection breaks (no response, or a 2xx whose body cannot be
// read). The items must not be posted again: that would duplicate every
// comment on the PR.
func TestPostInlineCommentsUnknownBatchOutcomeIsNotReposted(t *testing.T) {
	for _, mode := range []string{dropNoResponse, dropBodyRead} {
		f := newFake(t, "")
		f.registerUser()
		fr := newFakeReviews(f)
		fr.drop = func(n int, _ []any) string {
			if n == 1 {
				return mode
			}
			return ""
		}
		got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), sampleItems())
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if len(fr.posts) != 1 {
			t.Errorf("%s: %d review posts, want 1 (the items were posted again)", mode, len(fr.posts))
		}
		if len(fr.reviews) != 1 || fr.reviews[0].state != "COMMENT" || len(fr.reviews[0].comments) != 3 {
			t.Errorf("%s: %d reviews on the fake, want exactly the one submitted review", mode, len(fr.reviews))
		}
		for _, it := range sampleItems() {
			if n := fr.countBody(it.Body); n != 1 {
				t.Errorf("%s: comment %q is on the PR %d times", mode, it.Body, n)
			}
		}
		for i, r := range got {
			if r.Posted || r.Error != unknownOutcome {
				t.Errorf("%s: item %d: %+v", mode, i, r)
			}
		}
		if len(methods(f, "DELETE")) != 0 || fr.pendingLeft() != 0 {
			t.Errorf("%s: unexpected delete or pending review", mode)
		}
	}
}

// The same rule holds for one item of the per-item fallback: it is not
// retried, says that it may have been posted, and the next item goes on.
func TestPostInlineCommentsUnknownItemOutcomeIsNotRetried(t *testing.T) {
	f := newFake(t, "")
	f.registerUser()
	fr := newFakeReviews(f)
	fr.reject = func(n int, _ []any) int {
		if n == 1 {
			return http.StatusInternalServerError
		}
		return 0
	}
	fr.drop = func(_ int, comments []any) string {
		if len(comments) == 1 && comments[0].(map[string]any)["body"] == "second" {
			return dropNoResponse
		}
		return ""
	}
	got, err := f.provider(t, nil).PostInlineComments(context.Background(), ref(), headPR(), sampleItems())
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Posted || got[1].Posted || got[1].Error != unknownOutcome || !got[2].Posted {
		t.Fatalf("results = %+v", got)
	}
	if len(fr.posts) != 4 || fr.countBody("second") != 1 || fr.pendingLeft() != 0 {
		t.Fatalf("posts = %d, copies of the second comment = %d", len(fr.posts), fr.countBody("second"))
	}
}

func TestUpdatePullRequestSendsOnlyTheNamedFields(t *testing.T) {
	title, desc := "New title", "New body\r\n```\r\ncode  \r\n```\r\n\U0001F680"
	for name, c := range map[string]struct {
		up   provider.UpdatePR
		want string
	}{
		"title only":       {provider.UpdatePR{Title: &title}, `{"title":"New title"}`},
		"description only": {provider.UpdatePR{Description: &desc}, `{"body":"New body\r\n` + "```" + `\r\ncode  \r\n` + "```" + `\r\n` + "\U0001F680" + `"}`},
		"both":             {provider.UpdatePR{Title: &title, Description: new(string), Version: "9"}, `{"body":"","title":"New title"}`},
	} {
		f := newFake(t, "/gitea")
		f.handleJSON("PATCH", prAPI, prJSON("m"))
		if err := f.provider(t, nil).UpdatePullRequest(context.Background(), ref(), c.up); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := methods(f, "PATCH")
		if len(got) != 1 || got[0].Path != prAPI || got[0].Body != c.want {
			t.Errorf("%s: requests = %+v, want one PATCH %s", name, got, c.want)
		}
	}
}

func TestUpdatePullRequestIsValidatedBeforeAnyRequest(t *testing.T) {
	empty, multi := " ", "a\nb"
	f := newFake(t, "")
	f.handleJSON("PATCH", prAPI, prJSON("m"))
	p := f.provider(t, nil)
	for name, up := range map[string]provider.UpdatePR{
		"nothing":    {},
		"blank":      {Title: &empty},
		"multi line": {Title: &multi},
		"version":    {Title: &multi, Version: "x"},
	} {
		if err := p.UpdatePullRequest(context.Background(), ref(), up); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestUpdatePullRequestErrors(t *testing.T) {
	title := "T"
	for status, want := range map[int]*provider.Error{401: provider.ErrAuth, 403: provider.ErrAuth, 404: provider.ErrNotFound, 500: provider.ErrUpstream} {
		f := newFake(t, "")
		f.handle("PATCH", prAPI, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, testMarker+" "+testToken, status)
		})
		err := f.provider(t, nil).UpdatePullRequest(context.Background(), ref(), provider.UpdatePR{Title: &title})
		if !errors.Is(err, want) || strings.Contains(err.Error(), testMarker) || strings.Contains(err.Error(), testToken) {
			t.Errorf("%d: err = %v", status, err)
		}
	}
}
