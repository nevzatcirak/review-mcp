package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tools"
)

func decodeComments(t *testing.T, out string) tools.PRCommentsResult {
	t.Helper()
	var res tools.PRCommentsResult
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("stdout is not a PRCommentsResult: %v\n%s", err, out)
	}
	return res
}

func threadIDs(res tools.PRCommentsResult) string {
	var ids []string
	for _, th := range res.Threads {
		ids = append(ids, th.ID)
	}
	return strings.Join(ids, ",")
}

func commentIDs(th tools.ThreadOut) string {
	var ids []string
	for _, c := range th.Comments {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ",")
}

// checkCommentStreams asserts the stream rules of the comment commands: no
// token anywhere, and comment content (body and author markers) only on stdout.
func checkCommentStreams(t *testing.T, out, errs string) {
	t.Helper()
	for _, s := range []string{diagGiteaToken, diagBBSToken, fakeLLMKey, diagMarker} {
		if strings.Contains(out, s) || strings.Contains(errs, s) {
			t.Errorf("%q leaked", s)
		}
	}
	for _, s := range []string{commentBodyMarker, commentAuthorMarker} {
		if strings.Contains(errs, s) {
			t.Errorf("comment content %q leaked to stderr:\n%s", s, errs)
		}
	}
}

func TestDiagCommentsGitea(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)

	code, out, errs := diag(env, "comments", g.giteaPR()+"?token=URLQUERYSECRET")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	checkCommentStreams(t, out, errs)
	if !strings.HasSuffix(out, "}\n") || !strings.HasPrefix(out, "{\n  \"pr\": {") || strings.HasSuffix(out, "\n\n") {
		t.Errorf("not 2-space indented JSON with one trailing newline:\n%s", out)
	}
	if strings.Contains(out, "URLQUERYSECRET") {
		t.Error("URL query value printed")
	}
	res := decodeComments(t, out)
	if res.PR.Kind != "gitea" || res.PR.URL != g.giteaPR()+"?token=REDACTED" {
		t.Errorf("pr = %+v", res.PR)
	}
	if threadIDs(res) != "101,201" || res.Truncated.ResolvedHidden != 1 {
		t.Errorf("threads %s hidden %d", threadIDs(res), res.Truncated.ResolvedHidden)
	}
	if th := res.Threads[0]; th.Kind != "general" || th.ReplyInThread || th.Resolved != nil ||
		th.Comments[0].Author != commentAuthorMarker || !strings.Contains(th.Comments[0].Body, commentBodyMarker) {
		t.Errorf("general thread = %+v", th)
	}
	if th := res.Threads[1]; th.Kind != "inline" || th.Path != "src/app.go" || th.Line != 10 ||
		th.Resolved == nil || *th.Resolved || commentIDs(th) != "201,202" {
		t.Errorf("inline thread = %+v", th)
	}

	code, out, errs = diag(env, "comments", g.giteaPR(), "--include-resolved") // flag after the URL
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	res = decodeComments(t, out)
	if threadIDs(res) != "101,201,301" || res.Truncated.ResolvedHidden != 0 {
		t.Errorf("--include-resolved: threads %s hidden %d", threadIDs(res), res.Truncated.ResolvedHidden)
	}
}

func TestDiagCommentsBitbucket(t *testing.T) {
	b := newFakeBBS(t)
	env := diagEnv(nil, b)

	code, out, errs := diag(env, "comments", b.bbsPR())
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	checkCommentStreams(t, out, errs)
	res := decodeComments(t, out)
	if res.PR.Kind != "bitbucket_server" || threadIDs(res) != "1,10" || res.Truncated.ResolvedHidden != 1 {
		t.Errorf("kind %s threads %s hidden %d", res.PR.Kind, threadIDs(res), res.Truncated.ResolvedHidden)
	}
	if th := res.Threads[0]; th.Kind != "general" || !th.ReplyInThread || commentIDs(th) != "1,2" {
		t.Errorf("general thread = %+v", th)
	}
	if th := res.Threads[1]; th.Kind != "inline" || th.Path != "src/app.go" || th.Line != 12 {
		t.Errorf("inline thread = %+v", th)
	}

	code, out, errs = diag(env, "comments", "--include-resolved", b.bbsPR()) // flag before the URL
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if res = decodeComments(t, out); threadIDs(res) != "1,20,10" { // general threads first
		t.Errorf("--include-resolved: threads %s", threadIDs(res))
	}
}

func decodeReply(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if len(m) != 3 || m["id"] == nil || m["url"] == nil || m["in_thread"] == nil {
		t.Fatalf("reply keys = %v, want exactly id, url, in_thread", m)
	}
	if !strings.HasSuffix(out, "}\n") || !strings.HasPrefix(out, "{\n  \"id\"") {
		t.Errorf("not 2-space indented JSON with a trailing newline:\n%q", out)
	}
	return m
}

func TestDiagReplyGitea(t *testing.T) {
	g := newFakeGitea(t)
	const body = "  thanks\n\n```\nkept verbatim\n```  "
	// 201 is a review comment: the issue-comment lookup 404s and the
	// provider falls back to scanning the review comments.
	code, out, errs := diag(diagEnv(g, nil), "reply", g.giteaPR(), "--comment-id", "201", "--body", body)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	checkCommentStreams(t, out, errs)
	m := decodeReply(t, out)
	if m["id"] != "55" || m["in_thread"] != false || m["url"] != "https://your-gitea.example/octo/demo/pulls/7" {
		t.Errorf("result = %v", m)
	}
	posts := g.posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %v", posts)
	}
	var posted map[string]string
	if err := json.Unmarshal([]byte(posts[0]), &posted); err != nil {
		t.Fatal(err)
	}
	if want := "> Replying to @bob on src/app.go:10\n\n" + body; posted["body"] != want {
		t.Errorf("posted body = %q, want %q", posted["body"], want)
	}

	// A general comment: quote header without a path.
	g2 := newFakeGitea(t)
	code, out, errs = diag(diagEnv(g2, nil), "reply", g2.giteaPR(), "--body", "ok", "--comment-id", "101")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if m := decodeReply(t, out); m["in_thread"] != false {
		t.Errorf("result = %v", m)
	}
	var posted2 map[string]string
	if p := g2.posts(); len(p) != 1 || json.Unmarshal([]byte(p[0]), &posted2) != nil ||
		posted2["body"] != "> Replying to @"+commentAuthorMarker+"\n\nok" {
		t.Errorf("posts = %v", p)
	}
}

func TestDiagReplyBitbucket(t *testing.T) {
	b := newFakeBBS(t)
	code, out, errs := diag(diagEnv(nil, b), "reply", b.bbsPR(), "--comment-id", "10", "--body", "thanks")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	checkCommentStreams(t, out, errs)
	m := decodeReply(t, out)
	if m["id"] != "77" || m["in_thread"] != true || m["url"] == "" {
		t.Errorf("result = %v", m)
	}
	posts := b.posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %v", posts)
	}
	var posted struct {
		Text   string `json:"text"`
		Parent struct {
			ID int `json:"id"`
		} `json:"parent"`
	}
	if err := json.Unmarshal([]byte(posts[0]), &posted); err != nil || posted.Text != "thanks" || posted.Parent.ID != 10 {
		t.Errorf("posted = %q (%v)", posts[0], err)
	}
}

func TestDiagCommentsAndReplyUsageErrors(t *testing.T) {
	g, b := newFakeGitea(t), newFakeBBS(t)
	env := diagEnv(g, b)
	u := g.giteaPR()
	for name, args := range map[string][]string{
		"comments no url":          {"comments"},
		"comments two urls":        {"comments", u, u},
		"comments bad flag":        {"comments", u, "--bogus"},
		"comments bad bool":        {"comments", u, "--include-resolved=maybe"},
		"comments stray arg":       {"comments", u, "extra"},
		"reply no url":             {"reply", "--comment-id", "1", "--body", "x"},
		"reply two urls":           {"reply", u, u, "--comment-id", "1", "--body", "x"},
		"reply no comment id":      {"reply", u, "--body", "x"},
		"reply empty comment id":   {"reply", u, "--comment-id", "", "--body", "x"},
		"reply non-numeric id":     {"reply", u, "--comment-id", "abc", "--body", "x"},
		"reply zero id":            {"reply", u, "--comment-id", "0", "--body", "x"},
		"reply negative id":        {"reply", u, "--comment-id", "-3", "--body", "x"},
		"reply padded id":          {"reply", u, "--comment-id", " 3", "--body", "x"},
		"reply no body":            {"reply", u, "--comment-id", "1"},
		"reply empty body":         {"reply", u, "--comment-id", "1", "--body", ""},
		"reply whitespace body":    {"reply", u, "--comment-id", "1", "--body", " \n\t"},
		"reply bad flag":           {"reply", u, "--comment-id", "1", "--body", "x", "--bogus"},
		"reply flag without value": {"reply", u, "--body", "x", "--comment-id"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errs := diag(env, args...)
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if out != "" {
				t.Errorf("stdout must be empty, got %q", out)
			}
			if !strings.Contains(errs, "usage:") || !strings.Contains(errs, "diag comments <PR_URL>") || !strings.Contains(errs, "diag reply <PR_URL>") {
				t.Errorf("stderr lacks the usage text: %q", errs)
			}
		})
	}
	if n := g.requests() + b.requests(); n != 0 {
		t.Errorf("usage errors made %d requests", n)
	}
}

func TestDiagCommentsAndReplyInvalidConfigNoNetwork(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12"
	for _, args := range [][]string{
		{"comments", g.giteaPR()},
		{"reply", g.giteaPR(), "--comment-id", "1", "--body", "x"},
	} {
		code, out, errs := diag(env, args...)
		if code != 1 || out != "" || strings.TrimSpace(errs) == "" {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out, errs)
		}
	}
	if n := g.requests(); n != 0 {
		t.Errorf("invalid config made %d requests", n)
	}
}

func TestDiagCommentsAndReplyProviderErrors(t *testing.T) {
	bad := newFakeGitea(t)
	bad.failStatus.Store(401)
	env := diagEnv(bad, nil)
	want := (&provider.Error{Class: provider.ClassAuth, Status: 401}).Error()
	for _, args := range [][]string{
		{"comments", bad.giteaPR()},
		{"reply", bad.giteaPR(), "--comment-id", "101", "--body", "x"},
	} {
		code, out, errs := diag(env, args...)
		if code != 1 || out != "" || strings.TrimSpace(errs) == "" || !hasLine(errs, want) {
			t.Errorf("%v: exit %d stdout %q; stderr lacks %q:\n%s", args, code, out, want, errs)
		}
		checkCommentStreams(t, out, errs)
	}

	// An unknown comment id is not_found, with the fixed sentence.
	g := newFakeGitea(t)
	code, out, errs := diag(diagEnv(g, nil), "reply", g.giteaPR(), "--comment-id", "999", "--body", "x")
	if wantNF := (&provider.Error{Class: provider.ClassNotFound, Status: 404}).Error(); code != 1 || out != "" || !hasLine(errs, wantNF) {
		t.Errorf("unknown id: exit %d stdout %q stderr %q", code, out, errs)
	}
	if p := g.posts(); len(p) != 0 {
		t.Errorf("a reply to an unknown comment was posted: %v", p)
	}
}

func TestTopLevelUsageListsCommentCommands(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"bogus"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	for _, s := range []string{"diag comments <PR_URL> [--include-resolved]", "diag reply <PR_URL> --comment-id <ID> --body <TEXT>"} {
		if !strings.Contains(errb.String(), s) {
			t.Errorf("usage lacks %q:\n%s", s, errb.String())
		}
	}
}
