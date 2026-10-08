package ask

import (
	"context"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
)

const snippetSentinel = "SNIPPET-SENTINEL-4c9e1b"

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

func defFile(path, def string) provider.FilePatch {
	return provider.FilePatch{Path: path, Type: provider.ChangeModified,
		Patch:      "@@ -1,2 +1,3 @@\n a\n+" + def + "\n b\n",
		BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap}
}

// TestAskRepoContext: pr_ask puts the block between the main-language line
// and the diff, searches the symbols of the files in its prepared diff, and
// reports the structured coverage; disabled, it does no git work and the
// prompt is the one without the feature; a gitctx failure is a note.
func TestAskRepoContext(t *testing.T) {
	hit := gitctx.Hit{Symbol: "Alpha", Path: "pkg/use.go", Line: 12, StartLine: 11, Snippet: "x\nAlpha() // " + snippetSentinel + "\ny"}
	setup := func() (*harness, *fakeRepo) {
		h := newHarness("The answer.")
		h.prov.files = []provider.FilePatch{defFile("pkg/a.go", "func Alpha() {}"), defFile("pkg/b.go", "func Beta() {}")}
		h.deps.Config.Context.Repo.Enabled = true
		fb := &fakeRepo{hits: map[string][]gitctx.Hit{"Alpha": {hit}}}
		h.deps.RepoContext = fb
		return h, fb
	}

	h, fb := setup()
	pl, err := Prepare(context.Background(), h.deps, h.args())
	if err != nil {
		t.Fatal(err)
	}
	user := pl.Prompts.User
	idx := func(s string) int { return strings.Index(user, s) }
	if idx("Main PR language") >= idx(repoctx.Header) || idx(repoctx.Header) >= idx("The PR Git Diff:") {
		t.Errorf("order: language %d, block %d, diff %d", idx("Main PR language"), idx(repoctx.Header), idx("The PR Git Diff:"))
	}
	if !strings.Contains(user, "[pkg/use.go:12] uses Alpha\nx\nAlpha() // "+snippetSentinel+"\ny") {
		t.Errorf("the entry is not in the prompt:\n%s", user)
	}
	if got, want := pl.Result.Coverage.RepoContext, (llmrun.RepoContext{Status: "used", Symbols: 2, References: 1, Files: 1}); got != want {
		t.Errorf("coverage.repo_context = %+v, want %+v", got, want)
	}
	if fb.ensures != 1 || len(fb.queries) != 1 || len(fb.queries[0].Symbols) != 2 {
		t.Errorf("ensure %d, queries %d", fb.ensures, len(fb.queries))
	}
	if pl.RepoReport == nil || pl.RepoReport.Tokens == 0 || len(pl.RepoReport.Symbols) != 2 {
		t.Errorf("report = %+v", pl.RepoReport)
	}

	// Off: no git work, the prompt of a run without the feature.
	h2, fb2 := setup()
	h2.deps.Config.Context.Repo.Enabled = false
	off, err := Prepare(context.Background(), h2.deps, h2.args())
	if err != nil {
		t.Fatal(err)
	}
	if fb2.ensures != 0 || len(fb2.queries) != 0 || off.RepoReport != nil {
		t.Errorf("git work while disabled")
	}
	if got := off.Result.Coverage.RepoContext; got != (llmrun.RepoContext{Status: "off"}) {
		t.Errorf("coverage.repo_context = %+v", got)
	}
	if strings.Contains(off.Prompts.User, repoctx.Header) || off.Prompts.User == user {
		t.Error("the disabled prompt carries the block")
	}

	// A failure is a note; the prompt equals the disabled one.
	h3, fb3 := setup()
	fb3.ensureErr = &gitctx.Error{Reason: gitctx.ReasonNotFound}
	res, err := Run(context.Background(), h3.deps, h3.args())
	if err != nil {
		t.Fatalf("the answer failed: %v", err)
	}
	if got, want := res.Coverage.RepoContext, (llmrun.RepoContext{Status: "skipped", Reason: "not_found"}); got != want {
		t.Errorf("coverage.repo_context = %+v, want %+v", got, want)
	}
	found := false
	for _, n := range res.Notes {
		found = found || n == gitctx.Note(gitctx.ReasonNotFound)
	}
	if !found || h3.llm.calls[0].user != off.Prompts.User || res.Answer == "" {
		t.Errorf("notes %v; prompt equals the disabled one: %v", res.Notes, h3.llm.calls[0].user == off.Prompts.User)
	}

	// Leak canary: the snippet and the names stay out of the debug log.
	for _, leak := range []string{snippetSentinel, "Alpha", "pkg/use.go"} {
		if strings.Contains(h.logs.String(), leak) {
			t.Errorf("%q reached the debug log", leak)
		}
	}
}
