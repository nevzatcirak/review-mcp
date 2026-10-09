package improve

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// The capabilities of the two providers that exist today (neither has
// SuggestionBlocks or QuickActions).
var (
	giteaCaps = provider.Capabilities{GFM: true, MarkdownTables: true, Labels: true, InlineComments: true}
	bbsCaps   = provider.Capabilities{MarkdownTables: true, InlineComments: true}
)

// storedComment is a comment of ours the fake keeps between runs.
type storedComment struct {
	id, body string
	inline   bool
	path     string
	line     int
}

// pubProvider is a stateful provider for publishing: it keeps the comments a
// run posts and lists them back as threads of the token's user, so a second
// run sees them. It wraps the synthetic PR of fakeProvider.
type pubProvider struct {
	*fakeProvider
	caps   provider.Capabilities
	stored []storedComment
	nextID int

	// scripted failures
	postErr, editErr, inlineErr, listErr error
	// inlineReason, when set, decides the result of the item at that index
	// of a call: Posted false with this reason.
	inlineReason map[int]provider.InlineReason

	// recorded writes
	posts   []string
	edits   []string // "id\x00body"
	batches [][]provider.InlineComment
}

func newPubProvider(h *harness, caps provider.Capabilities) *pubProvider {
	return &pubProvider{fakeProvider: h.prov, caps: caps, nextID: 100}
}

func (p *pubProvider) Capabilities() provider.Capabilities { return p.caps }

func (p *pubProvider) FileLineURL(_ provider.PRRef, _ *provider.PullRequest, path string, line int) string {
	return "https://your-gitea.example/octo/demo/src/" + path + "#L" + strconv.Itoa(line)
}

func (p *pubProvider) ListThreads(ctx context.Context, ref provider.PRRef) ([]provider.Thread, error) {
	if p.listErr != nil {
		return nil, p.listErr
	}
	threads, _ := p.fakeProvider.ListThreads(ctx, ref)
	threads = slices.Clone(threads)
	for i, c := range p.stored {
		ci := provider.CommentItem{ID: c.id, Author: botUser.Name, AuthorID: botUser.ID, AuthorLogin: botUser.Name, Body: c.body,
			CreatedAt: time.Date(2026, 10, 9, 9, i, 0, 0, time.UTC), URL: "https://your-gitea.example/c/" + c.id}
		th := provider.Thread{ID: "t" + c.id, Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{ci}}
		if c.inline {
			th.Kind, th.Path, th.Line = provider.ThreadInline, c.path, c.line
		}
		threads = append(threads, th)
	}
	return threads, nil
}

func (p *pubProvider) PostComment(_ context.Context, _ provider.PRRef, body string) (*provider.Comment, error) {
	if p.postErr != nil {
		return nil, p.postErr
	}
	p.nextID++
	id := strconv.Itoa(p.nextID)
	p.posts = append(p.posts, body)
	p.stored = append(p.stored, storedComment{id: id, body: body})
	return &provider.Comment{ID: id, URL: "https://your-gitea.example/c/" + id}, nil
}

func (p *pubProvider) EditComment(_ context.Context, _ provider.PRRef, id, body string) error {
	if p.editErr != nil {
		return p.editErr
	}
	p.edits = append(p.edits, id+"\x00"+body)
	for i := range p.stored {
		if p.stored[i].id == id {
			p.stored[i].body = body
		}
	}
	return nil
}

func (p *pubProvider) PostInlineComments(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, items []provider.InlineComment) ([]provider.InlineResult, error) {
	p.batches = append(p.batches, slices.Clone(items))
	if p.inlineErr != nil {
		return nil, p.inlineErr
	}
	out := make([]provider.InlineResult, len(items))
	for i, it := range items {
		if r, ok := p.inlineReason[i]; ok {
			out[i] = provider.InlineResult{Error: "the server sent an unexpected response (HTTP 422)", Reason: r}
			continue
		}
		p.nextID++
		id := strconv.Itoa(p.nextID)
		p.stored = append(p.stored, storedComment{id: id, body: it.Body, inline: true, path: it.Path, line: it.Line})
		out[i] = provider.InlineResult{Posted: true, ID: id, URL: "https://your-gitea.example/c/" + id, Reason: provider.InlineReasonPosted}
	}
	return out, nil
}

// inlineBodies are the bodies of every inline comment posted so far.
func (p *pubProvider) inlineBodies() []string {
	var out []string
	for _, b := range p.batches {
		for _, it := range b {
			out = append(out, it.Body)
		}
	}
	return out
}

type pubResolver struct{ p provider.Provider }

func (r *pubResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

// sugUnverified quotes code that is nowhere in the diff: it stays unverified.
var sugUnverified = sug{"src/app.go", "go", "this code is not in the diff", "Handle the missing case.",
	"x := 1", "Handle the missing case", "general"}

// publishAnswers: two verified suggestions (app.go line 12, app_test.go line
// 2) and one unverified, all scored 8 or 9.
func publishAnswers() map[int][]string {
	return map[int][]string{
		0: {suggestionsAnswer(sugDelay, sugTest, sugUnverified)},
		reflectKind(0): {reflectionAnswer(
			fb{number: 1, summary: sugDelay.summary, file: "src/app.go", start: 12, end: 12, score: 8, why: "An unbounded `delay` can stall callers."},
			fb{number: 2, summary: sugTest.summary, file: "src/app_test.go", start: 2, end: 2, score: 9, why: "The test cannot fail."},
			fb{number: 3, summary: sugUnverified.summary, file: "src/app.go", start: 12, end: 12, score: 8, why: "A case is missing."},
		)},
	}
}

func newPubHarness(t *testing.T, caps provider.Capabilities, answers map[int][]string) (*harness, *pubProvider) {
	t.Helper()
	h := newHarness(answers)
	p := newPubProvider(h, caps)
	h.deps.Resolver = &pubResolver{p: p}
	h.deps.RenderOverview = testOverview
	h.deps.RenderInline = testInline
	return h, p
}

// testOverview and testInline are minimal renderers: the real ones are in
// improve/render, which this package cannot import. They carry what the
// pipeline's tests check: the summaries, and the anchors the links come from.
func testOverview(res *Result, _ provider.Capabilities, link func(string, int) string) string {
	var b strings.Builder
	b.WriteString("## OVERVIEW\n")
	for i := range res.Suggestions {
		s := &res.Suggestions[i]
		b.WriteString("- " + s.Summary)
		if s.Anchor != nil {
			b.WriteString(" [" + s.Anchor.Status + " " + s.Anchor.URL + "]")
		}
		if s.Verified {
			b.WriteString(" " + link(s.File, *s.StartLine))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func testInline(s *Suggestion, _ provider.Capabilities) string {
	return "**" + s.Summary + "**\n\n" + s.Content
}

func anchorStatuses(res *Result) map[string]string {
	out := map[string]string{}
	for _, s := range res.Suggestions {
		st := "<nil>"
		if s.Anchor != nil {
			st = s.Anchor.Status
		}
		out[s.Summary] = st
	}
	return out
}

// TestPublishOverviewAndInline: both providers' capabilities. The overview
// is posted first and once; only the verified suggestions are posted inline,
// each ending in its fingerprint marker; the overview is then edited once
// with the links; the unverified suggestion has no anchor.
func TestPublishOverviewAndInline(t *testing.T) {
	for name, caps := range map[string]provider.Capabilities{"gitea": giteaCaps, "bitbucket_server": bbsCaps} {
		t.Run(name, func(t *testing.T) {
			h, p := newPubHarness(t, caps, publishAnswers())
			res := h.run(t, Args{Publish: true})

			if len(p.posts) != 1 || !strings.HasSuffix(p.posts[0], "\n\n"+OverviewMarker) {
				t.Fatalf("overview posts = %q", p.posts)
			}
			if len(p.batches) != 1 || len(p.batches[0]) != 2 {
				t.Fatalf("inline batches = %+v, want one batch of the two verified suggestions", p.batches)
			}
			var lines []string
			for _, it := range p.batches[0] {
				lines = append(lines, it.Path+":"+strconv.Itoa(it.Line)+":"+string(it.LineType))
				fp, ok := ParseSuggestionMarker(it.Body)
				if !ok || !strings.HasSuffix(it.Body, SuggestionMarker(fp)) {
					t.Errorf("inline body does not end in a suggestion marker: %q", it.Body)
				}
				if strings.Contains(it.Body, "this code is not in the diff") || strings.Contains(it.Body, "missing case") {
					t.Errorf("the unverified suggestion was posted inline: %q", it.Body)
				}
			}
			if want := []string{"src/app_test.go:2:added", "src/app.go:12:added"}; !slices.Equal(lines, want) {
				t.Errorf("inline anchors = %v, want %v", lines, want)
			}
			want := map[string]string{
				sugTest.summary: AnchorPosted, sugDelay.summary: AnchorPosted, sugUnverified.summary: "<nil>"}
			if got := anchorStatuses(res); !mapEqual(got, want) {
				t.Errorf("anchors = %v, want %v", got, want)
			}
			pub := res.Publish
			if pub == nil || !pub.Published || pub.Updated || pub.CommentID == "" || pub.Error != "" ||
				pub.Inline == nil || *pub.Inline != (InlineSummary{Posted: 2}) {
				t.Errorf("publish = %+v inline=%+v", pub, pub.Inline)
			}
			// The one edit after the inline post carries the links.
			if len(p.edits) != 1 || !strings.Contains(p.edits[0], "posted https://your-gitea.example/c/") ||
				!strings.HasSuffix(p.edits[0], OverviewMarker) {
				t.Errorf("overview edits = %q", p.edits)
			}
			// The unverified suggestion is in the overview.
			if !strings.Contains(p.stored[0].body, sugUnverified.summary) {
				t.Errorf("the overview lacks the unverified suggestion: %q", p.stored[0].body)
			}
		})
	}
}

func mapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestPublishSecondRun: the overview of the first run is edited in place and
// the suggestions already on the PR (their fingerprint markers by the token's
// user) are not posted again.
func TestPublishSecondRun(t *testing.T) {
	h, p := newPubHarness(t, giteaCaps, publishAnswers())
	first := h.run(t, Args{Publish: true})
	overviewID := first.Publish.CommentID
	postsBefore, batchesBefore := len(p.posts), len(p.batches)

	h.llm.seen = nil // the scripted answers start again
	second := h.run(t, Args{Publish: true})

	if len(p.posts) != postsBefore {
		t.Errorf("a second overview was posted")
	}
	pub := second.Publish
	if !pub.Published || !pub.Updated || pub.CommentID != overviewID {
		t.Errorf("publish = %+v, want the overview %s updated in place", pub, overviewID)
	}
	if len(p.batches) != batchesBefore {
		t.Errorf("inline comments were posted again: %+v", p.batches[batchesBefore:])
	}
	if pub.Inline == nil || *pub.Inline != (InlineSummary{SkippedDuplicate: 2}) {
		t.Errorf("inline = %+v, want 2 skipped duplicates", pub.Inline)
	}
	want := map[string]string{
		sugTest.summary: AnchorSkippedDuplicate, sugDelay.summary: AnchorSkippedDuplicate, sugUnverified.summary: "<nil>"}
	if got := anchorStatuses(second); !mapEqual(got, want) {
		t.Errorf("anchors = %v, want %v", got, want)
	}
	if !slices.Contains(second.Notes, noteAlreadyPosted(2)) {
		t.Errorf("notes = %q", second.Notes)
	}
	// A marker typed by someone else does not make a suggestion a duplicate.
	var fps []string
	for _, b := range p.inlineBodies() {
		fp, _ := ParseSuggestionMarker(b)
		fps = append(fps, fp)
	}
	if len(fps) != 2 || fps[0] == fps[1] {
		t.Errorf("fingerprints = %v", fps)
	}
}

// TestPublishForeignMarkerIsNotOurs: a suggestion marker in a comment by
// another user, and an overview marker in one, are not adopted.
func TestPublishForeignMarkerIsNotOurs(t *testing.T) {
	h, p := newPubHarness(t, giteaCaps, publishAnswers())
	h.run(t, Args{Publish: true})
	// Hand every stored comment to another user.
	alice := provider.User{ID: "11", Name: "alice"}
	p.threads = nil
	for i, c := range p.stored {
		p.threads = append(p.threads, provider.Thread{ID: "x" + c.id, Kind: map[bool]provider.ThreadKind{true: provider.ThreadInline, false: provider.ThreadGeneral}[c.inline],
			Path: c.path, Line: c.line, Comments: []provider.CommentItem{comment(alice, i, c.body)}})
	}
	p.stored = nil
	h.llm.seen = nil
	posts, batches := len(p.posts), len(p.batches)
	second := h.run(t, Args{Publish: true})
	if len(p.posts) != posts+1 || len(p.batches) != batches+1 || second.Publish.Updated {
		t.Errorf("another user's markers were adopted: posts %d batches %d publish %+v", len(p.posts)-posts, len(p.batches)-batches, second.Publish)
	}
}

// TestPublishUnanchorable: a verified range that is not on lines of the
// diff's hunks has no inline comment and is listed in the overview.
func TestPublishUnanchorable(t *testing.T) {
	full := "package app\n\nfunc a() {}\nfunc b() {}\nfunc c() {}\n\nfunc d() {\n\tx := compute()\n}\n"
	h, p := newPubHarness(t, giteaCaps, nil)
	h.prov.files = append(h.prov.files, provider.FilePatch{
		Path: "src/far.go", Type: provider.ChangeModified,
		Patch:      "@@ -1,3 +1,3 @@\n package app\n \n-func a() {}\n+func a() { return }\n",
		HeadStatus: provider.ContentFull, HeadContent: &full, BaseStatus: provider.ContentNotFetchedSizeCap,
	})
	far := sug{"src/far.go", "go", "\tx := compute()", "Check the result.", "\tx, err := compute()", "Check the compute result", "general"}
	h.llm.answers = map[int][]string{
		0: {suggestionsAnswer(sugDelay, far)},
		reflectKind(0): {reflectionAnswer(
			fb{number: 1, summary: sugDelay.summary, file: "src/app.go", start: 12, end: 12, score: 8, why: "Stalls."},
			fb{number: 2, summary: far.summary, file: "src/far.go", start: 8, end: 8, score: 8, why: "Ignored error."},
		)}}
	res := h.run(t, Args{Publish: true})

	want := map[string]string{sugDelay.summary: AnchorPosted, far.summary: AnchorUnanchorable}
	if got := anchorStatuses(res); !mapEqual(got, want) {
		t.Fatalf("anchors = %v, want %v", got, want)
	}
	for _, sg := range res.Suggestions {
		if !sg.Verified {
			t.Errorf("%q is not verified (%s)", sg.Summary, sg.UnverifiedReason)
		}
	}
	if in := res.Publish.Inline; in == nil || *in != (InlineSummary{Posted: 1, Unanchorable: 1}) {
		t.Errorf("inline = %+v", in)
	}
	if len(p.batches) != 1 || len(p.batches[0]) != 1 || !slices.Contains(res.Notes, noteUnanchorable(1)) {
		t.Errorf("batches = %+v notes = %q", p.batches, res.Notes)
	}
}

// TestPublishProviderReasons: InlineResult.Reason decides between
// unanchorable (the server refused the position) and failed.
func TestPublishProviderReasons(t *testing.T) {
	h, p := newPubHarness(t, giteaCaps, publishAnswers())
	p.inlineReason = map[int]provider.InlineReason{0: provider.InlineReasonUnanchorable, 1: provider.InlineReasonFailed}
	res := h.run(t, Args{Publish: true})
	want := map[string]string{sugTest.summary: AnchorUnanchorable, sugDelay.summary: AnchorFailed, sugUnverified.summary: "<nil>"}
	if got := anchorStatuses(res); !mapEqual(got, want) {
		t.Errorf("anchors = %v, want %v", got, want)
	}
	if in := res.Publish.Inline; in == nil || *in != (InlineSummary{Unanchorable: 1, Failed: 1}) {
		t.Errorf("inline = %+v", in)
	}
	for _, n := range []string{noteUnanchorable(1), noteInlineFailed(1)} {
		if !slices.Contains(res.Notes, n) {
			t.Errorf("notes %q lack %q", res.Notes, n)
		}
	}
	// A suggestion whose comment failed is listed in the overview with its text.
	if a := res.Suggestions[1].Anchor; a == nil || a.Error == "" || a.Line != 12 {
		t.Errorf("failed anchor = %+v", a)
	}
}

// TestPublishNeverFailsTheRun: every failed write is an outcome, and the
// suggestions are returned either way.
func TestPublishNeverFailsTheRun(t *testing.T) {
	boom := &provider.Error{Class: provider.ClassUpstream, Status: 500}
	t.Run("overview post fails", func(t *testing.T) {
		h, p := newPubHarness(t, giteaCaps, publishAnswers())
		p.postErr = boom
		res := h.run(t, Args{Publish: true})
		if res.Publish.Published || res.Publish.Error != boom.Error() || len(p.batches) != 0 || len(res.Suggestions) != 3 {
			t.Errorf("publish = %+v batches=%d suggestions=%d", res.Publish, len(p.batches), len(res.Suggestions))
		}
		// The inline comments need the overview: no inline notes either.
		if len(res.Notes) != 0 {
			t.Errorf("notes = %q", res.Notes)
		}
	})
	t.Run("inline post fails", func(t *testing.T) {
		h, p := newPubHarness(t, giteaCaps, publishAnswers())
		p.inlineErr = boom
		res := h.run(t, Args{Publish: true})
		if !res.Publish.Published || res.Publish.Inline == nil || res.Publish.Inline.Failed != 2 {
			t.Errorf("publish = %+v inline=%+v", res.Publish, res.Publish.Inline)
		}
		if !slices.Contains(res.Notes, noteInlineFailed(2)) {
			t.Errorf("notes = %q", res.Notes)
		}
		if a := res.Suggestions[0].Anchor; a == nil || a.Status != AnchorFailed || a.Error != boom.Error() {
			t.Errorf("anchor = %+v", a)
		}
	})
	t.Run("lookup fails", func(t *testing.T) {
		h, p := newPubHarness(t, giteaCaps, publishAnswers())
		p.listErr = boom
		h.deps.Config.Review.MaxDiscussionTokens = 0 // the prompt does not read the threads
		res := h.run(t, Args{Publish: true})
		if !res.Publish.Published || len(p.batches) != 0 {
			t.Errorf("publish = %+v batches=%d", res.Publish, len(p.batches))
		}
		for _, n := range []string{NoteOverviewLookupFailed, NoteInlineNotChecked} {
			if !slices.Contains(res.Notes, n) {
				t.Errorf("notes %q lack %q", res.Notes, n)
			}
		}
		if in := res.Publish.Inline; in == nil || in.Failed != 2 {
			t.Errorf("inline = %+v", in)
		}
	})
	t.Run("edit fails", func(t *testing.T) {
		h, p := newPubHarness(t, giteaCaps, publishAnswers())
		h.run(t, Args{Publish: true})
		p.editErr = &provider.Error{Class: provider.ClassNotOwner}
		h.llm.seen = nil
		res := h.run(t, Args{Publish: true})
		if !res.Publish.Published || res.Publish.Updated || !slices.Contains(res.Notes, NoteOverviewReplaced) || len(p.posts) != 2 {
			t.Errorf("publish = %+v notes=%q posts=%d", res.Publish, res.Notes, len(p.posts))
		}
	})
	t.Run("edit and post fail", func(t *testing.T) {
		h, p := newPubHarness(t, giteaCaps, publishAnswers())
		h.run(t, Args{Publish: true})
		p.editErr, p.postErr = errors.New("x"), errors.New("y")
		h.llm.seen = nil
		res := h.run(t, Args{Publish: true})
		if res.Publish.Published || res.Publish.Error != publishFailedMessage || slices.Contains(res.Notes, NoteOverviewReplaced) {
			t.Errorf("publish = %+v notes=%q", res.Publish, res.Notes)
		}
	})
}

// TestPublishOffWritesNothing: publish=false never writes and sets no
// publish outcome.
func TestPublishOffWritesNothing(t *testing.T) {
	h, p := newPubHarness(t, giteaCaps, publishAnswers())
	res := h.run(t, Args{})
	if res.Publish != nil || len(p.posts)+len(p.edits)+len(p.batches) != 0 {
		t.Errorf("publish = %+v writes %d", res.Publish, len(p.posts)+len(p.edits)+len(p.batches))
	}
	for _, s := range res.Suggestions {
		if s.Anchor != nil {
			t.Errorf("anchor without publish: %+v", s.Anchor)
		}
	}
}

// TestPublishSanitizesQuickActions: with QuickActions every posted body, the
// overview and the inline comments, has a space in front of a line that
// starts with "/".
func TestPublishSanitizesQuickActions(t *testing.T) {
	caps := giteaCaps
	caps.QuickActions = true
	h, p := newPubHarness(t, caps, publishAnswers())
	h.deps.RenderInline = func(s *Suggestion, _ provider.Capabilities) string { return "**x**\n\n/approve\n/close" }
	h.deps.RenderOverview = func(*Result, provider.Capabilities, func(string, int) string) string { return "## O\n\n/merge\n" }
	h.run(t, Args{Publish: true})
	bodies := slices.Concat(p.posts, p.inlineBodies())
	for _, e := range p.edits {
		bodies = append(bodies, strings.SplitN(e, "\x00", 2)[1])
	}
	if len(bodies) < 4 {
		t.Fatalf("bodies = %q", bodies)
	}
	for _, b := range bodies {
		for _, l := range strings.Split(b, "\n") {
			if strings.HasPrefix(l, "/") {
				t.Errorf("a line starts with a slash in %q", b)
			}
		}
	}
	if !strings.Contains(strings.Join(bodies, "\n"), " /approve") {
		t.Errorf("the quick action was not neutralised: %q", bodies)
	}
}

func TestAnchorFor(t *testing.T) {
	two := "@@ -1,4 +1,5 @@\n a\n-b\n+B\n+B2\n c\n d\n@@ -20,3 +21,3 @@\n e\n-f\n+F\n g\n"
	adjacent := "@@ -1,2 +1,2 @@\n a\n-b\n+B\n@@ -3,2 +3,2 @@\n c\n-d\n+D\n"
	del := "@@ -5,3 +5,1 @@\n x\n-y\n-z\n"
	file := func(typ provider.ChangeType, p string) *provider.FilePatch {
		return &provider.FilePatch{Path: "n.go", OldPath: "o.go", Type: typ, Patch: p}
	}
	for name, tc := range map[string]struct {
		fp         *provider.FilePatch
		start, end int
		want       string
	}{
		"context line":                    {file(provider.ChangeModified, two), 1, 1, "n.go:1:context"},
		"added line":                      {file(provider.ChangeModified, two), 2, 2, "n.go:2:added"},
		"context to added":                {file(provider.ChangeModified, two), 1, 4, "n.go:1:context"},
		"across a removed line":           {file(provider.ChangeModified, two), 1, 3, "n.go:1:context"},
		"whole second hunk":               {file(provider.ChangeModified, two), 21, 23, "n.go:21:context"},
		"leaves the first hunk":           {file(provider.ChangeModified, two), 4, 6, ""},
		"gap between hunks":               {file(provider.ChangeModified, two), 5, 21, ""},
		"only in the gap":                 {file(provider.ChangeModified, two), 10, 11, ""},
		"before the first hunk":           {file(provider.ChangeModified, two), 0, 1, ""},
		"end before start":                {file(provider.ChangeModified, two), 3, 2, ""},
		"adjacent hunks not one":          {file(provider.ChangeModified, adjacent), 2, 3, ""},
		"removed-only position":           {file(provider.ChangeModified, del), 6, 6, ""},
		"line before a removal":           {file(provider.ChangeModified, del), 5, 5, "n.go:5:context"},
		"renamed keeps the old":           {file(provider.ChangeRenamed, two), 2, 2, "n.go:2:added"},
		"deleted file":                    {file(provider.ChangeDeleted, two), 1, 1, ""},
		"unparsable":                      {file(provider.ChangeModified, "not a patch"), 1, 1, ""},
		"binary":                          {&provider.FilePatch{Path: "n.go", Binary: true, Patch: two}, 1, 1, ""},
		"no file":                         {nil, 1, 1, ""},
		"no newline marker is not a line": {file(provider.ChangeModified, "@@ -1,1 +1,1 @@\n-a\n+b\n\\ No newline at end of file\n"), 1, 2, ""},
	} {
		c, ok := anchorFor(tc.fp, tc.start, tc.end)
		got := ""
		if ok {
			got = c.Path + ":" + strconv.Itoa(c.Line) + ":" + string(c.LineType)
		}
		if got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	if c, ok := anchorFor(file(provider.ChangeRenamed, two), 2, 2); !ok || c.OldPath != "o.go" {
		t.Errorf("a renamed file's old path = %+v", c)
	}
}

func TestSuggestionMarker(t *testing.T) {
	s := &Suggestion{File: "a.go", Summary: "Do it", ExistingCode: "x"}
	fp := SuggestionFingerprint(s)
	m := SuggestionMarker(fp)
	if got, ok := ParseSuggestionMarker("text\n\n" + m + "\r\n"); !ok || got != fp {
		t.Errorf("parse = %q %v", got, ok)
	}
	for _, bad := range []string{m + "\ntext", "[//]: # (review-mcp:finding:" + fp + ")", "[//]: # (review-mcp:suggestion:XYZ)",
		"[//]: # (review-mcp:suggestion:" + fp + "00)", "", "a [//]: # (review-mcp:suggestion:" + fp + ")"} {
		if _, ok := ParseSuggestionMarker(bad); ok {
			t.Errorf("%q parsed as a marker", bad)
		}
	}
}

// rewordedAnswers is publishAnswers with every summary reworded; the code is
// the same.
func rewordedAnswers() map[int][]string {
	d, tt, u := sugDelay, sugTest, sugUnverified
	d.summary, tt.summary, u.summary = "Bound the retry delay", "Make the test able to fail", "Cover the missing case"
	return map[int][]string{
		0: {suggestionsAnswer(d, tt, u)},
		reflectKind(0): {reflectionAnswer(
			fb{number: 1, summary: d.summary, file: "src/app.go", start: 12, end: 12, score: 8, why: "An unbounded `delay` can stall callers."},
			fb{number: 2, summary: tt.summary, file: "src/app_test.go", start: 2, end: 2, score: 9, why: "The test cannot fail."},
			fb{number: 3, summary: u.summary, file: "src/app.go", start: 12, end: 12, score: 8, why: "A case is missing."},
		)},
	}
}

// TestPublishRewordedSummaryIsDuplicate: the already-posted key leaves the
// summary out, so a rerun whose model reworded every summary (same code)
// posts nothing again.
func TestPublishRewordedSummaryIsDuplicate(t *testing.T) {
	h, p := newPubHarness(t, giteaCaps, publishAnswers())
	h.run(t, Args{Publish: true})
	batches := len(p.batches)

	h.llm.seen = nil
	h.llm.answers = rewordedAnswers()
	second := h.run(t, Args{Publish: true})

	if len(p.batches) != batches {
		t.Errorf("inline comments were posted again: %+v", p.batches[batches:])
	}
	if in := second.Publish.Inline; in == nil || *in != (InlineSummary{SkippedDuplicate: 2}) {
		t.Errorf("inline = %+v, want 2 skipped duplicates", in)
	}
	want := map[string]string{"Bound the retry delay": AnchorSkippedDuplicate,
		"Make the test able to fail": AnchorSkippedDuplicate, "Cover the missing case": "<nil>"}
	if got := anchorStatuses(second); !mapEqual(got, want) {
		t.Errorf("anchors = %v, want %v", got, want)
	}
}

// TestPublishLegacyKeyIsDuplicate: a comment whose marker carries the key of
// the previous build (X-13 fingerprint with the summary) is still recognised.
func TestPublishLegacyKeyIsDuplicate(t *testing.T) {
	h, p := newPubHarness(t, giteaCaps, publishAnswers())
	first := h.run(t, Args{Publish: true})
	// Rewrite the stored inline markers to the legacy keys.
	for i := range p.stored {
		if !p.stored[i].inline {
			continue
		}
		var legacy string
		for j := range first.Suggestions {
			s := &first.Suggestions[j]
			if strings.HasSuffix(p.stored[i].body, SuggestionMarker(SuggestionFingerprint(s))) {
				legacy = legacySuggestionFingerprint(s)
			}
		}
		if legacy == "" {
			t.Fatalf("no suggestion for stored comment %q", p.stored[i].body)
		}
		b := p.stored[i].body
		p.stored[i].body = b[:strings.LastIndex(b, suggestionMarkerPrefix)] + SuggestionMarker(legacy)
	}
	batches := len(p.batches)
	h.llm.seen = nil
	second := h.run(t, Args{Publish: true})
	if len(p.batches) != batches || second.Publish.Inline == nil || *second.Publish.Inline != (InlineSummary{SkippedDuplicate: 2}) {
		t.Errorf("legacy markers not recognised: inline = %+v", second.Publish.Inline)
	}
}

// TestPublishDifferentImprovedCodeIsNew: the same existing code with another
// replacement is a new suggestion.
func TestPublishDifferentImprovedCodeIsNew(t *testing.T) {
	h, p := newPubHarness(t, giteaCaps, publishAnswers())
	h.run(t, Args{Publish: true})
	batches := len(p.batches)

	d := sugDelay
	d.improved = "delay := min(retries*50, 500)"
	tt := sugTest
	h.llm.seen = nil
	h.llm.answers = map[int][]string{
		0: {suggestionsAnswer(d, tt)},
		reflectKind(0): {reflectionAnswer(
			fb{number: 1, summary: d.summary, file: "src/app.go", start: 12, end: 12, score: 8, why: "x"},
			fb{number: 2, summary: tt.summary, file: "src/app_test.go", start: 2, end: 2, score: 9, why: "y"},
		)},
	}
	second := h.run(t, Args{Publish: true})
	if len(p.batches) != batches+1 || len(p.batches[batches]) != 1 || p.batches[batches][0].Path != "src/app.go" {
		t.Fatalf("batches = %+v", p.batches[batches:])
	}
	if in := second.Publish.Inline; in == nil || *in != (InlineSummary{Posted: 1, SkippedDuplicate: 1}) {
		t.Errorf("inline = %+v", in)
	}
}

// TestSuggestionFingerprintKey: the key ignores the summary and the white
// space and indentation of the code, and nothing else.
func TestSuggestionFingerprintKey(t *testing.T) {
	base := Suggestion{File: "a.go", Summary: "one", ExistingCode: "if x {\n\ty()\n}", ImprovedCode: "if x {\n\tz()\n}"}
	key := SuggestionFingerprint(&base)
	same := map[string]Suggestion{
		"summary":     {File: "a.go", Summary: "other", ExistingCode: base.ExistingCode, ImprovedCode: base.ImprovedCode},
		"indentation": {File: "a.go", ExistingCode: "    if x {\n    \ty()\n    }", ImprovedCode: "\t\tif x {\n\t\t\tz()\n\t\t}"},
		"trailing":    {File: "a.go", ExistingCode: "if x {  \r\n\ty()\t\r\n}\r\n", ImprovedCode: "\nif x {\n\tz()\n}\n\n"},
	}
	for name, s := range same {
		if got := SuggestionFingerprint(&s); got != key {
			t.Errorf("%s changes the key", name)
		}
	}
	differ := map[string]Suggestion{
		"file":     {File: "b.go", ExistingCode: base.ExistingCode, ImprovedCode: base.ImprovedCode},
		"existing": {File: "a.go", ExistingCode: "if x {\n\tq()\n}", ImprovedCode: base.ImprovedCode},
		"improved": {File: "a.go", ExistingCode: base.ExistingCode, ImprovedCode: "if x {\n\tq()\n}"},
		"swapped":  {File: "a.go", ExistingCode: base.ImprovedCode, ImprovedCode: base.ExistingCode},
		"relative": {File: "a.go", ExistingCode: "if x {\n\ty()\n\t}", ImprovedCode: base.ImprovedCode},
		"boundary": {File: "a.go", ExistingCode: base.ExistingCode + base.ImprovedCode[:1], ImprovedCode: base.ImprovedCode[1:]},
	}
	for name, s := range differ {
		if SuggestionFingerprint(&s) == key {
			t.Errorf("%s does not change the key", name)
		}
	}
}
