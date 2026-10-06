package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// ---- fakes ----

type fakeProvider struct {
	provider.Provider // nil: any method the tests do not expect panics

	threads  []provider.Thread
	listErr  error
	reply    *provider.ReplyResult
	replyErr error

	gotComment, gotBody string
	replyCalls          int
}

func (f *fakeProvider) ListThreads(context.Context, provider.PRRef) ([]provider.Thread, error) {
	return f.threads, f.listErr
}

func (f *fakeProvider) ReplyToComment(_ context.Context, _ provider.PRRef, id, body string) (*provider.ReplyResult, error) {
	f.replyCalls++
	f.gotComment, f.gotBody = id, body
	return f.reply, f.replyErr
}

type fakeResolver struct {
	p   *fakeProvider
	err error
	got string
}

func (r *fakeResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	r.got = u
	if r.err != nil {
		return provider.PRRef{}, nil, r.err
	}
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

const testPRURL = "https://user:FAKE-pw@your-gitea.example/octo/demo/pulls/7?token=FAKE-q" //nolint:gosec // synthetic fake credentials used to test redaction

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func item(id, author, body string, min int) provider.CommentItem {
	ts := t0.Add(time.Duration(min) * time.Minute)
	return provider.CommentItem{ID: id, Author: author, Body: body, CreatedAt: ts, UpdatedAt: ts}
}

func general(id string, min int, body string) provider.Thread {
	return provider.Thread{ID: id, Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{item(id, "alice", body, min)}}
}

func inline(id, path string, line, min int, resolved *bool, body string) provider.Thread {
	return provider.Thread{ID: id, Kind: provider.ThreadInline, Path: path, Line: line, Resolved: resolved,
		Comments: []provider.CommentItem{item(id, "bob", body, min)}}
}

func bptr(b bool) *bool { return &b }

func ids(ts []ThreadOut) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func list(t *testing.T, ts []provider.Thread, include bool) PRCommentsResult {
	t.Helper()
	res, err := PRComments(context.Background(), &fakeResolver{p: &fakeProvider{threads: ts}}, testPRURL, include)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// ---- PRComments ----

func TestPRCommentsRefIsRedacted(t *testing.T) {
	res := list(t, nil, false)
	if res.PR.Kind != "gitea" {
		t.Errorf("kind = %q", res.PR.Kind)
	}
	for _, s := range []string{"FAKE-pw", "FAKE-q", "user:"} {
		if strings.Contains(res.PR.URL, s) {
			t.Errorf("url %q contains %q", res.PR.URL, s)
		}
	}
	if !strings.HasPrefix(res.PR.URL, "https://your-gitea.example/octo/demo/pulls/7") {
		t.Errorf("url = %q", res.PR.URL)
	}
}

func TestPRCommentsResolvedFiltering(t *testing.T) {
	ts := []provider.Thread{
		general("1", 1, "g"),                              // Resolved nil: unknown, shown
		inline("2", "a.go", 1, 2, bptr(true), "resolved"), // hidden by default
		inline("3", "a.go", 2, 3, bptr(false), "open"),
		inline("4", "b.go", 1, 4, bptr(true), "resolved too"),
	}
	res := list(t, ts, false)
	if got := strings.Join(ids(res.Threads), ","); got != "1,3" {
		t.Errorf("default threads = %s, want 1,3", got)
	}
	if res.Truncated.ResolvedHidden != 2 {
		t.Errorf("resolved_hidden = %d, want 2", res.Truncated.ResolvedHidden)
	}
	res = list(t, ts, true)
	if got := strings.Join(ids(res.Threads), ","); got != "1,2,3,4" {
		t.Errorf("include_resolved threads = %s", got)
	}
	if res.Truncated.ResolvedHidden != 0 {
		t.Errorf("resolved_hidden = %d, want 0", res.Truncated.ResolvedHidden)
	}
	if res.Threads[0].Resolved != nil || res.Threads[1].Resolved == nil || !*res.Threads[1].Resolved {
		t.Errorf("resolved state not carried: %+v / %+v", res.Threads[0].Resolved, res.Threads[1].Resolved)
	}
}

func TestPRCommentsThreadCapKeepsNewestRoots(t *testing.T) {
	const n = MaxThreads + 5
	ts := make([]provider.Thread, 0, n)
	for i := 0; i < n; i++ {
		// Root times are a permutation of 0..n-1 minutes that differs from
		// the provider order, so "drop the last 5" and "drop the 5 oldest"
		// are different answers.
		ts = append(ts, general(strconv.Itoa(i), (i*37)%n, "b"))
	}
	res := list(t, ts, false)
	if len(res.Threads) != MaxThreads || res.Truncated.ThreadsOmitted != 5 {
		t.Fatalf("threads = %d omitted = %d", len(res.Threads), res.Truncated.ThreadsOmitted)
	}
	wantIDs := []string{}
	for i := 0; i < n; i++ {
		if (i*37)%n >= 5 { // minutes 0..4 are the oldest roots
			wantIDs = append(wantIDs, strconv.Itoa(i))
		}
	}
	if got, want := strings.Join(ids(res.Threads), ","), strings.Join(wantIDs, ","); got != want {
		t.Errorf("kept threads (provider order) = %s\nwant %s", got, want)
	}
	// The cap runs after the resolved filter.
	ts = append(ts, inline("r", "a.go", 1, 0, bptr(true), "x"))
	res = list(t, ts, false)
	if res.Truncated.ResolvedHidden != 1 || res.Truncated.ThreadsOmitted != 5 {
		t.Errorf("counts = %+v", res.Truncated)
	}
}

func TestPRCommentsExactlyAtCapIsNotTruncated(t *testing.T) {
	ts := make([]provider.Thread, 0, MaxThreads)
	for i := 0; i < MaxThreads; i++ {
		ts = append(ts, general(strconv.Itoa(i), i, "b"))
	}
	res := list(t, ts, false)
	if len(res.Threads) != MaxThreads || res.Truncated.ThreadsOmitted != 0 {
		t.Errorf("threads = %d omitted = %d", len(res.Threads), res.Truncated.ThreadsOmitted)
	}
}

func TestTruncateBody(t *testing.T) {
	for name, unit := range map[string]string{"ascii": "a", "2-byte": "é", "3-byte": "日", "4-byte": "😀"} {
		t.Run(name, func(t *testing.T) {
			at := strings.Repeat(unit, MaxBodyChars)
			if got, cut := truncateBody(at); cut || got != at {
				t.Errorf("a body of exactly %d characters must be kept whole", MaxBodyChars)
			}
			over := at + unit + "tail"
			got, cut := truncateBody(over)
			if !cut {
				t.Fatal("expected truncation")
			}
			if !utf8.ValidString(got) {
				t.Fatal("cut body is not valid UTF-8 (cut inside a rune)")
			}
			if want := at + TruncationMarker; got != want {
				t.Errorf("got %d runes, want the first %d runes plus the marker", utf8.RuneCountInString(got), MaxBodyChars)
			}
		})
	}
}

func TestTruncateBodyInvalidUTF8(t *testing.T) {
	got, _ := truncateBody("ok\xff\xfe" + strings.Repeat("x", MaxBodyChars))
	if !utf8.ValidString(got) {
		t.Error("invalid UTF-8 survived")
	}
}

func TestPRCommentsCounts(t *testing.T) {
	long := strings.Repeat("日", MaxBodyChars+1)
	th := general("1", 1, long)
	th.Comments = append(th.Comments, item("2", "bob", long, 2), item("3", "bob", "short", 3))
	ts := []provider.Thread{th, inline("4", "a.go", 1, 4, bptr(true), long), general("5", 5, "fine")}
	res := list(t, ts, false)
	if res.Truncated.BodiesTruncated != 2 || res.Truncated.ResolvedHidden != 1 || res.Truncated.ThreadsOmitted != 0 {
		t.Errorf("counts = %+v", res.Truncated)
	}
	// A body that sits in a hidden thread is not counted.
	res = list(t, ts, true)
	if res.Truncated.BodiesTruncated != 3 {
		t.Errorf("bodies_truncated with include_resolved = %d, want 3", res.Truncated.BodiesTruncated)
	}
	if got := res.Threads[0].Comments[2].Body; got != "short" {
		t.Errorf("short body changed: %q", got)
	}
}

func TestPRCommentsArraysNeverNull(t *testing.T) {
	th := provider.Thread{ID: "1", Kind: provider.ThreadGeneral} // Comments nil
	for _, res := range []PRCommentsResult{list(t, nil, false), list(t, []provider.Thread{th}, false)} {
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "null") && !strings.Contains(string(raw), `"resolved":null`) {
			t.Errorf("null in %s", raw)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		if string(m["threads"]) == "null" {
			t.Errorf("threads is null: %s", raw)
		}
	}
	if raw, _ := json.Marshal(list(t, []provider.Thread{th}, false).Threads[0]); !strings.Contains(string(raw), `"comments":[]`) {
		t.Errorf("comments not an empty array: %s", raw)
	}
}

func TestPRCommentsStructureKeys(t *testing.T) {
	res := list(t, []provider.Thread{inline("2", "a.go", 3, 2, nil, "x")}, false)
	raw, _ := json.Marshal(res)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := func(v any) string { return strings.Join(sortedKeys(v.(map[string]any)), ",") }
	if got := keys(m); got != "pr,threads,truncated" {
		t.Errorf("top-level keys = %s", got)
	}
	if got := keys(m["pr"]); got != "kind,url" {
		t.Errorf("pr keys = %s", got)
	}
	if got := keys(m["truncated"]); got != "bodies_truncated,resolved_hidden,threads_omitted" {
		t.Errorf("truncated keys = %s", got)
	}
	thr := m["threads"].([]any)[0]
	if got := keys(thr); got != "comments,id,kind,line,outdated,path,reply_in_thread,resolved" {
		t.Errorf("thread keys = %s", got)
	}
	if got := keys(thr.(map[string]any)["comments"].([]any)[0]); got != "author,body,created_at,id,updated_at" {
		t.Errorf("comment keys = %s", got)
	}
}

func TestPRCommentsErrors(t *testing.T) {
	want := &provider.Error{Class: provider.ClassAuth, Status: 401}
	_, err := PRComments(context.Background(), &fakeResolver{err: want}, testPRURL, false)
	if !errors.Is(err, provider.ErrAuth) {
		t.Errorf("resolve error = %v", err)
	}
	_, err = PRComments(context.Background(), &fakeResolver{p: &fakeProvider{listErr: want}}, testPRURL, false)
	if !errors.Is(err, provider.ErrAuth) {
		t.Errorf("list error = %v", err)
	}
}

// ---- rendering ----

func sampleResult() PRCommentsResult {
	ts := []provider.Thread{
		general("101", 1, "Looks good overall.\nOne question about the retry logic."),
		inline("201", "src/app.go", 10, 5, bptr(true), "Why not use a constant here?"),
		inline("202", "src/old.go", 0, 6, nil, "Outdated anchor."),
	}
	ts[1].Outdated = true
	ts[1].Comments = append(ts[1].Comments, item("203", "alice", "Fixed in the next commit.\n```go\nconst x = 1\n```", 7))
	return list(&testing.T{}, ts, true)
}

func TestRenderPRCommentsMarkdown(t *testing.T) {
	md := RenderPRCommentsMarkdown(sampleResult())
	for _, want := range []string{
		"# Comments on `gitea https://your-gitea.example/octo/demo/pulls/7?token=REDACTED`",
		"3 threads shown",
		UntrustedNotice,
		"### Thread 101 · general\n",
		"### Thread 201 · `src/app.go:10` (outdated) (resolved)\n",
		"### Thread 202 · `src/old.go`\n", // line 0: path alone
		"**`alice`** · 2026-01-02T03:05:05Z · id 101",
		"**`bob`** · 2026-01-02T03:09:05Z · id 201",
		"````\nFixed in the next commit.", // a body with a 3-backtick run gets a 4-backtick fence
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "> Note") {
		t.Errorf("no note expected when nothing was cut:\n%s", md)
	}
	assertNoHTML(t, md)
}

func TestRenderEmptyAndNotes(t *testing.T) {
	md := RenderPRCommentsMarkdown(list(t, nil, false))
	if !strings.Contains(md, "No comment threads.") || !strings.Contains(md, UntrustedNotice) {
		t.Errorf("empty rendering:\n%s", md)
	}
	r := list(t, []provider.Thread{
		general("1", 1, strings.Repeat("x", MaxBodyChars+1)),
		inline("2", "a.go", 1, 2, bptr(true), "hidden"),
	}, false)
	r.Truncated.ThreadsOmitted = 3
	md = RenderPRCommentsMarkdown(r)
	for _, want := range []string{"3 older thread(s) omitted", "1 comment body(ies) cut at 4000", "1 resolved thread(s) hidden", "1 resolved hidden", "3 threads omitted", "1 bodies truncated"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	assertNoHTML(t, md)
}

func TestRenderNoHTMLOutsideCode(t *testing.T) {
	evil := "<script>alert(1)</script> <img src=x onerror=1> &lt; <!-- c -->"
	th := inline("7", "a.go", 1, 1, nil, evil)
	th.Comments[0].Author = "<b>mallory</b>"
	assertNoHTML(t, RenderPRCommentsMarkdown(list(t, []provider.Thread{th}, true)))
}

func TestRenderPathInjection(t *testing.T) {
	for _, path := range []string{
		"src/`a`<script>x</script>.go",
		"a``b```c````d",
		"`lead",
		"x\n# Fake heading\n**bold**",
		"x\r\n- injected item",
		"<img src=x>",
	} {
		th := inline("7", path, 4, 1, nil, "body")
		md := RenderPRCommentsMarkdown(list(t, []provider.Thread{th}, true))
		assertNoHTML(t, md)
		var heading string
		for _, l := range strings.Split(md, "\n") {
			if strings.HasPrefix(l, "### Thread 7") {
				heading = l
			}
			if strings.HasPrefix(l, "# Fake") || strings.HasPrefix(l, "- injected") {
				t.Errorf("path %q injected a line: %q", path, l)
			}
		}
		if heading == "" || !strings.HasSuffix(heading, "`") || !strings.Contains(heading, ":4") {
			t.Errorf("path %q: heading %q is not one line ending in a code span", path, heading)
		}
	}
}

func TestRenderAuthorInjection(t *testing.T) {
	for _, author := range []string{
		"**bold** _it_ [link](http://x) ![img](http://x)",
		"<img src=x onerror=1>",
		"mal`lory",
		"``double``",
		"two\nlines\n# heading",
		"a\r\n- list",
		"",
		"  ",
	} {
		th := general("1", 1, "body")
		th.Comments[0].Author = author
		md := RenderPRCommentsMarkdown(list(t, []provider.Thread{th}, true))
		assertNoHTML(t, md)
		var line string
		for _, l := range strings.Split(md, "\n") {
			if strings.HasPrefix(l, "**") {
				line = l
			}
			if strings.HasPrefix(l, "# heading") || strings.HasPrefix(l, "- list") {
				t.Errorf("author %q injected a line: %q", author, l)
			}
		}
		if !strings.HasSuffix(line, " · id 1") {
			t.Errorf("author %q: comment line %q does not end in the id", author, line)
		}
		if strings.TrimSpace(author) == "" && !strings.Contains(line, "`unknown`") {
			t.Errorf("empty author not rendered as unknown: %q", line)
		}
	}
}

// ---- fence safety ----

var (
	fenceOpen = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})([^`]*)$")
	fenceEnd  = regexp.MustCompile("^ {0,3}(`+|~+)[ \t]*$")
)

// parseFences is a small CommonMark-style fence parser: it returns the
// content of every fenced block (a fence is closed by a run of the same
// character that is at least as long as the opening one) and the lines
// outside any block. It fails when a block is never closed.
func parseFences(t *testing.T, md string) (blocks []string, outside []string) {
	t.Helper()
	var cur []string
	var char byte
	n := 0
	in := false
	for _, l := range strings.Split(md, "\n") {
		if !in {
			// A backtick fence's info string may not contain a backtick.
			if m := fenceOpen.FindStringSubmatch(l); m != nil && (m[1][0] != '`' || !strings.Contains(m[2], "`")) {
				in, char, n, cur = true, m[1][0], len(m[1]), nil
				continue
			}
			outside = append(outside, l)
			continue
		}
		if m := fenceEnd.FindStringSubmatch(l); m != nil && m[1][0] == char && len(m[1]) >= n {
			blocks = append(blocks, strings.Join(cur, "\n")+"\n")
			in = false
			continue
		}
		cur = append(cur, l)
	}
	if in {
		t.Fatalf("fenced block never closed:\n%s", md)
	}
	return blocks, outside
}

// TestRenderFenceSafety [canary]: bodies holding backtick and tilde fences
// of every length must stay inside their own fence: the parsed blocks equal
// the bodies exactly and nothing of a body leaks outside.
func TestRenderFenceSafety(t *testing.T) {
	bodies := []string{
		"plain",
		"```\nclose attempt\n```\n# Injected heading",
		"````\nfour\n````\n```\nthree\n```\n**bold**",
		"~~~\ntildes\n~~~\n# Injected after tildes",
		"~~~~~~\nlong tildes\n~~~~~~",
		"``` text\n`````````\n- injected item",
		"inline ``` run in the middle ```` of a line\n# H",
		"ends with fence\n```",
		"```",
		"",
		"trailing newline\n",
		"   ```\n   ~~~\n",
	}
	ts := []provider.Thread{}
	for i, b := range bodies {
		ts = append(ts, general(strconv.Itoa(i+1), i, b))
	}
	md := RenderPRCommentsMarkdown(list(t, ts, true))
	blocks, outside := parseFences(t, md)
	if len(blocks) != len(bodies) {
		t.Fatalf("parsed %d fenced blocks, want %d:\n%s", len(blocks), len(bodies), md)
	}
	for i, b := range bodies {
		want := b
		if !strings.HasSuffix(want, "\n") {
			want += "\n"
		}
		if blocks[i] != want {
			t.Errorf("body %d did not stay inside its fence:\n got %q\nwant %q", i, blocks[i], want)
		}
	}
	for _, l := range outside {
		for _, bad := range []string{"# Injected", "- injected", "**bold**", "close attempt", "tildes", "long tildes"} {
			if strings.Contains(l, bad) {
				t.Errorf("body text escaped its fence: %q", l)
			}
		}
	}
	assertNoHTML(t, md)
}

func TestFenceLen(t *testing.T) {
	for in, want := range map[string]int{
		"": 3, "a": 3, "``": 3, "```": 4, "````": 5, "a`b```c": 4, "`````````": 10, "~~~": 3,
	} {
		if got := fenceLen(in); got != want {
			t.Errorf("fenceLen(%q) = %d, want %d", in, got, want)
		}
	}
}

// ---- reply ----

func TestPRCommentReply(t *testing.T) {
	for _, inThread := range []bool{true, false} {
		fp := &fakeProvider{reply: &provider.ReplyResult{
			Comment:  provider.Comment{ID: "55", URL: "https://x.example/pr/7?token=FAKE-q#c55"},
			InThread: inThread,
		}}
		res, err := PRCommentReply(context.Background(), &fakeResolver{p: fp}, testPRURL, "12", "thanks")
		if err != nil {
			t.Fatal(err)
		}
		if res.ID != "55" || res.InThread != inThread || strings.Contains(res.URL, "FAKE-q") {
			t.Errorf("result = %+v", res)
		}
		if fp.gotComment != "12" || fp.gotBody != "thanks" {
			t.Errorf("provider got %q %q", fp.gotComment, fp.gotBody)
		}
		text := RenderPRCommentReplyText(res)
		wantFirst := "This provider cannot reply inside review threads; the reply was posted as a PR-level comment quoting the referenced comment."
		if inThread {
			wantFirst = "Reply posted in thread."
		}
		if !strings.HasPrefix(text, wantFirst+"\n\n") || !strings.Contains(text, "`55`") {
			t.Errorf("text = %q", text)
		}
		assertNoHTML(t, text)
	}
}

func TestPRCommentReplyErrors(t *testing.T) {
	pe := &provider.Error{Class: provider.ClassProtocol, Hint: "empty body"}
	_, err := PRCommentReply(context.Background(), &fakeResolver{p: &fakeProvider{replyErr: pe}}, testPRURL, "1", " ")
	if !errors.Is(err, provider.ErrProtocol) {
		t.Errorf("err = %v", err)
	}
	_, err = PRCommentReply(context.Background(), &fakeResolver{err: provider.ErrURLNotConfigured}, testPRURL, "1", "x")
	if !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Errorf("err = %v", err)
	}
	_, err = PRCommentReply(context.Background(), &fakeResolver{p: &fakeProvider{}}, testPRURL, "1", "x") // nil result
	if err == nil {
		t.Error("a nil provider result must be an error")
	}
}

func TestUserMessage(t *testing.T) {
	if le := (&llm.Error{Class: llm.ClassAuth, Status: 401, Hint: "REVIEW_MCP_LLM_API_KEY"}); UserMessage(fmt.Errorf("wrapped: %w", le)) != le.Error() {
		t.Errorf("llm error not shown as its fixed sentence")
	}
	pe := &provider.Error{Class: provider.ClassNotFound, Status: 404, Hint: "x"}
	if got := UserMessage(fmt.Errorf("wrapped: %w", pe)); got != pe.Error() {
		t.Errorf("provider error = %q", got)
	}
	const raw = "Get \"https://h.example/x?token=FAKE-q\": boom"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New(raw), genericErrorMessage},
		{fmt.Errorf("%s: %w", raw, context.DeadlineExceeded), genericErrorMessage + " (class: timeout)"},
		{fmt.Errorf("%s: %w", raw, context.Canceled), genericErrorMessage + " (class: canceled)"},
	} {
		if got := UserMessage(tc.err); got != tc.want {
			t.Errorf("UserMessage = %q, want %q", got, tc.want)
		}
	}
}
