package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Markers that must reach the model but never the logs (X-8).
const (
	titleMarker  = "TITLE-MARKER-7f3a"
	descMarker   = "DESC-MARKER-91c2"
	diffMarker   = "DIFF-MARKER-55e0"
	answerMarker = "ANSWER-MARKER-c4d1"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }

// fakeProvider serves one PR. GetDiff applies the Include filter like the
// real providers.
type fakeProvider struct {
	provider.Provider // nil: unexpected methods panic

	pr      provider.PullRequest
	files   []provider.FilePatch
	postErr error
	// quickActions makes Capabilities report QuickActions.
	quickActions bool

	calls    int
	posted   []string
	postedTo []provider.PRRef

	// seq records the write calls in order: "post", "inline", "edit".
	seq []string
	// inline holds the items of each PostInlineComments call; inlineErr
	// fails the call and inlineResult, when set, decides each item.
	inline       [][]provider.InlineComment
	inlineErr    error
	inlineResult func(i int, it provider.InlineComment) provider.InlineResult
	// edits holds each EditComment call; editErr fails it.
	edits   []edit
	editErr error

	// comments are the PR's general comments: those PostComment added and
	// any a test plants. ListThreads lists them (listErr fails it, lists
	// counts the calls); EditComment edits them after the ownership check
	// the real providers make. me is CurrentUser (meErr fails it).
	comments []fakeComment
	listErr  error
	lists    int
	me       provider.User
	meErr    error
	// threads are further threads a test plants (inline ones, replies,
	// resolved ones); ListThreads lists them after the general comments, and
	// then the inline comments PostInlineComments recorded as the token's own
	// (inlineThreads), like a real server does.
	threads       []provider.Thread
	inlineThreads []provider.Thread
	// skipped are files the provider reports as skipped (binary, too large).
	skipped []provider.SkippedFile
}

type edit struct{ id, body string }

// fakeComment is a general comment of the fake PR.
type fakeComment struct {
	id, body string
	author   provider.User
	created  time.Time
}

func (f *fakeProvider) addComment(author provider.User, body string) string {
	id := strconv.Itoa(42 + len(f.comments))
	f.comments = append(f.comments, fakeComment{id: id, body: body, author: author,
		created: time.Date(2026, 10, 1, 0, len(f.comments), 0, 0, time.UTC)})
	return id
}

func (f *fakeProvider) ListThreads(context.Context, provider.PRRef) ([]provider.Thread, error) {
	f.calls++
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := []provider.Thread{}
	for _, c := range f.comments {
		out = append(out, provider.Thread{ID: c.id, Kind: provider.ThreadGeneral, Comments: []provider.CommentItem{{
			ID: c.id, Author: c.author.Name, Body: c.body, CreatedAt: c.created, UpdatedAt: c.created,
			AuthorID: c.author.ID, AuthorLogin: c.author.Name, URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-" + c.id,
		}}})
	}
	out = append(out, f.threads...)
	return append(out, f.inlineThreads...), nil
}

func (f *fakeProvider) CurrentUser(context.Context) (provider.User, error) {
	f.calls++
	return f.me, f.meErr
}

func (f *fakeProvider) PostInlineComments(_ context.Context, _ provider.PRRef, pr *provider.PullRequest, items []provider.InlineComment) ([]provider.InlineResult, error) {
	f.calls++
	f.seq = append(f.seq, "inline")
	f.inline = append(f.inline, items)
	if pr == nil || pr.HeadSHA == "" {
		return nil, &provider.Error{Class: provider.ClassProtocol, Hint: "no head"}
	}
	if err := provider.ValidateInlineComments(items); err != nil {
		return nil, err
	}
	if f.inlineErr != nil {
		return nil, f.inlineErr
	}
	out := make([]provider.InlineResult, len(items))
	for i, it := range items {
		if f.inlineResult != nil {
			out[i] = f.inlineResult(i, it)
			continue
		}
		id := strconv.Itoa(900 + len(f.inlineThreads))
		f.inlineThreads = append(f.inlineThreads, provider.Thread{ID: id, Kind: provider.ThreadInline, Path: it.Path,
			Line: it.Line, Resolved: new(bool), Comments: []provider.CommentItem{{ID: id, Author: f.me.Name, Body: it.Body,
				CreatedAt: time.Date(2026, 10, 2, 0, len(f.inlineThreads), 0, 0, time.UTC),
				AuthorID:  f.me.ID, AuthorLogin: f.me.Name}}})
		out[i] = provider.InlineResult{Posted: true, ID: id, URL: "https://your-gitea.example/octo/demo/pulls/7/files#issuecomment-" + id}
	}
	return out, nil
}

func (f *fakeProvider) EditComment(_ context.Context, _ provider.PRRef, id, body string) error {
	f.calls++
	f.seq = append(f.seq, "edit")
	f.edits = append(f.edits, edit{id, body})
	if f.editErr != nil {
		return f.editErr
	}
	for i := range f.comments {
		c := &f.comments[i]
		if c.id != id {
			continue
		}
		if !provider.IsUser(f.me, c.author.ID, c.author.Name) {
			return &provider.Error{Class: provider.ClassNotOwner}
		}
		c.body = body
		return nil
	}
	return &provider.Error{Class: provider.ClassNotFound, Status: 404}
}

func (f *fakeProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{GFM: true, QuickActions: f.quickActions}
}

func (f *fakeProvider) GetPullRequest(context.Context, provider.PRRef) (*provider.PullRequest, error) {
	f.calls++
	pr := f.pr
	return &pr, nil
}

func (f *fakeProvider) GetDiff(_ context.Context, _ provider.PRRef, _ *provider.PullRequest, opts provider.DiffOptions) (*provider.Diff, error) {
	f.calls++
	d := &provider.Diff{Skipped: slices.Clone(f.skipped)}
	for _, fp := range f.files {
		if opts.Include != nil && !opts.Include(fp.Path) {
			d.Skipped = append(d.Skipped, provider.SkippedFile{Path: fp.Path, Reason: provider.SkipFiltered})
			continue
		}
		d.Files = append(d.Files, fp)
	}
	return d, nil
}

func (f *fakeProvider) PostComment(_ context.Context, ref provider.PRRef, body string) (*provider.Comment, error) {
	f.calls++
	f.seq = append(f.seq, "post")
	f.posted = append(f.posted, body)
	f.postedTo = append(f.postedTo, ref)
	if f.postErr != nil {
		return nil, f.postErr
	}
	id := f.addComment(f.me, body)
	return &provider.Comment{ID: id, URL: "https://your-gitea.example/octo/demo/pulls/7#issuecomment-" + id}, nil
}

func (f *fakeProvider) FileLineURL(_ provider.PRRef, _ *provider.PullRequest, path string, line int) string {
	return "https://your-gitea.example/octo/demo/src/commit/abc/" + path + "#L" + strconv.Itoa(line)
}

type fakeResolver struct {
	p     *fakeProvider
	calls int
}

func (r *fakeResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	r.calls++
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

type llmCall struct{ system, user string }

// fakeLLM answers from a script; each call takes the next answer.
type fakeLLM struct {
	answers   []string
	truncated []bool
	err       error
	calls     []llmCall
}

func (f *fakeLLM) Complete(_ context.Context, system, user string) (*llm.Response, error) {
	f.calls = append(f.calls, llmCall{system, user})
	if f.err != nil {
		return nil, f.err
	}
	i := len(f.calls) - 1
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	r := &llm.Response{Content: f.answers[i], Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 20}}
	if i < len(f.truncated) {
		r.Truncated = f.truncated[i]
	}
	return r, nil
}

const testPRURL = "https://your-gitea.example/octo/demo/pulls/7"

func numberedLines(from, to int, mod func(n int) string) string {
	var b strings.Builder
	for n := from; n <= to; n++ {
		if mod != nil {
			if s := mod(n); s != "" {
				b.WriteString(s + "\n")
				continue
			}
		}
		fmt.Fprintf(&b, "line %d\n", n)
	}
	return b.String()
}

// sampleFiles: app.go has full head content (snippets from the head),
// util.go has none (snippets from the patch walk), vendor/lib.go is
// filtered by the default ignore glob.
func sampleFiles() []provider.FilePatch {
	head := numberedLines(1, 20, func(n int) string {
		if n == 11 {
			return "line 11 " + diffMarker
		}
		return ""
	})
	base := numberedLines(1, 20, nil)
	base = strings.Replace(base, "line 11\n", "", 1)
	return []provider.FilePatch{
		{
			Path: "src/app.go", Type: provider.ChangeModified, Additions: 1,
			Patch:       "@@ -10,3 +10,4 @@\n line 10\n+line 11 " + diffMarker + "\n line 12\n line 13\n",
			BaseContent: &base, HeadContent: &head, BaseStatus: provider.ContentFull, HeadStatus: provider.ContentFull,
		},
		{
			Path: "src/util.go", Type: provider.ChangeModified, Additions: 1,
			Patch:      "@@ -1,2 +1,3 @@\n a\n+b\n c\n",
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap,
		},
		{
			Path: "vendor/lib.go", Type: provider.ChangeAdded, Additions: 1,
			Patch:      "@@ -0,0 +1 @@\n+x\n",
			BaseStatus: provider.ContentNotApplicable, HeadStatus: provider.ContentNotFetchedSizeCap,
		},
	}
}

const goodAnswer = "```yaml\nreview:\n" +
	"  estimated_effort_to_review: 2\n" +
	"  relevant_tests: |\n    No\n" +
	"  key_issues_to_review:\n" +
	"    - relevant_file: |\n        src/app.go\n" +
	"      issue_header: |\n        Possible Bug\n" +
	"      issue_content: |\n        Line 11 is wrong " + answerMarker + ".\n" +
	"      start_line: 10\n      end_line: 12\n" +
	"    - relevant_file: src/util.go\n      issue_header: Style\n      issue_content: The range is partly outside the diff.\n" +
	"      start_line: 2\n      end_line: 5\n" +
	"  security_concerns: |\n    No\n" +
	"  performance_concerns: |\n    No\n```\n"

func testConfig() *config.Config {
	cfg := config.Defaults()
	cfg.LLM.BaseURL = "https://llm.example.com/v1"
	cfg.LLM.Model = "test-model"
	cfg.LLM.ContextWindow = 32000
	cfg.Gitea.BaseURL = "https://your-gitea.example"
	return cfg
}

type harness struct {
	deps     Deps
	prov     *fakeProvider
	resolver *fakeResolver
	llm      *fakeLLM
	logs     *bytes.Buffer
	rendered []*Result
	// stages are the progress stages of the last runChunked.
	stages []string
}

func newHarness(answers ...string) *harness {
	h := &harness{logs: &bytes.Buffer{}}
	h.prov = &fakeProvider{
		pr: provider.PullRequest{Title: "Retry " + titleMarker, Description: "Adds a retry. " + descMarker,
			SourceBranch: "feature/retry", HeadSHA: "abc"},
		files: sampleFiles(),
		me:    provider.User{ID: "5", Name: "review-bot"},
	}
	h.resolver = &fakeResolver{p: h.prov}
	h.llm = &fakeLLM{answers: answers}
	h.deps = Deps{
		Config:   testConfig(),
		Logger:   slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Resolver: h.resolver,
		LLM:      h.llm,
		Clock:    fixedClock{},
		RenderProvider: func(r *Result, caps provider.Capabilities) string {
			h.rendered = append(h.rendered, r)
			return fmt.Sprintf("rendered %d findings gfm=%v", len(r.Review.KeyIssuesToReview), caps.GFM)
		},
		RenderInline: func(ki *KeyIssue, caps provider.Capabilities) string {
			return fmt.Sprintf("inline %s gfm=%v\n", ki.IssueHeader, caps.GFM)
		},
	}
	return h
}

func (h *harness) checkNoLeaks(t *testing.T, errs ...error) {
	t.Helper()
	for _, m := range []string{titleMarker, descMarker, diffMarker, answerMarker} {
		if strings.Contains(h.logs.String(), m) {
			t.Errorf("marker %s leaked into the logs", m)
		}
		for _, err := range errs {
			if err != nil && strings.Contains(err.Error(), m) {
				t.Errorf("marker %s leaked into an error", m)
			}
		}
	}
}

func TestRunReview(t *testing.T) {
	h := newHarness(goodAnswer)
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.llm.calls) != 1 {
		t.Fatalf("LLM calls = %d", len(h.llm.calls))
	}
	call := h.llm.calls[0]
	for _, m := range []string{titleMarker, descMarker, diffMarker} {
		if !strings.Contains(call.user, m) {
			t.Errorf("marker %s did not reach the model", m)
		}
	}
	if !strings.Contains(call.user, "Today's Date: 2026-10-06") || !strings.Contains(call.user, "Branch: 'feature/retry'") {
		t.Errorf("PR info missing from the user prompt")
	}
	if !strings.HasSuffix(call.user, ResponseLine+"\n```yaml") || strings.Contains(call.user, ReaskNote) {
		t.Errorf("unexpected user prompt tail")
	}
	if !strings.Contains(call.system, "(0-3 issues)") {
		t.Errorf("max findings not in the system prompt")
	}

	r := res.Review
	if r.EstimatedEffortToReview == nil || *r.EstimatedEffortToReview != 2 || r.RelevantTests == nil || *r.RelevantTests ||
		r.HasSecurityConcerns() || r.PerformanceConcerns == nil || r.HasPerformanceConcerns() || len(r.KeyIssuesToReview) != 2 {
		t.Fatalf("review = %+v", r)
	}
	app, util := r.KeyIssuesToReview[0], r.KeyIssuesToReview[1]
	if app.Snippet != "line 10\nline 11 "+diffMarker+"\nline 12" || app.SnippetNote != "" {
		t.Errorf("app snippet = %q (%q)", app.Snippet, app.SnippetNote)
	}
	if app.Link != "https://your-gitea.example/octo/demo/src/commit/abc/src/app.go#L10" {
		t.Errorf("link = %q", app.Link)
	}
	if util.Snippet != "" || util.SnippetNote != SnippetNoteUnverified || util.Link == "" {
		t.Errorf("util finding = %+v", util)
	}
	if !slices.Equal(res.EnabledFields, []string{KeyEffort, KeyRelevantTests, KeyKeyIssues, KeySecurityConcerns, KeyPerformanceConcerns}) {
		t.Errorf("enabled fields = %v", res.EnabledFields)
	}
	c := res.Coverage
	if !slices.Equal(c.Included, []string{"src/app.go", "src/util.go"}) || len(c.Filtered) != 1 ||
		c.Filtered[0].Path != "vendor/lib.go" || c.Filtered[0].Reason == provider.SkipFiltered ||
		c.ModelCalls != 1 || c.FailedParts != 0 {
		t.Errorf("coverage = %+v", c)
	}
	m := res.Metadata
	if m.Model != "test-model" || m.LLMCalls != 1 || m.Reasked || m.RepairTactic != "direct" || !m.FastPath ||
		m.PromptTokens <= 0 || m.DiffTokens <= 0 || m.RequestTokens <= m.PromptTokens || m.DiffTrimmed {
		t.Errorf("metadata = %+v", m)
	}
	if len(res.Notes) != 0 || res.Publish != nil {
		t.Errorf("notes %v publish %+v", res.Notes, res.Publish)
	}
	if res.PR.Number != 7 || res.PR.Kind != "gitea" || res.PR.URL != testPRURL {
		t.Errorf("pr = %+v", res.PR)
	}
	if !strings.Contains(h.logs.String(), "repair_tactic=direct") {
		t.Errorf("tactic not logged: %s", h.logs.String())
	}
	h.checkNoLeaks(t)
}

func TestRunOverrides(t *testing.T) {
	h := newHarness(goodAnswer)
	h.deps.Config.Review.RequireTests = false
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, MaxFindings: 1,
		ExtraInstructions: "Look at retries.", OutputLanguage: "tr-TR"})
	if err != nil {
		t.Fatal(err)
	}
	sys := h.llm.calls[0].system
	if !strings.Contains(sys, "(0-1 issues)") || !strings.Contains(sys, "Look at retries.\n======\n\nIn addition, ") ||
		!strings.Contains(sys, "locale code: 'tr-TR'") || strings.Contains(sys, KeyRelevantTests) {
		t.Errorf("overrides not in the system prompt")
	}
	if res.Review.RelevantTests != nil || len(res.Review.KeyIssuesToReview) != 1 {
		t.Errorf("review = %+v", res.Review)
	}
	if !slices.Contains(res.Notes, "1 finding beyond the limit of 1 (max_findings) was dropped.") {
		t.Errorf("notes = %v", res.Notes)
	}
}

func TestRunConfigInvalidSendsNothing(t *testing.T) {
	for name, mutate := range map[string]func(*Deps){
		"load error": func(d *Deps) { d.ConfigErr = errors.New("invalid configuration: x") },
		"nil config": func(d *Deps) { d.Config = nil },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(goodAnswer)
			mutate(&h.deps)
			_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
			if !errors.Is(err, ErrConfigInvalid) {
				t.Fatalf("err = %v", err)
			}
			if h.resolver.calls+h.prov.calls+len(h.llm.calls) != 0 {
				t.Errorf("calls: resolver %d provider %d llm %d", h.resolver.calls, h.prov.calls, len(h.llm.calls))
			}
		})
	}
}

// TestRunReaskOnce: an unparseable first answer is re-asked exactly once,
// with the same system prompt and the re-ask note on its own line right
// before the response line.
func TestRunReaskOnce(t *testing.T) {
	h := newHarness("I could not review this pull request "+answerMarker+".", goodAnswer)
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.llm.calls) != 2 {
		t.Fatalf("LLM calls = %d, want 2", len(h.llm.calls))
	}
	first, second := h.llm.calls[0], h.llm.calls[1]
	if first.system != second.system {
		t.Errorf("the re-ask changed the system prompt")
	}
	if strings.Contains(first.user, ReaskNote) {
		t.Errorf("the first request carries the re-ask note")
	}
	want := "\n" + ReaskNote + "\n" + ResponseLine + "\n```yaml"
	if !strings.HasSuffix(second.user, want) {
		t.Errorf("re-ask tail = %q", second.user[max(0, len(second.user)-200):])
	}
	if strings.Replace(second.user, ReaskNote+"\n", "", 1) != first.user {
		t.Errorf("the re-ask changed more than the note")
	}
	if strings.Count(second.user, ReaskNote) != 1 {
		t.Errorf("note count = %d", strings.Count(second.user, ReaskNote))
	}
	if !res.Metadata.Reasked || res.Metadata.LLMCalls != 2 || !slices.Contains(res.Notes, NoteReasked) {
		t.Errorf("metadata %+v notes %v", res.Metadata, res.Notes)
	}
	h.checkNoLeaks(t)
}

func TestRunUnparseableTwice(t *testing.T) {
	h := newHarness("not yaml: [", "review: just text")
	_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if !errors.Is(err, ErrUnparseable) || !errors.Is(err, ErrFallbackEligible) {
		t.Fatalf("err = %v", err)
	}
	if len(h.llm.calls) != 2 {
		t.Errorf("LLM calls = %d, want exactly 2", len(h.llm.calls))
	}
	var re *Error
	if !errors.As(err, &re) || re.UserMessage() != sentences[ClassUnparseable] {
		t.Errorf("not the fixed sentence: %v", err)
	}
	if len(h.prov.posted) != 0 {
		t.Errorf("a failed review was published")
	}
	h.checkNoLeaks(t, err)
}

func TestRunLLMErrorIsReturnedAsIs(t *testing.T) {
	h := newHarness(goodAnswer)
	h.llm.err = &llm.Error{Class: llm.ClassAuth, Status: 401}
	_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if !errors.Is(err, llm.ErrAuth) || len(h.llm.calls) != 1 {
		t.Errorf("err = %v, calls %d", err, len(h.llm.calls))
	}
}

func TestRunEmptyPreparedDiffMakesNoLLMCall(t *testing.T) {
	h := newHarness(goodAnswer)
	h.deps.Config.Ignore.Glob = []string{"**"}
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.llm.calls) != 0 {
		t.Fatalf("LLM calls = %d, want 0", len(h.llm.calls))
	}
	if !slices.Equal(res.Notes, []string{NoteNoReviewableChanges}) || len(res.Review.KeyIssuesToReview) != 0 ||
		res.Review.KeyIssuesToReview == nil {
		t.Errorf("result = %+v", res)
	}
	if len(res.Coverage.Filtered) != 3 || len(res.Coverage.Included) != 0 || res.Metadata.LLMCalls != 0 || res.Coverage.ModelCalls != 0 {
		t.Errorf("coverage = %+v", res.Coverage)
	}
	if res.Publish == nil || !res.Publish.Published {
		t.Errorf("publish = %+v", res.Publish)
	}
}

func TestRunDoesNotFit(t *testing.T) {
	t.Run("no room for the diff", func(t *testing.T) {
		h := newHarness(goodAnswer)
		h.deps.Config.LLM.ContextWindow = 3000
		_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
		if !errors.Is(err, ErrDoesNotFit) || !errors.Is(err, ErrFallbackEligible) || len(h.llm.calls) != 0 {
			t.Errorf("err = %v, calls %d", err, len(h.llm.calls))
		}
	})
	t.Run("no file fits under skip", func(t *testing.T) {
		h := newHarness(goodAnswer)
		big := "@@ -1 +1,4000 @@\n-x\n" + strings.Repeat("+some fairly long added line of code\n", 4000)
		h.prov.files = []provider.FilePatch{{Path: "big.go", Type: provider.ChangeModified, Patch: big,
			HeadStatus: provider.ContentNotFetchedSizeCap}}
		h.deps.Config.Diff.LargePatchPolicy = "skip"
		h.deps.Config.LLM.ContextWindow = 8000
		_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
		if !errors.Is(err, ErrDoesNotFit) || len(h.llm.calls) != 0 {
			t.Errorf("err = %v, calls %d", err, len(h.llm.calls))
		}
		if err != nil && err.Error() != sentences[ClassDoesNotFit] {
			t.Errorf("not the fixed sentence: %q", err.Error())
		}
	})
}

// findingsAnswer is a valid answer whose findings name the given files.
func findingsAnswer(files ...string) string {
	var b strings.Builder
	b.WriteString("```yaml\nreview:\n  key_issues_to_review:\n")
	for _, f := range files {
		fmt.Fprintf(&b, "    - relevant_file: %s\n      issue_header: Issue\n      issue_content: Something is wrong.\n"+
			"      start_line: 10\n      end_line: 10\n", f)
	}
	b.WriteString("```\n")
	return b.String()
}

// TestRunLinksOnlyFilesOfThePR: a finding whose relevant_file is not a
// reviewable file of the PR (a path escaping with "..", or one that does not
// exist) gets no link; a matched finding in the same answer keeps its link
// (architect review C1).
func TestRunLinksOnlyFilesOfThePR(t *testing.T) {
	h := newHarness(findingsAnswer("src/app.go", "../../evil/x.go", "not/in/pr.go"))
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	kis := res.Review.KeyIssuesToReview
	if len(kis) != 3 {
		t.Fatalf("findings = %d, want 3", len(kis))
	}
	if kis[0].Link == "" {
		t.Errorf("the finding on a PR file lost its link")
	}
	for _, ki := range kis[1:] {
		if ki.Link != "" {
			t.Errorf("finding on %q has link %q, want none", ki.RelevantFile, ki.Link)
		}
		if ki.RelevantFile == "" || ki.StartLine != 10 {
			t.Errorf("finding lost its file or line: %+v", ki)
		}
	}
}

func TestRunTruncatedNote(t *testing.T) {
	h := newHarness(goodAnswer)
	h.llm.truncated = []bool{true}
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Metadata.Truncated || !slices.Contains(res.Notes, NoteTruncated) {
		t.Errorf("metadata %+v notes %v", res.Metadata, res.Notes)
	}
}

// TestRunTruncationDescribesTheConvertedAnswer: the truncation flag and note
// follow the answer that was converted, not any earlier attempt (architect
// review C2).
func TestRunTruncationDescribesTheConvertedAnswer(t *testing.T) {
	cases := []struct {
		name      string
		truncated []bool
		want      bool
	}{
		{"cut-off first answer, complete re-ask", []bool{true, false}, false},
		{"complete first answer, cut-off re-ask", []bool{false, true}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A cut-off answer that no repair tactic rescues.
			h := newHarness("not yaml: [", goodAnswer)
			h.llm.truncated = c.truncated
			res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Metadata.Reasked || !slices.Contains(res.Notes, NoteReasked) {
				t.Fatalf("no re-ask: metadata %+v notes %v", res.Metadata, res.Notes)
			}
			if res.Metadata.Truncated != c.want || slices.Contains(res.Notes, NoteTruncated) != c.want {
				t.Errorf("truncated = %v, note present = %v, want %v", res.Metadata.Truncated,
					slices.Contains(res.Notes, NoteTruncated), c.want)
			}
		})
	}
}

func TestRunPublish(t *testing.T) {
	h := newHarness(goodAnswer)
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Publish == nil || !res.Publish.Published || res.Publish.CommentID != "42" || res.Publish.Error != "" {
		t.Errorf("publish = %+v", res.Publish)
	}
	if len(h.prov.posted) != 1 || h.prov.posted[0] != "rendered 2 findings gfm=true\n\n"+OverviewMarker || len(h.rendered) != 2 ||
		h.rendered[0] != res || h.rendered[1] != res {
		t.Errorf("posted %q", h.prov.posted)
	}
	if !slices.Equal(h.prov.seq, []string{"post", "inline", "edit"}) || len(h.prov.edits) != 1 || h.prov.edits[0].id != "42" {
		t.Errorf("write calls %v, edits %+v", h.prov.seq, h.prov.edits)
	}
	if in := res.Publish.Inline; in == nil || *in != (InlineSummary{Posted: 2}) {
		t.Errorf("inline = %+v", res.Publish.Inline)
	}
}

// TestRunPublishFailureKeepsReview: a failed publish is reported next to
// the review and never discards it.
func TestRunPublishFailureKeepsReview(t *testing.T) {
	for name, perr := range map[string]error{
		"provider error": &provider.Error{Class: provider.ClassAuth, Status: 403, Hint: "REVIEW_MCP_GITEA_TOKEN"},
		"other error":    errors.New("dial tcp: https://your-gitea.example/?token=FAKE-SECRET refused"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(goodAnswer)
			h.prov.postErr = perr
			res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
			if err != nil {
				t.Fatalf("a publish failure failed the review: %v", err)
			}
			if res.Review == nil || len(res.Review.KeyIssuesToReview) != 2 {
				t.Fatalf("review discarded: %+v", res.Review)
			}
			if res.Publish == nil || res.Publish.Published || res.Publish.Error == "" {
				t.Fatalf("publish = %+v", res.Publish)
			}
			var pe *provider.Error
			if errors.As(perr, &pe) && res.Publish.Error != pe.Error() {
				t.Errorf("publish error = %q", res.Publish.Error)
			}
			if strings.Contains(res.Publish.Error, "FAKE-SECRET") || strings.Contains(h.logs.String(), "FAKE-SECRET") {
				t.Errorf("raw error text leaked")
			}
		})
	}
	h := newHarness(goodAnswer)
	h.deps.RenderProvider = nil
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true})
	if err != nil {
		t.Fatalf("without a renderer: %v", err)
	}
	if res.Publish == nil || res.Publish.Published || len(h.prov.posted) != 0 {
		t.Errorf("without a renderer: publish %+v", res.Publish)
	}
}

func TestErrorClasses(t *testing.T) {
	for _, e := range []*Error{ErrConfigInvalid, ErrDoesNotFit, ErrUnparseable} {
		if e.UserMessage() == "" || e.UserMessage() != e.Error() {
			t.Errorf("%s: no fixed sentence", e.Class)
		}
		wrapped := fmt.Errorf("x: %w", &Error{Class: e.Class})
		if !errors.Is(wrapped, e) {
			t.Errorf("%s: errors.Is failed", e.Class)
		}
	}
	if errors.Is(ErrConfigInvalid, ErrFallbackEligible) || !errors.Is(ErrDoesNotFit, ErrFallbackEligible) ||
		!errors.Is(ErrUnparseable, ErrFallbackEligible) || errors.Is(ErrDoesNotFit, ErrUnparseable) {
		t.Errorf("fallback eligibility wrong")
	}
	if !errors.Is(doesNotFit(errSentinelForTest), errSentinelForTest) {
		t.Errorf("cause not unwrapped")
	}
}

var errSentinelForTest = errors.New("cause")
