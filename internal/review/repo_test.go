package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// snippetSentinel must reach the model but never the logs (X-8, WP-11c).
const snippetSentinel = "SNIPPET-SENTINEL-4c9e1b"

// fakeRepo is the git backend of repository context (repoctx.Backend): it
// serves hits by symbol name and records what it was asked.
type fakeRepo struct {
	ensureErr error
	ensures   int
	queries   []gitctx.Query
	hits      map[string][]gitctx.Hit
}

func (f *fakeRepo) Ensure(context.Context, gitctx.Repo, gitctx.PR) (gitctx.Checkout, error) {
	f.ensures++
	return gitctx.Checkout{GitDir: "/cache/x.git", HeadSHA: strings.Repeat("a", 40)}, f.ensureErr
}

func (f *fakeRepo) Grep(_ context.Context, _ gitctx.Checkout, q gitctx.Query) (gitctx.Result, error) {
	f.queries = append(f.queries, q)
	res := gitctx.Result{Symbols: len(q.Symbols)}
	files := map[string]bool{}
	for _, s := range q.Symbols {
		for _, h := range f.hits[s.Name] {
			res.Hits = append(res.Hits, h)
			files[h.Path] = true
		}
	}
	res.Files = len(files)
	return res, nil
}

// symbolsOf returns the names of the symbols of query i.
func (f *fakeRepo) symbolsOf(i int) []string {
	var out []string
	for _, s := range f.queries[i].Symbols {
		out = append(out, s.Name)
	}
	return out
}

// defFiles are two modified files that each define a symbol.
func defFiles() []provider.FilePatch {
	mk := func(path, def string) provider.FilePatch {
		return provider.FilePatch{Path: path, Type: provider.ChangeModified,
			Patch:      "@@ -1,2 +1,3 @@\n a\n+" + def + "\n b\n",
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap}
	}
	return []provider.FilePatch{mk("pkg/a.go", "func Alpha() {}"), mk("pkg/b.go", "func Beta() {}")}
}

func useHit(symbol, path string, line int) gitctx.Hit {
	return gitctx.Hit{Symbol: symbol, Path: path, Line: line, StartLine: line - 1,
		Snippet: "before\n" + symbol + "() // " + snippetSentinel + "\nafter"}
}

// repoHarness is a harness with repository context on and a fake backend.
func repoHarness(files []provider.FilePatch, hits map[string][]gitctx.Hit) (*harness, *fakeRepo) {
	h := newHarness(goodAnswer)
	h.prov.files = files
	h.deps.Config.Context.Repo.Enabled = true
	fb := &fakeRepo{hits: hits}
	h.deps.RepoContext = fb
	return h, fb
}

func prepareOK(t *testing.T, h *harness) *Plan {
	t.Helper()
	pl, err := Prepare(context.Background(), h.deps, Args{PRURL: testPRURL})
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

// offBaseline prepares the same review with repository context off.
func offBaseline(t *testing.T, h *harness) *Plan {
	t.Helper()
	b := newHarness(goodAnswer)
	b.prov.files = h.prov.files
	b.deps.Config.LLM.ContextWindow = h.deps.Config.LLM.ContextWindow
	b.deps.Config.Diff.MaxTokens = h.deps.Config.Diff.MaxTokens
	b.prov.comments = h.prov.comments
	return prepareOK(t, b)
}

// TestRepoContextBlockInPrompt: the block follows the discussion block and
// precedes the diff, with the fixed header, the entries and their snippets;
// the coverage counts it; the head is fetched once; the pull request's own
// files are excluded from the search.
func TestRepoContextBlockInPrompt(t *testing.T) {
	h, fb := repoHarness(defFiles(), map[string][]gitctx.Hit{
		"Alpha": {useHit("Alpha", "pkg/use.go", 12), useHit("Alpha", "cmd/main.go", 7)},
		"Beta":  {useHit("Beta", "pkg/use.go", 30)},
	})
	h.prov.addComment(provider.User{ID: "9", Name: "alice"}, "Please check the retry limit.")
	pl := prepareOK(t, h)
	user := pl.Prompts.User

	for _, want := range []string{repoctx.Header, "[pkg/use.go:12] uses Alpha", "[cmd/main.go:7] uses Alpha",
		"[pkg/use.go:30] uses Beta", snippetSentinel} {
		if !strings.Contains(user, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	idx := func(s string) int { return strings.Index(user, s) }
	if idx(descMarker) >= idx(DiscussionHeader) || idx(DiscussionHeader) >= idx(repoctx.Header) || idx(repoctx.Header) >= idx("The PR code diff:") {
		t.Errorf("order: description %d, discussion %d, repository context %d, diff %d",
			idx(descMarker), idx(DiscussionHeader), idx(repoctx.Header), idx("The PR code diff:"))
	}
	if got, want := pl.Result.Coverage.RepoContext, (llmrun.RepoContext{Status: "used", Symbols: 2, References: 3, Files: 2}); got != want {
		t.Errorf("coverage.repo_context = %+v, want %+v", got, want)
	}
	if fb.ensures != 1 || len(fb.queries) != 1 {
		t.Errorf("ensure %d, grep %d; want 1 and 1", fb.ensures, len(fb.queries))
	}
	if ex := fb.queries[0].Exclude; len(ex) != 2 || ex[0] != "pkg/a.go" || ex[1] != "pkg/b.go" {
		t.Errorf("the pull request's own files are not excluded: %v", ex)
	}
	if len(pl.Result.Notes) != 0 {
		t.Errorf("notes = %v", pl.Result.Notes)
	}

	// The block is part of the prompt tokens, and the diff is the one of a
	// review without it.
	off := offBaseline(t, h)
	m, om := pl.Result.Metadata, off.Result.Metadata
	if m.PromptTokens <= om.PromptTokens || m.DiffTokens != om.DiffTokens || m.RequestTokens <= om.RequestTokens {
		t.Errorf("tokens with the block %+v, without %+v", m, om)
	}
	block := tokens.Estimate(repoctx.Render(append(fb.hits["Alpha"], fb.hits["Beta"]...), 1<<20, 0.3).Text, 0.3)
	if d := m.PromptTokens - om.PromptTokens; d < block-40 || d > block+40 {
		t.Errorf("prompt tokens grew by %d, the block is about %d", d, block)
	}
	if pl.Budget != off.Budget {
		t.Errorf("the diff budget changed: %+v vs %+v", pl.Budget, off.Budget)
	}
	if h.logs.Len() > 0 {
		h.checkNoLeaks(t)
	}
}

// TestRepoContextOffIsUntouched: disabled, no git work at all and the
// prompt is byte for byte the one without the feature; enabled with no
// symbol in the diff, the same, without a fetch.
func TestRepoContextOffIsUntouched(t *testing.T) {
	h, fb := repoHarness(defFiles(), map[string][]gitctx.Hit{"Alpha": {useHit("Alpha", "pkg/use.go", 12)}})
	h.deps.Config.Context.Repo.Enabled = false
	pl := prepareOK(t, h)
	if fb.ensures != 0 || len(fb.queries) != 0 {
		t.Errorf("git work while disabled: ensure %d, grep %d", fb.ensures, len(fb.queries))
	}
	if got := pl.Result.Coverage.RepoContext; got != (llmrun.RepoContext{Status: "off"}) {
		t.Errorf("coverage.repo_context = %+v", got)
	}
	if strings.Contains(pl.Prompts.User, repoctx.Header) {
		t.Error("the block is in the prompt while disabled")
	}
	raw, _ := json.Marshal(pl.Result.Coverage)
	if !strings.Contains(string(raw), `"repo_context":{"status":"off","reason":"","symbols":0,"references":0,"files":0}`) {
		t.Errorf("coverage JSON = %s", raw)
	}

	// Enabled, but the diff defines no symbol: nothing to search.
	h2, fb2 := repoHarness(sampleFiles(), nil)
	pl2 := prepareOK(t, h2)
	if fb2.ensures != 0 || len(fb2.queries) != 0 {
		t.Errorf("git work without a symbol: ensure %d, grep %d", fb2.ensures, len(fb2.queries))
	}
	if got := pl2.Result.Coverage.RepoContext; got != (llmrun.RepoContext{Status: "used"}) {
		t.Errorf("coverage.repo_context = %+v", got)
	}
	off := offBaseline(t, h2)
	if pl2.Prompts != off.Prompts || pl2.Result.Metadata != off.Result.Metadata {
		t.Error("a review with repository context but no symbol differs from one without it")
	}
}

// TestRepoContextFailureIsANote: every gitctx failure is a fixed note and a
// skipped coverage, never a failure of the review, and the prompt is the
// one without the block.
func TestRepoContextFailureIsANote(t *testing.T) {
	for _, reason := range []string{gitctx.ReasonAuth, gitctx.ReasonGitUnavailable, gitctx.ReasonTimeout, gitctx.ReasonTooLarge} {
		t.Run(reason, func(t *testing.T) {
			h, fb := repoHarness(defFiles(), nil)
			fb.ensureErr = &gitctx.Error{Reason: reason}
			res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL})
			if err != nil {
				t.Fatalf("the review failed: %v", err)
			}
			if got, want := res.Coverage.RepoContext, (llmrun.RepoContext{Status: "skipped", Reason: reason}); got != want {
				t.Errorf("coverage.repo_context = %+v, want %+v", got, want)
			}
			if !contains(res.Notes, gitctx.Note(reason)) {
				t.Errorf("notes = %v, want %q", res.Notes, gitctx.Note(reason))
			}
			off := offBaseline(t, h)
			if h.llm.calls[0].user != off.Prompts.User {
				t.Error("the prompt differs from the one without repository context")
			}
			if res.Review == nil {
				t.Error("no review")
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func manyHits(n int) map[string][]gitctx.Hit {
	var hits []gitctx.Hit
	for i := range n {
		hits = append(hits, useHit("Alpha", fmt.Sprintf("use/u%02d.go", i), 10+i))
	}
	return map[string][]gitctx.Hit{"Alpha": hits}
}

// TestRepoContextClippedByWholeEntries: context.repo.max_tokens clips the
// block by whole entries, with a note that counts the rest.
func TestRepoContextClippedByWholeEntries(t *testing.T) {
	h, _ := repoHarness(defFiles()[:1], manyHits(30))
	h.deps.Config.Context.Repo.MaxTokens = 300
	pl := prepareOK(t, h)
	rc := pl.Result.Coverage.RepoContext
	if rc.Status != "used" || rc.References < 1 || rc.References >= 30 || rc.Files != rc.References {
		t.Fatalf("coverage.repo_context = %+v", rc)
	}
	if !contains(pl.Result.Notes, repoctx.NoteClipped(30-rc.References)) {
		t.Errorf("notes = %v", pl.Result.Notes)
	}
	user := pl.Prompts.User
	start := strings.Index(user, repoctx.Header)
	end := strings.Index(user, "The PR code diff:")
	if n := tokens.Estimate(user[start:end], 0.3); n > 300 {
		t.Errorf("the block is %d tokens, over context.repo.max_tokens 300", n)
	}
	if got := strings.Count(user, "] uses Alpha"); got != rc.References {
		t.Errorf("%d entries in the prompt, coverage says %d", got, rc.References)
	}
	// The first entries are kept, whole, in rank order.
	if !strings.Contains(user, "[use/u00.go:10] uses Alpha\nbefore\nAlpha() // "+snippetSentinel+"\nafter") {
		t.Error("the first entry is not whole")
	}
}

// TestRepoContextYieldsToTheDiff: the diff always wins. With a context
// window that leaves no room beside the diff, the block is dropped with a
// note and the review is exactly the one without repository context; with
// room for some entries only, it is clipped to them; the request never
// exceeds the soft limit.
func TestRepoContextYieldsToTheDiff(t *testing.T) {
	h, _ := repoHarness(defFiles()[:1], manyHits(30))
	h.deps.Config.Context.Repo.MaxTokens = 4000
	off := offBaseline(t, h)
	soft := tokens.Budget{}.SoftReserve() // 1500: no max output tokens set
	oneEntry := tokens.Estimate(repoctx.Render(manyHits(1)["Alpha"], 1<<20, 0.3).Text, 0.3)

	for _, tc := range []struct {
		name      string
		room      int
		wantDrop  bool
		wantEntry bool
	}{
		{"no room", 30, true, false},
		{"room for one entry", oneEntry + 20, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := repoHarness(defFiles()[:1], manyHits(30))
			h.deps.Config.Context.Repo.MaxTokens = 4000
			window := off.Result.Metadata.RequestTokens + soft + tc.room
			h.deps.Config.LLM.ContextWindow = window
			base := offBaseline(t, h)
			pl := prepareOK(t, h)
			rc := pl.Result.Coverage.RepoContext
			if base.Result.Metadata.DiffTokens != off.Result.Metadata.DiffTokens || base.Result.Metadata.DiffTrimmed {
				t.Fatalf("test setup: the baseline diff changed with the window: %+v", base.Result.Metadata)
			}
			if pl.Result.Metadata.DiffTokens != base.Result.Metadata.DiffTokens || pl.Result.Metadata.DiffTrimmed ||
				pl.Result.Coverage.ReviewedFiles != base.Result.Coverage.ReviewedFiles {
				t.Errorf("the diff changed: %+v vs %+v", pl.Result.Metadata, base.Result.Metadata)
			}
			if got := pl.Result.Metadata.RequestTokens; got > window-soft {
				t.Errorf("request %d tokens exceeds the soft limit %d", got, window-soft)
			}
			if tc.wantDrop {
				if rc.Status != "skipped" || rc.Reason != repoctx.ReasonBudget || !contains(pl.Result.Notes, repoctx.NoteBudget) {
					t.Errorf("coverage.repo_context = %+v, notes %v", rc, pl.Result.Notes)
				}
				if pl.Prompts != base.Prompts {
					t.Error("the dropped block left a trace in the prompt")
				}
			}
			if tc.wantEntry && (rc.Status != "used" || rc.References < 1 || rc.References >= 30) {
				t.Errorf("coverage.repo_context = %+v, want a clipped block", rc)
			}
		})
	}
}

// bigDefFiles are n files of about 700 tokens, the first line of each
// defining Sym<NN> (see bigFiles).
func bigDefFiles(n int) []provider.FilePatch {
	files := bigFiles(n)
	for i := range files {
		files[i].Patch = strings.Replace(files[i].Patch, " a\n", fmt.Sprintf(" a\n+func Sym%02d() {}\n", i), 1)
	}
	return files
}

func symHits(n int) map[string][]gitctx.Hit {
	out := map[string][]gitctx.Hit{}
	for i := range n {
		s := fmt.Sprintf("Sym%02d", i)
		out[s] = []gitctx.Hit{useHit(s, fmt.Sprintf("use/%s.go", strings.ToLower(s)), 5)}
	}
	return out
}

// TestRepoContextPerPart: in a review in three parts each part searches and
// is given the symbols of its own files only, the head is fetched once, and
// the packing budget reserves context.repo.max_tokens for every part's block.
func TestRepoContextPerPart(t *testing.T) {
	h, fb := repoHarness(bigDefFiles(6), symHits(6))
	capTokens := 2000
	h.deps.Config.Diff.MaxTokens = &capTokens
	f := &partLLM{answers: map[int]string{}, errs: map[int]error{}}
	h.deps.LLM = f
	res := runChunked(t, h, Args{})

	if len(f.calls) != 3 || len(fb.queries) != 3 || fb.ensures != 1 {
		t.Fatalf("calls %d, greps %d, ensures %d; want 3, 3 and 1", len(f.calls), len(fb.queries), fb.ensures)
	}
	for i := range 3 {
		own := map[string]bool{fmt.Sprintf("Sym%02d", 2*i): true, fmt.Sprintf("Sym%02d", 2*i+1): true}
		if got := fb.symbolsOf(i); len(got) != 2 || !own[got[0]] || !own[got[1]] {
			t.Errorf("part %d searched %v, want %v", i+1, got, own)
		}
		user := f.calls[i].user
		for s := range 6 {
			name := fmt.Sprintf("Sym%02d", s)
			if got := strings.Contains(user, "] uses "+name+"\n"); got != own[name] {
				t.Errorf("part %d: %s in the prompt = %v", i+1, name, got)
			}
		}
		if !strings.Contains(user, repoctx.Header+"\n") {
			t.Errorf("part %d has no block", i+1)
		}
		hdr, block := strings.Index(user, PartHeader(i+1, 3)), strings.Index(user, repoctx.Header)
		if block < 0 || hdr < block {
			t.Errorf("part %d: the block must precede the part line (block %d, part line %d)", i+1, block, hdr)
		}
	}
	if got, want := res.Coverage.RepoContext, (llmrun.RepoContext{Status: "used", Symbols: 6, References: 6, Files: 6}); got != want {
		t.Errorf("coverage.repo_context = %+v, want %+v", got, want)
	}

	// The reservation: each part's packing budget holds max_tokens more.
	off, _ := chunkHarness(t, nil)
	off.prov.files = bigDefFiles(6)
	offPlan := prepareOK(t, off)
	if len(offPlan.parts) != 3 {
		t.Fatalf("test setup: %d parts without repository context", len(offPlan.parts))
	}
	h2, _ := repoHarness(bigDefFiles(6), symHits(6))
	h2.deps.Config.Diff.MaxTokens = &capTokens
	pl := prepareOK(t, h2)
	if d := pl.Budget.PromptTokens - offPlan.Budget.PromptTokens; d != 2000 {
		t.Errorf("the packing budget grew by %d, want context.repo.max_tokens 2000", d)
	}
	if pl.Result.Metadata.PromptTokens-offPlan.Result.Metadata.PromptTokens != 2000 {
		t.Errorf("prompt tokens do not count the reserved block")
	}
}

// TestRepoContextPartsFitTheWindow: when the context window, not
// diff.max_tokens, limits the parts, the reservation leaves every part room
// for its block, and no part's request (with the block) passes the guard's
// limit or has its diff trimmed.
func TestRepoContextPartsFitTheWindow(t *testing.T) {
	h, _ := repoHarness(bigDefFiles(6), symHits(6))
	h.deps.Config.LLM.ContextWindow = 6500
	pl := prepareOK(t, h)
	if len(pl.parts) < 2 {
		t.Fatalf("%d parts; want a review in parts", len(pl.parts))
	}
	limit := h.deps.Config.LLM.ContextWindow - pl.Budget.HardReserve()
	blocks := 0
	for i, pt := range pl.parts {
		if pt.fit.requestTokens > limit || pt.fit.keptLines >= 0 {
			t.Errorf("part %d: request %d tokens against the limit %d (trimmed at %d)", i+1, pt.fit.requestTokens, limit, pt.fit.keptLines)
		}
		if strings.Contains(pt.fit.prompts.User, repoctx.Header) {
			blocks++
		}
	}
	if blocks != len(pl.parts) {
		t.Errorf("%d of %d parts have a block", blocks, len(pl.parts))
	}
}

// TestRepoContextNeverReachesTheLogs [canary]: a snippet, a symbol or a path
// of the repository never reaches the debug log (X-8), in a review in one
// call or in parts; the log holds counts.
func TestRepoContextNeverReachesTheLogs(t *testing.T) {
	check := func(t *testing.T, h *harness) {
		t.Helper()
		for _, leak := range []string{snippetSentinel, "Alpha", "Beta", "use/", "Sym0", "pkg/use.go"} {
			if strings.Contains(h.logs.String(), leak) {
				t.Errorf("%q reached the debug log:\n%s", leak, h.logs.String())
			}
		}
		if !strings.Contains(h.logs.String(), "repository context") {
			t.Errorf("no repository-context line in the log: %s", h.logs.String())
		}
	}
	t.Run("one call", func(t *testing.T) {
		h, _ := repoHarness(defFiles(), map[string][]gitctx.Hit{
			"Alpha": {useHit("Alpha", "pkg/use.go", 12)}, "Beta": {useHit("Beta", "pkg/use.go", 30)}})
		if _, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(h.llm.calls[0].user, snippetSentinel) {
			t.Fatal("test setup: the snippet is not in the prompt")
		}
		check(t, h)
	})
	t.Run("parts", func(t *testing.T) {
		h, _ := repoHarness(bigDefFiles(6), symHits(6))
		capTokens := 2000
		h.deps.Config.Diff.MaxTokens = &capTokens
		runChunked(t, h, Args{})
		check(t, h)
	})
}

// TestRepoContextPartsSkipped: when the head cannot be fetched, a review in
// parts still runs, with one note and a skipped coverage, and every part's
// prompt is the one without the block; the failed fetch is made once.
func TestRepoContextPartsSkipped(t *testing.T) {
	h, fb := repoHarness(bigDefFiles(6), symHits(6))
	fb.ensureErr = &gitctx.Error{Reason: gitctx.ReasonAuth}
	capTokens := 2000
	h.deps.Config.Diff.MaxTokens = &capTokens
	f := &partLLM{answers: map[int]string{}, errs: map[int]error{}}
	h.deps.LLM = f
	res := runChunked(t, h, Args{})
	if len(f.calls) != 3 || fb.ensures != 1 || len(fb.queries) != 0 {
		t.Errorf("calls %d, ensures %d, greps %d", len(f.calls), fb.ensures, len(fb.queries))
	}
	if got, want := res.Coverage.RepoContext, (llmrun.RepoContext{Status: "skipped", Reason: "auth"}); got != want {
		t.Errorf("coverage.repo_context = %+v, want %+v", got, want)
	}
	n := 0
	for _, note := range res.Notes {
		if note == gitctx.Note("auth") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d notes about the skipped context, want 1: %v", n, res.Notes)
	}
	for i, c := range f.calls {
		if strings.Contains(c.user, repoctx.Header) {
			t.Errorf("part %d has a block", i+1)
		}
	}
}
